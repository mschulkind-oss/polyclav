package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/bootstrap"
	"github.com/mschulkind-oss/polyclav/internal/config"
	"github.com/mschulkind-oss/polyclav/internal/controls"
	"github.com/mschulkind-oss/polyclav/internal/controls/pages"
	"github.com/mschulkind-oss/polyclav/internal/launchkey"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/driver"
	"github.com/mschulkind-oss/polyclav/internal/midi"
	"github.com/mschulkind-oss/polyclav/internal/osc"
	"github.com/mschulkind-oss/polyclav/internal/patches"
	"github.com/mschulkind-oss/polyclav/internal/player"
	"github.com/mschulkind-oss/polyclav/internal/state"
	"github.com/mschulkind-oss/polyclav/internal/supervisor"
	"github.com/mschulkind-oss/polyclav/internal/velocity"
	"github.com/mschulkind-oss/polyclav/internal/web"
)

// defaultSoundfontDest is the soundfont root used by bootstrap and by
// the example config's `~/.local/share/polyclav/soundfonts/` paths.
// Stored as a `~`-prefixed string so config.ExpandHome is the single
// source for tilde expansion (avoids divergence between bootstrap's
// idea of "home" and the config loader's).
const defaultSoundfontDest = "~/.local/share/polyclav/soundfonts"

