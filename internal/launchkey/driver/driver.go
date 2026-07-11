package driver

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gitlab.com/gomidi/midi/v2/drivers"
	"gitlab.com/gomidi/midi/v2/drivers/rtmididrv"

	"github.com/mschulkind-oss/polyclav/internal/midi"
)

type Event interface{ isEvent() }

type KnobEvent struct {
	Index int
	Delta int8
}

type FaderEvent struct {
	Index int
	Value uint8
}

type FaderButtonEvent struct {
	Index   int
	Pressed bool
}

type PadEvent struct {
	Row      int
	Col      int
	Pressed  bool
	Velocity uint8
}

type TransportButton int

const (
	TransportPlay TransportButton = iota
	TransportStop
	TransportRecord
	TransportLoop
	TransportRewind
	TransportFastForward
	TransportTrackLeft
	TransportTrackRight
	TransportSceneUp
	TransportSceneDown
	TransportShift
)

type TransportEvent struct {
	Button  TransportButton
	Pressed bool
}

func (KnobEvent) isEvent()        {}
func (FaderEvent) isEvent()       {}
func (FaderButtonEvent) isEvent() {}
func (PadEvent) isEvent()         {}
func (TransportEvent) isEvent()   {}

const (
	// In Relative mode (enabled by B6 45 7F at Open() time), the device
	// remaps the 8 encoders from CC 21..28 (absolute) to CC 85..92 (0x55..
	// 0x5C). Hardware-confirmed 2026-05-25 via the "daw rx" diagnostic log:
	// twisting a knob produced `BF 56 42` (+2) and `BF 56 3F` (-1) — i.e.
	// channel 16, CC 0x56 (relative-mode knob 2), value encoded as binary
	// offset around 64.
	ccKnobBase        = 0x55
	ccFaderBase       = 5
	ccFaderButtonBase = 37

	noteTransportRewind      = 0x71
	noteTransportFastForward = 0x72
	noteTransportStop        = 0x74
	noteTransportPlay        = 0x73
	noteTransportLoop        = 0x76
	noteTransportRecord      = 0x75
	noteTransportTrackLeft   = 0x66
	noteTransportTrackRight  = 0x67
	noteTransportSceneUp     = 0x68
	noteTransportSceneDown   = 0x69
	noteTransportShift       = 0x6A

	noteDAWModeOn   = 0x0C
	noteFeatureCtrl = 0x0B
)

type Driver struct {
	logger   *slog.Logger
	rtmidi   *rtmididrv.Driver
	in       drivers.In
	out      drivers.Out
	events   chan Event
	stop     func()
	cancel   context.CancelFunc
	closed   chan struct{}
	closeMu  sync.Mutex
	isClosed bool

	activityMu  sync.Mutex
	lastEventAt time.Time
	sends       []SendRecord
}

// SendRecord is one outbound message this Driver sent, kept in a capped
// ring buffer (see RecentSends) for post-hoc diagnosis when the
// connection goes silent — this is what let us confirm, during a real
// 2026-07-11 Launchkey wedge, that every message we sent was accepted by
// ALSA (no error returned) but never actually reached the device.
type SendRecord struct {
	At  time.Time
	Msg []byte
}

// sendRingCap bounds RecentSends' memory — enough history to cover the
// startup handshake (~30 messages) plus a stretch of ordinary use.
const sendRingCap = 128

