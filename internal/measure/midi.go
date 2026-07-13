// Package measure provides offline, device-free tooling for rendering
// short performances through polyclav's patches and checking loudness
// invariants against the result: does patch A sound about as loud as
// patch B, does a parameter sweep change loudness gradually rather than
// jumping, does peak stay under a safety bound. It exists to nail down
// exactly the kind of regression the drive-pedal "1% is already
// maximally distorted" bug was — see docs/VISION.md.
//
// The package splits into three concerns, each independently useful:
//   - midi.go: parse a Standard MIDI File into frame-timed events.
//   - patch.go: render a patch against those events and measure it.
//   - checks.go: generic, rendering-agnostic invariant checks over a
//     slice of labeled measurements (consistency, gradualness, bounds).
package measure

import (
	"fmt"
	"sort"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/smf"

	"github.com/mschulkind-oss/polyclav/internal/audio"
)

// SampleRate matches audio-core's hardcoded engine rate
// (audio-core/src/lib.rs SAMPLE_RATE) — frame-offset math throughout
// this package assumes 48 kHz.
const SampleRate = 48_000

// LoadMIDIFile parses a Standard MIDI File at path into
// audio.OfflineMIDIEvent, merging all tracks and converting each
// event's tick position to an absolute 48 kHz frame offset via the
// file's own tempo map. Only NoteOn/NoteOff/ControlChange/PitchBend
// messages become events; meta events (track name, tempo, time
// signature, ...) are consumed for timing only and otherwise dropped.
//
// tailFrames is added after the last event's frame to give the final
// note room to release/decay before the render is measured — without
// it, a clip would cut off mid-envelope right at the last NoteOff.
// Returns the events (sorted by Frame ascending, satisfying
// RenderOfflineEvents' ordering requirement) and the total frame count
// a render needs to cover the whole file plus that tail.
func LoadMIDIFile(path string, tailFrames uint32) ([]audio.OfflineMIDIEvent, uint32, error) {
	s, err := smf.ReadFile(path)
	if err != nil {
		return nil, 0, fmt.Errorf("read midi file %q: %w", path, err)
	}

	type timedMsg struct {
		absTicks int64
		msg      smf.Message
	}
	var all []timedMsg
	for _, track := range s.Tracks {
		var absTicks int64
		for _, ev := range track {
			absTicks += int64(ev.Delta)
			all = append(all, timedMsg{absTicks: absTicks, msg: ev.Message})
		}
	}
	sort.Slice(all, func(i, j int) bool { return all[i].absTicks < all[j].absTicks })

	var events []audio.OfflineMIDIEvent
	var maxFrame uint32
	for _, tm := range all {
		frame := microsToFrame(s.TimeAt(tm.absTicks))
		m := midi.Message(tm.msg.Bytes())

		var channel, key, vel, cc, val uint8
		var bendRel int16
		var bendAbs uint16
		var ev audio.OfflineMIDIEvent
		var ok bool
		switch {
		case m.GetNoteStart(&channel, &key, &vel):
			ev, ok = audio.OfflineMIDIEvent{Frame: frame, Kind: audio.OfflineNoteOn, Channel: channel, Data1: key, Data2: uint16(vel)}, true
		case m.GetNoteEnd(&channel, &key):
			ev, ok = audio.OfflineMIDIEvent{Frame: frame, Kind: audio.OfflineNoteOff, Channel: channel, Data1: key}, true
		case m.GetControlChange(&channel, &cc, &val):
			ev, ok = audio.OfflineMIDIEvent{Frame: frame, Kind: audio.OfflineControlChange, Channel: channel, Data1: cc, Data2: uint16(val)}, true
		case m.GetPitchBend(&channel, &bendRel, &bendAbs):
			ev, ok = audio.OfflineMIDIEvent{Frame: frame, Kind: audio.OfflinePitchBend, Channel: channel, Data2: bendAbs}, true
		}
		if !ok {
			continue // meta event — already consumed for timing above
		}
		events = append(events, ev)
		if frame > maxFrame {
			maxFrame = frame
		}
	}

	return events, maxFrame + tailFrames, nil
}

// microsToFrame converts an absolute microsecond timestamp (as
// returned by smf.SMF.TimeAt) to a frame offset at SampleRate. Kept in
// int64 throughout — a multi-minute file's microsecond count times
// 48000 is still comfortably inside int64 range, but would overflow
// uint32 before the final divide.
func microsToFrame(micros int64) uint32 {
	return uint32(micros * SampleRate / 1_000_000)
}