func main() {
	// Subcommand dispatch — `polyclav bootstrap [...]` runs the
	// soundfont downloader and exits. Everything else falls through
	// to the daemon path (preserving --config / --version semantics).
	if len(os.Args) > 1 && os.Args[1] == "bootstrap" {
		os.Exit(runBootstrap(os.Args[2:]))
	}
	if len(os.Args) > 1 && os.Args[1] == "doctor" {
		os.Exit(runDoctor(os.Args[2:]))
	}
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		printVersion()
		return
	}

	configPath := flag.String("config", "", "path to polyclav.toml (default: $XDG_CONFIG_HOME/polyclav/polyclav.toml)")
	showVersion := flag.Bool("version", false, "print version and exit")
	playClip := flag.String("play", "", "audition clip id to play at startup (see docs/AUDITION.md; empty = none)")
	playLoop := flag.Bool("loop", false, "loop the --play clip until shutdown")
	playTempo := flag.Float64("tempo", 1.0, "tempo multiplier for --play (0.25..2.0; 0 = 1.0)")
	webFlag := flag.String("web", "", "enable the web UI, overriding [web] in polyclav.toml: a listen address (e.g. 127.0.0.1:8666 or :8666), or \"on\" for the configured/default address")
	flag.Parse()

	if *showVersion {
		printVersion()
		return
	}

	handler := slog.NewTextHandler(os.Stdout, &slog.HandlerOptions{Level: slog.LevelInfo})
	logger := slog.New(handler)

	path := *configPath
	if path == "" {
		cfgDir, err := os.UserConfigDir()
		if err != nil {
			logger.Error("resolve user config dir", "err", err)
			os.Exit(1)
		}
		path = filepath.Join(cfgDir, "polyclav", "polyclav.toml")
	}

	// First-run config write: if no polyclav.toml exists at the
	// resolved path, drop the embedded example there so the user has
	// a sane starting point (rather than the previous Defaults()
	// fallback which had zero patches and silently produced an unlit
	// surface — see the "functioning config or refuse" story).
	if err := ensureConfigExists(path, logger); err != nil {
		logger.Error("seed default config", "path", path, "err", err)
		os.Exit(1)
	}

	cfg, err := config.Load(path)
	if err != nil {
		logger.Error("load config", "path", path, "err", err)
		os.Exit(1)
	}
	if err := config.Validate(cfg); err != nil {
		var mde *config.MissingDepsError
		if errors.As(err, &mde) {
			printStartupError(os.Stderr, path, mde)
			os.Exit(1)
		}
		logger.Error("validate config", "path", path, "err", err)
		os.Exit(1)
	}
	// Empty xr18_host means OSC mixer control is disabled (the default);
	// surface that explicitly so the startup log is unambiguous.
	xr18Host := cfg.OSC.XR18.Host
	if xr18Host == "" {
		xr18Host = "(disabled)"
	}
	applyWebFlag(cfg, *webFlag)
	// Same treatment for the web UI (off by default) and the global
	// velocity curve. The curve resolved here is the [midi.velocity]
	// default only — per-patch overrides are re-resolved on every patch
	// change below; Load already validated the settings, so an error is
	// unexpected.
	webListen := "(disabled)"
	if cfg.Web.Enabled {
		webListen = cfg.Web.Listen
	}
	velLabel := "linear"
	if curve, verr := resolveVelocity(cfg.MIDI.Velocity, nil); verr != nil {
		logger.Warn("resolve global velocity curve", "err", verr)
	} else {
		velLabel = curve.Describe()
	}
	logger.Info("config loaded", "path", path,
		"xr18_host", xr18Host, "xr18_port", cfg.OSC.XR18.Port,
		"soundfont", cfg.Soundfont.Path,
		"web", webListen, "velocity", velLabel)

	// Graceful sfizz degradation: if libsfizz isn't available, .sfz patches
	// can't play. Warn by name so it's obvious why those pads are silent —
	// SF2/SF3, native, and plugin patches are unaffected. See `polyclav doctor`.
	if sfz := sfzPatchNames(cfg); len(sfz) > 0 && !audio.SfizzAvailable() {
		logger.Warn("libsfizz not found — SFZ patches will be silent (install sfizz to enable)",
			"sfz_patches", strings.Join(sfz, ", "))
	}

	statePath := filepath.Join(filepath.Dir(path), "state.toml")
	initialState, err := state.Load(statePath)
	if err != nil {
		logger.Error("load state", "path", statePath, "err", err)
		os.Exit(1)
	}
	logger.Info("state loaded", "path", statePath, "current_patch", initialState.CurrentPatch, "patches", len(initialState.Patches))
	stateStore := state.NewStore(statePath, 2*time.Second, logger, initialState)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	go func() { _ = stateStore.Run(ctx) }()

	audio.SetSoundfont(cfg.Soundfont.Path)
	audio.SetLatencyFrames(cfg.Audio.LatencyFrames) // 0 = engine default; must precede Start
	if err := audio.Start(); err != nil {
		logger.Error("audio start", "err", err)
	}

	registry := patches.New(patches.FromConfig(cfg.Patches))
	hub := controls.NewHub()
	ctl := controls.New(logger, audioBackend{}, registry, stateStore, hub)

	var compAmount float32 = 0.5
	var limiterCeilingDB float32 = -0.3
	if cfg.Mastering != nil {
		compAmount = cfg.Mastering.CompAmount
		limiterCeilingDB = cfg.Mastering.LimiterCeilingDB
	}
	ctl.InitMastering(compAmount, limiterCeilingDB)

	// pushSynth is the synth fork of the MIDI funnel — keyboard and
	// audition-player events both land here. The velocity curve applies to
	// NoteOn only and ONLY on this fork; the OSC mapper must keep seeing
	// raw velocities (docs/VELOCITY_CURVES.md).
	//
	// Every NoteOn also feeds the web UI's velocity monitor
	// (docs/VELOCITY_CURVES.md "Live tweaking"): a "note" hub change
	// carrying the raw and remapped velocity, throttled by noteGate to
	// ~30 events/s. Extras are dropped, never queued — the monitor is a
	// visualization, and it must never back-pressure the MIDI path.
	noteGate := newRateGate(33 * time.Millisecond)
	pushSynth := func(ev midi.Event) {
		switch ev.Kind {
		case midi.NoteOn:
			// Raw velocity is captured BEFORE the curve so the monitor
			// plots true (in, out) pairs.
			raw := ev.Vel
			applied := ctl.ApplyVelocity(raw)
			audio.PushMIDI(audio.MIDIEvent{Kind: audio.MIDINoteOn, Channel: ev.Channel, Note: ev.Note, Vel: applied})
			if noteGate() {
				hub.Publish(controls.Change{Type: "note", Data: map[string]any{
					"in":   int(raw),
					"out":  int(applied),
					"note": int(ev.Note),
				}})
			}
		case midi.NoteOff:
			audio.PushMIDI(audio.MIDIEvent{Kind: audio.MIDINoteOff, Channel: ev.Channel, Note: ev.Note})
		case midi.ControlChange:
			audio.PushMIDI(audio.MIDIEvent{Kind: audio.MIDIControlChange, Channel: ev.Channel, CC: ev.CC, Value: ev.Value})
		case midi.PitchBend:
			audio.PushMIDI(audio.MIDIEvent{Kind: audio.MIDIPitchBend, Channel: ev.Channel, Bend: ev.Bend})
		}
	}

	// Audition player (docs/AUDITION.md): clip events go down the synth
	// fork only — never mapper.Dispatch, so clip notes can't fire mixer
	// bindings. Transport changes bridge onto the hub for the web UI.
	plr := player.New(logger, pushSynth)
	// User clips (docs/AUDITION.md P3): any .mid/.midi under the clips dir
	// joins the audition registry after the built-in patterns. Loaded here,
	// before the Player is shared with the web server or --play, per
	// LoadUserClips's not-during-playback contract. A missing dir is fine.
	clipsDir := config.ExpandHome("~/.local/share/polyclav/clips")
	if n, cerr := plr.LoadUserClips(clipsDir); cerr != nil {
		logger.Warn("load user clips", "dir", clipsDir, "err", cerr)
	} else if n > 0 {
		logger.Info("user clips loaded", "dir", clipsDir, "count", n)
	}
	plr.OnChange(func(st player.State) {
		hub.Publish(controls.Change{Type: "player", Data: map[string]any{
			"playing": st.Playing,
			"clip":    st.ClipID,
			"loop":    st.Loop,
			"tempo":   st.Tempo,
		}})
	})

	if len(registry.All()) > 0 {
		if err := ctl.SelectPatchIndex(0); err != nil {
			logger.Warn("patch select initial", "err", err)
		}
		// If state.toml recorded a previously active patch and it still exists,
		// switch to it. Falls through silently on no-match — the user's
		// polyclav.toml ordering wins for unknown names.
		if initialState.CurrentPatch != "" {
			found := false
			for _, p := range registry.All() {
				if p.Name == initialState.CurrentPatch {
					found = true
					break
				}
			}
			if found {
				if err := ctl.SelectPatch(initialState.CurrentPatch); err != nil {
					logger.Warn("patch select from state", "name", initialState.CurrentPatch, "err", err)
				}
			}
		}
		// SelectPatch/SelectPatchIndex restore the patch's saved knob
		// values and record it as current in the state store; only the
		// log line remains main's job.
		if cur := registry.Current(); cur != nil {
			logger.Info("initial patch selected", "name", cur.Name, "soundfont", cur.Soundfont, "gain_db", cur.GainDB)
		}
	}

	// The global [midi.velocity] spec lives in a mutable holder rather
	// than being read off the boot-time cfg: the web UI's velocity save
	// (PUT /api/velocity {save:true}) rewrites the config FILE, and the
	// patch follower below re-resolves the curve on every patch change —
	// resolving from boot-time config would silently revert the saved
	// curve for the rest of the session. web.Deps.SetGlobalVelocity
	// updates the holder after each successful file write, keeping file
	// and daemon in agreement. Session-only applies (save=false) leave
	// the holder alone on purpose — they are deliberately session-scoped
	// and a patch change reverts them to config.
	gvel := newGlobalVelocity(cfg.MIDI.Velocity)

	// installVelocity resolves the curve for the given patch (per-patch
	// override or global default) and installs it at the funnel point.
	installVelocity := makeInstallVelocity(logger, ctl, gvel)
	installVelocity(registry.Current())
	logger.Info("velocity curve installed", "curve", ctl.VelocityLabel())

	// --play: start auditioning right after the initial patch is loaded.
	// An unknown clip id refuses to boot (exit 1 with the library listed)
	// — a typo should not produce a silent daemon.
	if *playClip != "" {
		if err := plr.Play(*playClip, *playLoop, *playTempo); err != nil {
			fmt.Fprintf(os.Stderr, "polyclav: %v\n", err)
			fmt.Fprintln(os.Stderr, "Available clips:")
			for _, c := range plr.Clips() {
				fmt.Fprintf(os.Stderr, "  %-14s %s\n", c.ID, c.Name)
			}
			os.Exit(1)
		}
	}

	var sup *supervisor.Supervisor
	var mapper *osc.Mapper
	var pg *pages.Pages

	pushPadColors := func() {
		lk := sup.Launchkey()
		for i, p := range registry.All() {
			if i >= 8 {
				break
			}
			if err := lk.SetPadColor(0, i, p.PadColor); err != nil {
				logger.Warn("launchkey set pad color", "col", i, "err", err)
			}
		}
		// The device forgets pad LEDs across power cycles: repaint the
		// page-indicator row (row 1) alongside the patch row (row 0).
		pg.RefreshPads()
		if cur := registry.Current(); cur != nil {
			if err := lk.SetDisplayText(cur.Display, ""); err != nil {
				logger.Warn("launchkey set display text", "err", err)
			}
		}
	}

	onMIDIEvent := func(ev midi.Event) {
		pushSynth(ev)
		if mapper != nil {
			// The mapper always sees the RAW event — OSC bindings key on
			// unremapped velocity (docs/VELOCITY_CURVES.md).
			mapper.Dispatch(ev)
		}
	}

	var (
		knobMu           sync.Mutex
		knobRestoreTimer *time.Timer
	)
	restoreDisplayToPatch := func() {
		if cur := registry.Current(); cur != nil {
			_ = sup.Launchkey().SetDisplayText(cur.Display, "")
		}
	}
	// armScreenRestore (re)starts the 800 ms revert-to-patch-name timer.
	// Every pages-driven screen write (knob value popup, page-name flash,
	// play/stop flash) shares it — the ROADMAP §2.3 base-layer behavior,
	// unchanged from the pre-pages hardcoded knobs.
	armScreenRestore := func() {
		knobMu.Lock()
		if knobRestoreTimer != nil {
			knobRestoreTimer.Stop()
		}
		knobRestoreTimer = time.AfterFunc(800*time.Millisecond, restoreDisplayToPatch)
		knobMu.Unlock()
	}

	onDAWEvent := func(ev driver.Event) {
		switch e := ev.(type) {
		case driver.KnobEvent:
			// The pages state machine owns all knob→param routing
			// (docs/ROADMAP.md §2.1; page 1 keeps the pre-pages knob 1-4
			// behavior: volume/reverb/comp/cutoff). It mutates through the
			// controls layer only and writes the label+value popup through
			// the screen adapter below, which arms the 800 ms restore.
			pg.HandleKnob(e.Index, e.Delta)
		case driver.TransportEvent:
			// ROADMAP §2.5 transport table — status as shipped:
			//
			//   Play      → audition-player toggle of the last-used clip.
			//               (§2.5 proposed LFO tap-tempo; there is no
			//               tempo-synced LFO, and the player toggle is the
			//               transport-shaped capability we do have.)
			//   Stop      → unmapped. §2.5 wants panic / all-notes-off;
			//               controls exposes no panic surface yet — deferred
			//               with it.
			//   Record    → OBSOLETE, unmapped. §2.5/§3.4's "arm save"
			//               button predates the controls layer: every synth
			//               edit already persists (debounced) to state.toml,
			//               so there is nothing to arm.
			//   Loop      → unmapped (reserved in §2.5 as well).
			//   Rewind/FF → unmapped (reserved for XR18 control per §2.5).
			//   Track ←/→ → unmapped for now. §2.2 proposed page switching
			//               here; pages moved to Scene ↑/↓, keeping Track
			//               free for a future octave shift or patch bank.
			//   Scene ↑   → previous knob page (§2.2 adapted).
			//   Scene ↓   → next knob page.
			//   Shift     → unmapped (fine-adjust modifier stays an open
			//               §2.2 question).
			if !e.Pressed {
				return
			}
			dispatchTransport(pg, e.Button)
		case driver.PadEvent:
			logger.Info("pad event", "row", e.Row, "col", e.Col, "pressed", e.Pressed, "vel", e.Velocity)
			if e.Row != 0 || !e.Pressed {
				return
			}
			// SelectPatchIndex restores the patch's saved knob values,
			// records it as current, and publishes the change.
			if err := ctl.SelectPatchIndex(e.Col); err != nil {
				logger.Warn("patch select", "col", e.Col, "err", err)
				return
			}
			// The hub patch follower (below) owns the Launchkey screen
			// repaint for EVERY patch-select surface — pads included — so
			// the display is updated exactly once per change.
			if cur := registry.Current(); cur != nil {
				logger.Info("patch selected", "col", e.Col, "name", cur.Name, "soundfont", cur.Soundfont, "gain_db", cur.GainDB)
			}
		}
	}

	// Heartbeat pointer semantics (config.XR18Config.Heartbeat): nil (key
	// absent) → the X-Air "/xinfo" default; explicit "" → presence polling
	// disabled, sends become fire-and-forget UDP.
	heartbeat := "/xinfo"
	if cfg.OSC.XR18.Heartbeat != nil {
		heartbeat = *cfg.OSC.XR18.Heartbeat
	}

	// publishDeviceState mirrors a hardware reconciler transition onto the
	// hub so the dashboard's device chips update live instead of freezing
	// at their page-load snapshot. The payload shape ({device, state}) is
	// what static/index.html's "device" SSE listener consumes; the state
	// strings are each reconciler's own State() vocabulary, matching the
	// snapshot's devices object.
	publishDeviceState := func(device, state string) {
		hub.Publish(controls.Change{Type: "device", Data: map[string]any{
			"device": device,
			"state":  state,
		}})
	}

	supCfg := supervisor.Config{
		Launchkey: launchkey.ReconcilerConfig{
			PortMatch:    cfg.MIDI.PortMatch,
			PollInterval: 1 * time.Second,
			OnMIDIEvent:  onMIDIEvent,
			OnDAWEvent:   onDAWEvent,
			// The callbacks run inside the supervisor's reconciler
			// goroutines, which start strictly after `sup` is assigned —
			// reading it here is race-free.
			OnReconnect: func() {
				pushPadColors()
				publishDeviceState("launchkey", sup.Launchkey().State())
			},
			OnDisconnect: func() {
				logger.Info("launchkey gone")
				publishDeviceState("launchkey", sup.Launchkey().State())
			},
		},
		XR18: osc.ReconcilerConfig{
			Host:          cfg.OSC.XR18.Host,
			Port:          cfg.OSC.XR18.Port,
			Heartbeat:     heartbeat,
			PollInterval:  5 * time.Second,
			Timeout:       3 * time.Second,
			MissThreshold: 3,
			OnStateChange: func(state string) { publishDeviceState("xr18", state) },
		},
	}
	sup = supervisor.New(logger, supCfg)
	mapper = osc.NewMapper(sup.XR18(), logger, cfg.OSC.XR18.Bindings)

	// Knob-page state machine (docs/ROADMAP.md §2): page state and all
	// knob→param routing live in internal/controls/pages; main only adapts
	// the launchkey reconciler into its screen/pad seams. pg is assigned
	// before sup.Run starts the reconciler goroutines, so onDAWEvent and
	// pushPadColors reading it is race-free (the same ordering argument as
	// sup itself, per the supCfg comment above).
	pg = pages.New(ctl,
		screenAdapter{write: func(line1, line2 string) error {
			err := sup.Launchkey().SetDisplayText(line1, line2)
			if err != nil {
				logger.Warn("launchkey pages display", "err", err)
			}
			armScreenRestore()
			return err
		}},
		padAdapter{set: func(row, col int, color components.Color) error {
			err := sup.Launchkey().SetPadColor(row, col, color)
			if err != nil {
				logger.Warn("launchkey page indicator pad", "row", row, "col", col, "err", err)
			}
			return err
		}},
	)
	pg.AttachPlayer(playerToggle{plr: plr})
	// Seed the page machine with the already-selected boot patch; the
	// followPages hub follower below keeps it in sync from here on.
	if cur := registry.Current(); cur != nil {
		pg.OnPatchChange(cur.Type)
	}

	// Follow patch changes from ANY surface (pads, web selects, future
	// OSC): re-resolve the velocity curve and repaint the Launchkey
	// display. Level-triggered on purpose — the subscription buffer is
	// drop-oldest, so a burst that drops a "patch" event must not leave a
	// stale curve installed or a stale name on the screen; every received
	// event re-checks registry.Current() against the last patch each
	// follower applied. Between the initial installVelocity above and this
	// point no surface can select a patch (pads and the web UI both need
	// the supervisor), so seeding the followers with the current name
	// misses nothing. SetVelocityRemap publishes type "velocity", which
	// the level check ignores as a no-change — no feedback loop.
	currentPatchName := func() string {
		if cur := registry.Current(); cur != nil {
			return cur.Name
		}
		return ""
	}
	followVelocity := newPatchFollower(currentPatchName(), registry.Current, installVelocity)
	followDisplay := newPatchFollower(currentPatchName(), registry.Current, func(cur *patches.Patch) bool {
		if cur == nil {
			return true
		}
		if err := sup.Launchkey().SetDisplayText(cur.Display, ""); err != nil {
			logger.Warn("launchkey set display text", "err", err)
		}
		return true
	})
	// Page availability follows the patch type: leaving the native engine
	// snaps the knob pages back to MAIN and refreshes the indicator pads.
	followPages := newPatchFollower(currentPatchName(), registry.Current, func(cur *patches.Patch) bool {
		typ := ""
		if cur != nil {
			typ = cur.Type
		}
		pg.OnPatchChange(typ)
		return true
	})
	patchCh, patchCancel := hub.Subscribe(64)
	defer patchCancel()
	go func() {
		for range patchCh {
			followVelocity()
			followDisplay()
			followPages()
		}
	}()

	// Web UI — started only after the supervisor exists, because the SSE
	// snapshot reports device reconciler states through Deps.Devices. A
	// serve failure (port in use, bad listen addr) is logged, not fatal:
	// the hardware surfaces keep working without the browser one.
	if cfg.Web.Enabled {
		srv := web.New(web.Deps{
			Logger:     logger,
			Controls:   ctl,
			Hub:        hub,
			Registry:   registry,
			Player:     plr,
			Devices:    sup,
			ConfigTOML: func() ([]byte, error) { return os.ReadFile(path) },
			ConfigPath: path,
			// Keeps the daemon's global velocity spec in sync with a
			// velocity curve SAVED to the config file, so the patch
			// follower's re-resolve installs the saved curve instead of
			// the boot-time one (see gvel above).
			SetGlobalVelocity: gvel.Set,
			Version:           buildVersion(),
		})
		logger.Info("web ui starting", "url", "http://"+cfg.Web.Listen+"/")
		go func() {
			if err := srv.Serve(ctx, cfg.Web.Listen); err != nil {
				logger.Error("web server", "err", err)
			}
		}()
	}

	errCh := make(chan error, 1)
	go func() { errCh <- sup.Run(ctx) }()

	select {
	case <-ctx.Done():
		logger.Info("shutdown signal received")
	case err := <-errCh:
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Error("supervisor exited", "err", err)
		} else {
			logger.Info("supervisor exited")
		}
	}

	select {
	case <-errCh:
	case <-time.After(2 * time.Second):
		logger.Warn("supervisor did not exit within 2s")
	}

	// Stop the player before the audio engine so its NoteOff hygiene
	// (releasing anything still ringing) lands on a live engine.
	plr.Stop()
	audio.Stop()
	logger.Info("shutdown complete")
	os.Exit(0)
}

