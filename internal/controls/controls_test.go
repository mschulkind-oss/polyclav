package controls

import (
	"errors"
	"fmt"
	"math"
	"sync"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/patches"
	"github.com/mschulkind-oss/polyclav/internal/state"
)

// oscCall records one SetNativeOsc apply for assertion.
type oscCall struct {
	idx           int
	wave          string
	octave        int
	detune, level float32
}

// fakeAudio records every apply so tests can assert the controls layer
// actually drove the engine (mirrors internal/patches's fakeAudio style).
type fakeAudio struct {
	mu sync.Mutex

	volume, reverb, compressor       float32
	cutoffHz                         float32
	masteringComp, limiterCeilingDB  float32
	resonance, noise, glide          float32
	feA, feD, feS, feR, feAmt        float32
	lastOsc                          oscCall
	oscErr                           error // forced SetNativeOsc failure
	volumeCalls, reverbCalls         int
	compressorCalls, cutoffCalls     int
	masteringCalls, limiterCalls     int
	resonanceCalls, filterEnvCalls   int
	oscCalls, noiseCalls, glideCalls int
}

func (f *fakeAudio) SetMasterVolume(v float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.volume = v
	f.volumeCalls++
}

func (f *fakeAudio) SetReverb(v float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reverb = v
	f.reverbCalls++
}

func (f *fakeAudio) SetCompressor(v float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.compressor = v
	f.compressorCalls++
}

func (f *fakeAudio) SetNativeCutoffHz(hz float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cutoffHz = hz
	f.cutoffCalls++
}

func (f *fakeAudio) SetMasteringCompressor(v float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.masteringComp = v
	f.masteringCalls++
}

func (f *fakeAudio) SetLimiterCeilingDB(v float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limiterCeilingDB = v
	f.limiterCalls++
}

func (f *fakeAudio) SetNativeResonance(v float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resonance = v
	f.resonanceCalls++
}

func (f *fakeAudio) SetNativeFilterEnv(a, d, s, r, amount float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.feA, f.feD, f.feS, f.feR, f.feAmt = a, d, s, r, amount
	f.filterEnvCalls++
}

func (f *fakeAudio) SetNativeOsc(idx int, wave string, octave int, detuneCents, level float32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.oscErr != nil {
		return f.oscErr
	}
	f.lastOsc = oscCall{idx: idx, wave: wave, octave: octave, detune: detuneCents, level: level}
	f.oscCalls++
	return nil
}

func (f *fakeAudio) SetNativeNoise(level float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noise = level
	f.noiseCalls++
}

func (f *fakeAudio) SetNativeGlide(s float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.glide = s
	f.glideCalls++
}

// synthCalls sums every native-synth apply, for "audio untouched" checks.
func (f *fakeAudio) synthCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.resonanceCalls + f.filterEnvCalls + f.oscCalls + f.noiseCalls + f.glideCalls
}

// fakeRegistry implements Registry over an in-memory patch list with no
// audio side effects.
type fakeRegistry struct {
	patches   []patches.Patch
	current   int // -1 == none selected
	selectErr error
}

func newFakeRegistry(ps ...patches.Patch) *fakeRegistry {
	return &fakeRegistry{patches: ps, current: -1}
}

func (f *fakeRegistry) All() []patches.Patch {
	out := make([]patches.Patch, len(f.patches))
	copy(out, f.patches)
	return out
}

func (f *fakeRegistry) Current() *patches.Patch {
	if f.current < 0 || f.current >= len(f.patches) {
		return nil
	}
	p := f.patches[f.current]
	return &p
}

func (f *fakeRegistry) Select(name string) error {
	if f.selectErr != nil {
		return f.selectErr
	}
	for i, p := range f.patches {
		if p.Name == name {
			f.current = i
			return nil
		}
	}
	return fmt.Errorf("patch %q not found", name)
}

func (f *fakeRegistry) SelectIndex(i int) error {
	if f.selectErr != nil {
		return f.selectErr
	}
	if i < 0 || i >= len(f.patches) {
		return fmt.Errorf("patch index %d out of range [0, %d)", i, len(f.patches))
	}
	f.current = i
	return nil
}

// knobUpdate records one UpdatePatchKnob call for assertion.
type knobUpdate struct {
	patch, field string
	value        float32
}

// fakeStore implements StateStore in memory. Deliberately unsynchronized:
// every call happens under Controls.applyMu, so the race detector flags
// any path that escapes the writer lock.
type fakeStore struct {
	knobs        map[string]state.Knob
	synths       map[string]state.SynthState
	currentPatch string
	updates      []knobUpdate
	synthUpdates int
	setCurrCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		knobs:  map[string]state.Knob{},
		synths: map[string]state.SynthState{},
	}
}

func (f *fakeStore) PatchKnob(name string) state.Knob {
	if k, ok := f.knobs[name]; ok {
		return k
	}
	return state.Defaults()
}

func (f *fakeStore) UpdatePatchKnob(name, field string, value float32) {
	k := f.PatchKnob(name)
	switch field {
	case "volume":
		k.Volume = value
	case "reverb":
		k.Reverb = value
	case "compressor":
		k.Compressor = value
	}
	f.knobs[name] = k
	f.updates = append(f.updates, knobUpdate{patch: name, field: field, value: value})
}

func (f *fakeStore) PatchSynth(name string) (state.SynthState, bool) {
	syn, ok := f.synths[name]
	return syn, ok
}

func (f *fakeStore) UpdatePatchSynth(name string, syn state.SynthState) {
	f.synths[name] = syn
	f.synthUpdates++
}

func (f *fakeStore) SetCurrentPatch(name string) {
	f.currentPatch = name
	f.setCurrCalls++
}

// fixture bundles a Controls with observable fakes and a subscribed
// change channel.
type fixture struct {
	audio *fakeAudio
	reg   *fakeRegistry
	st    *fakeStore
	hub   *Hub
	ch    <-chan Change
	c     *Controls
}

func newFixture(t *testing.T, ps ...patches.Patch) *fixture {
	t.Helper()
	f := &fixture{
		audio: &fakeAudio{},
		reg:   newFakeRegistry(ps...),
		st:    newFakeStore(),
		hub:   NewHub(),
	}
	ch, cancel := f.hub.Subscribe(64)
	t.Cleanup(cancel)
	f.ch = ch
	f.c = New(nil, f.audio, f.reg, f.st, f.hub)
	return f
}

// recvChange pops the next buffered change or fails the test. Publish is
// synchronous, so the change is already buffered when the setter returns.
func recvChange(t *testing.T, ch <-chan Change) Change {
	t.Helper()
	select {
	case c := <-ch:
		return c
	default:
		t.Fatal("expected a published change, got none")
		return Change{}
	}
}

func assertNoChange(t *testing.T, ch <-chan Change) {
	t.Helper()
	select {
	case c := <-ch:
		t.Fatalf("expected no published change, got %+v", c)
	default:
	}
}

func approxEq(a, b float32) bool {
	return math.Abs(float64(a-b)) < 1e-3
}

var (
	sfPatch      = patches.Patch{Name: "salamander", Display: "Salamander", Type: "soundfont"}
	nativePatch  = patches.Patch{Name: "moog", Display: "Moog", Type: "native", Engine: "minimoog"}
	nativePatch2 = patches.Patch{Name: "moog2", Display: "Moog II", Type: "native", Engine: "minimoog"}
)

