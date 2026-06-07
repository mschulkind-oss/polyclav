package osc

import (
	"io"
	"log/slog"
	"testing"
	"time"
)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestReconcilerDefaults(t *testing.T) {
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 10024})
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
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 10024})
	r.recordHit()
	if got := r.State(); got != "reachable" {
		t.Errorf("after recordHit: State=%q, want reachable", got)
	}
}

func TestReconcilerMissesBelowThresholdStayReachable(t *testing.T) {
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 10024, MissThreshold: 3})
	r.recordHit()
	r.recordMiss()
	r.recordMiss()
	if got := r.State(); got != "reachable" {
		t.Errorf("after 2 misses (threshold 3): State=%q, want reachable", got)
	}
}

func TestReconcilerMissesAtThresholdGoAbsent(t *testing.T) {
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 10024, MissThreshold: 3})
	r.recordHit()
	r.recordMiss()
	r.recordMiss()
	r.recordMiss()
	if got := r.State(); got != "absent" {
		t.Errorf("after 3 misses (threshold 3): State=%q, want absent", got)
	}
}

func TestReconcilerRecoversAfterAbsent(t *testing.T) {
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 10024, MissThreshold: 3})
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
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 10024})
	if got := r.State(); got != "absent" {
		t.Fatalf("setup: State=%q, want absent", got)
	}
	if err := r.Send("/foo", float32(0.5)); err != nil {
		t.Errorf("Send while absent: got err=%v, want nil (no-op)", err)
	}
}

func TestReconcilerSendCallsClientWhenReachable(t *testing.T) {
	r := NewReconciler(discardLogger(), ReconcilerConfig{Host: "127.0.0.1", Port: 65535})
	r.recordHit()
	// Send to a likely-closed UDP port. UDP is connectionless so Write
	// generally succeeds even with no listener; we just verify no panic
	// and that the code path runs.
	_ = r.Send("/foo", float32(0.1))
}
