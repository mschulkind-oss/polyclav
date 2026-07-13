package controls

import (
	"errors"
	"fmt"
	"log/slog"
	"math"
	"sync"
	"sync/atomic"

	"github.com/mschulkind-oss/polyclav/internal/patches"
	"github.com/mschulkind-oss/polyclav/internal/state"
)

// Audio is the slice of internal/audio the controls layer drives. An
// interface (rather than calling the audio package directly) so tests
// can observe every apply without a running engine, mirroring the
// audioBackend seam in internal/patches.
type Audio interface {
	SetMasterVolume(float32)
	SetReverb(float32)
	SetCompressor(float32)
	SetDrivePedal(float32)
	SetNativeCutoffHz(float32)
	SetMasteringCompressor(float32)
	SetLimiterCeilingDB(float32)
	SetNativeResonance(v float32)
	SetNativeFilterEnv(a, d, s, r, amount float32)
	SetNativeOsc(idx int, wave string, octave int, detuneCents, level float32) error
	SetNativeNoise(level float32)
	SetNativeGlide(s float32)
	SetNativeAmpEnv(a, d, s, r float32)
	SetNativePulseWidth(w float32)
	SetNativeDrive(d float32)
	SetNativeVelRouting(toCutoff, toAmp float32)
	SetNativeKbdTrack(amt float32)
	SetNativeLFO(wave string, rateHz, toPitchCents, toCutoffOct, toAmp float32) error
	SetNativeBendRange(st float32)
	SetNativeVoiceMode(mode string) error
	SetNativeOversample(on bool)
}

// Registry is the slice of *patches.Registry the controls layer needs
// for patch selection and current-patch lookups.
type Registry interface {
	All() []patches.Patch
	Current() *patches.Patch
	Select(name string) error
	SelectIndex(i int) error
}

// StateStore is the slice of *state.Store the controls layer persists
// through, so browser and Launchkey edits are indistinguishable on disk.
type StateStore interface {
	PatchKnob(string) state.Knob
	UpdatePatchKnob(string, string, float32)
	// PatchSynth/UpdatePatchSynth carry the per-patch native-synth block
	// (ROADMAP §3): ok=false from PatchSynth means "never tweaked", which
	// SelectPatch maps to the factory defaults.
	PatchSynth(string) (state.SynthState, bool)
	UpdatePatchSynth(string, state.SynthState)
	SetCurrentPatch(string)
}

// Compile-time guarantees that the production types satisfy the seams —
// a signature drift in patches/state breaks this package's build, not
// main's wiring at runtime.
var (
	_ Registry   = (*patches.Registry)(nil)
	_ StateStore = (*state.Store)(nil)
)

var errNoPatch = errors.New("no patch selected")

// ErrNoNativePatch gates every native-synth setter: they only apply while
// the current patch has Type=="native". Exported so the web layer can map
// it to 409 Conflict instead of a generic 400.
var ErrNoNativePatch = errors.New("no native patch selected")

// defaultCutoffPos is the boot/reset knob position for the native-synth
// cutoff. 0.5 ≈ ~632 Hz on the log taper — open enough that the first
// note rings, leaving room to sweep both ways (same rationale as the
// original main.go default).
const defaultCutoffPos = 0.5

// velocityRemap pairs the remap func with its display label so both swap
// atomically — a reader can never observe a new curve with an old label.
type velocityRemap struct {
	fn    func(uint8) uint8
	label string
}

// FilterEnv is the native synth's filter-envelope (env 2) ADSR plus the
// env→cutoff modulation amount (docs/ROADMAP.md §1.4).
type FilterEnv struct {
	Attack, Decay, Sustain, Release, Amount float32
}

// AmpEnv is the native synth's amp-envelope (env 1) ADSR — FilterEnv's
// shape minus the modulation Amount (the amp env always drives the VCA
// at full depth).
type AmpEnv struct {
	Attack, Decay, Sustain, Release float32
}

// OscParams is one native-synth oscillator's settings (docs/ROADMAP.md §1.4).
type OscParams struct {
	Wave        string
	Octave      int
	DetuneCents float32
	Level       float32
}

// VelRouting is the native synth's velocity-routing amounts: ToCutoff
// modulates the filter cutoff (±1 octave around velocity 64 at 1),
// ToAmp scales the per-note amplitude (1 = classic vel/127, 0 = ignore
// velocity).
type VelRouting struct {
	ToCutoff, ToAmp float32
}

// LFO is the native synth's global LFO block: wave (triangle, saw,
// square, or sh) plus rate and the three modulation depths (vibrato
// cents, cutoff octaves, tremolo amount).
type LFO struct {
	Wave                              string
	RateHz, ToPitchCents, ToCutoffOct float32
	ToAmp                             float32
}

// SynthSnapshot is the cached view of every native-synth parameter this
// layer pushes. Cached here (not read back from the engine) because the
// audio atomics are write-only from this side of the fence — same
// rationale as the mastering cache.
type SynthSnapshot struct {
	Resonance  float32
	FilterEnv  FilterEnv
	AmpEnv     AmpEnv
	Oscs       [3]OscParams
	Noise      float32
	Glide      float32
	PulseWidth float32
	Drive      float32
	VelRouting VelRouting
	KbdTrack   float32
	LFO        LFO
	BendRange  float32
	VoiceMode  string
	Oversample bool
}

// Native-synth clamp ranges, mirroring the Rust-side clamps in
// audio-core (internal/audio doc comments are the contract).
const (
	maxResonance     = 0.95   // headroom below ladder self-oscillation
	minEnvTime       = 0.0001 // seconds
	maxEnvTime       = 10     // seconds
	maxDetune        = 100    // cents
	maxGlide         = 5      // seconds
	minPulseWidth    = 0.05   // duty cycle
	maxPulseWidth    = 0.95   // duty cycle
	minLFORateHz     = 0.05
	maxLFORateHz     = 20
	maxLFOPitchCents = 100
	maxLFOCutoffOct  = 2
	maxBendRange     = 12 // semitones
)

// defaultSynth returns the boot values: the audio-core defaults
// (oscillator.rs default_bank(), filter/env defaults) so the cache and
// the engine agree before any setter runs. Oscs 2/3 are pre-dialed but
// silent (level 0) — turning a level up immediately sounds Moog-ish.
func defaultSynth() SynthSnapshot {
	return SynthSnapshot{
		Resonance: 0.3,
		FilterEnv: FilterEnv{Attack: 0.005, Decay: 0.6, Sustain: 0.4, Release: 0.6, Amount: 0},
		AmpEnv:    AmpEnv{Attack: 0.005, Decay: 0.2, Sustain: 0.7, Release: 0.4},
		Oscs: [3]OscParams{
			{Wave: "saw", Octave: 0, DetuneCents: 0, Level: 1.0},
			{Wave: "saw", Octave: 0, DetuneCents: -7, Level: 0.0},
			{Wave: "saw", Octave: -1, DetuneCents: 5, Level: 0.0},
		},
		Noise:      0,
		Glide:      0,
		PulseWidth: 0.25,
		Drive:      0,
		VelRouting: VelRouting{ToCutoff: 0, ToAmp: 1},
		KbdTrack:   0,
		LFO:        LFO{Wave: "triangle", RateHz: 5, ToPitchCents: 0, ToCutoffOct: 0, ToAmp: 0},
		BendRange:  2,
		VoiceMode:  "mono_legato",
		Oversample: false,
	}
}

// synthToState converts the live cache to its persisted mirror
// (state.SynthState). The mirror is duplicated in internal/state because
// state must not import controls; these two helpers are the single
// crossing point, so a field added to SynthSnapshot fails to compile
// here until the state mirror learns it too.
func synthToState(s SynthSnapshot) state.SynthState {
	out := state.SynthState{
		Resonance: s.Resonance,
		FilterEnv: state.FilterEnvState{
			Attack:  s.FilterEnv.Attack,
			Decay:   s.FilterEnv.Decay,
			Sustain: s.FilterEnv.Sustain,
			Release: s.FilterEnv.Release,
			Amount:  s.FilterEnv.Amount,
		},
		AmpEnv: state.AmpEnvState{
			Attack:  s.AmpEnv.Attack,
			Decay:   s.AmpEnv.Decay,
			Sustain: s.AmpEnv.Sustain,
			Release: s.AmpEnv.Release,
		},
		Noise:      s.Noise,
		Glide:      s.Glide,
		PulseWidth: s.PulseWidth,
		Drive:      s.Drive,
		VelRouting: state.VelRoutingState{ToCutoff: s.VelRouting.ToCutoff, ToAmp: s.VelRouting.ToAmp},
		KbdTrack:   s.KbdTrack,
		LFO: state.LFOState{
			Wave:         s.LFO.Wave,
			RateHz:       s.LFO.RateHz,
			ToPitchCents: s.LFO.ToPitchCents,
			ToCutoffOct:  s.LFO.ToCutoffOct,
			ToAmp:        s.LFO.ToAmp,
		},
		BendRange:  s.BendRange,
		VoiceMode:  s.VoiceMode,
		Oversample: s.Oversample,
	}
	for i, o := range s.Oscs {
		out.Oscs[i] = state.OscState{Wave: o.Wave, Octave: o.Octave, DetuneCents: o.DetuneCents, Level: o.Level}
	}
	return out
}