func TestAbsoluteSetters(t *testing.T) {
	cases := []struct {
		name   string
		set    func(c *Controls, v float32) (float32, error)
		field  string
		audioV func(a *fakeAudio) float32
		calls  func(a *fakeAudio) int
		stateV func(k state.Knob) float32
		in     float32
		want   float32
	}{
		{"volume", (*Controls).SetVolume, "volume",
			func(a *fakeAudio) float32 { return a.volume },
			func(a *fakeAudio) int { return a.volumeCalls },
			func(k state.Knob) float32 { return k.Volume }, 0.8, 0.8},
		{"reverb", (*Controls).SetReverb, "reverb",
			func(a *fakeAudio) float32 { return a.reverb },
			func(a *fakeAudio) int { return a.reverbCalls },
			func(k state.Knob) float32 { return k.Reverb }, 0.3, 0.3},
		{"compressor", (*Controls).SetCompressor, "compressor",
			func(a *fakeAudio) float32 { return a.compressor },
			func(a *fakeAudio) int { return a.compressorCalls },
			func(k state.Knob) float32 { return k.Compressor }, 0.6, 0.6},
		{"volume clamps high", (*Controls).SetVolume, "volume",
			func(a *fakeAudio) float32 { return a.volume },
			func(a *fakeAudio) int { return a.volumeCalls },
			func(k state.Knob) float32 { return k.Volume }, 1.7, 1.0},
		{"reverb clamps low", (*Controls).SetReverb, "reverb",
			func(a *fakeAudio) float32 { return a.reverb },
			func(a *fakeAudio) int { return a.reverbCalls },
			func(k state.Knob) float32 { return k.Reverb }, -0.5, 0.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, sfPatch)
			if err := f.reg.Select("salamander"); err != nil {
				t.Fatalf("select: %v", err)
			}

			got, err := tc.set(f.c, tc.in)
			if err != nil {
				t.Fatalf("setter returned error: %v", err)
			}
			if got != tc.want {
				t.Errorf("returned value: expected %v, got %v", tc.want, got)
			}
			if v := tc.audioV(f.audio); v != tc.want {
				t.Errorf("audio value: expected %v, got %v", tc.want, v)
			}
			if n := tc.calls(f.audio); n != 1 {
				t.Errorf("expected 1 audio call, got %d", n)
			}
			if v := tc.stateV(f.st.PatchKnob("salamander")); v != tc.want {
				t.Errorf("state value: expected %v, got %v", tc.want, v)
			}

			ch := recvChange(t, f.ch)
			if ch.Type != "params" {
				t.Errorf("expected change type %q, got %q", "params", ch.Type)
			}
			if ch.Data["field"] != tc.field {
				t.Errorf("expected Data[field]=%q, got %v", tc.field, ch.Data["field"])
			}
			if ch.Data["value"] != tc.want {
				t.Errorf("expected Data[value]=%v, got %v", tc.want, ch.Data["value"])
			}
			if ch.Data["patch"] != "salamander" {
				t.Errorf("expected Data[patch]=salamander, got %v", ch.Data["patch"])
			}
		})
	}
}

func TestAbsoluteSettersNoCurrentPatch(t *testing.T) {
	f := newFixture(t, sfPatch) // nothing selected
	for name, set := range map[string]func(v float32) (float32, error){
		"SetVolume":     f.c.SetVolume,
		"SetReverb":     f.c.SetReverb,
		"SetCompressor": f.c.SetCompressor,
	} {
		if _, err := set(0.5); err == nil {
			t.Errorf("%s: expected error with no current patch", name)
		}
	}
	if f.audio.volumeCalls+f.audio.reverbCalls+f.audio.compressorCalls != 0 {
		t.Error("audio must not be touched when no patch is selected")
	}
	if len(f.st.updates) != 0 {
		t.Error("state must not be touched when no patch is selected")
	}
	assertNoChange(t, f.ch)
}

func TestDeltaAdjusters(t *testing.T) {
	cases := []struct {
		name   string
		adjust func(c *Controls, d float32) (float32, bool)
		field  string
		start  state.Knob
		delta  float32
		want   float32
	}{
		{"volume down from stored", (*Controls).AdjustVolume, "volume",
			state.Knob{Volume: 0.5, Reverb: 0.1, Compressor: 0.2}, -0.1, 0.4},
		{"reverb up from stored", (*Controls).AdjustReverb, "reverb",
			state.Knob{Volume: 0.5, Reverb: 0.1, Compressor: 0.2}, 0.25, 0.35},
		{"compressor from defaults", (*Controls).AdjustCompressor, "compressor",
			state.Defaults(), 0.5, 0.5},
		{"volume clamps high", (*Controls).AdjustVolume, "volume",
			state.Knob{Volume: 0.9}, 0.5, 1.0},
		{"reverb clamps low", (*Controls).AdjustReverb, "reverb",
			state.Knob{Reverb: 0.05}, -0.5, 0.0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t, sfPatch)
			if err := f.reg.Select("salamander"); err != nil {
				t.Fatalf("select: %v", err)
			}
			if tc.start != state.Defaults() {
				f.st.knobs["salamander"] = tc.start
			}

			got, ok := tc.adjust(f.c, tc.delta)
			if !ok {
				t.Fatal("expected ok=true with a current patch")
			}
			if !approxEq(got, tc.want) {
				t.Errorf("returned value: expected %v, got %v", tc.want, got)
			}
			if v := knobField(f.st.PatchKnob("salamander"), tc.field); !approxEq(v, tc.want) {
				t.Errorf("state value: expected %v, got %v", tc.want, v)
			}

			ch := recvChange(t, f.ch)
			if ch.Type != "params" || ch.Data["field"] != tc.field {
				t.Errorf("expected params/%s change, got %q/%v", tc.field, ch.Type, ch.Data["field"])
			}
		})
	}
}

func TestDeltaAdjustersNoCurrentPatch(t *testing.T) {
	f := newFixture(t, sfPatch)
	for name, adjust := range map[string]func(d float32) (float32, bool){
		"AdjustVolume":     f.c.AdjustVolume,
		"AdjustReverb":     f.c.AdjustReverb,
		"AdjustCompressor": f.c.AdjustCompressor,
	} {
		if _, ok := adjust(0.1); ok {
			t.Errorf("%s: expected ok=false with no current patch", name)
		}
	}
	assertNoChange(t, f.ch)
}

func TestAdjustCutoffGatedOnPatchType(t *testing.T) {
	t.Run("no patch", func(t *testing.T) {
		f := newFixture(t, nativePatch)
		if _, ok := f.c.AdjustCutoff(0.1); ok {
			t.Error("expected ok=false with no current patch")
		}
		if f.audio.cutoffCalls != 0 {
			t.Error("audio cutoff must not be touched")
		}
		assertNoChange(t, f.ch)
	})
	t.Run("non-native patch", func(t *testing.T) {
		f := newFixture(t, sfPatch)
		if err := f.reg.Select("salamander"); err != nil {
			t.Fatalf("select: %v", err)
		}
		if _, ok := f.c.AdjustCutoff(0.1); ok {
			t.Error("expected ok=false for non-native patch")
		}
		if f.audio.cutoffCalls != 0 {
			t.Error("audio cutoff must not be touched")
		}
		assertNoChange(t, f.ch)
	})
}

func TestAdjustCutoffNative(t *testing.T) {
	f := newFixture(t, nativePatch)
	if err := f.reg.Select("moog"); err != nil {
		t.Fatalf("select: %v", err)
	}

	// Default pos 0.5 + 0.5 clamps to 1.0 -> 20 kHz.
	hz, ok := f.c.AdjustCutoff(0.5)
	if !ok {
		t.Fatal("expected ok=true for native patch")
	}
	if !approxEq(hz, 20000) {
		t.Errorf("expected 20000 Hz at pos 1.0, got %v", hz)
	}
	if !approxEq(f.audio.cutoffHz, 20000) {
		t.Errorf("audio cutoff: expected 20000, got %v", f.audio.cutoffHz)
	}
	ch := recvChange(t, f.ch)
	if ch.Type != "params" || ch.Data["field"] != "cutoff" {
		t.Errorf("expected params/cutoff change, got %q/%v", ch.Type, ch.Data["field"])
	}

	// Big negative delta clamps to pos 0 -> 20 Hz.
	hz, ok = f.c.AdjustCutoff(-2)
	if !ok || !approxEq(hz, 20) {
		t.Errorf("expected 20 Hz at pos 0, got %v (ok=%v)", hz, ok)
	}
	pos, gotHz := f.c.CutoffState()
	if pos != 0 || !approxEq(gotHz, 20) {
		t.Errorf("CutoffState: expected (0, 20), got (%v, %v)", pos, gotHz)
	}
}

func TestSetCutoffPos(t *testing.T) {
	f := newFixture(t, nativePatch, sfPatch)

	if _, err := f.c.SetCutoffPos(0.5); err == nil {
		t.Error("expected error with no current patch")
	}
	if err := f.reg.Select("salamander"); err != nil {
		t.Fatalf("select: %v", err)
	}
	if _, err := f.c.SetCutoffPos(0.5); err == nil {
		t.Error("expected error with non-native patch")
	}
	if f.audio.cutoffCalls != 0 {
		t.Error("audio cutoff must not be touched on gated calls")
	}

	if err := f.reg.Select("moog"); err != nil {
		t.Fatalf("select: %v", err)
	}
	hz, err := f.c.SetCutoffPos(0.5)
	if err != nil {
		t.Fatalf("SetCutoffPos: %v", err)
	}
	// 20 * 1000^0.5 = 20 * sqrt(1000) ≈ 632.456
	if !approxEq(hz, 632.456) {
		t.Errorf("expected ~632.456 Hz at pos 0.5, got %v", hz)
	}
	if !approxEq(f.audio.cutoffHz, 632.456) {
		t.Errorf("audio cutoff: expected ~632.456, got %v", f.audio.cutoffHz)
	}

	// Out-of-range positions clamp.
	if hz, err := f.c.SetCutoffPos(7); err != nil || !approxEq(hz, 20000) {
		t.Errorf("expected clamp to pos 1 (20 kHz), got %v err=%v", hz, err)
	}
	pos, _ := f.c.CutoffState()
	if pos != 1 {
		t.Errorf("expected stored pos 1, got %v", pos)
	}
}

