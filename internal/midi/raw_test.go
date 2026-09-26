package midi

import "testing"

func TestNewRawEventDecodesAftertouchAndCopiesIdentity(t *testing.T) {
	raw := []byte{0xD2, 0x45}
	ev := NewRawEvent("performance", "Launchkey MK4 MIDI", raw)
	raw[1] = 0x00

	if ev.Source != "performance" || ev.Port != "Launchkey MK4 MIDI" {
		t.Fatalf("identity = source %q port %q", ev.Source, ev.Port)
	}
	if ev.Kind != "aftertouch" || ev.Raw != "d245" {
		t.Fatalf("decode = kind %q raw %q", ev.Kind, ev.Raw)
	}
	if ev.Channel == nil || *ev.Channel != 2 || ev.Data1 == nil || *ev.Data1 != 0x45 {
		t.Fatalf("decoded fields = channel %v data1 %v", ev.Channel, ev.Data1)
	}
}

func TestNewRawEventDecodesPolyAftertouch(t *testing.T) {
	ev := NewRawEvent("launchkey-daw", "Launchkey MK4 DAW", []byte{0xA0, 0x60, 0x7F})
	if ev.Kind != "poly-aftertouch" {
		t.Fatalf("kind = %q, want poly-aftertouch", ev.Kind)
	}
	if ev.Channel == nil || *ev.Channel != 0 || ev.Data1 == nil || *ev.Data1 != 0x60 || ev.Data2 == nil || *ev.Data2 != 0x7F {
		t.Fatalf("decoded fields = channel %v data1 %v data2 %v", ev.Channel, ev.Data1, ev.Data2)
	}
}
