// Package web is the polyclav daemon's browser front panel
// (docs/WEB_UI.md, phases A+B, plus the audition transport from
// docs/AUDITION.md §Control surfaces): a stdlib net/http server exposing
// the REST + SSE API over the controls layer, and an embedded interim
// dashboard page. The server only serves — all wiring (config, listen
// address, hub bridging for player/device events) happens in main.
package web

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/config"
	"github.com/mschulkind-oss/polyclav/internal/controls"
	"github.com/mschulkind-oss/polyclav/internal/midi"
	"github.com/mschulkind-oss/polyclav/internal/midiprobe"
	"github.com/mschulkind-oss/polyclav/internal/patches"
	"github.com/mschulkind-oss/polyclav/internal/player"
)

// DeviceStates reports the reconciler state of each hardware device.
// *supervisor.Supervisor satisfies this; declared locally so this package
// does not import internal/supervisor.
type DeviceStates interface {
	LaunchkeyState() string
	XR18State() string
}

// MIDIDevices abstracts the multi-keyboard note-input reconciler for the
// devices panel (GET/PUT /api/midi/devices). *midi.Multiplexer satisfies
// this; declared locally so this package only needs internal/midi (a
// leaf package), not internal/supervisor — mirroring DeviceStates above.
type MIDIDevices interface {
	// Match is the configured [midi].port_match restriction (immutable
	// for the process lifetime — there is no live setter for it).
	Match() string
	// Ignore is the currently-active ignore list (original case).
	Ignore() []string
	// SetIgnore replaces the live ignore list immediately.
	SetIgnore(names []string)
}

// Deps carries everything the server needs. Logger, Player, Devices,
// ConfigTOML and ConfigPath are optional (see field comments); Controls,
// Hub and Registry are required.
type Deps struct {
	Logger      *slog.Logger
	Controls    *controls.Controls
	Hub         *controls.Hub
	Registry    controls.Registry
	Player      *player.Player // may be nil → player endpoints return 503
	Devices     DeviceStates   // may be nil → device states report "unknown"
	MIDIDevices MIDIDevices    // may be nil → /api/midi/devices endpoints return 503
	// MIDIPortLister enumerates current MIDI input port names for GET
	// /api/midi/devices. Defaults to midi.PortNames (real rtmidi
	// enumeration); tests inject a fake so they never touch a real ALSA
	// sequencer / CoreMIDI client.
	MIDIPortLister func() ([]string, error)
	Probe          *midiprobe.Session     // may be nil → probe endpoints return 503
	ConfigTOML     func() ([]byte, error) // reads polyclav.toml verbatim; nil → GET /api/config falls back to ConfigPath
	ConfigPath     string                 // path to polyclav.toml; "" → PUT /api/config and velocity save return 404
	// SetGlobalVelocity (may be nil) tells the daemon its global
	// [midi.velocity] spec changed. The velocity save path calls it
	// AFTER a successful config-file write — and ONLY then — so the
	// daemon's in-memory global spec (which patch changes re-resolve
	// curves from) tracks the file instead of staying frozen at its
	// boot-time value. Session-only applies never call it: they are
	// deliberately session-scoped and revert on the next patch change.
	SetGlobalVelocity func(config.VelocityConfig)
	Version           string
}

// Server serves the web UI and its API. Construct with New; it holds no
// listener state of its own — Serve owns the http.Server lifecycle.
type Server struct {
	deps Deps
	mux  *http.ServeMux

	// velMu guards the session-velocity bookkeeping behind GET
	// /api/velocity's "source" field: the Describe() label of the last
	// curve installed by PUT /api/velocity, and whether that PUT also
	// saved it to the config file (see handleVelocityGet).
	velMu           sync.Mutex
	sessionVelLabel string
	sessionVelSaved bool

	// cfgMu serializes the two config-file writers — PUT /api/config and
	// the velocity managed-block save — each of which must hold it across
	// its WHOLE read → merge → validate → rename sequence. Without it a
	// velocity save could read the file, lose the CPU to a config PUT's
	// rename, then rename its own merge of the STALE text over the top,
	// silently dropping the config PUT.
	cfgMu sync.Mutex
}