// synthFromState is synthToState's inverse. It does NOT sanitize —
// state.toml is hand-editable, so every load goes through clampSynth
// before touching the engine (see applySynthAll).
func synthFromState(s state.SynthState) SynthSnapshot {
	out := SynthSnapshot{
		Resonance: s.Resonance,
		FilterEnv: FilterEnv{
			Attack:  s.FilterEnv.Attack,
			Decay:   s.FilterEnv.Decay,
			Sustain: s.FilterEnv.Sustain,
			Release: s.FilterEnv.Release,
			Amount:  s.FilterEnv.Amount,
		},
		AmpEnv: AmpEnv{
			Attack:  s.AmpEnv.Attack,
			Decay:   s.AmpEnv.Decay,
			Sustain: s.AmpEnv.Sustain,
			Release: s.AmpEnv.Release,
		},
		Noise:      s.Noise,
		Glide:      s.Glide,
		PulseWidth: s.PulseWidth,
		Drive:      s.Drive,
		VelRouting: VelRouting{ToCutoff: s.VelRouting.ToCutoff, ToAmp: s.VelRouting.ToAmp},
		KbdTrack:   s.KbdTrack,
		LFO: LFO{
			Wave:         s.LFO.Wave,
			RateHz:       s.LFO.RateHz,
			ToPitchCents: s.LFO.ToPitchCents,
			ToCutoffOct:  s.LFO.ToCutoffOct,
			ToAmp:        s.LFO.ToAmp,
		},
		BendRange:  s.BendRange,
		VoiceMode:  s.VoiceMode,
		Oversample: s.Oversample,
	}
	for i, o := range s.Oscs {
		out.Oscs[i] = OscParams{Wave: o.Wave, Octave: o.Octave, DetuneCents: o.DetuneCents, Level: o.Level}
	}
	return out
}

// clampSynth re-applies every setter's clamp to a whole snapshot, and
// swaps invalid osc waves for the factory default wave. Needed when a
// snapshot arrives wholesale (a persisted block at patch select) rather
// than through the individually-clamping setters: state.toml is
// hand-editable, so persisted values are inputs, not gospel.
func clampSynth(in SynthSnapshot) SynthSnapshot {
	def := defaultSynth()
	out := SynthSnapshot{
		Resonance: clampRange(in.Resonance, 0, maxResonance),
		FilterEnv: FilterEnv{
			Attack:  clampRange(in.FilterEnv.Attack, minEnvTime, maxEnvTime),
			Decay:   clampRange(in.FilterEnv.Decay, minEnvTime, maxEnvTime),
			Sustain: clamp01(in.FilterEnv.Sustain),
			Release: clampRange(in.FilterEnv.Release, minEnvTime, maxEnvTime),
			Amount:  clamp01(in.FilterEnv.Amount),
		},
		AmpEnv: AmpEnv{
			Attack:  clampRange(in.AmpEnv.Attack, minEnvTime, maxEnvTime),
			Decay:   clampRange(in.AmpEnv.Decay, minEnvTime, maxEnvTime),
			Sustain: clamp01(in.AmpEnv.Sustain),
			Release: clampRange(in.AmpEnv.Release, minEnvTime, maxEnvTime),
		},
		Noise:      clamp01(in.Noise),
		Glide:      clampRange(in.Glide, 0, maxGlide),
		PulseWidth: clampRange(in.PulseWidth, minPulseWidth, maxPulseWidth),
		Drive:      clamp01(in.Drive),
		VelRouting: VelRouting{ToCutoff: clamp01(in.VelRouting.ToCutoff), ToAmp: clamp01(in.VelRouting.ToAmp)},
		KbdTrack:   clamp01(in.KbdTrack),
		LFO: LFO{
			Wave:         in.LFO.Wave,
			RateHz:       clampRange(in.LFO.RateHz, minLFORateHz, maxLFORateHz),
			ToPitchCents: clampRange(in.LFO.ToPitchCents, 0, maxLFOPitchCents),
			ToCutoffOct:  clampRange(in.LFO.ToCutoffOct, 0, maxLFOCutoffOct),
			ToAmp:        clamp01(in.LFO.ToAmp),
		},
		BendRange:  clampRange(in.BendRange, 0, maxBendRange),
		VoiceMode:  in.VoiceMode,
		Oversample: in.Oversample,
	}
	if validateLFOWave(out.LFO.Wave) != nil {
		out.LFO.Wave = def.LFO.Wave
	}
	if validateVoiceMode(out.VoiceMode) != nil {
		out.VoiceMode = def.VoiceMode
	}
	for i, o := range in.Oscs {
		if validateOsc(i, o.Wave) != nil {
			o.Wave = def.Oscs[i].Wave
		}
		if o.Octave < -2 {
			o.Octave = -2
		} else if o.Octave > 2 {
			o.Octave = 2
		}
		o.DetuneCents = clampRange(o.DetuneCents, -maxDetune, maxDetune)
		o.Level = clamp01(o.Level)
		out.Oscs[i] = o
	}
	return out
}

// synthData is the wire shape of a full synth block inside a "patch"
// change: keys match the web layer's synthJSON (resonance, filter_env,
// osc, noise, glide) so SSE clients decode both the same way.
func synthData(s SynthSnapshot) map[string]any {
	oscs := make([]map[string]any, len(s.Oscs))
	for i, o := range s.Oscs {
		oscs[i] = map[string]any{
			"wave":         o.Wave,
			"octave":       o.Octave,
			"detune_cents": o.DetuneCents,
			"level":        o.Level,
		}
	}
	return map[string]any{
		"resonance": s.Resonance,
		"filter_env": map[string]any{
			"attack":  s.FilterEnv.Attack,
			"decay":   s.FilterEnv.Decay,
			"sustain": s.FilterEnv.Sustain,
			"release": s.FilterEnv.Release,
			"amount":  s.FilterEnv.Amount,
		},
		"amp_env":     ampEnvData(s.AmpEnv),
		"osc":         oscs,
		"noise":       s.Noise,
		"glide":       s.Glide,
		"pulse_width": s.PulseWidth,
		"drive":       s.Drive,
		"vel_routing": velRoutingData(s.VelRouting),
		"kbd_track":   s.KbdTrack,
		"lfo":         lfoData(s.LFO),
		"bend_range":  s.BendRange,
		"voice_mode":  s.VoiceMode,
		"oversample":  s.Oversample,
	}
}

// ampEnvData/velRoutingData/lfoData render one sub-block in the shared
// wire shape (synthJSON keys) — used by both the whole-block synthData
// and the per-field "synth" change publishes.
func ampEnvData(ae AmpEnv) map[string]any {
	return map[string]any{
		"attack":  ae.Attack,
		"decay":   ae.Decay,
		"sustain": ae.Sustain,
		"release": ae.Release,
	}
}

func velRoutingData(vr VelRouting) map[string]any {
	return map[string]any{"to_cutoff": vr.ToCutoff, "to_amp": vr.ToAmp}
}

func lfoData(l LFO) map[string]any {
	return map[string]any{
		"wave":           l.Wave,
		"rate_hz":        l.RateHz,
		"to_pitch_cents": l.ToPitchCents,
		"to_cutoff_oct":  l.ToCutoffOct,
		"to_amp":         l.ToAmp,
	}
}

