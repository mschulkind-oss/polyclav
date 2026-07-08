package config

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writeFile is the bare-minimum helper for these tests; we write
// throwaway "fake sfN" payloads since Validate only stats the path.
func writeFile(t *testing.T, path string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("mkdir %q: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte("fake"), 0o644); err != nil {
		t.Fatalf("write %q: %v", path, err)
	}
}

func TestValidateAllPatchesPresent(t *testing.T) {
	dir := t.TempDir()
	sf := filepath.Join(dir, "p.sf2")
	clap := filepath.Join(dir, "Dexed.clap")
	writeFile(t, sf)
	writeFile(t, clap)

	cfg := &Config{
		Patches: []PatchConfig{
			{Name: "sf", Type: PatchTypeSoundfont, Soundfont: sf},
			{Name: "lv2", Type: PatchTypeLV2, PluginURI: "urn:example:plugin"},
			{Name: "clap", Type: PatchTypeCLAP, PluginPath: clap, PluginID: "id"},
			{Name: "native", Type: PatchTypeNative, Engine: "minimoog"},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateSoundfontMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Patches: []PatchConfig{
			{Name: "gone", Type: PatchTypeSoundfont, Soundfont: filepath.Join(dir, "nope.sf2")},
		},
	}
	err := Validate(cfg)
	if err == nil {
		t.Fatal("expected MissingDepsError, got nil")
	}
	var mde *MissingDepsError
	if !errors.As(err, &mde) {
		t.Fatalf("expected MissingDepsError, got %T: %v", err, err)
	}
	if len(mde.Missing) != 1 {
		t.Fatalf("expected 1 missing, got %d", len(mde.Missing))
	}
	if mde.Missing[0].PatchName != "gone" {
		t.Errorf("unexpected patch name: %q", mde.Missing[0].PatchName)
	}
	if !strings.Contains(mde.Error(), "soundfont file not found") {
		t.Errorf("error text missing 'soundfont file not found': %q", mde.Error())
	}
}

func TestValidateCLAPMissing(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Patches: []PatchConfig{
			{Name: "dexed", Type: PatchTypeCLAP, PluginPath: filepath.Join(dir, "nope.clap"), PluginID: "id"},
		},
	}
	err := Validate(cfg)
	var mde *MissingDepsError
	if !errors.As(err, &mde) {
		t.Fatalf("expected MissingDepsError, got %T: %v", err, err)
	}
	if !strings.Contains(mde.Error(), "CLAP plugin not found") {
		t.Errorf("error text missing 'CLAP plugin not found': %q", mde.Error())
	}
}

func TestValidateNativeUnknownEngine(t *testing.T) {
	cfg := &Config{
		Patches: []PatchConfig{
			{Name: "weird", Type: PatchTypeNative, Engine: "moog-supreme"},
		},
	}
	err := Validate(cfg)
	var mde *MissingDepsError
	if !errors.As(err, &mde) {
		t.Fatalf("expected MissingDepsError, got %T: %v", err, err)
	}
	if !strings.Contains(mde.Error(), `unknown native engine "moog-supreme"`) {
		t.Errorf("error text missing unknown-engine reason: %q", mde.Error())
	}
	if !strings.Contains(mde.Error(), "minimoog") {
		t.Errorf("error text should list known engines (minimoog): %q", mde.Error())
	}
}

func TestValidateCollectsAllFailures(t *testing.T) {
	dir := t.TempDir()
	cfg := &Config{
		Patches: []PatchConfig{
			{Name: "a", Type: PatchTypeSoundfont, Soundfont: filepath.Join(dir, "a.sf2")},
			{Name: "b", Type: PatchTypeSoundfont, Soundfont: filepath.Join(dir, "b.sf2")},
			{Name: "c", Type: PatchTypeCLAP, PluginPath: filepath.Join(dir, "c.clap"), PluginID: "id"},
			{Name: "d", Type: PatchTypeNative, Engine: "ghost"},
		},
	}
	err := Validate(cfg)
	var mde *MissingDepsError
	if !errors.As(err, &mde) {
		t.Fatalf("expected MissingDepsError, got %T", err)
	}
	if len(mde.Missing) != 4 {
		t.Errorf("expected 4 missing, got %d: %v", len(mde.Missing), mde.Missing)
	}
}

