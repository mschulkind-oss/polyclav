// Package state persists per-patch knob values (volume/reverb/compressor/
// drive_pedal), per-patch native-synth parameters, and the active patch
// name to a TOML file. The Store debounces writes so rapid knob twists
// coalesce into a single disk flush, while a final synchronous flush on
// shutdown ensures the user's last edit survives.
package state

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/BurntSushi/toml"
)

// Knob holds the user-controllable post-synth DSP values for a patch:
// the four original knobs (volume/reverb/compressor/drive) plus the
// chorus/tremolo/analog-delay chain-effect params and per-stage enable
// flags. Volume/reverb/compressor/mix/depth values are 0..1 linear
// (matching audio.SetMasterVolume etc.); the rate/time params carry
// their engine units (Hz, ms).
type Knob struct {
	Volume     float32 `toml:"volume"`
	Reverb     float32 `toml:"reverb"`
	Compressor float32 `toml:"compressor"`
	// DrivePedal is the drive-pedal amount (docs/OPEN_SOUND_ENGINES.md
	// §1). Backend-agnostic like Volume/Reverb/Compressor above — it
	// runs in the shared post-synth chain, not inside the native synth
	// — so it lives here rather than in SynthState.
	DrivePedal float32 `toml:"drive_pedal"`
	// Post-synth chain-effect params (chorus/tremolo/analog-delay),
	// backend-agnostic like DrivePedal above. Units are the engine's:
	// *_rate_hz in Hz, delay_time_ms in ms, the rest 0..1. The registry
	// in internal/controls/chain.go is the single source of the ranges,
	// defaults, and which audio setter each drives.
	ChorusRateHz  float32 `toml:"chorus_rate_hz"`
	ChorusDepth   float32 `toml:"chorus_depth"`
	ChorusMix     float32 `toml:"chorus_mix"`
	TremoloRateHz float32 `toml:"tremolo_rate_hz"`
	TremoloDepth  float32 `toml:"tremolo_depth"`
	DelayTimeMs   float32 `toml:"delay_time_ms"`
	DelayFeedback float32 `toml:"delay_feedback"`
	DelayMix      float32 `toml:"delay_mix"`
	// Per-stage enable flags. An ABSENT enable means "enabled" (see
	// fillChainDefaults) so legacy state.toml files and hand-written
	// partial blocks keep every pedal on. Disabling a stage parks its
	// gate param (mix/amount/depth) at 0 in the engine — there is no
	// Rust-side bypass — but the stored param value is preserved so
	// re-enabling restores it.
	DriveEnabled   bool `toml:"drive_enabled"`
	ChorusEnabled  bool `toml:"chorus_enabled"`
	TremoloEnabled bool `toml:"tremolo_enabled"`
	DelayEnabled   bool `toml:"delay_enabled"`
}

// SynthState is the persisted per-patch native-synth block — the
// [patches.<name>.synth] sub-table of state.toml (docs/ROADMAP.md §3.1).
// It mirrors internal/controls.SynthSnapshot field-for-field: state must
// stay import-free of controls (the dependency arrow points controls ->
// state), so the shape is duplicated here and controls owns the
// conversion helpers. Keep the two types in lockstep.
//
// Deviations from the §3.1 sketch, matching what actually shipped
// (Phase 2):
//   - resonance sits at the synth root — there is no [synth.filter]
//     table because cutoff is deliberately session-only (see
//     controls.AdjustCutoff) and the other filter params don't exist yet.
//   - envelope times are seconds (the controls/engine unit), not ms.
//   - oscillators are an array-of-tables [[...synth.oscs]] rather than
//     osc1/osc2/osc3 named tables: SynthSnapshot models them as an
//     indexed array, and a TOML array round-trips the indices directly.
//   - only the shipped params exist (no page/mod/noise-color).
//
// Phase 3/4 fields (amp_env, pulse_width, drive, vel_routing,
// kbd_track, lfo, bend_range, voice_mode, oversample) may be ABSENT in
// files written by older builds; Load fills those with the engine
// defaults (see fillSynthDefaults) so a legacy block cannot silently
// zero them (vel_routing.to_amp = 0 would mute velocity response).
type SynthState struct {
	Resonance  float32         `toml:"resonance"`
	FilterEnv  FilterEnvState  `toml:"filter_env"`
	AmpEnv     AmpEnvState     `toml:"amp_env"`
	Oscs       [3]OscState     `toml:"oscs"`
	Noise      float32         `toml:"noise"`
	Glide      float32         `toml:"glide"`
	PulseWidth float32         `toml:"pulse_width"`
	Drive      float32         `toml:"drive"`
	VelRouting VelRoutingState `toml:"vel_routing"`
	KbdTrack   float32         `toml:"kbd_track"`
	LFO        LFOState        `toml:"lfo"`
	BendRange  float32         `toml:"bend_range"`
	VoiceMode  string          `toml:"voice_mode"`
	Oversample bool            `toml:"oversample"`
}

