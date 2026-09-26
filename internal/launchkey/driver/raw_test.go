package driver

import (
	"context"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/midi"
)

func TestHandleInboundRawSinkSeesUnmappedAftertouchBeforeParseDrop(t *testing.T) {
	d := &Driver{events: make(chan Event, 1), closed: make(chan struct{})}
	var got midi.RawEvent
	d.handleInbound(context.Background(), "Launchkey MK4 DAW", []byte{0xA0, 0x60, 0x7F}, func(ev midi.RawEvent) {
		got = ev
	})

	if got.Source != "launchkey-daw" || got.Port != "Launchkey MK4 DAW" || got.Kind != "poly-aftertouch" || got.Raw != "a0607f" {
		t.Fatalf("raw event = %+v", got)
	}
	select {
	case ev := <-d.events:
		t.Fatalf("unmapped aftertouch should not emit parsed launchkey event, got %#v", ev)
	default:
	}
}
