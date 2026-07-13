package pages

import (
	"fmt"
	"math"
	"sync"
	"testing"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/controls"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
	"github.com/mschulkind-oss/polyclav/internal/patches"
	"github.com/mschulkind-oss/polyclav/internal/state"
)

// fakeAudio implements controls.Audio, recording the last value per
// setter plus a per-method call count, so routing tests can assert
// exactly which engine surface a knob tick reached (the same style as
// internal/controls's own fakeAudio, re-implemented here because test
// fakes don't cross package boundaries). Unsynchronized on purpose:
// every test drives it from a single goroutine through Controls'
// writer lock.
type fakeAudio struct {
	calls map[string]int

	// oscHook, when set, runs at SetNativeOsc entry — the gate the C1
	// concurrency regression uses to hold a MergeSynth mid-apply.
	oscHook func()

	volume, reverb, compressor, cutoffHz       float32
	drivePedal                                 float32
	masteringComp, limiterDB                   float32
	resonance, noise, glide, pulseWidth, drive float32
	kbdTrack, bendRange                        float32
	feA, feD, feS, feR, feAmt                  float32
	aeA, aeD, aeS, aeR                         float32
	velToCutoff, velToAmp                      float32
	oscIdx, oscOctave                          int
	oscWave                                    string
	oscDetune, oscLevel                        float32
	lfoWave                                    string
	lfoRate, lfoPitch, lfoCutoff, lfoAmp       float32
	voiceMode                                  string
	oversample                                 bool
}

func newFakeAudio() *fakeAudio { return &fakeAudio{calls: map[string]int{}} }

func (f *fakeAudio) rec(m string) { f.calls[m]++ }

// reset clears the call counts (values persist — they are the engine's
// state) so a test can assert only the calls it caused.
func (f *fakeAudio) reset() { f.calls = map[string]int{} }

func (f *fakeAudio) SetMasterVolume(v float32) { f.rec("SetMasterVolume"); f.volume = v }
func (f *fakeAudio) SetReverb(v float32)       { f.rec("SetReverb"); f.reverb = v }
func (f *fakeAudio) SetCompressor(v float32)   { f.rec("SetCompressor"); f.compressor = v }
func (f *fakeAudio) SetDrivePedal(v float32)   { f.rec("SetDrivePedal"); f.drivePedal = v }
func (f *fakeAudio) SetNativeCutoffHz(hz float32) {
	f.rec("SetNativeCutoffHz")
	f.cutoffHz = hz
}
func (f *fakeAudio) SetMasteringCompressor(v float32) {
	f.rec("SetMasteringCompressor")
	f.masteringComp = v
}
func (f *fakeAudio) SetLimiterCeilingDB(db float32) { f.rec("SetLimiterCeilingDB"); f.limiterDB = db }
func (f *fakeAudio) SetNativeResonance(v float32)   { f.rec("SetNativeResonance"); f.resonance = v }
func (f *fakeAudio) SetNativeFilterEnv(a, d, s, r, amount float32) {
	f.rec("SetNativeFilterEnv")
	f.feA, f.feD, f.feS, f.feR, f.feAmt = a, d, s, r, amount
}
func (f *fakeAudio) SetNativeOsc(idx int, wave string, octave int, detuneCents, level float32) error {
	if f.oscHook != nil {
		f.oscHook()
	}
	f.rec("SetNativeOsc")
	f.oscIdx, f.oscWave, f.oscOctave, f.oscDetune, f.oscLevel = idx, wave, octave, detuneCents, level
	return nil
}
func (f *fakeAudio) SetNativeNoise(level float32) { f.rec("SetNativeNoise"); f.noise = level }
func (f *fakeAudio) SetNativeGlide(s float32)     { f.rec("SetNativeGlide"); f.glide = s }
func (f *fakeAudio) SetNativeAmpEnv(a, d, s, r float32) {
	f.rec("SetNativeAmpEnv")
	f.aeA, f.aeD, f.aeS, f.aeR = a, d, s, r
}
func (f *fakeAudio) SetNativePulseWidth(w float32) { f.rec("SetNativePulseWidth"); f.pulseWidth = w }
func (f *fakeAudio) SetNativeDrive(d float32)      { f.rec("SetNativeDrive"); f.drive = d }
func (f *fakeAudio) SetNativeVelRouting(toCutoff, toAmp float32) {
	f.rec("SetNativeVelRouting")
	f.velToCutoff, f.velToAmp = toCutoff, toAmp
}
func (f *fakeAudio) SetNativeKbdTrack(amt float32) { f.rec("SetNativeKbdTrack"); f.kbdTrack = amt }
func (f *fakeAudio) SetNativeLFO(wave string, rateHz, toPitchCents, toCutoffOct, toAmp float32) error {
	f.rec("SetNativeLFO")
	f.lfoWave, f.lfoRate, f.lfoPitch, f.lfoCutoff, f.lfoAmp = wave, rateHz, toPitchCents, toCutoffOct, toAmp
	return nil
}
func (f *fakeAudio) SetNativeBendRange(st float32) { f.rec("SetNativeBendRange"); f.bendRange = st }
func (f *fakeAudio) SetNativeVoiceMode(mode string) error {
	f.rec("SetNativeVoiceMode")
	f.voiceMode = mode
	return nil
}
func (f *fakeAudio) SetNativeOversample(on bool) { f.rec("SetNativeOversample"); f.oversample = on }