// Controls owns the param-change sequence shared by every surface:
// clamp → audio apply → state persist → hub publish. It also holds the
// bits of runtime state that previously lived in main.go closures (the
// native-cutoff knob position, the mastering cache, the velocity remap).
// All methods are goroutine-safe.
type Controls struct {
	logger *slog.Logger
	audio  Audio
	reg    Registry
	st     StateStore
	hub    *Hub

	// applyMu is the single writer lock: every mutating method (the
	// knob setters/adjusters, cutoff, the SetSynth* family, MergeSynth,
	// patch selection, mastering, the velocity-remap swap) holds it
	// across its ENTIRE clamp → audio apply → state persist → hub
	// publish sequence. Without it, concurrent writers interleave those
	// steps and leave the engine, state.toml, and SSE subscribers
	// disagreeing about the last write. Every applied operation is
	// cheap (audio atomics, a map update, a non-blocking publish), so
	// one coarse writer lock is fine.
	//
	// Two-lock discipline: applyMu is always acquired strictly OUTSIDE
	// (before) mu, and mu is never held across a call out to audio, the
	// state store, or the hub. Read-only methods take only mu.
	applyMu sync.Mutex

	// mu guards the position/cache fields below. Knob values themselves
	// are NOT cached here — the state store stays the single source of
	// truth so a restart and a live read agree.
	mu               sync.Mutex
	cutoffPos        float32
	masteringComp    float32
	limiterCeilingDB float32
	// synth caches the native-synth params of the CURRENT patch. The
	// per-patch persistence contract (ROADMAP §3): every synth mutation
	// writes the whole resulting block to the state store for the
	// current patch, and every NATIVE patch select replaces cache and
	// engine from that patch's stored block (factory defaults when the
	// patch has never been tweaked). Non-native selects leave cache and
	// engine alone — the params are inaudible there, and clobbering them
	// would churn state.toml for nothing.
	synth SynthSnapshot

	// vel is an atomic pointer (not a mutex) because ApplyVelocity runs
	// on the MIDI goroutine per NoteOn — the hot path must never contend
	// with a web request swapping curves.
	vel atomic.Pointer[velocityRemap]
}

// New wires a Controls over the given collaborators. A nil logger falls
// back to slog.Default() (matching state.NewStore); a nil hub gets a
// private one so publishes never panic even when nothing subscribes.
func New(logger *slog.Logger, audio Audio, reg Registry, st StateStore, hub *Hub) *Controls {
	if logger == nil {
		logger = slog.Default()
	}
	if hub == nil {
		hub = NewHub()
	}
	return &Controls{
		logger:    logger,
		audio:     audio,
		reg:       reg,
		st:        st,
		hub:       hub,
		cutoffPos: defaultCutoffPos,
		synth:     defaultSynth(),
	}
}

// SetVolume sets the current patch's master volume to v (clamped to
// [0,1]), persists it, and publishes a "params" change. Returns the
// applied value, or an error if no patch is selected.
func (c *Controls) SetVolume(v float32) (float32, error) {
	return c.setKnob("volume", v)
}

// SetReverb is SetVolume's twin for the reverb send.
func (c *Controls) SetReverb(v float32) (float32, error) {
	return c.setKnob("reverb", v)
}

// SetCompressor is SetVolume's twin for the per-patch compressor amount.
func (c *Controls) SetCompressor(v float32) (float32, error) {
	return c.setKnob("compressor", v)
}

// AdjustVolume applies a signed delta (Launchkey relative knob) to the
// current patch's stored volume, with the same clamp/persist/publish
// sequence as SetVolume. ok is false when no patch is selected.
func (c *Controls) AdjustVolume(delta float32) (float32, bool) {
	return c.adjustKnob("volume", delta)
}

// AdjustReverb is AdjustVolume's twin for the reverb send.
func (c *Controls) AdjustReverb(delta float32) (float32, bool) {
	return c.adjustKnob("reverb", delta)
}

// AdjustCompressor is AdjustVolume's twin for the compressor amount.
func (c *Controls) AdjustCompressor(delta float32) (float32, bool) {
	return c.adjustKnob("compressor", delta)
}

// SetDrivePedal is SetVolume's twin for the per-patch drive-pedal
// amount. Backend-agnostic like Volume/Reverb/Compressor — the pedal
// runs in the shared post-synth chain, not inside the native synth.
func (c *Controls) SetDrivePedal(v float32) (float32, error) {
	return c.setKnob("drive_pedal", v)
}

// AdjustDrivePedal is AdjustVolume's twin for the drive-pedal amount.
func (c *Controls) AdjustDrivePedal(delta float32) (float32, bool) {
	return c.adjustKnob("drive_pedal", delta)
}

// setKnob is the absolute-setter path shared by SetVolume/Reverb/Compressor.
func (c *Controls) setKnob(field string, v float32) (float32, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur := c.reg.Current()
	if cur == nil {
		return 0, errNoPatch
	}
	v = clamp01(v)
	c.applyKnob(field, v)
	c.st.UpdatePatchKnob(cur.Name, field, v)
	c.publishKnob(field, v, cur.Name)
	return v, nil
}

// adjustKnob is the delta path shared by AdjustVolume/Reverb/Compressor.
// The current value is read from the state store (not cached locally) so
// deltas compose correctly with absolute sets from other surfaces.
func (c *Controls) adjustKnob(field string, delta float32) (float32, bool) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur := c.reg.Current()
	if cur == nil {
		return 0, false
	}
	knob := c.st.PatchKnob(cur.Name)
	v := clamp01(knobField(knob, field) + delta)
	c.applyKnob(field, v)
	c.st.UpdatePatchKnob(cur.Name, field, v)
	c.publishKnob(field, v, cur.Name)
	return v, true
}

func (c *Controls) applyKnob(field string, v float32) {
	switch field {
	case "volume":
		c.audio.SetMasterVolume(v)
	case "reverb":
		c.audio.SetReverb(v)
	case "compressor":
		c.audio.SetCompressor(v)
	case "drive_pedal":
		c.audio.SetDrivePedal(v)
	}
}

func (c *Controls) publishKnob(field string, v float32, patch string) {
	c.hub.Publish(Change{Type: "params", Data: map[string]any{
		"field": field,
		"value": v,
		"patch": patch,
	}})
}

// knobField reads the named field off a state.Knob. Field names match
// state.Store.UpdatePatchKnob ("volume", "reverb", "compressor").
func knobField(k state.Knob, field string) float32 {
	switch field {
	case "volume":
		return k.Volume
	case "reverb":
		return k.Reverb
	case "compressor":
		return k.Compressor
	case "drive_pedal":
		return k.DrivePedal
	}
	return 0
}

// AdjustCutoff nudges the native-synth cutoff knob position by delta and
// applies the resulting log-taper Hz to the engine. ok is false unless
// the current patch is a native synth — for every other patch type knob
// 4 is unmapped (matching the original main.go gating). The 0..1
// position lives here, not in the state store: cutoff persistence is
// deliberately Phase-2 work (docs/ROADMAP.md).
func (c *Controls) AdjustCutoff(delta float32) (float32, bool) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur := c.reg.Current()
	if cur == nil || cur.Type != "native" {
		return 0, false
	}
	c.mu.Lock()
	c.cutoffPos = clamp01(c.cutoffPos + delta)
	pos := c.cutoffPos
	c.mu.Unlock()
	hz := cutoffHzFromPos(pos)
	c.audio.SetNativeCutoffHz(hz)
	c.publishCutoff(cur.Name, pos, hz)
	return hz, true
}

// SetCutoffPos sets the cutoff knob to an absolute 0..1 position (web
// slider path). Errors unless a native patch is selected.
func (c *Controls) SetCutoffPos(pos float32) (float32, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur := c.reg.Current()
	if cur == nil || cur.Type != "native" {
		return 0, ErrNoNativePatch
	}
	pos = clamp01(pos)
	c.mu.Lock()
	c.cutoffPos = pos
	c.mu.Unlock()
	hz := cutoffHzFromPos(pos)
	c.audio.SetNativeCutoffHz(hz)
	c.publishCutoff(cur.Name, pos, hz)
	return hz, nil
}

// CutoffState reports the current cutoff knob position and its mapped
// frequency, for status snapshots and screens.
func (c *Controls) CutoffState() (pos, hz float32) {
	c.mu.Lock()
	pos = c.cutoffPos
	c.mu.Unlock()
	return pos, cutoffHzFromPos(pos)
}

func (c *Controls) publishCutoff(patch string, pos, hz float32) {
	c.hub.Publish(Change{Type: "params", Data: map[string]any{
		"field": "cutoff",
		"pos":   pos,
		"hz":    hz,
		"patch": patch,
	}})
}

