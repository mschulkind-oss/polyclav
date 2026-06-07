package components

import (
	"bytes"
	"testing"
)

func TestSysexLen(t *testing.T) {
	cases := []struct {
		ct   ControlType
		want int
	}{
		{ControlOff, 0},
		{ControlNote, 8},
		{ControlCC, 9},
		{ControlProgramChange, 9},
		{ControlRPN14, 8},
		{ControlNRPN14, 12},
		{ControlCC14, 12},
		{ControlPitchBend, 11},
		{ControlChAftertouch, 9},
		{ControlRPN, 8},
		{ControlNRPN, 11},
		{ControlHID, 7},
	}
	for _, c := range cases {
		if got := c.ct.SysexLen(); got != c.want {
			t.Errorf("%v.SysexLen() = %d, want %d", c.ct, got, c.want)
		}
	}
	// Out-of-range returns 0.
	if got := ControlType(13).SysexLen(); got != 0 {
		t.Errorf("ControlType(13).SysexLen() = %d, want 0", got)
	}
	if got := ControlType(255).SysexLen(); got != 0 {
		t.Errorf("ControlType(255).SysexLen() = %d, want 0", got)
	}
}

func TestModeIndex(t *testing.T) {
	cases := []struct {
		name    string
		surface SurfaceType
		slot    uint8
		want    uint8
	}{
		{"pads", SurfacePads, 0, 5},
		{"pads-7", SurfacePads, 7, 5},
		{"pots-0", SurfacePots, 0, 6},
		{"pots-3", SurfacePots, 3, 9},
		{"pots-4", SurfacePots, 4, 18},
		{"pots-7", SurfacePots, 7, 21},
		{"faders-0", SurfaceFaders, 0, 6},
		{"faders-3", SurfaceFaders, 3, 9},
		{"faders-4", SurfaceFaders, 4, 18},
		{"faders-7", SurfaceFaders, 7, 21},
		{"pedal", SurfacePedal, 0, 0},
		{"modwheel", SurfaceModWheel, 0, 0},
	}
	for _, c := range cases {
		m := &CustomMode{Surface: c.surface, Slot: c.slot}
		if got := m.ModeIndex(); got != c.want {
			t.Errorf("%s: ModeIndex() = %d, want %d", c.name, got, c.want)
		}
	}
}

func TestEncodeNameBytes(t *testing.T) {
	cases := []struct {
		name   string
		input  string
		maxLen int
		want   []byte
	}{
		{"printable", "MyPots", 16, []byte{'M', 'y', 'P', 'o', 't', 's'}},
		{"non-printable replaced with space", "A\x01B\x1FC\x7EZ", 16, []byte{'A', 0x20, 'B', 0x20, 'C', 0x20, 'Z'}},
		{"truncated", "ABCDEFGHIJ", 4, []byte{'A', 'B', 'C', 'D'}},
		{"empty", "", 8, []byte{}},
	}
	for _, c := range cases {
		got := encodeNameBytes(c.input, c.maxLen)
		if !bytes.Equal(got, c.want) {
			t.Errorf("%s: encodeNameBytes(%q, %d) = % X, want % X", c.name, c.input, c.maxLen, got, c.want)
		}
	}
}

func TestEncodeControlSpec_OFF(t *testing.T) {
	c := Control{Index: 56, Type: ControlOff}
	got, err := encodeControlSpec(&c)
	if err != nil {
		t.Fatalf("encodeControlSpec OFF: %v", err)
	}
	want := []byte{0x40, 0x38}
	if !bytes.Equal(got, want) {
		t.Errorf("OFF: got % X, want % X", got, want)
	}
}

func TestEncodeControlSpec_CC(t *testing.T) {
	c := Control{
		Index:       56,
		Type:        ControlCC,
		OffColor:    0,
		OnColor:     0x1A,
		Channel:     0,
		Behaviour:   BehaviourMomentary,
		Flags:       0,
		CCNumber:    1,
		TopValue:    127,
		BottomValue: 0,
		Sensitivity: 0,
	}
	got, err := encodeControlSpec(&c)
	if err != nil {
		t.Fatalf("encodeControlSpec CC: %v", err)
	}
	want := []byte{0x49, 0x38, 0x02, 0x00, 0x1A, 0x00, 0x00, 0x00, 0x01, 0x7F, 0x00}
	if !bytes.Equal(got, want) {
		t.Errorf("CC: got % X, want % X", got, want)
	}
}

func TestEncodeControlSpec_NOTE(t *testing.T) {
	c := Control{
		Index:     0,
		Type:      ControlNote,
		Channel:   0,
		Behaviour: BehaviourMomentary,
		Note:      60,
		Velocity:  127,
	}
	got, err := encodeControlSpec(&c)
	if err != nil {
		t.Fatalf("encodeControlSpec NOTE: %v", err)
	}
	// tag = 0x40|8 = 0x48; index = 0; type = 1; offColor=0; onColor=0;
	// ch|beh = 0; flags = 0; byte[7]=0; velocity=127; note=60.
	want := []byte{0x48, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x7F, 0x3C}
	if !bytes.Equal(got, want) {
		t.Errorf("NOTE: got % X, want % X", got, want)
	}
}

