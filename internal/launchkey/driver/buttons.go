package driver

import "fmt"

// faderButtonLED sets a DAW-mode button's stationary palette color on channel 1.
func faderButtonLED(index int, on bool) []byte {
	if index < 1 || index > 9 {
		return nil
	}
	value := byte(0)
	if on {
		value = 21 // green in the Launchkey palette
	}
	return []byte{0xB0, byte(ccFaderButtonBase + index - 1), value}
}

// SetFaderButtonLED lights or clears one of the nine fader buttons.
func (d *Driver) SetFaderButtonLED(index int, on bool) error {
	msg := faderButtonLED(index, on)
	if msg == nil {
		return fmt.Errorf("fader button %d out of range [1,9]", index)
	}
	return d.send(msg)
}