// audioBackend adapts the internal/audio package functions to the
// controls.Audio seam (mirrors realAudioBackend in internal/patches).
type audioBackend struct{}

var _ controls.Audio = audioBackend{}

func (audioBackend) SetMasterVolume(v float32)        { audio.SetMasterVolume(v) }
func (audioBackend) SetReverb(v float32)              { audio.SetReverb(v) }
func (audioBackend) SetCompressor(v float32)          { audio.SetCompressor(v) }
func (audioBackend) SetNativeCutoffHz(hz float32)     { audio.SetNativeCutoffHz(hz) }
func (audioBackend) SetMasteringCompressor(v float32) { audio.SetMasteringCompressor(v) }
func (audioBackend) SetLimiterCeilingDB(db float32)   { audio.SetLimiterCeilingDB(db) }
func (audioBackend) SetNativeResonance(v float32)     { audio.SetNativeResonance(v) }
func (audioBackend) SetNativeFilterEnv(a, d, s, r, amount float32) {
	audio.SetNativeFilterEnv(a, d, s, r, amount)
}
func (audioBackend) SetNativeOsc(idx int, wave string, octave int, detuneCents, level float32) error {
	return audio.SetNativeOsc(idx, wave, octave, detuneCents, level)
}
func (audioBackend) SetNativeNoise(level float32) { audio.SetNativeNoise(level) }
func (audioBackend) SetNativeGlide(s float32)     { audio.SetNativeGlide(s) }
func (audioBackend) SetNativeAmpEnv(a, d, s, r float32) {
	audio.SetNativeAmpEnv(a, d, s, r)
}
func (audioBackend) SetNativePulseWidth(w float32) { audio.SetNativePulseWidth(w) }
func (audioBackend) SetNativeDrive(d float32)      { audio.SetNativeDrive(d) }
func (audioBackend) SetNativeVelRouting(toCutoff, toAmp float32) {
	audio.SetNativeVelRouting(toCutoff, toAmp)
}
func (audioBackend) SetNativeKbdTrack(amt float32) { audio.SetNativeKbdTrack(amt) }
func (audioBackend) SetNativeLFO(wave string, rateHz, toPitchCents, toCutoffOct, toAmp float32) error {
	return audio.SetNativeLFO(wave, rateHz, toPitchCents, toCutoffOct, toAmp)
}
func (audioBackend) SetNativeBendRange(st float32) { audio.SetNativeBendRange(st) }
func (audioBackend) SetNativeVoiceMode(mode string) error {
	return audio.SetNativeVoiceMode(mode)
}
func (audioBackend) SetNativeOversample(on bool) { audio.SetNativeOversample(on) }

