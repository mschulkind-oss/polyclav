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
	// Allow is the ALLOWLIST of case-insensitive SUBSTRINGS naming the
	// MIDI input ports that may send notes, and the ONLY thing that
	// decides which ports open. A port opens only if its name contains
	// at least one entry.
	//
	// EMPTY MEANS NOTHING OPENS — no keyboard sends notes until a device
	// is explicitly selected. This is deliberately opt-in: a machine can
	// enumerate loopback buses, control surfaces, and other people's
	// hardware, and silently binding to all of them is worse than making
	// the user name what they want. cmd/polyclav prints a loud startup
	// banner when this list is empty (see printNoMIDIDevicesBanner) so
	// the silence is never a mystery.
	//
	// An explicit entry WINS over the DAW/loopback name heuristics: a
	// user who names a Launchkey's DAW port gets its raw CC stream
	// (docs/USER_GUIDE.md's OSC-binding workflow). The heuristics survive
	// only as descriptive labels on unselected ports (see PortStatus).
	//
	// Substring (not exact) matching is deliberate: a stable fragment of
	// the name (e.g. "CASIO USB-MIDI") keeps matching across a
	// replug/reboot even though ALSA appends a volatile " <client>:<port>"
	// address to the full port name (docs/MIDI_DEVICE_MATCHING.md). This is
	// only the INITIAL value seeded at construction; SetAllow replaces it
	// live thereafter (the web UI's devices panel calls it on a running
	// daemon — see internal/web's /api/midi/devices).
	Allow []string

	PollInterval time.Duration

	// IdleThreshold, if > 0, arms a per-port idle watchdog: once an open
	// port has gone this long with no inbound traffic, one Warn-level
	// incident is logged (idle duration, port name, current port list)
	// and then suppressed until fresh traffic arrives on that port. Not a
	// confident "wedged" detector — a keyboard nobody's touching is
	// normal — it exists purely so a real device-level wedge (see the
	// 2026-07-11 Launchkey investigation) leaves a timestamped record of
	// when the silence started, instead of needing to be caught live.
	// 0 disables it.
	IdleThreshold time.Duration

	// Sink receives every parsed MIDI event from every currently-open
	// port. Shared across all ports — events carry no per-port identity,
	// matching the existing Event shape (callers that care about origin
	// would need a new field; today's synth/OSC-mapper consumers don't).
	Sink Sink

	// RawSink receives copied raw inbound messages from existing open
	// performance listeners before parsing, including message kinds the
	// synth decoder ignores (aftertouch, program change, etc.).
	RawSink RawSink

	// PortLister enumerates current MIDI input port names. Tests inject
	// a fake; production defaults to PortNames.
	PortLister func() ([]string, error)
	// Opener listens to a single named port and streams its events to
	// sink until ctx is cancelled or the port dies. rawSink, when non-nil,
	// receives copied raw messages before decode. Tests inject a fake;
	// production defaults to ListenWithRaw (an exact port name is specific
	// enough to be its own unique "match" substring — see NewMultiplexer).
	Opener func(ctx context.Context, logger *slog.Logger, portName string, sink Sink, rawSink func([]byte)) error
}

// Multiplexer is a hotplug reconciler for reading note input from every
// SELECTED MIDI keyboard at once (see MultiplexerConfig.Allow) rather
// than a single user-picked device. Each port gets its own independent
// listener goroutine — one device disconnecting doesn't affect the
// others, and a selected device that isn't plugged in yet simply opens
// the moment it appears.
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
	allow []string // as given (original case) — for display/persistence
	lower []string // lowercased mirror of allow — substrings to match against a lowercased port name

	// lastPortNames backs the port-list-changed debug log in tick() —
	// only ever touched from the Run() goroutine, so no lock needed.
	lastPortNames []string
}

