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

// registerTestClip injects a synthetic clip into the library. Test-only;
// must be called before playback starts (no locking).
func registerTestClip(p *Player, info ClipInfo, evs []TimedEvent) {
	p.byID[info.ID] = clipData{info: info, events: evs}
	p.clips = append(p.clips, info)
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

// TestNaturalEndVsPlayRace hammers the natural-end/Play() race: a clip a
// few milliseconds long ends naturally while (or just as) Play switches
// to a clip that holds notes indefinitely. The contract under test:
//
//   - the stale run's teardown must never emit NoteOffs for the new
//     run's sounding notes (held notes are per-run);
//   - no old-run event may reach the sink after the new run's first
//     event (full serialization: the old run finishes before the new
//     one is installed);
//   - OnChange ordering ends at playing=true for the new clip — a stale
//     playing=false must never land after the new playing=true;
//   - after Stop() returns, the sink receives nothing further.
//
// Run with -race: the varying sleep phase sweeps Play() across the old
// run's natural-end window.
func TestNaturalEndVsPlayRace(t *testing.T) {
	t.Parallel()

	// The short clip ends naturally ~4 ms in while STILL holding note 10
	// (no NoteOff in the clip), so its teardown owes a real NoteOff — the
	// stale-scheduler bug then has observable work to misplace.
	shortInfo := ClipInfo{ID: "race-short", Name: "race short", Beats: 0.04, RefBPM: 600}
	shortEvs := []TimedEvent{
		{Beat: 0, Ev: midi.Event{Kind: midi.NoteOn, Note: 10, Vel: 100}},
	}
	// Notes with no NoteOff in the clip and a far-off end: they stay held
	// for the whole test, so any stale releaseHeld that could cut them
	// has every opportunity to do so.
	heldInfo := ClipInfo{ID: "race-held", Name: "race held", Beats: 1000, RefBPM: 600}
	heldEvs := []TimedEvent{
		{Beat: 0, Ev: midi.Event{Kind: midi.NoteOn, Note: 20, Vel: 100}},
		{Beat: 0, Ev: midi.Event{Kind: midi.NoteOn, Note: 21, Vel: 100}},
		{Beat: 0, Ev: midi.Event{Kind: midi.NoteOn, Note: 22, Vel: 100}},
	}

	for i := 0; i < 96; i++ {
		s := &recSink{}
		// The sink takes real time to deliver the old run's owed NoteOff
		// (as any sink doing work might). Teardown must therefore be
		// sequenced by the done channel, not by luck: a Play racing the
		// natural end has a 500 µs window to sneak in if the player ever
		// lets go of the run before its final events have been delivered.
		sink := func(ev midi.Event) {
			if ev.Kind == midi.NoteOff && ev.Note == 10 {
				time.Sleep(500 * time.Microsecond)
			}
			s.push(ev)
		}
		p := New(nil, sink)
		registerTestClip(p, shortInfo, shortEvs)
		registerTestClip(p, heldInfo, heldEvs)
		var stMu sync.Mutex
		var states []State
		p.OnChange(func(st State) {
			stMu.Lock()
			states = append(states, st)
			stMu.Unlock()
		})

		if err := p.Play("race-short", false, 1.0); err != nil {
			t.Fatal(err)
		}
		// race-short ends naturally ~4 ms in; sweep Play() across that
		// moment in 100 µs steps (sleep jitter spreads each attempt) so
		// many iterations land inside the teardown window.
		time.Sleep(3*time.Millisecond + time.Duration(i%12)*100*time.Microsecond)
		if err := p.Play("race-held", false, 1.0); err != nil {
			t.Fatal(err)
		}

		countHeldOns := func() int {
			n := 0
			for _, ev := range s.events() {
				if ev.Kind == midi.NoteOn && ev.Note >= 20 {
					n++
				}
			}
			return n
		}
		waitFor(t, 5*time.Second, func() bool { return countHeldOns() == 3 }, "held clip NoteOns")
		// Give a stale scheduler (the bug) time to misbehave before we assert.
		time.Sleep(5 * time.Millisecond)

		seenNew := false
		for _, ev := range s.events() {
			if ev.Note >= 20 {
				if ev.Kind == midi.NoteOff {
					t.Fatalf("iter %d: stale run cut the new run's note: %+v", i, ev)
				}
				seenNew = true
			} else if seenNew {
				t.Fatalf("iter %d: old-run event %+v emitted after the new run started", i, ev)
			}
		}

		stMu.Lock()
		last := states[len(states)-1]
		stMu.Unlock()
		if !last.Playing || last.ClipID != "race-held" {
			t.Fatalf("iter %d: last OnChange = %+v, want playing race-held (stale stop published after new play)", i, last)
		}

		p.Stop()
		n := len(s.events())
		time.Sleep(5 * time.Millisecond)
		if got := len(s.events()); got != n {
			t.Fatalf("iter %d: %d sink events arrived after Stop returned", i, got-n)
		}
		stMu.Lock()
		last = states[len(states)-1]
		stMu.Unlock()
		if last.Playing {
			t.Fatalf("iter %d: last OnChange after Stop = %+v, want stopped", i, last)
		}
	}
}

// TestOnChangeOrdering hammers SetTempo from several goroutines and
// checks that callback delivery order matches state mutation order: the
// last callback delivered must carry exactly the final State. (Run with
// -race; before callback invocations were serialized, a snapshot taken
// under the state lock could be delivered late and out of order.)
func TestOnChangeOrdering(t *testing.T) {
	t.Parallel()
	p, _ := newTestPlayer()
	var mu sync.Mutex
	var got []State
	p.OnChange(func(st State) {
		mu.Lock()
		got = append(got, st)
		mu.Unlock()
	})

	const goroutines, calls = 4, 100
	tempos := []float64{0.25, 0.5, 1.0, 1.5, 2.0}
	var wg sync.WaitGroup
	for g := 0; g < goroutines; g++ {
		wg.Add(1)
		go func(g int) {
			defer wg.Done()
			for j := 0; j < calls; j++ {
				p.SetTempo(tempos[(g+j)%len(tempos)])
			}
		}(g)
	}
	wg.Wait()

	mu.Lock()
	defer mu.Unlock()
	if len(got) != goroutines*calls {
		t.Fatalf("received %d OnChange callbacks, want %d (one per SetTempo)", len(got), goroutines*calls)
	}
	last := got[len(got)-1]
	if fin := p.State(); last != fin {
		t.Fatalf("last OnChange %+v != final State %+v (callbacks delivered out of mutation order)", last, fin)
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
