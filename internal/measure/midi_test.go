package measure

import (
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/audio"
)

func TestLoadMIDIFileParsesShortPhrase(t *testing.T) {
	events, totalFrames, err := LoadMIDIFile("testdata/short_phrase.mid", SampleRate/2)
	if err != nil {
		t.Fatalf("LoadMIDIFile: %v", err)
	}
	if len(events) == 0 {
		t.Fatal("expected at least one event")
	}

	// Events must be sorted by Frame ascending — RenderOfflineEvents'
	// documented requirement.
	for i := 1; i < len(events); i++ {
		if events[i].Frame < events[i-1].Frame {
			t.Fatalf("events not sorted: [%d].Frame=%d < [%d].Frame=%d",
				i, events[i].Frame, i-1, events[i-1].Frame)
		}
	}

	// The fixture's first event is the arpeggio's opening NoteOn
	// (middle C, velocity 90) at t=0.
	first := events[0]
	if first.Kind != audio.OfflineNoteOn || first.Frame != 0 || first.Data1 != 60 || first.Data2 != 90 {
		t.Errorf("unexpected first event: %+v", first)
	}

	// At 120 BPM, 480 ticks/quarter, one beat is exactly 0.5s = 24000
	// frames at 48kHz — the fixture's second event (NoteOff 60) should
	// land there.
	second := events[1]
	if second.Kind != audio.OfflineNoteOff || second.Data1 != 60 {
		t.Errorf("unexpected second event: %+v", second)
	}
	const wantFrame = SampleRate / 2
	if diff := int64(second.Frame) - wantFrame; diff < -10 || diff > 10 {
		t.Errorf("second event frame = %d, want close to %d (one beat at 120 BPM)", second.Frame, wantFrame)
	}

	// totalFrames must cover the last event plus the requested tail.
	lastFrame := events[len(events)-1].Frame
	if totalFrames < lastFrame+SampleRate/2 {
		t.Errorf("totalFrames=%d doesn't cover last event (%d) plus tail", totalFrames, lastFrame)
	}
}

func TestLoadMIDIFileMissingFile(t *testing.T) {
	_, _, err := LoadMIDIFile("testdata/does_not_exist.mid", 0)
	if err == nil {
		t.Fatal("expected an error for a missing file")
	}
}