func TestValidateLV2NotChecked(t *testing.T) {
	// LV2 URIs are abstract — Validate must not touch the filesystem
	// for them. A patch with a nonsensical URI but no other issues
	// passes here; the host resolves it at instantiation time.
	cfg := &Config{
		Patches: []PatchConfig{
			{Name: "x", Type: PatchTypeLV2, PluginURI: "urn:made-up:thing"},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestValidateNativeMinimoogOK(t *testing.T) {
	// Belt-and-braces for the "don't break native zero-deps story"
	// requirement: a config with ONLY the native minimoog patch must
	// pass without any filesystem dependencies.
	cfg := &Config{
		Patches: []PatchConfig{
			{Name: "moog", Type: PatchTypeNative, Engine: "minimoog"},
		},
	}
	if err := Validate(cfg); err != nil {
		t.Fatalf("Validate (native-only): %v", err)
	}
}

func TestValidateNilCfg(t *testing.T) {
	if err := Validate(nil); err != nil {
		t.Errorf("Validate(nil) should be a no-op, got %v", err)
	}
}

// loadTOML writes body to a throwaway polyclav.toml and runs Load on it.
// Central helper for the decode/validation tests below (velocity, web,
// mixer alias, heartbeat) so each case is just TOML-in, assertions-out.
func loadTOML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "polyclav.toml")
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatalf("write config: %v", err)
	}
	return Load(path)
}

// mustLoadTOML is loadTOML for cases where any error is a test failure.
func mustLoadTOML(t *testing.T, body string) *Config {
	t.Helper()
	cfg, err := loadTOML(t, body)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	return cfg
}

// --- [midi.velocity] -------------------------------------------------------

func TestLoadVelocityDefaultsZeroValue(t *testing.T) {
	cfg := mustLoadTOML(t, "")
	v := cfg.MIDI.Velocity
	if v.Curve != "" || v.Gamma != 0 || v.OutMin != 0 || v.OutMax != 0 {
		t.Errorf("expected zero-value velocity defaults (linear), got %+v", v)
	}
}

func TestLoadVelocityGoodConfigs(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"preset linear", "[midi.velocity]\ncurve = \"linear\"\n"},
		{"preset soft", "[midi.velocity]\ncurve = \"soft\"\n"},
		{"preset hard", "[midi.velocity]\ncurve = \"hard\"\n"},
		{"custom with gamma", "[midi.velocity]\ncurve = \"custom\"\ngamma = 0.8\n"},
		{"out clamps", "[midi.velocity]\ncurve = \"soft\"\nout_min = 1\nout_max = 120\n"},
		{"per-patch preset override",
			"[[patches]]\nname = \"p\"\nsoundfont = \"/tmp/p.sf2\"\nvelocity_curve = \"soft\"\n"},
		{"per-patch custom with gamma",
			"[[patches]]\nname = \"p\"\nsoundfont = \"/tmp/p.sf2\"\nvelocity_curve = \"custom\"\nvelocity_gamma = 0.7\n"},
		{"per-patch gamma alone implies custom",
			"[[patches]]\nname = \"p\"\nsoundfont = \"/tmp/p.sf2\"\nvelocity_gamma = 0.7\n"},
		{"global gamma alone implies custom",
			"[midi.velocity]\ngamma = 0.7\n"},
		{"global points",
			"[midi.velocity]\npoints = [[0, 0], [64, 90], [127, 127]]\n"},
		{"global points minimum pair count",
			"[midi.velocity]\npoints = [[0, 0], [127, 127]]\n"},
		{"global points with clamps",
			"[midi.velocity]\npoints = [[0, 0], [64, 90], [127, 127]]\nout_min = 10\nout_max = 120\n"},
		{"global points flat segment (ys may repeat)",
			"[midi.velocity]\npoints = [[0, 0], [64, 90], [100, 90], [127, 127]]\n"},
		{"per-patch points",
			"[[patches]]\nname = \"p\"\nsoundfont = \"/tmp/p.sf2\"\nvelocity_points = [[0, 0], [64, 90], [127, 127]]\n"},
		{"global curve with per-patch points (different scopes)",
			"[midi.velocity]\ncurve = \"soft\"\n\n[[patches]]\nname = \"p\"\nsoundfont = \"/tmp/p.sf2\"\nvelocity_points = [[0, 0], [127, 127]]\n"},
		{"global points with per-patch curve (different scopes)",
			"[midi.velocity]\npoints = [[0, 0], [127, 127]]\n\n[[patches]]\nname = \"p\"\nsoundfont = \"/tmp/p.sf2\"\nvelocity_curve = \"hard\"\n"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := loadTOML(t, tc.body); err != nil {
				t.Errorf("expected valid config, got error: %v", err)
			}
		})
	}
}