// fakeRegistry implements controls.Registry over a fixed patch list.
type fakeRegistry struct {
	patches []patches.Patch
	current int
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
	for i, p := range f.patches {
		if p.Name == name {
			f.current = i
			return nil
		}
	}
	return fmt.Errorf("patch %q not found", name)
}

func (f *fakeRegistry) SelectIndex(i int) error {
	if i < 0 || i >= len(f.patches) {
		return fmt.Errorf("patch index %d out of range", i)
	}
	f.current = i
	return nil
}

// fakeStore implements controls.StateStore in memory.
type fakeStore struct {
	knobs        map[string]state.Knob
	synths       map[string]state.SynthState
	currentPatch string
}

func newFakeStore() *fakeStore {
	return &fakeStore{knobs: map[string]state.Knob{}, synths: map[string]state.SynthState{}}
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
}

func (f *fakeStore) PatchSynth(name string) (state.SynthState, bool) {
	syn, ok := f.synths[name]
	return syn, ok
}

func (f *fakeStore) UpdatePatchSynth(name string, syn state.SynthState) { f.synths[name] = syn }

func (f *fakeStore) SetCurrentPatch(name string) { f.currentPatch = name }

// fakeScreen records SetDisplayText writes.
type screenWrite struct{ line1, line2 string }

type fakeScreen struct {
	// hook, when set, runs at SetDisplayText entry — the gate the C2
	// regression uses to hold a cycle() between its paint and its flash.
	hook   func()
	writes []screenWrite
}

func (s *fakeScreen) SetDisplayText(line1, line2 string) error {
	if s.hook != nil {
		s.hook()
	}
	s.writes = append(s.writes, screenWrite{line1, line2})
	return nil
}

func (s *fakeScreen) last(t *testing.T) screenWrite {
	t.Helper()
	if len(s.writes) == 0 {
		t.Fatal("expected a screen write, got none")
	}
	return s.writes[len(s.writes)-1]
}

// fakePads records the latest color per pad.
type padKey struct{ row, col int }

type fakePads struct{ colors map[padKey]components.Color }

func newFakePads() *fakePads { return &fakePads{colors: map[padKey]components.Color{}} }

func (p *fakePads) SetPadColor(row, col int, color components.Color) error {
	p.colors[padKey{row, col}] = color
	return nil
}

// fakePlayer implements PlayerControl: toggles an in-memory transport.
type fakePlayer struct {
	playing bool
	clip    string
	toggles int
}

func (p *fakePlayer) Toggle() (bool, string, bool) {
	if p.clip == "" {
		return false, "", false
	}
	p.playing = !p.playing
	p.toggles++
	return p.playing, p.clip, true
}

var (
	nativePatch = patches.Patch{Name: "moog", Display: "Moog", Type: "native", Engine: "minimoog"}
	sfPatch     = patches.Patch{Name: "piano", Display: "Piano", Type: "soundfont"}
)

type fixture struct {
	audio  *fakeAudio
	reg    *fakeRegistry
	st     *fakeStore
	ctl    *controls.Controls
	screen *fakeScreen
	pads   *fakePads
	pg     *Pages
}

func newFixture(t *testing.T, ps ...patches.Patch) *fixture {
	t.Helper()
	f := &fixture{
		audio:  newFakeAudio(),
		reg:    newFakeRegistry(ps...),
		st:     newFakeStore(),
		screen: &fakeScreen{},
		pads:   newFakePads(),
	}
	f.ctl = controls.New(nil, f.audio, f.reg, f.st, nil)
	f.pg = New(f.ctl, f.screen, f.pads)
	return f
}

// newNativeFixture selects the native patch, syncs the page machine,
// and clears the setup noise so tests assert only what they cause.
func newNativeFixture(t *testing.T) *fixture {
	t.Helper()
	f := newFixture(t, nativePatch, sfPatch)
	if err := f.ctl.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	f.pg.OnPatchChange("native")
	f.audio.reset()
	f.screen.writes = nil
	return f
}

func approxEq(a, b float32) bool { return math.Abs(float64(a-b)) < 1e-3 }

