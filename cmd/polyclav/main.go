package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"runtime/debug"
	"sync"
	"syscall"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/bootstrap"
	"github.com/mschulkind-oss/polyclav/internal/config"
	"github.com/mschulkind-oss/polyclav/internal/launchkey"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/driver"
	"github.com/mschulkind-oss/polyclav/internal/midi"
	"github.com/mschulkind-oss/polyclav/internal/osc"
	"github.com/mschulkind-oss/polyclav/internal/patches"
	"github.com/mschulkind-oss/polyclav/internal/state"
	"github.com/mschulkind-oss/polyclav/internal/supervisor"
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
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		printVersion()
		return
	}

	configPath := flag.String("config", "", "path to polyclav.toml (default: $XDG_CONFIG_HOME/polyclav/polyclav.toml)")
	showVersion := flag.Bool("version", false, "print version and exit")
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
	logger.Info("config loaded", "path", path,
		"xr18_host", xr18Host, "xr18_port", cfg.OSC.XR18.Port,
		"soundfont", cfg.Soundfont.Path)

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
	if err := audio.Start(); err != nil {
		logger.Error("audio start", "err", err)
	}

	var compAmount float32 = 0.5
	var limiterCeilingDB float32 = -0.3
	if cfg.Mastering != nil {
		compAmount = cfg.Mastering.CompAmount
		limiterCeilingDB = cfg.Mastering.LimiterCeilingDB
	}
	audio.SetMasteringCompressor(compAmount)
	audio.SetLimiterCeilingDB(limiterCeilingDB)

	registry := patches.New(patches.FromConfig(cfg.Patches))
	if len(registry.All()) > 0 {
		if err := registry.SelectIndex(0); err != nil {
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
				if err := registry.Select(initialState.CurrentPatch); err != nil {
					logger.Warn("patch select from state", "name", initialState.CurrentPatch, "err", err)
				}
			}
		}
		if cur := registry.Current(); cur != nil {
			logger.Info("initial patch selected", "name", cur.Name, "soundfont", cur.Soundfont, "gain_db", cur.GainDB)
			// Apply the restored knob state for this patch (or defaults if absent).
			k := stateStore.PatchKnob(cur.Name)
			audio.SetMasterVolume(k.Volume)
			audio.SetReverb(k.Reverb)
			audio.SetCompressor(k.Compressor)
			stateStore.SetCurrentPatch(cur.Name)
		}
	}

	var sup *supervisor.Supervisor
	var mapper *osc.Mapper

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
		if cur := registry.Current(); cur != nil {
			if err := lk.SetDisplayText(cur.Display, ""); err != nil {
				logger.Warn("launchkey set display text", "err", err)
			}
		}
	}

	onMIDIEvent := func(ev midi.Event) {
		switch ev.Kind {
		case midi.NoteOn:
			audio.PushMIDI(audio.MIDIEvent{Kind: audio.MIDINoteOn, Channel: ev.Channel, Note: ev.Note, Vel: ev.Vel})
		case midi.NoteOff:
			audio.PushMIDI(audio.MIDIEvent{Kind: audio.MIDINoteOff, Channel: ev.Channel, Note: ev.Note})
		case midi.ControlChange:
			audio.PushMIDI(audio.MIDIEvent{Kind: audio.MIDIControlChange, Channel: ev.Channel, CC: ev.CC, Value: ev.Value})
		case midi.PitchBend:
			audio.PushMIDI(audio.MIDIEvent{Kind: audio.MIDIPitchBend, Channel: ev.Channel, Bend: ev.Bend})
		}
		if mapper != nil {
			mapper.Dispatch(ev)
		}
	}

	knobLabels := map[int]string{1: "Volume", 2: "Reverb", 3: "Comp", 4: "Cutoff"}

	// Phase 1 native-synth knob-4 cutoff override. The knob is delta-driven
	// (Launchkey relative mode), so we track a 0..1 position in-process and
	// map it onto a log-tapered Hz range when knob 4 turns. State.toml
	// persistence is Phase 2 work (see docs/ROADMAP.md).
	// Default 0.5 ≈ ~630 Hz on the log curve — open enough that the first
	// note rings, leaving plenty of room to sweep up and down.
	var nativeCutoffPos float32 = 0.5
	audio.SetNativeCutoffHz(cutoffHzFromPos(nativeCutoffPos))

	var (
		knobMu           sync.Mutex
		knobRestoreTimer *time.Timer
	)
	restoreDisplayToPatch := func() {
		if cur := registry.Current(); cur != nil {
			_ = sup.Launchkey().SetDisplayText(cur.Display, "")
		}
	}

	onDAWEvent := func(ev driver.Event) {
		switch e := ev.(type) {
		case driver.KnobEvent:
			label, ok := knobLabels[e.Index]
			if !ok {
				return // unmapped knobs 4..8 still don't update anything
			}
			cur := registry.Current()
			if cur == nil {
				return
			}
			knob := stateStore.PatchKnob(cur.Name)
			const step = 1.0 / 127.0
			delta := float32(e.Delta) * step

			var newVal float32
			var field string
			var displayValue string
			switch e.Index {
			case 1:
				newVal = clamp01(knob.Volume + delta)
				audio.SetMasterVolume(newVal)
				field = "volume"
			case 2:
				newVal = clamp01(knob.Reverb + delta)
				audio.SetReverb(newVal)
				field = "reverb"
			case 3:
				newVal = clamp01(knob.Compressor + delta)
				audio.SetCompressor(newVal)
				field = "compressor"
			case 4:
				// Phase 1 hardcoded knob 4 → native synth cutoff. Only
				// active when the current patch is native; other patch
				// types fall through to the unmapped branch. Phase 2
				// will fold this into the multi-page knob system
				// (see docs/ROADMAP.md).
				if cur.Type != "native" {
					return
				}
				nativeCutoffPos = clamp01(nativeCutoffPos + delta)
				hz := cutoffHzFromPos(nativeCutoffPos)
				audio.SetNativeCutoffHz(hz)
				displayValue = formatCutoffHz(hz)
			default:
				return
			}
			if field != "" {
				stateStore.UpdatePatchKnob(cur.Name, field, newVal)
				displayValue = fmt.Sprintf("%d%%", int(newVal*100+0.5))
			}

			if err := sup.Launchkey().SetDisplayText(label, displayValue); err != nil {
				logger.Warn("launchkey knob display", "err", err)
			}
			knobMu.Lock()
			if knobRestoreTimer != nil {
				knobRestoreTimer.Stop()
			}
			knobRestoreTimer = time.AfterFunc(800*time.Millisecond, restoreDisplayToPatch)
			knobMu.Unlock()
		case driver.PadEvent:
			logger.Info("pad event", "row", e.Row, "col", e.Col, "pressed", e.Pressed, "vel", e.Velocity)
			if e.Row != 0 || !e.Pressed {
				return
			}
			if err := registry.SelectIndex(e.Col); err != nil {
				logger.Warn("patch select", "col", e.Col, "err", err)
				return
			}
			if cur := registry.Current(); cur != nil {
				logger.Info("patch selected", "col", e.Col, "name", cur.Name, "soundfont", cur.Soundfont, "gain_db", cur.GainDB)
				// Restore this patch's saved knob values (Defaults() if never set).
				k := stateStore.PatchKnob(cur.Name)
				audio.SetMasterVolume(k.Volume)
				audio.SetReverb(k.Reverb)
				audio.SetCompressor(k.Compressor)
				stateStore.SetCurrentPatch(cur.Name)
				if err := sup.Launchkey().SetDisplayText(cur.Display, ""); err != nil {
					logger.Warn("launchkey set display text", "err", err)
				}
			}
		}
	}

	supCfg := supervisor.Config{
		Launchkey: launchkey.ReconcilerConfig{
			PortMatch:    cfg.MIDI.PortMatch,
			PollInterval: 1 * time.Second,
			OnMIDIEvent:  onMIDIEvent,
			OnDAWEvent:   onDAWEvent,
			OnReconnect:  pushPadColors,
			OnDisconnect: func() { logger.Info("launchkey gone") },
		},
		XR18: osc.ReconcilerConfig{
			Host:          cfg.OSC.XR18.Host,
			Port:          cfg.OSC.XR18.Port,
			PollInterval:  5 * time.Second,
			Timeout:       3 * time.Second,
			MissThreshold: 3,
		},
	}
	sup = supervisor.New(logger, supCfg)
	mapper = osc.NewMapper(sup.XR18(), logger, cfg.OSC.XR18.Bindings)

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

	audio.Stop()
	logger.Info("shutdown complete")
	os.Exit(0)
}

func clamp01(v float32) float32 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// cutoffHzFromPos maps a 0..1 knob position onto a log-tapered Hz range
// (20 Hz – 20 kHz). 0 -> 20 Hz, 1 -> 20 kHz, 0.5 -> ~632 Hz. Matches the
// taper described in docs/ROADMAP.md.
func cutoffHzFromPos(pos float32) float32 {
	pos = clamp01(pos)
	// 20 Hz * (1000)^pos; 1000 = 20000/20.
	return float32(20.0 * math.Pow(1000.0, float64(pos)))
}

// formatCutoffHz renders a cutoff Hz for the Launchkey screen — kHz with
// one decimal place above 1 kHz, integer Hz below.
func formatCutoffHz(hz float32) string {
	if hz >= 1000.0 {
		return fmt.Sprintf("%.1f kHz", hz/1000.0)
	}
	return fmt.Sprintf("%d Hz", int(hz+0.5))
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
