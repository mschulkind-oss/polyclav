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
		Patches: map[string]PatchState{
			"ydp-grand": {Knob: Knob{Volume: 0.8, Reverb: 0.2, Compressor: 0.1}},
			"rhodes":    {Knob: Knob{Volume: 0.6, Reverb: 0.4, Compressor: 0.3}},
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

// ---- per-patch synth persistence (ROADMAP §3) -------------------------------

// testSynth returns a fully-populated synth block so round-trip tests
// exercise every field, including negative octaves/detunes.
func testSynth() SynthState {
	return SynthState{
		Resonance: 0.7,
		FilterEnv: FilterEnvState{Attack: 0.005, Decay: 0.6, Sustain: 0.4, Release: 0.6, Amount: 0.3},
		Oscs: [3]OscState{
			{Wave: "saw", Octave: 0, DetuneCents: 0, Level: 1.0},
			{Wave: "square", Octave: 0, DetuneCents: -7, Level: 0.5},
			{Wave: "pulse", Octave: -1, DetuneCents: 5, Level: 0.25},
		},
		Noise: 0.1,
		Glide: 0.05,
	}
}

func TestSynthRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	want := testSynth()

	store := NewStore(path, 10*time.Millisecond, slog.Default(), Snapshot{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = store.Run(ctx); close(done) }()

	// One native patch with knobs + synth, one soundfont patch with
	// knobs only — the latter must stay synth-free through the trip.
	store.UpdatePatchKnob("moog", "volume", 0.8)
	store.UpdatePatchSynth("moog", want)
	store.UpdatePatchKnob("ydp-grand", "volume", 0.6)

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
	moog, ok := got.Patches["moog"]
	if !ok {
		t.Fatal("moog entry missing")
	}
	if moog.Volume != 0.8 {
		t.Errorf("moog volume = %v, want 0.8", moog.Volume)
	}
	if moog.Synth == nil {
		t.Fatal("moog synth table missing after round trip")
	}
	if *moog.Synth != want {
		t.Errorf("moog synth = %+v, want %+v", *moog.Synth, want)
	}
	grand, ok := got.Patches["ydp-grand"]
	if !ok {
		t.Fatal("ydp-grand entry missing")
	}
	if grand.Synth != nil {
		t.Errorf("ydp-grand grew a synth table: %+v", *grand.Synth)
	}

	// The reloaded store answers PatchSynth the same way.
	store2 := NewStore(path, time.Second, slog.Default(), got)
	syn, ok := store2.PatchSynth("moog")
	if !ok || syn != want {
		t.Errorf("PatchSynth(moog) after reload = %+v/%v, want %+v/true", syn, ok, want)
	}
	if _, ok := store2.PatchSynth("ydp-grand"); ok {
		t.Error("PatchSynth(ydp-grand): expected ok=false for knob-only patch")
	}
}

func TestLoadCorruptSynthTable(t *testing.T) {
	dir := t.TempDir()
	for name, body := range map[string]string{
		"non-numeric resonance": "[patches.moog.synth]\nresonance = \"high\"\n",
		"malformed table":       "[patches.moog.synth\nresonance = 0.5\n",
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(dir, "state.toml")
			if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
				t.Fatalf("seed file: %v", err)
			}
			if _, err := Load(path); err == nil {
				t.Fatal("Load corrupt synth: expected error, got nil")
			}
		})
	}
}

