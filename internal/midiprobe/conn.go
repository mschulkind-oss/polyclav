package midiprobe

import (
	"fmt"

	"gitlab.com/gomidi/midi/v2/drivers"
	"gitlab.com/gomidi/midi/v2/drivers/rtmididrv"
)

// rawConn abstracts a MIDI in/out port pair down to raw-byte send/receive so
// Session can be unit-tested with a fake, with no real MIDI hardware
// involved. realConn (below) is the rtmididrv-backed production
// implementation.
type rawConn interface {
	// Listen starts delivering every raw inbound message (including SysEx)
	// to onMsg. Returns a stop func.
	Listen(onMsg func(raw []byte)) (stop func(), err error)
	// Send writes raw bytes verbatim to the output port. No framing is
	// added — callers building a SysEx message must include F0...F7
	// themselves.
	Send(raw []byte) error
	// Close releases the underlying ports.
	Close() error
}

// sysExBufSize is the SysEx receive buffer, larger than gomidi's own
// built-in default (1024B, drivers/reader.go) since custom-mode/preset
// dumps from an unknown device can exceed 1KB.
const sysExBufSize = 4096

// realConn is the production rawConn: an exact-named MIDI in/out pair
// opened via rtmididrv.
type realConn struct {
	in  drivers.In
	out drivers.Out
}

// openConn opens the exact-named in/out ports (as returned by
// midi.PortNames()/midi.OutPortNames()) for raw probing. Unlike
// launchkey/driver.Open or midi.Listen, this does NOT do substring/role
// matching: the caller (the web layer) already resolved an exact name from
// a dropdown populated by full enumeration — the whole point of this tool
// is the user doesn't know the device's port conventions yet.
func openConn(inName, outName string) (rawConn, error) {
	drv, err := rtmididrv.New()
	if err != nil {
		return nil, fmt.Errorf("midi driver: %w", err)
	}

	// A failure here (e.g. no ALSA sequencer present at all — some minimal
	// CI runners have no /dev/snd/seq) is, from the caller's perspective,
	// indistinguishable from "that port doesn't exist": either way there
	// is nothing to connect to. Map both to ErrPortNotFound so callers get
	// one consistent, actionable error regardless of the underlying cause.
	ins, err := drv.Ins()
	if err != nil {
		return nil, fmt.Errorf("%w: enumerate midi ins: %v", ErrPortNotFound, err)
	}
	var in drivers.In
	for _, p := range ins {
		if p.String() == inName {
			in = p
			break
		}
	}
	if in == nil {
		return nil, fmt.Errorf("%w: input port %q", ErrPortNotFound, inName)
	}

	outs, err := drv.Outs()
	if err != nil {
		return nil, fmt.Errorf("%w: enumerate midi outs: %v", ErrPortNotFound, err)
	}
	var out drivers.Out
	for _, p := range outs {
		if p.String() == outName {
			out = p
			break
		}
	}
	if out == nil {
		return nil, fmt.Errorf("%w: output port %q", ErrPortNotFound, outName)
	}

	if err := in.Open(); err != nil {
		return nil, fmt.Errorf("open input %q: %w", inName, err)
	}
	if err := out.Open(); err != nil {
		_ = in.Close()
		return nil, fmt.Errorf("open output %q: %w", outName, err)
	}

	return &realConn{in: in, out: out}, nil
}

func (c *realConn) Listen(onMsg func(raw []byte)) (func(), error) {
	return c.in.Listen(func(msg []byte, _ int32) {
		onMsg(msg)
	}, drivers.ListenConfig{
		SysEx:           true, // the whole point: capture raw SysEx for reverse engineering
		SysExBufferSize: sysExBufSize,
	})
}

func (c *realConn) Send(raw []byte) error {
	return c.out.Send(raw)
}

func (c *realConn) Close() error {
	// NOT c.drv.Close(): that only closes ports registered in the
	// driver's own bookkeeping, which rtmididrv populates exclusively for
	// OpenVirtualIn/OpenVirtualOut — a port opened via the normal
	// Ins()/Outs() + Open() path (as openConn does) is never tracked
	// there, so drv.Close() would silently close nothing. Close each
	// port's own handle instead.
	inErr := c.in.Close()
	outErr := c.out.Close()
	if inErr != nil {
		return inErr
	}
	return outErr
}
