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
	cutoffHz                        float32
	masteringComp, limiterCeilingDB float32
	resonance, noise, glide         float32
	feAttack, feAmount              float32
	oscCalls                        int
}

func (f *fakeAudio) SetMasterVolume(v float32) { f.mu.Lock(); defer f.mu.Unlock(); f.volume = v }
func (f *fakeAudio) SetReverb(v float32)       { f.mu.Lock(); defer f.mu.Unlock(); f.reverb = v }
func (f *fakeAudio) SetCompressor(v float32)   { f.mu.Lock(); defer f.mu.Unlock(); f.compressor = v }
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

func (f *fakeAudio) getOscCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.oscCalls
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
	currentPatch string
}

func newFakeStore() *fakeStore { return &fakeStore{knobs: map[string]state.Knob{}} }

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
	}
	f.knobs[name] = k
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
	if n := f.audio.getOscCalls(); n != 3 {
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
	} {
		t.Run(name, func(t *testing.T) {
			wantStatus(t, f.do(t, "PATCH", "/api/synth", body), http.StatusBadRequest)
		})
	}
	// Nothing may have leaked into the engine or the cache.
	if v := f.audio.get("resonance"); v != 0 {
		t.Errorf("audio resonance must be untouched by rejected patches, got %v", v)
	}
	if n := f.audio.getOscCalls(); n != 0 {
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

func TestIndexServed(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "GET", "/", nil)
	wantStatus(t, rec, http.StatusOK)
	if ct := rec.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/html") {
		t.Errorf("content-type: expected text/html, got %q", ct)
	}
	body := rec.Body.String()
	for _, marker := range []string{"polyclav", "/api/events", "patch-grid"} {
		if !strings.Contains(body, marker) {
			t.Errorf("index.html missing %q", marker)
		}
	}
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
