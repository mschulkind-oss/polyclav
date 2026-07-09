package midi

import (
	"context"
	"errors"
	"log/slog"
	"sort"
	"strings"
	"sync"
	"time"
)

// MultiplexerConfig configures the multi-device note-input reconciler.
type MultiplexerConfig struct {
	// Match, if non-empty, restricts note input to ports whose name
	// contains this substring (case-insensitive) — an explicit,
	// deliberate restriction (matching the pre-existing [midi].port_match
	// behavior). When Match is set, DAW-role ports are NOT excluded
	// automatically: docs/USER_GUIDE.md documents binding OSC to a
	// Launchkey's raw DAW CC stream via port_match = "DAW", and an
	// explicit substring is trusted as intentional.
	//
	// When Match is empty (the new default), every currently-present
	// input port is opened EXCEPT ones that look like a Launchkey-style
	// DAW control-surface port (see looksLikeDAWPort) — notes should
	// come from actual keyboards, not a control surface's CC/SysEx
	// stream, and the launchkey.Reconciler already owns that port
	// independently via its own fixed detection string.
	Match string

	PollInterval time.Duration

	// Sink receives every parsed MIDI event from every currently-open
	// port. Shared across all ports — events carry no per-port identity,
	// matching the existing Event shape (callers that care about origin
	// would need a new field; today's synth/OSC-mapper consumers don't).
	Sink Sink

	// PortLister enumerates current MIDI input port names. Tests inject
	// a fake; production defaults to PortNames.
	PortLister func() ([]string, error)
	// Opener listens to a single named port and streams its events to
	// sink until ctx is cancelled or the port dies. Tests inject a fake;
	// production defaults to Listen (an exact port name is specific
	// enough to be its own unique "match" substring — see NewMultiplexer).
	Opener func(ctx context.Context, logger *slog.Logger, portName string, sink Sink) error
}

// Multiplexer is a hotplug reconciler for reading note input from every
// currently-connected MIDI keyboard at once, instead of a single
// user-picked device (that's now launchkey.Reconciler's job, and only
// for the Launchkey's own DAW control-surface half). Each port gets its
// own independent listener goroutine — one device disconnecting doesn't
// affect the others.
//
// Known limitation: if two ports enumerate with the IDENTICAL name (a
// documented kernel bug affecting a single Launchkey's own MIDI/DAW
// pair — see the Role doc comment above), they collide on the same map
// key here and only one gets a listener. launchkey.Reconciler handles
// that specific duplicate-name case itself via an index tiebreaker;
// Multiplexer does not attempt to generalize that for arbitrary
// multi-device duplicate names, since it's a much rarer scenario for
// distinct physical keyboards.
type Multiplexer struct {
	logger *slog.Logger
	cfg    MultiplexerConfig

	mu    sync.Mutex
	ports map[string]*muxPort
}

type muxPort struct {
	cancel context.CancelFunc
	done   chan struct{}
}

// NewMultiplexer builds a Multiplexer with defaults filled in.
func NewMultiplexer(logger *slog.Logger, cfg MultiplexerConfig) *Multiplexer {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 1 * time.Second
	}
	if cfg.PortLister == nil {
		cfg.PortLister = PortNames
	}
	if cfg.Opener == nil {
		// A port's own full name is specific enough to uniquely select
		// itself as Listen's substring match — see PickPortName's step-1
		// filter followed by RoleMIDI's step-3 tiebreaker, which returns
		// the sole (already-unique) match regardless of role keyword.
		cfg.Opener = Listen
	}
	return &Multiplexer{
		logger: logger,
		cfg:    cfg,
		ports:  make(map[string]*muxPort),
	}
}

// PortCount reports how many ports are currently open.
func (m *Multiplexer) PortCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return len(m.ports)
}

// OpenPorts reports the currently-open port names, sorted.
func (m *Multiplexer) OpenPorts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	names := make([]string, 0, len(m.ports))
	for name := range m.ports {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Run drives the reconciler until ctx is done. Always returns nil.
func (m *Multiplexer) Run(ctx context.Context) error {
	ticker := time.NewTicker(m.cfg.PollInterval)
	defer ticker.Stop()

	m.tick(ctx)
	for {
		select {
		case <-ctx.Done():
			m.closeAll()
			return nil
		case <-ticker.C:
			m.tick(ctx)
		}
	}
}

func (m *Multiplexer) tick(ctx context.Context) {
	names, err := m.cfg.PortLister()
	if err != nil {
		m.logger.Warn("midi multiplexer port list", "err", err)
		return
	}
	wanted := m.wantedPorts(names)

	m.mu.Lock()
	var toCancel []*muxPort
	for name, p := range m.ports {
		if !wanted[name] {
			toCancel = append(toCancel, p)
			delete(m.ports, name)
		}
	}
	var toOpen []string
	for name := range wanted {
		if _, ok := m.ports[name]; !ok {
			toOpen = append(toOpen, name)
		}
	}
	m.mu.Unlock()

	for _, p := range toCancel {
		p.cancel()
	}
	for _, name := range toOpen {
		m.open(ctx, name)
	}
}

// wantedPorts applies Match (or, absent Match, the DAW-role exclusion)
// to the currently-enumerated port names.
func (m *Multiplexer) wantedPorts(names []string) map[string]bool {
	wanted := make(map[string]bool, len(names))
	needle := strings.ToLower(m.cfg.Match)
	for _, n := range names {
		if needle != "" {
			if !strings.Contains(strings.ToLower(n), needle) {
				continue
			}
		} else if looksLikeDAWPort(n) {
			continue
		}
		wanted[n] = true
	}
	return wanted
}

func (m *Multiplexer) open(ctx context.Context, name string) {
	portCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	self := &muxPort{cancel: cancel, done: done}

	m.mu.Lock()
	m.ports[name] = self
	m.mu.Unlock()

	m.logger.Info("midi multiplexer port opened", "port", name)
	go func() {
		defer close(done)
		err := m.cfg.Opener(portCtx, m.logger, name, m.cfg.Sink)

		m.mu.Lock()
		if m.ports[name] == self {
			delete(m.ports, name)
		}
		m.mu.Unlock()

		if err != nil && !errors.Is(err, context.Canceled) {
			m.logger.Warn("midi multiplexer port closed", "port", name, "err", err)
		} else {
			m.logger.Info("midi multiplexer port closed", "port", name)
		}
	}()
}

func (m *Multiplexer) closeAll() {
	m.mu.Lock()
	ports := make([]*muxPort, 0, len(m.ports))
	for _, p := range m.ports {
		ports = append(ports, p)
	}
	m.ports = make(map[string]*muxPort)
	m.mu.Unlock()

	for _, p := range ports {
		p.cancel()
	}
	for _, p := range ports {
		<-p.done
	}
}
