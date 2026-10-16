package main

import (
	"sync"
	"testing"
	"time"
)

type screenRecorder struct {
	mu    sync.Mutex
	lines [][2]string
}

func (r *screenRecorder) write(line1, line2 string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.lines = append(r.lines, [2]string{line1, line2})
	return nil
}

func (r *screenRecorder) last() [2]string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.lines[len(r.lines)-1]
}

func TestScreenArbiterPopupRestoresToPatch(t *testing.T) {
	rec := &screenRecorder{}
	a := newScreenArbiter(rec.write, 20*time.Millisecond, "Piano", "")
	if err := a.ShowPatch("Piano", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.ShowPopup("8' drawbar", "7"); err != nil {
		t.Fatal(err)
	}
	if a.Idle() {
		t.Fatal("screen must be busy while a popup is up")
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := rec.last(); got == [2]string{"Piano", ""} && a.Idle() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("popup never restored to patch; last=%v idle=%v", rec.last(), a.Idle())
}

// TestScreenArbiterPatchTakesPrecedenceOverPopup is the reported bug: a
// drawbar popup in flight while the patch changes must not leave the
// screen stuck on the drawbar value — the patch display wins, and the
// popup's stale restore timer must not repaint over it.
func TestScreenArbiterPatchTakesPrecedenceOverPopup(t *testing.T) {
	rec := &screenRecorder{}
	a := newScreenArbiter(rec.write, 20*time.Millisecond, "Organ", "")
	if err := a.ShowPatch("Organ", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.ShowPopup("8' drawbar", "7"); err != nil {
		t.Fatal(err)
	}
	// Patch switch lands while the drawbar popup is up.
	if err := a.ShowPatch("New Patch", "Loading"); err != nil {
		t.Fatal(err)
	}
	if !a.Idle() {
		t.Fatal("patch display must clear popup-busy")
	}
	// Well past the popup delay, the stale restore must not have fired.
	time.Sleep(80 * time.Millisecond)
	if got := rec.last(); got != [2]string{"New Patch", "Loading"} {
		t.Fatalf("stale popup restore repainted over the patch display: last=%v", got)
	}
}

func TestScreenArbiterPopupAfterPatchStillRestores(t *testing.T) {
	rec := &screenRecorder{}
	a := newScreenArbiter(rec.write, 20*time.Millisecond, "Organ", "")
	if err := a.ShowPatch("Organ", ""); err != nil {
		t.Fatal(err)
	}
	if err := a.ShowPatch("Organ", ""); err != nil { // load finished
		t.Fatal(err)
	}
	if err := a.ShowPopup("8' drawbar", "7"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if got := rec.last(); got == [2]string{"Organ", ""} && a.Idle() {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("popup after patch never restored; last=%v", rec.last())
}
