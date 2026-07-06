package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/config"
	"github.com/mschulkind-oss/polyclav/internal/controls"
	"github.com/mschulkind-oss/polyclav/internal/velocity"
)

// newConfigFixture is newFixture plus a real polyclav.toml on disk in a
// temp dir, wired through both ConfigPath (write path) and ConfigTOML
// (read path) like main does.
func newConfigFixture(t *testing.T, content string) (*fixture, string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "polyclav.toml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	f := newFixture(t, func(d *Deps) {
		d.ConfigPath = path
		d.ConfigTOML = func() ([]byte, error) { return os.ReadFile(path) }
	})
	return f, path
}

// assertNoTempLitter fails if anything but the config file itself is left
// in the config directory — the atomicity contract for both save paths.
func assertNoTempLitter(t *testing.T, path string) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(path))
	if err != nil {
		t.Fatalf("read config dir: %v", err)
	}
	for _, e := range entries {
		if e.Name() != filepath.Base(path) {
			t.Errorf("unexpected file left in config dir: %s", e.Name())
		}
	}
}

func readConfigFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read config: %v", err)
	}
	return string(b)
}

// ---- PUT /api/config -------------------------------------------------------

const baseConfig = "[web]\nenabled = false\n"

func TestConfigPutRoundTrip(t *testing.T) {
	f, path := newConfigFixture(t, baseConfig)
	newText := baseConfig + "\n# saved from the web UI\n[midi.velocity]\ncurve = \"soft\"\n"

	rec := f.do(t, "PUT", "/api/config", newText)
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if m["saved"] != true || m["restart_required"] != true {
		t.Errorf("response: expected saved+restart_required, got %v", m)
	}

	if got := readConfigFile(t, path); got != newText {
		t.Errorf("file after save:\nwant %q\ngot  %q", newText, got)
	}
	// The saved file reloads through the same startup path.
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("reload saved config: %v", err)
	}
	if cfg.MIDI.Velocity.Curve != "soft" {
		t.Errorf("reloaded velocity curve: expected soft, got %q", cfg.MIDI.Velocity.Curve)
	}
	// GET serves the new text.
	rec = f.do(t, "GET", "/api/config", nil)
	wantStatus(t, rec, http.StatusOK)
	if rec.Body.String() != newText {
		t.Errorf("GET after save: got %q", rec.Body.String())
	}
	assertNoTempLitter(t, path)
}

func TestConfigPutInvalidTOML(t *testing.T) {
	f, path := newConfigFixture(t, baseConfig)
	rec := f.do(t, "PUT", "/api/config", "not [valid toml%%%")
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	m := decodeBody(t, rec)
	if msg, _ := m["error"].(string); msg == "" {
		t.Errorf("expected an error message, got %v", m)
	}
	if got := readConfigFile(t, path); got != baseConfig {
		t.Errorf("original file must be untouched after failed save, got %q", got)
	}
	assertNoTempLitter(t, path)
}

func TestConfigPutSchemaViolation(t *testing.T) {
	f, path := newConfigFixture(t, baseConfig)
	rec := f.do(t, "PUT", "/api/config", "[midi.velocity]\ncurve = \"bogus\"\n")
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	m := decodeBody(t, rec)
	msg, _ := m["error"].(string)
	if !strings.Contains(msg, "unknown curve") {
		t.Errorf("expected the startup-style validation message, got %q", msg)
	}
	// The temp file's name must not leak into the user-facing error.
	if strings.Contains(msg, ".polyclav-") {
		t.Errorf("temp file name leaked into error: %q", msg)
	}
	if got := readConfigFile(t, path); got != baseConfig {
		t.Errorf("original file must be untouched, got %q", got)
	}
	assertNoTempLitter(t, path)
}

func TestConfigPutMissingDeps(t *testing.T) {
	f, path := newConfigFixture(t, baseConfig)
	body := "[[patches]]\nname = \"ghost\"\ntype = \"soundfont\"\nsoundfont = \"/nonexistent/ghost.sf2\"\n"
	rec := f.do(t, "PUT", "/api/config", body)
	wantStatus(t, rec, http.StatusUnprocessableEntity)
	m := decodeBody(t, rec)
	msg, _ := m["error"].(string)
	if !strings.Contains(msg, "reference missing files") {
		t.Errorf("expected the missing-deps startup error, got %q", msg)
	}
	if got := readConfigFile(t, path); got != baseConfig {
		t.Errorf("original file must be untouched, got %q", got)
	}
	assertNoTempLitter(t, path)
}