// TestKnobRoutingAllPages is the full page×slot routing matrix: every
// bound slot gets one tick from the factory-default state and must (a)
// hit exactly its controls setter (observed at the fake engine), (b)
// apply the slot's per-tick step, and (c) show "Label" + formatted
// value on the screen. Unbound slots must do nothing at all.
//
// Expected values derive from controls' defaults: knob store
// (volume 1.0, reverb 0, comp 0), cutoff pos 0.5, and defaultSynth()
// (resonance 0.3, filter env 5ms/600ms/0.4/600ms amount 0, amp env
// 5ms/200ms/0.7/400ms, osc1 saw lvl 1.0 det 0, osc2 saw lvl 0 det -7,
// osc3 saw -1 oct lvl 0 det +5, noise 0, glide 0, pw 0.25, drive 0,
// vel 0/1, kbd 0, LFO triangle 5 Hz depths 0, bend 2, mono_legato).
func TestKnobRoutingAllPages(t *testing.T) {
	type tc struct {
		name         string
		page         int  // 0-based page index
		knob         int  // 1..8
		ticks        int8 // raw encoder delta
		wantLabel    string
		wantDisplay  string                    // "" = use wantDisplayF
		wantDisplayF func(a *fakeAudio) string // dynamic expectation
		wantCall     string                    // fakeAudio method, exactly once
		check        func(t *testing.T, a *fakeAudio)
	}
	cases := []tc{
		// ---- Page 1 MAIN ----
		{name: "MAIN volume", page: 0, knob: 1, ticks: -1, wantLabel: "Volume", wantDisplay: "99%",
			wantCall: "SetMasterVolume",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.volume, 1-1.0/127) {
					t.Errorf("volume = %v", a.volume)
				}
			}},
		{name: "MAIN reverb", page: 0, knob: 2, ticks: 1, wantLabel: "Reverb", wantDisplay: "1%",
			wantCall: "SetReverb",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.reverb, 1.0/127) {
					t.Errorf("reverb = %v", a.reverb)
				}
			}},
		{name: "MAIN comp", page: 0, knob: 3, ticks: 1, wantLabel: "Comp", wantDisplay: "1%",
			wantCall: "SetCompressor",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.compressor, 1.0/127) {
					t.Errorf("compressor = %v", a.compressor)
				}
			}},
		{name: "MAIN pedal", page: 0, knob: 4, ticks: 1, wantLabel: "Pedal", wantDisplay: "1%",
			wantCall: "SetDrivePedal",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.drivePedal, 1.0/127) {
					t.Errorf("drivePedal = %v", a.drivePedal)
				}
			}},
		{name: "MAIN resonance", page: 0, knob: 5, ticks: 1, wantLabel: "Resonance", wantDisplay: "31%",
			wantCall: "SetNativeResonance",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.resonance, 0.3075) {
					t.Errorf("resonance = %v", a.resonance)
				}
			}},
		{name: "MAIN glide", page: 0, knob: 6, ticks: 1, wantLabel: "Glide", wantDisplay: "50 ms",
			wantCall: "SetNativeGlide",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.glide, 0.05) {
					t.Errorf("glide = %v", a.glide)
				}
			}},
		{name: "MAIN drive", page: 0, knob: 7, ticks: 1, wantLabel: "Drive", wantDisplay: "1%",
			wantCall: "SetNativeDrive",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.drive, 1.0/127) {
					t.Errorf("drive = %v", a.drive)
				}
			}},
		{name: "MAIN knob 8 unbound", page: 0, knob: 8, ticks: 1},

		// ---- Page 2 OSC ----
		{name: "OSC osc1 level", page: 1, knob: 1, ticks: -1, wantLabel: "Osc1 Level", wantDisplay: "99%",
			wantCall: "SetNativeOsc",
			check: func(t *testing.T, a *fakeAudio) {
				if a.oscIdx != 0 || a.oscWave != "saw" || a.oscOctave != 0 ||
					!approxEq(a.oscDetune, 0) || !approxEq(a.oscLevel, 1-1.0/127) {
					t.Errorf("osc = %+v", *a)
				}
			}},
		{name: "OSC osc1 detune", page: 1, knob: 2, ticks: 1, wantLabel: "Osc1 Detune", wantDisplay: "+1 c",
			wantCall: "SetNativeOsc",
			check: func(t *testing.T, a *fakeAudio) {
				if a.oscIdx != 0 || !approxEq(a.oscDetune, 1) || !approxEq(a.oscLevel, 1) {
					t.Errorf("osc idx=%d detune=%v level=%v", a.oscIdx, a.oscDetune, a.oscLevel)
				}
			}},
		{name: "OSC osc2 level", page: 1, knob: 3, ticks: 1, wantLabel: "Osc2 Level", wantDisplay: "1%",
			wantCall: "SetNativeOsc",
			check: func(t *testing.T, a *fakeAudio) {
				if a.oscIdx != 1 || !approxEq(a.oscLevel, 1.0/127) || !approxEq(a.oscDetune, -7) {
					t.Errorf("osc idx=%d level=%v detune=%v", a.oscIdx, a.oscLevel, a.oscDetune)
				}
			}},
		{name: "OSC osc2 detune", page: 1, knob: 4, ticks: 1, wantLabel: "Osc2 Detune", wantDisplay: "-6 c",
			wantCall: "SetNativeOsc",
			check: func(t *testing.T, a *fakeAudio) {
				if a.oscIdx != 1 || !approxEq(a.oscDetune, -6) {
					t.Errorf("osc idx=%d detune=%v", a.oscIdx, a.oscDetune)
				}
			}},
		{name: "OSC osc3 level", page: 1, knob: 5, ticks: 1, wantLabel: "Osc3 Level", wantDisplay: "1%",
			wantCall: "SetNativeOsc",
			check: func(t *testing.T, a *fakeAudio) {
				if a.oscIdx != 2 || a.oscOctave != -1 || !approxEq(a.oscLevel, 1.0/127) {
					t.Errorf("osc idx=%d oct=%d level=%v", a.oscIdx, a.oscOctave, a.oscLevel)
				}
			}},
		{name: "OSC osc3 detune", page: 1, knob: 6, ticks: 1, wantLabel: "Osc3 Detune", wantDisplay: "+6 c",
			wantCall: "SetNativeOsc",
			check: func(t *testing.T, a *fakeAudio) {
				if a.oscIdx != 2 || !approxEq(a.oscDetune, 6) {
					t.Errorf("osc idx=%d detune=%v", a.oscIdx, a.oscDetune)
				}
			}},
		{name: "OSC noise", page: 1, knob: 7, ticks: 1, wantLabel: "Noise", wantDisplay: "1%",
			wantCall: "SetNativeNoise",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.noise, 1.0/127) {
					t.Errorf("noise = %v", a.noise)
				}
			}},
		{name: "OSC pulse width", page: 1, knob: 8, ticks: 1, wantLabel: "Pulse Width", wantDisplay: "26%",
			wantCall: "SetNativePulseWidth",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.pulseWidth, 0.25+1.0/127) {
					t.Errorf("pulseWidth = %v", a.pulseWidth)
				}
			}},

		// ---- Page 3 FILTER ----
		{name: "FILTER cutoff", page: 2, knob: 1, ticks: 1, wantLabel: "Cutoff",
			wantDisplayF: func(a *fakeAudio) string { return formatHz(a.cutoffHz) },
			wantCall:     "SetNativeCutoffHz"},
		{name: "FILTER resonance", page: 2, knob: 2, ticks: 1, wantLabel: "Resonance", wantDisplay: "31%",
			wantCall: "SetNativeResonance"},
		{name: "FILTER env amount", page: 2, knob: 3, ticks: 1, wantLabel: "Env Amount", wantDisplay: "1%",
			wantCall: "SetNativeFilterEnv",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.feAmt, 1.0/127) || !approxEq(a.feA, 0.005) ||
					!approxEq(a.feD, 0.6) || !approxEq(a.feS, 0.4) || !approxEq(a.feR, 0.6) {
					t.Errorf("filter env = %v %v %v %v amt %v", a.feA, a.feD, a.feS, a.feR, a.feAmt)
				}
			}},
		{name: "FILTER attack", page: 2, knob: 4, ticks: 1, wantLabel: "F.Attack", wantDisplay: "30 ms",
			wantCall: "SetNativeFilterEnv",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.feA, 0.03) {
					t.Errorf("feA = %v", a.feA)
				}
			}},
		{name: "FILTER decay", page: 2, knob: 5, ticks: 1, wantLabel: "F.Decay", wantDisplay: "625 ms",
			wantCall: "SetNativeFilterEnv",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.feD, 0.625) {
					t.Errorf("feD = %v", a.feD)
				}
			}},
		{name: "FILTER sustain", page: 2, knob: 6, ticks: 1, wantLabel: "F.Sustain", wantDisplay: "41%",
			wantCall: "SetNativeFilterEnv",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.feS, 0.4+1.0/127) {
					t.Errorf("feS = %v", a.feS)
				}
			}},
		{name: "FILTER release", page: 2, knob: 7, ticks: 1, wantLabel: "F.Release", wantDisplay: "625 ms",
			wantCall: "SetNativeFilterEnv",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.feR, 0.625) {
					t.Errorf("feR = %v", a.feR)
				}
			}},
		{name: "FILTER kbd track", page: 2, knob: 8, ticks: 1, wantLabel: "Kbd Track", wantDisplay: "1%",
			wantCall: "SetNativeKbdTrack",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.kbdTrack, 1.0/127) {
					t.Errorf("kbdTrack = %v", a.kbdTrack)
				}
			}},

		// ---- Page 4 AMP ----
		{name: "AMP attack", page: 3, knob: 1, ticks: 1, wantLabel: "A.Attack", wantDisplay: "30 ms",
			wantCall: "SetNativeAmpEnv",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.aeA, 0.03) || !approxEq(a.aeD, 0.2) ||
					!approxEq(a.aeS, 0.7) || !approxEq(a.aeR, 0.4) {
					t.Errorf("amp env = %v %v %v %v", a.aeA, a.aeD, a.aeS, a.aeR)
				}
			}},
		{name: "AMP decay", page: 3, knob: 2, ticks: 1, wantLabel: "A.Decay", wantDisplay: "225 ms",
			wantCall: "SetNativeAmpEnv",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.aeD, 0.225) {
					t.Errorf("aeD = %v", a.aeD)
				}
			}},
		{name: "AMP sustain", page: 3, knob: 3, ticks: 1, wantLabel: "A.Sustain", wantDisplay: "71%",
			wantCall: "SetNativeAmpEnv",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.aeS, 0.7+1.0/127) {
					t.Errorf("aeS = %v", a.aeS)
				}
			}},
		{name: "AMP release", page: 3, knob: 4, ticks: 1, wantLabel: "A.Release", wantDisplay: "425 ms",
			wantCall: "SetNativeAmpEnv",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.aeR, 0.425) {
					t.Errorf("aeR = %v", a.aeR)
				}
			}},
		{name: "AMP vel to amp", page: 3, knob: 5, ticks: -1, wantLabel: "Vel>Amp", wantDisplay: "99%",
			wantCall: "SetNativeVelRouting",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.velToAmp, 1-1.0/127) || !approxEq(a.velToCutoff, 0) {
					t.Errorf("vel = %v/%v", a.velToCutoff, a.velToAmp)
				}
			}},
		{name: "AMP vel to cutoff", page: 3, knob: 6, ticks: 1, wantLabel: "Vel>Cutoff", wantDisplay: "1%",
			wantCall: "SetNativeVelRouting",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.velToCutoff, 1.0/127) || !approxEq(a.velToAmp, 1) {
					t.Errorf("vel = %v/%v", a.velToCutoff, a.velToAmp)
				}
			}},
		{name: "AMP drive", page: 3, knob: 7, ticks: 1, wantLabel: "Drive", wantDisplay: "1%",
			wantCall: "SetNativeDrive"},
		{name: "AMP knob 8 unbound", page: 3, knob: 8, ticks: 1},

		// ---- Page 5 LFO/MOD ----
		{name: "LFO rate", page: 4, knob: 1, ticks: 1, wantLabel: "LFO Rate", wantDisplay: "5.1 Hz",
			wantCall: "SetNativeLFO",
			check: func(t *testing.T, a *fakeAudio) {
				if a.lfoWave != "triangle" || !approxEq(a.lfoRate, 5.1) {
					t.Errorf("lfo = %q %v", a.lfoWave, a.lfoRate)
				}
			}},
		{name: "LFO to pitch", page: 4, knob: 2, ticks: 1, wantLabel: "LFO>Pitch", wantDisplay: "1 c",
			wantCall: "SetNativeLFO",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.lfoPitch, 1) {
					t.Errorf("lfoPitch = %v", a.lfoPitch)
				}
			}},
		{name: "LFO to cutoff", page: 4, knob: 3, ticks: 1, wantLabel: "LFO>Cutoff", wantDisplay: "0.02 oct",
			wantCall: "SetNativeLFO",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.lfoCutoff, 0.02) {
					t.Errorf("lfoCutoff = %v", a.lfoCutoff)
				}
			}},
		{name: "LFO to amp", page: 4, knob: 4, ticks: 1, wantLabel: "LFO>Amp", wantDisplay: "1%",
			wantCall: "SetNativeLFO",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.lfoAmp, 1.0/127) {
					t.Errorf("lfoAmp = %v", a.lfoAmp)
				}
			}},
		{name: "LFO bend range", page: 4, knob: 5, ticks: 1, wantLabel: "Bend Range", wantDisplay: "3 st",
			wantCall: "SetNativeBendRange",
			check: func(t *testing.T, a *fakeAudio) {
				if !approxEq(a.bendRange, 3) {
					t.Errorf("bendRange = %v", a.bendRange)
				}
			}},
		{name: "LFO glide", page: 4, knob: 6, ticks: 1, wantLabel: "Glide", wantDisplay: "50 ms",
			wantCall: "SetNativeGlide"},
		{name: "LFO voice mode", page: 4, knob: 7, ticks: 1, wantLabel: "Voice Mode", wantDisplay: "Mono Retrig",
			wantCall: "SetNativeVoiceMode",
			check: func(t *testing.T, a *fakeAudio) {
				if a.voiceMode != "mono_retrig" {
					t.Errorf("voiceMode = %q", a.voiceMode)
				}
			}},
		{name: "LFO knob 8 unbound", page: 4, knob: 8, ticks: 1},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			f := newNativeFixture(t)
			for i := 0; i < c.page; i++ {
				f.pg.NextPage()
			}
			if idx, _ := f.pg.CurrentPage(); idx != c.page {
				t.Fatalf("setup: on page %d, want %d", idx, c.page)
			}
			f.audio.reset()
			f.screen.writes = nil

			f.pg.HandleKnob(c.knob, c.ticks)

			if c.wantLabel == "" { // unbound slot: nothing at all
				if len(f.screen.writes) != 0 {
					t.Fatalf("unbound slot wrote screen: %+v", f.screen.writes)
				}
				if len(f.audio.calls) != 0 {
					t.Fatalf("unbound slot drove audio: %v", f.audio.calls)
				}
				return
			}
			if got := f.audio.calls[c.wantCall]; got != 1 {
				t.Fatalf("%s called %d times, want 1 (all calls: %v)", c.wantCall, got, f.audio.calls)
			}
			w := f.screen.last(t)
			if w.line1 != c.wantLabel {
				t.Errorf("screen line1 = %q, want %q", w.line1, c.wantLabel)
			}
			want := c.wantDisplay
			if c.wantDisplayF != nil {
				want = c.wantDisplayF(f.audio)
			}
			if w.line2 != want {
				t.Errorf("screen line2 = %q, want %q", w.line2, want)
			}
			if c.check != nil {
				c.check(t, f.audio)
			}
		})
	}
}

