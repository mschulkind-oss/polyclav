package main

import (
	"sync"
	"time"
)

// screenArbiter owns the Launchkey's two-line display. Before it existed,
// every surface wrote the screen directly — knob/drawbar value popups, the
// patch-name follower, the limiter indicator, pad-bank flashes — and a
// shared 800 ms timer restored "the patch name" afterwards. Nothing gave
// a patch change precedence over a popup already on screen: a drawbar
// popup landing after the patch follower's final write for a switch had
// no later event to re-assert the patch name, so the screen could sit on
// a bare drawbar value ("7") until the user switched patches again and
// generated a fresh follower write.
//
// The arbiter makes the precedence explicit:
//
//   - ShowPatch is the base layer (patch name + load state). It cancels
//     any popup, invalidates its pending restore, and becomes the text a
//     later popup restores to. The patch display always wins.
//   - ShowPopup is a transient value display (drawbar, knob, bank). It
//     restores to the current base after the delay unless a ShowPatch
//     (or a newer popup) superseded it; the expiry checks the epoch so a
//     stale timer can never repaint over a newer patch display.
//
// All writes are serialized, and state transitions happen under the same
// lock, so the on-screen text and the arbiter's idea of it cannot drift
// apart the way the open-coded writers could.
type screenArbiter struct {
	write func(line1, line2 string) error
	delay time.Duration

	writeMu sync.Mutex // serializes device writes with state transitions
	mu      sync.Mutex
	busy    bool // a popup (not the patch base) is on screen
	epoch   uint64
	timer   *time.Timer
	base1   string
	base2   string
}

func newScreenArbiter(write func(line1, line2 string) error, delay time.Duration, base1, base2 string) *screenArbiter {
	return &screenArbiter{write: write, delay: delay, base1: base1, base2: base2}
}

// ShowPatch displays the patch base layer and takes precedence over any
// popup: the popup's pending restore is cancelled and will not repaint.
func (a *screenArbiter) ShowPatch(line1, line2 string) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	a.mu.Lock()
	a.epoch++
	a.busy = false
	if a.timer != nil {
		a.timer.Stop()
		a.timer = nil
	}
	a.base1, a.base2 = line1, line2
	a.mu.Unlock()
	return a.write(line1, line2)
}

// ShowPopup displays a transient value for the arbiter's delay, then
// restores the patch base — unless a patch display or a newer popup has
// superseded it in the meantime.
func (a *screenArbiter) ShowPopup(line1, line2 string) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	a.mu.Lock()
	a.busy = true
	epoch := a.epoch
	if a.timer != nil {
		a.timer.Stop()
	}
	a.timer = time.AfterFunc(a.delay, func() { a.expirePopup(epoch) })
	a.mu.Unlock()
	return a.write(line1, line2)
}

func (a *screenArbiter) expirePopup(epoch uint64) {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	a.mu.Lock()
	if epoch != a.epoch || !a.busy {
		a.mu.Unlock()
		return
	}
	a.busy = false
	a.timer = nil
	line1, line2 := a.base1, a.base2
	a.mu.Unlock()
	_ = a.write(line1, line2)
}

// WriteDirect writes without changing the base or popup state — the
// limiter indicator's line, which only writes while Idle and re-asserts
// itself on its own cadence.
func (a *screenArbiter) WriteDirect(line1, line2 string) error {
	a.writeMu.Lock()
	defer a.writeMu.Unlock()
	return a.write(line1, line2)
}

// Idle reports whether the patch base (not a popup) owns the screen.
func (a *screenArbiter) Idle() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return !a.busy
}
