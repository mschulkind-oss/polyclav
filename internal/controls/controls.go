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
	SetNativeCutoffHz(float32)
	SetMasteringCompressor(float32)
	SetLimiterCeilingDB(float32)
	SetNativeResonance(v float32)
	SetNativeFilterEnv(a, d, s, r, amount float32)
	SetNativeOsc(idx int, wave string, octave int, detuneCents, level float32) error
	SetNativeNoise(level float32)
	SetNativeGlide(s float32)
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

// OscParams is one native-synth oscillator's settings (docs/ROADMAP.md §1.4).
type OscParams struct {
	Wave        string
	Octave      int
	DetuneCents float32
	Level       float32
}

// SynthSnapshot is the cached view of every native-synth parameter this
// layer pushes. Cached here (not read back from the engine) because the
// audio atomics are write-only from this side of the fence — same
// rationale as the mastering cache.
type SynthSnapshot struct {
	Resonance float32
	FilterEnv FilterEnv
	Oscs      [3]OscParams
	Noise     float32
	Glide     float32
}

// Native-synth clamp ranges, mirroring the Rust-side clamps in
// audio-core (internal/audio doc comments are the contract).
const (
	maxResonance = 0.95   // headroom below ladder self-oscillation
	minEnvTime   = 0.0001 // seconds
	maxEnvTime   = 10     // seconds
	maxDetune    = 100    // cents
	maxGlide     = 5      // seconds
)

