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
//   - only the shipped params exist (no page/lfo/mod/amp_env/noise-color).
type SynthState struct {
	Resonance float32        `toml:"resonance"`
	FilterEnv FilterEnvState `toml:"filter_env"`
	Oscs      [3]OscState    `toml:"oscs"`
	Noise     float32        `toml:"noise"`
	Glide     float32        `toml:"glide"`
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
	_, err := toml.DecodeFile(path, &snap)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Snapshot{Patches: map[string]PatchState{}}, nil
		}
		return Snapshot{}, fmt.Errorf("load state %q: %w", path, err)
	}
	if snap.Patches == nil {
		snap.Patches = map[string]PatchState{}
	}
	return snap, nil
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