// FilterEnvState is SynthState's filter-envelope section (ADSR seconds/
// levels plus the env→cutoff amount), mirroring controls.FilterEnv.
type FilterEnvState struct {
	Attack  float32 `toml:"attack"`
	Decay   float32 `toml:"decay"`
	Sustain float32 `toml:"sustain"`
	Release float32 `toml:"release"`
	Amount  float32 `toml:"amount"`
}

// AmpEnvState is SynthState's amp-envelope section (ADSR, no modulation
// amount), mirroring controls.AmpEnv.
type AmpEnvState struct {
	Attack  float32 `toml:"attack"`
	Decay   float32 `toml:"decay"`
	Sustain float32 `toml:"sustain"`
	Release float32 `toml:"release"`
}

// VelRoutingState is SynthState's velocity-routing section, mirroring
// controls.VelRouting.
type VelRoutingState struct {
	ToCutoff float32 `toml:"to_cutoff"`
	ToAmp    float32 `toml:"to_amp"`
}

// LFOState is SynthState's global-LFO section, mirroring controls.LFO.
type LFOState struct {
	Wave         string  `toml:"wave"`
	RateHz       float32 `toml:"rate_hz"`
	ToPitchCents float32 `toml:"to_pitch_cents"`
	ToCutoffOct  float32 `toml:"to_cutoff_oct"`
	ToAmp        float32 `toml:"to_amp"`
}

// OscState is one oscillator's persisted settings, mirroring
// controls.OscParams.
type OscState struct {
	Wave        string  `toml:"wave"`
	Octave      int     `toml:"octave"`
	DetuneCents float32 `toml:"detune_cents"`
	Level       float32 `toml:"level"`
}

// PatchState is one patch's persisted entry: the knob block inline at the
// patch root (unchanged from the pre-synth schema, so existing state.toml
// files decode as-is) plus an OPTIONAL synth sub-table. Synth is a
// pointer so its absence survives a round trip — soundfont patches never
// grow a synth table, and a native patch only gets one after its first
// synth tweak.
type PatchState struct {
	Knob
	Synth *SynthState `toml:"synth,omitempty"`
}

// Macro is one Launchkey-style macro-slot assignment: slot 1..8 mapped
// to a board param (Target, an opaque id like "delay.mix") with a display
// Name and the Min/Max the web maps the knob sweep across. The backend
// only STORES + broadcasts these assignments — the web drives the target
// params directly through the existing setters — so nothing here is
// clamped or applied to the engine.
type Macro struct {
	Slot   int     `toml:"slot" json:"slot"`
	Target string  `toml:"target" json:"target"`
	Name   string  `toml:"name,omitempty" json:"name"`
	Min    float32 `toml:"min" json:"min"`
	Max    float32 `toml:"max" json:"max"`
}

// Snapshot is the file-level shape persisted to state.toml.
type Snapshot struct {
	CurrentPatch string                `toml:"current_patch"`
	Patches      map[string]PatchState `toml:"patches"`
	// PedalOrder is the GLOBAL order of the six post-synth FX pedals
	// ("drive", "chorus", "tremolo", "delay", "comp", "reverb"), empty
	// until the user reorders. This is AUDIBLE: controls packs it and
	// pushes it to the engine (polyclav_dsp_set_fx_order), which applies
	// the pedals in this order (render_block). Stored globally (not
	// per-patch) — a pedalboard layout, not a patch tone.
	PedalOrder []string `toml:"pedal_order,omitempty"`
	// Macros is the GLOBAL set of the 8 macro-slot assignments, empty
	// until the user assigns one. Stored globally (not per-patch) and
	// edited only by the web UI; the backend persists and broadcasts the
	// assignments but never applies them (the web drives each Target param
	// through the existing setters). See controls.SetMacros.
	Macros []Macro `toml:"macros,omitempty"`
}

