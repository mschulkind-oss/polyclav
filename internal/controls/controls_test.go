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

// fakeAudio records every apply so tests can assert the controls layer
// actually drove the engine (mirrors internal/patches's fakeAudio style).
type fakeAudio struct {
	mu sync.Mutex

	volume, reverb, compressor      float32
	cutoffHz                        float32
	masteringComp, limiterCeilingDB float32
	volumeCalls, reverbCalls        int
	compressorCalls, cutoffCalls    int
	masteringCalls, limiterCalls    int
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

// fakeStore implements StateStore in memory.
type fakeStore struct {
	knobs        map[string]state.Knob
	currentPatch string
	updates      []knobUpdate
	setCurrCalls int
}

func newFakeStore() *fakeStore {
	return &fakeStore{knobs: map[string]state.Knob{}}
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
	sfPatch     = patches.Patch{Name: "salamander", Display: "Salamander", Type: "soundfont"}
	nativePatch = patches.Patch{Name: "moog", Display: "Moog", Type: "native", Engine: "minimoog"}
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