// TestLoadLegacySchemaWithoutSynth pins backward compatibility: a
// state.toml written before per-patch synth persistence (knobs only, no
// [patches.X.synth] tables) must load unchanged, and its patches must
// report "no synth stored" rather than a zero-value block.
func TestLoadLegacySchemaWithoutSynth(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	legacy := `current_patch = "ydp-grand"

[patches.ydp-grand]
volume = 0.8
reverb = 0.2
compressor = 0.1
`
	if err := os.WriteFile(path, []byte(legacy), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	snap, err := Load(path)
	if err != nil {
		t.Fatalf("Load legacy: %v", err)
	}
	if snap.CurrentPatch != "ydp-grand" {
		t.Errorf("CurrentPatch = %q, want ydp-grand", snap.CurrentPatch)
	}
	p, ok := snap.Patches["ydp-grand"]
	if !ok {
		t.Fatal("ydp-grand entry missing")
	}
	if want := (Knob{Volume: 0.8, Reverb: 0.2, Compressor: 0.1}); p.Knob != want {
		t.Errorf("knobs = %+v, want %+v", p.Knob, want)
	}
	if p.Synth != nil {
		t.Errorf("legacy patch grew a synth table: %+v", *p.Synth)
	}

	store := NewStore(path, time.Second, slog.Default(), snap)
	if _, ok := store.PatchSynth("ydp-grand"); ok {
		t.Error("PatchSynth on legacy patch: expected ok=false")
	}
	if k := store.PatchKnob("ydp-grand"); k != p.Knob {
		t.Errorf("PatchKnob = %+v, want %+v", k, p.Knob)
	}
}

func TestPatchSynthAbsent(t *testing.T) {
	store := NewStore("/dev/null/never-written", time.Second, slog.Default(), Snapshot{})
	if syn, ok := store.PatchSynth("never-set"); ok || syn != (SynthState{}) {
		t.Errorf("PatchSynth(absent patch) = %+v/%v, want zero/false", syn, ok)
	}
	// A patch that exists via a knob write still has no synth block.
	store.UpdatePatchKnob("moog", "volume", 0.5)
	if _, ok := store.PatchSynth("moog"); ok {
		t.Error("PatchSynth(knob-only patch): expected ok=false")
	}
}

func TestUpdatePatchSynthCreatesEntryWithDefaultKnobs(t *testing.T) {
	store := NewStore("/dev/null/never-written", time.Second, slog.Default(), Snapshot{})
	want := testSynth()
	store.UpdatePatchSynth("moog", want)

	if syn, ok := store.PatchSynth("moog"); !ok || syn != want {
		t.Errorf("PatchSynth = %+v/%v, want %+v/true", syn, ok, want)
	}
	if k := store.PatchKnob("moog"); k != Defaults() {
		t.Errorf("first synth write must seed default knobs, got %+v", k)
	}
}

func TestUpdatePatchKnobPreservesSynth(t *testing.T) {
	store := NewStore("/dev/null/never-written", time.Second, slog.Default(), Snapshot{})
	want := testSynth()
	store.UpdatePatchSynth("moog", want)
	store.UpdatePatchKnob("moog", "volume", 0.3)

	if syn, ok := store.PatchSynth("moog"); !ok || syn != want {
		t.Errorf("synth block lost by knob update: %+v/%v", syn, ok)
	}
	if v := store.PatchKnob("moog").Volume; v != 0.3 {
		t.Errorf("volume = %v, want 0.3", v)
	}
}

// TestSynthCopySemantics pins the aliasing contract: neither mutating the
// caller's SynthState after UpdatePatchSynth nor mutating a Snapshot()
// result may leak into the store.
func TestSynthCopySemantics(t *testing.T) {
	store := NewStore("/dev/null/never-written", time.Second, slog.Default(), Snapshot{})
	want := testSynth()
	arg := want
	store.UpdatePatchSynth("moog", arg)
	arg.Resonance = 0.99 // caller keeps ownership of its copy

	if syn, _ := store.PatchSynth("moog"); syn != want {
		t.Errorf("caller mutation leaked into store: %+v", syn)
	}

	snap := store.Snapshot()
	snap.Patches["moog"].Synth.Resonance = 0.11 // deep copy — must not alias
	if syn, _ := store.PatchSynth("moog"); syn != want {
		t.Errorf("Snapshot mutation leaked into store: %+v", syn)
	}
}

func TestSynthUpdateFlushesAfterDebounce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")

	store := NewStore(path, 20*time.Millisecond, slog.Default(), Snapshot{})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan struct{})
	go func() { _ = store.Run(ctx); close(done) }()

	want := testSynth()
	store.UpdatePatchSynth("moog", want)

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
	p, ok := snap.Patches["moog"]
	if !ok || p.Synth == nil {
		t.Fatalf("moog synth missing after flush: %+v (ok=%v)", p, ok)
	}
	if *p.Synth != want {
		t.Errorf("flushed synth = %+v, want %+v", *p.Synth, want)
	}

	cancel()
	<-done
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