func TestLoadVelocityGlobalGammaAloneNormalizedToCustom(t *testing.T) {
	// Global shorthand parity with the per-patch fields: [midi.velocity]
	// gamma > 0 with the curve omitted means "custom", not "silently
	// ignored". Load normalizes the spelling so every consumer sees it.
	cfg := mustLoadTOML(t, "[midi.velocity]\ngamma = 0.7\n")
	v := cfg.MIDI.Velocity
	if v.Curve != "custom" {
		t.Errorf("gamma without curve: Curve=%q, want %q", v.Curve, "custom")
	}
	if v.Gamma != 0.7 {
		t.Errorf("gamma without curve: Gamma=%g, want 0.7", v.Gamma)
	}
}

func TestLoadVelocityGlobalExplicitCurveNotOverridden(t *testing.T) {
	// The shorthand only fills an EMPTY curve name — an explicit preset
	// with a stray gamma keeps the preset (the velocity package ignores
	// gamma for named presets).
	cfg := mustLoadTOML(t, "[midi.velocity]\ncurve = \"soft\"\ngamma = 0.7\n")
	if got := cfg.MIDI.Velocity.Curve; got != "soft" {
		t.Errorf("explicit curve rewritten: got %q, want %q", got, "soft")
	}
}

func TestLoadVelocityDecodesFields(t *testing.T) {
	cfg := mustLoadTOML(t, `
[midi.velocity]
curve = "custom"
gamma = 0.8
out_min = 1
out_max = 120

[[patches]]
name = "p"
soundfont = "/tmp/p.sf2"
velocity_curve = "soft"
velocity_gamma = 0.7
`)
	v := cfg.MIDI.Velocity
	if v.Curve != "custom" || v.Gamma != 0.8 || v.OutMin != 1 || v.OutMax != 120 {
		t.Errorf("global velocity fields wrong: %+v", v)
	}
	p := cfg.Patches[0]
	if p.VelocityCurve != "soft" || p.VelocityGamma != 0.7 {
		t.Errorf("per-patch velocity fields wrong: curve=%q gamma=%g", p.VelocityCurve, p.VelocityGamma)
	}
}

func TestLoadVelocityDecodesPoints(t *testing.T) {
	cfg := mustLoadTOML(t, `
[midi.velocity]
points = [[0, 0], [64, 90], [127, 127]]

[[patches]]
name = "p"
soundfont = "/tmp/p.sf2"
velocity_points = [[0, 0], [32, 20], [127, 127]]
`)
	wantGlobal := [][]int{{0, 0}, {64, 90}, {127, 127}}
	if !equalPoints(cfg.MIDI.Velocity.Points, wantGlobal) {
		t.Errorf("global points = %v, want %v", cfg.MIDI.Velocity.Points, wantGlobal)
	}
	wantPatch := [][]int{{0, 0}, {32, 20}, {127, 127}}
	if !equalPoints(cfg.Patches[0].VelocityPoints, wantPatch) {
		t.Errorf("patch velocity_points = %v, want %v", cfg.Patches[0].VelocityPoints, wantPatch)
	}
}

