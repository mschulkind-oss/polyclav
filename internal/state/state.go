// Package state persists per-patch knob values (volume/reverb/compressor)
// and the active patch name to a TOML file. The Store debounces writes
// so rapid knob twists coalesce into a single disk flush, while a final
// synchronous flush on shutdown ensures the user's last edit survives.
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

// Snapshot is the file-level shape persisted to state.toml.
type Snapshot struct {
	CurrentPatch string          `toml:"current_patch"`
	Patches      map[string]Knob `toml:"patches"`
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
			return Snapshot{Patches: map[string]Knob{}}, nil
		}
		return Snapshot{}, fmt.Errorf("load state %q: %w", path, err)
	}
	if snap.Patches == nil {
		snap.Patches = map[string]Knob{}
	}
	return snap, nil
}

// NewStore constructs a Store with the given initial snapshot.
func NewStore(path string, debounce time.Duration, logger *slog.Logger, initial Snapshot) *Store {
	if initial.Patches == nil {
		initial.Patches = map[string]Knob{}
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

// Snapshot returns a deep copy of the current in-memory snapshot.
func (s *Store) Snapshot() Snapshot {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := Snapshot{
		CurrentPatch: s.snap.CurrentPatch,
		Patches:      make(map[string]Knob, len(s.snap.Patches)),
	}
	for k, v := range s.snap.Patches {
		out.Patches[k] = v
	}
	return out
}

// PatchKnob returns the stored Knob for patchName, or Defaults() if absent.
func (s *Store) PatchKnob(patchName string) Knob {
	s.mu.Lock()
	defer s.mu.Unlock()
	if k, ok := s.snap.Patches[patchName]; ok {
		return k
	}
	return Defaults()
}

// UpdatePatchKnob sets one of volume/reverb/compressor for patchName.
// field is case-sensitive: "volume", "reverb", or "compressor".
func (s *Store) UpdatePatchKnob(patchName string, field string, value float32) {
	s.mu.Lock()
	knob, ok := s.snap.Patches[patchName]
	if !ok {
		knob = Defaults()
	}
	switch field {
	case "volume":
		knob.Volume = value
	case "reverb":
		knob.Reverb = value
	case "compressor":
		knob.Compressor = value
	default:
		s.mu.Unlock()
		s.logger.Debug("invalid knob field", "field", field)
		return
	}
	s.snap.Patches[patchName] = knob
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
		Patches:      make(map[string]Knob, len(s.snap.Patches)),
	}
	for k, v := range s.snap.Patches {
		snap.Patches[k] = v
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
