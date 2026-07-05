package player

import (
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/midi"
)

// TestPatternsWellFormed checks the invariants every pattern must hold:
// events sorted by beat, NoteOn/NoteOff strictly paired per note (no
// stuck or double-struck notes), velocities in 1..127, channel 0 only,
// and a Beats length covering the last event.
func TestPatternsWellFormed(t *testing.T) {
	for _, build := range builders {
		evs, info := build()
		t.Run(info.ID, func(t *testing.T) {
			if info.ID == "" || info.Name == "" || info.Description == "" {
				t.Fatalf("incomplete ClipInfo: %+v", info)
			}
			if info.RefBPM <= 0 {
				t.Fatalf("RefBPM = %v, want > 0", info.RefBPM)
			}
			if len(evs) == 0 {
				t.Fatal("pattern has no events")
			}

			held := map[byte]bool{}
			lastBeat := 0.0
			for i, te := range evs {
				if te.Beat < lastBeat {
					t.Fatalf("event %d at beat %v before previous beat %v — not sorted", i, te.Beat, lastBeat)
				}
				lastBeat = te.Beat
				if te.Ev.Channel != 0 {
					t.Fatalf("event %d on channel %d, want 0", i, te.Ev.Channel)
				}
				switch te.Ev.Kind {
				case midi.NoteOn:
					if te.Ev.Vel < 1 { // byte, so > 127 is impossible
						t.Fatalf("event %d NoteOn note %d vel %d, want 1..127", i, te.Ev.Note, te.Ev.Vel)
					}
					if held[te.Ev.Note] {
						t.Fatalf("event %d re-strikes note %d while it is held", i, te.Ev.Note)
					}
					held[te.Ev.Note] = true
				case midi.NoteOff:
					if !held[te.Ev.Note] {
						t.Fatalf("event %d NoteOff for note %d that is not held", i, te.Ev.Note)
					}
					delete(held, te.Ev.Note)
				default:
					t.Fatalf("event %d has kind %v, want NoteOn/NoteOff only", i, te.Ev.Kind)
				}
			}
			if len(held) != 0 {
				t.Fatalf("clip ends with held notes (stuck): %v", held)
			}
			if info.Beats < lastBeat {
				t.Fatalf("Beats = %v < last event beat %v", info.Beats, lastBeat)
			}
		})
	}
}

// TestVelRampCoverage: the ramp exists to sweep the velocity-curve
// input domain, so it must reach both extremes — something at or below
// 8 and the 127 ceiling — and stay on middle C.
func TestVelRampCoverage(t *testing.T) {
	evs, info := velRamp()
	if info.ID != "vel-ramp" {
		t.Fatalf("ID = %q, want vel-ramp", info.ID)
	}
	minVel, maxVel := byte(127), byte(0)
	for _, te := range evs {
		if te.Ev.Kind != midi.NoteOn {
			continue
		}
		if te.Ev.Note != 60 {
			t.Fatalf("vel-ramp plays note %d, want middle C (60) only", te.Ev.Note)
		}
		minVel = min(minVel, te.Ev.Vel)
		maxVel = max(maxVel, te.Ev.Vel)
	}
	if minVel > 8 {
		t.Errorf("min velocity = %d, want <= 8", minVel)
	}
	if maxVel != 127 {
		t.Errorf("max velocity = %d, want 127", maxVel)
	}
}

// TestClipCatalog pins the registration order and the metadata other
// components key off (PolyOnly labels in pickers, demo-button → clip ID
// mapping in the web UI).
func TestClipCatalog(t *testing.T) {
	p := New(nil, nil)
	clips := p.Clips()

	want := []struct {
		id       string
		polyOnly bool
	}{
		{"vel-ramp", false},
		{"sustain-chord", true},
		{"arp", false},
		{"bass-riff", false},
		{"chromatic", false},
		{"staccato", false},
		{"burst", true},
	}
	if len(clips) != len(want) {
		t.Fatalf("Clips() returned %d clips, want %d", len(clips), len(want))
	}
	for i, w := range want {
		if clips[i].ID != w.id {
			t.Errorf("clip %d ID = %q, want %q", i, clips[i].ID, w.id)
		}
		if clips[i].PolyOnly != w.polyOnly {
			t.Errorf("clip %q PolyOnly = %v, want %v", clips[i].ID, clips[i].PolyOnly, w.polyOnly)
		}
	}
}
