package player

import (
	"sync"
	"testing"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/midi"
)

// recSink records every event the player pushes, for assertions.
type recSink struct {
	mu  sync.Mutex
	evs []midi.Event
}

func (r *recSink) push(ev midi.Event) {
	r.mu.Lock()
	r.evs = append(r.evs, ev)
	r.mu.Unlock()
}

func (r *recSink) events() []midi.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]midi.Event, len(r.evs))
	copy(out, r.evs)
	return out
}

func (r *recSink) countKind(k midi.Kind) int {
	n := 0
	for _, ev := range r.events() {
		if ev.Kind == k {
			n++
		}
	}
	return n
}

func newTestPlayer() (*Player, *recSink) {
	s := &recSink{}
	return New(nil, s.push), s
}

// waitFor polls cond until true or the deadline passes.
func waitFor(t *testing.T, timeout time.Duration, cond func() bool, what string) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %s", what)
}

func recvState(t *testing.T, ch <-chan State, what string) State {
	t.Helper()
	select {
	case s := <-ch:
		return s
	case <-time.After(5 * time.Second):
		t.Fatalf("timed out waiting for OnChange: %s", what)
		return State{}
	}
}

func TestPlayUnknownClip(t *testing.T) {
	p, _ := newTestPlayer()
	if err := p.Play("no-such-clip", false, 1.0); err == nil {
		t.Fatal("Play(unknown) returned nil error")
	}
	if st := p.State(); st.Playing {
		t.Fatalf("player playing after failed Play: %+v", st)
	}
}

// TestTempoClamp covers the clamp contract on both SetTempo and Play:
// [0.25, 2.0], with 0 meaning 1.0.
func TestTempoClamp(t *testing.T) {
	cases := []struct {
		name string
		in   float64
		want float64
	}{
		{"zero-means-default", 0, 1.0},
		{"below-floor", 0.1, 0.25},
		{"floor", 0.25, 0.25},
		{"in-range", 1.5, 1.5},
		{"ceiling", 2.0, 2.0},
		{"above-ceiling", 5.0, 2.0},
		{"negative", -3, 0.25},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p, _ := newTestPlayer()
			p.SetTempo(tc.in)
			if got := p.State().Tempo; got != tc.want {
				t.Errorf("SetTempo(%v): State().Tempo = %v, want %v", tc.in, got, tc.want)
			}
		})
	}

	// Play's tempo argument goes through the same clamp.
	p, _ := newTestPlayer()
	if err := p.Play("arp", false, 0); err != nil {
		t.Fatal(err)
	}
	if got := p.State().Tempo; got != 1.0 {
		t.Errorf("Play(tempo=0): State().Tempo = %v, want 1.0", got)
	}
	p.Stop()
}

// TestNaturalEnd plays arp once at 2.0x and checks the full contract:
// events arrive in exactly the pattern's order, the transport reports
// playing while running, and the natural end transitions to stopped via
// OnChange with no stuck notes.
func TestNaturalEnd(t *testing.T) {
	t.Parallel()
	p, s := newTestPlayer()
	states := make(chan State, 32)
	p.OnChange(func(st State) { states <- st })

	if err := p.Play("arp", false, 2.0); err != nil {
		t.Fatal(err)
	}
	st := recvState(t, states, "play state")
	want := State{Playing: true, ClipID: "arp", Loop: false, Tempo: 2.0}
	if st != want {
		t.Fatalf("play OnChange = %+v, want %+v", st, want)
	}
	if got := p.State(); got != want {
		t.Fatalf("State() during playback = %+v, want %+v", got, want)
	}

	st = recvState(t, states, "natural end state")
	if st.Playing {
		t.Fatalf("natural-end OnChange still playing: %+v", st)
	}
	if got := p.State(); got.Playing {
		t.Fatalf("State() after natural end still playing: %+v", got)
	}

	// The recorded stream must match the pattern verbatim: same events,
	// same order (the scheduler adds timing, never reordering).
	wantEvs, _ := arp()
	got := s.events()
	if len(got) != len(wantEvs) {
		t.Fatalf("received %d events, want %d", len(got), len(wantEvs))
	}
	for i, ev := range got {
		if ev != wantEvs[i].Ev {
			t.Fatalf("event %d = %+v, want %+v", i, ev, wantEvs[i].Ev)
		}
	}
}

// TestLoopPlaysTwice loops arp at 2.0x until two full iterations of
// NoteOns have arrived, checks the note sequence stays on-cycle across
// the loop seam, then stops and verifies the stream is balanced (every
// NoteOn matched by a NoteOff — nothing stuck after Stop).
func TestLoopPlaysTwice(t *testing.T) {
	t.Parallel()
	p, s := newTestPlayer()
	if err := p.Play("arp", true, 2.0); err != nil {
		t.Fatal(err)
	}
	// 16 NoteOns per iteration; 33 proves two complete iterations played
	// and a third began. At 2.0x (0.075 s per 16th) this is ~2.4 s.
	waitFor(t, 5*time.Second, func() bool { return s.countKind(midi.NoteOn) >= 33 }, "two loop iterations")
	p.Stop()

	if st := p.State(); st.Playing {
		t.Fatalf("State() after Stop still playing: %+v", st)
	}

	cycle := []byte{57, 60, 64, 67, 69, 67, 64, 60}
	ons := 0
	balance := map[byte]int{}
	for _, ev := range s.events() {
		switch ev.Kind {
		case midi.NoteOn:
			if want := cycle[ons%len(cycle)]; ev.Note != want {
				t.Fatalf("NoteOn %d = note %d, want %d (loop seam broke the cycle)", ons, ev.Note, want)
			}
			ons++
			balance[ev.Note]++
		case midi.NoteOff:
			balance[ev.Note]--
		}
	}
	for note, n := range balance {
		if n != 0 {
			t.Errorf("note %d has %+d unmatched NoteOns after Stop (stuck note)", note, n)
		}
	}

	p.Stop() // idempotent: second Stop is a no-op
}