func TestCutoffStateDefault(t *testing.T) {
	f := newFixture(t)
	pos, hz := f.c.CutoffState()
	if pos != 0.5 {
		t.Errorf("expected default pos 0.5, got %v", pos)
	}
	if !approxEq(hz, 632.456) {
		t.Errorf("expected ~632.456 Hz, got %v", hz)
	}
}

func TestSelectPatchRestoresKnobsAndPublishes(t *testing.T) {
	f := newFixture(t, sfPatch, nativePatch)
	f.st.knobs["salamander"] = state.Knob{Volume: 0.7, Reverb: 0.2, Compressor: 0.4}

	if err := f.c.SelectPatch("salamander"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}

	if cur := f.reg.Current(); cur == nil || cur.Name != "salamander" {
		t.Fatalf("expected registry current salamander, got %v", cur)
	}
	if f.audio.volume != 0.7 || f.audio.reverb != 0.2 || f.audio.compressor != 0.4 {
		t.Errorf("expected knobs (0.7, 0.2, 0.4) restored to audio, got (%v, %v, %v)",
			f.audio.volume, f.audio.reverb, f.audio.compressor)
	}
	if f.st.currentPatch != "salamander" {
		t.Errorf("expected SetCurrentPatch(salamander), got %q", f.st.currentPatch)
	}
	if f.audio.cutoffCalls != 0 {
		t.Error("cutoff must not be initialized for a non-native patch")
	}
	if f.audio.synthCalls() != 0 {
		t.Error("synth params must not be touched by a non-native select")
	}

	ch := recvChange(t, f.ch)
	if ch.Type != "patch" {
		t.Errorf("expected change type %q, got %q", "patch", ch.Type)
	}
	if ch.Data["name"] != "salamander" || ch.Data["display"] != "Salamander" {
		t.Errorf("unexpected patch change data: %v", ch.Data)
	}
	if ch.Data["volume"] != float32(0.7) {
		t.Errorf("expected Data[volume]=0.7, got %v", ch.Data["volume"])
	}
	if _, present := ch.Data["cutoff_pos"]; present {
		t.Error("cutoff_pos must not appear in a non-native patch change")
	}
	if _, present := ch.Data["cutoff_hz"]; present {
		t.Error("cutoff_hz must not appear in a non-native patch change")
	}
	if _, present := ch.Data["synth"]; present {
		t.Error("synth must not appear in a non-native patch change")
	}
}

func TestSelectPatchNativePublishesCutoff(t *testing.T) {
	f := newFixture(t, sfPatch, nativePatch)
	if err := f.c.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}

	ch := recvChange(t, f.ch)
	if ch.Type != "patch" || ch.Data["name"] != "moog" {
		t.Fatalf("expected patch/moog change, got %q/%v", ch.Type, ch.Data["name"])
	}
	// The select reset the cutoff to the default, so the change must
	// carry the new position or SSE clients keep showing a stale one.
	pos, ok := ch.Data["cutoff_pos"].(float32)
	if !ok || pos != 0.5 {
		t.Errorf("expected cutoff_pos=0.5 in native patch change, got %v", ch.Data["cutoff_pos"])
	}
	hz, ok := ch.Data["cutoff_hz"].(float32)
	if !ok || !approxEq(hz, 632.456) {
		t.Errorf("expected cutoff_hz~632.456 in native patch change, got %v", ch.Data["cutoff_hz"])
	}
	// A native select applies and publishes the full synth block (factory
	// defaults here — the patch has never been tweaked).
	syn, ok := ch.Data["synth"].(map[string]any)
	if !ok {
		t.Fatalf("expected synth block in native patch change, got %T", ch.Data["synth"])
	}
	if syn["resonance"] != float32(0.3) || syn["noise"] != float32(0) || syn["glide"] != float32(0) {
		t.Errorf("unexpected synth scalars in patch change: %v", syn)
	}
	fe, ok := syn["filter_env"].(map[string]any)
	if !ok || fe["decay"] != float32(0.6) {
		t.Errorf("unexpected filter_env in patch change: %v", syn["filter_env"])
	}
	oscs, ok := syn["osc"].([]map[string]any)
	if !ok || len(oscs) != 3 {
		t.Fatalf("expected 3 oscs in patch change, got %v", syn["osc"])
	}
	if oscs[0]["wave"] != "saw" || oscs[0]["level"] != float32(1.0) {
		t.Errorf("unexpected osc 0 in patch change: %v", oscs[0])
	}
}

func TestSelectPatchNativeInitializesCutoff(t *testing.T) {
	f := newFixture(t, sfPatch, nativePatch)

	// Move the cutoff first (via a native selection), then leave and
	// come back — the position must reset to the 0.5 default.
	if err := f.c.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch(moog): %v", err)
	}
	if _, err := f.c.SetCutoffPos(0.9); err != nil {
		t.Fatalf("SetCutoffPos: %v", err)
	}
	if err := f.c.SelectPatch("salamander"); err != nil {
		t.Fatalf("SelectPatch(salamander): %v", err)
	}
	if err := f.c.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch(moog) again: %v", err)
	}

	pos, hz := f.c.CutoffState()
	if pos != 0.5 {
		t.Errorf("expected cutoff pos reset to 0.5, got %v", pos)
	}
	if !approxEq(hz, 632.456) {
		t.Errorf("expected ~632.456 Hz, got %v", hz)
	}
	if !approxEq(f.audio.cutoffHz, 632.456) {
		t.Errorf("expected default cutoff applied to audio, got %v", f.audio.cutoffHz)
	}
}

func TestSelectPatchErrorPropagatesWithoutSideEffects(t *testing.T) {
	f := newFixture(t, sfPatch)

	if err := f.c.SelectPatch("ghost"); err == nil {
		t.Error("expected error for unknown patch")
	}
	if err := f.c.SelectPatchIndex(99); err == nil {
		t.Error("expected error for out-of-range index")
	}

	f.reg.selectErr = errors.New("boom")
	if err := f.c.SelectPatch("salamander"); err == nil || !errors.Is(err, f.reg.selectErr) {
		t.Errorf("expected registry error to propagate, got %v", err)
	}

	if f.st.setCurrCalls != 0 {
		t.Error("SetCurrentPatch must not be called on failed selection")
	}
	if f.audio.volumeCalls != 0 {
		t.Error("audio must not be touched on failed selection")
	}
	assertNoChange(t, f.ch)
}

func TestSelectPatchIndex(t *testing.T) {
	f := newFixture(t, sfPatch, nativePatch)
	if err := f.c.SelectPatchIndex(1); err != nil {
		t.Fatalf("SelectPatchIndex(1): %v", err)
	}
	if f.st.currentPatch != "moog" {
		t.Errorf("expected current patch moog, got %q", f.st.currentPatch)
	}
	ch := recvChange(t, f.ch)
	if ch.Type != "patch" || ch.Data["name"] != "moog" {
		t.Errorf("expected patch/moog change, got %q/%v", ch.Type, ch.Data["name"])
	}
}

func TestSetMastering(t *testing.T) {
	f := newFixture(t)
	comp := float32(0.6)
	ceiling := float32(-1.0)

	// Both set.
	gotComp, gotCeiling := f.c.SetMastering(&comp, &ceiling)
	if gotComp != 0.6 || gotCeiling != -1.0 {
		t.Errorf("expected (0.6, -1.0), got (%v, %v)", gotComp, gotCeiling)
	}
	if f.audio.masteringComp != 0.6 || f.audio.limiterCeilingDB != -1.0 {
		t.Errorf("audio: expected (0.6, -1.0), got (%v, %v)", f.audio.masteringComp, f.audio.limiterCeilingDB)
	}
	ch := recvChange(t, f.ch)
	if ch.Type != "mastering" {
		t.Errorf("expected mastering change, got %q", ch.Type)
	}
	if ch.Data["comp_amount"] != float32(0.6) || ch.Data["limiter_ceiling_db"] != float32(-1.0) {
		t.Errorf("unexpected mastering data: %v", ch.Data)
	}

	// nil comp leaves it unchanged; only the ceiling key is published.
	ceiling2 := float32(-0.3)
	gotComp, gotCeiling = f.c.SetMastering(nil, &ceiling2)
	if gotComp != 0.6 || gotCeiling != -0.3 {
		t.Errorf("expected (0.6, -0.3), got (%v, %v)", gotComp, gotCeiling)
	}
	if f.audio.masteringCalls != 1 {
		t.Errorf("expected mastering comp untouched (1 call), got %d", f.audio.masteringCalls)
	}
	if f.audio.limiterCalls != 2 {
		t.Errorf("expected 2 limiter calls, got %d", f.audio.limiterCalls)
	}
	ch = recvChange(t, f.ch)
	if _, present := ch.Data["comp_amount"]; present {
		t.Error("comp_amount must not appear in a ceiling-only change")
	}
	if ch.Data["limiter_ceiling_db"] != float32(-0.3) {
		t.Errorf("expected limiter_ceiling_db=-0.3, got %v", ch.Data["limiter_ceiling_db"])
	}

	// Both nil: no applies, no publish, returns cache.
	gotComp, gotCeiling = f.c.SetMastering(nil, nil)
	if gotComp != 0.6 || gotCeiling != -0.3 {
		t.Errorf("expected cached (0.6, -0.3), got (%v, %v)", gotComp, gotCeiling)
	}
	assertNoChange(t, f.ch)

	// Mastering() mirrors the cache.
	if c2, l2 := f.c.Mastering(); c2 != 0.6 || l2 != -0.3 {
		t.Errorf("Mastering(): expected (0.6, -0.3), got (%v, %v)", c2, l2)
	}
}

