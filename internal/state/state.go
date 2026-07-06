// Package state persists per-patch knob values (volume/reverb/compressor),
// per-patch native-synth parameters, and the active patch name to a TOML
// file. The Store debounces writes so rapid knob twists coalesce into a
// single disk flush, while a final synchronous flush on shutdown ensures
// the user's last edit survives.
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

// Knob holds the three user-controllable DSP knob values for a patch.
// Values are 0..1 linear (matching audio.SetMasterVolume etc.).
type Knob struct {
	Volume     float32 `toml:"volume"`
	Reverb     float32 `toml:"reverb"`
	Compressor float32 `toml:"compressor"`
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

// Snapshot is the file-level shape persisted to state.toml.
type Snapshot struct {
	CurrentPatch string                `toml:"current_patch"`
	Patches      map[string]PatchState `toml:"patches"`
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
// seen before: full volume, no reverb, no compression.
func Defaults() Knob {
	return Knob{Volume: 1.0, Reverb: 0.0, Compressor: 0.0}
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
	return snap, nil
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
	}
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

// UpdatePatchKnob sets one of volume/reverb/compressor for patchName.
// field is case-sensitive: "volume", "reverb", or "compressor".
// The patch's synth block (if any) is untouched.
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