// nativeCurrent returns the current patch iff it is a native synth —
// the shared gate for every SetSynth* setter (mirrors SetCutoffPos).
func (c *Controls) nativeCurrent() (*patches.Patch, error) {
	cur := c.reg.Current()
	if cur == nil || cur.Type != "native" {
		return nil, ErrNoNativePatch
	}
	return cur, nil
}

// publishSynth emits one "synth" change. data carries the changed values
// plus a "field" discriminator; the patch name rides along like the
// "params" changes do.
func (c *Controls) publishSynth(patch string, data map[string]any) {
	data["patch"] = patch
	c.hub.Publish(Change{Type: "synth", Data: data})
}

// persistSynth writes the current cached snapshot to the state store as
// patch's synth block — the save half of the ROADMAP §3 contract (every
// synth mutation persists; every native select restores). Callers hold
// applyMu, so the cache read and the store write cannot interleave with
// another mutation's sequence.
func (c *Controls) persistSynth(patch string) {
	c.st.UpdatePatchSynth(patch, synthToState(c.Synth()))
}

// applySynthAll pushes an ENTIRE snapshot into the engine and replaces
// the cache — the patch-select restore path, where the whole block
// changes at once. Unlike the apply* helpers it neither publishes (the
// caller folds the block into its "patch" change, so SSE clients see one
// atomic switch) nor persists (restoring is not an edit; a fresh patch
// only reaches disk on its first tweak). Values are re-clamped because
// persisted blocks are hand-editable. If the engine still rejects an
// oscillator, that osc keeps its previous cached value so cache and
// engine stay in agreement. Callers hold applyMu. Returns the snapshot
// as applied.
func (c *Controls) applySynthAll(in SynthSnapshot) SynthSnapshot {
	syn := clampSynth(in)
	c.audio.SetNativeResonance(syn.Resonance)
	c.audio.SetNativeFilterEnv(syn.FilterEnv.Attack, syn.FilterEnv.Decay, syn.FilterEnv.Sustain, syn.FilterEnv.Release, syn.FilterEnv.Amount)
	c.audio.SetNativeAmpEnv(syn.AmpEnv.Attack, syn.AmpEnv.Decay, syn.AmpEnv.Sustain, syn.AmpEnv.Release)
	c.audio.SetNativeNoise(syn.Noise)
	c.audio.SetNativeGlide(syn.Glide)
	c.audio.SetNativePulseWidth(syn.PulseWidth)
	c.audio.SetNativeDrive(syn.Drive)
	c.audio.SetNativeVelRouting(syn.VelRouting.ToCutoff, syn.VelRouting.ToAmp)
	c.audio.SetNativeKbdTrack(syn.KbdTrack)
	// clampSynth swapped any invalid LFO wave / voice mode for the factory
	// value, but if the engine still rejects, that section keeps its
	// previous cached value so cache and engine stay in agreement (the osc
	// rejection contract below).
	if err := c.audio.SetNativeLFO(syn.LFO.Wave, syn.LFO.RateHz, syn.LFO.ToPitchCents, syn.LFO.ToCutoffOct, syn.LFO.ToAmp); err != nil {
		c.mu.Lock()
		syn.LFO = c.synth.LFO
		c.mu.Unlock()
		c.logger.Warn("restore synth lfo rejected by engine", "err", err)
	}
	c.audio.SetNativeBendRange(syn.BendRange)
	if err := c.audio.SetNativeVoiceMode(syn.VoiceMode); err != nil {
		c.mu.Lock()
		syn.VoiceMode = c.synth.VoiceMode
		c.mu.Unlock()
		c.logger.Warn("restore synth voice mode rejected by engine", "err", err)
	}
	c.audio.SetNativeOversample(syn.Oversample)
	for i := range syn.Oscs {
		o := syn.Oscs[i]
		if err := c.audio.SetNativeOsc(i, o.Wave, o.Octave, o.DetuneCents, o.Level); err != nil {
			c.mu.Lock()
			syn.Oscs[i] = c.synth.Oscs[i]
			c.mu.Unlock()
			c.logger.Warn("restore synth osc rejected by engine", "index", i, "err", err)
		}
	}
	c.mu.Lock()
	c.synth = syn
	c.mu.Unlock()
	return syn
}

// SetSynthResonance sets the native filter resonance (clamped to
// [0, 0.95]), applies it to the engine, caches it, and publishes a
// "synth" change. Errors unless a native patch is selected.
func (c *Controls) SetSynthResonance(v float32) (float32, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return 0, err
	}
	return c.applyResonance(cur.Name, v), nil
}

// applyResonance is SetSynthResonance's clamp/apply/cache/persist/publish
// body. Callers hold applyMu and have passed the native-patch gate.
func (c *Controls) applyResonance(patch string, v float32) float32 {
	v = clampRange(v, 0, maxResonance)
	c.mu.Lock()
	c.synth.Resonance = v
	c.mu.Unlock()
	c.audio.SetNativeResonance(v)
	c.persistSynth(patch)
	c.publishSynth(patch, map[string]any{"field": "resonance", "resonance": v})
	return v
}

// SetSynthFilterEnv sets the filter-envelope ADSR + env→cutoff amount.
// Times clamp to [0.0001, 10] s; sustain and amount to [0, 1]. Returns
// the clamped values; errors unless a native patch is selected.
func (c *Controls) SetSynthFilterEnv(a, d, s, r, amount float32) (FilterEnv, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return FilterEnv{}, err
	}
	return c.applyFilterEnv(cur.Name, FilterEnv{Attack: a, Decay: d, Sustain: s, Release: r, Amount: amount}), nil
}

// applyFilterEnv is SetSynthFilterEnv's clamp/apply/cache/persist/publish
// body. Callers hold applyMu and have passed the native-patch gate.
func (c *Controls) applyFilterEnv(patch string, in FilterEnv) FilterEnv {
	fe := FilterEnv{
		Attack:  clampRange(in.Attack, minEnvTime, maxEnvTime),
		Decay:   clampRange(in.Decay, minEnvTime, maxEnvTime),
		Sustain: clamp01(in.Sustain),
		Release: clampRange(in.Release, minEnvTime, maxEnvTime),
		Amount:  clamp01(in.Amount),
	}
	c.mu.Lock()
	c.synth.FilterEnv = fe
	c.mu.Unlock()
	c.audio.SetNativeFilterEnv(fe.Attack, fe.Decay, fe.Sustain, fe.Release, fe.Amount)
	c.persistSynth(patch)
	c.publishSynth(patch, map[string]any{
		"field": "filter_env",
		"filter_env": map[string]any{
			"attack":  fe.Attack,
			"decay":   fe.Decay,
			"sustain": fe.Sustain,
			"release": fe.Release,
			"amount":  fe.Amount,
		},
	})
	return fe
}

// validateOsc is the shared idx/wave gate for SetSynthOsc and MergeSynth.
func validateOsc(idx int, wave string) error {
	if idx < 0 || idx > 2 {
		return fmt.Errorf("osc index %d out of range 0..2", idx)
	}
	switch wave {
	case "saw", "square", "pulse":
		return nil
	default:
		return fmt.Errorf("unknown osc wave %q (valid: saw, square, pulse)", wave)
	}
}

// validateLFOWave is the shared wave gate for SetSynthLFO, MergeSynth,
// and clampSynth (mirrors audio.SetNativeLFO's accepted names).
func validateLFOWave(wave string) error {
	switch wave {
	case "triangle", "saw", "square", "sh":
		return nil
	default:
		return fmt.Errorf("unknown lfo wave %q (valid: triangle, saw, square, sh)", wave)
	}
}

// validateVoiceMode is the shared mode gate for SetSynthVoiceMode,
// MergeSynth, and clampSynth (mirrors audio.SetNativeVoiceMode).
func validateVoiceMode(mode string) error {
	switch mode {
	case "mono_legato", "mono_retrig", "poly":
		return nil
	default:
		return fmt.Errorf("unknown voice mode %q (valid: mono_legato, mono_retrig, poly)", mode)
	}
}

// SetSynthOsc sets one oscillator (idx 0..2). wave must be saw, square,
// or pulse; octave clamps to [-2, 2], detune to [-100, 100] cents,
// level to [0, 1]. Returns the applied params; errors on a bad idx or
// wave, or unless a native patch is selected.
func (c *Controls) SetSynthOsc(idx int, wave string, octave int, detuneCents, level float32) (OscParams, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return OscParams{}, err
	}
	if err := validateOsc(idx, wave); err != nil {
		return OscParams{}, err
	}
	return c.applyOsc(cur.Name, idx, OscParams{Wave: wave, Octave: octave, DetuneCents: detuneCents, Level: level})
}