func Open(ctx context.Context, logger *slog.Logger, portMatch string) (*Driver, error) {
	if logger == nil {
		logger = slog.Default()
	}
	drv, err := rtmididrv.New()
	if err != nil {
		return nil, fmt.Errorf("rtmidi driver: %w", err)
	}

	ins, err := drv.Ins()
	if err != nil {
		drv.Close()
		return nil, fmt.Errorf("enumerate midi ins: %w", err)
	}
	outs, err := drv.Outs()
	if err != nil {
		drv.Close()
		return nil, fmt.Errorf("enumerate midi outs: %w", err)
	}

	in := pickInDAW(ins, portMatch)
	if in == nil {
		drv.Close()
		return nil, fmt.Errorf("no DAW input port matching %q (have: %s)", portMatch, strings.Join(inNames(ins), ", "))
	}
	out := pickOutDAW(outs, portMatch)
	if out == nil {
		drv.Close()
		return nil, fmt.Errorf("no DAW output port matching %q (have: %s)", portMatch, strings.Join(outNames(outs), ", "))
	}

	if err := in.Open(); err != nil {
		drv.Close()
		return nil, fmt.Errorf("open in %q: %w", in.String(), err)
	}
	if err := out.Open(); err != nil {
		in.Close()
		drv.Close()
		return nil, fmt.Errorf("open out %q: %w", out.String(), err)
	}

	logger.Info("launchkey daw open", "in", in.String(), "out", out.String())

	d := &Driver{
		logger:      logger,
		rtmidi:      drv,
		in:          in,
		out:         out,
		events:      make(chan Event, 64),
		closed:      make(chan struct{}),
		lastEventAt: time.Now(),
	}

	if err := d.send([]byte{0x9F, noteDAWModeOn, 0x7F}); err != nil {
		d.shutdown()
		return nil, fmt.Errorf("daw-mode enter: %w", err)
	}
	if err := d.send([]byte{0x9F, noteFeatureCtrl, 0x7F}); err != nil {
		d.shutdown()
		return nil, fmt.Errorf("feature controls enable: %w", err)
	}

	for row := 0; row <= 1; row++ {
		for col := 0; col <= 7; col++ {
			note, _ := padNote(row, col)
			if err := d.send([]byte{0x90, note, 0x00}); err != nil {
				logger.Warn("clear pad failed", "row", row, "col", col, "err", err)
			}
		}
	}

	suppressTargets := []byte{
		0x15, 0x16, 0x17, 0x18, 0x19, 0x1A, 0x1B, 0x1C,
		0x21,
	}
	for _, target := range suppressTargets {
		msg := append([]byte{}, mk4SysExHeader...)
		msg = append(msg, 0x04, target, displayConfigArrangement1, sysExEnd)
		if err := d.send(msg); err != nil {
			logger.Warn("suppress temp display failed", "target", fmt.Sprintf("0x%02X", target), "err", err)
		}
	}

	primeMsgs := [][]byte{
		encodeConfigureTarget(ScreenStationary, displayConfigArrangement1),
		encodeTextField(ScreenStationary, 0|displayPaintBit, nil),
		encodeTextField(ScreenStationary, 1|displayPaintBit, nil),
		encodeConfigureTarget(ScreenDawPad, displayConfigArrangement1),
	}
	for _, msg := range primeMsgs {
		if err := d.send(msg); err != nil {
			logger.Warn("launchkey screen prime failed", "err", err)
		}
	}

	if err := d.send([]byte{0xB6, 0x45, 0x7F}); err != nil {
		logger.Warn("encoder relative-mode enable failed", "err", err)
	}

	listenCtx, cancel := context.WithCancel(ctx)
	d.cancel = cancel
	stop, err := in.Listen(func(msg []byte, _ int32) {
		// Wire-level liveness: stamped for ANY inbound byte, independent
		// of whether parseMessage recognizes it — LastEventAt is the
		// idle watchdog's signal (internal/launchkey.Reconciler), and it
		// must not go quiet just because the device sent something this
		// package doesn't decode.
		d.recordEvent()
		ev, ok := parseMessage(msg)
		if !ok {
			return
		}
		select {
		case d.events <- ev:
		case <-listenCtx.Done():
		default:
			logger.Warn("launchkey events channel full; dropping event")
		}
	}, drivers.ListenConfig{
		SysEx:       false,
		ActiveSense: false,
		TimeCode:    false,
		OnErr: func(e error) {
			logger.Warn("launchkey listen error", "err", e)
		},
	})
	if err != nil {
		d.cancel()
		d.shutdown()
		return nil, fmt.Errorf("listen %s: %w", in.String(), err)
	}
	d.stop = stop

	go func() {
		<-listenCtx.Done()
		_ = d.Close()
	}()

	return d, nil
}

func (d *Driver) Events() <-chan Event { return d.events }

func (d *Driver) Close() error {
	d.closeMu.Lock()
	if d.isClosed {
		d.closeMu.Unlock()
		return nil
	}
	d.isClosed = true
	d.closeMu.Unlock()

	if d.cancel != nil {
		d.cancel()
	}
	if d.stop != nil {
		d.stop()
	}
	if d.out != nil && d.out.IsOpen() {
		_ = d.send([]byte{0x9F, noteDAWModeOn, 0x00})
	}
	d.shutdown()
	close(d.events)
	close(d.closed)
	return nil
}

func (d *Driver) shutdown() {
	if d.in != nil {
		_ = d.in.Close()
	}
	if d.out != nil {
		_ = d.out.Close()
	}
	if d.rtmidi != nil {
		_ = d.rtmidi.Close()
	}
}

func (d *Driver) send(msg []byte) error {
	if d.out == nil {
		return errors.New("driver: out port not open")
	}
	d.recordSend(msg)
	return d.out.Send(msg)
}