// Store owns the in-memory snapshot and a debounced write loop.
// Methods are goroutine-safe. Caller must invoke Run(ctx) in a goroutine
// for writes to be flushed; without Run, updates are recorded in memory
// only (useful for tests).
type Store struct {
	path     string
	debounce time.Duration
	logger   *slog.Logger

	mu    sync.Mutex
	snap  Snapshot
	dirty bool

	// wake signals the Run loop that an update arrived; buffered len=1 so
	// multiple updates between debounce ticks coalesce.
	wake chan struct{}
}

// Defaults returns sensible starting knob values for a patch we've never
// seen before: full volume, no reverb, no compression, no drive, every
// chain effect at its registry default (chorus 0.8 Hz / tremolo 4 Hz /
// delay 300 ms rates and times; every mix/depth/feedback at 0 = silent),
// and every stage enabled. The non-zero rate/time defaults mirror
// internal/controls/chain.go (and fillChainDefaults) — keep the three
// in lockstep.
func Defaults() Knob {
	return Knob{
		Volume: 1.0, Reverb: 0.0, Compressor: 0.0, DrivePedal: 0.0,
		ChorusRateHz: 0.8, ChorusDepth: 0.0, ChorusMix: 0.0,
		TremoloRateHz: 4.0, TremoloDepth: 0.0,
		DelayTimeMs: 300.0, DelayFeedback: 0.0, DelayMix: 0.0,
		DriveEnabled: true, ChorusEnabled: true, TremoloEnabled: true, DelayEnabled: true,
	}
}

// Load reads a TOML file at path and returns its decoded Snapshot.
// If the file does not exist, returns an empty snapshot (normal first-run).
func Load(path string) (Snapshot, error) {
	var snap Snapshot
	md, err := toml.DecodeFile(path, &snap)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Snapshot{Patches: map[string]PatchState{}}, nil
		}
		return Snapshot{}, fmt.Errorf("load state %q: %w", path, err)
	}
	if snap.Patches == nil {
		snap.Patches = map[string]PatchState{}
	}
	fillSynthDefaults(md, &snap)
	fillChainDefaults(md, &snap)
	return snap, nil
}

// fillChainDefaults backfills the post-synth chain knobs whose TOML keys
// are absent — the backward-compat path for state.toml files written
// before the chain params existed, and for hand-written partial blocks.
// Only the leaves whose default is NON-ZERO need filling: an absent key
// otherwise decodes to the Go zero value, which IS the registry default
// for every mix/depth/feedback (0) and for drive_pedal. The three
// rate/time params (chorus_rate_hz 0.8, tremolo_rate_hz 4, delay_time_ms
// 300) and the four enable flags (absent == enabled) do need it. Values
// mirror controls.chain.go and Defaults() — keep the three in lockstep.
func fillChainDefaults(md toml.MetaData, snap *Snapshot) {
	for name, p := range snap.Patches {
		defined := func(key string) bool {
			return md.IsDefined("patches", name, key)
		}
		if !defined("chorus_rate_hz") {
			p.ChorusRateHz = 0.8
		}
		if !defined("tremolo_rate_hz") {
			p.TremoloRateHz = 4
		}
		if !defined("delay_time_ms") {
			p.DelayTimeMs = 300
		}
		if !defined("drive_enabled") {
			p.DriveEnabled = true
		}
		if !defined("chorus_enabled") {
			p.ChorusEnabled = true
		}
		if !defined("tremolo_enabled") {
			p.TremoloEnabled = true
		}
		if !defined("delay_enabled") {
			p.DelayEnabled = true
		}
		snap.Patches[name] = p
	}
}

