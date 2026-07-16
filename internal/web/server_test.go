package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/controls"
	"github.com/mschulkind-oss/polyclav/internal/patches"
	"github.com/mschulkind-oss/polyclav/internal/player"
	"github.com/mschulkind-oss/polyclav/internal/state"
)

// ---- fakes (mirroring internal/controls/controls_test.go) --------------

// fakeAudio records every apply so tests can assert the HTTP layer drove
// the engine through controls.
type fakeAudio struct {
	mu sync.Mutex

	volume, reverb, compressor      float32
	drivePedal                      float32
	chorusRateHz, chorusDepth       float32
	chorusMix                       float32
	tremoloRateHz, tremoloDepth     float32
	delayTimeMs, delayFeedback      float32
	delayMix                        float32
	cutoffHz                        float32
	masteringComp, limiterCeilingDB float32
	resonance, noise, glide         float32
	feAttack, feAmount              float32
	aeAttack, aeSustain             float32
	pulseWidth, drive               float32
	velToCutoff, velToAmp           float32
	kbdTrack, bendRange             float32
	lfoWave                         string
	lfoRateHz, lfoToPitchCents      float32
	voiceMode                       string
	oversample                      bool
	oscCalls                        int
}

func (f *fakeAudio) SetMasterVolume(v float32) { f.mu.Lock(); defer f.mu.Unlock(); f.volume = v }
func (f *fakeAudio) SetReverb(v float32)       { f.mu.Lock(); defer f.mu.Unlock(); f.reverb = v }
func (f *fakeAudio) SetCompressor(v float32)   { f.mu.Lock(); defer f.mu.Unlock(); f.compressor = v }
func (f *fakeAudio) SetDrivePedal(v float32)   { f.mu.Lock(); defer f.mu.Unlock(); f.drivePedal = v }
func (f *fakeAudio) SetChorusRateHz(hz float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.chorusRateHz = hz
}
func (f *fakeAudio) SetChorusDepth(v float32) { f.mu.Lock(); defer f.mu.Unlock(); f.chorusDepth = v }
func (f *fakeAudio) SetChorusMix(v float32)   { f.mu.Lock(); defer f.mu.Unlock(); f.chorusMix = v }
func (f *fakeAudio) SetTremoloRateHz(hz float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tremoloRateHz = hz
}
func (f *fakeAudio) SetTremoloDepth(v float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.tremoloDepth = v
}
func (f *fakeAudio) SetAnalogDelayTimeMs(ms float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delayTimeMs = ms
}
func (f *fakeAudio) SetAnalogDelayFeedback(v float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delayFeedback = v
}
func (f *fakeAudio) SetAnalogDelayMix(v float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.delayMix = v
}
func (f *fakeAudio) SetNativeCutoffHz(hz float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.cutoffHz = hz
}
func (f *fakeAudio) SetMasteringCompressor(v float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.masteringComp = v
}
func (f *fakeAudio) SetLimiterCeilingDB(v float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.limiterCeilingDB = v
}

func (f *fakeAudio) SetNativeResonance(v float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.resonance = v
}

func (f *fakeAudio) SetNativeFilterEnv(a, d, s, r, amount float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.feAttack, f.feAmount = a, amount
}

func (f *fakeAudio) SetNativeOsc(idx int, wave string, octave int, detuneCents, level float32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.oscCalls++
	return nil
}

func (f *fakeAudio) SetNativeNoise(level float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.noise = level
}

func (f *fakeAudio) SetNativeGlide(s float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.glide = s
}

func (f *fakeAudio) SetNativeAmpEnv(a, d, s, r float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.aeAttack, f.aeSustain = a, s
}

func (f *fakeAudio) SetNativePulseWidth(w float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulseWidth = w
}

func (f *fakeAudio) SetNativeDrive(d float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.drive = d
}

func (f *fakeAudio) SetNativeVelRouting(toCutoff, toAmp float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.velToCutoff, f.velToAmp = toCutoff, toAmp
}

func (f *fakeAudio) SetNativeKbdTrack(amt float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.kbdTrack = amt
}

func (f *fakeAudio) SetNativeLFO(wave string, rateHz, toPitchCents, _, _ float32) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.lfoWave, f.lfoRateHz, f.lfoToPitchCents = wave, rateHz, toPitchCents
	return nil
}

func (f *fakeAudio) SetNativeBendRange(st float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bendRange = st
}

func (f *fakeAudio) SetNativeVoiceMode(mode string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.voiceMode = mode
	return nil
}

func (f *fakeAudio) SetNativeOversample(on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.oversample = on
}

func (f *fakeAudio) getOscCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.oscCalls
}

func (f *fakeAudio) getVoiceMode() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.voiceMode
}

func (f *fakeAudio) getOversample() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.oversample
}

func (f *fakeAudio) get(field string) float32 {
	f.mu.Lock()
	defer f.mu.Unlock()
	switch field {
	case "volume":
		return f.volume
	case "reverb":
		return f.reverb
	case "compressor":
		return f.compressor
	case "drive_pedal":
		return f.drivePedal
	case "chorusRateHz":
		return f.chorusRateHz
	case "chorusDepth":
		return f.chorusDepth
	case "chorusMix":
		return f.chorusMix
	case "tremoloRateHz":
		return f.tremoloRateHz
	case "tremoloDepth":
		return f.tremoloDepth
	case "delayTimeMs":
		return f.delayTimeMs
	case "delayFeedback":
		return f.delayFeedback
	case "delayMix":
		return f.delayMix
	case "cutoffHz":
		return f.cutoffHz
	case "masteringComp":
		return f.masteringComp
	case "limiterCeilingDB":
		return f.limiterCeilingDB
	case "resonance":
		return f.resonance
	case "noise":
		return f.noise
	case "glide":
		return f.glide
	case "feAttack":
		return f.feAttack
	case "feAmount":
		return f.feAmount
	case "aeAttack":
		return f.aeAttack
	case "aeSustain":
		return f.aeSustain
	case "pulseWidth":
		return f.pulseWidth
	case "drive":
		return f.drive
	case "velToCutoff":
		return f.velToCutoff
	case "velToAmp":
		return f.velToAmp
	case "kbdTrack":
		return f.kbdTrack
	case "bendRange":
		return f.bendRange
	case "lfoRateHz":
		return f.lfoRateHz
	case "lfoToPitchCents":
		return f.lfoToPitchCents
	}
	return 0
}

// fakeRegistry implements controls.Registry over an in-memory list.
type fakeRegistry struct {
	mu      sync.Mutex
	patches []patches.Patch
	current int
}

func newFakeRegistry(ps ...patches.Patch) *fakeRegistry {
	return &fakeRegistry{patches: ps, current: -1}
}

func (f *fakeRegistry) All() []patches.Patch {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]patches.Patch, len(f.patches))
	copy(out, f.patches)
	return out
}

func (f *fakeRegistry) Current() *patches.Patch {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.current < 0 || f.current >= len(f.patches) {
		return nil
	}
	p := f.patches[f.current]
	return &p
}

func (f *fakeRegistry) Select(name string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	for i, p := range f.patches {
		if p.Name == name {
			f.current = i
			return nil
		}
	}
	return fmt.Errorf("patch %q not found", name)
}

func (f *fakeRegistry) SelectIndex(i int) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if i < 0 || i >= len(f.patches) {
		return fmt.Errorf("patch index %d out of range [0, %d)", i, len(f.patches))
	}
	f.current = i
	return nil
}

// fakeStore implements controls.StateStore in memory.
type fakeStore struct {
	mu           sync.Mutex
	knobs        map[string]state.Knob
	synths       map[string]state.SynthState
	currentPatch string
	pedalOrder   []string
}

func newFakeStore() *fakeStore {
	return &fakeStore{
		knobs:  map[string]state.Knob{},
		synths: map[string]state.SynthState{},
	}
}

func (f *fakeStore) PatchKnob(name string) state.Knob {
	f.mu.Lock()
	defer f.mu.Unlock()
	if k, ok := f.knobs[name]; ok {
		return k
	}
	return state.Defaults()
}