func equalPoints(a, b [][]int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if len(a[i]) != len(b[i]) {
			return false
		}
		for j := range a[i] {
			if a[i][j] != b[i][j] {
				return false
			}
		}
	}
	return true
}

func TestLoadVelocityErrorCases(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // substring the error must contain
	}{
		{"unknown global curve",
			"[midi.velocity]\ncurve = \"banana\"\n",
			`unknown curve "banana"`},
		{"custom without gamma",
			"[midi.velocity]\ncurve = \"custom\"\n",
			`curve "custom" requires gamma > 0`},
		{"negative gamma",
			"[midi.velocity]\ncurve = \"soft\"\ngamma = -0.5\n",
			"gamma must be > 0"},
		{"out_min out of range",
			"[midi.velocity]\nout_min = 200\n",
			"out_min must be in 0..127"},
		{"out_max out of range",
			"[midi.velocity]\nout_max = 200\n",
			"out_max must be in 0..127"},
		{"out_min above out_max",
			"[midi.velocity]\nout_min = 100\nout_max = 50\n",
			"out_min (100) must be <= out_max (50)"},
		{"per-patch unknown curve",
			"[[patches]]\nname = \"p\"\nsoundfont = \"/tmp/p.sf2\"\nvelocity_curve = \"wonky\"\n",
			`patch "p": unknown velocity_curve "wonky"`},
		{"per-patch custom without gamma",
			"[[patches]]\nname = \"p\"\nsoundfont = \"/tmp/p.sf2\"\nvelocity_curve = \"custom\"\n",
			`patch "p": velocity_curve "custom" requires velocity_gamma > 0`},
		{"per-patch negative gamma",
			"[[patches]]\nname = \"p\"\nsoundfont = \"/tmp/p.sf2\"\nvelocity_gamma = -1.0\n",
			`patch "p": velocity_gamma must be > 0`},
		{"points single pair",
			"[midi.velocity]\npoints = [[0, 0]]\n",
			"points must have 2..16 [x, y] pairs (got 1)"},
		{"points too many pairs",
			"[midi.velocity]\npoints = [[0,0],[1,1],[2,2],[3,3],[4,4],[5,5],[6,6],[7,7],[8,8],[9,9],[10,10],[11,11],[12,12],[13,13],[14,14],[15,15],[127,127]]\n",
			"points must have 2..16 [x, y] pairs (got 17)"},
		{"points malformed pair",
			"[midi.velocity]\npoints = [[0, 0], [64], [127, 127]]\n",
			"points[1] must be an [x, y] pair (got 1 values)"},
		{"points triple",
			"[midi.velocity]\npoints = [[0, 0], [64, 90, 1], [127, 127]]\n",
			"points[1] must be an [x, y] pair (got 3 values)"},
		{"points x out of range",
			"[midi.velocity]\npoints = [[0, 0], [200, 90], [127, 127]]\n",
			"points[1] x must be in 0..127 (got 200)"},
		{"points y out of range",
			"[midi.velocity]\npoints = [[0, 0], [64, -3], [127, 127]]\n",
			"points[1] y must be in 0..127 (got -3)"},
		{"points first not zero",
			"[midi.velocity]\npoints = [[0, 5], [127, 127]]\n",
			"points[0] must be [0, 0] (got [0, 5])"},
		{"points last x not 127",
			"[midi.velocity]\npoints = [[0, 0], [126, 127]]\n",
			"points last x must be 127 (got 126)"},
		{"points x not strictly increasing",
			"[midi.velocity]\npoints = [[0, 0], [64, 60], [64, 90], [127, 127]]\n",
			"points[2] x (64) must be > previous x (64)"},
		{"points y decreasing",
			"[midi.velocity]\npoints = [[0, 0], [64, 90], [127, 80]]\n",
			"points[2] y (80) must be >= previous y (90)"},
		{"points and curve in same scope",
			"[midi.velocity]\ncurve = \"soft\"\npoints = [[0, 0], [127, 127]]\n",
			"midi.velocity: points and curve/gamma are mutually exclusive"},
		{"points and gamma in same scope",
			"[midi.velocity]\ngamma = 0.8\npoints = [[0, 0], [127, 127]]\n",
			"midi.velocity: points and curve/gamma are mutually exclusive"},
		{"per-patch points and curve in same scope",
			"[[patches]]\nname = \"p\"\nsoundfont = \"/tmp/p.sf2\"\nvelocity_curve = \"soft\"\nvelocity_points = [[0, 0], [127, 127]]\n",
			`patch "p": velocity_points and velocity_curve/velocity_gamma are mutually exclusive`},
		{"per-patch points and gamma in same scope",
			"[[patches]]\nname = \"p\"\nsoundfont = \"/tmp/p.sf2\"\nvelocity_gamma = 0.7\nvelocity_points = [[0, 0], [127, 127]]\n",
			`patch "p": velocity_points and velocity_curve/velocity_gamma are mutually exclusive`},
		{"per-patch points shape error carries patch prefix",
			"[[patches]]\nname = \"p\"\nsoundfont = \"/tmp/p.sf2\"\nvelocity_points = [[0, 0], [126, 127]]\n",
			`patch "p": velocity_points last x must be 127 (got 126)`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadTOML(t, tc.body)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q missing substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestLoadVelocityErrorsListAllOffenders(t *testing.T) {
	// Errors-not-warnings rule: every velocity offender shows up in the ONE
	// returned error, so the user fixes the config in a single pass.
	_, err := loadTOML(t, `
[midi.velocity]
curve = "banana"
out_min = 100
out_max = 50

[[patches]]
name = "p1"
soundfont = "/tmp/p1.sf2"
velocity_curve = "custom"

[[patches]]
name = "p2"
soundfont = "/tmp/p2.sf2"
velocity_gamma = -2.0

[[patches]]
name = "p3"
soundfont = "/tmp/p3.sf2"
velocity_curve = "soft"
velocity_points = [[0, 0], [90, 80], [64, 90], [127, 127]]
`)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	for _, want := range []string{
		`unknown curve "banana"`,
		"out_min (100) must be <= out_max (50)",
		`patch "p1": velocity_curve "custom" requires velocity_gamma > 0`,
		`patch "p2": velocity_gamma must be > 0`,
		`patch "p3": velocity_points and velocity_curve/velocity_gamma are mutually exclusive`,
		`patch "p3": velocity_points[2] x (64) must be > previous x (90)`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing offender %q:\n%s", want, err.Error())
		}
	}
}

// --- [web] -----------------------------------------------------------------

func TestWebDefaults(t *testing.T) {
	cfg := mustLoadTOML(t, "")
	if !cfg.Web.Enabled {
		t.Error("web must be enabled by default")
	}
	if cfg.Web.Listen != "127.0.0.1:8666" {
		t.Errorf("default listen: got %q, want 127.0.0.1:8666", cfg.Web.Listen)
	}
}

func TestWebExplicitDisableHonored(t *testing.T) {
	cfg := mustLoadTOML(t, "[web]\nenabled = false\n")
	if cfg.Web.Enabled {
		t.Error("explicit enabled = false must override the enabled-by-default default")
	}
}

func TestWebEnabledEmptyListenBackfilled(t *testing.T) {
	cfg := mustLoadTOML(t, "[web]\nenabled = true\nlisten = \"\"\n")
	if !cfg.Web.Enabled {
		t.Error("expected web enabled")
	}
	if cfg.Web.Listen != "127.0.0.1:8666" {
		t.Errorf("empty listen not backfilled: got %q", cfg.Web.Listen)
	}
}

func TestWebCustomListenPreserved(t *testing.T) {
	cfg := mustLoadTOML(t, "[web]\nenabled = true\nlisten = \"0.0.0.0:9000\"\n")
	if cfg.Web.Listen != "0.0.0.0:9000" {
		t.Errorf("custom listen not preserved: got %q", cfg.Web.Listen)
	}
}

// --- [osc.mixer] alias + heartbeat ------------------------------------------

func TestMixerAliasWinsOverXR18(t *testing.T) {
	cfg := mustLoadTOML(t, `
[osc.xr18]
host = "192.0.2.1"
port = 10024

[osc.mixer]
host = "192.0.2.2"
port = 9000
`)
	if cfg.OSC.XR18.Host != "192.0.2.2" {
		t.Errorf("[osc.mixer] should win over [osc.xr18]: host=%q", cfg.OSC.XR18.Host)
	}
	if cfg.OSC.XR18.Port != 9000 {
		t.Errorf("[osc.mixer] port should win: got %d", cfg.OSC.XR18.Port)
	}
	if cfg.OSC.Mixer != nil {
		t.Error("Mixer must be nil after Load folds it into XR18")
	}
}

func TestMixerAliasAloneDefaultsPort(t *testing.T) {
	cfg := mustLoadTOML(t, "[osc.mixer]\nhost = \"192.0.2.9\"\n")
	if cfg.OSC.XR18.Host != "192.0.2.9" {
		t.Errorf("mixer host not folded into XR18: got %q", cfg.OSC.XR18.Host)
	}
	if cfg.OSC.XR18.Port != 10024 {
		t.Errorf("mixer without port should get the 10024 default, got %d", cfg.OSC.XR18.Port)
	}
	if cfg.OSC.Mixer != nil {
		t.Error("Mixer must be nil after Load folds it into XR18")
	}
}

func TestXR18AloneStillWorks(t *testing.T) {
	cfg := mustLoadTOML(t, "[osc.xr18]\nhost = \"192.0.2.6\"\n")
	if cfg.OSC.XR18.Host != "192.0.2.6" {
		t.Errorf("legacy [osc.xr18] host lost: got %q", cfg.OSC.XR18.Host)
	}
	if cfg.OSC.Mixer != nil {
		t.Error("Mixer should stay nil when only [osc.xr18] is present")
	}
}

func TestHeartbeatAbsentIsNil(t *testing.T) {
	// nil pointer = key absent = "use the /xinfo default" (resolved by the
	// caller wiring the reconciler, not by config).
	cfg := mustLoadTOML(t, "[osc.xr18]\nhost = \"192.0.2.6\"\n")
	if cfg.OSC.XR18.Heartbeat != nil {
		t.Errorf("absent heartbeat should decode as nil, got %q", *cfg.OSC.XR18.Heartbeat)
	}
}

func TestHeartbeatExplicitEmptyMeansDisabled(t *testing.T) {
	cfg := mustLoadTOML(t, "[osc.xr18]\nhost = \"192.0.2.6\"\nheartbeat = \"\"\n")
	h := cfg.OSC.XR18.Heartbeat
	if h == nil {
		t.Fatal("explicit heartbeat = \"\" must decode as non-nil pointer")
	}
	if *h != "" {
		t.Errorf("expected empty heartbeat (polling disabled), got %q", *h)
	}
}

func TestHeartbeatCustomAddress(t *testing.T) {
	cfg := mustLoadTOML(t, "[osc.mixer]\nhost = \"192.0.2.6\"\nheartbeat = \"/status\"\n")
	h := cfg.OSC.XR18.Heartbeat // carried through the mixer→xr18 fold
	if h == nil {
		t.Fatal("custom heartbeat lost in Load")
	}
	if *h != "/status" {
		t.Errorf("expected heartbeat %q, got %q", "/status", *h)
	}
}

func TestHeartbeatFormatErrors(t *testing.T) {
	cases := []struct {
		name string
		body string
		want string // substring the error must contain
	}{
		{"missing leading slash",
			"[osc.xr18]\nhost = \"192.0.2.6\"\nheartbeat = \"xinfo\"\n",
			`osc.xr18: heartbeat "xinfo" must start with "/"`},
		{"contains space",
			"[osc.xr18]\nhost = \"192.0.2.6\"\nheartbeat = \"/x info\"\n",
			`osc.xr18: heartbeat "/x info" must not contain spaces`},
		{"mixer alias reports its own block name",
			"[osc.mixer]\nhost = \"192.0.2.6\"\nheartbeat = \"status\"\n",
			`osc.mixer: heartbeat "status" must start with "/"`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadTOML(t, tc.body)
			if err == nil {
				t.Fatal("expected error, got nil")
			}
			if !strings.Contains(err.Error(), tc.want) {
				t.Errorf("error %q missing substring %q", err.Error(), tc.want)
			}
		})
	}
}

