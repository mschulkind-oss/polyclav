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

	// Ignore lists exact port names (case-insensitive) to exclude from
	// note input on top of Match/the DAW exclusion — a denylist, not an
	// allowlist: a device NOT in this list just works the moment it's
	// plugged in, without needing to be added anywhere first. This is
	// only the INITIAL value seeded at construction; SetIgnore replaces
	// it live thereafter (the web UI's devices panel calls it on a
	// running daemon — see internal/web's /api/midi/devices).
	Ignore []string

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

	mu     sync.Mutex
	ports  map[string]*muxPort
	ignore []string        // as given (original case) — for display/persistence
	lower  map[string]bool // lowercased mirror of ignore — for fast case-insensitive lookup
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
		ignore: append([]string(nil), cfg.Ignore...),
		lower:  lowerSet(cfg.Ignore),
	}
}

func lowerSet(names []string) map[string]bool {
	set := make(map[string]bool, len(names))
	for _, n := range names {
		set[strings.ToLower(n)] = true
	}
	return set
}

// SetIgnore replaces the live ignore list (exact, case-insensitive
// device names). Safe to call from any goroutine — the web UI's PUT
// /api/midi/devices handler calls it directly on a running daemon. Takes
// effect on the next poll tick (at most PollInterval away), same as any
// other hotplug change; it does not force an immediate re-tick.
func (m *Multiplexer) SetIgnore(names []string) {
	m.mu.Lock()
	m.ignore = append([]string(nil), names...)
	m.lower = lowerSet(names)
	m.mu.Unlock()
}

// Ignore reports the currently-active ignore list, in the original case
// it was set with.
func (m *Multiplexer) Ignore() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.ignore...)
}

// Match reports the configured restriction substring — immutable after
// construction, so no lock is needed.
func (m *Multiplexer) Match() string { return m.cfg.Match }

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

	m.mu.Lock()
	lower := m.lower
	m.mu.Unlock()
	wanted := m.wantedPorts(names, lower)

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

// wantedPorts applies Match (or, absent Match, the DAW-role exclusion),
// then subtracts lower (a snapshot of the live ignore set — see
// SetIgnore) from the currently-enumerated port names.
func (m *Multiplexer) wantedPorts(names []string, lower map[string]bool) map[string]bool {
	needle := strings.ToLower(m.cfg.Match)
	wanted := make(map[string]bool, len(names))
	for _, n := range names {
		if classifyOne(n, needle, lower) == PortSendingNotes {
			wanted[n] = true
		}
	}
	return wanted
}

// PortStatus classifies a currently-enumerated MIDI input port for
// display (the `polyclav midi list` CLI and the web devices panel) —
// purely descriptive, never consulted by wantedPorts itself (which
// shares classifyOne, the one place the actual decision is made).
type PortStatus string

const (
	// PortSendingNotes: not excluded by Match, the DAW heuristic, or
	// Ignore — this port is currently feeding the synth.
	PortSendingNotes PortStatus = "notes"
	// PortDAWOnly: excluded by the default DAW-role heuristic (only
	// reachable when Match is empty) — a Launchkey control-surface
	// port, never a note source regardless of Ignore.
	PortDAWOnly PortStatus = "daw"
	// PortIgnored: would otherwise send notes, but is in the Ignore list.
	PortIgnored PortStatus = "ignored"
	// PortRestricted: Match is set and this port's name doesn't contain it.
	PortRestricted PortStatus = "restricted"
)

// PortInfo is one classified port name, for display only.
type PortInfo struct {
	Name   string
	Status PortStatus
}

// ClassifyPorts classifies every name in names against match/ignore,
// sharing classifyOne with wantedPorts so the CLI (`polyclav midi list`)
// and the web devices panel can never disagree with what the
// Multiplexer is actually doing. ignore is matched case-insensitively
// and exactly (not substring), same as SetIgnore.
func ClassifyPorts(names []string, match string, ignore []string) []PortInfo {
	lower := lowerSet(ignore)
	needle := strings.ToLower(match)
	out := make([]PortInfo, len(names))
	for i, n := range names {
		out[i] = PortInfo{Name: n, Status: classifyOne(n, needle, lower)}
	}
	return out
}

// classifyOne is the single source of truth behind both wantedPorts'
// pass/fail decision and ClassifyPorts' descriptive label. needle is
// already lowercased (the caller's match, or "" for the default mode);
// lower is a lowercased ignore-name set.
func classifyOne(name, needle string, lower map[string]bool) PortStatus {
	ln := strings.ToLower(name)
	if needle != "" {
		if !strings.Contains(ln, needle) {
			return PortRestricted
		}
	} else if looksLikeDAWPort(name) {
		return PortDAWOnly
	}
	if lower[ln] {
		return PortIgnored
	}
	return PortSendingNotes
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
