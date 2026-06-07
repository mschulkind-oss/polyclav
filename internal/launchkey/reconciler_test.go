package launchkey

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/launchkey/driver"
	"github.com/mschulkind-oss/polyclav/internal/midi"
)

type fakeRig struct {
	presentMu sync.Mutex
	present   bool

	openFailErr error

	openCount  atomic.Int32
	closeCount atomic.Int32
	reconnect  atomic.Int32
	disconnect atomic.Int32

	lostMu sync.Mutex
	lostCh chan struct{}
}

func newFakeRig() *fakeRig { return &fakeRig{} }

func (f *fakeRig) setPresent(p bool) {
	f.presentMu.Lock()
	f.present = p
	f.presentMu.Unlock()
}

func (f *fakeRig) lister() ([]string, error) {
	f.presentMu.Lock()
	defer f.presentMu.Unlock()
	if f.present {
		return []string{"Launchkey MK4 MIDI", "Launchkey MK4 DAW"}, nil
	}
	return []string{"Other Device"}, nil
}

func (f *fakeRig) opener(_ context.Context, _ *slog.Logger, _ string,
	_ midi.Sink, _ func(driver.Event)) (Connection, error) {
	if f.openFailErr != nil {
		return Connection{}, f.openFailErr
	}
	f.openCount.Add(1)
	f.lostMu.Lock()
	lostCh := make(chan struct{})
	f.lostCh = lostCh
	f.lostMu.Unlock()
	closeFn := func() { f.closeCount.Add(1) }
	return Connection{Driver: nil, Close: closeFn, Lost: lostCh}, nil
}

func (f *fakeRig) signalLost() {
	f.lostMu.Lock()
	ch := f.lostCh
	f.lostMu.Unlock()
	if ch == nil {
		return
	}
	defer func() { _ = recover() }()
	close(ch)
}

func newTestReconciler(rig *fakeRig) *Reconciler {
	return NewReconciler(
		slog.New(slog.NewTextHandler(io.Discard, nil)),
		ReconcilerConfig{
			PortMatch:    "launchkey",
			PollInterval: 5 * time.Millisecond,
			OnReconnect:  func() { rig.reconnect.Add(1) },
			OnDisconnect: func() { rig.disconnect.Add(1) },
			PortLister:   rig.lister,
			Opener:       rig.opener,
		},
	)
}

func waitState(t *testing.T, r *Reconciler, want string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if r.State() == want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitState: never reached %q (current %q)", want, r.State())
}

func waitCount(t *testing.T, get func() int32, want int32, label string) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		if get() >= want {
			return
		}
		time.Sleep(2 * time.Millisecond)
	}
	t.Fatalf("waitCount %s: got %d, want >= %d", label, get(), want)
}

func runReconciler(t *testing.T, r *Reconciler) (context.CancelFunc, <-chan struct{}) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		_ = r.Run(ctx)
		close(done)
	}()
	return cancel, done
}

func stopReconciler(t *testing.T, cancel context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not exit")
	}
}

func TestReconcilerStartsAbsent(t *testing.T) {
	rig := newFakeRig()
	r := newTestReconciler(rig)
	if got := r.State(); got != "absent" {
		t.Errorf("initial State: got %q, want absent", got)
	}
}

func TestReconcilerCycleAbsentActiveAbsent(t *testing.T) {
	rig := newFakeRig()
	r := newTestReconciler(rig)
	cancel, done := runReconciler(t, r)
	defer stopReconciler(t, cancel, done)

	waitState(t, r, "absent")

	rig.setPresent(true)
	waitState(t, r, "active")
	waitCount(t, func() int32 { return rig.reconnect.Load() }, 1, "reconnect")

	rig.setPresent(false)
	waitState(t, r, "absent")
	waitCount(t, func() int32 { return rig.disconnect.Load() }, 1, "disconnect")
}

func TestReconcilerReconnectsAfterPortLoss(t *testing.T) {
	rig := newFakeRig()
	rig.setPresent(true)
	r := newTestReconciler(rig)
	cancel, done := runReconciler(t, r)
	defer stopReconciler(t, cancel, done)

	waitState(t, r, "active")
	waitCount(t, func() int32 { return rig.reconnect.Load() }, 1, "reconnect 1")

	// Simulate port-loss while the physical port is still "present" (e.g.
	// transient ALSA error). Reconciler should disconnect then re-open
	// on the next tick.
	rig.signalLost()

	waitCount(t, func() int32 { return rig.disconnect.Load() }, 1, "disconnect 1")
	waitState(t, r, "active")
	waitCount(t, func() int32 { return rig.reconnect.Load() }, 2, "reconnect 2")
}

func TestReconcilerStaysOpeningOnOpenFailure(t *testing.T) {
	rig := newFakeRig()
	rig.openFailErr = errors.New("simulated open failure")
	rig.setPresent(true)
	r := newTestReconciler(rig)
	cancel, done := runReconciler(t, r)
	defer stopReconciler(t, cancel, done)

	// Give the reconciler several ticks to attempt-and-fail.
	time.Sleep(50 * time.Millisecond)

	if got := r.State(); got != "opening" {
		t.Errorf("State during repeated open failures: got %q, want opening", got)
	}
	if rig.reconnect.Load() != 0 {
		t.Errorf("reconnect count during failures: got %d, want 0", rig.reconnect.Load())
	}
}

func TestReconcilerAbsentToActiveBackAndForth(t *testing.T) {
	rig := newFakeRig()
	r := newTestReconciler(rig)
	cancel, done := runReconciler(t, r)
	defer stopReconciler(t, cancel, done)

	waitState(t, r, "absent")

	rig.setPresent(true)
	waitState(t, r, "active")
	waitCount(t, func() int32 { return rig.reconnect.Load() }, 1, "reconnect 1")

	rig.setPresent(false)
	waitState(t, r, "absent")
	waitCount(t, func() int32 { return rig.disconnect.Load() }, 1, "disconnect 1")

	rig.setPresent(true)
	waitState(t, r, "active")
	waitCount(t, func() int32 { return rig.reconnect.Load() }, 2, "reconnect 2")
}
