package midi

import (
	"context"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeMuxRig is a fake PortLister + Opener pair for Multiplexer tests —
// no real rtmidi/hardware needed, mirroring the launchkey.fakeRig
// pattern used for the single-device reconciler.
type fakeMuxRig struct {
	mu    sync.Mutex
	names []string

	openCount  atomic.Int32
	closeCount atomic.Int32

	activeMu sync.Mutex
	active   map[string]bool
}

func newFakeMuxRig() *fakeMuxRig {
	return &fakeMuxRig{active: make(map[string]bool)}
}

func (f *fakeMuxRig) setNames(names []string) {
	f.mu.Lock()
	f.names = names
	f.mu.Unlock()
}

func (f *fakeMuxRig) lister() ([]string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]string, len(f.names))
	copy(out, f.names)
	return out, nil
}

// opener blocks until ctx is cancelled, simulating a live per-port
// listener goroutine; it pushes one event through sink immediately so
// tests can confirm the shared sink is actually wired per port.
func (f *fakeMuxRig) opener(ctx context.Context, _ *slog.Logger, portName string, sink Sink) error {
	f.openCount.Add(1)
	f.activeMu.Lock()
	f.active[portName] = true
	f.activeMu.Unlock()
	defer func() {
		f.activeMu.Lock()
		delete(f.active, portName)
		f.activeMu.Unlock()
		f.closeCount.Add(1)
	}()
	sink(Event{Kind: NoteOn, Note: 60})
	<-ctx.Done()
	return ctx.Err()
}

func (f *fakeMuxRig) isActive(name string) bool {
	f.activeMu.Lock()
	defer f.activeMu.Unlock()
	return f.active[name]
}

func waitMuxCondition(t *testing.T, cond func() bool, label string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitMuxCondition: %s never became true", label)
}

func runMultiplexer(t *testing.T, m *Multiplexer) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = m.Run(ctx)
		close(done)
	}()
	return cancel, done
}

func stopMultiplexer(t *testing.T, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit")
	}
}

func newTestMultiplexer(rig *fakeMuxRig, match string, sink Sink) *Multiplexer {
	if sink == nil {
		sink = func(Event) {}
	}
	return NewMultiplexer(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		MultiplexerConfig{
			Match:        match,
			PollInterval: 5 * time.Millisecond,
			Sink:         sink,
			PortLister:   rig.lister,
			Opener:       rig.opener,
		},
	)
}

func TestMultiplexerOpensAndClosesPerPort(t *testing.T) {
	rig := newFakeMuxRig()
	var noteCount atomic.Int32
	m := newTestMultiplexer(rig, "", func(Event) { noteCount.Add(1) })
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	rig.setNames([]string{"Some Synth"})
	waitMuxCondition(t, func() bool { return m.PortCount() == 1 }, "port opens")
	waitMuxCondition(t, func() bool { return noteCount.Load() > 0 }, "sink receives an event")
	waitMuxCondition(t, func() bool { return rig.isActive("Some Synth") }, "opener sees the port active")

	rig.setNames(nil)
	waitMuxCondition(t, func() bool { return m.PortCount() == 0 }, "port closes")
	waitMuxCondition(t, func() bool { return !rig.isActive("Some Synth") }, "opener sees the port inactive")
}

func TestMultiplexerHandlesMultipleDevicesIndependently(t *testing.T) {
	rig := newFakeMuxRig()
	m := newTestMultiplexer(rig, "", nil)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	rig.setNames([]string{"Keyboard A", "Keyboard B"})
	waitMuxCondition(t, func() bool { return m.PortCount() == 2 }, "both ports open")
	waitMuxCondition(t, func() bool { return rig.isActive("Keyboard A") && rig.isActive("Keyboard B") }, "both active")

	// Unplug just A -- B must be unaffected.
	rig.setNames([]string{"Keyboard B"})
	waitMuxCondition(t, func() bool { return !rig.isActive("Keyboard A") }, "A closes")
	if !rig.isActive("Keyboard B") {
		t.Error("unplugging A must not affect B")
	}
	if got := m.PortCount(); got != 1 {
		t.Errorf("PortCount after unplugging A = %d, want 1", got)
	}

	// Plug A back in -- both active again, independently reopened.
	rig.setNames([]string{"Keyboard A", "Keyboard B"})
	waitMuxCondition(t, func() bool { return rig.isActive("Keyboard A") }, "A reopens")
	if got := m.PortCount(); got != 2 {
		t.Errorf("PortCount after A returns = %d, want 2", got)
	}
}

