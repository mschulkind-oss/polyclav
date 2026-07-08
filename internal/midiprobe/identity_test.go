package midiprobe

import "testing"

func TestIsIdentityReply(t *testing.T) {
	valid := []byte{0xF0, 0x7E, 0x7F, 0x06, 0x02, 0x00, 0x20, 0x6B, 0x01, 0x02, 0x00, 0x01, 0x00, 0x00, 0xF7}
	if !isIdentityReply(valid) {
		t.Error("expected valid identity reply to be recognized")
	}
	if isIdentityReply([]byte{0xF0, 0x00, 0x20, 0x29, 0xF7}) {
		t.Error("a non-identity sysex must not be recognized as an identity reply")
	}
	if isIdentityReply([]byte{0xF0, 0x7E, 0x7F, 0x06, 0x01, 0xF7}) { // SubID2=0x01 is the REQUEST, not the reply
		t.Error("an identity REQUEST must not be recognized as a reply")
	}
	if isIdentityReply([]byte{0xF0, 0x7E}) {
		t.Error("a short buffer must not panic or be misrecognized")
	}
}

func TestDecodeIdentityReplyExtendedManufacturer(t *testing.T) {
	// Arturia (extended 00 20 6B), family 0x01 0x02, model 0x00 0x01, version 4 bytes.
	raw := []byte{0xF0, 0x7E, 0x7F, 0x06, 0x02, 0x00, 0x20, 0x6B, 0x01, 0x02, 0x00, 0x01, 0x01, 0x00, 0x00, 0x00, 0xF7}
	var result IdentityResult
	decodeIdentityReply(raw, &result)

	if result.ManufacturerName != "Arturia" {
		t.Errorf("manufacturer name = %q, want Arturia", result.ManufacturerName)
	}
	if string(result.ManufacturerID) != string([]byte{0x00, 0x20, 0x6B}) {
		t.Errorf("manufacturer id = % x, want 00 20 6b", result.ManufacturerID)
	}
	if string(result.FamilyCode) != string([]byte{0x01, 0x02}) {
		t.Errorf("family code = % x, want 01 02", result.FamilyCode)
	}
	if string(result.ModelNumber) != string([]byte{0x00, 0x01}) {
		t.Errorf("model number = % x, want 00 01", result.ModelNumber)
	}
	if string(result.VersionBytes) != string([]byte{0x01, 0x00, 0x00, 0x00}) {
		t.Errorf("version = % x, want 01 00 00 00", result.VersionBytes)
	}
}

func TestDecodeIdentityReplyClassicManufacturer(t *testing.T) {
	// Roland (0x41, classic 1-byte), minimal family/model/version.
	raw := []byte{0xF0, 0x7E, 0x00, 0x06, 0x02, 0x41, 0x00, 0x00, 0x00, 0x00, 0xF7}
	var result IdentityResult
	decodeIdentityReply(raw, &result)

	if result.ManufacturerName != "" {
		t.Errorf("unmapped classic manufacturer should have no name, got %q", result.ManufacturerName)
	}
	if string(result.ManufacturerID) != string([]byte{0x41}) {
		t.Errorf("manufacturer id = % x, want 41", result.ManufacturerID)
	}
}

func TestDecodeIdentityReplyUnknownExtendedManufacturer(t *testing.T) {
	raw := []byte{0xF0, 0x7E, 0x7F, 0x06, 0x02, 0x00, 0xAA, 0xBB, 0x00, 0x00, 0x00, 0x00, 0xF7}
	var result IdentityResult
	decodeIdentityReply(raw, &result)
	if result.ManufacturerName != "" {
		t.Errorf("unknown extended manufacturer should have no name, got %q", result.ManufacturerName)
	}
	if string(result.ManufacturerID) != string([]byte{0x00, 0xAA, 0xBB}) {
		t.Errorf("manufacturer id = % x, want 00 aa bb (raw bytes preserved even though unknown)", result.ManufacturerID)
	}
}

func TestDecodeIdentityReplyTooShortDoesNotPanic(t *testing.T) {
	var result IdentityResult
	decodeIdentityReply([]byte{0xF0, 0x7E, 0x00, 0x06, 0x02}, &result)
	// Just must not panic; fields best-effort (likely all empty here).
}

func TestDecodeIdentityReplyNonReplyIsNoOp(t *testing.T) {
	var result IdentityResult
	decodeIdentityReply([]byte{0xF0, 0x00, 0x20, 0x29, 0xF7}, &result)
	if result.ManufacturerID != nil {
		t.Error("a non-identity-reply buffer must leave the result untouched")
	}
}