// screenAdapter and padAdapter bridge the launchkey reconciler (plus
// main's restore-timer / logging concerns) into the pages package's
// driver-agnostic ScreenWriter/PadWriter seams.
type screenAdapter struct {
	write func(line1, line2 string) error
}

func (a screenAdapter) SetDisplayText(line1, line2 string) error { return a.write(line1, line2) }

type padAdapter struct {
	set func(row, col int, color components.Color) error
}

func (a padAdapter) SetPadColor(row, col int, color components.Color) error {
	return a.set(row, col, color)
}

// playerToggle adapts the audition player to pages.PlayerControl: the
// transport Play button restarts the last-used clip when stopped and
// stops it when playing (player.State retains ClipID/Loop/Tempo after a
// stop precisely so it can be resumed). ok=false until something has
// played this session — there is no clip to restart yet.
type playerToggle struct{ plr *player.Player }

func (t playerToggle) Toggle() (playing bool, clip string, ok bool) {
	st := t.plr.State()
	if st.Playing {
		t.plr.Stop()
		return false, st.ClipID, true
	}
	if st.ClipID == "" {
		return false, "", false
	}
	if err := t.plr.Play(st.ClipID, st.Loop, st.Tempo); err != nil {
		return false, st.ClipID, false
	}
	return true, st.ClipID, true
}

