// SMF user clips (docs/AUDITION.md "User clips (v1.5)", P3): any .mid
// file dropped in the user clips directory joins the registry next to
// the built-in patterns. Scope is deliberately notes-only v1: all
// tracks are merged onto one beat timeline and note on/off events are
// re-stamped to channel 0 (nothing downstream is multi-timbral — the
// doc's open question 3 says flatten and revisit); CC, pitch bend,
// sysex, and non-tempo meta messages are skipped. RefBPM comes from
// the file's FIRST tempo meta event (the SMF-implied 120 when absent);
// later tempo changes are ignored — a clip that needs a mid-file tempo
// map is sequencer territory, not audition material.
// (Package doc lives in player.go.)
package player

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"math"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"gitlab.com/gomidi/midi/v2/smf"

	"github.com/mschulkind-oss/polyclav/internal/midi"
)

// defaultSMFBPM is the tempo assumed for files with no tempo meta
// event — the SMF spec's implicit default.
const defaultSMFBPM = 120.0

// LoadUserClips scans dir (non-recursive) for *.mid / *.midi files and
// appends them to the clip registry: built-ins keep their positions,
// files follow sorted by name. A missing dir is not an error — a fresh
// install simply has no user clips. Files that fail to parse are
// logged (Warn) and skipped so one bad file can't take down the whole
// scan; loaded counts only the files that made it in. Duplicate clip
// IDs (e.g. foo.mid next to foo.midi) resolve last-wins with a Warn,
// keeping a single registry entry.
//
// Not goroutine-safe with concurrent Clips()/Play() callers: the
// registry is otherwise immutable after New, so call this during
// startup, before the Player is shared. (Play is excluded by the
// transport lock as a backstop, but Clips reads the registry unlocked
// by design — registration order is the contract, not synchronization.)
func (p *Player) LoadUserClips(dir string) (loaded int, err error) {
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("player: scan user clips: %w", err)
	}

	var names []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		switch strings.ToLower(filepath.Ext(e.Name())) {
		case ".mid", ".midi":
			names = append(names, e.Name())
		}
	}
	// ReadDir already sorts by filename, but the name order is part of
	// the registry contract (UIs index the clip list), so keep it
	// explicit rather than inherited.
	slices.Sort(names)

	p.transport.Lock()
	defer p.transport.Unlock()
	for _, name := range names {
		cd, err := loadSMFClip(filepath.Join(dir, name))
		if err != nil {
			p.logger.Warn("player: skipping user clip", "file", name, "err", err)
			continue
		}
		p.registerClip(cd)
		p.logger.Info("player: loaded user clip", "id", cd.info.ID, "beats", cd.info.Beats, "refBPM", cd.info.RefBPM)
		loaded++
	}
	return loaded, nil
}

// registerClip adds cd to the registry. On a duplicate ID the new data
// wins but the clip keeps its first-seen list position — replacing
// in place preserves the stable indexable order UIs depend on.
func (p *Player) registerClip(cd clipData) {
	if _, dup := p.byID[cd.info.ID]; dup {
		p.logger.Warn("player: duplicate clip id, last wins", "id", cd.info.ID, "name", cd.info.Name)
		for i, c := range p.clips {
			if c.ID == cd.info.ID {
				p.clips[i] = cd.info
				break
			}
		}
	} else {
		p.clips = append(p.clips, cd.info)
	}
	p.byID[cd.info.ID] = cd
}

// rejectSMPTE sniffs the SMF header's division field (bytes 12–13 of
// the MThd chunk; high bit set means SMPTE frames) BEFORE the file is
// handed to smf.ReadFile: the gomidi reader's tempo-map pass
// type-asserts MetricTicks unconditionally and panics on SMPTE files,
// so the rejection has to happen up front — and a purposeful error
// beats a recovered panic anyway. Anything that is not positively an
// SMPTE-timed MThd passes through so smf.ReadFile can report its own,
// more specific parse error.
func rejectSMPTE(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("open smf: %w", err)
	}
	defer f.Close()
	var hdr [14]byte
	if _, err := io.ReadFull(f, hdr[:]); err != nil {
		return nil
	}
	if string(hdr[:4]) != "MThd" {
		return nil
	}
	if hdr[12]&0x80 != 0 {
		return fmt.Errorf("SMPTE-timed SMF (%d fps) not supported; re-export with metric (ticks-per-quarter) timing", -int8(hdr[12]))
	}
	return nil
}

