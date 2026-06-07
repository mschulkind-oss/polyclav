package state

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLoadMissingFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "does-not-exist.toml")
	snap, err := Load(path)
	if err != nil {
		t.Fatalf("Load missing: unexpected error: %v", err)
	}
	if snap.CurrentPatch != "" {
		t.Errorf("CurrentPatch = %q, want empty", snap.CurrentPatch)
	}
	if snap.Patches == nil {
		t.Error("Patches is nil, want empty map")
	}
	if len(snap.Patches) != 0 {
		t.Errorf("len(Patches) = %d, want 0", len(snap.Patches))
	}
}

func TestLoadCorruptFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	if err := os.WriteFile(path, []byte("this is not = = valid [[ toml"), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	_, err := Load(path)
	if err == nil {
		t.Fatal("Load corrupt: expected error, got nil")
	}
}

func TestLoadRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")

	initial := Snapshot{
		CurrentPatch: "rhodes",
		Patches: map[string]Knob{
			"ydp-grand": {Volume: 0.8, Reverb: 0.2, Compressor: 0.1},
			"rhodes":    {Volume: 0.6, Reverb: 0.4, Compressor: 0.3},
		},
	}

	store := NewStore(path, 10*time.Millisecond, slog.Default(), Snapshot{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = store.Run(ctx); close(done) }()

	store.SetCurrentPatch(initial.CurrentPatch)
	for name, k := range initial.Patches {
		store.UpdatePatchKnob(name, "volume", k.Volume)
		store.UpdatePatchKnob(name, "reverb", k.Reverb)
		store.UpdatePatchKnob(name, "compressor", k.Compressor)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s")
	}

	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load round-trip: %v", err)
	}
	if got.CurrentPatch != initial.CurrentPatch {
		t.Errorf("CurrentPatch = %q, want %q", got.CurrentPatch, initial.CurrentPatch)
	}
	if len(got.Patches) != len(initial.Patches) {
		t.Fatalf("len(Patches) = %d, want %d", len(got.Patches), len(initial.Patches))
	}
	for name, want := range initial.Patches {
		gotK, ok := got.Patches[name]
		if !ok {
			t.Errorf("patch %q missing", name)
			continue
		}
		if gotK != want {
			t.Errorf("patch %q = %+v, want %+v", name, gotK, want)
		}
	}
}

func TestUpdateFlushesAfterDebounce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	debounce := 20 * time.Millisecond

	store := NewStore(path, debounce, slog.Default(), Snapshot{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = store.Run(ctx); close(done) }()

	store.UpdatePatchKnob("ydp-grand", "volume", 0.42)

	deadline := time.Now().Add(1 * time.Second)
	var found bool
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			found = true
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if !found {
		t.Fatalf("state file %q did not appear within 1s", path)
	}

	snap, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	k, ok := snap.Patches["ydp-grand"]
	if !ok {
		t.Fatal("ydp-grand entry missing after flush")
	}
	if k.Volume != 0.42 {
		t.Errorf("Volume = %v, want 0.42", k.Volume)
	}

	cancel()
	<-done
}

func TestUpdateWithoutRunDoesNotFlush(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")

	store := NewStore(path, 10*time.Millisecond, slog.Default(), Snapshot{})
	store.UpdatePatchKnob("ydp-grand", "volume", 0.5)
	store.SetCurrentPatch("rhodes")

	time.Sleep(50 * time.Millisecond)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("state file exists without Run: stat err=%v (want IsNotExist)", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf("state .tmp file exists without Run: stat err=%v (want IsNotExist)", err)
	}
}

func TestAtomicWriteNoTempLeftover(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	debounce := 10 * time.Millisecond

	store := NewStore(path, debounce, slog.Default(), Snapshot{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = store.Run(ctx); close(done) }()

	store.UpdatePatchKnob("ydp-grand", "volume", 0.7)

	deadline := time.Now().Add(1 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := os.Stat(path); err == nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := os.Stat(path); err != nil {
		t.Fatalf("state file missing: %v", err)
	}
	if _, err := os.Stat(path + ".tmp"); !os.IsNotExist(err) {
		t.Errorf(".tmp file leftover after successful flush: stat err=%v", err)
	}

	cancel()
	<-done
}

func TestCtxCancelFlushesPending(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	debounce := 5 * time.Second

	store := NewStore(path, debounce, slog.Default(), Snapshot{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = store.Run(ctx); close(done) }()

	store.UpdatePatchKnob("ydp-grand", "volume", 0.55)
	store.SetCurrentPatch("ydp-grand")

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not exit within 2s")
	}

	snap, err := Load(path)
	if err != nil {
		t.Fatalf("Load after cancel: %v", err)
	}
	if snap.CurrentPatch != "ydp-grand" {
		t.Errorf("CurrentPatch = %q, want %q", snap.CurrentPatch, "ydp-grand")
	}
	k, ok := snap.Patches["ydp-grand"]
	if !ok {
		t.Fatal("ydp-grand patch missing after cancel flush")
	}
	if k.Volume != 0.55 {
		t.Errorf("Volume = %v, want 0.55", k.Volume)
	}
}

func TestPatchKnobReturnsDefaultsWhenAbsent(t *testing.T) {
	store := NewStore("/dev/null/never-written", time.Second, slog.Default(), Snapshot{})
	got := store.PatchKnob("never-set")
	want := Defaults()
	if got != want {
		t.Errorf("PatchKnob(absent) = %+v, want %+v", got, want)
	}
}

func TestInvalidFieldIsNoop(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	store := NewStore(path, 5*time.Millisecond, slog.Default(), Snapshot{})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = store.Run(ctx); close(done) }()

	store.UpdatePatchKnob("ydp-grand", "bogus-field", 0.9)

	time.Sleep(50 * time.Millisecond)

	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Errorf("state file exists after bogus field update: stat err=%v", err)
	}

	k := store.PatchKnob("ydp-grand")
	if k != Defaults() {
		t.Errorf("PatchKnob = %+v, want Defaults %+v", k, Defaults())
	}

	cancel()
	<-done
}