// dispatchTransport maps a pressed transport button onto the pages state
// machine — split from onDAWEvent so the §2.5 mapping is unit-testable.
// Unlisted buttons are intentionally unbound; see the status table at
// the TransportEvent case in onDAWEvent.
func dispatchTransport(pg *pages.Pages, b driver.TransportButton) {
	switch b {
	case driver.TransportSceneUp:
		pg.PrevPage()
	case driver.TransportSceneDown:
		pg.NextPage()
	case driver.TransportPlay:
		pg.TogglePlay()
	}
}

// newRateGate returns a non-blocking rate limiter: each call reports
// whether the caller may proceed, allowing at most one pass per minGap.
// Extra calls are dropped, never queued — the gate exists to throttle the
// velocity monitor's hub traffic without ever delaying a NoteOn. Safe for
// concurrent use (pushSynth runs on both the MIDI callback goroutine and
// the audition player's scheduler); the CAS means a race between two
// callers admits exactly one.
func newRateGate(minGap time.Duration) func() bool {
	var last atomic.Int64
	return func() bool {
		now := time.Now().UnixNano()
		prev := last.Load()
		if now-prev < int64(minGap) {
			return false
		}
		return last.CompareAndSwap(prev, now)
	}
}

// newPatchFollower returns a level-triggered change handler for hub
// subscribers: each call compares the current patch's name against the
// last name for which apply succeeded, and re-invokes apply when they
// differ. Level-triggered (current state, not event payloads) because
// hub subscriptions are drop-oldest under bursts: a dropped "patch"
// event must not strand stale per-patch state — the next event of ANY
// type re-syncs. apply returning false means "not applied": the same
// change is retried on the next call. Not goroutine-safe; call from a
// single subscriber goroutine.
func newPatchFollower(last string, current func() *patches.Patch, apply func(*patches.Patch) bool) func() {
	return func() {
		cur := current()
		name := ""
		if cur != nil {
			name = cur.Name
		}
		if name == last {
			return
		}
		if apply(cur) {
			last = name
		}
	}
}