// knownChainStage reports whether id is a valid reorderable FX stage id —
// the set SetPedalOrder validates against. Duplicated here (rather than
// imported from controls) so state stays import-free of controls;
// internal/controls/chain.go fxOrderStages is the authority, and these six
// ids must stay in lockstep with it.
func knownChainStage(id string) bool {
	switch id {
	case "drive", "chorus", "tremolo", "delay", "comp", "reverb":
		return true
	}
	return false
}

// fillSynthDefaults backfills ENGINE defaults into synth blocks whose
// TOML keys are absent — the backward-compat path for state.toml files
// written before newer params existed, and for hand-written partial
// blocks. EVERY field whose engine default is non-zero is filled, Phase
// 2 (resonance, filter_env times/levels, the oscillator bank) and Phase
// 3/4 (amp_env, pulse_width, vel_routing.to_amp, lfo, bend_range,
// voice_mode) alike; an absent key otherwise decodes to the Go zero
// value, which IS the engine default only for drive, kbd_track, glide,
// noise, filter_env.amount, vel_routing.to_cutoff, the LFO depths, and
// oversample. Per-leaf on purpose: a hand-edited file carrying a
// partial [synth.lfo] table must keep its explicit values while the
// missing siblings default sanely.
// The values mirror audio-core (synth/mod.rs, synth/voice.rs,
// synth/lfo.rs) and controls.defaultSynth — keep all three in lockstep.
func fillSynthDefaults(md toml.MetaData, snap *Snapshot) {
	for name, p := range snap.Patches {
		if p.Synth == nil {
			continue
		}
		defined := func(key ...string) bool {
			return md.IsDefined(append([]string{"patches", name, "synth"}, key...)...)
		}
		s := p.Synth
		if !defined("resonance") {
			s.Resonance = 0.3
		}
		if !defined("filter_env", "attack") {
			s.FilterEnv.Attack = 0.005
		}
		if !defined("filter_env", "decay") {
			s.FilterEnv.Decay = 0.6
		}
		if !defined("filter_env", "sustain") {
			s.FilterEnv.Sustain = 0.4
		}
		if !defined("filter_env", "release") {
			s.FilterEnv.Release = 0.6
		}
		// filter_env.amount defaults to 0 — absent already decodes right.
		fillOscDefaults(md, name, s)
		if !defined("amp_env", "attack") {
			s.AmpEnv.Attack = 0.005
		}
		if !defined("amp_env", "decay") {
			s.AmpEnv.Decay = 0.2
		}
		if !defined("amp_env", "sustain") {
			s.AmpEnv.Sustain = 0.7
		}
		if !defined("amp_env", "release") {
			s.AmpEnv.Release = 0.4
		}
		if !defined("pulse_width") {
			s.PulseWidth = 0.25
		}
		if !defined("vel_routing", "to_amp") {
			s.VelRouting.ToAmp = 1
		}
		if !defined("lfo", "wave") {
			s.LFO.Wave = "triangle"
		}
		if !defined("lfo", "rate_hz") {
			s.LFO.RateHz = 5
		}
		if !defined("bend_range") {
			s.BendRange = 2
		}
		if !defined("voice_mode") {
			s.VoiceMode = "mono_legato"
		}
	}
}

// defaultOscs is the engine's oscillator bank (controls.defaultSynth /
// audio-core oscillator.rs default_bank()): osc 1 sounding, oscs 2/3
// pre-dialed Moog-ish but silent.
func defaultOscs() [3]OscState {
	return [3]OscState{
		{Wave: "saw", Octave: 0, DetuneCents: 0, Level: 1.0},
		{Wave: "saw", Octave: 0, DetuneCents: -7, Level: 0},
		{Wave: "saw", Octave: -1, DetuneCents: 5, Level: 0},
	}
}

