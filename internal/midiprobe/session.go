package midiprobe

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/controls"
	"github.com/mschulkind-oss/polyclav/internal/midi"
	"gitlab.com/gomidi/midi/v2/sysex"
)

var (
	ErrAlreadyRunning  = errors.New("midiprobe: session already running")
	ErrNotRunning      = errors.New("midiprobe: session not running")
	ErrPortNotFound    = errors.New("midiprobe: port not found")
	ErrLabelInProgress = errors.New("midiprobe: a label capture window is already open")
)

// defaultLabelWindow is used when BeginLabel's window is <= 0.
const defaultLabelWindow = 2 * time.Second

// defaultBufferCap is used when Start's bufferCap is <= 0.
const defaultBufferCap = 2000

// SessionStatus is the JSON view of a Session's current state (GET
// /api/probe/status and the "probe-status" SSE event).
type SessionStatus struct {
	Active      bool      `json:"active"`
	InPort      string    `json:"inPort,omitempty"`
	OutPort     string    `json:"outPort,omitempty"`
	StartedAt   time.Time `json:"startedAt,omitempty"`
	EventCount  int       `json:"eventCount"`
	BufferCap   int       `json:"bufferCap"`
	Labeling    bool      `json:"labeling"`
	LabelText   string    `json:"labelText,omitempty"`
	LabelEndsAt time.Time `json:"labelEndsAt,omitempty"`
}

// Session is a generic MIDI device reverse-engineering session: connect to
// an exact-named in/out port pair, record every raw message, let the
// caller tag events with a label, probe with a Universal Identity
// Request, and export everything as a DeviceProfile. One Session serves
// the whole web UI (there is only ever one probe target at a time).
//
// All exported methods are goroutine-safe.
type Session struct {
	hub    *controls.Hub
	logger *slog.Logger
	// open is overridable in tests to inject a fake rawConn with no real
	// MIDI hardware involved; production code always uses openConn.
	open func(inName, outName string) (rawConn, error)
	// listIns/listOuts default to midi.PortNames/midi.OutPortNames;
	// overridable in tests to deterministically exercise ListPorts'
	// degrade-on-failure behavior without depending on whether this host
	// actually has a MIDI subsystem.
	listIns  func() ([]string, error)
	listOuts func() ([]string, error)

	mu            sync.Mutex
	active        bool
	inPort        string
	outPort       string
	startedAt     time.Time
	conn          rawConn
	stopListen    func()
	bufferCap     int
	events        []Event
	nextSeq       uint64
	labelActive   bool
	labelText     string
	labelDeadline time.Time
	identityCh    chan Event
	lastIdentity  *IdentityResult
}

// NewSession returns a Session ready for ListPorts/Start. It opens no
// hardware until Start is called.
func NewSession(hub *controls.Hub, logger *slog.Logger) *Session {
	if logger == nil {
		logger = slog.Default()
	}
	return &Session{hub: hub, logger: logger, open: openConn, listIns: midi.PortNames, listOuts: midi.OutPortNames}
}

// ListPorts enumerates the currently-visible MIDI input and output port
// names. Safe to call whether or not a Session is currently connected.
// Degrades to empty lists — never an error — if the underlying MIDI
// subsystem can't even initialize (e.g. no ALSA sequencer present at all,
// as on some minimal CI runners): from this tool's perspective that's
// indistinguishable from "zero devices," and this is a diagnostic/UI
// endpoint, not a startup-critical path.
func (s *Session) ListPorts() (ins, outs []string) {
	ins, err := s.listIns()
	if err != nil {
		s.logger.Warn("midiprobe: list input ports failed, reporting none", "err", err)
		ins = nil
	}
	outs, err = s.listOuts()
	if err != nil {
		s.logger.Warn("midiprobe: list output ports failed, reporting none", "err", err)
		outs = nil
	}
	return ins, outs
}