func (f *fakeStore) UpdatePatchKnob(name, field string, value float32) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.knobs[name]
	if !ok {
		k = state.Defaults()
	}
	switch field {
	case "volume":
		k.Volume = value
	case "reverb":
		k.Reverb = value
	case "compressor":
		k.Compressor = value
	case "drive_pedal":
		k.DrivePedal = value
	case "chorus_rate_hz":
		k.ChorusRateHz = value
	case "chorus_depth":
		k.ChorusDepth = value
	case "chorus_mix":
		k.ChorusMix = value
	case "tremolo_rate_hz":
		k.TremoloRateHz = value
	case "tremolo_depth":
		k.TremoloDepth = value
	case "delay_time_ms":
		k.DelayTimeMs = value
	case "delay_feedback":
		k.DelayFeedback = value
	case "delay_mix":
		k.DelayMix = value
	}
	f.knobs[name] = k
}

func (f *fakeStore) UpdatePatchEnable(name, stage string, on bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	k, ok := f.knobs[name]
	if !ok {
		k = state.Defaults()
	}
	switch stage {
	case "drive":
		k.DriveEnabled = on
	case "chorus":
		k.ChorusEnabled = on
	case "tremolo":
		k.TremoloEnabled = on
	case "delay":
		k.DelayEnabled = on
	}
	f.knobs[name] = k
}

func (f *fakeStore) PedalOrder() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pedalOrder...)
}

func (f *fakeStore) SetPedalOrder(order []string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pedalOrder = append([]string(nil), order...)
	return nil
}

func (f *fakeStore) PatchSynth(name string) (state.SynthState, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	syn, ok := f.synths[name]
	return syn, ok
}

func (f *fakeStore) UpdatePatchSynth(name string, syn state.SynthState) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.synths[name] = syn
}

func (f *fakeStore) SetCurrentPatch(name string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.currentPatch = name
}

func (f *fakeStore) getCurrentPatch() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.currentPatch
}

// fakeDevices implements DeviceStates with fixed strings.
type fakeDevices struct{ lk, xr string }

func (f fakeDevices) LaunchkeyState() string { return f.lk }
func (f fakeDevices) XR18State() string      { return f.xr }

// ---- fixture ------------------------------------------------------------

var (
	sfPatch     = patches.Patch{Name: "salamander", Display: "Salamander", Type: "soundfont", PadColor: 25, GainDB: -3}
	nativePatch = patches.Patch{Name: "moog", Display: "Moog", Type: "native", Engine: "minimoog", PadColor: 41}
)

type fixture struct {
	audio *fakeAudio
	reg   *fakeRegistry
	st    *fakeStore
	hub   *controls.Hub
	ctrl  *controls.Controls
	srv   *Server
}

// newFixture builds a Server over real controls.Controls wired to fakes.
// mod (may be nil) edits the Deps before New — e.g. to add a Player,
// Devices, or ConfigTOML.
func newFixture(t *testing.T, mod func(*Deps)) *fixture {
	t.Helper()
	f := &fixture{
		audio: &fakeAudio{},
		reg:   newFakeRegistry(sfPatch, nativePatch),
		st:    newFakeStore(),
		hub:   controls.NewHub(),
	}
	f.ctrl = controls.New(nil, f.audio, f.reg, f.st, f.hub)
	deps := Deps{
		Controls: f.ctrl,
		Hub:      f.hub,
		Registry: f.reg,
		Version:  "test-1",
	}
	if mod != nil {
		mod(&deps)
	}
	f.srv = New(deps)
	return f
}

// do runs one request through the handler and returns the recorder.
func (f *fixture) do(t *testing.T, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var rd *bytes.Reader
	if s, ok := body.(string); ok {
		rd = bytes.NewReader([]byte(s))
	} else if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("marshal body: %v", err)
		}
		rd = bytes.NewReader(b)
	} else {
		rd = bytes.NewReader(nil)
	}
	req := httptest.NewRequest(method, path, rd)
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// decodeBody unmarshals a JSON response body into a generic map.
func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var m map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &m); err != nil {
		t.Fatalf("unmarshal response %q: %v", rec.Body.String(), err)
	}
	return m
}

func wantStatus(t *testing.T, rec *httptest.ResponseRecorder, want int) {
	t.Helper()
	if rec.Code != want {
		t.Fatalf("status: expected %d, got %d (body: %s)", want, rec.Code, rec.Body.String())
	}
}

func approxEq(a, b float64) bool { return math.Abs(a-b) < 1e-3 }

// newTestPlayer returns a real player with a no-op sink, stopped on cleanup.
func newTestPlayer(t *testing.T) *player.Player {
	t.Helper()
	p := player.New(nil, nil)
	t.Cleanup(p.Stop)
	return p
}

// ---- status --------------------------------------------------------------

func TestStatusShape(t *testing.T) {
	f := newFixture(t, func(d *Deps) {
		d.Devices = fakeDevices{lk: "connected", xr: "reconnecting"}
		d.Player = newTestPlayer(t)
	})
	if err := f.ctrl.SelectPatch("salamander"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	f.ctrl.InitMastering(0.4, -0.6)

	rec := f.do(t, "GET", "/api/status", nil)
	wantStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "application/json") {
		t.Errorf("content-type: expected application/json, got %q", ct)
	}
	m := decodeBody(t, rec)

	if m["version"] != "test-1" {
		t.Errorf("version: expected test-1, got %v", m["version"])
	}
	dev, ok := m["devices"].(map[string]any)
	if !ok {
		t.Fatalf("devices: expected object, got %T", m["devices"])
	}
	if dev["launchkey"] != "connected" || dev["xr18"] != "reconnecting" {
		t.Errorf("devices: expected connected/reconnecting, got %v", dev)
	}

	params, ok := m["params"].(map[string]any)
	if !ok {
		t.Fatalf("params: expected object, got %T", m["params"])
	}
	if params["patch"] != "salamander" || params["patch_display"] != "Salamander" {
		t.Errorf("params patch: expected salamander/Salamander, got %v/%v", params["patch"], params["patch_display"])
	}
	if v := params["volume"].(float64); v != 1.0 { // state.Defaults()
		t.Errorf("params.volume: expected 1.0, got %v", v)
	}
	if v := params["mastering_comp"].(float64); !approxEq(v, 0.4) {
		t.Errorf("params.mastering_comp: expected 0.4, got %v", v)
	}
	if v := params["limiter_ceiling_db"].(float64); !approxEq(v, -0.6) {
		t.Errorf("params.limiter_ceiling_db: expected -0.6, got %v", v)
	}

	list, ok := m["patches"].([]any)
	if !ok || len(list) != 2 {
		t.Fatalf("patches: expected 2 entries, got %v", m["patches"])
	}
	p0 := list[0].(map[string]any)
	if p0["name"] != "salamander" || p0["display"] != "Salamander" || p0["type"] != "soundfont" {
		t.Errorf("patches[0]: unexpected %v", p0)
	}
	if p0["pad_color"].(float64) != 25 || !approxEq(p0["gain_db"].(float64), -3) || p0["index"].(float64) != 0 {
		t.Errorf("patches[0] pad_color/gain_db/index: unexpected %v", p0)
	}
	p1 := list[1].(map[string]any)
	if p1["name"] != "moog" || p1["type"] != "native" || p1["index"].(float64) != 1 {
		t.Errorf("patches[1]: unexpected %v", p1)
	}

	pl, ok := m["player"].(map[string]any)
	if !ok {
		t.Fatalf("player: expected object, got %T", m["player"])
	}
	if pl["playing"] != false || pl["tempo"].(float64) != 1.0 {
		t.Errorf("player: expected stopped at tempo 1.0, got %v", pl)
	}
}

