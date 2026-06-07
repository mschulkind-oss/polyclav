package components

import "fmt"

const (
	NovationMfrByte1   byte = 0x00
	NovationMfrByte2   byte = 0x20
	NovationMfrByte3   byte = 0x29
	CmdWriteCustomMode byte = 0x05
	SubCmdZero         byte = 0x00
	DefaultMasterCh    byte = 0x45
	SysExStart         byte = 0xF0
	SysExEnd           byte = 0xF7
)

// Encode generates the complete SysEx payload for a CustomMode.
func (m *CustomMode) Encode(pid ProductID) ([]byte, error) {
	b := make([]byte, 0, 64)

	b = append(b,
		SysExStart,
		NovationMfrByte1, NovationMfrByte2, NovationMfrByte3,
		pid[0], pid[1],
		CmdWriteCustomMode, SubCmdZero,
		DefaultMasterCh,
		byte(m.Surface),
		m.ModeIndex(),
	)

	b = writeTLV1(b, 0x00, byte(m.OnColor))

	ext := m.ExternalColor
	if ext == 0 && m.OnColor != 0 {
		ext = m.OnColor
	}
	b = writeTLV1(b, 0x01, byte(ext))

	var modeFlags byte = 0x40
	if m.EnableTransposition {
		modeFlags = 0x42
	}
	b = writeTLV1(b, 0x04, modeFlags)

	var octaveBounds byte = 0x00
	if m.EnableTransposition {
		octaveBounds = 0x33
	}
	b = writeTLV1(b, 0x07, octaveBounds)

	if m.DefaultOctave != 0 {
		b = writeTLV1(b, 0x08, m.DefaultOctave)
	}

	b = writeTLVName(b, m.Name)

	for i := range m.Controls {
		spec, err := encodeControlSpec(&m.Controls[i])
		if err != nil {
			return nil, fmt.Errorf("control %d: %w", i, err)
		}
		b = append(b, spec...)
	}

	if m.ControlNames != nil {
		for i := range m.Controls {
			idx := m.Controls[i].Index
			if name, ok := m.ControlNames[idx]; ok {
				b = writeControlName(b, idx, name)
			}
		}
	}

	b = append(b, SysExEnd)
	return b, nil
}

func abs(x int8) int8 {
	if x < 0 {
		return -x
	}
	return x
}

// encodeControlSpec generates the TLV record for a single control.
func encodeControlSpec(c *Control) ([]byte, error) {
	sysexLen := c.Type.SysexLen()

	if c.Type == ControlOff {
		return []byte{0x40, c.Index}, nil
	}

	buf := make([]byte, 2+sysexLen)
	buf[0] = byte(0x40) | byte(sysexLen)
	buf[1] = c.Index

	buf[2] = byte(c.Type)
	buf[3] = byte(c.OffColor) & 0x7F
	buf[4] = byte(c.OnColor) & 0x7F

	ch := abs(c.Channel)
	flags := c.Flags
	if c.Channel == -1 {
		flags |= FlagChannelIsAll
	}
	buf[5] = byte(ch) | (byte(c.Behaviour) << 4)
	buf[6] = byte(flags)

	switch c.Type {
	case ControlNote:
		buf[7] = 0
		buf[8] = c.Velocity
		buf[9] = c.Note

	case ControlCC:
		buf[7] = c.Sensitivity
		buf[8] = c.CCNumber
		buf[9] = byte(c.TopValue & 0x7F)
		buf[10] = byte(c.BottomValue & 0x7F)

	case ControlCC14:
		buf[7] = c.Sensitivity
		buf[8] = c.CCNumber
		buf[10] = byte(c.TopValue & 0x7F)
		buf[11] = byte((c.TopValue >> 7) & 0x7F)
		buf[12] = byte(c.BottomValue & 0x7F)
		buf[13] = byte((c.BottomValue >> 7) & 0x7F)

	case ControlProgramChange:
		switch c.ProgramTriggerMode {
		case ProgramTrigger:
			buf[5] = 0x20 | byte(ch)
			buf[9] = byte(c.TopValue)
			buf[10] = byte(c.TopValue)
		case ProgramIncrement, ProgramDecrement:
			return nil, fmt.Errorf("program change %v not yet implemented", c.ProgramTriggerMode)
		default:
			return nil, fmt.Errorf("unknown program trigger mode %d", c.ProgramTriggerMode)
		}

	default:
		return nil, fmt.Errorf("control type %v not yet implemented", c.Type)
	}

	return buf, nil
}