// fillOscDefaults backfills the oscillator bank for one patch's synth
// block. Three cases:
//
//   - no oscs key at all (a partial hand-written synth block): the whole
//     default bank — decode left it zero-filled (wave "", osc1 level 0),
//     which would be an invalid, silent voice.
//   - the standard [[...oscs]] array-of-tables form (what the store's
//     encoder writes): per-leaf backfill, attributing keys to elements
//     via oscDefinedKeys.
//   - the inline `oscs = [{...}, ...]` form: BurntSushi's metadata
//     flattens all element keys under one header, so per-element
//     attribution is impossible. Backfill only wave — "" is never a
//     valid wave, so it can only mean "absent" — and keep every numeric
//     leaf as decoded (an explicit 0 must survive).
func fillOscDefaults(md toml.MetaData, patch string, s *SynthState) {
	defaults := defaultOscs()
	if !md.IsDefined("patches", patch, "synth", "oscs") {
		s.Oscs = defaults
		return
	}
	perElem, ok := oscDefinedKeys(md, patch)
	if !ok {
		for i := range s.Oscs {
			if s.Oscs[i].Wave == "" {
				s.Oscs[i].Wave = defaults[i].Wave
			}
		}
		return
	}
	for i := range s.Oscs {
		if !perElem[i]["wave"] {
			s.Oscs[i].Wave = defaults[i].Wave
		}
		if !perElem[i]["octave"] {
			s.Oscs[i].Octave = defaults[i].Octave
		}
		if !perElem[i]["detune_cents"] {
			s.Oscs[i].DetuneCents = defaults[i].DetuneCents
		}
		if !perElem[i]["level"] {
			s.Oscs[i].Level = defaults[i].Level
		}
	}
}

// oscDefinedKeys attributes [[patches.<patch>.synth.oscs]] leaf keys to
// their array element. MetaData.IsDefined cannot see inside arrays of
// tables, but MetaData.Keys() emits each [[...]] element as its header
// key followed by that element's own leaves, in file order — so counting
// header occurrences recovers the element index. ok is false when the
// array wasn't written as exactly three [[...]] tables (e.g. the inline
// array form emits ONE header with all leaves flattened after it), i.e.
// per-element attribution is impossible; the decoder has already
// enforced the array length, so a well-formed array-of-tables file
// always yields ok — callers fall back to value-based filling otherwise.
func oscDefinedKeys(md toml.MetaData, patch string) (defined [3]map[string]bool, ok bool) {
	header := toml.Key{"patches", patch, "synth", "oscs"}
	idx := -1
	for _, k := range md.Keys() {
		switch {
		case keyEqual(k, header):
			idx++
			if idx >= len(defined) {
				return defined, false
			}
			defined[idx] = map[string]bool{}
		case len(k) == len(header)+1 && keyEqual(k[:len(header)], header):
			if idx < 0 {
				return defined, false
			}
			defined[idx][k[len(header)]] = true
		}
	}
	return defined, idx == len(defined)-1
}

func keyEqual(a, b toml.Key) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// NewStore constructs a Store with the given initial snapshot.
func NewStore(path string, debounce time.Duration, logger *slog.Logger, initial Snapshot) *Store {
	if initial.Patches == nil {
		initial.Patches = map[string]PatchState{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Store{
		path:     path,
		debounce: debounce,
		logger:   logger,
		snap:     initial,
		wake:     make(chan struct{}, 1),
	}
}

// clonePatches deep-copies a patches map. Snapshot() hands copies to
// callers and flush() encodes outside the lock, so a shared *SynthState
// pointer would let one side observe the other's edits mid-write.
func clonePatches(in map[string]PatchState) map[string]PatchState {
	out := make(map[string]PatchState, len(in))
	for k, v := range in {
		if v.Synth != nil {
			syn := *v.Synth
			v.Synth = &syn
		}
		out[k] = v
	}
	return out
}

// Snapshot returns a deep copy of the current in-memory snapshot.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	return Snapshot{
		CurrentPatch: s.snap.CurrentPatch,
		Patches:      clonePatches(s.snap.Patches),
		PedalOrder:   clonePedalOrder(s.snap.PedalOrder),
		Macros:       cloneMacros(s.snap.Macros),
	}
}

// clonePedalOrder copies the global pedal-order slice so callers (and
// the flush encoder) never share the store's backing array.
func clonePedalOrder(in []string) []string {
	if len(in) == 0 {
		return nil
	}
	return append([]string(nil), in...)
}

// cloneMacros copies the global macro-assignment slice so callers (and
// the flush encoder) never share the store's backing array. Macro is all
// value types, so a shallow slice copy is a full clone.
func cloneMacros(in []Macro) []Macro {
	if len(in) == 0 {
		return nil
	}
	return append([]Macro(nil), in...)
}