// New builds a Server over deps and registers all routes. A nil Logger
// falls back to slog.Default(); a nil Hub gets a private one so the SSE
// handler never panics (mirroring controls.New).
func New(deps Deps) *Server {
	if deps.Logger == nil {
		deps.Logger = slog.Default()
	}
	if deps.Hub == nil {
		deps.Hub = controls.NewHub()
	}
	if deps.MIDIPortLister == nil {
		deps.MIDIPortLister = midi.PortNames
	}
	s := &Server{deps: deps, mux: http.NewServeMux()}
	s.routes()
	return s
}

func (s *Server) routes() {
	s.mux.HandleFunc("GET /{$}", s.handleIndex)
	s.routesStatic() // static.go: /app/ (embedded Next.js export) + /legacy (interim page)
	s.mux.HandleFunc("GET /api/status", s.handleStatus)
	s.mux.HandleFunc("GET /api/events", s.handleEvents)
	s.mux.HandleFunc("GET /api/patches", s.handlePatches)
	s.mux.HandleFunc("POST /api/patches/{name}/select", s.handlePatchSelect)
	s.mux.HandleFunc("PATCH /api/params", s.handleParams)
	s.mux.HandleFunc("PATCH /api/synth", s.handleSynth)
	s.mux.HandleFunc("GET /api/chain", s.handleChainGet)
	s.mux.HandleFunc("PATCH /api/chain", s.handleChainPatch)
	s.mux.HandleFunc("PATCH /api/mastering", s.handleMastering)
	s.mux.HandleFunc("GET /api/config", s.handleConfig)
	s.mux.HandleFunc("PUT /api/config", s.handleConfigPut)
	s.mux.HandleFunc("GET /api/velocity", s.handleVelocityGet)
	s.mux.HandleFunc("PUT /api/velocity", s.handleVelocityPut)
	s.mux.HandleFunc("GET /api/midi/devices", s.handleMIDIDevicesGet)
	s.mux.HandleFunc("PUT /api/midi/devices", s.handleMIDIDevicesPut)
	s.mux.HandleFunc("GET /api/clips", s.handleClips)
	s.mux.HandleFunc("POST /api/player", s.handlePlayerPlay)
	s.mux.HandleFunc("POST /api/player/stop", s.handlePlayerStop)
	s.mux.HandleFunc("POST /api/player/tempo", s.handlePlayerTempo)
	s.routesProbe() // probe.go: /api/probe/* (generic MIDI device reverse-engineering tool)
}

// Handler returns the routed handler, for tests and for callers that
// want to mount the server themselves.
func (s *Server) Handler() http.Handler {
	return s.mux
}

// Serve listens on listen and serves until ctx is cancelled, then shuts
// down gracefully (5s drain). Request contexts derive from ctx
// (http.Server.BaseContext), so cancelling ctx also releases long-lived
// SSE streams — without that, Shutdown would wait on them forever.
func (s *Server) Serve(ctx context.Context, listen string) error {
	srv := &http.Server{
		Addr:              listen,
		Handler:           s.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		BaseContext:       func(net.Listener) context.Context { return ctx },
	}
	errc := make(chan error, 1)
	go func() { errc <- srv.ListenAndServe() }()
	s.deps.Logger.Info("web ui listening", "addr", listen)

	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		err := srv.Shutdown(shutdownCtx)
		// Reap ListenAndServe's return; ErrServerClosed is the normal
		// graceful-shutdown result, not an error.
		if e := <-errc; err == nil && e != nil && !errors.Is(e, http.ErrServerClosed) {
			err = e
		}
		return err
	}
}

// ---- JSON wire views -------------------------------------------------
//
// Wire shapes are snake_case and owned by this package: the embedded
// dashboard (static/index.html) and the future Next.js app read exactly
// these keys.

type devicesJSON struct {
	Launchkey string `json:"launchkey"`
	XR18      string `json:"xr18"`
}

type paramsJSON struct {
	Patch            string    `json:"patch"`
	PatchDisplay     string    `json:"patch_display"`
	Volume           float32   `json:"volume"`
	Reverb           float32   `json:"reverb"`
	Compressor       float32   `json:"compressor"`
	DrivePedal       float32   `json:"drive_pedal"`
	CutoffPos        float32   `json:"cutoff_pos"`
	CutoffHz         float32   `json:"cutoff_hz"`
	MasteringComp    float32   `json:"mastering_comp"`
	LimiterCeilingDB float32   `json:"limiter_ceiling_db"`
	VelocityCurve    string    `json:"velocity_curve"`
	Synth            synthJSON `json:"synth"`
}

