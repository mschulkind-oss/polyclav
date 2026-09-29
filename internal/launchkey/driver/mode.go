package driver

// The MK4 reports user-selected DAW surface layouts on channel 7. The same
// messages sent to DAW Out select those layouts (MK4 Programmer's Guide,
// "Mode report and select"). This is distinct from entering DAW mode via
// 9F 0C 7F, which selects these three layouts on connection.
func (d *Driver) handleModeReport(msg []byte) {
	if len(msg) != 3 || msg[0] != 0xB6 {
		return
	}
	var area string
	var expected byte
	switch msg[1] {
	case 29:
		area, expected = "pads", 2 // DAW
	case 30:
		area, expected = "encoders", 2 // Plugin
	case 31:
		area, expected = "faders", 1 // Volume
	default:
		return
	}
	if msg[2] == expected {
		// A layout change may reset encoder output to absolute CC 21..28.
		// The knob parser expects relative CC 85..92; reassert it when
		// Plugin is selected, including on a correction acknowledgement.
		if area == "encoders" && d.restoreDAWLayout {
			d.enableRelativeEncoders()
		}
		return
	}
	d.logger.Warn("launchkey unsupported surface layout", "area", area,
		"reported", msg[2], "supported", expected, "restore_enabled", d.restoreDAWLayout)
	if !d.restoreDAWLayout {
		return
	}
	if err := d.send([]byte{0xB6, msg[1], expected}); err != nil {
		d.logger.Warn("launchkey surface layout restore failed", "area", area, "err", err)
		return
	}
	if area == "encoders" {
		d.enableRelativeEncoders()
	}
}

func (d *Driver) enableRelativeEncoders() {
	if err := d.send([]byte{0xB6, 0x45, 0x7F}); err != nil {
		d.logger.Warn("encoder relative-mode enable failed", "err", err)
	}
}