// PedalOrder returns a copy of the global chain display order, or nil
// when the user has never reordered (callers fall back to the registry's
// canonical order).
func (s *Store) PedalOrder() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return clonePedalOrder(s.snap.PedalOrder)
}

// SetPedalOrder replaces the global FX chain order and schedules a debounced
// write. order must contain only known stage ids
// ("drive"/"chorus"/"tremolo"/"delay"/"comp"/"reverb") with no duplicates; an
// invalid order is rejected with an error and nothing is stored. controls
// pushes this order to the engine, so it is audible (see Snapshot.PedalOrder).
func (s *Store) SetPedalOrder(order []string) error {
	seen := make(map[string]bool, len(order))
	for _, id := range order {
		if !knownChainStage(id) {
			return fmt.Errorf("unknown pedal stage %q", id)
		}
		if seen[id] {
			return fmt.Errorf("duplicate pedal stage %q", id)
		}
		seen[id] = true
	}
	s.mu.Lock()
	s.snap.PedalOrder = clonePedalOrder(order)
	s.dirty = true
	s.mu.Unlock()
	s.signalWake()
	return nil
}

// Macros returns a copy of the global macro-slot assignments, or nil when
// the user has never assigned one.
func (s *Store) Macros() []Macro {
	s.mu.Lock()
	defer s.mu.Unlock()
	return cloneMacros(s.snap.Macros)
}

// SetMacros replaces the global macro-slot assignments and schedules a
// debounced write. Mirrors SetPedalOrder minus the validation, which
// lives in controls (the store persists whatever assignments controls has
// already vetted). The error return is always nil — it exists so the type
// satisfies the same StateStore seam SetPedalOrder does.
func (s *Store) SetMacros(m []Macro) error {
	s.mu.Lock()
	s.snap.Macros = cloneMacros(m)
	s.dirty = true
	s.mu.Unlock()
	s.signalWake()
	return nil
}

// PatchKnob returns the stored Knob for patchName, or Defaults() if absent.
func (s *Store) PatchKnob(patchName string) Knob {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.snap.Patches[patchName]; ok {
		return p.Knob
	}
	return Defaults()
}

// UpdatePatchKnob sets one post-synth knob for patchName. field is
// case-sensitive and matches the state-key column of the chain registry
// (internal/controls/chain.go): "volume", "reverb", "compressor",
// "drive_pedal", or one of the chain params ("chorus_rate_hz",
// "chorus_depth", "chorus_mix", "tremolo_rate_hz", "tremolo_depth",
// "delay_time_ms", "delay_feedback", "delay_mix"). The patch's synth
// block (if any) and per-stage enables are untouched.
func (s *Store) UpdatePatchKnob(patchName string, field string, value float32) {
	s.mu.Lock()
	p, ok := s.snap.Patches[patchName]
	if !ok {
		p = PatchState{Knob: Defaults()}
	}
	switch field {
	case "volume":
		p.Volume = value
	case "reverb":
		p.Reverb = value
	case "compressor":
		p.Compressor = value
	case "drive_pedal":
		p.DrivePedal = value
	case "chorus_rate_hz":
		p.ChorusRateHz = value
	case "chorus_depth":
		p.ChorusDepth = value
	case "chorus_mix":
		p.ChorusMix = value
	case "tremolo_rate_hz":
		p.TremoloRateHz = value
	case "tremolo_depth":
		p.TremoloDepth = value
	case "delay_time_ms":
		p.DelayTimeMs = value
	case "delay_feedback":
		p.DelayFeedback = value
	case "delay_mix":
		p.DelayMix = value
	default:
		s.mu.Unlock()
		s.logger.Debug("invalid knob field", "field", field)
		return
	}
	s.snap.Patches[patchName] = p
	s.dirty = true
	s.mu.Unlock()
	s.signalWake()
}

