package controls

import (
	"errors"
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

var (
	errNoPatch       = errors.New("no patch selected")
	errNoNativePatch = errors.New("no native patch selected")
)

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
		return 0, errNoNativePatch
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
	return s
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
// (20 Hz – 20 kHz): hz = 20 * 1000^pos. 0 -> 20 Hz, 1 -> 20 kHz,
// 0.5 -> ~632 Hz. Copied verbatim from cmd/polyclav/main.go so both map
// identically until main is refactored onto this package.
func cutoffHzFromPos(pos float32) float32 {
	pos = clamp01(pos)
	// 20 Hz * (1000)^pos; 1000 = 20000/20.
	return float32(20.0 * math.Pow(1000.0, float64(pos)))
}