func TestStatusNilPlayerAndDevices(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "GET", "/api/status", nil)
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)

	if m["player"] != nil {
		t.Errorf("player: expected null with nil player, got %v", m["player"])
	}
	dev := m["devices"].(map[string]any)
	if dev["launchkey"] != "unknown" || dev["xr18"] != "unknown" {
		t.Errorf("devices: expected unknown/unknown, got %v", dev)
	}
	params := m["params"].(map[string]any)
	if params["patch"] != "" {
		t.Errorf("params.patch: expected empty with no selection, got %v", params["patch"])
	}
}

// ---- patches ---------------------------------------------------------------

func TestPatchesList(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "GET", "/api/patches", nil)
	wantStatus(t, rec, http.StatusOK)
	var list []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &list); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(list) != 2 || list[0]["name"] != "salamander" || list[1]["name"] != "moog" {
		t.Errorf("unexpected patch list: %v", list)
	}
}

func TestPatchSelect(t *testing.T) {
	f := newFixture(t, nil)
	f.st.knobs = map[string]state.Knob{
		"salamander": {Volume: 0.7, Reverb: 0.2, Compressor: 0.4},
	}

	rec := f.do(t, "POST", "/api/patches/salamander/select", nil)
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if m["patch"] != "salamander" {
		t.Errorf("response params.patch: expected salamander, got %v", m["patch"])
	}
	if v := m["volume"].(float64); !approxEq(v, 0.7) {
		t.Errorf("response params.volume: expected 0.7, got %v", v)
	}
	// The select restored the stored knobs into the (fake) engine.
	if v := f.audio.get("volume"); !approxEq(float64(v), 0.7) {
		t.Errorf("audio volume: expected 0.7, got %v", v)
	}
	if f.st.getCurrentPatch() != "salamander" {
		t.Errorf("state current patch: expected salamander, got %q", f.st.getCurrentPatch())
	}
}

func TestPatchSelectUnknown(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "POST", "/api/patches/ghost/select", nil)
	wantStatus(t, rec, http.StatusNotFound)
	if f.reg.Current() != nil {
		t.Error("selection must not change on unknown patch")
	}
}

// ---- params -----------------------------------------------------------------

func TestParamsPatchSingleField(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("salamander"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}

	rec := f.do(t, "PATCH", "/api/params", map[string]any{"volume": 0.8})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	applied := m["applied"].(map[string]any)
	if v := applied["volume"].(float64); !approxEq(v, 0.8) {
		t.Errorf("applied.volume: expected 0.8, got %v", v)
	}
	if _, present := m["errors"]; present {
		t.Errorf("expected no errors key, got %v", m["errors"])
	}
	if v := f.audio.get("volume"); !approxEq(float64(v), 0.8) {
		t.Errorf("audio volume: expected 0.8, got %v", v)
	}
	if k := f.st.PatchKnob("salamander"); !approxEq(float64(k.Volume), 0.8) {
		t.Errorf("state volume: expected 0.8, got %v", k.Volume)
	}
}

func TestParamsPatchMultiField(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("moog"); err != nil { // native: cutoff is legal
		t.Fatalf("SelectPatch: %v", err)
	}

	rec := f.do(t, "PATCH", "/api/params", map[string]any{
		"volume":     0.7,
		"reverb":     0.2,
		"compressor": 0.3,
		"cutoff_pos": 0.5,
	})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	applied := m["applied"].(map[string]any)
	for field, want := range map[string]float64{"volume": 0.7, "reverb": 0.2, "compressor": 0.3, "cutoff_pos": 0.5} {
		if v := applied[field].(float64); !approxEq(v, want) {
			t.Errorf("applied.%s: expected %v, got %v", field, want, v)
		}
	}
	// 20 * 1000^0.5 ≈ 632.456 Hz
	if v := applied["cutoff_hz"].(float64); math.Abs(v-632.456) > 0.1 {
		t.Errorf("applied.cutoff_hz: expected ~632.456, got %v", v)
	}
	if v := f.audio.get("volume"); !approxEq(float64(v), 0.7) {
		t.Errorf("audio volume: expected 0.7, got %v", v)
	}
	if v := f.audio.get("reverb"); !approxEq(float64(v), 0.2) {
		t.Errorf("audio reverb: expected 0.2, got %v", v)
	}
	if v := f.audio.get("cutoffHz"); math.Abs(float64(v)-632.456) > 0.1 {
		t.Errorf("audio cutoff: expected ~632.456, got %v", v)
	}
}

func TestParamsPatchNoCurrentPatch(t *testing.T) {
	f := newFixture(t, nil) // nothing selected
	rec := f.do(t, "PATCH", "/api/params", map[string]any{"volume": 0.5})
	wantStatus(t, rec, http.StatusConflict)
	if v := f.audio.get("volume"); v != 0 {
		t.Errorf("audio must not be touched, got volume %v", v)
	}
}

func TestParamsPatchBadJSON(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "PATCH", "/api/params", `{"volume": nope}`)
	wantStatus(t, rec, http.StatusBadRequest)
}

func TestParamsPatchOutOfRange(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("salamander"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	for _, body := range []map[string]any{
		{"volume": 1.5},
		{"reverb": -0.1},
		{"cutoff_pos": 2.0},
	} {
		rec := f.do(t, "PATCH", "/api/params", body)
		wantStatus(t, rec, http.StatusBadRequest)
	}
	if v := f.audio.get("volume"); v != 1.0 { // still the select-restored default
		t.Errorf("audio volume must be untouched by rejected patches, got %v", v)
	}
}

func TestParamsPatchCutoffOnNonNativeCollectsError(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("salamander"); err != nil { // soundfont: no cutoff
		t.Fatalf("SelectPatch: %v", err)
	}

	rec := f.do(t, "PATCH", "/api/params", map[string]any{"volume": 0.6, "cutoff_pos": 0.5})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	applied := m["applied"].(map[string]any)
	if v := applied["volume"].(float64); !approxEq(v, 0.6) {
		t.Errorf("applied.volume: expected 0.6, got %v", v)
	}
	if _, present := applied["cutoff_pos"]; present {
		t.Error("cutoff_pos must not be in applied for a non-native patch")
	}
	errs, ok := m["errors"].(map[string]any)
	if !ok {
		t.Fatalf("expected errors object, got %v", m["errors"])
	}
	if _, present := errs["cutoff_pos"]; !present {
		t.Errorf("expected errors.cutoff_pos, got %v", errs)
	}
	if v := f.audio.get("cutoffHz"); v != 0 {
		t.Errorf("audio cutoff must not be touched, got %v", v)
	}
}

// ---- synth -----------------------------------------------------------------

// synthOsc pulls osc[i] out of a decoded synth response body.
func synthOsc(t *testing.T, m map[string]any, i int) map[string]any {
	t.Helper()
	osc, ok := m["osc"].([]any)
	if !ok || len(osc) != 3 {
		t.Fatalf("expected 3-entry osc array, got %v", m["osc"])
	}
	o, ok := osc[i].(map[string]any)
	if !ok {
		t.Fatalf("osc[%d]: expected object, got %T", i, osc[i])
	}
	return o
}

func TestSynthPatchSingleField(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}

	rec := f.do(t, "PATCH", "/api/synth", map[string]any{"resonance": 0.8})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if v := m["resonance"].(float64); !approxEq(v, 0.8) {
		t.Errorf("resonance: expected 0.8, got %v", v)
	}
	if v := f.audio.get("resonance"); !approxEq(float64(v), 0.8) {
		t.Errorf("audio resonance: expected 0.8, got %v", v)
	}
	// The rest of the snapshot is untouched defaults.
	fe := m["filter_env"].(map[string]any)
	if v := fe["decay"].(float64); !approxEq(v, 0.6) {
		t.Errorf("filter_env.decay: expected default 0.6, got %v", v)
	}
	o1 := synthOsc(t, m, 1)
	if o1["wave"] != "saw" || !approxEq(o1["detune_cents"].(float64), -7) || !approxEq(o1["level"].(float64), 0) {
		t.Errorf("osc[1]: expected default saw/-7c/0.0, got %v", o1)
	}
	if v := m["glide"].(float64); v != 0 {
		t.Errorf("glide: expected default 0, got %v", v)
	}
}