type filterEnvJSON struct {
	Attack  float32 `json:"attack"`
	Decay   float32 `json:"decay"`
	Sustain float32 `json:"sustain"`
	Release float32 `json:"release"`
	Amount  float32 `json:"amount"`
}

type ampEnvJSON struct {
	Attack  float32 `json:"attack"`
	Decay   float32 `json:"decay"`
	Sustain float32 `json:"sustain"`
	Release float32 `json:"release"`
}

type velRoutingJSON struct {
	ToCutoff float32 `json:"to_cutoff"`
	ToAmp    float32 `json:"to_amp"`
}

type lfoJSON struct {
	Wave         string  `json:"wave"`
	RateHz       float32 `json:"rate_hz"`
	ToPitchCents float32 `json:"to_pitch_cents"`
	ToCutoffOct  float32 `json:"to_cutoff_oct"`
	ToAmp        float32 `json:"to_amp"`
}

type synthOscJSON struct {
	Wave        string  `json:"wave"`
	Octave      int     `json:"octave"`
	DetuneCents float32 `json:"detune_cents"`
	Level       float32 `json:"level"`
}

// synthJSON is the wire view of controls.SynthSnapshot: the PATCH
// /api/synth response body and the "synth" block of params.
type synthJSON struct {
	Resonance  float32         `json:"resonance"`
	Glide      float32         `json:"glide"`
	Noise      float32         `json:"noise"`
	PulseWidth float32         `json:"pulse_width"`
	Drive      float32         `json:"drive"`
	KbdTrack   float32         `json:"kbd_track"`
	BendRange  float32         `json:"bend_range"`
	VoiceMode  string          `json:"voice_mode"`
	Oversample bool            `json:"oversample"`
	FilterEnv  filterEnvJSON   `json:"filter_env"`
	AmpEnv     ampEnvJSON      `json:"amp_env"`
	VelRouting velRoutingJSON  `json:"vel_routing"`
	LFO        lfoJSON         `json:"lfo"`
	Osc        [3]synthOscJSON `json:"osc"`
}

func synthView(sy controls.SynthSnapshot) synthJSON {
	out := synthJSON{
		Resonance: sy.Resonance,
		FilterEnv: filterEnvJSON{
			Attack:  sy.FilterEnv.Attack,
			Decay:   sy.FilterEnv.Decay,
			Sustain: sy.FilterEnv.Sustain,
			Release: sy.FilterEnv.Release,
			Amount:  sy.FilterEnv.Amount,
		},
		AmpEnv: ampEnvJSON{
			Attack:  sy.AmpEnv.Attack,
			Decay:   sy.AmpEnv.Decay,
			Sustain: sy.AmpEnv.Sustain,
			Release: sy.AmpEnv.Release,
		},
		Noise:      sy.Noise,
		Glide:      sy.Glide,
		PulseWidth: sy.PulseWidth,
		Drive:      sy.Drive,
		VelRouting: velRoutingJSON{ToCutoff: sy.VelRouting.ToCutoff, ToAmp: sy.VelRouting.ToAmp},
		KbdTrack:   sy.KbdTrack,
		LFO: lfoJSON{
			Wave:         sy.LFO.Wave,
			RateHz:       sy.LFO.RateHz,
			ToPitchCents: sy.LFO.ToPitchCents,
			ToCutoffOct:  sy.LFO.ToCutoffOct,
			ToAmp:        sy.LFO.ToAmp,
		},
		BendRange:  sy.BendRange,
		VoiceMode:  sy.VoiceMode,
		Oversample: sy.Oversample,
	}
	for i, o := range sy.Oscs {
		out.Osc[i] = synthOscJSON{Wave: o.Wave, Octave: o.Octave, DetuneCents: o.DetuneCents, Level: o.Level}
	}
	return out
}

type patchJSON struct {
	Name     string  `json:"name"`
	Display  string  `json:"display"`
	Type     string  `json:"type"`
	PadColor uint8   `json:"pad_color"`
	GainDB   float32 `json:"gain_db"`
	Index    int     `json:"index"`
}

type playerJSON struct {
	Playing bool    `json:"playing"`
	Clip    string  `json:"clip"`
	Loop    bool    `json:"loop"`
	Tempo   float64 `json:"tempo"`
}

type clipJSON struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	PolyOnly    bool    `json:"poly_only"`
	Beats       float64 `json:"beats"`
	RefBPM      float64 `json:"ref_bpm"`
}

