package midi

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"strings"
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

// newTestMultiplexer builds a Multiplexer over rig with the given
// allowlist. allow is explicit in every call because it is the only
// selection knob: an empty allowlist opens nothing at all.
func newTestMultiplexer(rig *fakeMuxRig, allow []string, sink Sink) *Multiplexer {
	if sink == nil {
		sink = func(Event) {}
	}
	return NewMultiplexer(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		MultiplexerConfig{
			Allow:        allow,
			PollInterval: 5 * time.Millisecond,
			Sink:         sink,
			PortLister:   rig.lister,
			Opener:       rig.opener,
		},
	)
}

// allowKeyboards is the allowlist for tests about hotplug/lifecycle
// rather than selection itself — every generic fake port below is named
// "... Keyboard ..." or "... Synth ...", so these two substrings select
// them all. Tests that DO exercise selection pass their own list.
var allowKeyboards = []string{"Keyboard", "Synth"}

func TestMultiplexerOpensAndClosesPerPort(t *testing.T) {
	rig := newFakeMuxRig()
	var noteCount atomic.Int32
	m := newTestMultiplexer(rig, allowKeyboards, func(Event) { noteCount.Add(1) })
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
	m := newTestMultiplexer(rig, allowKeyboards, nil)
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

// TestMultiplexerOpensNothingWithAnEmptyAllowlist is the headline
// property of the allowlist model: a machine full of connected keyboards
// stays completely silent until one is explicitly selected. cmd/polyclav
// pairs this with a loud startup banner so the silence is explained.
func TestMultiplexerOpensNothingWithAnEmptyAllowlist(t *testing.T) {
	rig := newFakeMuxRig()
	rig.setNames([]string{"Keyboard A", "Keyboard B", "Yamaha P-125"})
	m := newTestMultiplexer(rig, nil, nil)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	time.Sleep(30 * time.Millisecond) // several poll ticks: every chance to (wrongly) open
	if got := m.PortCount(); got != 0 {
		t.Errorf("PortCount with an empty allowlist = %d, want 0 (%v)", got, m.OpenPorts())
	}
}

func TestMultiplexerAllowlistSelectsOnlyNamedPorts(t *testing.T) {
	rig := newFakeMuxRig()
	rig.setNames([]string{"Yamaha P-125", "Some Other Synth"})
	m := newTestMultiplexer(rig, []string{"yamaha"}, nil)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	waitMuxCondition(t, func() bool { return rig.isActive("Yamaha P-125") }, "selected port opens")
	time.Sleep(30 * time.Millisecond)
	if rig.isActive("Some Other Synth") {
		t.Error("an unselected port must not open")
	}
	if got := m.PortCount(); got != 1 {
		t.Errorf("PortCount = %d, want 1", got)
	}
}

func TestMultiplexerLeavesDAWAndLoopbackPortsUnselected(t *testing.T) {
	// Selecting a keyboard must not drag in the Launchkey's DAW
	// control-surface port or ALSA's "Midi Through" loopback: neither
	// carries playing, and internal/midiprobe / internal/web's
	// real-hardware loopback tests deliberately push test traffic through
	// the latter while this project's dev jails share the host's real
	// ALSA sequencer bus (see looksLikeLoopbackPort's doc comment).
	rig := newFakeMuxRig()
	rig.setNames([]string{
		"Launchkey MK4 61 MIDI In",
		"Launchkey MK4 61 DAW In",
		"Midi Through:Midi Through Port-0 14:0",
	})
	m := newTestMultiplexer(rig, []string{"Launchkey MK4 61 MIDI"}, nil)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	waitMuxCondition(t, func() bool { return rig.isActive("Launchkey MK4 61 MIDI In") }, "the selected MIDI port opens")
	time.Sleep(30 * time.Millisecond) // every chance for the others to (wrongly) open
	if rig.isActive("Launchkey MK4 61 DAW In") {
		t.Error("the DAW-role port must stay closed when it isn't on the allowlist")
	}
	if rig.isActive("Midi Through:Midi Through Port-0 14:0") {
		t.Error("the Midi Through loopback port must stay closed when it isn't on the allowlist")
	}
	if got := m.PortCount(); got != 1 {
		t.Errorf("PortCount = %d, want 1", got)
	}
}

func TestMultiplexerAllowlistOverridesTheDAWHeuristic(t *testing.T) {
	// docs/USER_GUIDE.md documents binding OSC to a Launchkey's raw DAW
	// CC stream -- naming that port explicitly must reach it. An explicit
	// selection outranks the name heuristic.
	rig := newFakeMuxRig()
	rig.setNames([]string{"Launchkey MK4 61 MIDI In", "Launchkey MK4 61 DAW In"})
	m := newTestMultiplexer(rig, []string{"DAW"}, nil)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	waitMuxCondition(t, func() bool { return rig.isActive("Launchkey MK4 61 DAW In") }, "an explicit allowlist entry opens the DAW port")
	if rig.isActive("Launchkey MK4 61 MIDI In") {
		t.Error(`allow=["DAW"] must not also open the non-matching MIDI port`)
	}
	if got := m.PortCount(); got != 1 {
		t.Errorf("PortCount = %d, want 1", got)
	}
}

func TestMultiplexerOpenPortsSorted(t *testing.T) {
	rig := newFakeMuxRig()
	rig.setNames([]string{"Zeta Synth", "Alpha Synth"})
	m := newTestMultiplexer(rig, allowKeyboards, nil)
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
	m := newTestMultiplexer(rig, allowKeyboards, nil)
	cancel, done := runMultiplexer(t, m)

	waitMuxCondition(t, func() bool { return m.PortCount() == 2 }, "both ports open")
	stopMultiplexer(t, cancel, done)

	if rig.isActive("Keyboard A") || rig.isActive("Keyboard B") {
		t.Error("Run's shutdown must close every open port")
	}
}

// ---- Allow (allowlist) --------------------------------------------------

func TestMultiplexerAllowSelectsAtConstruction(t *testing.T) {
	rig := newFakeMuxRig()
	rig.setNames([]string{"Keyboard A", "Keyboard B"})
	m := NewMultiplexer(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		MultiplexerConfig{
			PollInterval: 5 * time.Millisecond,
			Sink:         func(Event) {},
			PortLister:   rig.lister,
			Opener:       rig.opener,
			Allow:        []string{"Keyboard B"},
		},
	)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	waitMuxCondition(t, func() bool { return rig.isActive("Keyboard B") }, "B opens")
	time.Sleep(30 * time.Millisecond)
	if rig.isActive("Keyboard A") {
		t.Error("Keyboard A must stay closed — it isn't on the initial Allow list")
	}
	if got := m.PortCount(); got != 1 {
		t.Errorf("PortCount = %d, want 1", got)
	}
}

func TestMultiplexerSetAllowClosesAndOpensLive(t *testing.T) {
	rig := newFakeMuxRig()
	rig.setNames([]string{"Keyboard A", "Keyboard B"})
	m := newTestMultiplexer(rig, allowKeyboards, nil)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	waitMuxCondition(t, func() bool { return m.PortCount() == 2 }, "both ports open")

	// Narrowing the selection closes the dropped port without touching
	// the one still selected.
	m.SetAllow([]string{"Keyboard B"})
	waitMuxCondition(t, func() bool { return !rig.isActive("Keyboard A") }, "A closes once deselected")
	if !rig.isActive("Keyboard B") {
		t.Error("deselecting A must not affect B")
	}
	if got := m.PortCount(); got != 1 {
		t.Errorf("PortCount after SetAllow = %d, want 1", got)
	}

	// Re-selecting re-opens it, live, without a restart.
	m.SetAllow(allowKeyboards)
	waitMuxCondition(t, func() bool { return rig.isActive("Keyboard A") }, "A reopens once re-selected")
	if got := m.PortCount(); got != 2 {
		t.Errorf("PortCount after re-selecting = %d, want 2", got)
	}

	// Clearing it entirely silences everything — the live counterpart of
	// booting with an empty [midi].allow_devices.
	m.SetAllow(nil)
	waitMuxCondition(t, func() bool { return m.PortCount() == 0 }, "clearing the allowlist closes every port")
}

func TestMultiplexerSetAllowIsCaseInsensitiveSubstringMatch(t *testing.T) {
	rig := newFakeMuxRig()
	rig.setNames([]string{"Keyboard A", "Keyboard B"})
	m := newTestMultiplexer(rig, nil, nil)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	// A substring shared by both names selects both -- substring, not
	// exact, match (docs/MIDI_DEVICE_MATCHING.md).
	m.SetAllow([]string{"Keyboard"})
	waitMuxCondition(t, func() bool { return rig.isActive("Keyboard A") && rig.isActive("Keyboard B") }, "both open on shared substring")

	// Different case, still matches.
	m.SetAllow([]string{"keyboard a"})
	waitMuxCondition(t, func() bool { return rig.isActive("Keyboard A") && !rig.isActive("Keyboard B") }, "A stays, B closes, on case-insensitive substring match")
}

func TestMultiplexerSetAllowEmptyEntryDoesNotMatchEverything(t *testing.T) {
	rig := newFakeMuxRig()
	rig.setNames([]string{"Keyboard A"})
	m := newTestMultiplexer(rig, nil, nil)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	m.SetAllow([]string{""})
	time.Sleep(30 * time.Millisecond)
	if rig.isActive("Keyboard A") {
		t.Error("an empty Allow entry must not act as a match-everything wildcard")
	}
}

// TestMultiplexerSetAllowEmptyWarns pins the running-daemon half of the
// "why is it silent?" answer: unchecking the last device in the web UI
// must leave a trace in the log, not just quietly stop the music.
func TestMultiplexerSetAllowEmptyWarns(t *testing.T) {
	var logBuf syncBuffer
	m := NewMultiplexer(slog.New(slog.NewTextHandler(&logBuf, nil)), MultiplexerConfig{
		Sink: func(Event) {},
	})

	m.SetAllow([]string{"Keyboard A"})
	if strings.Contains(logBuf.String(), "no input devices selected") {
		t.Error("selecting a device must not warn about an empty selection")
	}

	m.SetAllow(nil)
	if !strings.Contains(logBuf.String(), "no input devices selected") {
		t.Errorf("clearing the allowlist must warn; log was %q", logBuf.String())
	}

	// A list of nothing but empty strings can never match a port, so it
	// is the same "nothing selected" state and must warn the same way.
	var blankBuf syncBuffer
	blank := NewMultiplexer(slog.New(slog.NewTextHandler(&blankBuf, nil)), MultiplexerConfig{
		Sink: func(Event) {},
	})
	blank.SetAllow([]string{""})
	if !strings.Contains(blankBuf.String(), "no input devices selected") {
		t.Errorf(`SetAllow([]string{""}) must warn; log was %q`, blankBuf.String())
	}
}

func TestMultiplexerAllowRoundTrip(t *testing.T) {
	m := NewMultiplexer(slog.New(slog.NewTextHandler(io.Discard, nil)), MultiplexerConfig{
		Sink: func(Event) {},
	})
	if got := m.Allow(); len(got) != 0 {
		t.Errorf("Allow() on a fresh Multiplexer = %v, want empty", got)
	}
	m.SetAllow([]string{"Some Synth", "Other Synth"})
	got := m.Allow()
	want := []string{"Some Synth", "Other Synth"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Allow() = %v, want %v (original case, input order preserved)", got, want)
	}
}

// ---- idle watchdog --------------------------------------------------------

// syncBuffer wraps bytes.Buffer with a mutex so a test can safely read log
// output written from the Multiplexer's own Run() goroutine.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func TestMultiplexerIdleWatchdogFiresOnceThenStaysQuiet(t *testing.T) {
	rig := newFakeMuxRig()
	var logBuf syncBuffer
	m := NewMultiplexer(
		slog.New(slog.NewTextHandler(&logBuf, nil)),
		MultiplexerConfig{
			PollInterval:  5 * time.Millisecond,
			IdleThreshold: 20 * time.Millisecond,
			Allow:         allowKeyboards,
			Sink:          func(Event) {},
			PortLister:    rig.lister,
			Opener:        rig.opener,
		},
	)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	rig.setNames([]string{"Some Synth"})
	waitMuxCondition(t, func() bool { return rig.isActive("Some Synth") }, "port opens")

	// fakeMuxRig.opener sends exactly one event at open time, then goes
	// silent -- past IdleThreshold, one incident line should appear.
	waitMuxCondition(t, func() bool {
		return strings.Contains(logBuf.String(), "midi multiplexer idle watchdog")
	}, "idle watchdog fires")

	// Edge-triggered: staying silent well past another threshold window
	// must NOT produce a second incident line.
	time.Sleep(60 * time.Millisecond)
	got := strings.Count(logBuf.String(), "midi multiplexer idle watchdog")
	if got != 1 {
		t.Errorf("idle watchdog log count = %d, want exactly 1 (edge-triggered)", got)
	}
}

