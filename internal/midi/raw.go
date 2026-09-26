package midi

import (
	"encoding/hex"
	"time"
)

// RawEvent is one inbound MIDI wire message observed by an existing daemon
// listener before the ordinary decoder has a chance to drop unsupported
// messages such as aftertouch. It is read-only debug data for SSE clients.
type RawEvent struct {
	Time    time.Time `json:"time"`
	Port    string    `json:"port"`
	Source  string    `json:"source"`
	Kind    string    `json:"kind"`
	Raw     string    `json:"raw"`
	Channel *int      `json:"channel,omitempty"`
	Data1   *int      `json:"data1,omitempty"`
	Data2   *int      `json:"data2,omitempty"`
	Bend    *int      `json:"bend,omitempty"`
}

// RawSink receives a copied raw inbound MIDI event. Implementations must be
// non-blocking; these callbacks run on MIDI listener goroutines.
type RawSink func(RawEvent)

// NewRawEvent decodes a copied raw MIDI message for debug display.
// Data returns a JSON-friendly map for controls.Change.Data.
func (ev RawEvent) Data() map[string]any {
	data := map[string]any{
		"time":   ev.Time,
		"port":   ev.Port,
		"source": ev.Source,
		"kind":   ev.Kind,
		"raw":    ev.Raw,
	}
	if ev.Channel != nil {
		data["channel"] = *ev.Channel
	}
	if ev.Data1 != nil {
		data["data1"] = *ev.Data1
	}
	if ev.Data2 != nil {
		data["data2"] = *ev.Data2
	}
	if ev.Bend != nil {
		data["bend"] = *ev.Bend
	}
	return data
}

func NewRawEvent(source, port string, raw []byte) RawEvent {
	cp := append([]byte(nil), raw...)
	ev := RawEvent{Time: time.Now(), Source: source, Port: port, Raw: hex.EncodeToString(cp)}
	if len(cp) == 0 {
		ev.Kind = "other"
		return ev
	}
	if cp[0] == 0xF0 {
		ev.Kind = "sysex"
		return ev
	}
	status := cp[0]
	if status < 0x80 {
		ev.Kind = "other"
		return ev
	}
	b := func(i int) int {
		if i < len(cp) {
			return int(cp[i])
		}
		return 0
	}
	ch := int(status & 0x0F)
	ev.Channel = &ch
	switch status & 0xF0 {
	case 0x80:
		ev.Kind = "note-off"
		d1, d2 := b(1), b(2)
		ev.Data1, ev.Data2 = &d1, &d2
	case 0x90:
		ev.Kind = "note-on"
		d1, d2 := b(1), b(2)
		ev.Data1, ev.Data2 = &d1, &d2
	case 0xA0:
		ev.Kind = "poly-aftertouch"
		d1, d2 := b(1), b(2)
		ev.Data1, ev.Data2 = &d1, &d2
	case 0xB0:
		ev.Kind = "cc"
		d1, d2 := b(1), b(2)
		ev.Data1, ev.Data2 = &d1, &d2
	case 0xC0:
		ev.Kind = "program-change"
		d1 := b(1)
		ev.Data1 = &d1
	case 0xD0:
		ev.Kind = "aftertouch"
		d1 := b(1)
		ev.Data1 = &d1
	case 0xE0:
		ev.Kind = "pitch-bend"
		bend := (b(1) | (b(2) << 7)) - 8192
		ev.Bend = &bend
	default:
		ev.Kind = "other"
		ev.Channel = nil
	}
	return ev
}
