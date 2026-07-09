// Package midi opens MIDI input ports via gomidi/v2 + rtmididrv and
// forwards parsed events to a caller-provided sink.
//
// On Linux, rtmidi talks to the ALSA sequencer, which is the bridge
// PipeWire (and most Linux MIDI tooling) exposes USB MIDI devices through.
// The Launchkey MK4 enumerates as two seq ports — "MIDI" (keys, wheels,
// pads) and "DAW" (transport, knobs, faders, screen).
package midi

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/drivers"
	"gitlab.com/gomidi/midi/v2/drivers/rtmididrv"
)

// Event is a parsed MIDI message in the small subset polyclav cares about
// (notes, CC, pitch bend). Other message types (aftertouch, etc.) are
// dropped silently.
type Event struct {
	Kind    Kind
	Channel byte
	Note    byte
	Vel     byte
	CC      byte
	Value   byte
	Bend    uint16
}

type Kind uint8

const (
	NoteOn Kind = iota
	NoteOff
	ControlChange
	PitchBend
)

// Sink receives parsed MIDI events. Implementations should be non-blocking;
// blocking here delays subsequent events.
type Sink func(Event)

// Listen opens the first MIDI input port whose name contains `match`
// (case-insensitive) and forwards parsed events to sink. Returns when
// ctx is cancelled OR the underlying ALSA port dies (USB unplug, etc.).
func Listen(ctx context.Context, logger *slog.Logger, match string, sink Sink) error {
	drv, err := rtmididrv.New()
	if err != nil {
		return fmt.Errorf("midi driver: %w", err)
	}
	defer drv.Close()

	ins, err := drv.Ins()
	if err != nil {
		return fmt.Errorf("enumerate midi ins: %w", err)
	}
	if len(ins) == 0 {
		return fmt.Errorf("no MIDI input ports present")
	}

	in := pickPort(ins, match)
	if in == nil {
		return fmt.Errorf("no MIDI port matching %q (available: %s)", match, strings.Join(portNames(ins), ", "))
	}

	logger.Info("midi listen", "port", in.String())

	lc, cancel := context.WithCancel(ctx)
	defer cancel()

	// HandleError cancels the inner ctx so Listen returns on port-loss (USB unplug → ALSA stream death).
	stop, err := midi.ListenTo(in, func(msg midi.Message, _ int32) {
		if ev, ok := parse(msg); ok {
			sink(ev)
		}
	}, midi.HandleError(func(err error) {
		logger.Warn("midi listen error", "port", in.String(), "err", err)
		cancel()
	}))
	if err != nil {
		return fmt.Errorf("listen %s: %w", in.String(), err)
	}
	defer stop()

	<-lc.Done()
	logger.Info("midi stop", "port", in.String())
	return nil
}

// PortNames enumerates the currently-visible MIDI input port names.
func PortNames() ([]string, error) {
	drv, err := rtmididrv.New()
	if err != nil {
		return nil, err
	}
	defer drv.Close()
	ins, err := drv.Ins()
	if err != nil {
		return nil, err
	}
	return portNames(ins), nil
}

// OutPortNames enumerates the currently-visible MIDI output port names.
// Mirrors PortNames on the output side — used by tooling (the MIDI probe)
// that needs to send to a device as well as listen to it.
func OutPortNames() ([]string, error) {
	drv, err := rtmididrv.New()
	if err != nil {
		return nil, err
	}
	defer drv.Close()
	outs, err := drv.Outs()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(outs))
	for i, p := range outs {
		names[i] = p.String()
	}
	return names, nil
}

func pickPort(ins []drivers.In, match string) drivers.In {
	idx := PickPortName(portNames(ins), match, RoleMIDI)
	if idx < 0 {
		return nil
	}
	return ins[idx]
}

// Role indicates which Launchkey MK4 sub-port a caller wants. The MK4
// exposes two ALSA seq ports per device — a "MIDI" port (keys, wheels,
// pads) and a "DAW" port (transport, knobs, faders, screen). On healthy
// kernels the names disambiguate ("...MIDI In" vs "...DAW In"); on
// kernels affected by a known seq-port-naming bug (Ardour's
// launchkey_4 surface documents this) both ports can enumerate with
// identical names. In that case the second matching port is, by
// convention, the DAW port.
type Role int