func (d *Driver) recordSend(msg []byte) {
	cp := append([]byte(nil), msg...)
	d.activityMu.Lock()
	d.sends = append(d.sends, SendRecord{At: time.Now(), Msg: cp})
	if len(d.sends) > sendRingCap {
		d.sends = d.sends[len(d.sends)-sendRingCap:]
	}
	d.activityMu.Unlock()
}

func (d *Driver) recordEvent() {
	d.activityMu.Lock()
	d.lastEventAt = time.Now()
	d.activityMu.Unlock()
}

// LastEventAt reports when the device last sent ANY message (parsed or
// not) — internal/launchkey.Reconciler's idle watchdog polls this to
// notice a connection that's gone silent while still nominally open.
func (d *Driver) LastEventAt() time.Time {
	d.activityMu.Lock()
	defer d.activityMu.Unlock()
	return d.lastEventAt
}

// RecentSends returns a copy of the most recent outbound messages
// (oldest first, see SendRecord) — folded into the idle watchdog's
// incident log so a wedge report shows exactly what we sent right
// before the connection went quiet.
func (d *Driver) RecentSends() []SendRecord {
	d.activityMu.Lock()
	defer d.activityMu.Unlock()
	out := make([]SendRecord, len(d.sends))
	copy(out, d.sends)
	return out
}

func pickInDAW(ins []drivers.In, portMatch string) drivers.In {
	idx := midi.PickPortName(inNames(ins), portMatch, midi.RoleDAW)
	if idx < 0 {
		return nil
	}
	return ins[idx]
}

func pickOutDAW(outs []drivers.Out, portMatch string) drivers.Out {
	idx := midi.PickPortName(outNames(outs), portMatch, midi.RoleDAW)
	if idx < 0 {
		return nil
	}
	return outs[idx]
}

func inNames(ins []drivers.In) []string {
	out := make([]string, len(ins))
	for i, p := range ins {
		out[i] = p.String()
	}
	return out
}

func outNames(outs []drivers.Out) []string {
	names := make([]string, len(outs))
	for i, p := range outs {
		names[i] = p.String()
	}
	return names
}

func parseMessage(msg []byte) (Event, bool) {
	if len(msg) < 2 {
		return nil, false
	}
	status := msg[0]
	msgType := status & 0xF0
	channel := status & 0x0F

	switch msgType {
	case 0xB0:
		if len(msg) < 3 {
			return nil, false
		}
		if channel != 0x0F {
			return nil, false
		}
		cc := msg[1]
		val := msg[2]
		switch {
		case cc >= ccKnobBase && cc < ccKnobBase+8:
			var delta int8
			switch {
			case val > 64:
				delta = int8(val - 64)
			case val < 64:
				delta = -int8(64 - val)
			default:
				return nil, false
			}
			return KnobEvent{Index: int(cc-ccKnobBase) + 1, Delta: delta}, true
		case cc >= ccFaderBase && cc < ccFaderBase+9:
			return FaderEvent{Index: int(cc-ccFaderBase) + 1, Value: val}, true
		case cc >= ccFaderButtonBase && cc < ccFaderButtonBase+8:
			return FaderButtonEvent{Index: int(cc-ccFaderButtonBase) + 1, Pressed: val >= 64}, true
		}
		return nil, false

	case 0x90, 0x80:
		if len(msg) < 3 {
			return nil, false
		}
		note := msg[1]
		vel := msg[2]
		pressed := (msgType == 0x90) && vel > 0

		if channel == 0x00 {
			if note >= 96 && note <= 103 {
				return PadEvent{Row: 0, Col: int(note - 96), Pressed: pressed, Velocity: vel}, true
			}
			if note >= 112 && note <= 119 {
				return PadEvent{Row: 1, Col: int(note - 112), Pressed: pressed, Velocity: vel}, true
			}
			return nil, false
		}
		if channel == 0x0F {
			tb, ok := transportFromNote(note)
			if !ok {
				return nil, false
			}
			return TransportEvent{Button: tb, Pressed: pressed}, true
		}
		return nil, false
	}
	return nil, false
}

func transportFromNote(note byte) (TransportButton, bool) {
	switch note {
	case noteTransportPlay:
		return TransportPlay, true
	case noteTransportStop:
		return TransportStop, true
	case noteTransportRecord:
		return TransportRecord, true
	case noteTransportLoop:
		return TransportLoop, true
	case noteTransportRewind:
		return TransportRewind, true
	case noteTransportFastForward:
		return TransportFastForward, true
	case noteTransportTrackLeft:
		return TransportTrackLeft, true
	case noteTransportTrackRight:
		return TransportTrackRight, true
	case noteTransportSceneUp:
		return TransportSceneUp, true
	case noteTransportSceneDown:
		return TransportSceneDown, true
	case noteTransportShift:
		return TransportShift, true
	}
	return 0, false
}
