package midiprobe

import (
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/controls"
)

// fakeConn is a rawConn that never touches real MIDI hardware: Send
// records bytes, and Listen captures the sink so a test can inject
// inbound messages by calling inject directly (synchronously) or via
// autoReply (asynchronously, simulating a real device's response
// latency).
type fakeConn struct {
	mu    sync.Mutex
	sent  [][]byte
	onMsg func(raw []byte)
	// autoReply, if set, is checked on every Send: if match(sent) is
	// true, reply is delivered to onMsg on a short-lived goroutine (never
	// synchronously — mirrors a real device's async response).
	autoReplyMatch func(sent []byte) bool
	autoReply      []byte
	sendErr        error
	closed         bool
}

func (f *fakeConn) Listen(onMsg func(raw []byte)) (func(), error) {
	f.mu.Lock()
	f.onMsg = onMsg
	f.mu.Unlock()
	return func() {}, nil
}

func (f *fakeConn) Send(raw []byte) error {
	if f.sendErr != nil {
		return f.sendErr
	}
	f.mu.Lock()
	f.sent = append(f.sent, append([]byte(nil), raw...))
	match := f.autoReplyMatch
	reply := f.autoReply
	cb := f.onMsg
	f.mu.Unlock()
	if match != nil && match(raw) && cb != nil {
		go func() {
			time.Sleep(5 * time.Millisecond) // simulate real device latency
			cb(reply)
		}()
	}
	return nil
}

func (f *fakeConn) Close() error {
	f.closed = true
	return nil
}

func (f *fakeConn) inject(raw []byte) {
	f.mu.Lock()
	cb := f.onMsg
	f.mu.Unlock()
	if cb != nil {
		cb(raw)
	}
}

// newTestSession returns a Session wired to a fresh fakeConn; open always
// succeeds and returns that fake regardless of the requested port names
// (tests don't exercise ErrPortNotFound here — that's realConn's job,
// covered by openConn's own logic which is exercised at runtime, not unit
// tested against real hardware).
func newTestSession() (*Session, *fakeConn) {
	fake := &fakeConn{}
	s := NewSession(controls.NewHub(), nil)
	s.open = func(inName, outName string) (rawConn, error) {
		return fake, nil
	}
	return s, fake
}

func TestSessionStartStopLifecycle(t *testing.T) {
	s, fake := newTestSession()

	if err := s.Start("Device In", "Device Out", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	st := s.Status()
	if !st.Active || st.InPort != "Device In" || st.OutPort != "Device Out" {
		t.Errorf("status after start = %+v", st)
	}
	if st.BufferCap != defaultBufferCap {
		t.Errorf("bufferCap = %d, want default %d", st.BufferCap, defaultBufferCap)
	}

	if err := s.Start("Device In", "Device Out", 0); !errors.Is(err, ErrAlreadyRunning) {
		t.Errorf("second Start = %v, want ErrAlreadyRunning", err)
	}

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}
	if !fake.closed {
		t.Error("Stop must close the underlying connection")
	}
	if s.Status().Active {
		t.Error("status must report inactive after Stop")
	}

	if err := s.Stop(); !errors.Is(err, ErrNotRunning) {
		t.Errorf("second Stop = %v, want ErrNotRunning", err)
	}
}

