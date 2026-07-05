package osc

import (
	"bytes"
	"context"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestReconcilerDefaults(t *testing.T) {
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 10024, Heartbeat: "/xinfo"})
	if r.cfg.PollInterval != 5*time.Second {
		t.Errorf("PollInterval default: got %v, want 5s", r.cfg.PollInterval)
	}
	if r.cfg.Timeout != 3*time.Second {
		t.Errorf("Timeout default: got %v, want 3s", r.cfg.Timeout)
	}
	if r.cfg.MissThreshold != 3 {
		t.Errorf("MissThreshold default: got %d, want 3", r.cfg.MissThreshold)
	}
	if got := r.State(); got != "absent" {
		t.Errorf("initial State: got %q, want %q", got, "absent")
	}
}

func TestReconcilerOneHitGoesReachable(t *testing.T) {
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 10024, Heartbeat: "/xinfo"})
	r.recordHit()
	if got := r.State(); got != "reachable" {
		t.Errorf("after recordHit: State=%q, want reachable", got)
	}
}

func TestReconcilerMissesBelowThresholdStayReachable(t *testing.T) {
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 10024, Heartbeat: "/xinfo", MissThreshold: 3})
	r.recordHit()
	r.recordMiss()
	r.recordMiss()
	if got := r.State(); got != "reachable" {
		t.Errorf("after 2 misses (threshold 3): State=%q, want reachable", got)
	}
}

func TestReconcilerMissesAtThresholdGoAbsent(t *testing.T) {
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 10024, Heartbeat: "/xinfo", MissThreshold: 3})
	r.recordHit()
	r.recordMiss()
	r.recordMiss()
	r.recordMiss()
	if got := r.State(); got != "absent" {
		t.Errorf("after 3 misses (threshold 3): State=%q, want absent", got)
	}
}

func TestReconcilerRecoversAfterAbsent(t *testing.T) {
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 10024, Heartbeat: "/xinfo", MissThreshold: 3})
	r.recordHit()
	r.recordMiss()
	r.recordMiss()
	r.recordMiss()
	if got := r.State(); got != "absent" {
		t.Fatalf("setup: State=%q, want absent", got)
	}
	r.recordHit()
	if got := r.State(); got != "reachable" {
		t.Errorf("after recovery hit: State=%q, want reachable", got)
	}
}

func TestReconcilerSendNoOpWhileAbsent(t *testing.T) {
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 10024, Heartbeat: "/xinfo"})
	if got := r.State(); got != "absent" {
		t.Fatalf("setup: State=%q, want absent", got)
	}
	if err := r.Send("/foo", float32(0.5)); err != nil {
		t.Errorf("Send while absent: got err=%v, want nil (no-op)", err)
	}
}

func TestReconcilerSendCallsClientWhenReachable(t *testing.T) {
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 65535, Heartbeat: "/xinfo"})
	r.recordHit()
	// Send to a likely-closed UDP port. UDP is connectionless so Write
	// generally succeeds even with no listener; we just verify no panic
	// and that the code path runs.
	_ = r.Send("/foo", float32(0.1))
}

// --- OnStateChange -----------------------------------------------------------

func TestReconcilerStateChangeCallbackFiresOnTransitionsOnly(t *testing.T) {
	var got []string
	r := NewReconciler(discardLogger(), ReconcilerConfig{
		Host: "127.0.0.1", Port: 10024, Heartbeat: "/xinfo", MissThreshold: 3,
		OnStateChange: func(s string) { got = append(got, s) },
	})
	r.recordHit()  // absent -> reachable
	r.recordHit()  // still reachable: no callback
	r.recordMiss() // 1/3
	r.recordMiss() // 2/3
	r.recordMiss() // 3/3 -> absent
	r.recordMiss() // still absent: no repeat callback
	r.recordHit()  // absent -> reachable again
	want := []string{"reachable", "absent", "reachable"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("OnStateChange sequence: got %v, want %v", got, want)
	}
}

func TestReconcilerStateChangeCallbackInvokedOutsideMutex(t *testing.T) {
	// The callback contract says it runs OUTSIDE the reconciler's
	// internal mutex: a callback that drives the reconciler again (here,
	// an immediate recovery probe on "absent") must not deadlock. The
	// depth guard stops the reentrant hit's own callback from recursing
	// further.
	var r *Reconciler
	var depth int
	r = NewReconciler(discardLogger(), ReconcilerConfig{
		Host: "127.0.0.1", Port: 10024, Heartbeat: "/xinfo", MissThreshold: 1,
		OnStateChange: func(s string) {
			if depth > 0 {
				return
			}
			depth++
			defer func() { depth-- }()
			if s == "absent" {
				r.recordHit() // reenters recordHit; deadlocks if the callback held missMu
			}
		},
	})
	done := make(chan struct{})
	go func() {
		defer close(done)
		r.recordHit()
		r.recordMiss() // -> absent; callback immediately records a recovery hit
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("OnStateChange appears to run under the reconciler mutex (reentrant call deadlocked)")
	}
	if got := r.State(); got != "reachable" {
		t.Errorf("after reentrant recovery: State=%q, want reachable", got)
	}
}

