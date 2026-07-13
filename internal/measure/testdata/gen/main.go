// Command gen writes internal/measure/testdata/short_phrase.mid, the
// short test fixture the measure package's tests render through each
// patch. Not part of the normal build (testdata/ is always skipped by
// go build/vet/test) — run manually with `go run .` from this
// directory if the fixture ever needs regenerating.
//
// A simple 4-beat, 120 BPM arpeggio (C4-E4-G4-C5) on channel 0, plus a
// short two-note chord tail so the render exercises overlapping notes
// too — enough variety to be a meaningful "short performance" without
// needing anything elaborate.
package main

import (
	"log"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/smf"
)

func main() {
	clock := smf.MetricTicks(480)
	beat := clock.Ticks4th()

	var track smf.Track
	track.Add(0, smf.MetaTempo(120))
	track.Add(0, smf.MetaTrackSequenceName("short_phrase"))

	// Arpeggio: one note per beat.
	track.Add(0, midi.NoteOn(0, 60, 90))
	track.Add(beat, midi.NoteOff(0, 60))
	track.Add(0, midi.NoteOn(0, 64, 90))
	track.Add(beat, midi.NoteOff(0, 64))
	track.Add(0, midi.NoteOn(0, 67, 90))
	track.Add(beat, midi.NoteOff(0, 67))
	track.Add(0, midi.NoteOn(0, 72, 100))
	track.Add(beat, midi.NoteOff(0, 72))

	// A short overlapping two-note chord tail (C4+G4), so the fixture
	// also exercises simultaneous notes, not just a strict melody line.
	track.Add(0, midi.NoteOn(0, 60, 80))
	track.Add(0, midi.NoteOn(0, 67, 80))
	track.Add(beat*2, midi.NoteOff(0, 60))
	track.Add(0, midi.NoteOff(0, 67))

	track.Close(0)

	s := smf.New()
	s.TimeFormat = clock
	if err := s.Add(track); err != nil {
		log.Fatalf("add track: %v", err)
	}
	if err := s.WriteFile("../short_phrase.mid"); err != nil {
		log.Fatalf("write file: %v", err)
	}
}
