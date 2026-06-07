package components

import "fmt"

type SurfaceType uint8

const (
	SurfacePots     SurfaceType = 0
	SurfacePads     SurfaceType = 1
	SurfacePedal    SurfaceType = 2
	SurfaceFaders   SurfaceType = 3
	SurfaceModWheel SurfaceType = 4
)

// ProductID is the two-byte SysEx product identifier (bytes 4 and 5 of the
// outer envelope). For the entire Launchkey MK4 family these bytes are
// {0x02, 0x14} on the wire.
type ProductID [2]byte

// SysExProduct is the wire-encoded SysEx product-id header bytes for the
// MK4 family. The per-size USB PIDs (Variant25/37/49/61) are separate and
// used only for variant detection (e.g. hasFaders = pid[0] >= 69).
var SysExProduct = ProductID{0x02, 0x14}

// VariantID is the USB PID second-byte that identifies a specific
// Launchkey MK4 variant. The 49/61-key models gain faders.
type VariantID uint8

const (
	Variant25 VariantID = 67
	Variant37 VariantID = 68
	Variant49 VariantID = 69
	Variant61 VariantID = 70
)

func (v VariantID) HasFaders() bool { return v >= Variant49 }

// Behaviour maps to the JS bundle's noteBehaviour enum. Values:
//   - Momentary           = 0
//   - MomentaryAftertouch = 1
//   - Toggle              = 2
//   - ToggleAftertouch    = 3
type Behaviour uint8

const (
	BehaviourMomentary           Behaviour = 0
	BehaviourMomentaryAftertouch Behaviour = 1
	BehaviourToggle              Behaviour = 2
	BehaviourToggleAftertouch    Behaviour = 3
)

// ControlFlags is the per-control flag byte (byte[6] in a controlSpec
// record). Note that bits 2 and 3 carry different meanings depending on
// whether the surrounding control is a NOTE or a CC — this is how the
// firmware schema is structured (a single flags byte reinterpreted per
// type). Callers are responsible for choosing the right constant for the
// control type.
type ControlFlags uint8

const (
	FlagChannelIsAll       ControlFlags = 1 << 6
	FlagDisableLedFeedback ControlFlags = 1 << 5
	FlagUseControlOnColor  ControlFlags = 1 << 4

	// NOTE-only flags (overlap bits with CC interpretations below):
	FlagDisableTransposition ControlFlags = 1 << 3
	FlagFixedVelocity        ControlFlags = 1 << 2

	// CC-only flags (overlap with NOTE flags above):
	FlagContinuous ControlFlags = 1 << 3
	FlagBitDepth8  ControlFlags = 1 << 2
	FlagBipolar    ControlFlags = 1 << 1
)

type ProgramTriggerMode uint8

const (
	ProgramTrigger   ProgramTriggerMode = 0
	ProgramIncrement ProgramTriggerMode = 1
	ProgramDecrement ProgramTriggerMode = 2
)

// Control is one entry in a CustomMode's controls slice. Not all fields
// are used by every control type; see the per-type comments.
type Control struct {
	Index     uint8
	Type      ControlType
	OffColor  Color
	OnColor   Color
	Channel   int8 // -1 = "all" (sets FlagChannelIsAll); otherwise 0..15
	Behaviour Behaviour
	Flags     ControlFlags

	// NOTE only:
	Note     uint8
	Velocity uint8

	// CC / CC14 / Aftertouch:
	CCNumber    uint8
	TopValue    uint16
	BottomValue uint16
	Sensitivity uint8

	// ProgramChange:
	ProgramTriggerMode ProgramTriggerMode

	// RPN / NRPN family:
	RPNHigh uint8
	RPNLow  uint8

	// HID:
	KeyCode      uint8
	ModifierMask uint8
}

// CustomMode is the top-level model assembled by callers and passed to
// Encode. ExternalColor falls back to OnColor on encode when 0.
type CustomMode struct {
	Surface             SurfaceType
	Slot                uint8 // 0..7, user-visible slot
	Name                string
	OnColor             Color
	ExternalColor       Color // 0 falls back to OnColor on encode
	EnableTransposition bool
	DefaultOctave       uint8 // 0 to omit the 0x08 TLV
	Controls            []Control
	ControlNames        map[uint8]string // controlIndex → name
}

// PotsIndex returns the firmware controlIndex for pot n, where n is the
// zero-based slot position 0..15. Pots 0..7 use base 56; pots 8..15 use
// base 96. Panics if n is outside [0, 16).
func PotsIndex(n int) uint8 {
	if n < 0 || n >= 16 {
		panic(fmt.Sprintf("components.PotsIndex: n=%d out of range [0,16)", n))
	}
	if n < 8 {
		return uint8(56 + n)
	}
	return uint8(96 + (n - 8))
}

// PadsIndex returns the firmware controlIndex for pad n (0..15). Panics
// if n is outside [0, 16).
func PadsIndex(n int) uint8 {
	if n < 0 || n >= 16 {
		panic(fmt.Sprintf("components.PadsIndex: n=%d out of range [0,16)", n))
	}
	return uint8(n)
}

// FadersIndex returns the firmware controlIndex for fader n (0..8 on
// 49/61-key models). Panics if n is outside [0, 9).
func FadersIndex(n int) uint8 {
	if n < 0 || n >= 9 {
		panic(fmt.Sprintf("components.FadersIndex: n=%d out of range [0,9)", n))
	}
	return uint8(80 + n)
}

// ButtonsIndex returns the firmware controlIndex for fader-button n
// (0..8). Panics if n is outside [0, 9).
func ButtonsIndex(n int) uint8 {
	if n < 0 || n >= 9 {
		panic(fmt.Sprintf("components.ButtonsIndex: n=%d out of range [0,9)", n))
	}
	return uint8(40 + n)
}

// ModeIndex returns the firmware modeIndex byte (SysEx byte[10]) for this
// CustomMode based on its Surface and Slot.
//
//   - Pads: always 5 (pads section is at a fixed offset).
//   - Pots/Faders: Slot+6 when Slot < 4; Slot+14 otherwise.
//   - Pedal/ModWheel: 0 (TODO: spec).
func (m *CustomMode) ModeIndex() uint8 {
	switch m.Surface {
	case SurfacePads:
		return 5
	case SurfacePots, SurfaceFaders:
		if m.Slot < 4 {
			return m.Slot + 6
		}
		return m.Slot + 14
	case SurfacePedal, SurfaceModWheel:
		// TODO(spec): single-control sub-modes — modeIndex semantics not
		// yet confirmed; defaulting to 0.
		return 0
	default:
		return 0
	}
}
