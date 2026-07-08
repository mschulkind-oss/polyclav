package midiprobe

import "time"

// DeviceProfile is the portable JSON export of a probe session: everything
// a person on one machine captured about an unfamiliar MIDI device, in a
// form someone else (with no access to the physical hardware) can read to
// build real driver support.
type DeviceProfile struct {
	ExportedAt     time.Time       `json:"exportedAt"`
	InPort         string          `json:"inPort"`
	OutPort        string          `json:"outPort"`
	AllInPorts     []string        `json:"allInPorts"` // full enumeration at export time, for context
	AllOutPorts    []string        `json:"allOutPorts"`
	Identity       *IdentityResult `json:"identity,omitempty"`
	Events         []Event         `json:"events"` // full captured history, each with Label if tagged
	DistinctLabels []string        `json:"distinctLabels"`
}
