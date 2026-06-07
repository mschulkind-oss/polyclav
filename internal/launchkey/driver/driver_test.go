package driver

import (
	"bytes"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
)

var _ Event = KnobEvent{}
var _ Event = FaderEvent{}
var _ Event = FaderButtonEvent{}
var _ Event = PadEvent{}
var _ Event = TransportEvent{}

var _ components.Color = 0

func assertParseEvent(t *testing.T, msg []byte, want Event) {
	t.Helper()
	got, ok := parseMessage(msg)
	if !ok {
		t.Fatalf("parseMessage(% X): ok=false, want true", msg)
	}
	if got != want {
		t.Errorf("parseMessage(% X): got %#v, want %#v", msg, got, want)
	}
}

func TestParseKnobs(t *testing.T) {
	tests := []struct {
		name string
		msg  []byte
		want KnobEvent
	}{
		{"knob 1 +1", []byte{0xBF, 0x55, 0x41}, KnobEvent{Index: 1, Delta: +1}},
		{"knob 1 -1", []byte{0xBF, 0x55, 0x3F}, KnobEvent{Index: 1, Delta: -1}},
		{"knob 8 +2 fast turn", []byte{0xBF, 0x5C, 0x42}, KnobEvent{Index: 8, Delta: +2}},
		{"knob 4 -3 fast turn", []byte{0xBF, 0x58, 0x3D}, KnobEvent{Index: 4, Delta: -3}},
		{"knob 1 most negative", []byte{0xBF, 0x55, 0x01}, KnobEvent{Index: 1, Delta: -63}},
		{"knob 8 most positive", []byte{0xBF, 0x5C, 0x7F}, KnobEvent{Index: 8, Delta: +63}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertParseEvent(t, tt.msg, tt.want)
		})
	}
}

func TestParseKnobZeroIsNoOp(t *testing.T) {
	// CC value 0x40 (64) means "no change" in Relative mode (binary offset
	// around 64). The parser must treat it as a no-op (ok=false) so handlers
	// don't fire spurious zero-delta events.
	_, ok := parseMessage([]byte{0xBF, 0x55, 0x40})
	if ok {
		t.Errorf("parseMessage(BF 55 40): ok=true, want false (no-op)")
	}
}

func TestParseFaders(t *testing.T) {
	tests := []struct {
		name string
		msg  []byte
		want FaderEvent
	}{
		{"fader 1", []byte{0xBF, 5, 100}, FaderEvent{Index: 1, Value: 100}},
		{"fader 9 master", []byte{0xBF, 13, 64}, FaderEvent{Index: 9, Value: 64}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertParseEvent(t, tt.msg, tt.want)
		})
	}
}

func TestParseFaderButtons(t *testing.T) {
	tests := []struct {
		name string
		msg  []byte
		want FaderButtonEvent
	}{
		{"btn 1 pressed", []byte{0xBF, 37, 127}, FaderButtonEvent{Index: 1, Pressed: true}},
		{"btn 1 released", []byte{0xBF, 37, 0}, FaderButtonEvent{Index: 1, Pressed: false}},
		{"btn 8 pressed", []byte{0xBF, 44, 127}, FaderButtonEvent{Index: 8, Pressed: true}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertParseEvent(t, tt.msg, tt.want)
		})
	}
}

func TestParsePads(t *testing.T) {
	tests := []struct {
		name string
		msg  []byte
		want PadEvent
	}{
		{"top row 0 pressed", []byte{0x90, 96, 100}, PadEvent{Row: 0, Col: 0, Pressed: true, Velocity: 100}},
		{"top row 7 pressed", []byte{0x90, 103, 50}, PadEvent{Row: 0, Col: 7, Pressed: true, Velocity: 50}},
		{"bottom row 0 pressed", []byte{0x90, 112, 64}, PadEvent{Row: 1, Col: 0, Pressed: true, Velocity: 64}},
		{"bottom row 7 pressed", []byte{0x90, 119, 1}, PadEvent{Row: 1, Col: 7, Pressed: true, Velocity: 1}},
		{"note off", []byte{0x80, 96, 0}, PadEvent{Row: 0, Col: 0, Pressed: false, Velocity: 0}},
		{"note on vel 0", []byte{0x90, 96, 0}, PadEvent{Row: 0, Col: 0, Pressed: false, Velocity: 0}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertParseEvent(t, tt.msg, tt.want)
		})
	}
}