// applyOsc is SetSynthOsc's clamp/apply/cache/persist/publish body.
// Callers hold applyMu, have passed the native-patch gate, and have
// validated idx/in.Wave.
func (c *Controls) applyOsc(patch string, idx int, in OscParams) (OscParams, error) {
	octave := in.Octave
	if octave < -2 {
		octave = -2
	} else if octave > 2 {
		octave = 2
	}
	op := OscParams{
		Wave:        in.Wave,
		Octave:      octave,
		DetuneCents: clampRange(in.DetuneCents, -maxDetune, maxDetune),
		Level:       clamp01(in.Level),
	}
	// Audio first: idx/wave are pre-validated, but if the engine still
	// rejects, the cache must not drift from what actually applied.
	if err := c.audio.SetNativeOsc(idx, op.Wave, op.Octave, op.DetuneCents, op.Level); err != nil {
		return OscParams{}, err
	}
	c.mu.Lock()
	c.synth.Oscs[idx] = op
	c.mu.Unlock()
	c.persistSynth(patch)
	c.publishSynth(patch, map[string]any{
		"field":        "osc",
		"index":        idx,
		"wave":         op.Wave,
		"octave":       op.Octave,
		"detune_cents": op.DetuneCents,
		"level":        op.Level,
	})
	return op, nil
}

// SetSynthNoise sets the white-noise mixer level (clamped to [0, 1]).
// Errors unless a native patch is selected.
func (c *Controls) SetSynthNoise(level float32) (float32, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return 0, err
	}
	return c.applyNoise(cur.Name, level), nil
}

// applyNoise is SetSynthNoise's clamp/apply/cache/persist/publish body.
// Callers hold applyMu and have passed the native-patch gate.
func (c *Controls) applyNoise(patch string, level float32) float32 {
	level = clamp01(level)
	c.mu.Lock()
	c.synth.Noise = level
	c.mu.Unlock()
	c.audio.SetNativeNoise(level)
	c.persistSynth(patch)
	c.publishSynth(patch, map[string]any{"field": "noise", "noise": level})
	return level
}

// SetSynthGlide sets the glide (portamento) time constant in seconds
// (clamped to [0, 5]). Errors unless a native patch is selected.
func (c *Controls) SetSynthGlide(seconds float32) (float32, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return 0, err
	}
	return c.applyGlide(cur.Name, seconds), nil
}

// applyGlide is SetSynthGlide's clamp/apply/cache/persist/publish body.
// Callers hold applyMu and have passed the native-patch gate.
func (c *Controls) applyGlide(patch string, seconds float32) float32 {
	seconds = clampRange(seconds, 0, maxGlide)
	c.mu.Lock()
	c.synth.Glide = seconds
	c.mu.Unlock()
	c.audio.SetNativeGlide(seconds)
	c.persistSynth(patch)
	c.publishSynth(patch, map[string]any{"field": "glide", "glide": seconds})
	return seconds
}

// SetSynthAmpEnv sets the amp-envelope (env 1) ADSR. Times clamp to
// [0.0001, 10] s; sustain to [0, 1]. Returns the clamped values; errors
// unless a native patch is selected.
func (c *Controls) SetSynthAmpEnv(a, d, s, r float32) (AmpEnv, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return AmpEnv{}, err
	}
	return c.applyAmpEnv(cur.Name, AmpEnv{Attack: a, Decay: d, Sustain: s, Release: r}), nil
}

// applyAmpEnv is SetSynthAmpEnv's clamp/apply/cache/persist/publish
// body. Callers hold applyMu and have passed the native-patch gate.
func (c *Controls) applyAmpEnv(patch string, in AmpEnv) AmpEnv {
	ae := AmpEnv{
		Attack:  clampRange(in.Attack, minEnvTime, maxEnvTime),
		Decay:   clampRange(in.Decay, minEnvTime, maxEnvTime),
		Sustain: clamp01(in.Sustain),
		Release: clampRange(in.Release, minEnvTime, maxEnvTime),
	}
	c.mu.Lock()
	c.synth.AmpEnv = ae
	c.mu.Unlock()
	c.audio.SetNativeAmpEnv(ae.Attack, ae.Decay, ae.Sustain, ae.Release)
	c.persistSynth(patch)
	c.publishSynth(patch, map[string]any{"field": "amp_env", "amp_env": ampEnvData(ae)})
	return ae
}

// SetSynthPulseWidth sets the shared pulse-wave duty cycle (clamped to
// [0.05, 0.95]). Errors unless a native patch is selected.
func (c *Controls) SetSynthPulseWidth(w float32) (float32, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return 0, err
	}
	return c.applyPulseWidth(cur.Name, w), nil
}

// applyPulseWidth is SetSynthPulseWidth's clamp/apply/cache/persist/
// publish body. Callers hold applyMu and have passed the native gate.
func (c *Controls) applyPulseWidth(patch string, w float32) float32 {
	w = clampRange(w, minPulseWidth, maxPulseWidth)
	c.mu.Lock()
	c.synth.PulseWidth = w
	c.mu.Unlock()
	c.audio.SetNativePulseWidth(w)
	c.persistSynth(patch)
	c.publishSynth(patch, map[string]any{"field": "pulse_width", "pulse_width": w})
	return w
}

// SetSynthDrive sets the pre-filter tanh drive amount (clamped to
// [0, 1]). Errors unless a native patch is selected.
func (c *Controls) SetSynthDrive(d float32) (float32, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return 0, err
	}
	return c.applyDrive(cur.Name, d), nil
}

// applyDrive is SetSynthDrive's clamp/apply/cache/persist/publish body.
// Callers hold applyMu and have passed the native-patch gate.
func (c *Controls) applyDrive(patch string, d float32) float32 {
	d = clamp01(d)
	c.mu.Lock()
	c.synth.Drive = d
	c.mu.Unlock()
	c.audio.SetNativeDrive(d)
	c.persistSynth(patch)
	c.publishSynth(patch, map[string]any{"field": "drive", "drive": d})
	return d
}

// SetSynthVelRouting sets the velocity-routing amounts (both clamped to
// [0, 1]). Errors unless a native patch is selected.
func (c *Controls) SetSynthVelRouting(toCutoff, toAmp float32) (VelRouting, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return VelRouting{}, err
	}
	return c.applyVelRouting(cur.Name, VelRouting{ToCutoff: toCutoff, ToAmp: toAmp}), nil
}

// applyVelRouting is SetSynthVelRouting's clamp/apply/cache/persist/
// publish body. Callers hold applyMu and have passed the native gate.
func (c *Controls) applyVelRouting(patch string, in VelRouting) VelRouting {
	vr := VelRouting{ToCutoff: clamp01(in.ToCutoff), ToAmp: clamp01(in.ToAmp)}
	c.mu.Lock()
	c.synth.VelRouting = vr
	c.mu.Unlock()
	c.audio.SetNativeVelRouting(vr.ToCutoff, vr.ToAmp)
	c.persistSynth(patch)
	c.publishSynth(patch, map[string]any{"field": "vel_routing", "vel_routing": velRoutingData(vr)})
	return vr
}

// SetSynthKbdTrack sets the keyboard-tracking amount (clamped to
// [0, 1]). Errors unless a native patch is selected.
func (c *Controls) SetSynthKbdTrack(amt float32) (float32, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return 0, err
	}
	return c.applyKbdTrack(cur.Name, amt), nil
}

// applyKbdTrack is SetSynthKbdTrack's clamp/apply/cache/persist/publish
// body. Callers hold applyMu and have passed the native-patch gate.
func (c *Controls) applyKbdTrack(patch string, amt float32) float32 {
	amt = clamp01(amt)
	c.mu.Lock()
	c.synth.KbdTrack = amt
	c.mu.Unlock()
	c.audio.SetNativeKbdTrack(amt)
	c.persistSynth(patch)
	c.publishSynth(patch, map[string]any{"field": "kbd_track", "kbd_track": amt})
	return amt
}

