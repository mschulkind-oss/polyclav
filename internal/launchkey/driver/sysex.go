// Package driver speaks to the Novation Launchkey 61 MK4 in DAW mode.
//
// Scope is real-time control: handshake, knob/fader/pad/transport event
// parsing, screen text, pad LEDs. Custom-mode upload is a separate
// workstream and lives in internal/launchkey/components.
package driver

// MK4 SysEx header used for every live-control payload (display + pad
// RGB). Spec: F0 00 20 29 02 14 ...
//
// We keep this as a package-private literal because the encoders that use
// it want to read against the Novation Launchkey MK4 Programmer's Reference,
// byte for byte.
var mk4SysExHeader = []byte{0xF0, 0x00, 0x20, 0x29, 0x02, 0x14}

// sysExEnd terminates every SysEx payload.
const sysExEnd byte = 0xF7

// sevenBit clamps an arbitrary 8-bit value into a valid SysEx data byte
// (0..127) by masking the high bit. MIDI data bytes must have bit 7 clear.
func sevenBit(v uint8) uint8 { return v & 0x7F }

// rgbToSysEx converts an 8-bit-per-channel host RGB triplet to the 7-bit
// values the MK4 pad-RGB SysEx accepts. The device takes 0..127 per
// channel; the natural mapping is v >> 1.
func rgbToSysEx(r, g, b uint8) (br, bg, bb byte) {
	return r >> 1, g >> 1, b >> 1
}

// asciiClean coerces a string to ASCII bytes safe for a display-text
// payload. Bytes outside 0x20..0x7D become 0x20 (space). The output is
// truncated to maxLen. The MK4 firmware shows garbage for non-printable
// bytes — keep it printable.
func asciiClean(s string, maxLen int) []byte {
	raw := []byte(s)
	if len(raw) > maxLen {
		raw = raw[:maxLen]
	}
	out := make([]byte, len(raw))
	for i, c := range raw {
		if c < 0x20 || c > 0x7D {
			out[i] = 0x20
		} else {
			out[i] = c
		}
	}
	return out
}