// applyWebFlag overlays the --web CLI flag onto the loaded config. An
// empty value leaves the config untouched. "on" (or "true") enables the
// server on the config's listen address; anything else is taken as the
// listen address itself. The CLI always wins over [web] in polyclav.toml
// so `polyclav --web :8666` works without editing the config.
func applyWebFlag(cfg *config.Config, val string) {
	if val == "" {
		return
	}
	cfg.Web.Enabled = true
	if val != "on" && val != "true" {
		cfg.Web.Listen = val
	}
	if cfg.Web.Listen == "" {
		cfg.Web.Listen = "127.0.0.1:8666"
	}
}

// globalVelocity is a goroutine-safe holder for the daemon's global
// [midi.velocity] spec. It is seeded from the boot-time config and
// REPLACED when the web UI saves a velocity curve into polyclav.toml
// (web.Deps.SetGlobalVelocity), so the file on disk and the spec the
// patch follower re-resolves from can never disagree. atomic.Pointer
// because Set runs on web request goroutines while Get runs on the hub
// follower goroutine.
type globalVelocity struct {
	p atomic.Pointer[config.VelocityConfig]
}

func newGlobalVelocity(seed config.VelocityConfig) *globalVelocity {
	g := &globalVelocity{}
	g.Set(seed)
	return g
}

// Set replaces the global velocity spec (stores a copy).
func (g *globalVelocity) Set(v config.VelocityConfig) { g.p.Store(&v) }

// Get returns the current global velocity spec by value.
func (g *globalVelocity) Get() config.VelocityConfig { return *g.p.Load() }

