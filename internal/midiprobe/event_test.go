package midiprobe

import (
	"encoding/json"
	"testing"
)

func TestHexBytesMarshalRoundTrip(t *testing.T) {
	orig := HexBytes{0xF0, 0x00, 0x20, 0x29, 0xF7}
	b, err := json.Marshal(orig)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `"f0002029f7"` {
		t.Errorf("marshaled = %s, want \"f0002029f7\"", b)
	}
	var s string
	if err := json.Unmarshal(b, &s); err != nil {
		t.Fatalf("unmarshal to string: %v", err)
	}
	if s != "f0002029f7" {
		t.Errorf("hex string = %q, want %q", s, "f0002029f7")
	}

	var back HexBytes
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if string(back) != string(orig) {
		t.Errorf("round trip = % x, want % x", back, orig)
	}
}

func TestHexBytesEmptyRoundTrip(t *testing.T) {
	var h HexBytes
	b, err := json.Marshal(h)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if string(b) != `""` {
		t.Errorf("empty HexBytes marshals to %s, want \"\"", b)
	}
	var back HexBytes
	if err := json.Unmarshal(b, &back); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if back != nil {
		t.Errorf("unmarshal of \"\" should leave nil, got % x", back)
	}
}

func TestDecodeNoteOnOff(t *testing.T) {
	on := decode([]byte{0x91, 60, 100}) // channel 1, note 60, vel 100
	if on.Kind != KindNoteOn {
		t.Fatalf("kind = %s, want note-on", on.Kind)
	}
	if on.Channel == nil || *on.Channel != 1 {
		t.Errorf("channel = %v, want 1", on.Channel)
	}
	if on.Data1 == nil || *on.Data1 != 60 {
		t.Errorf("data1 = %v, want 60", on.Data1)
	}
	if on.Data2 == nil || *on.Data2 != 100 {
		t.Errorf("data2 = %v, want 100", on.Data2)
	}

	off := decode([]byte{0x80, 60, 0})
	if off.Kind != KindNoteOff {
		t.Fatalf("kind = %s, want note-off", off.Kind)
	}

	// A note-on with velocity 0 is still reported as note-on (the classic
	// convention some devices use as a note-off shorthand) — decode does
	// not reinterpret it.
	onVel0 := decode([]byte{0x90, 60, 0})
	if onVel0.Kind != KindNoteOn {
		t.Errorf("note-on vel0 kind = %s, want note-on (no reinterpretation)", onVel0.Kind)
	}
}

func TestDecodeControlChange(t *testing.T) {
	ev := decode([]byte{0xB2, 74, 127})
	if ev.Kind != KindControlChange {
		t.Fatalf("kind = %s, want cc", ev.Kind)
	}
	if ev.Channel == nil || *ev.Channel != 2 {
		t.Errorf("channel = %v, want 2", ev.Channel)
	}
	if ev.Data1 == nil || *ev.Data1 != 74 {
		t.Errorf("cc# = %v, want 74", ev.Data1)
	}
	if ev.Data2 == nil || *ev.Data2 != 127 {
		t.Errorf("value = %v, want 127", ev.Data2)
	}
}

func TestDecodeProgramChangeAndAftertouch(t *testing.T) {
	pc := decode([]byte{0xC0, 5})
	if pc.Kind != KindProgramChange || pc.Data1 == nil || *pc.Data1 != 5 {
		t.Errorf("program change: kind=%s data1=%v", pc.Kind, pc.Data1)
	}
	if pc.Data2 != nil {
		t.Errorf("program change should have no data2, got %v", pc.Data2)
	}

	at := decode([]byte{0xD3, 64})
	if at.Kind != KindAftertouch || at.Channel == nil || *at.Channel != 3 || at.Data1 == nil || *at.Data1 != 64 {
		t.Errorf("aftertouch: kind=%s channel=%v data1=%v", at.Kind, at.Channel, at.Data1)
	}

	pat := decode([]byte{0xA0, 60, 90})
	if pat.Kind != KindPolyAftertouch || pat.Data1 == nil || *pat.Data1 != 60 || pat.Data2 == nil || *pat.Data2 != 90 {
		t.Errorf("poly aftertouch: kind=%s data1=%v data2=%v", pat.Kind, pat.Data1, pat.Data2)
	}
}

func TestDecodePitchBend(t *testing.T) {
	// Centre (8192): LSB=0x00, MSB=0x40 -> data = 0 | (0x40<<7) = 8192 -> bend 0
	center := decode([]byte{0xE0, 0x00, 0x40})
	if center.Kind != KindPitchBend || center.Bend == nil || *center.Bend != 0 {
		t.Errorf("center bend = %v, want 0", center.Bend)
	}
	// Min (0): LSB=0x00 MSB=0x00 -> data=0 -> bend = -8192
	min := decode([]byte{0xE1, 0x00, 0x00})
	if min.Bend == nil || *min.Bend != -8192 {
		t.Errorf("min bend = %v, want -8192", min.Bend)
	}
	// Max (16383): LSB=0x7F MSB=0x7F -> data=16383 -> bend = 8191
	max := decode([]byte{0xE1, 0x7F, 0x7F})
	if max.Bend == nil || *max.Bend != 8191 {
		t.Errorf("max bend = %v, want 8191", max.Bend)
	}
}

func TestDecodeSysEx(t *testing.T) {
	ev := decode([]byte{0xF0, 0x00, 0x20, 0x29, 0x01, 0xF7})
	if ev.Kind != KindSysEx {
		t.Fatalf("kind = %s, want sysex", ev.Kind)
	}
	if ev.Channel != nil || ev.Data1 != nil || ev.Data2 != nil || ev.Bend != nil {
		t.Errorf("sysex should have no decoded channel-voice fields")
	}
	if string(ev.Raw) != string([]byte{0xF0, 0x00, 0x20, 0x29, 0x01, 0xF7}) {
		t.Errorf("raw bytes not preserved verbatim")
	}
}

func TestDecodeOtherAndEmpty(t *testing.T) {
	if got := decode(nil).Kind; got != KindOther {
		t.Errorf("empty decode kind = %s, want other", got)
	}
	if got := decode([]byte{0xF8}).Kind; got != KindOther { // MIDI clock (realtime)
		t.Errorf("clock byte kind = %s, want other", got)
	}
	if got := decode([]byte{0x00}).Kind; got != KindOther { // stray data byte, no status
		t.Errorf("stray data byte kind = %s, want other", got)
	}
}

func TestDecodeShortMessageDoesNotPanic(t *testing.T) {
	// Truncated CC (missing value byte) must not panic or index out of range.
	ev := decode([]byte{0xB0, 74})
	if ev.Kind != KindControlChange {
		t.Fatalf("kind = %s, want cc", ev.Kind)
	}
	if ev.Data2 == nil || *ev.Data2 != 0 {
		t.Errorf("missing data2 should default to 0, got %v", ev.Data2)
	}
}
