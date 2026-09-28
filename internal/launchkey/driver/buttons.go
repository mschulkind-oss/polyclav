package driver

import (
	"fmt"

	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
)

// faderButtonColor builds a CC message setting a DAW-mode button's stationary
// palette color on channel 1.
func faderButtonColor(index int, color components.Color) []byte {
	if index < 1 || index > 9 {
		return nil
	}
	return []byte{0xB0, byte(ccFaderButtonBase + index - 1), byte(color) & 0x7F}
}

// faderButtonLED sets a DAW-mode button's stationary palette color on channel 1.
// Maintained for backward compatibility; green (21) represents on.
func faderButtonLED(index int, on bool) []byte {
	color := components.ColorOff
	if on {
		color = components.ColorGreen // green in the Launchkey palette
	}
	return faderButtonColor(index, color)
}

// SetFaderButtonColor sets one of the nine fader buttons to a palette color.
func (d *Driver) SetFaderButtonColor(index int, color components.Color) error {
	msg := faderButtonColor(index, color)
	if msg == nil {
		return fmt.Errorf("fader button %d out of range [1,9]", index)
	}
	return d.send(msg)
}

// SetFaderButtonLED lights or clears one of the nine fader buttons.
func (d *Driver) SetFaderButtonLED(index int, on bool) error {
	msg := faderButtonLED(index, on)
	if msg == nil {
		return fmt.Errorf("fader button %d out of range [1,9]", index)
	}
	return d.send(msg)
}