type statusJSON struct {
	Version string      `json:"version"`
	Devices devicesJSON `json:"devices"`
	Params  paramsJSON  `json:"params"`
	Patches []patchJSON `json:"patches"`
	Player  *playerJSON `json:"player"`
}

func paramsView(sn controls.ParamsSnapshot) paramsJSON {
	return paramsJSON{
		Patch:            sn.Patch,
		PatchDisplay:     sn.PatchDisplay,
		Volume:           sn.Volume,
		Reverb:           sn.Reverb,
		Compressor:       sn.Compressor,
		DrivePedal:       sn.DrivePedal,
		CutoffPos:        sn.CutoffPos,
		CutoffHz:         sn.CutoffHz,
		MasteringComp:    sn.MasteringComp,
		LimiterCeilingDB: sn.LimiterCeilingDB,
		VelocityCurve:    sn.VelocityCurve,
		Synth:            synthView(sn.Synth),
	}
}

func patchesView(ps []patches.Patch) []patchJSON {
	out := make([]patchJSON, len(ps))
	for i, p := range ps {
		typ := p.Type
		if typ == "" {
			typ = "soundfont" // "" is the config default; normalize for clients
		}
		out[i] = patchJSON{
			Name:     p.Name,
			Display:  p.Display,
			Type:     typ,
			PadColor: uint8(p.PadColor),
			GainDB:   p.GainDB,
			Index:    i,
		}
	}
	return out
}

func playerView(st player.State) playerJSON {
	return playerJSON{Playing: st.Playing, Clip: st.ClipID, Loop: st.Loop, Tempo: st.Tempo}
}

func (s *Server) devicesView() devicesJSON {
	d := devicesJSON{Launchkey: "unknown", XR18: "unknown"}
	if s.deps.Devices != nil {
		d.Launchkey = s.deps.Devices.LaunchkeyState()
		d.XR18 = s.deps.Devices.XR18State()
	}
	return d
}

// statusView assembles the full /api/status payload; it is also the SSE
// "snapshot" event body.
func (s *Server) statusView() statusJSON {
	st := statusJSON{
		Version: s.deps.Version,
		Devices: s.devicesView(),
		Params:  paramsView(s.deps.Controls.Snapshot()),
		Patches: patchesView(s.deps.Registry.All()),
	}
	if s.deps.Player != nil {
		p := playerView(s.deps.Player.State())
		st.Player = &p
	}
	return st
}

// ---- helpers ----------------------------------------------------------

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	writeJSON(w, code, map[string]string{"error": msg})
}

// decodeJSON decodes the request body into dst with a 1 MiB cap. The
// caller maps errors to 400.
func decodeJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
	return json.NewDecoder(r.Body).Decode(dst)
}

// finite01 reports whether v is a finite number in [0,1].
func finite01(v float64) bool {
	return v >= 0 && v <= 1 // NaN and +Inf fail the comparison
}

func finite(v float64) bool {
	return !math.IsNaN(v) && !math.IsInf(v, 0)
}

// ---- handlers ----------------------------------------------------------

func (s *Server) handleStatus(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.statusView())
}

func (s *Server) handlePatches(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, patchesView(s.deps.Registry.All()))
}

func (s *Server) handlePatchSelect(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	if err := s.deps.Controls.SelectPatch(name); err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, paramsView(s.deps.Controls.Snapshot()))
}

// paramsPatchBody is the PATCH /api/params request: every field optional,
// only the present ones are applied.
type paramsPatchBody struct {
	Volume     *float64 `json:"volume"`
	Reverb     *float64 `json:"reverb"`
	Compressor *float64 `json:"compressor"`
	DrivePedal *float64 `json:"drive_pedal"`
	CutoffPos  *float64 `json:"cutoff_pos"`
}