// TestPageCyclingOrderAndWraparound pins the page order, both cycle
// directions, wraparound, and the flash text.
func TestPageCyclingOrderAndWraparound(t *testing.T) {
	f := newNativeFixture(t)
	wantForward := []struct{ name, flash string }{
		{"OSC", "Page 2/5"},
		{"FILTER", "Page 3/5"},
		{"AMP", "Page 4/5"},
		{"LFO/MOD", "Page 5/5"},
		{"MAIN", "Page 1/5"}, // wrap
	}
	for i, want := range wantForward {
		f.pg.NextPage()
		if _, name := f.pg.CurrentPage(); name != want.name {
			t.Fatalf("NextPage #%d: on %q, want %q", i+1, name, want.name)
		}
		w := f.screen.last(t)
		if w.line1 != want.name || w.line2 != want.flash {
			t.Errorf("NextPage #%d screen = %q/%q, want %q/%q", i+1, w.line1, w.line2, want.name, want.flash)
		}
	}
	// Back around the other way: MAIN → LFO/MOD (wrap).
	f.pg.PrevPage()
	if idx, name := f.pg.CurrentPage(); idx != 4 || name != "LFO/MOD" {
		t.Fatalf("PrevPage wrap: on %d %q, want 4 LFO/MOD", idx, name)
	}
}