// loadSMFClip parses one SMF file into the player's clip form under
// the notes-only v1 rules described in the file header. SMPTE-timed
// files are rejected: their deltas are wall-clock frames with no
// ticks-per-quarter, so there is no honest way to place events on the
// beat timeline the scheduler tempo-scales.
func loadSMFClip(path string) (clipData, error) {
	if err := rejectSMPTE(path); err != nil {
		return clipData{}, err
	}
	s, err := smf.ReadFile(path)
	if err != nil {
		return clipData{}, fmt.Errorf("read smf: %w", err)
	}
	ticks, ok := s.TimeFormat.(smf.MetricTicks)
	if !ok {
		// Unreachable while rejectSMPTE holds, but a cheap guard against
		// the two checks drifting apart.
		return clipData{}, fmt.Errorf("unsupported SMF time format %s", s.TimeFormat)
	}
	// Resolution() maps the zero value to the format's 960 default, so
	// a header quirk can never divide by zero.
	tpq := float64(ticks.Resolution())

	bpm := defaultSMFBPM
	tempoTick := uint64(math.MaxUint64) // absolute tick of the earliest tempo event seen

	var evs []TimedEvent
	for _, tr := range s.Tracks {
		var abs uint64
		for _, ev := range tr {
			abs += uint64(ev.Delta)
			var ch, key, vel uint8
			var fileBPM float64
			switch {
			case ev.Message.GetNoteStart(&ch, &key, &vel):
				evs = append(evs, TimedEvent{Beat: float64(abs) / tpq, Ev: midi.Event{Kind: midi.NoteOn, Note: key, Vel: vel}})
			case ev.Message.GetNoteEnd(&ch, &key):
				// GetNoteEnd also matches NoteOn-velocity-0, the
				// running-status idiom many exporters use for releases.
				evs = append(evs, TimedEvent{Beat: float64(abs) / tpq, Ev: midi.Event{Kind: midi.NoteOff, Note: key}})
			case ev.Message.GetMetaTempo(&fileBPM):
				// Strictly earlier wins, so the FIRST event at the
				// earliest tick sticks even across merged tracks.
				if abs < tempoTick {
					bpm, tempoTick = fileBPM, abs
				}
			}
		}
	}
	sortEvents(evs)

	// Clip hygiene: pair off anything still sounding at EOF with a
	// synthesized NoteOff at the final beat. The player's loop-seam
	// safety net would catch these too, but a self-contained clip keeps
	// that net a no-op and the loop musically clean.
	held := map[byte]struct{}{}
	beats := 0.0
	for _, te := range evs {
		beats = te.Beat // evs is sorted, so this ends at the final beat
		switch te.Ev.Kind {
		case midi.NoteOn:
			held[te.Ev.Note] = struct{}{}
		case midi.NoteOff:
			delete(held, te.Ev.Note)
		}
	}
	for _, note := range slices.Sorted(maps.Keys(held)) {
		evs = append(evs, TimedEvent{Beat: beats, Ev: midi.Event{Kind: midi.NoteOff, Note: note}})
	}

	name := filepath.Base(path)
	return clipData{
		info: ClipInfo{
			ID:          "file:" + strings.TrimSuffix(name, filepath.Ext(name)),
			Name:        name,
			Description: "user clip",
			PolyOnly:    false,
			Beats:       math.Max(beats, 1), // a degenerate clip still needs a real loop length
			RefBPM:      bpm,
		},
		events: evs,
	}, nil
}