func TestSynthPatchMergeSemantics(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	// A native select applies the patch's whole synth block (per-patch
	// persistence, ROADMAP §3) — baseline the osc counter after it so the
	// assertions below count only the PATCH requests' applies.
	oscBase := f.audio.getOscCalls()

	// Partial filter_env: attack only, then decay only — attack survives.
	rec := f.do(t, "PATCH", "/api/synth", map[string]any{"filter_env": map[string]any{"attack": 0.05}})
	wantStatus(t, rec, http.StatusOK)
	rec = f.do(t, "PATCH", "/api/synth", map[string]any{"filter_env": map[string]any{"decay": 1.5}})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	fe := m["filter_env"].(map[string]any)
	if v := fe["attack"].(float64); !approxEq(v, 0.05) {
		t.Errorf("filter_env.attack: expected 0.05 to survive decay-only patch, got %v", v)
	}
	if v := fe["decay"].(float64); !approxEq(v, 1.5) {
		t.Errorf("filter_env.decay: expected 1.5, got %v", v)
	}
	if v := fe["sustain"].(float64); !approxEq(v, 0.4) {
		t.Errorf("filter_env.sustain: expected default 0.4, got %v", v)
	}
	if v := f.audio.get("feAttack"); !approxEq(float64(v), 0.05) {
		t.Errorf("audio filter env attack: expected 0.05, got %v", v)
	}

	// Partial osc: level only — wave/octave/detune keep their values.
	rec = f.do(t, "PATCH", "/api/synth", map[string]any{
		"osc": []map[string]any{{"index": 1, "level": 0.6}},
	})
	wantStatus(t, rec, http.StatusOK)
	m = decodeBody(t, rec)
	o1 := synthOsc(t, m, 1)
	if !approxEq(o1["level"].(float64), 0.6) {
		t.Errorf("osc[1].level: expected 0.6, got %v", o1["level"])
	}
	if o1["wave"] != "saw" || !approxEq(o1["detune_cents"].(float64), -7) || o1["octave"].(float64) != 0 {
		t.Errorf("osc[1]: expected saw/0oct/-7c preserved, got %v", o1)
	}

	// Multi-field body: several sections in one request.
	rec = f.do(t, "PATCH", "/api/synth", map[string]any{
		"resonance": 0.5,
		"glide":     0.2,
		"noise":     0.1,
		"osc": []map[string]any{
			{"index": 0, "wave": "square"},
			{"index": 2, "octave": -2, "level": 0.4},
		},
	})
	wantStatus(t, rec, http.StatusOK)
	m = decodeBody(t, rec)
	if !approxEq(m["resonance"].(float64), 0.5) || !approxEq(m["glide"].(float64), 0.2) || !approxEq(m["noise"].(float64), 0.1) {
		t.Errorf("scalar fields: unexpected %v/%v/%v", m["resonance"], m["glide"], m["noise"])
	}
	o0 := synthOsc(t, m, 0)
	if o0["wave"] != "square" || !approxEq(o0["level"].(float64), 1.0) {
		t.Errorf("osc[0]: expected square with preserved level 1.0, got %v", o0)
	}
	o2 := synthOsc(t, m, 2)
	if o2["octave"].(float64) != -2 || !approxEq(o2["level"].(float64), 0.4) || !approxEq(o2["detune_cents"].(float64), 5) {
		t.Errorf("osc[2]: expected -2oct/0.4 with preserved +5c, got %v", o2)
	}
	if n := f.audio.getOscCalls() - oscBase; n != 3 {
		t.Errorf("expected 3 osc applies, got %d", n)
	}
	if v := f.audio.get("glide"); !approxEq(float64(v), 0.2) {
		t.Errorf("audio glide: expected 0.2, got %v", v)
	}
}

func TestSynthPatchConflictWithoutNativePatch(t *testing.T) {
	bodies := []map[string]any{
		{"resonance": 0.5},
		{"filter_env": map[string]any{"attack": 0.1}},
		{"osc": []map[string]any{{"index": 0, "level": 0.5}}},
		{"voice_mode": "poly"},
		{"oversample": true},
		{"lfo": map[string]any{"rate_hz": 3.0}},
		{"amp_env": map[string]any{"attack": 0.1}},
		{"drive": 0.4},
	}
	t.Run("no patch selected", func(t *testing.T) {
		f := newFixture(t, nil)
		for _, b := range bodies {
			wantStatus(t, f.do(t, "PATCH", "/api/synth", b), http.StatusConflict)
		}
	})
	t.Run("soundfont patch", func(t *testing.T) {
		f := newFixture(t, nil)
		if err := f.ctrl.SelectPatch("salamander"); err != nil {
			t.Fatalf("SelectPatch: %v", err)
		}
		for _, b := range bodies {
			wantStatus(t, f.do(t, "PATCH", "/api/synth", b), http.StatusConflict)
		}
		if v := f.audio.get("resonance"); v != 0 {
			t.Errorf("audio must not be touched on 409, got resonance %v", v)
		}
		if n := f.audio.getOscCalls(); n != 0 {
			t.Errorf("audio must not be touched on 409, got %d osc calls", n)
		}
	})
}

func TestSynthPatchValidation(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	// The select itself restores the synth block (factory defaults here);
	// baseline after it so the "nothing leaked" checks below see only what
	// the rejected PATCH requests did.
	resBase := f.audio.get("resonance")
	oscBase := f.audio.getOscCalls()
	for name, body := range map[string]any{
		"bad JSON":            `{"resonance": nope}`,
		"resonance high":      map[string]any{"resonance": 0.96},
		"resonance negative":  map[string]any{"resonance": -0.1},
		"glide high":          map[string]any{"glide": 5.1},
		"noise high":          map[string]any{"noise": 1.1},
		"env attack high":     map[string]any{"filter_env": map[string]any{"attack": 11.0}},
		"env attack negative": map[string]any{"filter_env": map[string]any{"attack": -1.0}},
		"env sustain high":    map[string]any{"filter_env": map[string]any{"sustain": 1.5}},
		"env amount negative": map[string]any{"filter_env": map[string]any{"amount": -0.2}},
		"osc missing index":   map[string]any{"osc": []map[string]any{{"level": 0.5}}},
		"osc index high":      map[string]any{"osc": []map[string]any{{"index": 3, "level": 0.5}}},
		"osc index negative":  map[string]any{"osc": []map[string]any{{"index": -1, "level": 0.5}}},
		"osc bad wave":        map[string]any{"osc": []map[string]any{{"index": 0, "wave": "sine"}}},
		"osc octave high":     map[string]any{"osc": []map[string]any{{"index": 0, "octave": 3}}},
		"osc detune high":     map[string]any{"osc": []map[string]any{{"index": 0, "detune_cents": 101.0}}},
		"osc level high":      map[string]any{"osc": []map[string]any{{"index": 0, "level": 1.5}}},

		// Phase 3/4 fields.
		"pulse_width low":          map[string]any{"pulse_width": 0.01},
		"pulse_width high":         map[string]any{"pulse_width": 0.96},
		"drive high":               map[string]any{"drive": 1.5},
		"drive negative":           map[string]any{"drive": -0.1},
		"kbd_track high":           map[string]any{"kbd_track": 1.1},
		"bend_range high":          map[string]any{"bend_range": 12.5},
		"bend_range negative":      map[string]any{"bend_range": -1.0},
		"bad voice_mode":           map[string]any{"voice_mode": "duophonic"},
		"amp_env attack high":      map[string]any{"amp_env": map[string]any{"attack": 11.0}},
		"amp_env sustain high":     map[string]any{"amp_env": map[string]any{"sustain": 1.5}},
		"vel_routing to_amp high":  map[string]any{"vel_routing": map[string]any{"to_amp": 1.5}},
		"vel_routing to_cutoff <0": map[string]any{"vel_routing": map[string]any{"to_cutoff": -0.1}},
		"bad lfo wave":             map[string]any{"lfo": map[string]any{"wave": "sine"}},
		"lfo rate low":             map[string]any{"lfo": map[string]any{"rate_hz": 0.01}},
		"lfo rate high":            map[string]any{"lfo": map[string]any{"rate_hz": 21.0}},
		"lfo pitch high":           map[string]any{"lfo": map[string]any{"to_pitch_cents": 101.0}},
		"lfo cutoff oct high":      map[string]any{"lfo": map[string]any{"to_cutoff_oct": 2.5}},
		"lfo amp high":             map[string]any{"lfo": map[string]any{"to_amp": 1.5}},
	} {
		t.Run(name, func(t *testing.T) {
			wantStatus(t, f.do(t, "PATCH", "/api/synth", body), http.StatusBadRequest)
		})
	}
	// Nothing may have leaked into the engine or the cache.
	if v := f.audio.get("resonance"); v != resBase {
		t.Errorf("audio resonance must be untouched by rejected patches, got %v (baseline %v)", v, resBase)
	}
	if n := f.audio.getOscCalls() - oscBase; n != 0 {
		t.Errorf("expected 0 osc applies after rejected patches, got %d", n)
	}
	if got := f.ctrl.Synth().Resonance; !approxEq(float64(got), 0.3) {
		t.Errorf("cache resonance must keep default 0.3, got %v", got)
	}
}