func TestReconcilerStateChangeCallbackConcurrentHitsAndMisses(t *testing.T) {
	// Race-detector exercise for the transition detection + callback
	// hand-off: hammer recordHit/recordMiss from many goroutines with a
	// live callback. Run with -race; the assertions are deliberately
	// weak (some transition fired, final state coherent) — the value is
	// the concurrent interleaving itself.
	var calls atomic.Int64
	r := NewReconciler(discardLogger(), ReconcilerConfig{
		Host: "127.0.0.1", Port: 10024, Heartbeat: "/xinfo", MissThreshold: 1,
		OnStateChange: func(s string) {
			if s != "reachable" && s != "absent" {
				t.Errorf("OnStateChange got unexpected state %q", s)
			}
			calls.Add(1)
		},
	})
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			for j := 0; j < 200; j++ {
				if (i+j)%2 == 0 {
					r.recordHit()
				} else {
					r.recordMiss()
				}
			}
		}(i)
	}
	wg.Wait()
	if calls.Load() == 0 {
		t.Error("expected at least one OnStateChange callback")
	}
	if s := r.State(); s != "reachable" && s != "absent" {
		t.Errorf("final State=%q, want reachable or absent", s)
	}
}

// --- heartbeat disabled (Heartbeat == "") ------------------------------------

func TestReconcilerHeartbeatDisabledSendForwardsWithoutProbe(t *testing.T) {
	// Heartbeat "" with a host configured = fire-and-forget mode: no
	// probing ever runs, so the state is pinned to reachable from
	// construction and Send always forwards.
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 65535, Heartbeat: ""})
	if got := r.State(); got != "reachable" {
		t.Fatalf("heartbeat disabled: State=%q, want reachable (no probe needed)", got)
	}
	// Forwarded straight to the client — UDP write to a closed port still
	// succeeds; the point is that Send does not short-circuit to the
	// absent no-op path.
	if err := r.Send("/foo", float32(0.5)); err != nil {
		t.Errorf("Send with heartbeat disabled: got err=%v, want nil", err)
	}
}

func TestReconcilerHeartbeatDisabledRunLogsOnceAndReturns(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, nil))
	r := NewReconciler(logger, ReconcilerConfig{Host: "127.0.0.1", Port: 65535, Heartbeat: ""})

	done := make(chan error, 1)
	go func() { done <- r.Run(context.Background()) }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("Run with heartbeat disabled: got err=%v, want nil", err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("Run with heartbeat disabled should return immediately (no poll loop)")
	}
	if got := buf.String(); !strings.Contains(got, "heartbeat disabled") {
		t.Errorf("expected fire-and-forget log line, got: %q", got)
	}
	if got := r.State(); got != "reachable" {
		t.Errorf("after Run: State=%q, want reachable", got)
	}
}

func TestReconcilerNoHostStaysFullyDisabled(t *testing.T) {
	// Host == "" means OSC mixer control is off entirely — even with the
	// heartbeat also empty, nothing must be treated as reachable and Send
	// stays a no-op.
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "", Heartbeat: ""})
	if got := r.State(); got != "absent" {
		t.Errorf("no host: State=%q, want absent", got)
	}
	if err := r.Run(context.Background()); err != nil {
		t.Errorf("Run with no host: got err=%v, want nil", err)
	}
	if got := r.State(); got != "absent" {
		t.Errorf("no host after Run: State=%q, want absent", got)
	}
	if err := r.Send("/foo", float32(0.5)); err != nil {
		t.Errorf("Send with no host: got err=%v, want nil (no-op)", err)
	}
}

// --- oscPingPacket -----------------------------------------------------------

func TestOscPingPacketXinfoMatchesLegacyLiteral(t *testing.T) {
	// Byte-identical invariant with the hand-rolled /xinfo packet the
	// reconciler historically sent to X-Air mixers.
	want := []byte{'/', 'x', 'i', 'n', 'f', 'o', 0, 0, ',', 0, 0, 0}
	if got := oscPingPacket("/xinfo"); !bytes.Equal(got, want) {
		t.Errorf("oscPingPacket(\"/xinfo\") = % x, want % x", got, want)
	}
}

func TestOscPingPacketCustomAddresses(t *testing.T) {
	cases := []struct {
		addr string
		want []byte
	}{
		// 7 chars + NUL = 8: already 4-aligned, no extra padding.
		{"/status", []byte{'/', 's', 't', 'a', 't', 'u', 's', 0, ',', 0, 0, 0}},
		// 5 chars + NUL = 6: pad to 8.
		{"/ping", []byte{'/', 'p', 'i', 'n', 'g', 0, 0, 0, ',', 0, 0, 0}},
		// 4 chars + NUL = 5: pad to 8.
		{"/xok", []byte{'/', 'x', 'o', 'k', 0, 0, 0, 0, ',', 0, 0, 0}},
		// 3 chars + NUL = 4: exactly aligned.
		{"/ok", []byte{'/', 'o', 'k', 0, ',', 0, 0, 0}},
	}
	for _, tc := range cases {
		t.Run(tc.addr, func(t *testing.T) {
			got := oscPingPacket(tc.addr)
			if !bytes.Equal(got, tc.want) {
				t.Errorf("oscPingPacket(%q) = % x, want % x", tc.addr, got, tc.want)
			}
			if len(got)%4 != 0 {
				t.Errorf("packet length %d not 4-byte aligned", len(got))
			}
		})
	}
}