func TestParseTransport(t *testing.T) {
	tests := []struct {
		name   string
		note   byte
		button TransportButton
	}{
		{"Play", 0x73, TransportPlay},
		{"Stop", 0x74, TransportStop},
		{"Record", 0x75, TransportRecord},
		{"Loop", 0x76, TransportLoop},
		{"Rewind", 0x71, TransportRewind},
		{"FastForward", 0x72, TransportFastForward},
		{"TrackLeft", 0x66, TransportTrackLeft},
		{"TrackRight", 0x67, TransportTrackRight},
		{"SceneUp", 0x68, TransportSceneUp},
		{"SceneDown", 0x69, TransportSceneDown},
		{"Shift", 0x6A, TransportShift},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assertParseEvent(t, []byte{0x9F, tt.note, 0x7F}, TransportEvent{Button: tt.button, Pressed: true})
			assertParseEvent(t, []byte{0x9F, tt.note, 0x00}, TransportEvent{Button: tt.button, Pressed: false})
			assertParseEvent(t, []byte{0x8F, tt.note, 0x00}, TransportEvent{Button: tt.button, Pressed: false})
		})
	}
}

func TestParseIgnoresOtherMessages(t *testing.T) {
	tests := []struct {
		name string
		msg  []byte
	}{
		{"empty", []byte{}},
		{"too short", []byte{0xBF}},
		{"CC wrong channel", []byte{0xB0, 21, 64}},
		{"CC unknown", []byte{0xBF, 99, 64}},
		{"Note unknown channel", []byte{0x91, 60, 100}},
		{"Note unknown pad", []byte{0x90, 50, 100}},
		{"Note unknown transport", []byte{0x9F, 0x10, 0x7F}},
		{"Random status", []byte{0xF0, 0x00, 0x00}},
		{"Program change", []byte{0xCF, 5}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, ok := parseMessage(tt.msg)
			if ok {
				t.Errorf("parseMessage(% X): ok=true, want false", tt.msg)
			}
		})
	}
}

func TestPadNote(t *testing.T) {
	tests := []struct {
		name    string
		row     int
		col     int
		want    byte
		wantErr bool
	}{
		{"0,0", 0, 0, 96, false},
		{"0,7", 0, 7, 103, false},
		{"1,0", 1, 0, 112, false},
		{"1,7", 1, 7, 119, false},
		{"row 2", 2, 0, 0, true},
		{"col 8", 0, 8, 0, true},
		{"row -1", -1, 0, 0, true},
		{"col -1", 0, -1, 0, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := padNote(tt.row, tt.col)
			if (err != nil) != tt.wantErr {
				t.Errorf("padNote(%d, %d) error = %v, wantErr %v", tt.row, tt.col, err, tt.wantErr)
				return
			}
			if got != tt.want {
				t.Errorf("padNote(%d, %d) = %d, want %d", tt.row, tt.col, got, tt.want)
			}
		})
	}
}

func TestEncodePadRGB(t *testing.T) {
	got := encodePadRGB(96, 0x7F, 0x00, 0x40)
	want := []byte{0xF0, 0x00, 0x20, 0x29, 0x02, 0x14, 0x01, 0x43, 96, 0x7F, 0x00, 0x40, 0xF7}
	if !bytes.Equal(got, want) {
		t.Errorf("encodePadRGB: got % X, want % X", got, want)
	}
}

func TestEncodeConfigureTarget(t *testing.T) {
	t.Run("Stationary", func(t *testing.T) {
		got := encodeConfigureTarget(ScreenStationary, 1)
		want := []byte{0xF0, 0x00, 0x20, 0x29, 0x02, 0x14, 0x04, 0x20, 0x01, 0xF7}
		if !bytes.Equal(got, want) {
			t.Errorf("got % X, want % X", got, want)
		}
	})
	t.Run("DawPad", func(t *testing.T) {
		got := encodeConfigureTarget(ScreenDawPad, 1)
		want := []byte{0xF0, 0x00, 0x20, 0x29, 0x02, 0x14, 0x04, 0x22, 0x01, 0xF7}
		if !bytes.Equal(got, want) {
			t.Errorf("got % X, want % X", got, want)
		}
	})
}

func TestEncodeTextField(t *testing.T) {
	got := encodeTextField(ScreenStationary, 0, []byte("Hello"))
	want := []byte{0xF0, 0x00, 0x20, 0x29, 0x02, 0x14, 0x06, 0x20, 0x00, 'H', 'e', 'l', 'l', 'o', 0xF7}
	if !bytes.Equal(got, want) {
		t.Errorf("got % X, want % X", got, want)
	}
}

