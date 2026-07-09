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

	for {
		lost := r.connLostChan()
		select {
		case <-ctx.Done():
			r.disconnect()
			return nil
		case <-ticker.C:
			r.tick(ctx)
		case <-lost:
			r.disconnect()
			r.tick(ctx)
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