// SetSynthLFO sets the global LFO block. wave must be triangle, saw,
// square, or sh; rate clamps to [0.05, 20] Hz, pitch depth to [0, 100]
// cents, cutoff depth to [0, 2] octaves, amp depth to [0, 1]. Returns
// the applied block; errors on a bad wave or unless a native patch is
// selected.
func (c *Controls) SetSynthLFO(wave string, rateHz, toPitchCents, toCutoffOct, toAmp float32) (LFO, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return LFO{}, err
	}
	if err := validateLFOWave(wave); err != nil {
		return LFO{}, err
	}
	return c.applyLFO(cur.Name, LFO{Wave: wave, RateHz: rateHz, ToPitchCents: toPitchCents, ToCutoffOct: toCutoffOct, ToAmp: toAmp})
}

// applyLFO is SetSynthLFO's clamp/apply/cache/persist/publish body.
// Callers hold applyMu, have passed the native-patch gate, and have
// validated in.Wave. Audio-first (like applyOsc): if the engine still
// rejects, the cache must not drift from what actually applied.
func (c *Controls) applyLFO(patch string, in LFO) (LFO, error) {
	l := LFO{
		Wave:         in.Wave,
		RateHz:       clampRange(in.RateHz, minLFORateHz, maxLFORateHz),
		ToPitchCents: clampRange(in.ToPitchCents, 0, maxLFOPitchCents),
		ToCutoffOct:  clampRange(in.ToCutoffOct, 0, maxLFOCutoffOct),
		ToAmp:        clamp01(in.ToAmp),
	}
	if err := c.audio.SetNativeLFO(l.Wave, l.RateHz, l.ToPitchCents, l.ToCutoffOct, l.ToAmp); err != nil {
		return LFO{}, err
	}
	c.mu.Lock()
	c.synth.LFO = l
	c.mu.Unlock()
	c.persistSynth(patch)
	c.publishSynth(patch, map[string]any{"field": "lfo", "lfo": lfoData(l)})
	return l, nil
}

// SetSynthBendRange sets the pitch-bend range in semitones (clamped to
// [0, 12]). Errors unless a native patch is selected.
func (c *Controls) SetSynthBendRange(st float32) (float32, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return 0, err
	}
	return c.applyBendRange(cur.Name, st), nil
}

// applyBendRange is SetSynthBendRange's clamp/apply/cache/persist/
// publish body. Callers hold applyMu and have passed the native gate.
func (c *Controls) applyBendRange(patch string, st float32) float32 {
	st = clampRange(st, 0, maxBendRange)
	c.mu.Lock()
	c.synth.BendRange = st
	c.mu.Unlock()
	c.audio.SetNativeBendRange(st)
	c.persistSynth(patch)
	c.publishSynth(patch, map[string]any{"field": "bend_range", "bend_range": st})
	return st
}

// SetSynthVoiceMode selects the voice-allocation mode (mono_legato,
// mono_retrig, or poly). Errors on an unknown mode or unless a native
// patch is selected.
func (c *Controls) SetSynthVoiceMode(mode string) (string, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return "", err
	}
	if err := validateVoiceMode(mode); err != nil {
		return "", err
	}
	return c.applyVoiceMode(cur.Name, mode)
}

// applyVoiceMode is SetSynthVoiceMode's apply/cache/persist/publish
// body. Callers hold applyMu, have passed the native-patch gate, and
// have validated mode. Audio-first (like applyOsc) so an engine
// rejection cannot desync the cache.
func (c *Controls) applyVoiceMode(patch, mode string) (string, error) {
	if err := c.audio.SetNativeVoiceMode(mode); err != nil {
		return "", err
	}
	c.mu.Lock()
	c.synth.VoiceMode = mode
	c.mu.Unlock()
	c.persistSynth(patch)
	c.publishSynth(patch, map[string]any{"field": "voice_mode", "voice_mode": mode})
	return mode, nil
}

// SetSynthOversample toggles 2x oversampling of the per-voice nonlinear
// section. Errors unless a native patch is selected.
func (c *Controls) SetSynthOversample(on bool) (bool, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return false, err
	}
	return c.applyOversample(cur.Name, on), nil
}

// applyOversample is SetSynthOversample's apply/cache/persist/publish
// body. Callers hold applyMu and have passed the native-patch gate.
func (c *Controls) applyOversample(patch string, on bool) bool {
	c.mu.Lock()
	c.synth.Oversample = on
	c.mu.Unlock()
	c.audio.SetNativeOversample(on)
	c.persistSynth(patch)
	c.publishSynth(patch, map[string]any{"field": "oversample", "oversample": on})
	return on
}

// SynthPartial is a partial native-synth update: nil fields (and nil
// sub-fields) keep their current values. It exists so partial PATCH
// bodies merge over the live snapshot INSIDE the applyMu critical
// section — a caller doing its own read-modify-write over Synth() would
// race concurrent writers and silently lose their updates.
type SynthPartial struct {
	Resonance  *float32
	FilterEnv  *FilterEnvPartial
	AmpEnv     *AmpEnvPartial
	Oscs       []OscPartial
	Noise      *float32
	Glide      *float32
	PulseWidth *float32
	Drive      *float32
	VelRouting *VelRoutingPartial
	KbdTrack   *float32
	LFO        *LFOPartial
	BendRange  *float32
	VoiceMode  *string
	Oversample *bool
}

// FilterEnvPartial is SynthPartial's filter-envelope section; nil
// fields keep the current envelope values.
type FilterEnvPartial struct {
	Attack, Decay, Sustain, Release, Amount *float32
}

// AmpEnvPartial is SynthPartial's amp-envelope section; nil fields keep
// the current envelope values.
type AmpEnvPartial struct {
	Attack, Decay, Sustain, Release *float32
}

// VelRoutingPartial is SynthPartial's velocity-routing section; nil
// fields keep the current amounts.
type VelRoutingPartial struct {
	ToCutoff, ToAmp *float32
}

// LFOPartial is SynthPartial's LFO section; nil fields keep the current
// values (a nil Wave keeps the current wave, which is always valid).
type LFOPartial struct {
	Wave                              *string
	RateHz, ToPitchCents, ToCutoffOct *float32
	ToAmp                             *float32
}

// OscPartial is one oscillator's partial update. Index says which osc
// to touch (0..2, required); every other field is optional.
type OscPartial struct {
	Index       int
	Wave        *string
	Octave      *int
	DetuneCents *float32
	Level       *float32
}