// TestStopEmitsNoteOffsForHeld stops mid-chord — sustain-chord holds six
// notes for 8 beats, so at stop time all six are guaranteed held — and
// verifies Stop emitted a NoteOff for each before returning.
func TestStopEmitsNoteOffsForHeld(t *testing.T) {
	t.Parallel()
	p, s := newTestPlayer()
	if err := p.Play("sustain-chord", false, 2.0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return s.countKind(midi.NoteOn) == 6 }, "chord NoteOns")
	p.Stop()

	// Stop is synchronous: the offs must already be recorded, no waiting.
	offs := map[byte]int{}
	for _, ev := range s.events() {
		if ev.Kind == midi.NoteOff {
			offs[ev.Note]++
		}
	}
	for _, n := range []byte{48, 60, 64, 67, 71, 74} {
		if offs[n] != 1 {
			t.Errorf("note %d: %d NoteOffs after Stop, want exactly 1", n, offs[n])
		}
	}
	if st := p.State(); st.Playing {
		t.Fatalf("State() after Stop still playing: %+v", st)
	}
}

// TestOnChangeSequence verifies the callback fires for Play, SetTempo,
// and Stop, in order, with the states the contract promises.
func TestOnChangeSequence(t *testing.T) {
	t.Parallel()
	p, _ := newTestPlayer()
	states := make(chan State, 32)
	p.OnChange(func(st State) { states <- st })

	if err := p.Play("sustain-chord", true, 2.0); err != nil {
		t.Fatal(err)
	}
	if st := recvState(t, states, "play"); st != (State{Playing: true, ClipID: "sustain-chord", Loop: true, Tempo: 2.0}) {
		t.Fatalf("play state = %+v", st)
	}

	p.SetTempo(1.0)
	if st := recvState(t, states, "tempo"); !st.Playing || st.Tempo != 1.0 {
		t.Fatalf("tempo state = %+v, want playing at 1.0", st)
	}

	p.Stop()
	if st := recvState(t, states, "stop"); st.Playing {
		t.Fatalf("stop state = %+v, want stopped", st)
	}
	// Stopped state keeps the last clip for UI display.
	if st := p.State(); st.ClipID != "sustain-chord" {
		t.Fatalf("stopped State().ClipID = %q, want sustain-chord", st.ClipID)
	}
}

// TestOnChangeReplaces: OnChange holds at most one callback — a second
// registration replaces the first.
func TestOnChangeReplaces(t *testing.T) {
	p, _ := newTestPlayer()
	var firstCalls int
	p.OnChange(func(State) { firstCalls++ })
	second := make(chan State, 32)
	p.OnChange(func(st State) { second <- st })

	if err := p.Play("arp", true, 2.0); err != nil {
		t.Fatal(err)
	}
	recvState(t, second, "play via replacement callback")
	p.Stop()
	recvState(t, second, "stop via replacement callback")
	if firstCalls != 0 {
		t.Errorf("replaced callback was invoked %d times, want 0", firstCalls)
	}
}

// TestStopIdempotentWhenNeverPlayed: Stop on a fresh player is a no-op.
func TestStopIdempotentWhenNeverPlayed(t *testing.T) {
	p, s := newTestPlayer()
	p.Stop()
	p.Stop()
	if n := len(s.events()); n != 0 {
		t.Fatalf("Stop on idle player emitted %d events, want 0", n)
	}
}

// TestRestartWhilePlaying: Play while playing switches clips cleanly —
// the old clip's held notes are released before the new clip starts.
func TestRestartWhilePlaying(t *testing.T) {
	t.Parallel()
	p, s := newTestPlayer()
	if err := p.Play("sustain-chord", false, 2.0); err != nil {
		t.Fatal(err)
	}
	waitFor(t, 5*time.Second, func() bool { return s.countKind(midi.NoteOn) == 6 }, "chord NoteOns")
	if err := p.Play("arp", false, 2.0); err != nil {
		t.Fatal(err)
	}
	st := p.State()
	if !st.Playing || st.ClipID != "arp" {
		t.Fatalf("State() after restart = %+v, want playing arp", st)
	}

	// All six chord notes must have been released before (or as) the
	// switch happened — scan the stream up to the first arp NoteOn.
	evs := s.events()
	released := map[byte]bool{}
	for _, ev := range evs {
		if ev.Kind == midi.NoteOn && ev.Note == 57 { // arp's first note
			break
		}
		if ev.Kind == midi.NoteOff {
			released[ev.Note] = true
		}
	}
	for _, n := range []byte{48, 60, 64, 67, 71, 74} {
		if !released[n] {
			t.Errorf("chord note %d not released before new clip started", n)
		}
	}
	p.Stop()
}