func (s *Server) handleParams(w http.ResponseWriter, r *http.Request) {
	var body paramsPatchBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	for name, p := range map[string]*float64{
		"volume":      body.Volume,
		"reverb":      body.Reverb,
		"compressor":  body.Compressor,
		"drive_pedal": body.DrivePedal,
		"cutoff_pos":  body.CutoffPos,
	} {
		if p != nil && !finite01(*p) {
			writeErr(w, http.StatusBadRequest, name+" must be in [0,1]")
			return
		}
	}
	if s.deps.Registry.Current() == nil {
		writeErr(w, http.StatusConflict, "no patch selected")
		return
	}

	applied := map[string]any{}
	fieldErrs := map[string]string{}
	apply := func(name string, p *float64, set func(float32) (float32, error)) {
		if p == nil {
			return
		}
		v, err := set(float32(*p))
		if err != nil {
			fieldErrs[name] = err.Error()
			return
		}
		applied[name] = v
	}
	apply("volume", body.Volume, s.deps.Controls.SetVolume)
	apply("reverb", body.Reverb, s.deps.Controls.SetReverb)
	apply("compressor", body.Compressor, s.deps.Controls.SetCompressor)
	apply("drive_pedal", body.DrivePedal, s.deps.Controls.SetDrivePedal)
	if body.CutoffPos != nil {
		hz, err := s.deps.Controls.SetCutoffPos(float32(*body.CutoffPos))
		if err != nil {
			fieldErrs["cutoff_pos"] = err.Error()
		} else {
			applied["cutoff_pos"] = float32(*body.CutoffPos)
			applied["cutoff_hz"] = hz
		}
	}

	resp := map[string]any{"applied": applied}
	if len(fieldErrs) > 0 {
		resp["errors"] = fieldErrs
	}
	writeJSON(w, http.StatusOK, resp)
}

// synthPatchBody is the PATCH /api/synth request: every field optional.
// filter_env and osc entries are themselves partial — absent sub-fields
// merge over the current cached values (read-modify-write against the
// controls snapshot).
type synthPatchBody struct {
	Resonance  *float64              `json:"resonance"`
	Glide      *float64              `json:"glide"`
	Noise      *float64              `json:"noise"`
	PulseWidth *float64              `json:"pulse_width"`
	Drive      *float64              `json:"drive"`
	KbdTrack   *float64              `json:"kbd_track"`
	BendRange  *float64              `json:"bend_range"`
	VoiceMode  *string               `json:"voice_mode"`
	Oversample *bool                 `json:"oversample"`
	FilterEnv  *synthFilterEnvPatch  `json:"filter_env"`
	AmpEnv     *synthAmpEnvPatch     `json:"amp_env"`
	VelRouting *synthVelRoutingPatch `json:"vel_routing"`
	LFO        *synthLFOPatch        `json:"lfo"`
	Osc        []synthOscPatch       `json:"osc"`
}

type synthFilterEnvPatch struct {
	Attack  *float64 `json:"attack"`
	Decay   *float64 `json:"decay"`
	Sustain *float64 `json:"sustain"`
	Release *float64 `json:"release"`
	Amount  *float64 `json:"amount"`
}

type synthAmpEnvPatch struct {
	Attack  *float64 `json:"attack"`
	Decay   *float64 `json:"decay"`
	Sustain *float64 `json:"sustain"`
	Release *float64 `json:"release"`
}

type synthVelRoutingPatch struct {
	ToCutoff *float64 `json:"to_cutoff"`
	ToAmp    *float64 `json:"to_amp"`
}

type synthLFOPatch struct {
	Wave         *string  `json:"wave"`
	RateHz       *float64 `json:"rate_hz"`
	ToPitchCents *float64 `json:"to_pitch_cents"`
	ToCutoffOct  *float64 `json:"to_cutoff_oct"`
	ToAmp        *float64 `json:"to_amp"`
}

type synthOscPatch struct {
	Index       *int     `json:"index"`
	Wave        *string  `json:"wave"`
	Octave      *int     `json:"octave"`
	DetuneCents *float64 `json:"detune_cents"`
	Level       *float64 `json:"level"`
}