// makeInstallVelocity returns the follower apply func: it resolves the
// curve for the given patch (per-patch override, or the CURRENT global
// spec from gvel) and installs it at the MIDI funnel. Config was
// validated at Load, so an error is unexpected: warn and keep the
// previous curve rather than silently going linear. Returns whether a
// curve was installed (false = resolve failed, so the patch follower
// retries on the next hub event).
//
// Known limit: per-patch velocity overrides come from the boot-time
// patch registry (config is read once at startup), so a config-file
// edit to a patch's velocity_* fields — by hand or via PUT /api/config
// — still needs a restart. Only the GLOBAL spec is live-updatable, via
// the web UI's velocity save (gvel).
func makeInstallVelocity(logger *slog.Logger, ctl *controls.Controls, gvel *globalVelocity) func(*patches.Patch) bool {
	return func(p *patches.Patch) bool {
		curve, err := resolveVelocity(gvel.Get(), p)
		if err != nil {
			logger.Warn("velocity curve resolve", "err", err)
			return false
		}
		ctl.SetVelocityRemap(curve.Apply, curve.Describe())
		return true
	}
}

// resolveVelocity picks the velocity curve for patch p, most specific
// override first (docs/VELOCITY_CURVES.md):
//
//	per-patch points > per-patch curve/gamma > global points > global curve/gamma
//
// global is the daemon's CURRENT [midi.velocity] spec (the boot config's
// block, or whatever the web UI last saved — see globalVelocity). Within
// one scope points vs curve/gamma is a config.Load error, so the two
// same-scope rungs only both exist for configs built in code — the order
// still matters there so tests and future callers get one deterministic
// answer. For the patch fields and the global block alike, Gamma > 0
// with no curve name is the "custom" shorthand. p == nil (no patch
// selected) resolves the global curve. config.Load normalizes the global
// shorthand too; it is re-applied here so configs built in code (tests,
// Defaults()) behave identically. The global OutMin/OutMax were
// range-checked (0..127) at config.Load, so the uint8 conversions are
// lossless.
func resolveVelocity(global config.VelocityConfig, p *patches.Patch) (velocity.Curve, error) {
	if p != nil && len(p.VelocityPoints) > 0 {
		// Per-patch overrides carry no clamp fields (same as the
		// curve/gamma rung below): the velocity package defaults to 1..127.
		return newPointCurve(p.VelocityPoints, 0, 0)
	}
	if p != nil && (p.VelocityCurve != "" || p.VelocityGamma > 0) {
		name := p.VelocityCurve
		if name == "" {
			name = "custom"
		}
		return velocity.New(name, p.VelocityGamma, 0, 0)
	}
	v := global
	if len(v.Points) > 0 {
		return newPointCurve(v.Points, uint8(v.OutMin), uint8(v.OutMax))
	}
	name := v.Curve
	if name == "" && v.Gamma > 0 {
		name = "custom"
	}
	return velocity.New(name, v.Gamma, uint8(v.OutMin), uint8(v.OutMax))
}

// newPointCurve bridges the config-shaped [][]int point list (TOML has
// no fixed-size arrays) into the velocity package's [][2]uint8. Shape
// and range were already validated at config.Load; they are re-checked
// here — like the rest of resolveVelocity — so configs built in code
// fail loudly instead of silently truncating to uint8.
func newPointCurve(pts [][]int, outMin, outMax uint8) (velocity.Curve, error) {
	pairs := make([][2]uint8, len(pts))
	for i, pt := range pts {
		if len(pt) != 2 {
			return velocity.Curve{}, fmt.Errorf("velocity points[%d]: want an [x, y] pair, got %d values", i, len(pt))
		}
		if pt[0] < 0 || pt[0] > 127 || pt[1] < 0 || pt[1] > 127 {
			return velocity.Curve{}, fmt.Errorf("velocity points[%d]: [%d, %d] out of range 0..127", i, pt[0], pt[1])
		}
		pairs[i] = [2]uint8{uint8(pt[0]), uint8(pt[1])}
	}
	return velocity.NewFromPoints(pairs, outMin, outMax)
}

// buildVersion returns the main module's version from build info, or
// "devel" for untagged/dev builds — the short form used by the web UI's
// status payload (printVersion keeps the long human-readable dump).
func buildVersion() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		if v := info.Main.Version; v != "" && v != "(devel)" {
			return v
		}
	}
	return "devel"
}