func TestSetMasteringClampsToEngineRanges(t *testing.T) {
	f := newFixture(t)

	// Above range: comp clamps to 1, ceiling to 0 dB. The cache, the
	// engine, the return values, and the published change must all agree
	// on the CLAMPED values — the engine clamps in Rust, so caching or
	// publishing the raw input would lie to status reads and SSE clients.
	comp := float32(1.5)
	ceiling := float32(3)
	gotComp, gotCeiling := f.c.SetMastering(&comp, &ceiling)
	if gotComp != 1 || gotCeiling != 0 {
		t.Errorf("returned: expected (1, 0), got (%v, %v)", gotComp, gotCeiling)
	}
	if f.audio.masteringComp != 1 || f.audio.limiterCeilingDB != 0 {
		t.Errorf("audio: expected (1, 0), got (%v, %v)", f.audio.masteringComp, f.audio.limiterCeilingDB)
	}
	if c2, l2 := f.c.Mastering(); c2 != 1 || l2 != 0 {
		t.Errorf("cache: expected (1, 0), got (%v, %v)", c2, l2)
	}
	ch := recvChange(t, f.ch)
	if ch.Data["comp_amount"] != float32(1) || ch.Data["limiter_ceiling_db"] != float32(0) {
		t.Errorf("published: expected clamped (1, 0), got %v", ch.Data)
	}

	// Below range: comp clamps to 0, ceiling to -12 dB.
	comp = -0.5
	ceiling = -30
	gotComp, gotCeiling = f.c.SetMastering(&comp, &ceiling)
	if gotComp != 0 || gotCeiling != -12 {
		t.Errorf("returned: expected (0, -12), got (%v, %v)", gotComp, gotCeiling)
	}
	if f.audio.masteringComp != 0 || f.audio.limiterCeilingDB != -12 {
		t.Errorf("audio: expected (0, -12), got (%v, %v)", f.audio.masteringComp, f.audio.limiterCeilingDB)
	}
	ch = recvChange(t, f.ch)
	if ch.Data["comp_amount"] != float32(0) || ch.Data["limiter_ceiling_db"] != float32(-12) {
		t.Errorf("published: expected clamped (0, -12), got %v", ch.Data)
	}
}

func TestInitMasteringClamps(t *testing.T) {
	f := newFixture(t)
	f.c.InitMastering(2, 5)
	if comp, ceiling := f.c.Mastering(); comp != 1 || ceiling != 0 {
		t.Errorf("cache: expected (1, 0), got (%v, %v)", comp, ceiling)
	}
	if f.audio.masteringComp != 1 || f.audio.limiterCeilingDB != 0 {
		t.Errorf("audio: expected (1, 0), got (%v, %v)", f.audio.masteringComp, f.audio.limiterCeilingDB)
	}
	assertNoChange(t, f.ch)
}

func TestInitMasteringAppliesWithoutPublish(t *testing.T) {
	f := newFixture(t)
	f.c.InitMastering(0.5, -0.3)

	if f.audio.masteringComp != 0.5 || f.audio.limiterCeilingDB != -0.3 {
		t.Errorf("audio: expected (0.5, -0.3), got (%v, %v)", f.audio.masteringComp, f.audio.limiterCeilingDB)
	}
	if comp, ceiling := f.c.Mastering(); comp != 0.5 || ceiling != -0.3 {
		t.Errorf("cache: expected (0.5, -0.3), got (%v, %v)", comp, ceiling)
	}
	assertNoChange(t, f.ch)
}

func TestVelocityIdentityWhenUnset(t *testing.T) {
	f := newFixture(t)
	for _, v := range []uint8{0, 1, 64, 127} {
		if got := f.c.ApplyVelocity(v); got != v {
			t.Errorf("ApplyVelocity(%d): expected identity, got %d", v, got)
		}
	}
	if label := f.c.VelocityLabel(); label != "" {
		t.Errorf("expected empty label when unset, got %q", label)
	}
}

func TestSetVelocityRemap(t *testing.T) {
	f := newFixture(t)
	f.c.SetVelocityRemap(func(v uint8) uint8 { return v / 2 }, "half")

	if got := f.c.ApplyVelocity(100); got != 50 {
		t.Errorf("expected remapped 50, got %d", got)
	}
	if label := f.c.VelocityLabel(); label != "half" {
		t.Errorf("expected label %q, got %q", "half", label)
	}
	ch := recvChange(t, f.ch)
	if ch.Type != "velocity" || ch.Data["curve"] != "half" {
		t.Errorf("expected velocity/half change, got %q/%v", ch.Type, ch.Data["curve"])
	}

	// nil fn restores identity behavior but keeps the label.
	f.c.SetVelocityRemap(nil, "linear")
	if got := f.c.ApplyVelocity(100); got != 100 {
		t.Errorf("expected identity with nil fn, got %d", got)
	}
	if label := f.c.VelocityLabel(); label != "linear" {
		t.Errorf("expected label %q, got %q", "linear", label)
	}
}

func TestVelocityConcurrentApplyAndSwap(t *testing.T) {
	f := newFixture(t)
	var wg sync.WaitGroup

	// Curve swapper (web request) races against per-NoteOn applies (MIDI
	// goroutine). Run under -race; every observed value must come from
	// either the identity or one of the installed curves.
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 500; i++ {
			shift := uint8(i % 4)
			f.c.SetVelocityRemap(func(v uint8) uint8 { return v >> shift }, fmt.Sprintf("curve-%d", shift))
		}
	}()
	for r := 0; r < 4; r++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 2000; i++ {
				got := f.c.ApplyVelocity(96)
				switch got {
				case 96, 48, 24, 12:
				default:
					t.Errorf("ApplyVelocity(96) returned %d — torn read?", got)
					return
				}
				_ = f.c.VelocityLabel()
			}
		}()
	}
	wg.Wait()
}

func TestSnapshot(t *testing.T) {
	f := newFixture(t, nativePatch)
	f.st.knobs["moog"] = state.Knob{Volume: 0.9, Reverb: 0.1, Compressor: 0.3}

	if err := f.c.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	if _, err := f.c.SetCutoffPos(1.0); err != nil {
		t.Fatalf("SetCutoffPos: %v", err)
	}
	f.c.InitMastering(0.4, -0.6)
	f.c.SetVelocityRemap(func(v uint8) uint8 { return v }, "soft-2")

	s := f.c.Snapshot()
	if s.Patch != "moog" || s.PatchDisplay != "Moog" {
		t.Errorf("patch: expected moog/Moog, got %q/%q", s.Patch, s.PatchDisplay)
	}
	if s.Volume != 0.9 || s.Reverb != 0.1 || s.Compressor != 0.3 {
		t.Errorf("knobs: expected (0.9, 0.1, 0.3), got (%v, %v, %v)", s.Volume, s.Reverb, s.Compressor)
	}
	if s.CutoffPos != 1.0 || !approxEq(s.CutoffHz, 20000) {
		t.Errorf("cutoff: expected (1.0, 20000), got (%v, %v)", s.CutoffPos, s.CutoffHz)
	}
	if s.MasteringComp != 0.4 || s.LimiterCeilingDB != -0.6 {
		t.Errorf("mastering: expected (0.4, -0.6), got (%v, %v)", s.MasteringComp, s.LimiterCeilingDB)
	}
	if s.VelocityCurve != "soft-2" {
		t.Errorf("velocity: expected soft-2, got %q", s.VelocityCurve)
	}
}