// validateSynthBody range-checks every present field; the returned
// message is empty when the body is valid. Env times accept 0..10 s on
// the wire (controls floors them at 0.0001 s), so a slider at zero is
// not a client error.
func validateSynthBody(b *synthPatchBody) string {
	check := func(p *float64, lo, hi float64, name string) string {
		if p != nil && !(*p >= lo && *p <= hi) { // NaN fails the comparison
			return fmt.Sprintf("%s must be in [%g,%g]", name, lo, hi)
		}
		return ""
	}
	if msg := check(b.Resonance, 0, 0.95, "resonance"); msg != "" {
		return msg
	}
	if msg := check(b.Glide, 0, 5, "glide"); msg != "" {
		return msg
	}
	if msg := check(b.Noise, 0, 1, "noise"); msg != "" {
		return msg
	}
	if msg := check(b.PulseWidth, 0.05, 0.95, "pulse_width"); msg != "" {
		return msg
	}
	if msg := check(b.Drive, 0, 1, "drive"); msg != "" {
		return msg
	}
	if msg := check(b.KbdTrack, 0, 1, "kbd_track"); msg != "" {
		return msg
	}
	if msg := check(b.BendRange, 0, 12, "bend_range"); msg != "" {
		return msg
	}
	if b.VoiceMode != nil {
		switch *b.VoiceMode {
		case "mono_legato", "mono_retrig", "poly":
		default:
			return fmt.Sprintf("voice_mode %q invalid (valid: mono_legato, mono_retrig, poly)", *b.VoiceMode)
		}
	}
	if fe := b.FilterEnv; fe != nil {
		for name, p := range map[string]*float64{
			"filter_env.attack": fe.Attack, "filter_env.decay": fe.Decay, "filter_env.release": fe.Release,
		} {
			if msg := check(p, 0, 10, name); msg != "" {
				return msg
			}
		}
		for name, p := range map[string]*float64{
			"filter_env.sustain": fe.Sustain, "filter_env.amount": fe.Amount,
		} {
			if msg := check(p, 0, 1, name); msg != "" {
				return msg
			}
		}
	}
	if ae := b.AmpEnv; ae != nil {
		for name, p := range map[string]*float64{
			"amp_env.attack": ae.Attack, "amp_env.decay": ae.Decay, "amp_env.release": ae.Release,
		} {
			if msg := check(p, 0, 10, name); msg != "" {
				return msg
			}
		}
		if msg := check(ae.Sustain, 0, 1, "amp_env.sustain"); msg != "" {
			return msg
		}
	}
	if vr := b.VelRouting; vr != nil {
		if msg := check(vr.ToCutoff, 0, 1, "vel_routing.to_cutoff"); msg != "" {
			return msg
		}
		if msg := check(vr.ToAmp, 0, 1, "vel_routing.to_amp"); msg != "" {
			return msg
		}
	}
	if l := b.LFO; l != nil {
		if l.Wave != nil {
			switch *l.Wave {
			case "triangle", "saw", "square", "sh":
			default:
				return fmt.Sprintf("lfo wave %q invalid (valid: triangle, saw, square, sh)", *l.Wave)
			}
		}
		if msg := check(l.RateHz, 0.05, 20, "lfo.rate_hz"); msg != "" {
			return msg
		}
		if msg := check(l.ToPitchCents, 0, 100, "lfo.to_pitch_cents"); msg != "" {
			return msg
		}
		if msg := check(l.ToCutoffOct, 0, 2, "lfo.to_cutoff_oct"); msg != "" {
			return msg
		}
		if msg := check(l.ToAmp, 0, 1, "lfo.to_amp"); msg != "" {
			return msg
		}
	}
	for _, o := range b.Osc {
		if o.Index == nil {
			return "osc entries require an index (0..2)"
		}
		if *o.Index < 0 || *o.Index > 2 {
			return fmt.Sprintf("osc index %d out of range 0..2", *o.Index)
		}
		if o.Wave != nil {
			switch *o.Wave {
			case "saw", "square", "pulse":
			default:
				return fmt.Sprintf("osc wave %q invalid (valid: saw, square, pulse)", *o.Wave)
			}
		}
		if o.Octave != nil && (*o.Octave < -2 || *o.Octave > 2) {
			return fmt.Sprintf("osc octave %d out of range [-2,2]", *o.Octave)
		}
		if msg := check(o.DetuneCents, -100, 100, "osc detune_cents"); msg != "" {
			return msg
		}
		if msg := check(o.Level, 0, 1, "osc level"); msg != "" {
			return msg
		}
	}
	return ""
}

// f32p converts an optional wire float to controls' optional float32,
// preserving nil-ness ("leave unchanged").
func f32p(p *float64) *float32 {
	if p == nil {
		return nil
	}
	v := float32(*p)
	return &v
}