func TestMultiplexerIdleWatchdogDisabledByDefault(t *testing.T) {
	rig := newFakeMuxRig()
	var logBuf syncBuffer
	m := NewMultiplexer(
		slog.New(slog.NewTextHandler(&logBuf, nil)),
		MultiplexerConfig{
			PollInterval: 5 * time.Millisecond,
			// IdleThreshold left at zero: watchdog must stay off.
			Allow:      allowKeyboards,
			Sink:       func(Event) {},
			PortLister: rig.lister,
			Opener:     rig.opener,
		},
	)
	cancel, done := runMultiplexer(t, m)
	defer stopMultiplexer(t, cancel, done)

	rig.setNames([]string{"Some Synth"})
	waitMuxCondition(t, func() bool { return rig.isActive("Some Synth") }, "port opens")
	time.Sleep(50 * time.Millisecond)

	if strings.Contains(logBuf.String(), "idle watchdog") {
		t.Error("idle watchdog logged with IdleThreshold=0 (should be disabled)")
	}
}

// ---- ClassifyPorts / classifyOne -----------------------------------------

func TestClassifyPortsLabelsSelectionAndHints(t *testing.T) {
	names := []string{
		"Launchkey MK4 61 MIDI In",
		"Launchkey MK4 61 DAW In",
		"Midi Through:Midi Through Port-0 14:0",
		"Yamaha P-125",
	}
	// One device selected. The rest are unselected -- but the DAW and
	// loopback ports carry their more specific advisory label so the CLI
	// and the web panel can say "you probably don't want this one".
	got := ClassifyPorts(names, []string{"Yamaha P-125"})
	want := map[string]PortStatus{
		"Launchkey MK4 61 MIDI In":              PortUnselected,
		"Launchkey MK4 61 DAW In":               PortDAWOnly,
		"Midi Through:Midi Through Port-0 14:0": PortLoopback,
		"Yamaha P-125":                          PortSendingNotes,
	}
	if len(got) != len(want) {
		t.Fatalf("ClassifyPorts returned %d entries, want %d", len(got), len(want))
	}
	for _, info := range got {
		if info.Status != want[info.Name] {
			t.Errorf("%s: status = %s, want %s", info.Name, info.Status, want[info.Name])
		}
	}
}