func TestSynthPatchEnvTimeZeroFloorsNotRejects(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	// A slider at 0 s is valid on the wire; controls floors it at 0.0001 s.
	rec := f.do(t, "PATCH", "/api/synth", map[string]any{"filter_env": map[string]any{"attack": 0.0}})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	fe := m["filter_env"].(map[string]any)
	if v := fe["attack"].(float64); !approxEq(v, 0.0001) {
		t.Errorf("filter_env.attack: expected floor 0.0001, got %v", v)
	}
}

// TestSynthPatchPhase34Fields drives the smoke-test body from the task
// sheet through the handler: voice_mode + a partial lfo + drive in one
// PATCH, expecting the full merged snapshot back.
func TestSynthPatchPhase34Fields(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}

	rec := f.do(t, "PATCH", "/api/synth", map[string]any{
		"voice_mode": "poly",
		"lfo":        map[string]any{"to_pitch_cents": 15.0},
		"drive":      0.4,
	})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if m["voice_mode"] != "poly" {
		t.Errorf("voice_mode: expected poly, got %v", m["voice_mode"])
	}
	if v := m["drive"].(float64); !approxEq(v, 0.4) {
		t.Errorf("drive: expected 0.4, got %v", v)
	}
	lfo := m["lfo"].(map[string]any)
	if v := lfo["to_pitch_cents"].(float64); !approxEq(v, 15) {
		t.Errorf("lfo.to_pitch_cents: expected 15, got %v", v)
	}
	// The untouched lfo siblings keep their engine defaults.
	if lfo["wave"] != "triangle" || !approxEq(lfo["rate_hz"].(float64), 5) {
		t.Errorf("lfo: expected triangle/5 Hz preserved, got %v", lfo)
	}
	// The rest of the snapshot is untouched defaults.
	ae := m["amp_env"].(map[string]any)
	if !approxEq(ae["decay"].(float64), 0.2) || !approxEq(ae["sustain"].(float64), 0.7) {
		t.Errorf("amp_env: expected defaults 0.2/0.7, got %v", ae)
	}
	vr := m["vel_routing"].(map[string]any)
	if !approxEq(vr["to_amp"].(float64), 1) {
		t.Errorf("vel_routing.to_amp: expected default 1, got %v", vr)
	}
	if v := m["bend_range"].(float64); !approxEq(v, 2) {
		t.Errorf("bend_range: expected default 2, got %v", v)
	}
	if m["oversample"] != false {
		t.Errorf("oversample: expected default false, got %v", m["oversample"])
	}
	// The engine was actually driven.
	if got := f.audio.getVoiceMode(); got != "poly" {
		t.Errorf("audio voice mode: expected poly, got %q", got)
	}
	if v := f.audio.get("drive"); !approxEq(float64(v), 0.4) {
		t.Errorf("audio drive: expected 0.4, got %v", v)
	}
	if v := f.audio.get("lfoToPitchCents"); !approxEq(float64(v), 15) {
		t.Errorf("audio lfo pitch depth: expected 15, got %v", v)
	}
}

func TestSynthPatchPhase34MergeSemantics(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}

	// Partial amp_env: attack only, then sustain only — attack survives.
	rec := f.do(t, "PATCH", "/api/synth", map[string]any{"amp_env": map[string]any{"attack": 0.05}})
	wantStatus(t, rec, http.StatusOK)
	rec = f.do(t, "PATCH", "/api/synth", map[string]any{"amp_env": map[string]any{"sustain": 0.5}})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	ae := m["amp_env"].(map[string]any)
	if v := ae["attack"].(float64); !approxEq(v, 0.05) {
		t.Errorf("amp_env.attack: expected 0.05 to survive sustain-only patch, got %v", v)
	}
	if v := ae["sustain"].(float64); !approxEq(v, 0.5) {
		t.Errorf("amp_env.sustain: expected 0.5, got %v", v)
	}
	if v := f.audio.get("aeAttack"); !approxEq(float64(v), 0.05) {
		t.Errorf("audio amp env attack: expected 0.05, got %v", v)
	}

	// Partial vel_routing: to_cutoff only — to_amp keeps its default 1.
	rec = f.do(t, "PATCH", "/api/synth", map[string]any{"vel_routing": map[string]any{"to_cutoff": 0.5}})
	wantStatus(t, rec, http.StatusOK)
	m = decodeBody(t, rec)
	vr := m["vel_routing"].(map[string]any)
	if !approxEq(vr["to_cutoff"].(float64), 0.5) || !approxEq(vr["to_amp"].(float64), 1) {
		t.Errorf("vel_routing: expected (0.5, 1), got %v", vr)
	}
	if v := f.audio.get("velToAmp"); !approxEq(float64(v), 1) {
		t.Errorf("audio vel to_amp: expected 1, got %v", v)
	}

	// Oversample toggles round trip.
	rec = f.do(t, "PATCH", "/api/synth", map[string]any{"oversample": true})
	wantStatus(t, rec, http.StatusOK)
	if m = decodeBody(t, rec); m["oversample"] != true {
		t.Errorf("oversample: expected true, got %v", m["oversample"])
	}
	if !f.audio.getOversample() {
		t.Error("audio oversample: expected on")
	}
	rec = f.do(t, "PATCH", "/api/synth", map[string]any{"oversample": false})
	wantStatus(t, rec, http.StatusOK)
	if m = decodeBody(t, rec); m["oversample"] != false {
		t.Errorf("oversample: expected false, got %v", m["oversample"])
	}
	if f.audio.getOversample() {
		t.Error("audio oversample: expected off")
	}

	// A slider at 0 s amp-env time floors (not rejects), like filter_env.
	rec = f.do(t, "PATCH", "/api/synth", map[string]any{"amp_env": map[string]any{"release": 0.0}})
	wantStatus(t, rec, http.StatusOK)
	m = decodeBody(t, rec)
	ae = m["amp_env"].(map[string]any)
	if v := ae["release"].(float64); !approxEq(v, 0.0001) {
		t.Errorf("amp_env.release: expected floor 0.0001, got %v", v)
	}
}