// synthPartial translates a validated wire body into the controls-layer
// partial. Osc entries rely on validateSynthBody having required Index.
func synthPartial(b *synthPatchBody) controls.SynthPartial {
	p := controls.SynthPartial{
		Resonance:  f32p(b.Resonance),
		Noise:      f32p(b.Noise),
		Glide:      f32p(b.Glide),
		PulseWidth: f32p(b.PulseWidth),
		Drive:      f32p(b.Drive),
		KbdTrack:   f32p(b.KbdTrack),
		BendRange:  f32p(b.BendRange),
		VoiceMode:  b.VoiceMode,
		Oversample: b.Oversample,
	}
	if fe := b.FilterEnv; fe != nil {
		p.FilterEnv = &controls.FilterEnvPartial{
			Attack:  f32p(fe.Attack),
			Decay:   f32p(fe.Decay),
			Sustain: f32p(fe.Sustain),
			Release: f32p(fe.Release),
			Amount:  f32p(fe.Amount),
		}
	}
	if ae := b.AmpEnv; ae != nil {
		p.AmpEnv = &controls.AmpEnvPartial{
			Attack:  f32p(ae.Attack),
			Decay:   f32p(ae.Decay),
			Sustain: f32p(ae.Sustain),
			Release: f32p(ae.Release),
		}
	}
	if vr := b.VelRouting; vr != nil {
		p.VelRouting = &controls.VelRoutingPartial{
			ToCutoff: f32p(vr.ToCutoff),
			ToAmp:    f32p(vr.ToAmp),
		}
	}
	if l := b.LFO; l != nil {
		p.LFO = &controls.LFOPartial{
			Wave:         l.Wave,
			RateHz:       f32p(l.RateHz),
			ToPitchCents: f32p(l.ToPitchCents),
			ToCutoffOct:  f32p(l.ToCutoffOct),
			ToAmp:        f32p(l.ToAmp),
		}
	}
	for _, o := range b.Osc {
		p.Oscs = append(p.Oscs, controls.OscPartial{
			Index:       *o.Index,
			Wave:        o.Wave,
			Octave:      o.Octave,
			DetuneCents: f32p(o.DetuneCents),
			Level:       f32p(o.Level),
		})
	}
	return p
}

func (s *Server) handleSynth(w http.ResponseWriter, r *http.Request) {
	var body synthPatchBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	if msg := validateSynthBody(&body); msg != "" {
		writeErr(w, http.StatusBadRequest, msg)
		return
	}

	// The merge over the current values happens inside the controls
	// layer, under its writer lock — merging here over a Synth()
	// snapshot would let two concurrent partial PATCHes lose updates.
	syn, err := s.deps.Controls.MergeSynth(synthPartial(&body))
	if err != nil {
		// The gating error maps to 409 (no native patch selected);
		// anything else — unreachable after validation — to 400.
		if errors.Is(err, controls.ErrNoNativePatch) {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, synthView(syn))
}

// handleChainGet is GET /api/chain: the post-synth pedal chain schema
// (stages + params) with the current patch's values/enables and the
// global display order. controls.ChainSnapshot is already JSON-shaped.
func (s *Server) handleChainGet(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, http.StatusOK, s.deps.Controls.ChainSnapshot())
}

// handleChainPatch is PATCH /api/chain: a flat object whose keys are
// chain param ids ("chorus.rate_hz" → number, clamped to the registry
// range), stage-enable toggles ("<stage>.enabled" → bool), and/or the
// global "order" (→ array of stage ids). "order" is global and allowed
// with no patch selected; every other key is patch-scoped and 409s with
// no current patch (mirroring PATCH /api/params). Unknown ids and
// invalid values become per-field errors in the body (200), not a
// request-level failure. Response: {applied, errors?}.
func (s *Server) handleChainPatch(w http.ResponseWriter, r *http.Request) {
	var body map[string]json.RawMessage
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}

	applied := map[string]any{}
	fieldErrs := map[string]string{}

	// "order" is global (allowed with no patch); everything else is
	// patch-scoped. Pull order aside, then run the patch gate on the
	// remaining keys BEFORE applying anything — so a 409 leaves the whole
	// request unapplied (no partial order write masked as a failure).
	orderRaw, hasOrder := body["order"]
	delete(body, "order")
	if len(body) > 0 && s.deps.Registry.Current() == nil {
		writeErr(w, http.StatusConflict, "no patch selected")
		return
	}

	if hasOrder {
		var order []string
		if err := json.Unmarshal(orderRaw, &order); err != nil {
			fieldErrs["order"] = "must be an array of stage ids"
		} else if err := s.deps.Controls.SetPedalOrder(order); err != nil {
			fieldErrs["order"] = err.Error()
		} else {
			applied["order"] = order
		}
	}
	for key, raw := range body {
		if stage, ok := strings.CutSuffix(key, ".enabled"); ok {
			var on bool
			if err := json.Unmarshal(raw, &on); err != nil {
				fieldErrs[key] = "must be a boolean"
				continue
			}
			v, err := s.deps.Controls.SetChainEnable(stage, on)
			if err != nil {
				fieldErrs[key] = err.Error()
				continue
			}
			applied[key] = v
			continue
		}
		var num float64
		if err := json.Unmarshal(raw, &num); err != nil || !finite(num) {
			fieldErrs[key] = "must be a finite number"
			continue
		}
		v, err := s.deps.Controls.SetChainParam(key, float32(num))
		if err != nil {
			fieldErrs[key] = err.Error()
			continue
		}
		applied[key] = v
	}

	resp := map[string]any{"applied": applied}
	if len(fieldErrs) > 0 {
		resp["errors"] = fieldErrs
	}
	writeJSON(w, http.StatusOK, resp)
}

