package launchkey

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"time"

	"gitlab.com/gomidi/midi/v2/drivers/rtmididrv"

	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/driver"
)

// launchkeyMatch is the fixed substring this package uses to
// auto-detect a Launchkey's DAW control-surface ports. It's
// intentionally NOT user-configurable: [midi].port_match now controls
// the generic multi-keyboard note listener instead (see
// internal/midi.Multiplexer), and the whole point of that split is that
// Launchkey extras (knobs/pads/screen/transport) just work whenever a
// Launchkey is plugged in, without any config. Note input from the
// Launchkey's own MIDI-role port is no longer this package's concern —
// the Multiplexer already picks it up (it isn't DAW-role, so it isn't
// excluded), which is why Reconciler no longer opens midi.Listen itself.
const launchkeyMatch = "launchkey"

// ReconcilerConfig configures the Launchkey hotplug reconciler.
type ReconcilerConfig struct {
	PollInterval time.Duration

	// IdleThreshold, if > 0, arms an idle watchdog: once the DAW
	// connection has gone this long with no inbound traffic (see
	// driver.Driver.LastEventAt) while still nominally "active", one
	// Warn-level incident is logged (idle duration, recent outbound
	// sends, current port list) and then suppressed until a fresh event
	// arrives. This is NOT a confident "it's wedged" detector — going
	// quiet on the DAW port is completely normal if you're just playing
	// notes and not touching a knob/pad/transport button. It exists so
	// that if a real wedge (see docs/ — 2026-07-11 investigation) happens
	// again, there's a timestamped record of when the silence started and
	// what was sent right before it, instead of needing to catch it live.
	// 0 disables it.
	IdleThreshold time.Duration

	OnDAWEvent   func(driver.Event)
	OnReconnect  func()
	OnDisconnect func()

	// PortLister enumerates current MIDI input port names. Tests inject a
	// fake; production uses rtmidi.
	PortLister func() ([]string, error)
	// Opener opens the DAW control-surface connection. Tests inject a
	// fake; production uses openReal.
	Opener func(ctx context.Context, logger *slog.Logger, portMatch string,
		dawSink func(driver.Event)) (Connection, error)
}

// Connection is one live joint MIDI+DAW connection to the keyboard.
// Driver is nil in tests; production always sets it. Close stops the
// listeners and returns the device to standalone. Lost is closed when
// the connection observes port-loss (USB unplug, ALSA stream death).
type Connection struct {
	Driver *driver.Driver
	Close  func()
	Lost   <-chan struct{}
}

// Reconciler keeps the Launchkey MK4 connection live across hotplug.
type Reconciler struct {
	logger *slog.Logger
	cfg    ReconcilerConfig

	mu    sync.Mutex
	state string
	conn  *Connection

	// idleAlerted/lastSeenEvent back the idle watchdog (see
	// ReconcilerConfig.IdleThreshold) — edge-triggered so one silent
	// stretch logs exactly once, not every poll tick.
	idleAlerted   bool
	lastSeenEvent time.Time

	// lastPortNames backs the port-list-changed debug log in tick() —
	// only ever touched from the Run() goroutine, so no lock needed.
	lastPortNames []string
}

// NewReconciler builds a Reconciler with defaults filled in.
func NewReconciler(logger *slog.Logger, cfg ReconcilerConfig) *Reconciler {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 1 * time.Second
	}
	if cfg.PortLister == nil {
		cfg.PortLister = defaultPortLister
	}
	if cfg.Opener == nil {
		cfg.Opener = openReal
	}
	return &Reconciler{
		logger: logger,
		cfg:    cfg,
		state:  "absent",
	}
}

// State returns "absent" | "opening" | "active".
func (r *Reconciler) State() string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.state
}

// Run drives the state machine until ctx is done. Always returns nil.
func (r *Reconciler) Run(ctx context.Context) error {
	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	r.tick(ctx)
	r.checkIdle()

	for {
		lost := r.connLostChan()
		select {
		case <-ctx.Done():
			r.disconnect()
			return nil
		case <-ticker.C:
			r.tick(ctx)
			r.checkIdle()
		case <-lost:
			r.disconnect()
			r.tick(ctx)
			r.checkIdle()
		}
	}
}

func (r *Reconciler) connLostChan() <-chan struct{} {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.conn == nil {
		return nil
	}
	return r.conn.Lost
}

func (r *Reconciler) tick(ctx context.Context) {
	names, err := r.cfg.PortLister()
	if err != nil {
		r.logger.Warn("launchkey port list", "err", err)
		return
	}
	if !equalStrings(names, r.lastPortNames) {
		r.logger.Debug("launchkey port list changed", "ports", names)
		r.lastPortNames = append([]string(nil), names...)
	}
	present := portPresent(names, launchkeyMatch)

	r.mu.Lock()
	state := r.state
	r.mu.Unlock()

	switch {
	case present && state == "active":
		return
	case !present && state == "active":
		r.disconnect()
	case !present && state != "active":
		r.mu.Lock()
		if r.state != "absent" {
			r.state = "absent"
		}
		r.mu.Unlock()
	case present && state != "active":
		r.mu.Lock()
		r.state = "opening"
		r.mu.Unlock()
		r.tryOpen(ctx)
	}
}

