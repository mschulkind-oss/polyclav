package midiprobe

import "time"

// IdentityResult is the outcome of a Universal Non-realtime Identity
// Request/Reply exchange (MIDI Universal SysEx: F0 7E <ch> 06 01 F7
// request, F0 7E <ch> 06 02 <manufacturer...> F7 reply). Not every device
// implements this — a timeout is meaningful data, not an error.
type IdentityResult struct {
	RequestSentAt    time.Time `json:"requestSentAt"`
	ReplyRaw         HexBytes  `json:"replyRaw,omitempty"`
	ReceivedAt       time.Time `json:"receivedAt,omitempty"`
	ManufacturerID   HexBytes  `json:"manufacturerId,omitempty"`   // 1 or 3 bytes
	ManufacturerName string    `json:"manufacturerName,omitempty"` // "" if unknown — the raw bytes are still shown to the user
	FamilyCode       HexBytes  `json:"familyCode,omitempty"`
	ModelNumber      HexBytes  `json:"modelNumber,omitempty"`
	VersionBytes     HexBytes  `json:"versionBytes,omitempty"`
	TimedOut         bool      `json:"timedOut"`
}

// extendedManufacturers maps the 3-byte extended manufacturer ID (used
// when the single ID byte is 0x00) to a human name. gomidi/v2's own
// sysex.ManufacturerID table only models classic 1-byte IDs, so this is a
// small, necessarily-incomplete supplement covering the devices relevant
// here. Add to this as new devices are identified — an unknown ID is
// still reported as raw bytes, never guessed.
var extendedManufacturers = map[[3]byte]string{
	{0x00, 0x20, 0x29}: "Novation/Focusrite", // internal/launchkey/driver/sysex.go
	{0x00, 0x20, 0x6B}: "Arturia",
}

// isIdentityReply reports whether raw looks like a Universal Non-realtime
// Identity Reply: F0 7E <channel> 06 02 ... F7.
func isIdentityReply(raw []byte) bool {
	return len(raw) >= 6 &&
		raw[0] == 0xF0 &&
		raw[1] == 0x7E &&
		raw[3] == 0x06 &&
		raw[4] == 0x02
}

// decodeIdentityReply fills in result's manufacturer/family/model/version
// fields from a raw Identity Reply, handling both classic 1-byte and
// extended 3-byte manufacturer IDs. Safe against short/malformed replies —
// each field is only set if enough bytes are present.
func decodeIdentityReply(raw []byte, result *IdentityResult) {
	if !isIdentityReply(raw) {
		return
	}
	// raw: F0 7E ch 06 02 [manufacturer(1 or 3)] [family(2)] [model(2)] [version(4)] F7
	i := 5
	if i >= len(raw) {
		return
	}
	if raw[i] == 0x00 && i+2 < len(raw) {
		id := [3]byte{raw[i], raw[i+1], raw[i+2]}
		result.ManufacturerID = HexBytes(id[:])
		result.ManufacturerName = extendedManufacturers[id]
		i += 3
	} else {
		result.ManufacturerID = HexBytes(raw[i : i+1])
		i++
	}
	if end := i + 2; end <= len(raw) {
		result.FamilyCode = HexBytes(raw[i:end])
		i = end
	}
	if end := i + 2; end <= len(raw) {
		result.ModelNumber = HexBytes(raw[i:end])
		i = end
	}
	// Version is up to 4 bytes; some devices reply with fewer before the
	// terminating F7 — take whatever is left, excluding the trailing F7.
	if end := len(raw) - 1; i < end {
		result.VersionBytes = HexBytes(raw[i:end])
	}
}