func TestSessionIngestDecodesAndBuffers(t *testing.T) {
	s, fake := newTestSession()
	if err := s.Start("In", "Out", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	fake.inject([]byte{0x90, 60, 100}) // note-on, seq 0
	fake.inject([]byte{0xB0, 74, 20})  // cc, seq 1
	fake.inject([]byte{0xC0, 5})       // program change, seq 2

	events := s.Events(0) // since=0 is the "everything" sentinel
	if len(events) != 3 {
		t.Fatalf("got %d events, want 3", len(events))
	}
	if events[0].Kind != KindNoteOn || events[0].Seq != 0 || events[0].Port != "In" {
		t.Errorf("event[0] = %+v", events[0])
	}
	if events[1].Kind != KindControlChange || events[1].Seq != 1 {
		t.Errorf("event[1] = %+v", events[1])
	}
	if events[2].Kind != KindProgramChange || events[2].Seq != 2 {
		t.Errorf("event[2] = %+v", events[2])
	}

	// Events(since=1) excludes seq 0 and 1, leaving only the strictly-later seq 2.
	onlyLast := s.Events(1)
	if len(onlyLast) != 1 || onlyLast[0].Seq != 2 {
		t.Errorf("Events(since=1) = %+v, want only seq 2", onlyLast)
	}
}

func TestSessionRingBufferDropsOldest(t *testing.T) {
	s, fake := newTestSession()
	if err := s.Start("In", "Out", 3); err != nil {
		t.Fatalf("Start: %v", err)
	}
	for i := 0; i < 5; i++ {
		fake.inject([]byte{0xB0, byte(i), 0})
	}
	events := s.Events(0)
	if len(events) != 3 {
		t.Fatalf("got %d buffered events, want cap 3", len(events))
	}
	// The oldest two (seq 0,1) must have been dropped; seq 2,3,4 remain.
	for i, want := range []uint64{2, 3, 4} {
		if events[i].Seq != want {
			t.Errorf("events[%d].Seq = %d, want %d", i, events[i].Seq, want)
		}
	}
}

func TestSessionBeginLabelTagsEventsInWindow(t *testing.T) {
	s, fake := newTestSession()
	if err := s.Start("In", "Out", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	if err := s.BeginLabel("Knob 1", 30*time.Millisecond); err != nil {
		t.Fatalf("BeginLabel: %v", err)
	}
	fake.inject([]byte{0xB0, 20, 64}) // within window

	time.Sleep(50 * time.Millisecond) // let the window lapse
	fake.inject([]byte{0xB0, 21, 64}) // after window

	events := s.Events(0)
	if len(events) != 2 {
		t.Fatalf("got %d events, want 2", len(events))
	}
	if events[0].Label != "Knob 1" {
		t.Errorf("event within window: label = %q, want %q", events[0].Label, "Knob 1")
	}
	if events[1].Label != "" {
		t.Errorf("event after window: label = %q, want empty", events[1].Label)
	}
	if s.Status().Labeling {
		t.Error("labeling should have lazily expired")
	}
}

func TestSessionBeginLabelConflictAndNotRunning(t *testing.T) {
	s, _ := newTestSession()

	if err := s.BeginLabel("X", time.Second); !errors.Is(err, ErrNotRunning) {
		t.Errorf("BeginLabel before Start = %v, want ErrNotRunning", err)
	}

	if err := s.Start("In", "Out", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.BeginLabel("Knob 1", time.Second); err != nil {
		t.Fatalf("first BeginLabel: %v", err)
	}
	if err := s.BeginLabel("Knob 2", time.Second); !errors.Is(err, ErrLabelInProgress) {
		t.Errorf("overlapping BeginLabel = %v, want ErrLabelInProgress", err)
	}
	if err := s.BeginLabel("", time.Second); err == nil {
		t.Error("empty label must be rejected")
	}
}

func TestSessionIdentityRequestReceivesReply(t *testing.T) {
	s, fake := newTestSession()
	if err := s.Start("In", "Out", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}

	reply := []byte{0xF0, 0x7E, 0x7F, 0x06, 0x02, 0x00, 0x20, 0x6B, 0x01, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0xF7}
	fake.autoReply = reply
	fake.autoReplyMatch = func(sent []byte) bool {
		return len(sent) >= 5 && sent[0] == 0xF0 && sent[3] == 0x06 && sent[4] == 0x01
	}

	result, err := s.IdentityRequest(0x7F, 500*time.Millisecond)
	if err != nil {
		t.Fatalf("IdentityRequest: %v", err)
	}
	if result.TimedOut {
		t.Fatal("expected a reply, got TimedOut")
	}
	if result.ManufacturerName != "Arturia" {
		t.Errorf("manufacturer = %q, want Arturia", result.ManufacturerName)
	}
	if len(fake.sent) != 1 {
		t.Fatalf("expected exactly one sent message (the request), got %d", len(fake.sent))
	}

	// The reply must NOT also show up as a plain ingested event competing
	// with the identity waiter — it still lands in the normal event log
	// too (ingest always appends), just also routed to the waiter.
	events := s.Events(0)
	if len(events) != 1 || events[0].Kind != KindSysEx {
		t.Errorf("events = %+v, want exactly one sysex event", events)
	}
}

func TestSessionIdentityRequestTimeout(t *testing.T) {
	s, _ := newTestSession()
	if err := s.Start("In", "Out", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	result, err := s.IdentityRequest(0x7F, 20*time.Millisecond)
	if err != nil {
		t.Fatalf("IdentityRequest: %v", err)
	}
	if !result.TimedOut {
		t.Error("expected TimedOut with no reply configured")
	}
}

func TestSessionIdentityRequestNotRunning(t *testing.T) {
	s, _ := newTestSession()
	if _, err := s.IdentityRequest(0x7F, time.Second); !errors.Is(err, ErrNotRunning) {
		t.Errorf("IdentityRequest before Start = %v, want ErrNotRunning", err)
	}
}

func TestSessionSendRaw(t *testing.T) {
	s, fake := newTestSession()
	if err := s.SendRaw([]byte{0xF0, 0xF7}); !errors.Is(err, ErrNotRunning) {
		t.Errorf("SendRaw before Start = %v, want ErrNotRunning", err)
	}

	if err := s.Start("In", "Out", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.SendRaw([]byte{0xF0, 0x01, 0xF7}); err != nil {
		t.Fatalf("SendRaw: %v", err)
	}
	if len(fake.sent) != 1 || string(fake.sent[0]) != string([]byte{0xF0, 0x01, 0xF7}) {
		t.Errorf("sent = %v, want the raw bytes forwarded verbatim", fake.sent)
	}
}

func TestSessionExportNothingCaptured(t *testing.T) {
	s, _ := newTestSession()
	if _, err := s.Export(); err == nil {
		t.Error("Export with nothing captured must error")
	}
}

func TestSessionExportAfterCapture(t *testing.T) {
	s, fake := newTestSession()
	if err := s.Start("In", "Out", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if err := s.BeginLabel("Knob 1", time.Second); err != nil {
		t.Fatalf("BeginLabel: %v", err)
	}
	fake.inject([]byte{0xB0, 20, 64})

	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	profile, err := s.Export()
	if err != nil {
		t.Fatalf("Export: %v", err)
	}
	if profile.InPort != "In" || profile.OutPort != "Out" {
		t.Errorf("profile ports = %q/%q", profile.InPort, profile.OutPort)
	}
	if len(profile.Events) != 1 || profile.Events[0].Label != "Knob 1" {
		t.Errorf("profile events = %+v", profile.Events)
	}
	if len(profile.DistinctLabels) != 1 || profile.DistinctLabels[0] != "Knob 1" {
		t.Errorf("distinct labels = %v", profile.DistinctLabels)
	}
	// Export enumerates live ports too (best-effort context) — in this
	// jail that's real ALSA enumeration (e.g. "Midi Through"), so just
	// assert it didn't error into a nil/empty-with-panic state.
	if profile.AllInPorts == nil {
		t.Error("AllInPorts should be populated (even if just the virtual through port)")
	}
}

func TestSessionStartResetsHistoryOnFreshConnect(t *testing.T) {
	s, fake := newTestSession()
	if err := s.Start("In", "Out", 0); err != nil {
		t.Fatalf("Start: %v", err)
	}
	fake.inject([]byte{0x90, 60, 100})
	if len(s.Events(0)) != 1 {
		t.Fatal("expected one captured event before Stop")
	}
	if err := s.Stop(); err != nil {
		t.Fatalf("Stop: %v", err)
	}

	if err := s.Start("Other In", "Other Out", 0); err != nil {
		t.Fatalf("second Start: %v", err)
	}
	if got := s.Events(0); len(got) != 0 {
		t.Errorf("a fresh Start must clear prior history, got %d events", len(got))
	}
}
