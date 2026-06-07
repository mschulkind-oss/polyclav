package driver

import (
	"fmt"

	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
)

// padNote returns the MIDI note number for the DAW pad at (row, col).
// row 0 = top row (notes 96..103); row 1 = bottom row (notes 112..119).
// col is 0..7 left-to-right.
func padNote(row, col int) (byte, error) {
	if col < 0 || col > 7 {
		return 0, fmt.Errorf("pad column %d out of range [0,7]", col)
	}
	switch row {
	case 0:
		return byte(96 + col), nil
	case 1:
		return byte(112 + col), nil
	default:
		return 0, fmt.Errorf("pad row %d out of range [0,1]", row)
	}
}

// encodePadRGB builds the SysEx payload for a single pad's 24-bit RGB color.
// Format: F0 00 20 29 02 14 01 43 <pad> <r> <g> <b> F7
// All of pad, r, g, b must already be 7-bit values (0..127).
func encodePadRGB(pad, r, g, b byte) []byte {
	msg := make([]byte, 0, 13)
	msg = append(msg, mk4SysExHeader...)
	msg = append(msg, 0x01, 0x43, pad, r, g, b)
	msg = append(msg, sysExEnd)
	return msg
}

// SetPadColor sets a single pad's LED to a Components-palette colour.
// Sends a Note On on channel 1 (status 0x90) with velocity = palette index.
// row must be 0 (top) or 1 (bottom); col must be 0..7.
func (d *Driver) SetPadColor(row, col int, color components.Color) error {
	note, err := padNote(row, col)
	if err != nil {
		return err
	}
	msg := []byte{0x90, note, byte(color) & 0x7F}
	return d.send(msg)
}

// SetPadRGB sets a single pad's LED to a 24-bit RGB colour via SysEx.
// 8-bit host RGB values are downshifted to the 7-bit values the device
// accepts.
func (d *Driver) SetPadRGB(row, col int, r, g, b uint8) error {
	note, err := padNote(row, col)
	if err != nil {
		return err
	}
	br, bg, bb := rgbToSysEx(r, g, b)
	return d.send(encodePadRGB(note, br, bg, bb))
}