// defaultSynth returns the boot values: the audio-core defaults
// (oscillator.rs default_bank(), filter/env defaults) so the cache and
// the engine agree before any setter runs. Oscs 2/3 are pre-dialed but
// silent (level 0) — turning a level up immediately sounds Moog-ish.
func defaultSynth() SynthSnapshot {
	return SynthSnapshot{
		Resonance: 0.3,
		FilterEnv: FilterEnv{Attack: 0.005, Decay: 0.6, Sustain: 0.4, Release: 0.6, Amount: 0},
		Oscs: [3]OscParams{
			{Wave: "saw", Octave: 0, DetuneCents: 0, Level: 1.0},
			{Wave: "saw", Octave: 0, DetuneCents: -7, Level: 0.0},
			{Wave: "saw", Octave: -1, DetuneCents: 5, Level: 0.0},
		},
		Noise: 0,
		Glide: 0,
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

	// mu guards the position/cache fields below. Knob values themselves
	// are NOT cached here — the state store stays the single source of
	// truth so a restart and a live read agree.
	mu               sync.Mutex
	cutoffPos        float32
	masteringComp    float32
	limiterCeilingDB float32
	// synth caches the native-synth params. Engine-global atomics for
	// now: patch selection does NOT reset them (per-patch persistence is
	// ROADMAP §3 work).
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

// setKnob is the absolute-setter path shared by SetVolume/Reverb/Compressor.
func (c *Controls) setKnob(field string, v float32) (float32, error) {
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

// SetSynthResonance sets the native filter resonance (clamped to
// [0, 0.95]), applies it to the engine, caches it, and publishes a
// "synth" change. Errors unless a native patch is selected.
func (c *Controls) SetSynthResonance(v float32) (float32, error) {
	cur, err := c.nativeCurrent()
	if err != nil {
		return 0, err
	}
	v = clampRange(v, 0, maxResonance)
	c.mu.Lock()
	c.synth.Resonance = v
	c.mu.Unlock()
	c.audio.SetNativeResonance(v)
	c.publishSynth(cur.Name, map[string]any{"field": "resonance", "resonance": v})
	return v, nil
}

// SetSynthFilterEnv sets the filter-envelope ADSR + env→cutoff amount.
// Times clamp to [0.0001, 10] s; sustain and amount to [0, 1]. Returns
// the clamped values; errors unless a native patch is selected.
func (c *Controls) SetSynthFilterEnv(a, d, s, r, amount float32) (FilterEnv, error) {
	cur, err := c.nativeCurrent()
	if err != nil {
		return FilterEnv{}, err
	}
	fe := FilterEnv{
		Attack:  clampRange(a, minEnvTime, maxEnvTime),
		Decay:   clampRange(d, minEnvTime, maxEnvTime),
		Sustain: clamp01(s),
		Release: clampRange(r, minEnvTime, maxEnvTime),
		Amount:  clamp01(amount),
	}
	c.mu.Lock()
	c.synth.FilterEnv = fe
	c.mu.Unlock()
	c.audio.SetNativeFilterEnv(fe.Attack, fe.Decay, fe.Sustain, fe.Release, fe.Amount)
	c.publishSynth(cur.Name, map[string]any{
		"field": "filter_env",
		"filter_env": map[string]any{
			"attack":  fe.Attack,
			"decay":   fe.Decay,
			"sustain": fe.Sustain,
			"release": fe.Release,
			"amount":  fe.Amount,
		},
	})
	return fe, nil
}

// SetSynthOsc sets one oscillator (idx 0..2). wave must be saw, square,
// or pulse; octave clamps to [-2, 2], detune to [-100, 100] cents,
// level to [0, 1]. Returns the applied params; errors on a bad idx or
// wave, or unless a native patch is selected.
func (c *Controls) SetSynthOsc(idx int, wave string, octave int, detuneCents, level float32) (OscParams, error) {
	cur, err := c.nativeCurrent()
	if err != nil {
		return OscParams{}, err
	}
	if idx < 0 || idx > 2 {
		return OscParams{}, fmt.Errorf("osc index %d out of range 0..2", idx)
	}
	switch wave {
	case "saw", "square", "pulse":
	default:
		return OscParams{}, fmt.Errorf("unknown osc wave %q (valid: saw, square, pulse)", wave)
	}
	if octave < -2 {
		octave = -2
	} else if octave > 2 {
		octave = 2
	}
	op := OscParams{
		Wave:        wave,
		Octave:      octave,
		DetuneCents: clampRange(detuneCents, -maxDetune, maxDetune),
		Level:       clamp01(level),
	}
	// Audio first: idx/wave are pre-validated, but if the engine still
	// rejects, the cache must not drift from what actually applied.
	if err := c.audio.SetNativeOsc(idx, op.Wave, op.Octave, op.DetuneCents, op.Level); err != nil {
		return OscParams{}, err
	}
	c.mu.Lock()
	c.synth.Oscs[idx] = op
	c.mu.Unlock()
	c.publishSynth(cur.Name, map[string]any{
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
	cur, err := c.nativeCurrent()
	if err != nil {
		return 0, err
	}
	level = clamp01(level)
	c.mu.Lock()
	c.synth.Noise = level
	c.mu.Unlock()
	c.audio.SetNativeNoise(level)
	c.publishSynth(cur.Name, map[string]any{"field": "noise", "noise": level})
	return level, nil
}

// SetSynthGlide sets the glide (portamento) time constant in seconds
// (clamped to [0, 5]). Errors unless a native patch is selected.
func (c *Controls) SetSynthGlide(seconds float32) (float32, error) {
	cur, err := c.nativeCurrent()
	if err != nil {
		return 0, err
	}
	seconds = clampRange(seconds, 0, maxGlide)
	c.mu.Lock()
	c.synth.Glide = seconds
	c.mu.Unlock()
	c.audio.SetNativeGlide(seconds)
	c.publishSynth(cur.Name, map[string]any{"field": "glide", "glide": seconds})
	return seconds, nil
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
// patch's saved knob values into the audio engine, record it as current
// in the state store, publish a "patch" change. Identical to a pad press.
func (c *Controls) SelectPatch(name string) error {
	if err := c.reg.Select(name); err != nil {
		return err
	}
	c.afterSelect()
	return nil
}

// SelectPatchIndex is SelectPatch by 0-based slot index (pad column).
func (c *Controls) SelectPatchIndex(i int) error {
	if err := c.reg.SelectIndex(i); err != nil {
		return err
	}
	c.afterSelect()
	return nil
}

// afterSelect is the shared post-selection sequence. It re-reads
// Current() from the registry rather than trusting its own arguments so
// the restored knobs always match what the registry actually loaded.
func (c *Controls) afterSelect() {
	cur := c.reg.Current()
	if cur == nil {
		return
	}
	k := c.st.PatchKnob(cur.Name)
	c.audio.SetMasterVolume(k.Volume)
	c.audio.SetReverb(k.Reverb)
	c.audio.SetCompressor(k.Compressor)
	c.st.SetCurrentPatch(cur.Name)
	if cur.Type == "native" {
		// Cutoff position is per-session, not persisted (Phase 2): every
		// entry into a native patch starts from the known-good default.
		c.mu.Lock()
		c.cutoffPos = defaultCutoffPos
		c.mu.Unlock()
		c.audio.SetNativeCutoffHz(cutoffHzFromPos(defaultCutoffPos))
	}
	c.logger.Debug("patch selected via controls", "name", cur.Name)
	c.hub.Publish(Change{Type: "patch", Data: map[string]any{
		"name":       cur.Name,
		"display":    cur.Display,
		"volume":     k.Volume,
		"reverb":     k.Reverb,
		"compressor": k.Compressor,
	}})
}

// SetMastering applies mastering params to the engine and caches them
// for status reads. Either pointer may be nil, meaning "leave that param
// unchanged" — the web PATCH body sends only what the user moved.
// Publishes a "mastering" change carrying only the keys that changed;
// nothing is published when both are nil. Returns the resulting values.
func (c *Controls) SetMastering(compAmount, ceilingDB *float32) (comp, ceiling float32) {
	data := make(map[string]any, 2)
	c.mu.Lock()
	if compAmount != nil {
		c.masteringComp = *compAmount
		data["comp_amount"] = *compAmount
	}
	if ceilingDB != nil {
		c.limiterCeilingDB = *ceilingDB
		data["limiter_ceiling_db"] = *ceilingDB
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
// early subscribers should not see a phantom edit.
func (c *Controls) InitMastering(comp, ceiling float32) {
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
	Patch, PatchDisplay             string
	Volume, Reverb, Compressor      float32
	CutoffPos, CutoffHz             float32
	MasteringComp, LimiterCeilingDB float32
	VelocityCurve                   string
	Synth                           SynthSnapshot
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
