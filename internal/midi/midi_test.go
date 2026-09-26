package midi

import (
	"testing"

	gomidi "gitlab.com/gomidi/midi/v2"
)

func TestParseVelocityZeroNoteOnIsNoteOff(t *testing.T) {
	ev, ok := parse(gomidi.NoteOn(3, 60, 0))
	if !ok {
		t.Fatal("parse returned ok=false")
	}
	if ev.Kind != NoteOff || ev.Channel != 3 || ev.Note != 60 {
		t.Fatalf("parse NoteOn velocity 0 = %+v, want channel 3 note-off 60", ev)
	}
}