type muxPort struct {
	cancel context.CancelFunc
	done   chan struct{}

	// activityMu guards the idle-watchdog state below (see
	// MultiplexerConfig.IdleThreshold) — written from the port's own
	// listener goroutine (via the wrapped sink in open()), read from
	// checkIdle() on the Run() goroutine.
	activityMu  sync.Mutex
	lastEventAt time.Time
	idleAlerted bool
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
		cfg.Opener = func(ctx context.Context, logger *slog.Logger, portName string, sink Sink, rawSink func([]byte)) error {
			return ListenWithRaw(ctx, logger, portName, sink, func(_ string, raw []byte) {
				if rawSink != nil {
					rawSink(raw)
				}
			})
		}
	}
	return &Multiplexer{
		logger: logger,
		cfg:    cfg,
		ports:  make(map[string]*muxPort),
		allow:  append([]string(nil), cfg.Allow...),
		lower:  lowerAll(cfg.Allow),
	}
}

func lowerAll(names []string) []string {
	out := make([]string, len(names))
	for i, n := range names {
		out[i] = strings.ToLower(n)
	}
	return out
}

// SetAllow replaces the live allowlist (case-insensitive substrings —
// see MultiplexerConfig.Allow). Safe to call from any goroutine — the
// web UI's PUT /api/midi/devices handler calls it directly on a running
// daemon. Takes effect on the next poll tick (at most PollInterval
// away), same as any other hotplug change; it does not force an
// immediate re-tick.
//
// Emptying the list silences every keyboard, so it is logged at Warn —
// the running-daemon counterpart to cmd/polyclav's startup banner.
func (m *Multiplexer) SetAllow(names []string) {
	m.mu.Lock()
	m.allow = append([]string(nil), names...)
	m.lower = lowerAll(names)
	m.mu.Unlock()
	if !hasNonEmpty(names) {
		m.logger.Warn("midi: no input devices selected — no keyboard will send notes until one is (see [midi].allow_devices)")
	}
}

// Allow reports the currently-active allowlist, in the original case it
// was set with.
func (m *Multiplexer) Allow() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.allow...)
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
	m.checkIdle()
	for {
		select {
		case <-ctx.Done():
			m.closeAll()
			return nil
		case <-ticker.C:
			m.tick(ctx)
			m.checkIdle()
		}
	}
}