func TestSnapshotNoPatch(t *testing.T) {
	f := newFixture(t, sfPatch) // nothing selected
	s := f.c.Snapshot()
	if s.Patch != "" || s.PatchDisplay != "" {
		t.Errorf("expected empty patch fields, got %q/%q", s.Patch, s.PatchDisplay)
	}
	if s.Volume != 0 || s.Reverb != 0 || s.Compressor != 0 {
		t.Errorf("expected zero knobs, got (%v, %v, %v)", s.Volume, s.Reverb, s.Compressor)
	}
	if s.CutoffPos != 0.5 {
		t.Errorf("expected default cutoff pos 0.5, got %v", s.CutoffPos)
	}
}

// ---- native synth params ---------------------------------------------------

// defaultSynthWant is the expected boot cache: audio-core's defaults.
var defaultSynthWant = SynthSnapshot{
	Resonance: 0.3,
	FilterEnv: FilterEnv{Attack: 0.005, Decay: 0.6, Sustain: 0.4, Release: 0.6, Amount: 0},
	Oscs: [3]OscParams{
		{Wave: "saw", Octave: 0, DetuneCents: 0, Level: 1.0},
		{Wave: "saw", Octave: 0, DetuneCents: -7, Level: 0.0},
		{Wave: "saw", Octave: -1, DetuneCents: 5, Level: 0.0},
	},
	Noise: 0,
	Glide: 0,
}

func TestSynthDefaults(t *testing.T) {
	f := newFixture(t)
	if got := f.c.Synth(); got != defaultSynthWant {
		t.Errorf("Synth() defaults:\nwant %+v\ngot  %+v", defaultSynthWant, got)
	}
	if s := f.c.Snapshot(); s.Synth != defaultSynthWant {
		t.Errorf("Snapshot().Synth defaults:\nwant %+v\ngot  %+v", defaultSynthWant, s.Synth)
	}
	if f.audio.synthCalls() != 0 {
		t.Error("defaults must be cache-only — no audio applies at construction")
	}
}

// synthSetters enumerates every SetSynth* under a uniform signature for
// the gating test.
func synthSetters(c *Controls) map[string]func() error {
	return map[string]func() error{
		"SetSynthResonance": func() error { _, err := c.SetSynthResonance(0.5); return err },
		"SetSynthFilterEnv": func() error { _, err := c.SetSynthFilterEnv(0.01, 0.5, 0.5, 0.5, 0.3); return err },
		"SetSynthOsc":       func() error { _, err := c.SetSynthOsc(0, "saw", 0, 0, 1); return err },
		"SetSynthNoise":     func() error { _, err := c.SetSynthNoise(0.2); return err },
		"SetSynthGlide":     func() error { _, err := c.SetSynthGlide(0.1); return err },
	}
}

func TestSynthSettersGatedOnPatchType(t *testing.T) {
	t.Run("no patch", func(t *testing.T) {
		f := newFixture(t, nativePatch) // nothing selected
		for name, set := range synthSetters(f.c) {
			if err := set(); !errors.Is(err, ErrNoNativePatch) {
				t.Errorf("%s: expected ErrNoNativePatch with no current patch, got %v", name, err)
			}
		}
		if f.audio.synthCalls() != 0 {
			t.Error("audio must not be touched when gated")
		}
		assertNoChange(t, f.ch)
	})
	t.Run("non-native patch", func(t *testing.T) {
		f := newFixture(t, sfPatch)
		if err := f.reg.Select("salamander"); err != nil {
			t.Fatalf("select: %v", err)
		}
		for name, set := range synthSetters(f.c) {
			if err := set(); !errors.Is(err, ErrNoNativePatch) {
				t.Errorf("%s: expected ErrNoNativePatch for soundfont patch, got %v", name, err)
			}
		}
		if f.audio.synthCalls() != 0 {
			t.Error("audio must not be touched when gated")
		}
		if got := f.c.Synth(); got != defaultSynthWant {
			t.Errorf("cache must not change when gated, got %+v", got)
		}
		assertNoChange(t, f.ch)
	})
}

// selectNative is the shared setup for the synth setter tests.
func selectNative(t *testing.T, f *fixture) {
	t.Helper()
	if err := f.reg.Select("moog"); err != nil {
		t.Fatalf("select: %v", err)
	}
}

func TestSetSynthResonance(t *testing.T) {
	f := newFixture(t, nativePatch)
	selectNative(t, f)

	got, err := f.c.SetSynthResonance(0.8)
	if err != nil {
		t.Fatalf("SetSynthResonance: %v", err)
	}
	if got != 0.8 || f.audio.resonance != 0.8 {
		t.Errorf("expected 0.8 returned and applied, got %v / %v", got, f.audio.resonance)
	}
	ch := recvChange(t, f.ch)
	if ch.Type != "synth" || ch.Data["field"] != "resonance" {
		t.Errorf("expected synth/resonance change, got %q/%v", ch.Type, ch.Data["field"])
	}
	if ch.Data["resonance"] != float32(0.8) || ch.Data["patch"] != "moog" {
		t.Errorf("unexpected change data: %v", ch.Data)
	}

	// Clamps both ways.
	if got, _ := f.c.SetSynthResonance(1.7); got != 0.95 {
		t.Errorf("expected clamp to 0.95, got %v", got)
	}
	if got, _ := f.c.SetSynthResonance(-0.2); got != 0 {
		t.Errorf("expected clamp to 0, got %v", got)
	}
	if f.c.Synth().Resonance != 0 {
		t.Errorf("cache: expected 0, got %v", f.c.Synth().Resonance)
	}
}

func TestSetSynthFilterEnv(t *testing.T) {
	f := newFixture(t, nativePatch)
	selectNative(t, f)

	fe, err := f.c.SetSynthFilterEnv(0.01, 0.8, 0.5, 1.2, 0.3)
	if err != nil {
		t.Fatalf("SetSynthFilterEnv: %v", err)
	}
	want := FilterEnv{Attack: 0.01, Decay: 0.8, Sustain: 0.5, Release: 1.2, Amount: 0.3}
	if fe != want {
		t.Errorf("returned env: want %+v, got %+v", want, fe)
	}
	if f.audio.feA != 0.01 || f.audio.feD != 0.8 || f.audio.feS != 0.5 || f.audio.feR != 1.2 || f.audio.feAmt != 0.3 {
		t.Errorf("audio env: unexpected (%v %v %v %v %v)", f.audio.feA, f.audio.feD, f.audio.feS, f.audio.feR, f.audio.feAmt)
	}
	ch := recvChange(t, f.ch)
	if ch.Type != "synth" || ch.Data["field"] != "filter_env" {
		t.Errorf("expected synth/filter_env change, got %q/%v", ch.Type, ch.Data["field"])
	}
	feData, ok := ch.Data["filter_env"].(map[string]any)
	if !ok {
		t.Fatalf("expected nested filter_env map, got %T", ch.Data["filter_env"])
	}
	if feData["attack"] != float32(0.01) || feData["amount"] != float32(0.3) {
		t.Errorf("unexpected filter_env data: %v", feData)
	}

	// Clamps: times to [0.0001, 10], sustain/amount to [0, 1].
	fe, err = f.c.SetSynthFilterEnv(0, 99, 1.5, -3, -0.5)
	if err != nil {
		t.Fatalf("SetSynthFilterEnv (clamping): %v", err)
	}
	want = FilterEnv{Attack: 0.0001, Decay: 10, Sustain: 1, Release: 0.0001, Amount: 0}
	if fe != want {
		t.Errorf("clamped env: want %+v, got %+v", want, fe)
	}
	if f.c.Synth().FilterEnv != want {
		t.Errorf("cache env: want %+v, got %+v", want, f.c.Synth().FilterEnv)
	}
}

