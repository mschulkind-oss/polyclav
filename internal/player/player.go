// Package player is a keyboard-free clip player (docs/AUDITION.md, P1).
// It schedules the events of a small built-in clip library on the wall
// clock and pushes them into a Sink — main wires that to the synth fork,
// the exact same funnel keyboard events use — so every polyclav setting
// can be auditioned live with no MIDI keyboard attached.
//
// Scheduling is deliberately simple (a goroutine sleeping to the next
// event): millisecond jitter is identical in kind to a human playing over
// USB, and this is an audition tool, not a sequencer.
package player

import (
	"fmt"
	"log/slog"
	"math"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/midi"
)

// Sink receives the player's MIDI events. main wires it to the synth
// fork only (never the OSC mapper — clip notes must not fire mixer
// bindings). Implementations should be non-blocking; blocking here
// delays subsequent clip events.
type Sink func(ev midi.Event)

// ClipInfo describes one clip in the library: identity for APIs/UIs,
// musical length in beats, and the reference tempo the Beat timeline is
// authored at. PolyOnly marks chordal clips that collapse to a single
// note on the mono-legato native engine, so pickers can label them
// "(poly patches)" instead of pretending otherwise.
type ClipInfo struct {
	ID          string
	Name        string
	Description string
	PolyOnly    bool
	Beats       float64
	RefBPM      float64
}

// TimedEvent is one clip event positioned in musical time (beats from
// clip start). Musical time — not wall time — is stored so the tempo
// multiplier can change live without rewriting the clip.
type TimedEvent struct {
	Beat float64
	Ev   midi.Event
}

// State is a snapshot of the transport. After a stop (explicit or
// natural end) Playing goes false but ClipID/Loop/Tempo retain their
// last values so UIs can still show what was playing.
type State struct {
	Playing bool
	ClipID  string
	Loop    bool
	Tempo   float64
}

// heldKey identifies a sounding (channel, note) pair. Held notes are
// tracked so stop/clip-switch/shutdown can emit NoteOffs for exactly
// what is ringing — no stuck notes, ever.
type heldKey struct {
	ch, note byte
}

// clipData pairs a clip's metadata with its pre-built event list.
type clipData struct {
	info   ClipInfo
	events []TimedEvent
}

// run is the per-playback handle shared between the transport methods
// and the scheduler goroutine. Tempo lives here as an atomic so the
// scheduler can read it without taking the Player mutex mid-sleep, and
// tempoKick wakes a sleeping scheduler so live tempo changes take
// effect immediately instead of after the current note gap.
//
// held is PER-RUN (not on Player) so a finishing run can only ever
// release the notes it started itself — a stale scheduler racing a new
// Play can never cut the new run's sounding notes. It is touched only
// by this run's scheduler goroutine (emit, loop-seam releaseHeld, and
// finish all execute there), so it needs no lock.
type run struct {
	cancel    chan struct{}
	done      chan struct{}
	stopOnce  sync.Once
	silent    atomic.Bool // suppress the stop OnChange (clip-switch restarts)
	tempoBits atomic.Uint64
	tempoKick chan struct{}        // buffered(1); coalesces rapid tempo changes
	held      map[heldKey]struct{} // scheduler goroutine only
}

func newRun(tempo float64) *run {
	r := &run{
		cancel:    make(chan struct{}),
		done:      make(chan struct{}),
		tempoKick: make(chan struct{}, 1),
		held:      map[heldKey]struct{}{},
	}
	r.tempoBits.Store(math.Float64bits(tempo))
	return r
}

func (r *run) tempo() float64 { return math.Float64frombits(r.tempoBits.Load()) }

func (r *run) setTempo(t float64) {
	r.tempoBits.Store(math.Float64bits(t))
	select {
	case r.tempoKick <- struct{}{}:
	default: // a kick is already pending; the scheduler re-reads tempo anyway
	}
}

func (r *run) stop() {
	r.stopOnce.Do(func() { close(r.cancel) })
}

// Player owns the transport state and the scheduler goroutine. All
// methods are safe from any goroutine; Stop (and a clip-switching Play)
// waits for the scheduler to exit, so on return no further events flow.
type Player struct {
	logger *slog.Logger
	sink   Sink

	clips []ClipInfo
	byID  map[string]clipData

	// transport serializes whole Play/Stop transitions so two concurrent
	// callers cannot both adopt the "current run" slot. Never held while
	// the scheduler needs it — the scheduler only touches mu and cbMu.
	transport sync.Mutex

	// cbMu serializes every state mutation that fires OnChange together
	// with its callback invocation, so observers receive callbacks in
	// exactly mutation order (a snapshot taken under mu but delivered
	// after unlocking can otherwise arrive out of order). Lock order is
	// cbMu → mu, never the reverse; mu is never held during a callback
	// (State() re-entry from the callback stays legal), and neither
	// lock is ever held while calling the sink.
	cbMu sync.Mutex

	mu       sync.Mutex
	st       State
	onChange func(State)
	run      *run
}

