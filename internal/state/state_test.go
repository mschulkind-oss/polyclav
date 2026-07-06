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
// exercise every field, including negative octaves/detunes and
// non-default Phase 3/4 values (so a lost field can't hide behind its
// default).
func testSynth() SynthState {
	return SynthState{
		Resonance: 0.7,
		FilterEnv: FilterEnvState{Attack: 0.005, Decay: 0.6, Sustain: 0.4, Release: 0.6, Amount: 0.3},
		AmpEnv:    AmpEnvState{Attack: 0.01, Decay: 0.3, Sustain: 0.6, Release: 0.5},
		Oscs: [3]OscState{
			{Wave: "saw", Octave: 0, DetuneCents: 0, Level: 1.0},
			{Wave: "square", Octave: 0, DetuneCents: -7, Level: 0.5},
			{Wave: "pulse", Octave: -1, DetuneCents: 5, Level: 0.25},
		},
		Noise:      0.1,
		Glide:      0.05,
		PulseWidth: 0.5,
		Drive:      0.4,
		VelRouting: VelRoutingState{ToCutoff: 0.3, ToAmp: 0.8},
		KbdTrack:   0.6,
		LFO:        LFOState{Wave: "square", RateHz: 2.5, ToPitchCents: 15, ToCutoffOct: 0.5, ToAmp: 0.2},
		BendRange:  7,
		VoiceMode:  "poly",
		Oversample: true,
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

// TestLoadOldSynthSchemaFillsEngineDefaults pins the Phase 3/4 backward
// compatibility contract: a state.toml whose synth block predates the
// amp_env/pulse_width/drive/vel_routing/kbd_track/lfo/bend_range/
// voice_mode/oversample fields must load with those fields at the
// ENGINE defaults, not Go zero values. Zeros would be audible damage:
// vel_routing.to_amp = 0 mutes velocity response, lfo rate 0 is below
// the engine minimum, amp_env all-zero clicks every note off.
func TestLoadOldSynthSchemaFillsEngineDefaults(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	old := `current_patch = "moog"

[patches.moog]
volume = 0.8
reverb = 0.2
compressor = 0.1

[patches.moog.synth]
resonance = 0.7
noise = 0.1
glide = 0.05

[patches.moog.synth.filter_env]
attack = 0.005
decay = 0.6
sustain = 0.4
release = 0.6
amount = 0.3

[[patches.moog.synth.oscs]]
wave = "saw"
octave = 0
detune_cents = 0.0
level = 1.0

[[patches.moog.synth.oscs]]
wave = "square"
octave = 0
detune_cents = -7.0
level = 0.5

[[patches.moog.synth.oscs]]
wave = "pulse"
octave = -1
detune_cents = 5.0
level = 0.25
`
	if err := os.WriteFile(path, []byte(old), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	snap, err := Load(path)
	if err != nil {
		t.Fatalf("Load old schema: %v", err)
	}
	p, ok := snap.Patches["moog"]
	if !ok || p.Synth == nil {
		t.Fatalf("moog synth block missing: %+v (ok=%v)", p, ok)
	}
	s := p.Synth

	// The old fields decode verbatim.
	if s.Resonance != 0.7 || s.Noise != 0.1 || s.Glide != 0.05 {
		t.Errorf("old scalars: want (0.7, 0.1, 0.05), got (%v, %v, %v)", s.Resonance, s.Noise, s.Glide)
	}
	if s.FilterEnv.Decay != 0.6 || s.FilterEnv.Amount != 0.3 {
		t.Errorf("old filter env changed: %+v", s.FilterEnv)
	}
	if s.Oscs[1].Wave != "square" || s.Oscs[1].DetuneCents != -7 {
		t.Errorf("old oscs changed: %+v", s.Oscs)
	}

	// The absent Phase 3/4 fields land at the ENGINE defaults.
	if want := (AmpEnvState{Attack: 0.005, Decay: 0.2, Sustain: 0.7, Release: 0.4}); s.AmpEnv != want {
		t.Errorf("amp_env: want engine defaults %+v, got %+v", want, s.AmpEnv)
	}
	if s.PulseWidth != 0.25 {
		t.Errorf("pulse_width: want 0.25, got %v", s.PulseWidth)
	}
	if s.Drive != 0 {
		t.Errorf("drive: want 0, got %v", s.Drive)
	}
	if want := (VelRoutingState{ToCutoff: 0, ToAmp: 1}); s.VelRouting != want {
		t.Errorf("vel_routing: want %+v (to_amp MUST NOT zero out), got %+v", want, s.VelRouting)
	}
	if s.KbdTrack != 0 {
		t.Errorf("kbd_track: want 0, got %v", s.KbdTrack)
	}
	if want := (LFOState{Wave: "triangle", RateHz: 5, ToPitchCents: 0, ToCutoffOct: 0, ToAmp: 0}); s.LFO != want {
		t.Errorf("lfo: want engine defaults %+v, got %+v", want, s.LFO)
	}
	if s.BendRange != 2 {
		t.Errorf("bend_range: want 2, got %v", s.BendRange)
	}
	if s.VoiceMode != "mono_legato" {
		t.Errorf("voice_mode: want mono_legato, got %q", s.VoiceMode)
	}
	if s.Oversample {
		t.Error("oversample: want false")
	}
}

// TestLoadPartialNewSynthKeysKeepExplicitValues is fillSynthDefaults's
// per-leaf contract: a hand-edited file carrying SOME new keys keeps
// its explicit values (including explicit zeros/non-defaults) while the
// missing siblings — even inside the same sub-table — default sanely.
func TestLoadPartialNewSynthKeysKeepExplicitValues(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	body := `[patches.moog.synth]
resonance = 0.5
pulse_width = 0.1
bend_range = 0.0

[patches.moog.synth.vel_routing]
to_cutoff = 0.5

[patches.moog.synth.lfo]
rate_hz = 0.5
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	snap, err := Load(path)
	if err != nil {
		t.Fatalf("Load partial: %v", err)
	}
	s := snap.Patches["moog"].Synth
	if s == nil {
		t.Fatal("moog synth block missing")
	}
	// Explicit values survive — including an explicit 0 bend_range and a
	// non-default pulse width.
	if s.PulseWidth != 0.1 || s.BendRange != 0 {
		t.Errorf("explicit values: want (0.1, 0), got (%v, %v)", s.PulseWidth, s.BendRange)
	}
	// Sibling keys inside a present sub-table still default per leaf.
	if want := (VelRoutingState{ToCutoff: 0.5, ToAmp: 1}); s.VelRouting != want {
		t.Errorf("vel_routing: want %+v, got %+v", want, s.VelRouting)
	}
	if s.LFO.Wave != "triangle" || s.LFO.RateHz != 0.5 {
		t.Errorf("lfo: want (triangle, 0.5), got (%q, %v)", s.LFO.Wave, s.LFO.RateHz)
	}
	// Whole absent sub-tables default too.
	if want := (AmpEnvState{Attack: 0.005, Decay: 0.2, Sustain: 0.7, Release: 0.4}); s.AmpEnv != want {
		t.Errorf("amp_env: want engine defaults %+v, got %+v", want, s.AmpEnv)
	}
	if s.VoiceMode != "mono_legato" {
		t.Errorf("voice_mode: want mono_legato, got %q", s.VoiceMode)
	}
}

// TestLoadPartialPhase2SynthBlock pins the Phase-2 half of the backfill
// contract: a hand-written synth block carrying ONLY one key must load
// with every OTHER nonzero-default Phase-2 field at the engine default —
// not zero-filled (resonance 0 kills the filter character, filter env
// times 0 click, osc waves "" are invalid, osc1 level 0 is silence).
func TestLoadPartialPhase2SynthBlock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	body := `[patches.moog.synth]
drive = 0.5
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	snap, err := Load(path)
	if err != nil {
		t.Fatalf("Load partial: %v", err)
	}
	s := snap.Patches["moog"].Synth
	if s == nil {
		t.Fatal("moog synth block missing")
	}
	// The one explicit key survives.
	if s.Drive != 0.5 {
		t.Errorf("drive: want 0.5, got %v", s.Drive)
	}
	// Phase-2 defaults.
	if s.Resonance != 0.3 {
		t.Errorf("resonance: want 0.3, got %v", s.Resonance)
	}
	if want := (FilterEnvState{Attack: 0.005, Decay: 0.6, Sustain: 0.4, Release: 0.6, Amount: 0}); s.FilterEnv != want {
		t.Errorf("filter_env: want engine defaults %+v, got %+v", want, s.FilterEnv)
	}
	if s.Oscs != defaultOscs() {
		t.Errorf("oscs: want default bank %+v, got %+v", defaultOscs(), s.Oscs)
	}
	// Zero-default Phase-2 fields stay zero.
	if s.Noise != 0 || s.Glide != 0 {
		t.Errorf("noise/glide: want 0/0, got %v/%v", s.Noise, s.Glide)
	}
	// The Phase 3/4 fill still applies alongside.
	if want := (AmpEnvState{Attack: 0.005, Decay: 0.2, Sustain: 0.7, Release: 0.4}); s.AmpEnv != want {
		t.Errorf("amp_env: want engine defaults %+v, got %+v", want, s.AmpEnv)
	}
	if s.VelRouting.ToAmp != 1 || s.PulseWidth != 0.25 || s.VoiceMode != "mono_legato" {
		t.Errorf("phase 3/4 defaults regressed: %+v", *s)
	}
}

// TestLoadPartialOscElements pins per-element osc backfill for the
// standard [[...oscs]] array-of-tables form: keys present in an element
// keep their explicit values (including explicit zeros), absent leaves
// take that element's engine default.
func TestLoadPartialOscElements(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	body := `[patches.moog.synth]
resonance = 0.5

[[patches.moog.synth.oscs]]
level = 0.5

[[patches.moog.synth.oscs]]
wave = "square"
detune_cents = 0.0

[[patches.moog.synth.oscs]]
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	snap, err := Load(path)
	if err != nil {
		t.Fatalf("Load partial oscs: %v", err)
	}
	s := snap.Patches["moog"].Synth
	if s == nil {
		t.Fatal("moog synth block missing")
	}
	if s.Resonance != 0.5 {
		t.Errorf("resonance: want explicit 0.5, got %v", s.Resonance)
	}
	// Osc 1: explicit level 0.5 survives; absent wave defaults.
	if want := (OscState{Wave: "saw", Octave: 0, DetuneCents: 0, Level: 0.5}); s.Oscs[0] != want {
		t.Errorf("osc 1: want %+v, got %+v", want, s.Oscs[0])
	}
	// Osc 2: explicit wave and EXPLICIT detune 0 survive (default is -7);
	// absent level stays at its 0 default.
	if want := (OscState{Wave: "square", Octave: 0, DetuneCents: 0, Level: 0}); s.Oscs[1] != want {
		t.Errorf("osc 2: want %+v, got %+v", want, s.Oscs[1])
	}
	// Osc 3: empty element takes the full per-element default.
	if want := (OscState{Wave: "saw", Octave: -1, DetuneCents: 5, Level: 0}); s.Oscs[2] != want {
		t.Errorf("osc 3: want %+v, got %+v", want, s.Oscs[2])
	}
}

// TestLoadInlineOscArrayConservative: the inline `oscs = [...]` form
// flattens element keys in the TOML metadata, so per-element
// attribution is impossible — only invalid empty waves are repaired and
// every numeric leaf is kept exactly as decoded.
func TestLoadInlineOscArrayConservative(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	body := `[patches.moog.synth]
oscs = [{level = 0.5}, {wave = "square"}, {}]
`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("seed file: %v", err)
	}

	snap, err := Load(path)
	if err != nil {
		t.Fatalf("Load inline oscs: %v", err)
	}
	s := snap.Patches["moog"].Synth
	if s == nil {
		t.Fatal("moog synth block missing")
	}
	if want := (OscState{Wave: "saw", Level: 0.5}); s.Oscs[0] != want {
		t.Errorf("osc 1: want %+v, got %+v", want, s.Oscs[0])
	}
	// The explicit square must NOT be clobbered back to saw.
	if want := (OscState{Wave: "square"}); s.Oscs[1] != want {
		t.Errorf("osc 2: want %+v, got %+v", want, s.Oscs[1])
	}
	if want := (OscState{Wave: "saw"}); s.Oscs[2] != want {
		t.Errorf("osc 3: want %+v, got %+v", want, s.Oscs[2])
	}
}

// TestSynthNewFieldsRoundTripVerbatim: a store flush writes every new
// field, and a reload does NOT re-default them (the fill only fires on
// truly absent keys — a saved non-default must never be clobbered).
func TestSynthNewFieldsRoundTripVerbatim(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "state.toml")
	want := testSynth()

	store := NewStore(path, 10*time.Millisecond, slog.Default(), Snapshot{})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = store.Run(ctx); close(done) }()
	store.UpdatePatchSynth("moog", want)
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
	p := got.Patches["moog"]
	if p.Synth == nil {
		t.Fatal("moog synth block missing after round trip")
	}
	if *p.Synth != want {
		t.Errorf("round trip changed the block:\nwant %+v\ngot  %+v", want, *p.Synth)
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