func TestSetSynthOsc(t *testing.T) {
	f := newFixture(t, nativePatch)
	selectNative(t, f)

	op, err := f.c.SetSynthOsc(1, "square", 1, -7, 0.6)
	if err != nil {
		t.Fatalf("SetSynthOsc: %v", err)
	}
	want := OscParams{Wave: "square", Octave: 1, DetuneCents: -7, Level: 0.6}
	if op != want {
		t.Errorf("returned osc: want %+v, got %+v", want, op)
	}
	if f.audio.lastOsc != (oscCall{idx: 1, wave: "square", octave: 1, detune: -7, level: 0.6}) {
		t.Errorf("audio osc: unexpected %+v", f.audio.lastOsc)
	}
	ch := recvChange(t, f.ch)
	if ch.Type != "synth" || ch.Data["field"] != "osc" {
		t.Errorf("expected synth/osc change, got %q/%v", ch.Type, ch.Data["field"])
	}
	if ch.Data["index"] != 1 || ch.Data["wave"] != "square" || ch.Data["octave"] != 1 ||
		ch.Data["detune_cents"] != float32(-7) || ch.Data["level"] != float32(0.6) {
		t.Errorf("unexpected osc change data: %v", ch.Data)
	}
	if f.c.Synth().Oscs[1] != want {
		t.Errorf("cache osc 1: want %+v, got %+v", want, f.c.Synth().Oscs[1])
	}
	// Untouched oscillators keep their defaults.
	if f.c.Synth().Oscs[0] != defaultSynthWant.Oscs[0] || f.c.Synth().Oscs[2] != defaultSynthWant.Oscs[2] {
		t.Errorf("oscs 0/2 must keep defaults, got %+v", f.c.Synth().Oscs)
	}

	// Numeric ranges clamp.
	op, err = f.c.SetSynthOsc(2, "pulse", 5, -500, 3)
	if err != nil {
		t.Fatalf("SetSynthOsc (clamping): %v", err)
	}
	want = OscParams{Wave: "pulse", Octave: 2, DetuneCents: -100, Level: 1}
	if op != want {
		t.Errorf("clamped osc: want %+v, got %+v", want, op)
	}
	recvChange(t, f.ch) // drain

	// Validation errors: bad idx, bad wave. No apply, no publish, no cache change.
	before := f.c.Synth()
	calls := f.audio.synthCalls()
	if _, err := f.c.SetSynthOsc(3, "saw", 0, 0, 1); err == nil {
		t.Error("expected error for idx 3")
	}
	if _, err := f.c.SetSynthOsc(-1, "saw", 0, 0, 1); err == nil {
		t.Error("expected error for idx -1")
	}
	if _, err := f.c.SetSynthOsc(0, "sine", 0, 0, 1); err == nil {
		t.Error("expected error for unknown wave")
	}
	if f.audio.synthCalls() != calls {
		t.Error("audio must not be touched on validation errors")
	}
	if f.c.Synth() != before {
		t.Error("cache must not change on validation errors")
	}
	assertNoChange(t, f.ch)

	// An engine-side failure propagates and leaves the cache alone.
	f.audio.oscErr = errors.New("boom")
	if _, err := f.c.SetSynthOsc(0, "saw", 0, 0, 1); err == nil {
		t.Error("expected engine error to propagate")
	}
	if f.c.Synth() != before {
		t.Error("cache must not change on engine error")
	}
	assertNoChange(t, f.ch)
}

func TestSetSynthNoiseAndGlide(t *testing.T) {
	f := newFixture(t, nativePatch)
	selectNative(t, f)

	if got, err := f.c.SetSynthNoise(0.4); err != nil || got != 0.4 || f.audio.noise != 0.4 {
		t.Errorf("noise: expected 0.4 applied, got %v (err=%v, audio=%v)", got, err, f.audio.noise)
	}
	ch := recvChange(t, f.ch)
	if ch.Type != "synth" || ch.Data["field"] != "noise" || ch.Data["noise"] != float32(0.4) {
		t.Errorf("unexpected noise change: %q/%v", ch.Type, ch.Data)
	}
	if got, _ := f.c.SetSynthNoise(1.5); got != 1 {
		t.Errorf("noise clamp high: expected 1, got %v", got)
	}
	if got, _ := f.c.SetSynthNoise(-1); got != 0 {
		t.Errorf("noise clamp low: expected 0, got %v", got)
	}

	if got, err := f.c.SetSynthGlide(0.25); err != nil || got != 0.25 || f.audio.glide != 0.25 {
		t.Errorf("glide: expected 0.25 applied, got %v (err=%v, audio=%v)", got, err, f.audio.glide)
	}
	for range 3 {
		recvChange(t, f.ch) // drain the two noise clamps + first glide
	}
	if got, _ := f.c.SetSynthGlide(9); got != 5 {
		t.Errorf("glide clamp high: expected 5, got %v", got)
	}
	if got, _ := f.c.SetSynthGlide(-1); got != 0 {
		t.Errorf("glide clamp low: expected 0, got %v", got)
	}
	if s := f.c.Synth(); s.Noise != 0 || s.Glide != 0 {
		t.Errorf("cache: expected noise 0 glide 0, got %v/%v", s.Noise, s.Glide)
	}
}

// TestSynthPerPatchPersistence pins the ROADMAP §3 contract end to end:
// tweak patch A, select fresh patch B (factory defaults hit the engine),
// re-select A (A's tweaks come back), with every step flowing through
// the state store.
func TestSynthPerPatchPersistence(t *testing.T) {
	f := newFixture(t, sfPatch, nativePatch, nativePatch2)
	if err := f.c.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch(moog): %v", err)
	}
	if _, err := f.c.SetSynthResonance(0.7); err != nil {
		t.Fatalf("SetSynthResonance: %v", err)
	}
	if _, err := f.c.SetSynthOsc(2, "pulse", -2, 12, 0.9); err != nil {
		t.Fatalf("SetSynthOsc: %v", err)
	}
	if _, err := f.c.SetSynthGlide(0.5); err != nil {
		t.Fatalf("SetSynthGlide: %v", err)
	}
	tweaked := f.c.Synth()

	// The tweaks were persisted for moog as they happened.
	stored, ok := f.st.PatchSynth("moog")
	if !ok {
		t.Fatal("moog synth block missing from state store after tweaks")
	}
	if got := synthFromState(stored); got != tweaked {
		t.Errorf("stored block: want %+v, got %+v", tweaked, got)
	}

	// A soundfont detour must not touch the engine's synth params.
	calls := f.audio.synthCalls()
	if err := f.c.SelectPatch("salamander"); err != nil {
		t.Fatalf("SelectPatch(salamander): %v", err)
	}
	if f.audio.synthCalls() != calls {
		t.Error("non-native select must leave engine synth params untouched")
	}

	// A fresh native patch gets FACTORY DEFAULTS applied to the engine —
	// not moog's leftovers.
	if err := f.c.SelectPatch("moog2"); err != nil {
		t.Fatalf("SelectPatch(moog2): %v", err)
	}
	if f.c.Synth() != defaultSynthWant {
		t.Errorf("moog2 cache: want factory defaults, got %+v", f.c.Synth())
	}
	if f.audio.resonance != 0.3 || f.audio.glide != 0 || f.audio.noise != 0 {
		t.Errorf("moog2 engine: want factory (0.3, 0, 0), got (%v, %v, %v)",
			f.audio.resonance, f.audio.glide, f.audio.noise)
	}
	if f.audio.feD != 0.6 {
		t.Errorf("moog2 engine filter decay: want factory 0.6, got %v", f.audio.feD)
	}
	// applySynthAll pushes oscs 0..2 in order, so the last osc call is
	// factory osc 2.
	if want := (oscCall{idx: 2, wave: "saw", octave: -1, detune: 5, level: 0}); f.audio.lastOsc != want {
		t.Errorf("moog2 engine osc 2: want factory %+v, got %+v", want, f.audio.lastOsc)
	}
	// Restoring defaults is not an edit: moog2 reaches disk only on its
	// first tweak.
	if _, ok := f.st.PatchSynth("moog2"); ok {
		t.Error("fresh native select must not persist a synth block")
	}

	// Re-selecting moog restores its tweaks to engine and cache.
	if err := f.c.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch(moog) again: %v", err)
	}
	if f.c.Synth() != tweaked {
		t.Errorf("moog cache after reselect: want %+v, got %+v", tweaked, f.c.Synth())
	}
	if f.audio.resonance != 0.7 || f.audio.glide != 0.5 {
		t.Errorf("moog engine after reselect: want (0.7, 0.5), got (%v, %v)",
			f.audio.resonance, f.audio.glide)
	}
	if want := (oscCall{idx: 2, wave: "pulse", octave: -2, detune: 12, level: 0.9}); f.audio.lastOsc != want {
		t.Errorf("moog engine osc 2 after reselect: want %+v, got %+v", want, f.audio.lastOsc)
	}
	if tweaked.FilterEnv != defaultSynthWant.FilterEnv {
		t.Errorf("filter env: expected untouched defaults, got %+v", tweaked.FilterEnv)
	}
}

// TestSynthMutationsPersist asserts EVERY mutating synth entry point
// writes the resulting whole-block snapshot to the state store.
func TestSynthMutationsPersist(t *testing.T) {
	mutations := map[string]func(c *Controls) error{
		"SetSynthResonance": func(c *Controls) error { _, err := c.SetSynthResonance(0.5); return err },
		"SetSynthFilterEnv": func(c *Controls) error { _, err := c.SetSynthFilterEnv(0.01, 0.5, 0.5, 0.5, 0.3); return err },
		"SetSynthOsc":       func(c *Controls) error { _, err := c.SetSynthOsc(1, "square", 1, -7, 0.6); return err },
		"SetSynthNoise":     func(c *Controls) error { _, err := c.SetSynthNoise(0.2); return err },
		"SetSynthGlide":     func(c *Controls) error { _, err := c.SetSynthGlide(0.1); return err },
		"MergeSynth": func(c *Controls) error {
			_, err := c.MergeSynth(SynthPartial{Noise: fp(0.4), Oscs: []OscPartial{{Index: 0, Level: fp(0.3)}}})
			return err
		},
	}
	for name, mutate := range mutations {
		t.Run(name, func(t *testing.T) {
			f := newFixture(t, nativePatch)
			selectNative(t, f)
			if err := mutate(f.c); err != nil {
				t.Fatalf("%s: %v", name, err)
			}
			stored, ok := f.st.PatchSynth("moog")
			if !ok {
				t.Fatal("mutation did not persist a synth block")
			}
			if want := synthToState(f.c.Synth()); stored != want {
				t.Errorf("persisted block diverges from cache:\nwant %+v\ngot  %+v", want, stored)
			}
		})
	}
}