func TestConfigPutNoPath(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "PUT", "/api/config", baseConfig)
	wantStatus(t, rec, http.StatusNotFound)
}

// ---- PUT /api/velocity: live apply ------------------------------------------

func TestVelocityPutAppliesPreset(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "PUT", "/api/velocity", map[string]any{"curve": "soft"})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	const wantLabel = "soft (γ=0.60, out 1..127)"
	if m["curve"] != wantLabel || m["saved"] != false {
		t.Errorf("response: expected %q unsaved, got %v", wantLabel, m)
	}
	// The remap is actually installed at the controls layer.
	if got := f.ctrl.VelocityLabel(); got != wantLabel {
		t.Errorf("VelocityLabel: expected %q, got %q", wantLabel, got)
	}
	// round(127·(64/127)^0.6) = 84 — the curve function, not a stub.
	if got := f.ctrl.ApplyVelocity(64); got != 84 {
		t.Errorf("ApplyVelocity(64): expected 84 on soft, got %d", got)
	}
	if got := f.ctrl.ApplyVelocity(0); got != 0 {
		t.Errorf("ApplyVelocity(0): expected NoteOff passthrough, got %d", got)
	}
}

func TestVelocityPutAppliesGammaShorthand(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "PUT", "/api/velocity", map[string]any{"gamma": 2.0, "out_min": 10, "out_max": 100})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if m["curve"] != "custom (γ=2.00, out 10..100)" {
		t.Errorf("response curve: got %v", m["curve"])
	}
	if got := f.ctrl.ApplyVelocity(127); got != 100 {
		t.Errorf("ApplyVelocity(127): expected out_max clamp 100, got %d", got)
	}
	if got := f.ctrl.ApplyVelocity(1); got != 10 {
		t.Errorf("ApplyVelocity(1): expected out_min clamp 10, got %d", got)
	}
}

func TestVelocityPutAppliesPoints(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "PUT", "/api/velocity", map[string]any{
		"points": [][]int{{0, 0}, {64, 90}, {127, 127}},
	})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if m["curve"] != "points[3] (out 1..127)" {
		t.Errorf("response curve: got %v", m["curve"])
	}
	for in, want := range map[uint8]uint8{0: 0, 32: 45, 64: 90, 127: 127} {
		if got := f.ctrl.ApplyVelocity(in); got != want {
			t.Errorf("ApplyVelocity(%d): expected %d, got %d", in, want, got)
		}
	}
}

func TestVelocityPutMutualExclusion(t *testing.T) {
	f := newFixture(t, nil)
	for name, body := range map[string]map[string]any{
		"points+curve": {"points": [][]int{{0, 0}, {127, 127}}, "curve": "soft"},
		"points+gamma": {"points": [][]int{{0, 0}, {127, 127}}, "gamma": 1.2},
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.do(t, "PUT", "/api/velocity", body)
			wantStatus(t, rec, http.StatusBadRequest)
		})
	}
	if got := f.ctrl.VelocityLabel(); got != "" {
		t.Errorf("rejected PUTs must not install a curve, got %q", got)
	}
}

func TestVelocityPutValidation(t *testing.T) {
	f := newFixture(t, nil)
	for name, body := range map[string]any{
		"bad JSON":             `{"curve":`,
		"empty body":           map[string]any{},
		"unknown curve":        map[string]any{"curve": "bogus"},
		"custom without gamma": map[string]any{"curve": "custom"},
		"gamma zero":           map[string]any{"gamma": 0.0},
		"gamma negative":       map[string]any{"gamma": -1.0},
		"out_min out of range": map[string]any{"curve": "linear", "out_min": 200},
		"out_max out of range": map[string]any{"curve": "linear", "out_max": -1},
		"out_min > out_max":    map[string]any{"curve": "linear", "out_min": 90, "out_max": 10},
		"one point":            map[string]any{"points": [][]int{{0, 0}}},
		"malformed pair":       map[string]any{"points": [][]int{{0, 0}, {64}, {127, 127}}},
		"point out of range":   map[string]any{"points": [][]int{{0, 0}, {64, 200}, {127, 127}}},
		"non-monotonic points": map[string]any{"points": [][]int{{0, 0}, {64, 90}, {127, 80}}},
		"first point not 0,0":  map[string]any{"points": [][]int{{0, 5}, {127, 127}}},
		"last x not 127":       map[string]any{"points": [][]int{{0, 0}, {100, 100}}},
	} {
		t.Run(name, func(t *testing.T) {
			rec := f.do(t, "PUT", "/api/velocity", body)
			wantStatus(t, rec, http.StatusBadRequest)
		})
	}
	if got := f.ctrl.VelocityLabel(); got != "" {
		t.Errorf("rejected PUTs must not install a curve, got %q", got)
	}
}