// UpdatePatchEnable sets one chain stage's enable flag for patchName.
// stage is one of "drive", "chorus", "tremolo", "delay". The patch's
// knob values and synth block are untouched. A never-seen patch gets
// Defaults() first so the entry is complete on disk.
func (s *Store) UpdatePatchEnable(patchName, stage string, on bool) {
	s.mu.Lock()
	p, ok := s.snap.Patches[patchName]
	if !ok {
		p = PatchState{Knob: Defaults()}
	}
	switch stage {
	case "drive":
		p.DriveEnabled = on
	case "chorus":
		p.ChorusEnabled = on
	case "tremolo":
		p.TremoloEnabled = on
	case "delay":
		p.DelayEnabled = on
	default:
		s.mu.Unlock()
		s.logger.Debug("invalid enable stage", "stage", stage)
		return
	}
	s.snap.Patches[patchName] = p
	s.dirty = true
	s.mu.Unlock()
	s.signalWake()
}

// PatchSynth returns the stored native-synth block for patchName. ok is
// false when the patch has no synth table (soundfont patches, or a
// native patch that has never been tweaked) — the caller decides the
// fallback (controls seeds its factory defaults), because "absent" and
// "all-zero" must stay distinguishable.
func (s *Store) PatchSynth(patchName string) (SynthState, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if p, ok := s.snap.Patches[patchName]; ok && p.Synth != nil {
		return *p.Synth, true
	}
	return SynthState{}, false
}

// UpdatePatchSynth replaces patchName's persisted synth block and
// schedules a debounced write (same coalescing as the knob path — a
// knob-page sweep is one disk flush). The knob block is untouched; a
// never-seen patch gets Defaults() knobs so the entry is complete on
// disk. syn is stored by value — later caller-side mutation of the
// argument cannot alter the store.
func (s *Store) UpdatePatchSynth(patchName string, syn SynthState) {
	s.mu.Lock()
	p, ok := s.snap.Patches[patchName]
	if !ok {
		p = PatchState{Knob: Defaults()}
	}
	p.Synth = &syn
	s.snap.Patches[patchName] = p
	s.dirty = true
	s.mu.Unlock()
	s.signalWake()
}

// SetCurrentPatch records the active patch name and schedules a debounced write.
func (s *Store) SetCurrentPatch(patchName string) {
	s.mu.Lock()
	if s.snap.CurrentPatch == patchName {
		s.mu.Unlock()
		return
	}
	s.snap.CurrentPatch = patchName
	s.dirty = true
	s.mu.Unlock()
	s.signalWake()
}

// Run is the debounced write loop. Blocks until ctx is done.
func (s *Store) Run(ctx context.Context) error {
	timer := time.NewTimer(s.debounce)
	if !timer.Stop() {
		<-timer.C
	}
	timerActive := false

	for {
		select {
		case <-ctx.Done():
			if timerActive {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
			if err := s.flush(); err != nil {
				s.logger.Warn("state final flush", "path", s.path, "err", err)
			}
			return nil
		case <-s.wake:
			if timerActive {
				if !timer.Stop() {
					select {
					case <-timer.C:
					default:
					}
				}
			}
			timer.Reset(s.debounce)
			timerActive = true
		case <-timer.C:
			timerActive = false
			if err := s.flush(); err != nil {
				s.logger.Warn("state flush", "path", s.path, "err", err)
			}
		}
	}
}

func (s *Store) signalWake() {
	select {
	case s.wake <- struct{}{}:
	default:
	}
}

func (s *Store) flush() error {
	s.mu.Lock()
	if !s.dirty {
		s.mu.Unlock()
		return nil
	}
	snap := Snapshot{
		CurrentPatch: s.snap.CurrentPatch,
		Patches:      clonePatches(s.snap.Patches),
		PedalOrder:   clonePedalOrder(s.snap.PedalOrder),
		Macros:       cloneMacros(s.snap.Macros),
	}
	s.dirty = false
	s.mu.Unlock()

	tmpPath := s.path + ".tmp"
	f, err := os.Create(tmpPath)
	if err != nil {
		return fmt.Errorf("state create temp %q: %w", tmpPath, err)
	}
	enc := toml.NewEncoder(f)
	if err := enc.Encode(&snap); err != nil {
		_ = f.Close()
		_ = os.Remove(tmpPath)
		return fmt.Errorf("state encode toml: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("state close temp %q: %w", tmpPath, err)
	}
	if err := os.Rename(tmpPath, s.path); err != nil {
		_ = os.Remove(tmpPath)
		return fmt.Errorf("state rename %q -> %q: %w", tmpPath, s.path, err)
	}
	return nil
}