// TestSelectPatchSanitizesStoredSynth pins the trust boundary: state.toml
// is hand-editable, so a restored block re-clamps numerics and falls back
// to the factory wave for an unknown osc wave before touching the engine.
func TestSelectPatchSanitizesStoredSynth(t *testing.T) {
	f := newFixture(t, nativePatch)
	f.st.synths["moog"] = state.SynthState{
		Resonance: 2.0, // > maxResonance
		FilterEnv: state.FilterEnvState{Attack: -1, Decay: 99, Sustain: 2, Release: 0, Amount: -3},
		Oscs: [3]state.OscState{
			{Wave: "sine", Octave: 9, DetuneCents: 999, Level: 7}, // invalid wave + out of range
			{Wave: "square", Octave: -2, DetuneCents: -7, Level: 0.5},
			{Wave: "pulse", Octave: 1, DetuneCents: 3, Level: 0.25},
		},
		Noise: -0.5,
		Glide: 99,
	}

	if err := f.c.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}

	s := f.c.Synth()
	if s.Resonance != 0.95 {
		t.Errorf("resonance: want clamp to 0.95, got %v", s.Resonance)
	}
	want := FilterEnv{Attack: 0.0001, Decay: 10, Sustain: 1, Release: 0.0001, Amount: 0}
	if s.FilterEnv != want {
		t.Errorf("filter env: want %+v, got %+v", want, s.FilterEnv)
	}
	if wantOsc := (OscParams{Wave: "saw", Octave: 2, DetuneCents: 100, Level: 1}); s.Oscs[0] != wantOsc {
		t.Errorf("osc 0: want sanitized %+v, got %+v", wantOsc, s.Oscs[0])
	}
	if s.Noise != 0 || s.Glide != 5 {
		t.Errorf("noise/glide: want (0, 5), got (%v, %v)", s.Noise, s.Glide)
	}
	if f.audio.resonance != 0.95 || f.audio.glide != 5 {
		t.Errorf("engine: want clamped (0.95, 5), got (%v, %v)", f.audio.resonance, f.audio.glide)
	}
}

// TestSelectPatchSynthEngineRejection: if the engine still rejects a
// sanitized oscillator during restore, that osc keeps its previous cached
// value so cache and engine agree; the rest of the block applies.
func TestSelectPatchSynthEngineRejection(t *testing.T) {
	f := newFixture(t, nativePatch)
	f.st.synths["moog"] = synthToState(SynthSnapshot{
		Resonance: 0.6,
		FilterEnv: defaultSynthWant.FilterEnv,
		Oscs: [3]OscParams{
			{Wave: "square", Octave: 1, DetuneCents: 2, Level: 0.9},
			defaultSynthWant.Oscs[1],
			defaultSynthWant.Oscs[2],
		},
	})
	f.audio.oscErr = errors.New("boom")

	if err := f.c.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	s := f.c.Synth()
	if s.Resonance != 0.6 || f.audio.resonance != 0.6 {
		t.Errorf("resonance: want 0.6 applied despite osc rejection, got %v/%v", s.Resonance, f.audio.resonance)
	}
	// All three oscs were rejected, so the cache keeps the pre-select
	// (factory) oscs rather than lying about what the engine holds.
	if s.Oscs != defaultSynthWant.Oscs {
		t.Errorf("oscs: want previous cached values on rejection, got %+v", s.Oscs)
	}
}

// ---- MergeSynth -------------------------------------------------------------

func fp(v float32) *float32 { return &v }
func sp(s string) *string   { return &s }
func ip(i int) *int         { return &i }

func TestMergeSynthPartialFields(t *testing.T) {
	f := newFixture(t, nativePatch)
	selectNative(t, f)

	// filter_env: attack only — every other env field keeps its default.
	syn, err := f.c.MergeSynth(SynthPartial{FilterEnv: &FilterEnvPartial{Attack: fp(0.05)}})
	if err != nil {
		t.Fatalf("MergeSynth(filter_env.attack): %v", err)
	}
	wantFE := defaultSynthWant.FilterEnv
	wantFE.Attack = 0.05
	if syn.FilterEnv != wantFE {
		t.Errorf("filter env: want %+v, got %+v", wantFE, syn.FilterEnv)
	}
	if f.audio.feA != 0.05 || f.audio.feD != 0.6 {
		t.Errorf("audio env: expected attack 0.05 with default decay 0.6, got %v/%v", f.audio.feA, f.audio.feD)
	}
	ch := recvChange(t, f.ch)
	if ch.Type != "synth" || ch.Data["field"] != "filter_env" {
		t.Errorf("expected synth/filter_env change, got %q/%v", ch.Type, ch.Data["field"])
	}

	// osc: level only on osc 1 — wave/octave/detune keep their values.
	syn, err = f.c.MergeSynth(SynthPartial{Oscs: []OscPartial{{Index: 1, Level: fp(0.6)}}})
	if err != nil {
		t.Fatalf("MergeSynth(osc.level): %v", err)
	}
	wantOsc := defaultSynthWant.Oscs[1]
	wantOsc.Level = 0.6
	if syn.Oscs[1] != wantOsc {
		t.Errorf("osc 1: want %+v, got %+v", wantOsc, syn.Oscs[1])
	}
	if f.audio.lastOsc != (oscCall{idx: 1, wave: "saw", octave: 0, detune: -7, level: 0.6}) {
		t.Errorf("audio osc: unexpected %+v", f.audio.lastOsc)
	}
	ch = recvChange(t, f.ch)
	if ch.Type != "synth" || ch.Data["field"] != "osc" {
		t.Errorf("expected synth/osc change, got %q/%v", ch.Type, ch.Data["field"])
	}

	// Multi-section body with clamping: each section publishes its own
	// change, exactly like the individual setters.
	syn, err = f.c.MergeSynth(SynthPartial{
		Resonance: fp(2.0), // clamps to 0.95
		Noise:     fp(0.1),
		Glide:     fp(0.2),
		Oscs:      []OscPartial{{Index: 0, Wave: sp("square")}, {Index: 2, Octave: ip(-2), Level: fp(0.4)}},
	})
	if err != nil {
		t.Fatalf("MergeSynth(multi): %v", err)
	}
	if syn.Resonance != 0.95 || syn.Noise != 0.1 || syn.Glide != 0.2 {
		t.Errorf("scalars: want (0.95, 0.1, 0.2), got (%v, %v, %v)", syn.Resonance, syn.Noise, syn.Glide)
	}
	if syn.Oscs[0].Wave != "square" || syn.Oscs[0].Level != 1.0 {
		t.Errorf("osc 0: expected square with preserved level 1.0, got %+v", syn.Oscs[0])
	}
	if syn.Oscs[2].Octave != -2 || syn.Oscs[2].Level != 0.4 || syn.Oscs[2].DetuneCents != 5 {
		t.Errorf("osc 2: expected -2oct/0.4 with preserved +5c, got %+v", syn.Oscs[2])
	}
	if f.c.Synth() != syn {
		t.Errorf("returned snapshot must match the cache: %+v vs %+v", syn, f.c.Synth())
	}
	for i := 0; i < 5; i++ { // resonance, noise, glide, osc0, osc2
		if ch := recvChange(t, f.ch); ch.Type != "synth" {
			t.Errorf("change %d: expected type synth, got %q", i, ch.Type)
		}
	}
	assertNoChange(t, f.ch)
}

func TestMergeSynthGatedOnPatchType(t *testing.T) {
	t.Run("no patch", func(t *testing.T) {
		f := newFixture(t, nativePatch) // nothing selected
		if _, err := f.c.MergeSynth(SynthPartial{Resonance: fp(0.5)}); !errors.Is(err, ErrNoNativePatch) {
			t.Errorf("expected ErrNoNativePatch, got %v", err)
		}
		if f.audio.synthCalls() != 0 {
			t.Error("audio must not be touched when gated")
		}
		assertNoChange(t, f.ch)
	})
	t.Run("non-native patch", func(t *testing.T) {
		f := newFixture(t, sfPatch)
		if err := f.reg.Select("salamander"); err != nil {
			t.Fatalf("select: %v", err)
		}
		if _, err := f.c.MergeSynth(SynthPartial{Noise: fp(0.5)}); !errors.Is(err, ErrNoNativePatch) {
			t.Errorf("expected ErrNoNativePatch, got %v", err)
		}
		if got := f.c.Synth(); got != defaultSynthWant {
			t.Errorf("cache must not change when gated, got %+v", got)
		}
		assertNoChange(t, f.ch)
	})
}