// ---- GET /api/velocity -------------------------------------------------------

func TestVelocityGetSource(t *testing.T) {
	f, _ := newConfigFixture(t, baseConfig)

	// Boot state: main installs the config-resolved curve.
	lin := velocity.Linear()
	f.ctrl.SetVelocityRemap(lin.Apply, lin.Describe())

	rec := f.do(t, "GET", "/api/velocity", nil)
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if m["curve"] != lin.Describe() || m["source"] != "config" {
		t.Errorf("boot state: expected config-sourced linear, got %v", m)
	}

	// Session-only PUT flips the source.
	wantStatus(t, f.do(t, "PUT", "/api/velocity", map[string]any{"curve": "hard"}), http.StatusOK)
	m = decodeBody(t, f.do(t, "GET", "/api/velocity", nil))
	if m["curve"] != "hard (γ=1.60, out 1..127)" || m["source"] != "session" {
		t.Errorf("after session PUT: expected session-sourced hard, got %v", m)
	}

	// A patch change re-resolving from config supersedes the session edit.
	f.ctrl.SetVelocityRemap(lin.Apply, lin.Describe())
	m = decodeBody(t, f.do(t, "GET", "/api/velocity", nil))
	if m["source"] != "config" {
		t.Errorf("after config reinstall: expected source config, got %v", m)
	}

	// A saved PUT is config-sourced: it lives in the file now.
	wantStatus(t, f.do(t, "PUT", "/api/velocity", map[string]any{"curve": "soft", "save": true}), http.StatusOK)
	m = decodeBody(t, f.do(t, "GET", "/api/velocity", nil))
	if m["curve"] != "soft (γ=0.60, out 1..127)" || m["source"] != "config" {
		t.Errorf("after saved PUT: expected config-sourced soft, got %v", m)
	}
}

// ---- PUT /api/velocity: save path ---------------------------------------------

func TestVelocitySaveWritesManagedBlock(t *testing.T) {
	f, path := newConfigFixture(t, baseConfig)

	rec := f.do(t, "PUT", "/api/velocity", map[string]any{"curve": "hard", "save": true})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if m["saved"] != true {
		t.Errorf("expected saved=true, got %v", m)
	}
	text := readConfigFile(t, path)
	if !strings.HasPrefix(text, baseConfig) {
		t.Errorf("original content must be preserved, got %q", text)
	}
	if strings.Count(text, velocityBeginMarker) != 1 || strings.Count(text, velocityEndMarker) != 1 {
		t.Fatalf("expected exactly one managed block, got:\n%s", text)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("saved config must survive config.Load: %v", err)
	}
	if cfg.MIDI.Velocity.Curve != "hard" {
		t.Errorf("loaded curve: expected hard, got %q", cfg.MIDI.Velocity.Curve)
	}

	// Re-save with a different shape REPLACES the block (still one fence),
	// and the new curve is live without a restart.
	rec = f.do(t, "PUT", "/api/velocity", map[string]any{
		"points": [][]int{{0, 0}, {64, 90}, {127, 127}}, "out_min": 5, "save": true,
	})
	wantStatus(t, rec, http.StatusOK)
	text = readConfigFile(t, path)
	if strings.Count(text, velocityBeginMarker) != 1 || strings.Count(text, velocityEndMarker) != 1 {
		t.Fatalf("expected the managed block to be replaced, got:\n%s", text)
	}
	if strings.Contains(text, `curve = "hard"`) {
		t.Errorf("stale curve key left behind:\n%s", text)
	}
	cfg, err = config.Load(path)
	if err != nil {
		t.Fatalf("re-saved config must survive config.Load: %v", err)
	}
	if len(cfg.MIDI.Velocity.Points) != 3 || cfg.MIDI.Velocity.OutMin != 5 || cfg.MIDI.Velocity.Curve != "" {
		t.Errorf("loaded velocity block: unexpected %+v", cfg.MIDI.Velocity)
	}
	if got := f.ctrl.VelocityLabel(); got != "points[3] (out 5..127)" {
		t.Errorf("live curve after save: expected points[3] (out 5..127), got %q", got)
	}
	assertNoTempLitter(t, path)
}