// New builds a Player with the seven built-in patterns registered. A nil
// logger or sink is tolerated (discard/no-op) so tests and partial wiring
// can't panic the scheduler.
func New(logger *slog.Logger, sink Sink) *Player {
	if logger == nil {
		logger = slog.New(slog.DiscardHandler)
	}
	if sink == nil {
		sink = func(midi.Event) {}
	}
	p := &Player{
		logger: logger,
		sink:   sink,
		byID:   map[string]clipData{},
		st:     State{Tempo: 1.0},
	}
	for _, build := range builders {
		evs, info := build()
		p.clips = append(p.clips, info)
		p.byID[info.ID] = clipData{info: info, events: evs}
	}
	return p
}

// Clips lists the clip library in stable registration order (UIs index
// this list, so the order is part of the contract).
func (p *Player) Clips() []ClipInfo {
	return slices.Clone(p.clips)
}

// Play starts clipID from the top. If something is already playing it is
// stopped first — held notes released, scheduler joined — so restarts are
// always clean. Unknown IDs return an error. tempo is clamped to
// [0.25, 2.0]; 0 (and NaN) mean 1.0.
func (p *Player) Play(clipID string, loop bool, tempo float64) error {
	p.transport.Lock()
	defer p.transport.Unlock()

	cd, ok := p.byID[clipID]
	if !ok {
		return fmt.Errorf("player: unknown clip %q (have: %s)", clipID, strings.Join(p.clipIDs(), ", "))
	}
	tempo = clampTempo(tempo)

	// Silent stop: the intermediate "stopped" state during a clip switch
	// is an implementation detail; observers only see the new Play state.
	// stopCurrent returns only once the previous run has fully finished
	// (its NoteOffs emitted and its stop callback delivered), so the new
	// run is never installed while a stale scheduler can still act.
	p.stopCurrent(true)

	r := newRun(tempo)
	p.cbMu.Lock()
	p.mu.Lock()
	p.run = r
	p.st = State{Playing: true, ClipID: clipID, Loop: loop, Tempo: tempo}
	st := p.st
	cb := p.onChange
	p.mu.Unlock()

	p.logger.Info("player play", "clip", clipID, "loop", loop, "tempo", tempo)
	go p.schedule(r, cd.events, cd.info, loop)
	if cb != nil {
		cb(st)
	}
	p.cbMu.Unlock()
	return nil
}

// Stop halts playback. Idempotent. On return the scheduler goroutine has
// exited and a NoteOff has been emitted for every held note — callers can
// rely on silence (bar release tails) after Stop returns.
func (p *Player) Stop() {
	p.transport.Lock()
	defer p.transport.Unlock()
	p.stopCurrent(false)
}

// stopCurrent cancels the active run (if any) and waits for its
// scheduler to exit. The scheduler's finish() emits the NoteOffs, fires
// the stop callback, and clears p.run — in that order — before done
// closes, so BOTH exits here mean cleanup is complete: waiting on done
// obviously, and seeing p.run == nil because a run only leaves p.run
// after its last sink push and callback (all that remains then is
// closing done). Caller must hold p.transport.
func (p *Player) stopCurrent(silent bool) {
	p.mu.Lock()
	r := p.run
	p.mu.Unlock()
	if r == nil {
		return
	}
	if silent {
		r.silent.Store(true)
	}
	r.stop()
	<-r.done
}

// SetTempo changes the tempo multiplier live (same clamp as Play). Works
// while stopped too — the value shows in State and applies visually in
// UIs, though Play's explicit tempo argument wins on the next start.
func (p *Player) SetTempo(t float64) {
	t = clampTempo(t)
	p.cbMu.Lock()
	defer p.cbMu.Unlock()
	p.mu.Lock()
	p.st.Tempo = t
	if p.run != nil {
		p.run.setTempo(t)
	}
	st := p.st
	cb := p.onChange
	p.mu.Unlock()
	if cb != nil {
		cb(st)
	}
}

// State returns a snapshot of the transport.
func (p *Player) State() State {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.st
}

// OnChange registers the single state-change callback (replacing any
// previous one — at most one observer, by contract). It is invoked on
// Play, Stop, SetTempo, and natural clip end, from whichever goroutine
// caused the change; keep it fast and non-blocking.
func (p *Player) OnChange(fn func(State)) {
	p.mu.Lock()
	p.onChange = fn
	p.mu.Unlock()
}

func (p *Player) clipIDs() []string {
	ids := make([]string, len(p.clips))
	for i, c := range p.clips {
		ids[i] = c.ID
	}
	return ids
}

// clampTempo maps the caller's tempo multiplier into the supported
// range. 0 means "default" (1.0) so callers can pass the zero value; NaN
// is treated the same so a garbage input can never wedge the scheduler's
// sleep math.
func clampTempo(t float64) float64 {
	if t == 0 || math.IsNaN(t) {
		return 1.0
	}
	return min(max(t, 0.25), 2.0)
}