func TestMergeSynthValidatesOscsBeforeApplyingAnything(t *testing.T) {
	f := newFixture(t, nativePatch)
	selectNative(t, f)

	for name, p := range map[string]SynthPartial{
		"index high":   {Resonance: fp(0.5), Oscs: []OscPartial{{Index: 3, Level: fp(0.5)}}},
		"index low":    {Resonance: fp(0.5), Oscs: []OscPartial{{Index: -1, Level: fp(0.5)}}},
		"unknown wave": {Resonance: fp(0.5), Oscs: []OscPartial{{Index: 0, Wave: sp("sine")}}},
		"bad late entry": {Resonance: fp(0.5), Oscs: []OscPartial{
			{Index: 0, Level: fp(0.5)}, {Index: 0, Wave: sp("sine")},
		}},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := f.c.MergeSynth(p); err == nil {
				t.Fatal("expected a validation error")
			}
			// Osc validation runs up front, so nothing — not even the
			// valid resonance section — may have been applied.
			if f.audio.synthCalls() != 0 {
				t.Error("audio must not be touched on a validation error")
			}
			if got := f.c.Synth(); got != defaultSynthWant {
				t.Errorf("cache must not change on a validation error, got %+v", got)
			}
			assertNoChange(t, f.ch)
		})
	}
}

func TestMergeSynthEngineErrorPropagates(t *testing.T) {
	f := newFixture(t, nativePatch)
	selectNative(t, f)
	f.audio.oscErr = errors.New("boom")

	before := f.c.Synth()
	if _, err := f.c.MergeSynth(SynthPartial{Oscs: []OscPartial{{Index: 0, Level: fp(0.5)}}}); err == nil {
		t.Fatal("expected engine error to propagate")
	}
	if f.c.Synth() != before {
		t.Error("cache must not change on engine error")
	}
	assertNoChange(t, f.ch)
}

// ---- writer-serialization probes (C2/C3) ------------------------------------

// TestConcurrentWritersConverge hammers every mutating family from
// concurrent goroutines and then asserts engine == cache/state ==
// last-published for each param. Run under -race: pre-fix, the
// clamp→audio→persist→publish sequences interleaved, so the three views
// could diverge — and the unsynchronized test fakes double as race-
// detector bait for any path that escapes the writer lock.
func TestConcurrentWritersConverge(t *testing.T) {
	f := newFixture(t, nativePatch)
	if err := f.c.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}

	const (
		perKind = 3
		iters   = 100
	)
	// Buffer sized for every publish so drop-oldest never fires and the
	// drained tail is the true last publish per field.
	ch, cancel := f.hub.Subscribe(4*perKind*iters + 16)
	defer cancel()

	var wg sync.WaitGroup
	start := make(chan struct{})
	writer := func(fn func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iters; i++ {
				fn(i)
			}
		}()
	}
	for g := 0; g < perKind; g++ {
		n := g * iters
		writer(func(i int) {
			if _, err := f.c.SetVolume(float32((n+i)%101) / 100); err != nil {
				t.Errorf("SetVolume: %v", err)
			}
		})
		writer(func(i int) {
			if _, err := f.c.SetSynthResonance(float32((n+i)%96) / 100); err != nil {
				t.Errorf("SetSynthResonance: %v", err)
			}
		})
		writer(func(i int) {
			comp := float32((n+i)%101) / 100
			f.c.SetMastering(&comp, nil)
		})
		writer(func(i int) {
			if _, err := f.c.SetCutoffPos(float32((n+i)%101) / 100); err != nil {
				t.Errorf("SetCutoffPos: %v", err)
			}
		})
	}
	close(start)
	wg.Wait()

	// Drain the full stream, keeping the last published value per field.
	last := map[string]float32{}
	for {
		select {
		case c := <-ch:
			switch c.Type {
			case "params":
				switch c.Data["field"] {
				case "volume":
					last["volume"] = c.Data["value"].(float32)
				case "cutoff":
					last["cutoff_pos"] = c.Data["pos"].(float32)
					last["cutoff_hz"] = c.Data["hz"].(float32)
				}
			case "synth":
				if c.Data["field"] == "resonance" {
					last["resonance"] = c.Data["resonance"].(float32)
				}
			case "mastering":
				last["comp"] = c.Data["comp_amount"].(float32)
			}
			continue
		default:
		}
		break
	}

	if f.audio.volume != last["volume"] || f.st.PatchKnob("moog").Volume != last["volume"] {
		t.Errorf("volume diverged: audio=%v state=%v last-published=%v",
			f.audio.volume, f.st.PatchKnob("moog").Volume, last["volume"])
	}
	if f.audio.resonance != last["resonance"] || f.c.Synth().Resonance != last["resonance"] {
		t.Errorf("resonance diverged: audio=%v cache=%v last-published=%v",
			f.audio.resonance, f.c.Synth().Resonance, last["resonance"])
	}
	comp, _ := f.c.Mastering()
	if f.audio.masteringComp != last["comp"] || comp != last["comp"] {
		t.Errorf("mastering comp diverged: audio=%v cache=%v last-published=%v",
			f.audio.masteringComp, comp, last["comp"])
	}
	pos, hz := f.c.CutoffState()
	if pos != last["cutoff_pos"] || !approxEq(hz, last["cutoff_hz"]) || !approxEq(f.audio.cutoffHz, last["cutoff_hz"]) {
		t.Errorf("cutoff diverged: cache=(%v, %v) audio=%v last-published=(%v, %v)",
			pos, hz, f.audio.cutoffHz, last["cutoff_pos"], last["cutoff_hz"])
	}
}

// TestConcurrentSelectVsWritersConverge is the C3 probe: patch selects
// (web SelectPatch vs pad SelectPatchIndex) racing knob writers must end
// with the registry, state store, engine, and published stream all
// agreeing on the same patch and volume. Run under -race.
func TestConcurrentSelectVsWritersConverge(t *testing.T) {
	f := newFixture(t, sfPatch, nativePatch)
	if err := f.c.SelectPatch("salamander"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}

	const iters = 150
	ch, cancel := f.hub.Subscribe(3*iters + 16)
	defer cancel()

	var wg sync.WaitGroup
	start := make(chan struct{})
	run := func(fn func(i int)) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			for i := 0; i < iters; i++ {
				fn(i)
			}
		}()
	}
	run(func(i int) { // web select path
		name := "salamander"
		if i%2 == 0 {
			name = "moog"
		}
		if err := f.c.SelectPatch(name); err != nil {
			t.Errorf("SelectPatch: %v", err)
		}
	})
	run(func(i int) { // pad select path
		if err := f.c.SelectPatchIndex(i % 2); err != nil {
			t.Errorf("SelectPatchIndex: %v", err)
		}
	})
	run(func(i int) { // knob writer
		if _, err := f.c.SetVolume(float32(i%101) / 100); err != nil {
			t.Errorf("SetVolume: %v", err)
		}
	})
	close(start)
	wg.Wait()

	// Publish order matches apply order (both happen under the writer
	// lock), so the drained tail is the final state each surface saw.
	var lastPatch, lastVolPatch string
	var lastVol float32
	for {
		select {
		case c := <-ch:
			switch c.Type {
			case "patch":
				lastPatch = c.Data["name"].(string)
				lastVol = c.Data["volume"].(float32)
				lastVolPatch = lastPatch
			case "params":
				if c.Data["field"] == "volume" {
					lastVol = c.Data["value"].(float32)
					lastVolPatch = c.Data["patch"].(string)
				}
			}
			continue
		default:
		}
		break
	}

	cur := f.reg.Current()
	if cur == nil {
		t.Fatal("no current patch after selects")
	}
	if cur.Name != lastPatch || f.st.currentPatch != lastPatch {
		t.Errorf("patch diverged: registry=%q state=%q last-published=%q",
			cur.Name, f.st.currentPatch, lastPatch)
	}
	if f.audio.volume != lastVol {
		t.Errorf("volume diverged: audio=%v last-published=%v", f.audio.volume, lastVol)
	}
	if got := f.st.PatchKnob(lastVolPatch).Volume; got != lastVol {
		t.Errorf("state volume diverged for %q: state=%v last-published=%v", lastVolPatch, got, lastVol)
	}
}