// Start opens the exact-named in/out ports and begins recording. bufferCap
// <= 0 uses defaultBufferCap. Resets any previously captured history —
// each Start begins a fresh capture of a (possibly different) device.
func (s *Session) Start(inPort, outPort string, bufferCap int) error {
	s.mu.Lock()
	if s.active {
		s.mu.Unlock()
		return ErrAlreadyRunning
	}
	s.mu.Unlock()

	if bufferCap <= 0 {
		bufferCap = defaultBufferCap
	}

	conn, err := s.open(inPort, outPort)
	if err != nil {
		return err
	}
	stop, err := conn.Listen(func(raw []byte) { s.ingest(inPort, raw) })
	if err != nil {
		_ = conn.Close()
		return fmt.Errorf("listen: %w", err)
	}

	s.mu.Lock()
	if s.active { // lost a race with a concurrent Start
		s.mu.Unlock()
		stop()
		_ = conn.Close()
		return ErrAlreadyRunning
	}
	s.active = true
	s.inPort = inPort
	s.outPort = outPort
	s.startedAt = time.Now()
	s.conn = conn
	s.stopListen = stop
	s.bufferCap = bufferCap
	s.events = nil
	s.nextSeq = 0
	s.labelActive = false
	s.lastIdentity = nil
	status := s.statusLocked()
	s.mu.Unlock()

	s.publishStatus(status)
	return nil
}

// Stop disconnects and stops recording. Captured history survives Stop —
// Events/Export still work — it is cleared only by the next Start.
func (s *Session) Stop() error {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return ErrNotRunning
	}
	stop := s.stopListen
	conn := s.conn
	s.active = false
	s.conn = nil
	s.stopListen = nil
	status := s.statusLocked()
	s.mu.Unlock()

	if stop != nil {
		stop()
	}
	if conn != nil {
		_ = conn.Close()
	}
	s.publishStatus(status)
	return nil
}

// Status returns a snapshot of the session's current state.
func (s *Session) Status() SessionStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.expireLabelLocked()
	return s.statusLocked()
}

func (s *Session) statusLocked() SessionStatus {
	return SessionStatus{
		Active:      s.active,
		InPort:      s.inPort,
		OutPort:     s.outPort,
		StartedAt:   s.startedAt,
		EventCount:  len(s.events),
		BufferCap:   s.bufferCap,
		Labeling:    s.labelActive,
		LabelText:   s.labelText,
		LabelEndsAt: s.labelDeadline,
	}
}

// expireLabelLocked lazily closes a label window whose deadline has
// passed. Must be called with mu held.
func (s *Session) expireLabelLocked() {
	if s.labelActive && !time.Now().Before(s.labelDeadline) {
		s.labelActive = false
	}
}

// Events returns a snapshot of captured events with Seq > since (since=0
// returns everything currently buffered).
func (s *Session) Events(since uint64) []Event {
	s.mu.Lock()
	defer s.mu.Unlock()
	if since == 0 {
		out := make([]Event, len(s.events))
		copy(out, s.events)
		return out
	}
	var out []Event
	for _, e := range s.events {
		if e.Seq > since {
			out = append(out, e)
		}
	}
	return out
}

// BeginLabel opens a label-capture window: every Event ingested while the
// window is open gets Label set to label. This is the core UX for a
// non-technical user: type a name for a control, click once, then move
// just that one control on the physical device. window <= 0 uses
// defaultLabelWindow.
func (s *Session) BeginLabel(label string, window time.Duration) error {
	if label == "" {
		return fmt.Errorf("midiprobe: label must not be empty")
	}
	if window <= 0 {
		window = defaultLabelWindow
	}
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return ErrNotRunning
	}
	s.expireLabelLocked()
	if s.labelActive {
		s.mu.Unlock()
		return ErrLabelInProgress
	}
	s.labelActive = true
	s.labelText = label
	s.labelDeadline = time.Now().Add(window)
	status := s.statusLocked()
	s.mu.Unlock()

	s.publishStatus(status)
	return nil
}

// IdentityRequest sends the MIDI Universal Non-realtime Identity Request
// on the connected port and waits up to timeout for a reply. A timeout is
// NOT an error return — TimedOut:true is meaningful data (some devices
// simply don't implement this). timeout <= 0 defaults to 2s.
func (s *Session) IdentityRequest(channel byte, timeout time.Duration) (*IdentityResult, error) {
	if timeout <= 0 {
		timeout = 2 * time.Second
	}
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return nil, ErrNotRunning
	}
	ch := make(chan Event, 1)
	s.identityCh = ch
	conn := s.conn
	s.mu.Unlock()

	req := sysex.IdentityRequest(channel)
	sentAt := time.Now()
	if err := conn.Send(req); err != nil {
		s.mu.Lock()
		if s.identityCh == ch {
			s.identityCh = nil
		}
		s.mu.Unlock()
		return nil, fmt.Errorf("identity request: send: %w", err)
	}

	result := &IdentityResult{RequestSentAt: sentAt}
	select {
	case ev := <-ch:
		result.ReceivedAt = ev.Time
		result.ReplyRaw = ev.Raw
		decodeIdentityReply(ev.Raw, result)
	case <-time.After(timeout):
		result.TimedOut = true
		s.mu.Lock()
		if s.identityCh == ch {
			s.identityCh = nil
		}
		s.mu.Unlock()
	}

	s.mu.Lock()
	s.lastIdentity = result
	s.mu.Unlock()
	return result, nil
}