// schedule is the playback goroutine: walk the (beat-sorted) events,
// sleeping to each one, looping seamlessly if asked. finish() runs
// before done closes so anyone waiting on done observes a fully
// cleaned-up player (NoteOffs emitted, stop callback delivered, state
// stopped). Defer order matters: finish first, close(done) last.
func (p *Player) schedule(r *run, events []TimedEvent, info ClipInfo, loop bool) {
	defer close(r.done)
	defer p.finish(r)
	for {
		beat := 0.0
		for _, te := range events {
			if !p.waitBeats(r, info.RefBPM, &beat, te.Beat) {
				return
			}
			p.emit(r, te.Ev)
		}
		// Hold to the clip's declared length so loop wraps land on the
		// musical grid and natural ends include trailing silence
		// (sustain-chord's 8 silent beats are part of the demo).
		if !p.waitBeats(r, info.RefBPM, &beat, info.Beats) {
			return
		}
		if !loop {
			return // natural end: finish() flips state + fires OnChange
		}
		// Loop-seam safety net only — patterns are self-contained (every
		// NoteOn has its NoteOff within the clip), so this is normally a
		// no-op. It exists so a buggy future pattern can't stack notes
		// forever.
		p.releaseHeld(r)
	}
}

// waitBeats sleeps until the musical position reaches target beats,
// honoring cancellation and live tempo changes (a tempo kick converts
// the elapsed portion of the sleep back into beats at the old tempo,
// then re-sleeps the remainder at the new one). Returns false when
// cancelled.
func (p *Player) waitBeats(r *run, refBPM float64, pos *float64, target float64) bool {
	for {
		select {
		case <-r.cancel:
			return false
		default:
		}
		remaining := target - *pos
		if remaining <= 0 {
			*pos = target
			return true
		}
		secPerBeat := 60.0 / (refBPM * r.tempo())
		timer := time.NewTimer(time.Duration(remaining * secPerBeat * float64(time.Second)))
		start := time.Now()
		select {
		case <-r.cancel:
			timer.Stop()
			return false
		case <-timer.C:
			*pos = target
			return true
		case <-r.tempoKick:
			timer.Stop()
			*pos += time.Since(start).Seconds() / secPerBeat
			// loop: recompute the rest of the wait at the new tempo
		}
	}
}

// emit pushes one event to the sink, maintaining the run's held-note
// set so stop paths know exactly which NoteOffs they owe. Runs only on
// r's scheduler goroutine, so the map access needs no lock.
func (p *Player) emit(r *run, ev midi.Event) {
	switch ev.Kind {
	case midi.NoteOn:
		r.held[heldKey{ch: ev.Channel, note: ev.Note}] = struct{}{}
	case midi.NoteOff:
		delete(r.held, heldKey{ch: ev.Channel, note: ev.Note})
	}
	p.sink(ev)
}

// releaseHeld emits a NoteOff for every note r currently holds (sorted
// for deterministic output) and clears the set. Because the set is
// per-run, this can only ever release r's own notes — never a newer
// run's. Runs only on r's scheduler goroutine.
func (p *Player) releaseHeld(r *run) {
	keys := make([]heldKey, 0, len(r.held))
	for k := range r.held {
		keys = append(keys, k)
	}
	clear(r.held)
	slices.SortFunc(keys, func(a, b heldKey) int {
		if a.ch != b.ch {
			return int(a.ch) - int(b.ch)
		}
		return int(a.note) - int(b.note)
	})
	for _, k := range keys {
		p.sink(midi.Event{Kind: midi.NoteOff, Channel: k.ch, Note: k.note})
	}
}

// finish is the single teardown path for a run, always executed on the
// scheduler goroutine (deferred in schedule) for both cancellation and
// natural end. The ordering is the concurrency contract:
//
//  1. emit this run's NoteOffs (per-run held set — only its own notes);
//  2. flip Playing false and deliver the stop OnChange, both under
//     cbMu so the callback lands in mutation order;
//  3. only then clear p.run (guarded by an identity check);
//  4. the caller closes r.done last.
//
// So a waiter on done — or a Play/Stop that observes p.run == nil —
// knows the run can push nothing further to the sink or the callback,
// and a stale playing=false can never be published after a newer Play's
// playing=true.
func (p *Player) finish(r *run) {
	p.releaseHeld(r)

	p.cbMu.Lock()
	p.mu.Lock()
	current := p.run == r
	if current {
		p.st.Playing = false
	}
	st := p.st
	cb := p.onChange
	p.mu.Unlock()
	if current && cb != nil && !r.silent.Load() {
		cb(st)
	}
	p.cbMu.Unlock()

	p.logger.Info("player stop", "clip", st.ClipID)

	p.mu.Lock()
	if p.run == r {
		p.run = nil
	}
	p.mu.Unlock()
}