// MergeSynth merges p over the current synth params and applies the
// result, all under the writer lock, so two concurrent partial updates
// to different fields both survive. Each touched section runs the same
// clamp/apply/cache/persist/publish sequence (and emits the same "synth"
// change) as its SetSynth* counterpart, in a fixed order: resonance,
// filter_env, amp_env, noise, glide, pulse_width, drive, vel_routing,
// kbd_track, lfo, bend_range, voice_mode, oversample, then oscs. Osc
// index/wave, the LFO wave, and the voice mode are validated up front
// so an invalid entry applies nothing. Returns the resulting snapshot;
// errors unless a native patch is selected.
func (c *Controls) MergeSynth(p SynthPartial) (SynthSnapshot, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return SynthSnapshot{}, err
	}
	base := c.Synth() // stable while applyMu is held
	for _, o := range p.Oscs {
		if o.Index < 0 || o.Index > 2 {
			return SynthSnapshot{}, fmt.Errorf("osc index %d out of range 0..2", o.Index)
		}
		// A nil Wave keeps the current one, which is always valid.
		if o.Wave != nil {
			if err := validateOsc(o.Index, *o.Wave); err != nil {
				return SynthSnapshot{}, err
			}
		}
	}
	if p.LFO != nil && p.LFO.Wave != nil {
		if err := validateLFOWave(*p.LFO.Wave); err != nil {
			return SynthSnapshot{}, err
		}
	}
	if p.VoiceMode != nil {
		if err := validateVoiceMode(*p.VoiceMode); err != nil {
			return SynthSnapshot{}, err
		}
	}

	if p.Resonance != nil {
		c.applyResonance(cur.Name, *p.Resonance)
	}
	if p.FilterEnv != nil {
		fe := base.FilterEnv
		if p.FilterEnv.Attack != nil {
			fe.Attack = *p.FilterEnv.Attack
		}
		if p.FilterEnv.Decay != nil {
			fe.Decay = *p.FilterEnv.Decay
		}
		if p.FilterEnv.Sustain != nil {
			fe.Sustain = *p.FilterEnv.Sustain
		}
		if p.FilterEnv.Release != nil {
			fe.Release = *p.FilterEnv.Release
		}
		if p.FilterEnv.Amount != nil {
			fe.Amount = *p.FilterEnv.Amount
		}
		c.applyFilterEnv(cur.Name, fe)
	}
	if p.AmpEnv != nil {
		ae := base.AmpEnv
		if p.AmpEnv.Attack != nil {
			ae.Attack = *p.AmpEnv.Attack
		}
		if p.AmpEnv.Decay != nil {
			ae.Decay = *p.AmpEnv.Decay
		}
		if p.AmpEnv.Sustain != nil {
			ae.Sustain = *p.AmpEnv.Sustain
		}
		if p.AmpEnv.Release != nil {
			ae.Release = *p.AmpEnv.Release
		}
		c.applyAmpEnv(cur.Name, ae)
	}
	if p.Noise != nil {
		c.applyNoise(cur.Name, *p.Noise)
	}
	if p.Glide != nil {
		c.applyGlide(cur.Name, *p.Glide)
	}
	if p.PulseWidth != nil {
		c.applyPulseWidth(cur.Name, *p.PulseWidth)
	}
	if p.Drive != nil {
		c.applyDrive(cur.Name, *p.Drive)
	}
	if p.VelRouting != nil {
		vr := base.VelRouting
		if p.VelRouting.ToCutoff != nil {
			vr.ToCutoff = *p.VelRouting.ToCutoff
		}
		if p.VelRouting.ToAmp != nil {
			vr.ToAmp = *p.VelRouting.ToAmp
		}
		c.applyVelRouting(cur.Name, vr)
	}
	if p.KbdTrack != nil {
		c.applyKbdTrack(cur.Name, *p.KbdTrack)
	}
	if p.LFO != nil {
		l := base.LFO
		if p.LFO.Wave != nil {
			l.Wave = *p.LFO.Wave
		}
		if p.LFO.RateHz != nil {
			l.RateHz = *p.LFO.RateHz
		}
		if p.LFO.ToPitchCents != nil {
			l.ToPitchCents = *p.LFO.ToPitchCents
		}
		if p.LFO.ToCutoffOct != nil {
			l.ToCutoffOct = *p.LFO.ToCutoffOct
		}
		if p.LFO.ToAmp != nil {
			l.ToAmp = *p.LFO.ToAmp
		}
		if _, err := c.applyLFO(cur.Name, l); err != nil {
			// Engine rejection mid-sequence: earlier sections stay applied
			// and persisted; the LFO cache is untouched (applyOsc contract).
			return SynthSnapshot{}, err
		}
	}
	if p.BendRange != nil {
		c.applyBendRange(cur.Name, *p.BendRange)
	}
	if p.VoiceMode != nil {
		if _, err := c.applyVoiceMode(cur.Name, *p.VoiceMode); err != nil {
			return SynthSnapshot{}, err
		}
	}
	if p.Oversample != nil {
		c.applyOversample(cur.Name, *p.Oversample)
	}
	// Osc entries fold over an evolving working copy of the bank so
	// duplicate indexes in one body compose: entry 2 merges over what
	// entry 1 actually applied (post-clamp), instead of over the
	// pre-merge base (which silently reverted entry 1's other fields).
	oscs := base.Oscs
	for _, o := range p.Oscs {
		m := oscs[o.Index]
		if o.Wave != nil {
			m.Wave = *o.Wave
		}
		if o.Octave != nil {
			m.Octave = *o.Octave
		}
		if o.DetuneCents != nil {
			m.DetuneCents = *o.DetuneCents
		}
		if o.Level != nil {
			m.Level = *o.Level
		}
		applied, err := c.applyOsc(cur.Name, o.Index, m)
		if err != nil {
			// Engine rejection mid-sequence: earlier sections stay
			// applied and persisted (cache/engine/state/publishes agree
			// on them); this osc's cache is untouched.
			return SynthSnapshot{}, err
		}
		oscs[o.Index] = applied
	}
	return c.Synth(), nil
}

// AdjustSynth is the atomic read-modify-write primitive for the native
// synth block: it snapshots the current params, runs mutate on that
// snapshot, and applies every section mutate changed — all inside the
// writer lock, so the read and the write are one step with respect to
// every other surface. A caller doing its own read-modify-write over
// Synth() (the pre-AdjustSynth knob-page code) races concurrent writers:
// a MergeSynth landing between the read and the write gets its
// sibling-field edit silently reverted by the stale block pushed back.
//
// Each changed section runs the same clamp/apply/cache/persist/publish
// sequence (and emits the same "synth" change) as its SetSynth*
// counterpart, in MergeSynth's fixed order. Enum fields mutate touched
// (osc waves, the LFO wave, the voice mode) are validated up front so an
// invalid mutation applies nothing; an engine rejection mid-sequence
// leaves earlier sections applied and that section's cache untouched
// (the MergeSynth contract). Gated like every other synth setter:
// errors unless a native patch is selected (mutate does not run).
// Returns the resulting snapshot.
func (c *Controls) AdjustSynth(mutate func(*SynthSnapshot)) (SynthSnapshot, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	cur, err := c.nativeCurrent()
	if err != nil {
		return SynthSnapshot{}, err
	}
	base := c.Synth() // stable while applyMu is held
	next := base
	mutate(&next)

	for i := range next.Oscs {
		if next.Oscs[i] != base.Oscs[i] {
			if err := validateOsc(i, next.Oscs[i].Wave); err != nil {
				return SynthSnapshot{}, err
			}
		}
	}
	if next.LFO != base.LFO {
		if err := validateLFOWave(next.LFO.Wave); err != nil {
			return SynthSnapshot{}, err
		}
	}
	if next.VoiceMode != base.VoiceMode {
		if err := validateVoiceMode(next.VoiceMode); err != nil {
			return SynthSnapshot{}, err
		}
	}

	if next.Resonance != base.Resonance {
		c.applyResonance(cur.Name, next.Resonance)
	}
	if next.FilterEnv != base.FilterEnv {
		c.applyFilterEnv(cur.Name, next.FilterEnv)
	}
	if next.AmpEnv != base.AmpEnv {
		c.applyAmpEnv(cur.Name, next.AmpEnv)
	}
	if next.Noise != base.Noise {
		c.applyNoise(cur.Name, next.Noise)
	}
	if next.Glide != base.Glide {
		c.applyGlide(cur.Name, next.Glide)
	}
	if next.PulseWidth != base.PulseWidth {
		c.applyPulseWidth(cur.Name, next.PulseWidth)
	}
	if next.Drive != base.Drive {
		c.applyDrive(cur.Name, next.Drive)
	}
	if next.VelRouting != base.VelRouting {
		c.applyVelRouting(cur.Name, next.VelRouting)
	}
	if next.KbdTrack != base.KbdTrack {
		c.applyKbdTrack(cur.Name, next.KbdTrack)
	}
	if next.LFO != base.LFO {
		if _, err := c.applyLFO(cur.Name, next.LFO); err != nil {
			return SynthSnapshot{}, err
		}
	}
	if next.BendRange != base.BendRange {
		c.applyBendRange(cur.Name, next.BendRange)
	}
	if next.VoiceMode != base.VoiceMode {
		if _, err := c.applyVoiceMode(cur.Name, next.VoiceMode); err != nil {
			return SynthSnapshot{}, err
		}
	}
	if next.Oversample != base.Oversample {
		c.applyOversample(cur.Name, next.Oversample)
	}
	for i := range next.Oscs {
		if next.Oscs[i] != base.Oscs[i] {
			if _, err := c.applyOsc(cur.Name, i, next.Oscs[i]); err != nil {
				return SynthSnapshot{}, err
			}
		}
	}
	return c.Synth(), nil
}

// Synth returns the cached native-synth params (the defaults until a
// setter runs). Used by status snapshots and by the web layer's
// read-modify-write for partial PATCH bodies.
func (c *Controls) Synth() SynthSnapshot {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.synth
}

// SelectPatch switches to the named patch: registry select, restore that
// patch's saved knob values (and, for native patches, its saved synth
// block) into the audio engine, record it as current in the state store,
// publish a "patch" change. Identical to a pad press.
// The whole sequence runs under the writer lock, so a concurrent select
// (web vs pad) can never leave the engine on one patch while the
// registry/state/SSE say another.
func (c *Controls) SelectPatch(name string) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	if err := c.reg.Select(name); err != nil {
		return err
	}
	c.afterSelect()
	return nil
}