func TestVelocitySaveCustomGamma(t *testing.T) {
	f, path := newConfigFixture(t, baseConfig)
	rec := f.do(t, "PUT", "/api/velocity", map[string]any{"curve": "custom", "gamma": 1.25, "save": true})
	wantStatus(t, rec, http.StatusOK)
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("saved config must survive config.Load: %v", err)
	}
	if cfg.MIDI.Velocity.Curve != "custom" || !approxEq(float64(cfg.MIDI.Velocity.Gamma), 1.25) {
		t.Errorf("loaded velocity block: unexpected %+v", cfg.MIDI.Velocity)
	}
}

func TestVelocitySaveRefusesUnmanagedSection(t *testing.T) {
	handWritten := "[midi.velocity]\ncurve = \"soft\"\n"
	f, path := newConfigFixture(t, handWritten)

	rec := f.do(t, "PUT", "/api/velocity", map[string]any{"curve": "hard", "save": true})
	wantStatus(t, rec, http.StatusConflict)
	m := decodeBody(t, rec)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "by hand") {
		t.Errorf("409 message should point at hand-editing, got %q", msg)
	}
	if got := readConfigFile(t, path); got != handWritten {
		t.Errorf("hand-written config must never be rewritten, got %q", got)
	}
	// The refused save must not have applied live either.
	if got := f.ctrl.VelocityLabel(); got != "" {
		t.Errorf("refused save must not install a curve, got %q", got)
	}
	assertNoTempLitter(t, path)
}

func TestVelocitySaveNoConfigPath(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "PUT", "/api/velocity", map[string]any{"curve": "hard", "save": true})
	wantStatus(t, rec, http.StatusNotFound)
	if got := f.ctrl.VelocityLabel(); got != "" {
		t.Errorf("failed save must not install a curve, got %q", got)
	}
}

// ---- managed-block merge unit coverage ----------------------------------------

func TestUpsertManagedVelocityCorruptMarkers(t *testing.T) {
	_, err := upsertManagedVelocity(velocityBeginMarker+"\n[midi.velocity]\ncurve = \"soft\"\n", "block")
	if err == nil || !strings.Contains(err.Error(), "corrupted") {
		t.Errorf("expected corrupt-marker error, got %v", err)
	}
}

func TestUpsertManagedVelocityUnmanagedOutsideFence(t *testing.T) {
	orig := "[midi.velocity]\ncurve = \"soft\"\n\n" +
		velocityBeginMarker + "\n[midi.velocity]\ncurve = \"hard\"\n" + velocityEndMarker + "\n"
	if _, err := upsertManagedVelocity(orig, "block"); err != errUnmanagedVelocity {
		t.Errorf("expected errUnmanagedVelocity for a second, hand-written section, got %v", err)
	}
}

// ---- velocity monitor SSE -------------------------------------------------------

// TestNoteEventSSEDelivery pins the monitor wire shape: a "note" change
// published on the hub (as cmd/polyclav's pushSynth does per NoteOn)
// reaches SSE clients as `event: note` with {in, out, note}.
func TestNoteEventSSEDelivery(t *testing.T) {
	f := newFixture(t, nil)
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()

	sc, cancel := openSSE(t, ts, 10*time.Second)
	defer cancel()
	if ev := readSSEEvent(t, sc); ev.name != "snapshot" {
		t.Fatalf("expected snapshot first, got %q", ev.name)
	}

	f.hub.Publish(controls.Change{Type: "note", Data: map[string]any{
		"in": 100, "out": 64, "note": 60,
	}})
	ev := readSSEEvent(t, sc)
	if ev.name != "note" {
		t.Fatalf("expected note event, got %q", ev.name)
	}
	var d map[string]any
	if err := json.Unmarshal([]byte(ev.data), &d); err != nil {
		t.Fatalf("note data: %v", err)
	}
	if d["in"].(float64) != 100 || d["out"].(float64) != 64 || d["note"].(float64) != 60 {
		t.Errorf("note data: unexpected %v", d)
	}
}