func TestClassifyPortsEmptyAllowlistSelectsNothing(t *testing.T) {
	// The fresh-install state: ports are connected, none is selected.
	got := ClassifyPorts([]string{"Yamaha P-125", "Casio Keyboard"}, nil)
	for _, info := range got {
		if info.Status != PortUnselected {
			t.Errorf("%s: status = %s, want %s with an empty allowlist", info.Name, info.Status, PortUnselected)
		}
	}
}

func TestClassifyPortsAllowlistOverridesDAWAndLoopbackHints(t *testing.T) {
	// Naming either port explicitly is trusted as intentional -- the
	// heuristics are hints about UNSELECTED ports, never a veto.
	got := ClassifyPorts([]string{"Launchkey MK4 61 DAW In"}, []string{"DAW"})
	if len(got) != 1 || got[0].Status != PortSendingNotes {
		t.Errorf("ClassifyPorts with the DAW port explicitly allowed = %+v, want PortSendingNotes", got)
	}

	got = ClassifyPorts([]string{"Midi Through:Midi Through Port-0 14:0"}, []string{"Midi Through"})
	if len(got) != 1 || got[0].Status != PortSendingNotes {
		t.Errorf("ClassifyPorts with the loopback port explicitly allowed = %+v, want PortSendingNotes", got)
	}
}