func TestStatusIncludesPhase34Synth(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	if _, err := f.ctrl.SetSynthKbdTrack(0.6); err != nil {
		t.Fatalf("SetSynthKbdTrack: %v", err)
	}
	if _, err := f.ctrl.SetSynthLFO("sh", 2.5, 10, 0.5, 0.2); err != nil {
		t.Fatalf("SetSynthLFO: %v", err)
	}

	rec := f.do(t, "GET", "/api/status", nil)
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	sy := m["params"].(map[string]any)["synth"].(map[string]any)
	if v := sy["kbd_track"].(float64); !approxEq(v, 0.6) {
		t.Errorf("synth.kbd_track: expected 0.6, got %v", v)
	}
	lfo := sy["lfo"].(map[string]any)
	if lfo["wave"] != "sh" || !approxEq(lfo["rate_hz"].(float64), 2.5) {
		t.Errorf("synth.lfo: expected sh/2.5, got %v", lfo)
	}
	ae := sy["amp_env"].(map[string]any)
	if !approxEq(ae["attack"].(float64), 0.005) || !approxEq(ae["release"].(float64), 0.4) {
		t.Errorf("synth.amp_env: expected engine defaults, got %v", ae)
	}
	if v := sy["pulse_width"].(float64); !approxEq(v, 0.25) {
		t.Errorf("synth.pulse_width: expected default 0.25, got %v", v)
	}
	if sy["voice_mode"] != "mono_legato" {
		t.Errorf("synth.voice_mode: expected mono_legato, got %v", sy["voice_mode"])
	}
	if sy["oversample"] != false {
		t.Errorf("synth.oversample: expected false, got %v", sy["oversample"])
	}
	vr := sy["vel_routing"].(map[string]any)
	if !approxEq(vr["to_amp"].(float64), 1) {
		t.Errorf("synth.vel_routing.to_amp: expected default 1, got %v", vr)
	}
	if v := sy["bend_range"].(float64); !approxEq(v, 2) {
		t.Errorf("synth.bend_range: expected default 2, got %v", v)
	}
}

// doRaw is do without any t.Fatalf path, so it is safe to call from
// non-test goroutines (concurrency tests report via t.Errorf).
func (f *fixture) doRaw(method, path, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	rec := httptest.NewRecorder()
	f.srv.Handler().ServeHTTP(rec, req)
	return rec
}

// TestSynthPatchConcurrentPartialUpdates is the lost-update regression:
// PATCH /api/synth's partial-body merge happens inside the controls
// layer under its writer lock, so two concurrent PATCHes to different
// fields must BOTH survive. Pre-fix, the handler merged over a Synth()
// snapshot read once, and the second writer resurrected the first's old
// values. Run with -race.
func TestSynthPatchConcurrentPartialUpdates(t *testing.T) {
	const rounds = 50

	patchPair := func(t *testing.T, f *fixture, bodyA, bodyB string) {
		t.Helper()
		var wg sync.WaitGroup
		for _, body := range []string{bodyA, bodyB} {
			wg.Add(1)
			go func(body string) {
				defer wg.Done()
				if rec := f.doRaw("PATCH", "/api/synth", body); rec.Code != http.StatusOK {
					t.Errorf("PATCH %s: status %d (body: %s)", body, rec.Code, rec.Body.String())
				}
			}(body)
		}
		wg.Wait()
	}

	t.Run("filter_env fields", func(t *testing.T) {
		f := newFixture(t, nil)
		if err := f.ctrl.SelectPatch("moog"); err != nil {
			t.Fatalf("SelectPatch: %v", err)
		}
		for r := 0; r < rounds; r++ {
			attack := 0.01 + float64(r)*0.01
			decay := 0.5 + float64(r)*0.01
			patchPair(t, f,
				fmt.Sprintf(`{"filter_env":{"attack":%g}}`, attack),
				fmt.Sprintf(`{"filter_env":{"decay":%g}}`, decay))
			syn := f.ctrl.Synth()
			if !approxEq(float64(syn.FilterEnv.Attack), attack) || !approxEq(float64(syn.FilterEnv.Decay), decay) {
				t.Fatalf("round %d: lost update: attack=%v (want %v) decay=%v (want %v)",
					r, syn.FilterEnv.Attack, attack, syn.FilterEnv.Decay, decay)
			}
		}
	})

	t.Run("osc fields", func(t *testing.T) {
		f := newFixture(t, nil)
		if err := f.ctrl.SelectPatch("moog"); err != nil {
			t.Fatalf("SelectPatch: %v", err)
		}
		for r := 0; r < rounds; r++ {
			level := 0.1 + float64(r)*0.01
			detune := float64(1 + r)
			patchPair(t, f,
				fmt.Sprintf(`{"osc":[{"index":1,"level":%g}]}`, level),
				fmt.Sprintf(`{"osc":[{"index":1,"detune_cents":%g}]}`, detune))
			o := f.ctrl.Synth().Oscs[1]
			if !approxEq(float64(o.Level), level) || !approxEq(float64(o.DetuneCents), detune) {
				t.Fatalf("round %d: lost update: level=%v (want %v) detune=%v (want %v)",
					r, o.Level, level, o.DetuneCents, detune)
			}
			if o.Wave != "saw" {
				t.Fatalf("round %d: osc wave clobbered: %q", r, o.Wave)
			}
		}
	})
}

func TestStatusIncludesSynth(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	if _, err := f.ctrl.SetSynthResonance(0.9); err != nil {
		t.Fatalf("SetSynthResonance: %v", err)
	}

	rec := f.do(t, "GET", "/api/status", nil)
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	params := m["params"].(map[string]any)
	sy, ok := params["synth"].(map[string]any)
	if !ok {
		t.Fatalf("params.synth: expected object, got %T", params["synth"])
	}
	if v := sy["resonance"].(float64); !approxEq(v, 0.9) {
		t.Errorf("synth.resonance: expected 0.9, got %v", v)
	}
	fe := sy["filter_env"].(map[string]any)
	if v := fe["release"].(float64); !approxEq(v, 0.6) {
		t.Errorf("synth.filter_env.release: expected 0.6, got %v", v)
	}
	o2 := synthOsc(t, sy, 2)
	if o2["octave"].(float64) != -1 || !approxEq(o2["detune_cents"].(float64), 5) {
		t.Errorf("synth.osc[2]: expected -1oct/+5c defaults, got %v", o2)
	}
}

// ---- mastering -----------------------------------------------------------

func TestMasteringPatch(t *testing.T) {
	f := newFixture(t, nil)

	rec := f.do(t, "PATCH", "/api/mastering", map[string]any{
		"comp_amount":        0.6,
		"limiter_ceiling_db": -1.0,
	})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if v := m["comp_amount"].(float64); !approxEq(v, 0.6) {
		t.Errorf("comp_amount: expected 0.6, got %v", v)
	}
	if v := m["limiter_ceiling_db"].(float64); !approxEq(v, -1.0) {
		t.Errorf("limiter_ceiling_db: expected -1.0, got %v", v)
	}
	if v := f.audio.get("masteringComp"); !approxEq(float64(v), 0.6) {
		t.Errorf("audio mastering comp: expected 0.6, got %v", v)
	}
	if v := f.audio.get("limiterCeilingDB"); !approxEq(float64(v), -1.0) {
		t.Errorf("audio limiter ceiling: expected -1.0, got %v", v)
	}

	// Partial update: only the ceiling; the comp value must survive.
	rec = f.do(t, "PATCH", "/api/mastering", map[string]any{"limiter_ceiling_db": -0.3})
	wantStatus(t, rec, http.StatusOK)
	m = decodeBody(t, rec)
	if v := m["comp_amount"].(float64); !approxEq(v, 0.6) {
		t.Errorf("comp_amount after partial: expected 0.6, got %v", v)
	}
	if v := m["limiter_ceiling_db"].(float64); !approxEq(v, -0.3) {
		t.Errorf("limiter_ceiling_db after partial: expected -0.3, got %v", v)
	}

	rec = f.do(t, "PATCH", "/api/mastering", `{"comp_amount": bad}`)
	wantStatus(t, rec, http.StatusBadRequest)
}