const (
	// RoleMIDI picks the MIDI sub-port — first preference is a name
	// containing "midi"; falls back to the first matching port when
	// names are ambiguous.
	RoleMIDI Role = iota
	// RoleDAW picks the DAW sub-port — first preference is a name
	// containing "daw"; falls back to the second matching port (per
	// Ardour's convention) when names are ambiguous; falls back to
	// -1 (not found) if there is only one matching port.
	RoleDAW
)

// PickPortName chooses an index into `names` for the requested role.
//
// Match semantics (case-insensitive throughout):
//  1. Filter `names` to entries containing `match` (substring). If
//     `match` is empty, all names match.
//  2. Among matches, prefer one whose name contains the role keyword
//     ("midi" for RoleMIDI, "daw" for RoleDAW) — return the first
//     such index. If RoleDAW and no name contains "daw" but a name
//     contains "midi 2" (Ardour's documented alternate suffix on
//     some kernels), that also counts.
//  3. If no role-keyword match: fall back to the index-based
//     tiebreaker for the duplicate-name kernel bug:
//     - RoleMIDI → first matching index
//     - RoleDAW → second matching index (or -1 if only one)
//  4. Returns -1 if no port matches `match`.
//
// This is the single source of truth for port-role selection across
// midi.Listen and launchkey/driver.Open.
func PickPortName(names []string, match string, role Role) int {
	needle := strings.ToLower(match)
	// Step 1: collect indices of substring matches.
	var matches []int
	for i, n := range names {
		ln := strings.ToLower(n)
		if needle == "" || strings.Contains(ln, needle) {
			matches = append(matches, i)
		}
	}
	if len(matches) == 0 {
		return -1
	}
	// Step 2: prefer role-keyword match.
	switch role {
	case RoleMIDI:
		for _, idx := range matches {
			ln := strings.ToLower(names[idx])
			// Prefer a name with "midi" but NOT one that also says
			// "daw" (some kernels could name a port "daw midi" etc.;
			// be conservative). The MK4's MIDI port is named
			// "...MK4 ... MIDI In" — has "midi", no "daw".
			if strings.Contains(ln, "midi") && !strings.Contains(ln, "daw") {
				return idx
			}
		}
	case RoleDAW:
		for _, idx := range matches {
			ln := strings.ToLower(names[idx])
			if strings.Contains(ln, "daw") {
				return idx
			}
		}
		// Ardour's alternate: "MIDI 2" suffix (rare; documented).
		for _, idx := range matches {
			ln := strings.ToLower(names[idx])
			if strings.Contains(ln, "midi 2") {
				return idx
			}
		}
	}
	// Step 3: index-based tiebreaker (duplicate-name fallback).
	switch role {
	case RoleMIDI:
		return matches[0]
	case RoleDAW:
		if len(matches) < 2 {
			return -1
		}
		return matches[1]
	}
	return -1
}

// looksLikeDAWPort reports whether name matches the DAW sub-port naming
// heuristic from Role/PickPortName's doc comment above: a name
// containing "daw", or the "midi 2" alternate suffix some kernels use
// for a device's second (DAW) port when names are otherwise ambiguous.
//
// Used by Multiplexer to exclude Launchkey-style control-surface ports
// from the generic multi-device note listener when no explicit Match
// filter is set — a control surface's CC/SysEx stream isn't note data,
// and the Launchkey reconciler (internal/launchkey) already owns that
// port on its own, fixed detection string.
func looksLikeDAWPort(name string) bool {
	ln := strings.ToLower(name)
	return strings.Contains(ln, "daw") || strings.Contains(ln, "midi 2")
}

func portNames(ins []drivers.In) []string {
	out := make([]string, len(ins))
	for i, in := range ins {
		out[i] = in.String()
	}
	return out
}

func parse(msg midi.Message) (Event, bool) {
	var channel, key, vel, cc, val uint8
	var rel int16
	var abs uint16
	switch {
	case msg.GetNoteStart(&channel, &key, &vel):
		return Event{Kind: NoteOn, Channel: channel, Note: key, Vel: vel}, true
	case msg.GetNoteEnd(&channel, &key):
		return Event{Kind: NoteOff, Channel: channel, Note: key}, true
	case msg.GetControlChange(&channel, &cc, &val):
		return Event{Kind: ControlChange, Channel: channel, CC: cc, Value: val}, true
	case msg.GetPitchBend(&channel, &rel, &abs):
		return Event{Kind: PitchBend, Channel: channel, Bend: abs}, true
	}
	return Event{}, false
}