// SendRaw writes raw bytes verbatim to the connected output port — the
// "try arbitrary hex against an unknown device" entry point. No framing
// is added; callers building a SysEx message must include F0...F7
// themselves.
func (s *Session) SendRaw(raw []byte) error {
	s.mu.Lock()
	if !s.active {
		s.mu.Unlock()
		return ErrNotRunning
	}
	conn := s.conn
	s.mu.Unlock()
	return conn.Send(raw)
}

// Export snapshots everything captured so far into a portable
// DeviceProfile — the file a user sends back after probing an unfamiliar
// device. Works after Stop, as long as something was captured.
func (s *Session) Export() (DeviceProfile, error) {
	s.mu.Lock()
	if !s.active && len(s.events) == 0 {
		s.mu.Unlock()
		return DeviceProfile{}, fmt.Errorf("midiprobe: nothing captured yet")
	}
	inPort, outPort := s.inPort, s.outPort
	identity := s.lastIdentity
	events := make([]Event, len(s.events))
	copy(events, s.events)
	s.mu.Unlock()

	ins, outs := s.ListPorts() // best-effort context; never blocks export

	var labels []string
	seen := map[string]struct{}{}
	for _, e := range events {
		if e.Label == "" {
			continue
		}
		if _, ok := seen[e.Label]; !ok {
			seen[e.Label] = struct{}{}
			labels = append(labels, e.Label)
		}
	}

	return DeviceProfile{
		ExportedAt:     time.Now(),
		InPort:         inPort,
		OutPort:        outPort,
		AllInPorts:     ins,
		AllOutPorts:    outs,
		Identity:       identity,
		Events:         events,
		DistinctLabels: labels,
	}, nil
}

// ingest decodes one raw message, assigns it a sequence number/timestamp,
// applies an active label window, appends it to the ring buffer (dropping
// the oldest on overflow), forwards it to a pending IdentityRequest
// waiter if it looks like an identity reply, and publishes it on the
// shared Hub. This is the single event pipeline used by both the real
// connection (via Start's Listen callback) and tests (called directly,
// with no real MIDI hardware).
func (s *Session) ingest(port string, raw []byte) {
	ev := decode(raw)

	s.mu.Lock()
	ev.Seq = s.nextSeq
	s.nextSeq++
	ev.Time = time.Now()
	ev.Port = port
	s.expireLabelLocked()
	if s.labelActive {
		ev.Label = s.labelText
	}
	s.events = append(s.events, ev)
	if s.bufferCap > 0 && len(s.events) > s.bufferCap {
		// Force a fresh backing array (the [:0:0] cap-zero trick) so the
		// dropped prefix is actually freed, not just unreachable via this
		// slice header while still pinning the old array alive.
		s.events = append(s.events[:0:0], s.events[len(s.events)-s.bufferCap:]...)
	}
	var idCh chan Event
	if ev.Kind == KindSysEx && isIdentityReply(ev.Raw) && s.identityCh != nil {
		idCh = s.identityCh
		s.identityCh = nil // consume once
	}
	s.mu.Unlock()

	if idCh != nil {
		select {
		case idCh <- ev:
		default:
		}
	}
	if s.hub != nil {
		s.hub.Publish(controls.Change{Type: "probe-event", Data: toMap(ev)})
	}
}

func (s *Session) publishStatus(st SessionStatus) {
	if s.hub != nil {
		s.hub.Publish(controls.Change{Type: "probe-status", Data: toMap(st)})
	}
}

// toMap round-trips v through JSON into a map[string]any — the shape
// controls.Change.Data expects. Simple and correct; probe traffic volume
// is far below where this would matter performance-wise.
func toMap(v any) map[string]any {
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{"error": err.Error()}
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		return map[string]any{"error": err.Error()}
	}
	return m
}