// TestNonNativeGating: with a soundfont patch only page 0 is live, page
// switches are refused with a "(native only)" flash, knobs 1-4 still
// work, and every native-only slot is inert.
func TestNonNativeGating(t *testing.T) {
	f := newFixture(t, nativePatch, sfPatch)
	if err := f.ctl.SelectPatch("piano"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	f.pg.OnPatchChange("soundfont")
	f.audio.reset()
	f.screen.writes = nil

	f.pg.NextPage()
	if idx, name := f.pg.CurrentPage(); idx != 0 || name != "MAIN" {
		t.Fatalf("NextPage on soundfont moved to %d %q", idx, name)
	}
	w := f.screen.last(t)
	if w.line1 != "(native only)" {
		t.Errorf("blocked switch screen line1 = %q, want %q", w.line1, "(native only)")
	}
	f.pg.PrevPage()
	if idx, _ := f.pg.CurrentPage(); idx != 0 {
		t.Fatalf("PrevPage on soundfont moved to %d", idx)
	}

	// Knobs 1-4 (global volume/reverb/comp/pedal) stay live.
	f.screen.writes = nil
	f.audio.reset()
	f.pg.HandleKnob(1, -1)
	if f.audio.calls["SetMasterVolume"] != 1 {
		t.Fatalf("volume knob dead on soundfont: %v", f.audio.calls)
	}
	if w := f.screen.last(t); w.line1 != "Volume" {
		t.Errorf("screen line1 = %q, want Volume", w.line1)
	}

	f.screen.writes = nil
	f.audio.reset()
	f.pg.HandleKnob(4, 1)
	if f.audio.calls["SetDrivePedal"] != 1 {
		t.Fatalf("pedal knob dead on soundfont: %v", f.audio.calls)
	}
	if w := f.screen.last(t); w.line1 != "Pedal" {
		t.Errorf("screen line1 = %q, want Pedal", w.line1)
	}

	// Knobs 5-8 on MAIN are native-only (or unbound): fully inert.
	for knob := 5; knob <= 8; knob++ {
		f.audio.reset()
		f.screen.writes = nil
		f.pg.HandleKnob(knob, 1)
		if len(f.audio.calls) != 0 {
			t.Errorf("knob %d drove audio on soundfont: %v", knob, f.audio.calls)
		}
		if len(f.screen.writes) != 0 {
			t.Errorf("knob %d wrote screen on soundfont: %+v", knob, f.screen.writes)
		}
	}
}

// TestPadIndicators pins the page-indicator painting: active page
// bright, others dim, row 1 only, columns 5-7 and the patch row (0)
// untouched.
func TestPadIndicators(t *testing.T) {
	f := newNativeFixture(t)
	f.pg.RefreshPads()

	for col := 0; col < 5; col++ {
		want := padPageAvailable
		if col == 0 {
			want = padPageActive
		}
		if got := f.pads.colors[padKey{PageIndicatorRow, col}]; got != want {
			t.Errorf("pad (1,%d) = %d, want %d", col, got, want)
		}
	}
	for col := 5; col <= 7; col++ {
		if _, ok := f.pads.colors[padKey{PageIndicatorRow, col}]; ok {
			t.Errorf("pad (1,%d) painted; columns 5-7 are reserved", col)
		}
	}
	for col := 0; col <= 7; col++ {
		if _, ok := f.pads.colors[padKey{0, col}]; ok {
			t.Errorf("pad (0,%d) painted; patch row belongs to main", col)
		}
	}

	// Switching pages moves the bright pad.
	f.pg.NextPage()
	if got := f.pads.colors[padKey{PageIndicatorRow, 1}]; got != padPageActive {
		t.Errorf("after NextPage pad (1,1) = %d, want active %d", got, padPageActive)
	}
	if got := f.pads.colors[padKey{PageIndicatorRow, 0}]; got != padPageAvailable {
		t.Errorf("after NextPage pad (1,0) = %d, want available %d", got, padPageAvailable)
	}
}

// TestOnPatchChangeRefresh: leaving the native engine snaps to page 0
// and darkens pages 2-5; returning re-lights them (page stays 0 — page
// persistence per patch is deferred, ROADMAP §3.1).
func TestOnPatchChangeRefresh(t *testing.T) {
	f := newNativeFixture(t)
	f.pg.NextPage()
	f.pg.NextPage() // FILTER
	if idx, _ := f.pg.CurrentPage(); idx != 2 {
		t.Fatalf("setup: page %d, want 2", idx)
	}

	f.pg.OnPatchChange("soundfont")
	if idx, name := f.pg.CurrentPage(); idx != 0 || name != "MAIN" {
		t.Fatalf("after soundfont select: page %d %q, want 0 MAIN", idx, name)
	}
	if got := f.pads.colors[padKey{PageIndicatorRow, 0}]; got != padPageActive {
		t.Errorf("pad (1,0) = %d, want active", got)
	}
	for col := 1; col < 5; col++ {
		if got := f.pads.colors[padKey{PageIndicatorRow, col}]; got != padPageUnavailable {
			t.Errorf("pad (1,%d) = %d, want unavailable %d", col, got, padPageUnavailable)
		}
	}

	f.pg.OnPatchChange("native")
	if idx, _ := f.pg.CurrentPage(); idx != 0 {
		t.Fatalf("after native re-select: page %d, want 0", idx)
	}
	for col := 1; col < 5; col++ {
		if got := f.pads.colors[padKey{PageIndicatorRow, col}]; got != padPageAvailable {
			t.Errorf("pad (1,%d) = %d, want available %d", col, got, padPageAvailable)
		}
	}
}

// TestTogglePlay pins the transport-Play screen feedback and the no-op
// paths (no player attached; nothing played yet).
func TestTogglePlay(t *testing.T) {
	f := newNativeFixture(t)

	f.pg.TogglePlay() // no player attached
	if len(f.screen.writes) != 0 {
		t.Fatalf("TogglePlay without player wrote screen: %+v", f.screen.writes)
	}

	p := &fakePlayer{}
	f.pg.AttachPlayer(p)
	f.pg.TogglePlay()
	if w := f.screen.last(t); w.line1 != "(no clip)" {
		t.Errorf("no-clip toggle screen = %q, want (no clip)", w.line1)
	}

	p.clip = "bass-riff"
	f.pg.TogglePlay()
	if w := f.screen.last(t); w.line1 != "PLAY" || w.line2 != "bass-riff" {
		t.Errorf("play screen = %q/%q, want PLAY/bass-riff", w.line1, w.line2)
	}
	f.pg.TogglePlay()
	if w := f.screen.last(t); w.line1 != "STOP" || w.line2 != "bass-riff" {
		t.Errorf("stop screen = %q/%q, want STOP/bass-riff", w.line1, w.line2)
	}
	if p.toggles != 2 {
		t.Errorf("toggles = %d, want 2", p.toggles)
	}
}

// TestVoiceModeCycleWraps: the 3-state selector wraps both directions
// and moves one state per detent regardless of tick magnitude.
func TestVoiceModeCycleWraps(t *testing.T) {
	f := newNativeFixture(t)
	for i := 0; i < 4; i++ {
		f.pg.NextPage()
	} // LFO/MOD

	steps := []struct {
		ticks int8
		want  string
	}{
		{1, "mono_retrig"},
		{1, "poly"},
		{3, "mono_legato"}, // wraps; |delta|>1 still one step
		{-1, "poly"},       // wraps backwards
		{-1, "mono_retrig"},
	}
	for i, s := range steps {
		f.pg.HandleKnob(7, s.ticks)
		if f.audio.voiceMode != s.want {
			t.Fatalf("step %d: voiceMode = %q, want %q", i, f.audio.voiceMode, s.want)
		}
	}
}

// TestHandleKnobBounds: out-of-range indices and zero deltas are inert.
func TestHandleKnobBounds(t *testing.T) {
	f := newNativeFixture(t)
	f.pg.HandleKnob(0, 1)
	f.pg.HandleKnob(9, 1)
	f.pg.HandleKnob(1, 0)
	if len(f.audio.calls) != 0 || len(f.screen.writes) != 0 {
		t.Fatalf("bad-input knob events had effects: calls=%v writes=%+v", f.audio.calls, f.screen.writes)
	}
}

// TestPageDefsShape sanity-checks the table: 5 pages of 8 slots, every
// bound slot has a step and a label that fits the 16-char display, and
// unbound slots are fully zero.
func TestPageDefsShape(t *testing.T) {
	defs := pageDefs()
	if len(defs) != 5 {
		t.Fatalf("pages = %d, want 5", len(defs))
	}
	for pi, page := range defs {
		if page.Name == "" || len(page.Name) > 16 {
			t.Errorf("page %d name %q invalid", pi, page.Name)
		}
		for si, slot := range page.Slots {
			if slot.Adjust == nil {
				if slot.Label != "" || slot.Step != 0 {
					t.Errorf("page %d slot %d: unbound slot has leftovers %+v", pi, si, slot)
				}
				continue
			}
			if slot.Label == "" || len(slot.Label) > 16 {
				t.Errorf("page %d slot %d: label %q invalid", pi, si, slot.Label)
			}
			if slot.Step <= 0 {
				t.Errorf("page %d slot %d: step %v invalid", pi, si, slot.Step)
			}
			for _, r := range slot.Label {
				if r < 0x20 || r > 0x7E {
					t.Errorf("page %d slot %d: label %q not printable ASCII", pi, si, slot.Label)
				}
			}
		}
	}
}

// TestKnobPersistsSynthBlock: a synth-page knob tick must land in the
// state store for the current patch (the ROADMAP §3 auto-persist that
// obsoletes §2.5's Record save-arm button).
func TestKnobPersistsSynthBlock(t *testing.T) {
	f := newNativeFixture(t)
	f.pg.HandleKnob(5, 1) // MAIN Resonance
	syn, ok := f.st.synths["moog"]
	if !ok {
		t.Fatal("no synth block persisted for moog")
	}
	if !approxEq(syn.Resonance, 0.3075) {
		t.Errorf("persisted resonance = %v, want 0.3075", syn.Resonance)
	}
}

// TestKnobComposesWithConcurrentMergeSynth is the C1 regression: a web
// PATCH (MergeSynth) holding the writer lock mid-apply must not have
// its sibling-field edit reverted by a knob tick that snapshotted the
// synth block before the PATCH landed. The knob's read-modify-write now
// runs inside the writer lock (controls.AdjustSynth reads current
// INSIDE the closure), so under any interleaving the final state —
// cache, engine, and the persisted block — carries BOTH edits. Run
// under -race: the unsynchronized fakes flag any apply that escapes the
// writer lock.
func TestKnobComposesWithConcurrentMergeSynth(t *testing.T) {
	f := newNativeFixture(t)
	f.pg.NextPage() // OSC page: knob 1 = Osc1 Level, knob 2 = Osc1 Detune

	entered := make(chan struct{})
	release := make(chan struct{})
	var gate sync.Once
	f.audio.oscHook = func() {
		gate.Do(func() {
			close(entered)
			<-release
		})
	}

	level := float32(0.5)
	mergeDone := make(chan struct{})
	go func() {
		defer close(mergeDone)
		if _, err := f.ctl.MergeSynth(controls.SynthPartial{
			Oscs: []controls.OscPartial{{Index: 0, Level: &level}},
		}); err != nil {
			t.Errorf("MergeSynth: %v", err)
		}
	}()
	<-entered // MergeSynth holds the writer lock, blocked in the engine apply

	knobDone := make(chan struct{})
	go func() {
		defer close(knobDone)
		f.pg.HandleKnob(2, 1) // Osc1 Detune: +1 cent
	}()
	// Let the knob goroutine run up to the writer lock while the merge
	// still holds it. The pre-fix adjuster read its snapshot HERE — before
	// the merge's level landed — and pushed the stale level back with the
	// detune, silently reverting the PATCH.
	time.Sleep(20 * time.Millisecond)
	close(release)
	<-mergeDone
	<-knobDone

	syn := f.ctl.Synth()
	if !approxEq(syn.Oscs[0].Level, 0.5) || !approxEq(syn.Oscs[0].DetuneCents, 1) {
		t.Errorf("osc0 = %+v, want BOTH the merge's level 0.5 and the knob's detune +1", syn.Oscs[0])
	}
	if !approxEq(f.audio.oscLevel, 0.5) || !approxEq(f.audio.oscDetune, 1) {
		t.Errorf("engine osc = level %v detune %v, want 0.5 / +1", f.audio.oscLevel, f.audio.oscDetune)
	}
	st, ok := f.st.synths["moog"]
	if !ok {
		t.Fatal("no synth block persisted for moog")
	}
	if !approxEq(st.Oscs[0].Level, 0.5) || !approxEq(st.Oscs[0].DetuneCents, 1) {
		t.Errorf("persisted osc0 = %+v, want the merged result (level 0.5, detune +1)", st.Oscs[0])
	}
}

// TestCycleRepaintCannotRevertPatchGating is the C2 regression: a page
// cycle that passed its native gate but has not finished its screen
// flash must not repaint the pads with pre-change gating after an
// OnPatchChange("soundfont") lands. The pads are painted from live
// state INSIDE the lock (never from a hardcoded native=true), so the
// gated palette painted by OnPatchChange is final.
func TestCycleRepaintCannotRevertPatchGating(t *testing.T) {
	f := newNativeFixture(t)

	entered := make(chan struct{})
	release := make(chan struct{})
	var gate sync.Once
	f.screen.hook = func() {
		gate.Do(func() {
			close(entered)
			<-release
		})
	}

	cycleDone := make(chan struct{})
	go func() {
		defer close(cycleDone)
		f.pg.NextPage() // native at gate time; paints, then blocks in the flash
	}()
	<-entered // cycle is past its critical section, stuck in the screen write

	f.pg.OnPatchChange("soundfont") // snaps to page 0, paints the gated palette
	close(release)
	<-cycleDone

	// The gated palette must survive: pre-fix, cycle painted
	// paintPads(page, true) AFTER the flash and re-lit the synth pages.
	if got := f.pads.colors[padKey{PageIndicatorRow, 0}]; got != padPageActive {
		t.Errorf("pad (1,0) = %d, want active %d", got, padPageActive)
	}
	for col := 1; col < 5; col++ {
		if got := f.pads.colors[padKey{PageIndicatorRow, col}]; got != padPageUnavailable {
			t.Errorf("pad (1,%d) = %d, want unavailable %d", col, got, padPageUnavailable)
		}
	}
	if idx, name := f.pg.CurrentPage(); idx != 0 || name != "MAIN" {
		t.Errorf("page = %d %q, want 0 MAIN after leaving native", idx, name)
	}
}
