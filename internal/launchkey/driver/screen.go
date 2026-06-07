package driver

// ScreenTarget is a display target ID used in commands 04h (configure display
// target) and 06h (set text field).
type ScreenTarget uint8

// Display targets per Launchkey MK4 Programmer's Reference Guide v2.0
// p.17. The 0x22..0x28 block is the mode-name targets; 0x20 is the
// persistent stationary overlay we use for polyclav's title.
const (
	ScreenDawPad     ScreenTarget = 0x22
	ScreenDrumPad    ScreenTarget = 0x23
	ScreenMixer      ScreenTarget = 0x24
	ScreenPlugin     ScreenTarget = 0x25
	ScreenSends      ScreenTarget = 0x26
	ScreenTransport  ScreenTarget = 0x27
	ScreenVolume     ScreenTarget = 0x28
	ScreenStationary ScreenTarget = 0x20 // Persistent overlay
)

// <config> byte for command 04h is a bitfield (PDF p.18):
//
//	bit 6 : auto-temp-on-change (device generates temp display on value change)
//	bit 5 : auto-temp-on-touch  (device generates temp display on Shift+touch)
//	bits 0-4 : arrangement ID (1..30); 0 means "cancel display"
//
// Plus two whole-byte special values: 0x00 cancels the target, 0x7F
// triggers the current contents.
const (
	displayConfigCancel         byte = 0x00
	displayConfigArrangement1   byte = 0x01 // arrangement 1, auto-temp bits cleared
	displayConfigAutoTempTouch  byte = 1 << 5
	displayConfigAutoTempChange byte = 1 << 6
	displayConfigTrigger        byte = 0x7F
)

// displayPaintBit, when OR'd into the field byte of a 06h "set text"
// message, tells the device to repaint immediately. Without it the text
// is buffered into the target but never shown on screen — the cause of
// the blank-display bug we hit before the cross-check against Ardour's
// launchkey_4.cc.
const displayPaintBit byte = 1 << 6

// encodeConfigureTarget emits a SysEx message to configure a display target
// with a specific arrangement / config byte. Format:
// F0 00 20 29 02 14 04 <target> <config> F7
func encodeConfigureTarget(target ScreenTarget, config byte) []byte {
	msg := make([]byte, 0, 10)
	msg = append(msg, mk4SysExHeader...)
	msg = append(msg, 0x04, byte(target), config)
	msg = append(msg, sysExEnd)
	return msg
}

// encodeTextField emits a SysEx message to write text to a specific field
// of a display target. Format:
// F0 00 20 29 02 14 06 <target> <field> <ASCII text bytes...> F7
// The text is presumed already ASCII-cleaned by the caller.
func encodeTextField(target ScreenTarget, field byte, text []byte) []byte {
	msg := make([]byte, 0, 9+len(text))
	msg = append(msg, mk4SysExHeader...)
	msg = append(msg, 0x06, byte(target), field)
	msg = append(msg, text...)
	msg = append(msg, sysExEnd)
	return msg
}

// encodeStationary returns the concatenated SysEx messages to write both lines
// of the persistent stationary overlay (target 0x20). Returns three complete
// messages back-to-back: configure target, set field 0, set field 1.
func encodeStationary(line1, line2 []byte) []byte {
	cfg := encodeConfigureTarget(ScreenStationary, displayConfigArrangement1)
	f1 := encodeTextField(ScreenStationary, 0|displayPaintBit, line1)
	f2 := encodeTextField(ScreenStationary, 1|displayPaintBit, line2)

	msg := make([]byte, 0, len(cfg)+len(f1)+len(f2))
	msg = append(msg, cfg...)
	msg = append(msg, f1...)
	msg = append(msg, f2...)
	return msg
}

// SetTitle writes a short title to the given screen target. The text is
// coerced to ASCII (non-printables → space) and capped at 16 bytes.
// Internally: 04h configure target → arrangement 1; 06h set field 0
// (the 'name' field of arrangement 1, with the paint bit set).
func (d *Driver) SetTitle(target ScreenTarget, text string) error {
	clean := asciiClean(text, 16)
	cfg := encodeConfigureTarget(target, displayConfigArrangement1)
	if err := d.send(cfg); err != nil {
		return err
	}
	return d.send(encodeTextField(target, 0|displayPaintBit, clean))
}

// SetStationary writes the two short lines of the persistent overlay
// (target 0x20). Each line is capped at 16 ASCII bytes.
func (d *Driver) SetStationary(line1, line2 string) error {
	l1 := asciiClean(line1, 16)
	l2 := asciiClean(line2, 16)
	cfg := encodeConfigureTarget(ScreenStationary, displayConfigArrangement1)
	if err := d.send(cfg); err != nil {
		return err
	}
	if err := d.send(encodeTextField(ScreenStationary, 0|displayPaintBit, l1)); err != nil {
		return err
	}
	return d.send(encodeTextField(ScreenStationary, 1|displayPaintBit, l2))
}

// SetDisplayText writes a two-line label to the persistent Stationary overlay
// (target 0x20). This is the call site for the patch-name display. It's
// effectively SetStationary(line1, line2) with a friendlier name.
func (d *Driver) SetDisplayText(line1, line2 string) error {
	return d.SetStationary(line1, line2)
}