// TestMasteringPatchClamps: out-of-range values clamp to the engine's
// ranges (comp [0,1], ceiling [-12,0] dB) in the controls layer, so the
// response and the cache report what the engine actually applied.
func TestMasteringPatchClamps(t *testing.T) {
	f := newFixture(t, nil)

	rec := f.do(t, "PATCH", "/api/mastering", map[string]any{
		"comp_amount":        2.5,
		"limiter_ceiling_db": -30.0,
	})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if v := m["comp_amount"].(float64); !approxEq(v, 1.0) {
		t.Errorf("comp_amount: expected clamp to 1.0, got %v", v)
	}
	if v := m["limiter_ceiling_db"].(float64); !approxEq(v, -12.0) {
		t.Errorf("limiter_ceiling_db: expected clamp to -12.0, got %v", v)
	}
	if v := f.audio.get("masteringComp"); !approxEq(float64(v), 1.0) {
		t.Errorf("audio mastering comp: expected 1.0, got %v", v)
	}
	if v := f.audio.get("limiterCeilingDB"); !approxEq(float64(v), -12.0) {
		t.Errorf("audio limiter ceiling: expected -12.0, got %v", v)
	}

	rec = f.do(t, "PATCH", "/api/mastering", map[string]any{
		"comp_amount":        -0.4,
		"limiter_ceiling_db": 3.0,
	})
	wantStatus(t, rec, http.StatusOK)
	m = decodeBody(t, rec)
	if v := m["comp_amount"].(float64); v != 0 {
		t.Errorf("comp_amount: expected clamp to 0, got %v", v)
	}
	if v := m["limiter_ceiling_db"].(float64); v != 0 {
		t.Errorf("limiter_ceiling_db: expected clamp to 0, got %v", v)
	}
}

// ---- config ----------------------------------------------------------------

func TestConfigVerbatim(t *testing.T) {
	raw := []byte("# polyclav.toml\n[osc.xr18]\nhost = \"192.168.1.50\"\n")
	f := newFixture(t, func(d *Deps) {
		d.ConfigTOML = func() ([]byte, error) { return raw, nil }
	})
	rec := f.do(t, "GET", "/api/config", nil)
	wantStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/plain") {
		t.Errorf("content-type: expected text/plain, got %q", ct)
	}
	if !bytes.Equal(rec.Body.Bytes(), raw) {
		t.Errorf("body not verbatim:\nwant %q\ngot  %q", raw, rec.Body.Bytes())
	}
}

func TestConfigNilSource(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "GET", "/api/config", nil)
	wantStatus(t, rec, http.StatusNotFound)
}

func TestConfigReadError(t *testing.T) {
	f := newFixture(t, func(d *Deps) {
		d.ConfigTOML = func() ([]byte, error) { return nil, errors.New("disk gone") }
	})
	rec := f.do(t, "GET", "/api/config", nil)
	wantStatus(t, rec, http.StatusInternalServerError)
}

// ---- clips + player ----------------------------------------------------------

func TestClips(t *testing.T) {
	f := newFixture(t, func(d *Deps) { d.Player = newTestPlayer(t) })
	rec := f.do(t, "GET", "/api/clips", nil)
	wantStatus(t, rec, http.StatusOK)
	var clips []map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &clips); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(clips) == 0 {
		t.Fatal("expected a non-empty clip library")
	}
	byID := map[string]map[string]any{}
	for _, c := range clips {
		byID[c["id"].(string)] = c
	}
	sc, ok := byID["sustain-chord"]
	if !ok {
		t.Fatalf("expected sustain-chord in clip list, got %v", byID)
	}
	if sc["poly_only"] != true {
		t.Errorf("sustain-chord must be poly_only, got %v", sc)
	}
	if arp, ok := byID["arp"]; !ok || arp["poly_only"] != false {
		t.Errorf("expected arp with poly_only=false, got %v", byID["arp"])
	}
}

func TestClipsNilPlayer(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "GET", "/api/clips", nil)
	wantStatus(t, rec, http.StatusServiceUnavailable)
}

func TestPlayerPlayStop(t *testing.T) {
	f := newFixture(t, func(d *Deps) { d.Player = newTestPlayer(t) })

	rec := f.do(t, "POST", "/api/player", map[string]any{"clip": "arp", "loop": true, "tempo": 2.0})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if m["playing"] != true || m["clip"] != "arp" || m["loop"] != true || m["tempo"].(float64) != 2.0 {
		t.Errorf("play state: unexpected %v", m)
	}

	rec = f.do(t, "POST", "/api/player/tempo", map[string]any{"tempo": 1.5})
	wantStatus(t, rec, http.StatusOK)
	m = decodeBody(t, rec)
	if m["tempo"].(float64) != 1.5 || m["playing"] != true {
		t.Errorf("tempo state: unexpected %v", m)
	}

	rec = f.do(t, "POST", "/api/player/stop", nil)
	wantStatus(t, rec, http.StatusOK)
	m = decodeBody(t, rec)
	if m["playing"] != false {
		t.Errorf("stop state: expected playing=false, got %v", m)
	}
	if m["clip"] != "arp" { // player retains the last clip after stop
		t.Errorf("stop state: expected clip=arp retained, got %v", m)
	}
}

func TestPlayerPlayUnknownClip(t *testing.T) {
	f := newFixture(t, func(d *Deps) { d.Player = newTestPlayer(t) })
	rec := f.do(t, "POST", "/api/player", map[string]any{"clip": "nope", "loop": false, "tempo": 1.0})
	wantStatus(t, rec, http.StatusNotFound)
}

func TestPlayerBadJSON(t *testing.T) {
	f := newFixture(t, func(d *Deps) { d.Player = newTestPlayer(t) })
	wantStatus(t, f.do(t, "POST", "/api/player", `{"clip":`), http.StatusBadRequest)
	wantStatus(t, f.do(t, "POST", "/api/player/tempo", `{`), http.StatusBadRequest)
}

func TestPlayerEndpointsNilPlayer(t *testing.T) {
	f := newFixture(t, nil)
	for _, tc := range []struct{ method, path string }{
		{"POST", "/api/player"},
		{"POST", "/api/player/stop"},
		{"POST", "/api/player/tempo"},
	} {
		rec := f.do(t, tc.method, tc.path, map[string]any{"clip": "arp", "tempo": 1.0})
		wantStatus(t, rec, http.StatusServiceUnavailable)
	}
}

// ---- static page + lifecycle ------------------------------------------------

// The Next.js export is committed under static/app/, so GET / redirects
// into it (static.go); the interim page stays reachable at /legacy.
func TestIndexRedirectsToApp(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "GET", "/", nil)
	wantStatus(t, rec, http.StatusFound)
	if loc := rec.Header().Get("Location"); loc != "/app/" {
		t.Errorf("location: expected /app/, got %q", loc)
	}
}

func TestLegacyServesInterimPage(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "GET", "/legacy", nil)
	wantStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type: expected text/html, got %q", ct)
	}
	body := rec.Body.String()
	for _, marker := range []string{"polyclav", "/api/events", "patch-grid", "vel-canvas", "/api/velocity", "config-text", "data-clip"} {
		if !strings.Contains(body, marker) {
			t.Errorf("index.html missing %q", marker)
		}
	}
}

func TestAppServed(t *testing.T) {
	f := newFixture(t, nil)

	rec := f.do(t, "GET", "/app/", nil)
	wantStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type: expected text/html, got %q", ct)
	}
	body := rec.Body.String()
	for _, marker := range []string{"polyclav", "/app/_next/static/"} {
		if !strings.Contains(body, marker) {
			t.Errorf("app index.html missing %q", marker)
		}
	}

	// A hashed asset referenced by the page must be served with a JS
	// content type and immutable caching. Pull the first quoted
	// /app/_next/... URL that ends in .js out of the HTML.
	var asset string
	rest := body
	for {
		i := strings.Index(rest, "/app/_next/static/")
		if i < 0 {
			t.Fatal("no /app/_next/static/ asset URL found in app index.html")
		}
		rest = rest[i:]
		j := strings.IndexByte(rest, '"')
		if j < 0 {
			t.Fatal("unterminated asset URL in app index.html")
		}
		if strings.HasSuffix(rest[:j], ".js") {
			asset = rest[:j]
			break
		}
		rest = rest[j:]
	}
	rec = f.do(t, "GET", asset, nil)
	wantStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/javascript") {
		t.Errorf("asset content-type: expected text/javascript, got %q", ct)
	}
	if cc := rec.Header().Get("Cache-Control"); !strings.Contains(cc, "immutable") {
		t.Errorf("asset cache-control: expected immutable, got %q", cc)
	}

	// Unknown app paths land on the export's 404 page.
	rec = f.do(t, "GET", "/app/nope/", nil)
	wantStatus(t, rec, http.StatusNotFound)
}

