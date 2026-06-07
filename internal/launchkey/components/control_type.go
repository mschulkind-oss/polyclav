// Package components defines control-type enums for Launchkey MK4 custom-mode SysEx encoding.
package components

import "fmt"

type ControlType uint8

const (
	ControlOff           ControlType = 0
	ControlNote          ControlType = 1
	ControlCC            ControlType = 2
	ControlProgramChange ControlType = 3
	ControlRPN14         ControlType = 4
	ControlNRPN14        ControlType = 5
	ControlCC14          ControlType = 6
	ControlPitchBend     ControlType = 7
	ControlChAftertouch  ControlType = 8
	ControlRPN           ControlType = 9
	ControlNRPN          ControlType = 10
	ControlHID           ControlType = 12
)

var sysexLenTable = [13]int{0, 8, 9, 9, 8, 12, 12, 11, 9, 8, 11, 11, 7}

func (ct ControlType) SysexLen() int {
	if ct >= 13 {
		return 0
	}
	return sysexLenTable[ct]
}

func (ct ControlType) String() string {
	switch ct {
	case ControlOff:
		return "OFF"
	case ControlNote:
		return "NOTE"
	case ControlCC:
		return "CC"
	case ControlProgramChange:
		return "PROGRAM_CHANGE"
	case ControlRPN14:
		return "RPN_14"
	case ControlNRPN14:
		return "NRPN_14"
	case ControlCC14:
		return "CC_14"
	case ControlPitchBend:
		return "PITCHBEND"
	case ControlChAftertouch:
		return "CH_AFTERTOUCH"
	case ControlRPN:
		return "RPN"
	case ControlNRPN:
		return "NRPN"
	case ControlHID:
		return "HID"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", ct)
	}
}