func TestClassifyPortsAllowCaseInsensitiveSubstring(t *testing.T) {
	got := ClassifyPorts([]string{"Some Synth"}, []string{"some synth"})
	if len(got) != 1 || got[0].Status != PortSendingNotes {
		t.Errorf("ClassifyPorts with a different-case exact allow entry = %+v, want PortSendingNotes", got)
	}

	// A stable substring (not the full, address-suffixed name) must
	// match -- this is the whole point: docs/MIDI_DEVICE_MATCHING.md.
	got = ClassifyPorts([]string{"Some Synth"}, []string{"Some"})
	if len(got) != 1 || got[0].Status != PortSendingNotes {
		t.Errorf("ClassifyPorts with a substring allow entry = %+v, want PortSendingNotes", got)
	}

	// An ALSA-style trailing address doesn't defeat a substring that
	// omits it -- the acceptance scenario from the handoff doc.
	got = ClassifyPorts([]string{"CASIO USB-MIDI:CASIO USB-MIDI MIDI 1 36:0"}, []string{"CASIO USB-MIDI"})
	if len(got) != 1 || got[0].Status != PortSendingNotes {
		t.Errorf("ClassifyPorts with a stable-name substring vs. an address-suffixed port = %+v, want PortSendingNotes", got)
	}

	// A non-matching substring must not select an unrelated port.
	got = ClassifyPorts([]string{"Launchkey MK4 61 MIDI In"}, []string{"CASIO USB-MIDI"})
	if len(got) != 1 || got[0].Status != PortUnselected {
		t.Errorf("ClassifyPorts with a non-matching allow entry = %+v, want PortUnselected", got)
	}
}