func (r *Reconciler) tryOpen(ctx context.Context) {
	dawSink := func(ev driver.Event) {
		if r.cfg.OnDAWEvent != nil {
			r.cfg.OnDAWEvent(ev)
		}
	}
	conn, err := r.cfg.Opener(ctx, r.logger, launchkeyMatch, dawSink)
	if err != nil {
		r.logger.Warn("launchkey open", "err", err)
		return
	}
	r.mu.Lock()
	r.conn = &conn
	r.state = "active"
	r.mu.Unlock()
	r.logger.Info("launchkey connected")
	if r.cfg.OnReconnect != nil {
		r.cfg.OnReconnect()
	}
}

func (r *Reconciler) disconnect() {
	r.mu.Lock()
	conn := r.conn
	wasActive := r.state == "active"
	r.conn = nil
	r.state = "absent"
	r.mu.Unlock()

	if conn != nil && conn.Close != nil {
		conn.Close()
	}
	if wasActive {
		r.logger.Info("launchkey disconnected")
		if r.cfg.OnDisconnect != nil {
			r.cfg.OnDisconnect()
		}
	}
}

// SetTitle proxies to the active driver; no-op if not active.
func (r *Reconciler) SetTitle(target driver.ScreenTarget, text string) error {
	r.mu.Lock()
	conn := r.conn
	r.mu.Unlock()
	if conn == nil || conn.Driver == nil {
		return nil
	}
	return conn.Driver.SetTitle(target, text)
}

// SetDisplayText proxies to the active driver; no-op if not active.
func (r *Reconciler) SetDisplayText(line1, line2 string) error {
	r.mu.Lock()
	conn := r.conn
	r.mu.Unlock()
	if conn == nil || conn.Driver == nil {
		return nil
	}
	return conn.Driver.SetDisplayText(line1, line2)
}

// SetPadColor proxies to the active driver; no-op if not active.
func (r *Reconciler) SetPadColor(row, col int, color components.Color) error {
	r.mu.Lock()
	conn := r.conn
	r.mu.Unlock()
	if conn == nil || conn.Driver == nil {
		return nil
	}
	return conn.Driver.SetPadColor(row, col, color)
}

// checkIdle implements ReconcilerConfig.IdleThreshold — see its doc
// comment. No-op when disabled, not connected, or already alerted for
// the current silent stretch.
func (r *Reconciler) checkIdle() {
	if r.cfg.IdleThreshold <= 0 {
		return
	}
	r.mu.Lock()
	conn := r.conn
	active := r.state == "active"
	r.mu.Unlock()
	if !active || conn == nil || conn.Driver == nil {
		return
	}

	last := conn.Driver.LastEventAt()
	r.mu.Lock()
	if !last.Equal(r.lastSeenEvent) {
		r.lastSeenEvent = last
		r.idleAlerted = false
	}
	idle := time.Since(last)
	shouldAlert := idle >= r.cfg.IdleThreshold && !r.idleAlerted
	if shouldAlert {
		r.idleAlerted = true
	}
	r.mu.Unlock()
	if !shouldAlert {
		return
	}

	names, _ := r.cfg.PortLister()
	r.logger.Warn("launchkey idle watchdog: no DAW-port traffic for a while",
		"idle", idle.Round(time.Second),
		"recent_sends", formatSends(conn.Driver.RecentSends()),
		"current_ports", names,
	)
}

// formatSends renders a driver.SendRecord ring buffer as one compact
// string for a structured log field — "HH:MM:SS.mmm HEXBYTES | ..."
// oldest first.
func formatSends(sends []driver.SendRecord) string {
	if len(sends) == 0 {
		return "(none)"
	}
	var b strings.Builder
	for i, s := range sends {
		if i > 0 {
			b.WriteString(" | ")
		}
		fmt.Fprintf(&b, "%s % X", s.At.Format("15:04:05.000"), s.Msg)
	}
	return b.String()
}

// equalStrings reports whether a and b have the same elements in the
// same order — good enough for the port-list-changed debug log, which
// only needs to notice a difference, not classify it.
func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func portPresent(names []string, match string) bool {
	needle := strings.ToLower(match)
	for _, n := range names {
		if strings.Contains(strings.ToLower(n), needle) {
			return true
		}
	}
	return false
}

func defaultPortLister() ([]string, error) {
	drv, err := rtmididrv.New()
	if err != nil {
		return nil, fmt.Errorf("rtmidi driver: %w", err)
	}
	defer drv.Close()
	ins, err := drv.Ins()
	if err != nil {
		return nil, fmt.Errorf("enumerate midi ins: %w", err)
	}
	names := make([]string, len(ins))
	for i, in := range ins {
		names[i] = in.String()
	}
	return names, nil
}

func openReal(ctx context.Context, logger *slog.Logger, portMatch string,
	dawSink func(driver.Event)) (Connection, error) {

	dawCtx, cancelDAW := context.WithCancel(ctx)
	lost := make(chan struct{})
	var lostOnce sync.Once
	signalLost := func() { lostOnce.Do(func() { close(lost) }) }

	d, err := driver.Open(dawCtx, logger, portMatch)
	if err != nil {
		cancelDAW()
		return Connection{}, fmt.Errorf("daw open: %w", err)
	}

	dawDone := make(chan struct{})
	go func() {
		defer close(dawDone)
		for ev := range d.Events() {
			if dawSink != nil {
				dawSink(ev)
			}
		}
		signalLost()
	}()

	closeFn := func() {
		cancelDAW()
		_ = d.Close()
		<-dawDone
	}

	return Connection{Driver: d, Close: closeFn, Lost: lost}, nil
}
