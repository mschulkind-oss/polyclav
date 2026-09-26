package osc

import (
	"io"
	"log/slog"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/midi"
)

type captureSender struct{ calls []oscCall }
type oscCall struct {
	addr string
	args []any
}

func (c *captureSender) Send(addr string, args ...any) error {
	c.calls = append(c.calls, oscCall{addr: addr, args: args})
	return nil
}

func TestMapperUsesOneBasedConfigChannelsForZeroBasedMIDIEvents(t *testing.T) {
	s := &captureSender{}
	m := NewMapper(s, slog.New(slog.NewTextHandler(io.Discard, nil)), []Binding{{SourceKind: "cc", Channel: 16, Controller: 13, OSC: "/lr/mix/fader", Transform: "scalar"}})
	m.Dispatch(midi.Event{Kind: midi.ControlChange, Channel: 15, CC: 13, Value: 64})
	if len(s.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(s.calls))
	}
	if s.calls[0].addr != "/lr/mix/fader" {
		t.Fatalf("addr = %q", s.calls[0].addr)
	}
	if got, want := s.calls[0].args[0].(float32), float32(64)/127; got != want {
		t.Fatalf("value = %v, want %v", got, want)
	}
}