func TestMultiplexerExcludesDAWRolePortsByDefault(t *testing.T) {
	rig := newFakeMuxRig()
	rig.setNames([]string{"Launchkey MK4 61 MIDI In", "Launchkey MK4 61 DAW In"})
	m := newTestMultiplexer(rig, "", nil)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	waitMuxCondition(t, func() bool { return rig.isActive("Launchkey MK4 61 MIDI In") }, "MIDI port opens")
	time.Sleep(30 * time.Millisecond) // give the DAW port every chance to (wrongly) open too
	if rig.isActive("Launchkey MK4 61 DAW In") {
		t.Error("DAW-role port must be excluded by default (empty Match)")
	}
	if got := m.PortCount(); got != 1 {
		t.Errorf("PortCount = %d, want 1 (DAW port excluded)", got)
	}
}

func TestMultiplexerMatchOverridesDAWExclusion(t *testing.T) {
	// docs/USER_GUIDE.md documents binding OSC to a Launchkey's raw DAW
	// CC stream via port_match = "DAW" -- an explicit Match must still
	// reach a DAW-shaped port, bypassing the default-only exclusion.
	rig := newFakeMuxRig()
	rig.setNames([]string{"Launchkey MK4 61 MIDI In", "Launchkey MK4 61 DAW In"})
	m := newTestMultiplexer(rig, "daw", nil)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	waitMuxCondition(t, func() bool { return rig.isActive("Launchkey MK4 61 DAW In") }, "explicit Match opens the DAW port")
	if rig.isActive("Launchkey MK4 61 MIDI In") {
		t.Error(`Match="daw" must not also open the non-matching MIDI port`)
	}
	if got := m.PortCount(); got != 1 {
		t.Errorf("PortCount = %d, want 1", got)
	}
}

func TestMultiplexerMatchRestrictsToSubstring(t *testing.T) {
	rig := newFakeMuxRig()
	rig.setNames([]string{"Yamaha P-125", "Some Other Synth"})
	m := newTestMultiplexer(rig, "yamaha", nil)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	waitMuxCondition(t, func() bool { return rig.isActive("Yamaha P-125") }, "matching port opens")
	time.Sleep(30 * time.Millisecond)
	if rig.isActive("Some Other Synth") {
		t.Error("non-matching port must not open")
	}
	if got := m.PortCount(); got != 1 {
		t.Errorf("PortCount = %d, want 1", got)
	}
}

func TestMultiplexerOpenPortsSorted(t *testing.T) {
	rig := newFakeMuxRig()
	rig.setNames([]string{"Zeta Synth", "Alpha Synth"})
	m := newTestMultiplexer(rig, "", nil)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	waitMuxCondition(t, func() bool { return m.PortCount() == 2 }, "both ports open")
	got := m.OpenPorts()
	want := []string{"Alpha Synth", "Zeta Synth"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("OpenPorts() = %v, want %v (sorted)", got, want)
	}
}

func TestMultiplexerRunShutsDownAllPortsOnCancel(t *testing.T) {
	rig := newFakeMuxRig()
	rig.setNames([]string{"Keyboard A", "Keyboard B"})
	m := newTestMultiplexer(rig, "", nil)
	cancel, done := runMultiplexer(t, m)

	waitMuxCondition(t, func() bool { return m.PortCount() == 2 }, "both ports open")
	stopMultiplexer(t, cancel, done)

	if rig.isActive("Keyboard A") || rig.isActive("Keyboard B") {
		t.Error("Run's shutdown must close every open port")
	}
}