func TestUnknownPathIs404(t *testing.T) {
	f := newFixture(t, nil)
	wantStatus(t, f.do(t, "GET", "/nope", nil), http.StatusNotFound)
}

func TestServeGracefulShutdown(t *testing.T) {
	f := newFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	errc := make(chan error, 1)
	go func() { errc <- f.srv.Serve(ctx, "127.0.0.1:0") }()

	// Give ListenAndServe a moment to bind, then trigger shutdown.
	time.Sleep(50 * time.Millisecond)
	cancel()

	select {
	case err := <-errc:
		if err != nil {
			t.Fatalf("Serve returned error on graceful shutdown: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return after ctx cancel")
	}
}

func TestServeBadListenAddr(t *testing.T) {
	f := newFixture(t, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	errc := make(chan error, 1)
	go func() { errc <- f.srv.Serve(ctx, "definitely:not:an:addr") }()
	select {
	case err := <-errc:
		if err == nil {
			t.Fatal("expected a listen error")
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Serve did not return a listen error")
	}
}

// ---- chain (post-synth pedal chain) --------------------------------------

func TestChainGetShape(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("salamander"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	rec := f.do(t, "GET", "/api/chain", nil)
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)

	if m["patch"] != "salamander" {
		t.Errorf("patch: expected salamander, got %v", m["patch"])
	}
	order, ok := m["order"].([]any)
	if !ok || len(order) != 4 {
		t.Fatalf("order: expected 4 stage ids, got %v", m["order"])
	}
	if order[0] != "drive" || order[3] != "delay" {
		t.Errorf("order: unexpected %v", order)
	}
	stages, ok := m["stages"].([]any)
	if !ok || len(stages) != 4 {
		t.Fatalf("stages: expected 4, got %v", m["stages"])
	}

	drive := stages[0].(map[string]any)
	if drive["id"] != "drive" || drive["kind"] != "drive" || drive["enabled"] != true {
		t.Errorf("stages[0] (drive): unexpected %v", drive)
	}
	dp := drive["params"].([]any)
	if len(dp) != 1 {
		t.Fatalf("drive params: expected 1, got %v", dp)
	}
	amount := dp[0].(map[string]any)
	if amount["id"] != "drive.amount" || amount["gate"] != true || amount["taper"] != "linear" {
		t.Errorf("drive.amount: unexpected %v", amount)
	}

	// chorus.rate_hz: exp taper, range 0.02..5, value at the 0.8 default.
	chorus := stages[1].(map[string]any)
	rate := chorus["params"].([]any)[0].(map[string]any)
	if rate["id"] != "chorus.rate_hz" || rate["taper"] != "exp" || rate["unit"] != "Hz" {
		t.Errorf("chorus.rate_hz: unexpected %v", rate)
	}
	if v := rate["value"].(float64); !approxEq(v, 0.8) {
		t.Errorf("chorus.rate_hz value: expected 0.8 default, got %v", v)
	}
	if v := rate["max"].(float64); !approxEq(v, 5) {
		t.Errorf("chorus.rate_hz max: expected 5, got %v", v)
	}
}

func TestChainPatchNumberAndEnable(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("salamander"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}

	rec := f.do(t, "PATCH", "/api/chain", map[string]any{
		"chorus.rate_hz": 100, // clamps to the 5 max
		"tremolo.depth":  0.5, // gate param, tremolo still enabled
		"chorus.enabled": false,
	})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if _, present := m["errors"]; present {
		t.Fatalf("expected no errors, got %v", m["errors"])
	}
	applied := m["applied"].(map[string]any)
	if v := applied["chorus.rate_hz"].(float64); !approxEq(v, 5) {
		t.Errorf("applied chorus.rate_hz: expected clamp to 5, got %v", v)
	}
	if v := applied["tremolo.depth"].(float64); !approxEq(v, 0.5) {
		t.Errorf("applied tremolo.depth: expected 0.5, got %v", v)
	}
	if applied["chorus.enabled"] != false {
		t.Errorf("applied chorus.enabled: expected false, got %v", applied["chorus.enabled"])
	}
	if v := f.audio.get("chorusRateHz"); !approxEq(float64(v), 5) {
		t.Errorf("audio chorusRateHz: expected 5, got %v", v)
	}
	if v := f.audio.get("tremoloDepth"); !approxEq(float64(v), 0.5) {
		t.Errorf("audio tremoloDepth: expected 0.5, got %v", v)
	}
	// chorus.mix (chorus's gate param) parked at 0 by the disable.
	if v := f.audio.get("chorusMix"); v != 0 {
		t.Errorf("audio chorusMix: expected 0 after disable, got %v", v)
	}
	if k := f.st.PatchKnob("salamander"); k.ChorusEnabled {
		t.Errorf("state chorus_enabled: expected false after disable")
	}
}

func TestChainPatchUnknownField(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("salamander"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	rec := f.do(t, "PATCH", "/api/chain", map[string]any{"bogus.knob": 0.5})
	wantStatus(t, rec, http.StatusOK) // per-field error, not a request failure
	m := decodeBody(t, rec)
	errs, ok := m["errors"].(map[string]any)
	if !ok {
		t.Fatalf("expected errors map, got %v", m)
	}
	if _, present := errs["bogus.knob"]; !present {
		t.Errorf("expected bogus.knob field error, got %v", errs)
	}
	if applied := m["applied"].(map[string]any); len(applied) != 0 {
		t.Errorf("expected nothing applied, got %v", applied)
	}
}

func TestChainPatchNoCurrentPatch(t *testing.T) {
	f := newFixture(t, nil) // nothing selected
	rec := f.do(t, "PATCH", "/api/chain", map[string]any{"chorus.rate_hz": 1})
	wantStatus(t, rec, http.StatusConflict)
	if v := f.audio.get("chorusRateHz"); v != 0 {
		t.Errorf("audio must not be touched, got chorusRateHz %v", v)
	}
}

func TestChainPatchOrderAlone(t *testing.T) {
	f := newFixture(t, nil) // nothing selected — order is a global, no-patch-required key
	rec := f.do(t, "PATCH", "/api/chain", map[string]any{
		"order": []string{"delay", "tremolo", "chorus", "drive"},
	})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	applied := m["applied"].(map[string]any)
	ord, ok := applied["order"].([]any)
	if !ok || len(ord) != 4 || ord[0] != "delay" || ord[3] != "drive" {
		t.Errorf("applied order: unexpected %v", applied["order"])
	}
	// GET reflects the new global order.
	rec = f.do(t, "GET", "/api/chain", nil)
	got := decodeBody(t, rec)["order"].([]any)
	if got[0] != "delay" || got[3] != "drive" {
		t.Errorf("GET order after reorder: unexpected %v", got)
	}
}

func TestSSEChainFrame(t *testing.T) {
	f := newFixture(t, nil)
	if err := f.ctrl.SelectPatch("salamander"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()

	sc, cancel := openSSE(t, ts, 10*time.Second)
	defer cancel()
	if ev := readSSEEvent(t, sc); ev.name != "snapshot" {
		t.Fatalf("expected snapshot, got %q", ev.name)
	}

	// A chain PATCH surfaces as an event: chain frame (the SSE layer
	// forwards any Change.Type verbatim).
	rec := f.do(t, "PATCH", "/api/chain", map[string]any{"delay.mix": 0.6})
	wantStatus(t, rec, http.StatusOK)

	ev := readSSEEvent(t, sc)
	if ev.name != "chain" {
		t.Fatalf("expected chain event, got %q", ev.name)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(ev.data), &data); err != nil {
		t.Fatalf("chain data: %v", err)
	}
	if data["field"] != "delay.mix" || !approxEq(data["value"].(float64), 0.6) {
		t.Errorf("chain data: unexpected %v", data)
	}
}
