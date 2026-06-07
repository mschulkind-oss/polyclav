// TLV writer helpers for Novation Launchkey MK4 custom-mode SysEx payloads.
package components

func writeTLV1(b []byte, tag, value byte) []byte {
	return append(b, tag, value)
}

func writeTLVName(b []byte, name string) []byte {
	nameBytes := encodeNameBytes(name, 16)
	b = append(b, 0x20, byte(len(nameBytes)))
	b = append(b, nameBytes...)
	return b
}

func writeControlName(b []byte, controlIndex uint8, name string) []byte {
	nameBytes := encodeNameBytes(name, 8)
	tag := byte(0x60) | byte(len(nameBytes))
	b = append(b, tag, controlIndex)
	b = append(b, nameBytes...)
	return b
}

func encodeNameBytes(name string, maxLen int) []byte {
	raw := []byte(name)
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