func (m *Multiplexer) tick(ctx context.Context) {
	names, err := m.cfg.PortLister()
	if err != nil {
		m.logger.Warn("midi multiplexer port list", "err", err)
		return
	}
	if !equalStrings(names, m.lastPortNames) {
		m.logger.Debug("midi multiplexer port list changed", "ports", names)
		m.lastPortNames = append([]string(nil), names...)
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

// wantedPorts keeps only the names matching lower (a snapshot of the
// live allowlist substrings — see SetAllow). An empty allowlist wants
// nothing.
func (m *Multiplexer) wantedPorts(names []string, lower []string) map[string]bool {
	wanted := make(map[string]bool, len(names))
	for _, n := range names {
		if classifyOne(n, lower) == PortSendingNotes {
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
	// PortSendingNotes: named by the allowlist — this port is currently
	// feeding the synth.
	PortSendingNotes PortStatus = "notes"
	// PortUnselected: absent from the allowlist. The default state of
	// every port, including on a fresh install — nothing sends notes
	// until it is explicitly selected.
	PortUnselected PortStatus = "unselected"
	// PortDAWOnly: unselected, AND shaped like a Launchkey-style DAW
	// control-surface port (see looksLikeDAWPort) — a hint that this one
	// carries knob/fader CC rather than keys, so it's usually not what
	// you want to select. Advisory only: an explicit allowlist entry
	// still opens it (that's how OSC bindings read the raw CC stream).
	PortDAWOnly PortStatus = "daw"
	// PortLoopback: unselected, AND shaped like ALSA's "Midi Through"
	// virtual patchbay port (see looksLikeLoopbackPort) — the same kind
	// of advisory hint as PortDAWOnly. Selecting it echoes whatever else
	// is on the bus back into the synth, which is almost never intended.
	PortLoopback PortStatus = "loopback"
)

// PortInfo is one classified port name, for display only.
type PortInfo struct {
	Name   string
	Status PortStatus
}

// ClassifyPorts classifies every name in names against allow, sharing
// classifyOne with wantedPorts so the CLI (`polyclav midi list`) and the
// web devices panel can never disagree with what the Multiplexer is
// actually doing. allow entries are matched case-insensitively as
// SUBSTRINGS, same as SetAllow.
func ClassifyPorts(names []string, allow []string) []PortInfo {
	lower := lowerAll(allow)
	out := make([]PortInfo, len(names))
	for i, n := range names {
		out[i] = PortInfo{Name: n, Status: classifyOne(n, lower)}
	}
	return out
}

// classifyOne is the single source of truth behind both wantedPorts'
// pass/fail decision and ClassifyPorts' descriptive label. lower is a
// lowercased list of allowlist substrings.
//
// Only PortSendingNotes opens a port. The DAW/loopback labels are
// reached solely on the unselected path — an explicit allowlist entry
// deliberately outranks both heuristics (see MultiplexerConfig.Allow).
func classifyOne(name string, lower []string) PortStatus {
	if containsAny(strings.ToLower(name), lower) {
		return PortSendingNotes
	}
	switch {
	case looksLikeDAWPort(name):
		return PortDAWOnly
	case looksLikeLoopbackPort(name):
		return PortLoopback
	default:
		return PortUnselected
	}
}

// containsAny reports whether ln (an already-lowercased port name)
// contains any of substrs (already-lowercased allowlist entries) —
// substring, not exact, so an allow_devices entry survives a
// replug/reboot ALSA-address change (docs/MIDI_DEVICE_MATCHING.md). An
// empty entry is skipped rather than treated as a match-everything
// wildcard, so a stray "" can never silently open every port on the
// machine.
func containsAny(ln string, substrs []string) bool {
	for _, s := range substrs {
		if s != "" && strings.Contains(ln, s) {
			return true
		}
	}
	return false
}

// hasNonEmpty reports whether names holds at least one entry that could
// ever match a port — the same "" skip containsAny applies, so a list of
// nothing but empty strings counts as no selection at all.
func hasNonEmpty(names []string) bool {
	for _, n := range names {
		if n != "" {
			return true
		}
	}
	return false
}

func (m *Multiplexer) open(ctx context.Context, name string) {
	portCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})
	self := &muxPort{cancel: cancel, done: done, lastEventAt: time.Now()}

	m.mu.Lock()
	m.ports[name] = self
	m.mu.Unlock()

	m.logger.Info("midi multiplexer port opened", "port", name)
	// Wrap the shared sink to stamp this port's own liveness (see
	// MultiplexerConfig.IdleThreshold) before forwarding — events carry
	// no per-port identity of their own, so this is the only place that
	// knows which port an event just arrived on.
	stampActivity := func() {
		self.activityMu.Lock()
		self.lastEventAt = time.Now()
		self.idleAlerted = false
		self.activityMu.Unlock()
	}
	wrappedRawSink := func(raw []byte) {
		stampActivity()
		if m.cfg.RawSink != nil {
			m.cfg.RawSink(NewRawEvent("performance", name, raw))
		}
	}
	wrappedSink := func(ev Event) {
		stampActivity()
		if m.cfg.Sink != nil {
			m.cfg.Sink(ev)
		}
	}
	go func() {
		defer close(done)
		err := m.cfg.Opener(portCtx, m.logger, name, wrappedSink, wrappedRawSink)

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

// checkIdle implements MultiplexerConfig.IdleThreshold — see its doc
// comment. No-op per port when disabled or already alerted for the
// current silent stretch.
func (m *Multiplexer) checkIdle() {
	if m.cfg.IdleThreshold <= 0 {
		return
	}
	m.mu.Lock()
	ports := make(map[string]*muxPort, len(m.ports))
	for name, p := range m.ports {
		ports[name] = p
	}
	m.mu.Unlock()

	for name, p := range ports {
		p.activityMu.Lock()
		idle := time.Since(p.lastEventAt)
		shouldAlert := idle >= m.cfg.IdleThreshold && !p.idleAlerted
		if shouldAlert {
			p.idleAlerted = true
		}
		p.activityMu.Unlock()
		if !shouldAlert {
			continue
		}
		names, _ := m.cfg.PortLister()
		m.logger.Warn("midi multiplexer idle watchdog: no traffic for a while",
			"port", name, "idle", idle.Round(time.Second), "current_ports", names)
	}
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