// ensureConfigExists is the first-run config bootstrap: if the user has
// no polyclav.toml at the resolved path, mkdir -p the parent and drop
// the embedded polyclav.example.toml there. Never overwrites an
// existing file — only the absent case is handled. Errors from
// permission / disk-full bubble up; on success we log an INFO line so
// the user sees where the config landed.
func ensureConfigExists(path string, logger *slog.Logger) error {
	if _, err := os.Stat(path); err == nil {
		return nil
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("stat %q: %w", path, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("mkdir %q: %w", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, config.ExampleConfig(), 0o644); err != nil {
		return fmt.Errorf("write %q: %w", path, err)
	}
	logger.Info("wrote default config", "path", path)
	return nil
}

// printStartupError renders a config-validation failure as a
// multi-line, human-formatted message on stderr — not via slog. The
// daemon log format (key=value pairs) is unreadable for a first-run
// user staring at a missing-soundfont message; this is the only path
// where we step outside structured logging.
func printStartupError(w *os.File, configPath string, mde *config.MissingDepsError) {
	fmt.Fprintln(w, "polyclav cannot start: config validation failed")
	fmt.Fprintln(w)
	fmt.Fprintln(w, mde.Error())
	fmt.Fprintln(w)
	fmt.Fprintln(w, "To fix, choose one:")
	fmt.Fprintln(w, "  - Hear sound now (no download):  keep only a native patch (type=\"native\") — needs no files")
	fmt.Fprintln(w, "  - Download example soundfonts:   polyclav bootstrap")
	fmt.Fprintf(w, "  - Trim broken patches:           edit %s\n", configPath)
	fmt.Fprintln(w)
	fmt.Fprintln(w, "Documentation: docs/INSTALL.md")
}

// runBootstrap dispatches to the bootstrap package after parsing its
// own flag set (a sub-FlagSet rooted at os.Args[2:] so the main daemon
// flag parser stays clean). Returns the exit code main() should use.
func runBootstrap(args []string) int {
	fs := flag.NewFlagSet("bootstrap", flag.ExitOnError)
	dest := fs.String("dest", "", "destination directory for soundfonts (default: ~/.local/share/polyclav/soundfonts)")
	acceptShort := fs.Bool("y", false, "accept all licenses without prompting (same as --accept-licenses)")
	accept := fs.Bool("accept-licenses", false, "accept all licenses without prompting")
	skipExisting := fs.Bool("skip-existing", true, "skip files already present at the target path")
	fs.Usage = func() {
		fmt.Fprintln(fs.Output(), "Usage: polyclav bootstrap [flags]")
		fmt.Fprintln(fs.Output())
		fmt.Fprintln(fs.Output(), "Download the example soundfonts referenced by polyclav.example.toml.")
		fmt.Fprintln(fs.Output())
		fmt.Fprintln(fs.Output(), "Flags:")
		fs.PrintDefaults()
	}
	if err := fs.Parse(args); err != nil {
		return 2
	}

	d := *dest
	if d == "" {
		d = config.ExpandHome(defaultSoundfontDest)
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := bootstrap.Run(ctx, bootstrap.Options{
		Dest:           d,
		AcceptLicenses: *accept || *acceptShort,
		SkipExisting:   *skipExisting,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "bootstrap failed: %v\n", err)
		return 1
	}
	return 0
}

func printVersion() {
	info, ok := debug.ReadBuildInfo()
	if !ok {
		fmt.Println("polyclav (no build info available — not built as a Go module?)")
		return
	}

	var (
		revision, commitTime              string
		dirty                             bool
		compiler, tags, ldflags, cgoFlags string
		cgoEnabled, goos, goarch, goamd64 string
	)
	for _, s := range info.Settings {
		switch s.Key {
		case "vcs.revision":
			revision = s.Value
		case "vcs.time":
			commitTime = s.Value
		case "vcs.modified":
			dirty = s.Value == "true"
		case "-compiler":
			compiler = s.Value
		case "-tags":
			tags = s.Value
		case "-ldflags":
			ldflags = s.Value
		case "CGO_ENABLED":
			cgoEnabled = s.Value
		case "CGO_LDFLAGS":
			cgoFlags = s.Value
		case "GOOS":
			goos = s.Value
		case "GOARCH":
			goarch = s.Value
		case "GOAMD64":
			goamd64 = s.Value
		}
	}

	version := info.Main.Version
	if version == "" || version == "(devel)" {
		version = "(devel)"
	}
	fmt.Printf("polyclav %s\n", version)
	fmt.Printf("  module:      %s\n", info.Main.Path)
	if revision != "" {
		dirtyTag := ""
		if dirty {
			dirtyTag = " (dirty)"
		}
		fmt.Printf("  commit:      %s%s\n", revision, dirtyTag)
	}
	if commitTime != "" {
		fmt.Printf("  committed:   %s\n", commitTime)
	}
	fmt.Printf("  go:          %s\n", info.GoVersion)
	if goos != "" && goarch != "" {
		archStr := goos + "/" + goarch
		if goamd64 != "" {
			archStr += " (" + goamd64 + ")"
		}
		fmt.Printf("  os/arch:     %s\n", archStr)
	}
	if compiler != "" {
		fmt.Printf("  compiler:    %s\n", compiler)
	}
	if cgoEnabled != "" {
		fmt.Printf("  cgo:         %s\n", cgoEnabled)
	}
	if cgoFlags != "" {
		fmt.Printf("  cgo ldflags: %s\n", cgoFlags)
	}
	if tags != "" {
		fmt.Printf("  build tags:  %s\n", tags)
	}
	if ldflags != "" {
		fmt.Printf("  ldflags:     %s\n", ldflags)
	}

	if len(info.Deps) > 0 {
		fmt.Printf("\nDependencies (%d):\n", len(info.Deps))
		for _, dep := range info.Deps {
			// Replaced modules show "actual" via dep.Replace.
			line := fmt.Sprintf("  %-50s %s", dep.Path, dep.Version)
			if dep.Replace != nil {
				line += fmt.Sprintf(" => %s %s", dep.Replace.Path, dep.Replace.Version)
			}
			fmt.Println(line)
		}
	}
}