func TestHeartbeatFormatErrorsCollectBothOffenses(t *testing.T) {
	// A value that is wrong in both ways reports both lines, and joins
	// the same all-offenders error as the velocity checks — one pass to
	// fix everything.
	_, err := loadTOML(t, `
[midi.velocity]
curve = "banana"

[osc.xr18]
host = "192.0.2.6"
heartbeat = "x info"
`)
	if err == nil {
		t.Fatal("expected error, got nil")
	}
	for _, want := range []string{
		`unknown curve "banana"`,
		`heartbeat "x info" must start with "/"`,
		`heartbeat "x info" must not contain spaces`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing offender %q:\n%s", want, err.Error())
		}
	}
}

func TestHeartbeatValidFormsPass(t *testing.T) {
	for _, hb := range []string{"/xinfo", "/status", "/very/nested/ping"} {
		body := "[osc.xr18]\nhost = \"192.0.2.6\"\nheartbeat = \"" + hb + "\"\n"
		if _, err := loadTOML(t, body); err != nil {
			t.Errorf("heartbeat %q: expected valid, got %v", hb, err)
		}
	}
}

func TestExampleConfigEmbedNonEmpty(t *testing.T) {
	// Sanity check the go:embed actually pulled in the example file —
	// catches regressions where the embed directive's path drifts.
	b := ExampleConfig()
	if len(b) == 0 {
		t.Fatal("ExampleConfig() is empty — go:embed broken?")
	}
	if !strings.Contains(string(b), "[[patches]]") {
		t.Errorf("ExampleConfig() missing [[patches]] sections — wrong file?")
	}
	if !strings.Contains(string(b), "ydp-grand") {
		t.Errorf("ExampleConfig() missing ydp-grand patch — wrong file?")
	}
	// The example doubles as user documentation — make sure the newer
	// config surfaces stay documented there.
	for _, want := range []string{
		"[midi.velocity]", "[web]", "heartbeat", "[osc.mixer]",
		"points  = [[0, 0], [64, 90], [127, 127]]", "velocity_points",
	} {
		if !strings.Contains(string(b), want) {
			t.Errorf("ExampleConfig() missing %q documentation block", want)
		}
	}
}

func TestExampleConfigLoadsCleanly(t *testing.T) {
	// The embedded example is written to disk verbatim on first run — it
	// must always pass Load, and its documentation-only blocks must stay
	// commented out (defaults preserved).
	cfg, err := loadTOML(t, string(ExampleConfig()))
	if err != nil {
		t.Fatalf("example config does not Load: %v", err)
	}
	if !cfg.Web.Enabled || cfg.Web.Listen != DefaultWebListen {
		t.Errorf("example must leave [web] at defaults (now enabled by default), got %+v", cfg.Web)
	}
	if cfg.OSC.XR18.Heartbeat != nil {
		t.Errorf("example must leave heartbeat commented (nil), got %q", *cfg.OSC.XR18.Heartbeat)
	}
	if cfg.MIDI.Velocity.Curve != "" {
		t.Errorf("example must leave [midi.velocity] commented, got %+v", cfg.MIDI.Velocity)
	}
}