// SelectPatchIndex is SelectPatch by 0-based slot index (pad column).
func (c *Controls) SelectPatchIndex(i int) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	if err := c.reg.SelectIndex(i); err != nil {
		return err
	}
	c.afterSelect()
	return nil
}

// afterSelect is the shared post-selection sequence; callers hold
// applyMu. It re-reads Current() from the registry rather than trusting
// its own arguments so the restored knobs always match what the
// registry actually loaded.
func (c *Controls) afterSelect() {
	cur := c.reg.Current()
	if cur == nil {
		return
	}
	k := c.st.PatchKnob(cur.Name)
	c.audio.SetMasterVolume(k.Volume)
	c.audio.SetReverb(k.Reverb)
	c.audio.SetCompressor(k.Compressor)
	c.audio.SetDrivePedal(k.DrivePedal)
	c.st.SetCurrentPatch(cur.Name)
	data := map[string]any{
		"name":        cur.Name,
		"display":     cur.Display,
		"volume":      k.Volume,
		"reverb":      k.Reverb,
		"compressor":  k.Compressor,
		"drive_pedal": k.DrivePedal,
	}
	if cur.Type == "native" {
		// Cutoff position is per-session, not persisted (Phase 2): every
		// entry into a native patch starts from the known-good default.
		c.mu.Lock()
		c.cutoffPos = defaultCutoffPos
		c.mu.Unlock()
		hz := cutoffHzFromPos(defaultCutoffPos)
		c.audio.SetNativeCutoffHz(hz)
		// The reset changed the cutoff, so the publish must carry it or
		// SSE clients keep showing the pre-select position. Non-native
		// patches have no cutoff and omit both keys.
		data["cutoff_pos"] = float32(defaultCutoffPos)
		data["cutoff_hz"] = hz
		// Per-patch synth restore (ROADMAP §3): the patch's persisted
		// block, or factory defaults for a patch never tweaked. The whole
		// snapshot goes to the engine and cache, and the resulting block
		// rides in this "patch" change so SSE clients switch atomically.
		// Non-native selects skip all of this: the synth params are
		// inaudible there, and the engine keeps whatever the last native
		// patch applied until the next native select overwrites it.
		syn := defaultSynth()
		if st, ok := c.st.PatchSynth(cur.Name); ok {
			syn = synthFromState(st)
		}
		data["synth"] = synthData(c.applySynthAll(syn))
	}
	c.logger.Debug("patch selected via controls", "name", cur.Name)
	c.hub.Publish(Change{Type: "patch", Data: data})
}

// Mastering clamp ranges, mirroring the Rust-side clamps in audio-core
// (dsp/compressor.rs: amount.clamp(0.0, 1.0); lib.rs: limiter ceiling
// store_clamped(v, -12.0, 0.0)). Clamping here keeps the cache and the
// published changes telling the truth about what the engine applied.
const (
	minLimiterCeilingDB = -12
	maxLimiterCeilingDB = 0
)

// SetMastering applies mastering params to the engine and caches them
// for status reads. Values clamp to the engine's ranges (comp amount to
// [0, 1], limiter ceiling to [-12, 0] dB). Either pointer may be nil,
// meaning "leave that param unchanged" — the web PATCH body sends only
// what the user moved. Publishes a "mastering" change carrying only the
// keys that changed; nothing is published when both are nil. Returns
// the resulting (clamped) values.
func (c *Controls) SetMastering(compAmount, ceilingDB *float32) (comp, ceiling float32) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	data := make(map[string]any, 2)
	c.mu.Lock()
	if compAmount != nil {
		v := clamp01(*compAmount)
		c.masteringComp = v
		data["comp_amount"] = v
	}
	if ceilingDB != nil {
		v := clampRange(*ceilingDB, minLimiterCeilingDB, maxLimiterCeilingDB)
		c.limiterCeilingDB = v
		data["limiter_ceiling_db"] = v
	}
	comp, ceiling = c.masteringComp, c.limiterCeilingDB
	c.mu.Unlock()
	if compAmount != nil {
		c.audio.SetMasteringCompressor(comp)
	}
	if ceilingDB != nil {
		c.audio.SetLimiterCeilingDB(ceiling)
	}
	if len(data) > 0 {
		c.hub.Publish(Change{Type: "mastering", Data: data})
	}
	return comp, ceiling
}

// Mastering returns the cached mastering params (last Init/SetMastering
// values). Cached here because the audio atomics are write-only from
// this side of the fence.
func (c *Controls) Mastering() (comp, ceiling float32) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.masteringComp, c.limiterCeilingDB
}

// InitMastering is the startup seed: apply the config-file values and
// cache them WITHOUT publishing — nothing has "changed" at boot, and
// early subscribers should not see a phantom edit. Values clamp to the
// same engine ranges as SetMastering so an out-of-range config value
// cannot make the cache disagree with the engine.
func (c *Controls) InitMastering(comp, ceiling float32) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	comp = clamp01(comp)
	ceiling = clampRange(ceiling, minLimiterCeilingDB, maxLimiterCeilingDB)
	c.mu.Lock()
	c.masteringComp = comp
	c.limiterCeilingDB = ceiling
	c.mu.Unlock()
	c.audio.SetMasteringCompressor(comp)
	c.audio.SetLimiterCeilingDB(ceiling)
}

// SetVelocityRemap installs a velocity curve and its display label, then
// publishes a "velocity" change. fn is func-typed (not a velocity-package
// type) so this package stays import-free of internal/velocity — the
// dependency arrow points from the curve implementation to here, never
// back.
func (c *Controls) SetVelocityRemap(fn func(uint8) uint8, label string) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	c.vel.Store(&velocityRemap{fn: fn, label: label})
	c.hub.Publish(Change{Type: "velocity", Data: map[string]any{
		"curve": label,
	}})
}

// ApplyVelocity remaps a NoteOn velocity through the installed curve,
// or returns v unchanged when none is set. Lock-free (atomic pointer
// load) because it runs per NoteOn on the MIDI goroutine.
func (c *Controls) ApplyVelocity(v uint8) uint8 {
	r := c.vel.Load()
	if r == nil || r.fn == nil {
		return v
	}
	return r.fn(v)
}

// VelocityLabel returns the installed curve's display label, or "" when
// no remap is set.
func (c *Controls) VelocityLabel() string {
	r := c.vel.Load()
	if r == nil {
		return ""
	}
	return r.label
}

// ParamsSnapshot is the one-shot state view for GET /api/status: current
// patch, its knob values, cutoff, mastering, and the velocity curve name.
type ParamsSnapshot struct {
	Patch, PatchDisplay                    string
	Volume, Reverb, Compressor, DrivePedal float32
	CutoffPos, CutoffHz                    float32
	MasteringComp, LimiterCeilingDB        float32
	VelocityCurve                          string
	Synth                                  SynthSnapshot
}

// Snapshot assembles a ParamsSnapshot from the registry, state store,
// and local caches. With no patch selected, Patch is "" and the knob
// fields are zero.
func (c *Controls) Snapshot() ParamsSnapshot {
	var s ParamsSnapshot
	if cur := c.reg.Current(); cur != nil {
		s.Patch = cur.Name
		s.PatchDisplay = cur.Display
		k := c.st.PatchKnob(cur.Name)
		s.Volume, s.Reverb, s.Compressor = k.Volume, k.Reverb, k.Compressor
		s.DrivePedal = k.DrivePedal
	}
	s.CutoffPos, s.CutoffHz = c.CutoffState()
	s.MasteringComp, s.LimiterCeilingDB = c.Mastering()
	s.VelocityCurve = c.VelocityLabel()
	s.Synth = c.Synth()
	return s
}

func clamp01(v float32) float32 {
	return clampRange(v, 0, 1)
}

func clampRange(v, lo, hi float32) float32 {
	if v < lo {
		return lo
	}
	if v > hi {
		return hi
	}
	return v
}

// cutoffHzFromPos maps a 0..1 knob position onto a log-tapered Hz range
// (20 Hz – 20 kHz): hz = 20 * 1000^pos. 0 -> 20 Hz, 1 -> 20 kHz,
// 0.5 -> ~632 Hz. Copied verbatim from cmd/polyclav/main.go so both map
// identically until main is refactored onto this package.
func cutoffHzFromPos(pos float32) float32 {
	pos = clamp01(pos)
	// 20 Hz * (1000)^pos; 1000 = 20000/20.
	return float32(20.0 * math.Pow(1000.0, float64(pos)))
}