type masteringPatchBody struct {
	CompAmount       *float64 `json:"comp_amount"`
	LimiterCeilingDB *float64 `json:"limiter_ceiling_db"`
}

func (s *Server) handleMastering(w http.ResponseWriter, r *http.Request) {
	var body masteringPatchBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	var compP, ceilP *float32
	if body.CompAmount != nil {
		if !finite(*body.CompAmount) {
			writeErr(w, http.StatusBadRequest, "comp_amount must be finite")
			return
		}
		v := float32(*body.CompAmount)
		compP = &v
	}
	if body.LimiterCeilingDB != nil {
		if !finite(*body.LimiterCeilingDB) {
			writeErr(w, http.StatusBadRequest, "limiter_ceiling_db must be finite")
			return
		}
		v := float32(*body.LimiterCeilingDB)
		ceilP = &v
	}
	comp, ceiling := s.deps.Controls.SetMastering(compP, ceilP)
	writeJSON(w, http.StatusOK, map[string]float32{
		"comp_amount":        comp,
		"limiter_ceiling_db": ceiling,
	})
}

func (s *Server) handleConfig(w http.ResponseWriter, _ *http.Request) {
	read := s.deps.ConfigTOML
	if read == nil && s.deps.ConfigPath != "" {
		read = func() ([]byte, error) { return os.ReadFile(s.deps.ConfigPath) }
	}
	if read == nil {
		writeErr(w, http.StatusNotFound, "config source not available")
		return
	}
	b, err := read()
	if err != nil {
		writeErr(w, http.StatusInternalServerError, "read config: "+err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write(b)
}

func (s *Server) handleClips(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Player == nil {
		writeErr(w, http.StatusServiceUnavailable, "player not available")
		return
	}
	infos := s.deps.Player.Clips()
	out := make([]clipJSON, len(infos))
	for i, c := range infos {
		out[i] = clipJSON{
			ID:          c.ID,
			Name:        c.Name,
			Description: c.Description,
			PolyOnly:    c.PolyOnly,
			Beats:       c.Beats,
			RefBPM:      c.RefBPM,
		}
	}
	writeJSON(w, http.StatusOK, out)
}

type playerPlayBody struct {
	Clip  string  `json:"clip"`
	Loop  bool    `json:"loop"`
	Tempo float64 `json:"tempo"`
}

func (s *Server) handlePlayerPlay(w http.ResponseWriter, r *http.Request) {
	if s.deps.Player == nil {
		writeErr(w, http.StatusServiceUnavailable, "player not available")
		return
	}
	var body playerPlayBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	if err := s.deps.Player.Play(body.Clip, body.Loop, body.Tempo); err != nil {
		writeErr(w, http.StatusNotFound, err.Error()) // only failure mode is an unknown clip
		return
	}
	writeJSON(w, http.StatusOK, playerView(s.deps.Player.State()))
}

func (s *Server) handlePlayerStop(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Player == nil {
		writeErr(w, http.StatusServiceUnavailable, "player not available")
		return
	}
	s.deps.Player.Stop()
	writeJSON(w, http.StatusOK, playerView(s.deps.Player.State()))
}

type playerTempoBody struct {
	Tempo float64 `json:"tempo"`
}

func (s *Server) handlePlayerTempo(w http.ResponseWriter, r *http.Request) {
	if s.deps.Player == nil {
		writeErr(w, http.StatusServiceUnavailable, "player not available")
		return
	}
	var body playerTempoBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	s.deps.Player.SetTempo(body.Tempo)
	writeJSON(w, http.StatusOK, playerView(s.deps.Player.State()))
}