func TestEncodeControlSpec_ChannelAll(t *testing.T) {
	c := Control{
		Index:       40,
		Type:        ControlCC,
		OnColor:     0x10,
		Channel:     -1,
		CCNumber:    7,
		TopValue:    100,
		BottomValue: 0,
	}
	got, err := encodeControlSpec(&c)
	if err != nil {
		t.Fatalf("encodeControlSpec channel-all: %v", err)
	}
	// tag=0x49, idx=40, type=2 (CC), offColor=0, onColor=0x10, ch|beh = 1|0 = 1, flags = FlagChannelIsAll = 0x40.
	// Then CC payload: sens=0, cc=7, top=100, bottom=0.
	want := []byte{0x49, 40, 0x02, 0x00, 0x10, 0x01, byte(FlagChannelIsAll), 0x00, 0x07, 100, 0x00}
	if !bytes.Equal(got, want) {
		t.Errorf("channel-all: got % X, want % X", got, want)
	}
	// Sanity-check the flag bit explicitly.
	if got[6]&byte(FlagChannelIsAll) == 0 {
		t.Errorf("channel-all: FlagChannelIsAll not set in byte[6]=0x%02X", got[6])
	}
}

func TestEncodeMode_PotsSlot0_MyPots(t *testing.T) {
	m := &CustomMode{
		Surface: SurfacePots,
		Slot:    0,
		Name:    "MyPots",
		OnColor: 0x1A,
		Controls: []Control{
			{
				Index:       56,
				Type:        ControlCC,
				OffColor:    0,
				OnColor:     0x1A,
				Channel:     0,
				Behaviour:   BehaviourMomentary,
				CCNumber:    1,
				TopValue:    127,
				BottomValue: 0,
			},
		},
	}
	got, err := m.Encode(SysExProduct)
	if err != nil {
		t.Fatalf("Encode: %v", err)
	}

	// Header check.
	wantHeader := []byte{0xF0, 0x00, 0x20, 0x29, 0x02, 0x14, 0x05, 0x00, 0x45, 0x00, 0x06}
	if !bytes.Equal(got[:len(wantHeader)], wantHeader) {
		t.Errorf("header: got % X, want % X", got[:len(wantHeader)], wantHeader)
	}

	// Contains singleton TLVs in expected order/values.
	expectedSingletons := [][]byte{
		{0x00, 0x1A}, // onColor
		{0x01, 0x1A}, // externalColor falls back to OnColor
		{0x04, 0x40}, // modeFlags
		{0x07, 0x00}, // octaveBounds
	}
	for _, s := range expectedSingletons {
		if !bytes.Contains(got, s) {
			t.Errorf("missing singleton TLV % X in payload % X", s, got)
		}
	}

	// Name TLV.
	nameTLV := []byte{0x20, 0x06, 'M', 'y', 'P', 'o', 't', 's'}
	if !bytes.Contains(got, nameTLV) {
		t.Errorf("missing name TLV % X in payload % X", nameTLV, got)
	}

	// CC spec.
	ccSpec := []byte{0x49, 0x38, 0x02, 0x00, 0x1A, 0x00, 0x00, 0x00, 0x01, 0x7F, 0x00}
	if !bytes.Contains(got, ccSpec) {
		t.Errorf("missing CC spec % X in payload % X", ccSpec, got)
	}

	// Terminator.
	if got[len(got)-1] != 0xF7 {
		t.Errorf("terminator: got 0x%02X, want 0xF7", got[len(got)-1])
	}
}

func TestPaletteRGB(t *testing.T) {
	cases := []struct {
		c          Color
		r, g, b    uint8
		descriptor string
	}{
		{0, 79, 79, 79, "off"},
		{5, 255, 1, 0, "vibrant_red"},
		{13, 250, 255, 6, "vibrant_yellow"},
		{25, 75, 253, 88, "vibrant_green"},
		{41, 0, 152, 254, "vibrant_blue"},
		{49, 150, 53, 255, "vibrant_purple"},
		{109, 250, 255, 162, "pastel_yellow"},
		{8, 0, 0, 0, "unnamed → black"},
		{127, 0, 0, 0, "unnamed → black"},
	}
	for _, tc := range cases {
		r, g, b := PaletteRGB(tc.c)
		if r != tc.r || g != tc.g || b != tc.b {
			t.Errorf("PaletteRGB(%d) [%s]: got (%d,%d,%d), want (%d,%d,%d)",
				tc.c, tc.descriptor, r, g, b, tc.r, tc.g, tc.b)
		}
	}
}