func TestEncodeStationary(t *testing.T) {
	got := encodeStationary([]byte("Line1"), []byte("Line2"))

	cfg := []byte{0xF0, 0x00, 0x20, 0x29, 0x02, 0x14, 0x04, 0x20, 0x01, 0xF7}
	f1 := []byte{0xF0, 0x00, 0x20, 0x29, 0x02, 0x14, 0x06, 0x20, 0x40, 'L', 'i', 'n', 'e', '1', 0xF7}
	f2 := []byte{0xF0, 0x00, 0x20, 0x29, 0x02, 0x14, 0x06, 0x20, 0x41, 'L', 'i', 'n', 'e', '2', 0xF7}

	want := append(append([]byte{}, cfg...), f1...)
	want = append(want, f2...)

	if !bytes.Equal(got, want) {
		t.Errorf("got % X, want % X", got, want)
	}
}

func TestAsciiClean(t *testing.T) {
	tests := []struct {
		name   string
		input  string
		maxLen int
		want   []byte
	}{
		{"simple", "Hello", 16, []byte("Hello")},
		{"non-printable", "A\x01B\x1FC\x7EZ", 16, []byte{'A', 0x20, 'B', 0x20, 'C', 0x20, 'Z'}},
		{"truncated", "ABCDEFGHIJ", 4, []byte("ABCD")},
		{"empty", "", 8, []byte{}},
		{"utf8", "Héllo", 16, []byte{'H', 0x20, 0x20, 'l', 'l', 'o'}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := asciiClean(tt.input, tt.maxLen)
			if !bytes.Equal(got, tt.want) {
				t.Errorf("asciiClean(%q, %d): got % X, want % X", tt.input, tt.maxLen, got, tt.want)
			}
		})
	}
}

func TestRgbToSysEx(t *testing.T) {
	tests := []struct {
		name                string
		r, g, b             uint8
		wantR, wantG, wantB byte
	}{
		{"mid", 0xFF, 0x00, 0x80, 0x7F, 0x00, 0x40},
		{"zero", 0, 0, 0, 0, 0, 0},
		{"max", 255, 255, 255, 127, 127, 127},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r, g, b := rgbToSysEx(tt.r, tt.g, tt.b)
			if r != tt.wantR || g != tt.wantG || b != tt.wantB {
				t.Errorf("rgbToSysEx(%d, %d, %d) = (%d, %d, %d), want (%d, %d, %d)",
					tt.r, tt.g, tt.b, r, g, b, tt.wantR, tt.wantG, tt.wantB)
			}
		})
	}
}

func TestSevenBit(t *testing.T) {
	tests := []struct {
		input uint8
		want  uint8
	}{
		{0xFF, 0x7F},
		{0x80, 0x00},
		{0x42, 0x42},
		{0x00, 0x00},
	}
	for _, tt := range tests {
		t.Run("", func(t *testing.T) {
			if got := sevenBit(tt.input); got != tt.want {
				t.Errorf("sevenBit(%d) = %d, want %d", tt.input, got, tt.want)
			}
		})
	}
}

func TestTransportFromNoteUnknown(t *testing.T) {
	_, ok := transportFromNote(0x00)
	if ok {
		t.Error("transportFromNote(0x00): ok=true, want false")
	}

	btn, ok := transportFromNote(0x73)
	if !ok {
		t.Fatal("transportFromNote(0x73): ok=false, want true")
	}
	if btn != TransportPlay {
		t.Errorf("transportFromNote(0x73): got %v, want TransportPlay", btn)
	}
}

func TestDisplayConfigCancelEncoder(t *testing.T) {
	if displayConfigCancel != 0x00 {
		t.Fatalf("displayConfigCancel = 0x%02X, want 0x00", displayConfigCancel)
	}

	// Byte sequence the driver.Open() encoder-cancel loop builds for
	// target 0x15 (encoder 1). Must match the literal SysEx we ship.
	got := append([]byte{}, mk4SysExHeader...)
	got = append(got, 0x04, 0x15, displayConfigCancel, sysExEnd)
	want := []byte{0xF0, 0x00, 0x20, 0x29, 0x02, 0x14, 0x04, 0x15, 0x00, 0xF7}
	if !bytes.Equal(got, want) {
		t.Errorf("encoder-cancel target 0x15: got % X, want % X", got, want)
	}

	// And via encodeConfigureTarget for the top of the encoder range.
	got2 := encodeConfigureTarget(ScreenTarget(0x1C), displayConfigCancel)
	want2 := []byte{0xF0, 0x00, 0x20, 0x29, 0x02, 0x14, 0x04, 0x1C, 0x00, 0xF7}
	if !bytes.Equal(got2, want2) {
		t.Errorf("encoder-cancel target 0x1C: got % X, want % X", got2, want2)
	}
}
