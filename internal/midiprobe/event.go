// Package midiprobe is a generic MIDI device reverse-engineering tool: it
// opens an arbitrary named MIDI in/out port pair, records every raw message
// (including SysEx) with decoded fields where possible, lets a user tag
// captured events with a label ("Knob 1"), sends a Universal Identity
// Request, and exports everything as a portable JSON "device profile".
//
// It is deliberately independent of internal/midi and internal/launchkey:
// those packages know the shape of a specific already-supported device
// (Launchkey port-role matching, the audio-facing note/cc/pitchbend-only
// event model); this package exists for devices nobody has written a driver
// for yet, so it makes no assumptions about port topology or message
// semantics beyond the MIDI spec itself.
package midiprobe

import (
	"encoding/hex"
	"encoding/json"
	"time"
)

// HexBytes marshals as a lowercase hex string (not Go's default base64)
// so raw SysEx/CC bytes are human-legible in the JSON export and in SSE
// payloads a non-technical user might eyeball.
type HexBytes []byte

func (h HexBytes) MarshalJSON() ([]byte, error) {
	return json.Marshal(hex.EncodeToString(h))
}

func (h *HexBytes) UnmarshalJSON(b []byte) error {
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		return err
	}
	if s == "" {
		*h = nil
		return nil
	}
	decoded, err := hex.DecodeString(s)
	if err != nil {
		return err
	}
	*h = decoded
	return nil
}

// EventKind classifies a decoded MIDI message.
type EventKind string

const (
	KindNoteOn         EventKind = "note-on"
	KindNoteOff        EventKind = "note-off"
	KindControlChange  EventKind = "cc"
	KindProgramChange  EventKind = "program-change"
	KindPitchBend      EventKind = "pitch-bend"
	KindAftertouch     EventKind = "aftertouch"      // channel pressure (0xD0)
	KindPolyAftertouch EventKind = "poly-aftertouch" // key pressure (0xA0)
	KindSysEx          EventKind = "sysex"
	KindOther          EventKind = "other" // realtime/system-common/unrecognized
)

// Event is one decoded MIDI message observed on a probe connection. Raw is
// always populated (even for kinds we can decode) so the UI can show exact
// bytes; the decoded fields are best-effort pointers, nil when not
// applicable to Kind (so JSON omits them via omitempty rather than
// emitting a misleading 0).
type Event struct {
	Seq     uint64    `json:"seq"` // monotonic within a Session
	Time    time.Time `json:"time"`
	Port    string    `json:"port"` // input port name this arrived on
	Kind    EventKind `json:"kind"`
	Raw     HexBytes  `json:"raw"`
	Channel *int      `json:"channel,omitempty"` // 0-15
	Data1   *int      `json:"data1,omitempty"`   // note/cc/program number
	Data2   *int      `json:"data2,omitempty"`   // velocity/value/pressure
	Bend    *int      `json:"bend,omitempty"`    // -8192..8191, pitch-bend only
	Label   string    `json:"label,omitempty"`   // set while a label-capture window is open
}

// decode parses a raw MIDI message into an Event (Seq/Time/Port/Label are
// filled in by the caller — decode only interprets the bytes themselves).
// Mirrors the status-nibble switch idiom of
// internal/launchkey/driver.parseMessage, but generic: no Launchkey-specific
// pad/knob/channel conventions.
func decode(raw []byte) Event {
	ev := Event{Raw: HexBytes(raw)}
	if len(raw) == 0 {
		ev.Kind = KindOther
		return ev
	}
	if raw[0] == 0xF0 {
		ev.Kind = KindSysEx
		return ev
	}
	status := raw[0]
	if status < 0x80 {
		ev.Kind = KindOther // stray data byte with no status — malformed/truncated
		return ev
	}
	b := func(i int) int {
		if i < len(raw) {
			return int(raw[i])
		}
		return 0
	}
	ch := int(status & 0x0F)
	switch status & 0xF0 {
	case 0x80:
		ev.Kind = KindNoteOff
		ev.Channel = &ch
		d1, d2 := b(1), b(2)
		ev.Data1, ev.Data2 = &d1, &d2
	case 0x90:
		ev.Kind = KindNoteOn // vel==0 still reported as NoteOn (classic note-on/vel0 convention)
		ev.Channel = &ch
		d1, d2 := b(1), b(2)
		ev.Data1, ev.Data2 = &d1, &d2
	case 0xA0:
		ev.Kind = KindPolyAftertouch
		ev.Channel = &ch
		d1, d2 := b(1), b(2)
		ev.Data1, ev.Data2 = &d1, &d2
	case 0xB0:
		ev.Kind = KindControlChange
		ev.Channel = &ch
		d1, d2 := b(1), b(2)
		ev.Data1, ev.Data2 = &d1, &d2
	case 0xC0:
		ev.Kind = KindProgramChange
		ev.Channel = &ch
		d1 := b(1)
		ev.Data1 = &d1
	case 0xD0:
		ev.Kind = KindAftertouch
		ev.Channel = &ch
		d1 := b(1)
		ev.Data1 = &d1
	case 0xE0:
		ev.Kind = KindPitchBend
		ev.Channel = &ch
		bend := (b(1) | (b(2) << 7)) - 8192
		ev.Bend = &bend
	default:
		// 0xF1-0xFF minus 0xF0: system-common/realtime bytes we don't
		// otherwise interpret (clock, active sense, etc.).
		ev.Kind = KindOther
	}
	return ev
}
