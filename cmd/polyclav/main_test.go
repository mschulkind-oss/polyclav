package main

import (
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/config"
	"github.com/mschulkind-oss/polyclav/internal/controls"
	"github.com/mschulkind-oss/polyclav/internal/controls/pages"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/driver"
	"github.com/mschulkind-oss/polyclav/internal/patches"
	"github.com/mschulkind-oss/polyclav/internal/web"
)

// TestNewRateGate pins the velocity monitor's throttle contract: extra
// calls inside the gap are dropped (never queued), and the gate reopens
// after the gap elapses.
func TestNewRateGate(t *testing.T) {
	gate := newRateGate(50 * time.Millisecond)
	if !gate() {
		t.Fatal("first call must pass")
	}
	for i := 0; i < 10; i++ {
		if gate() {
			t.Fatal("calls inside the gap must be dropped")
		}
	}
	time.Sleep(70 * time.Millisecond)
	if !gate() {
		t.Fatal("call after the gap must pass")
	}
	if gate() {
		t.Fatal("the pass must re-arm the gate")
	}
}

// TestResolveVelocity covers the precedence rules from
// docs/VELOCITY_CURVES.md: per-patch override fields win over the global
// [midi.velocity] block, and a patch gamma with no curve name means
// "custom".
func TestApplyWebFlag(t *testing.T) {
	cases := []struct {
		name       string
		enabled    bool
		listen     string
		flag       string
		wantOn     bool
		wantListen string
	}{
		{"empty flag leaves config off", false, "127.0.0.1:8666", "", false, "127.0.0.1:8666"},
		{"empty flag leaves config on", true, "127.0.0.1:9999", "", true, "127.0.0.1:9999"},
		{"address enables and overrides", false, "127.0.0.1:8666", ":7777", true, ":7777"},
		{"on keeps configured listen", false, "127.0.0.1:9999", "on", true, "127.0.0.1:9999"},
		{"true keeps configured listen", false, "127.0.0.1:9999", "true", true, "127.0.0.1:9999"},
		{"on backfills default when listen empty", false, "", "on", true, "127.0.0.1:8666"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := config.Defaults()
			cfg.Web.Enabled = tc.enabled
			cfg.Web.Listen = tc.listen
			applyWebFlag(cfg, tc.flag)
			if cfg.Web.Enabled != tc.wantOn {
				t.Errorf("Enabled = %v, want %v", cfg.Web.Enabled, tc.wantOn)
			}
			if cfg.Web.Listen != tc.wantListen {
				t.Errorf("Listen = %q, want %q", cfg.Web.Listen, tc.wantListen)
			}
		})
	}
}

func TestResolveVelocity(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		patch      *patches.Patch
		wantErr    bool
		wantPrefix string // Describe() prefix, e.g. "soft (γ=0.60"
	}{
		{
			name:       "zero config nil patch is linear",
			cfg:        config.Defaults(),
			patch:      nil,
			wantPrefix: "linear (γ=1.00, out 1..127)",
		},
		{
			name: "global preset with clamp",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Curve: "soft", OutMin: 10, OutMax: 100,
			}}},
			patch:      nil,
			wantPrefix: "soft (γ=0.60, out 10..100)",
		},
		{
			name: "patch with no override inherits global",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Curve: "hard",
			}}},
			patch:      &patches.Patch{Name: "p"},
			wantPrefix: "hard (γ=1.60, out 1..127)",
		},
		{
			name: "patch curve name wins over global",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Curve: "soft", OutMin: 10, OutMax: 100,
			}}},
			patch: &patches.Patch{Name: "p", VelocityCurve: "hard"},
			// Per-patch overrides carry no clamp fields: defaults 1..127.
			wantPrefix: "hard (γ=1.60, out 1..127)",
		},
		{
			name:       "patch gamma alone implies custom",
			cfg:        config.Defaults(),
			patch:      &patches.Patch{Name: "p", VelocityGamma: 2.5},
			wantPrefix: "custom (γ=2.50, out 1..127)",
		},
		{
			name:    "patch custom without gamma errors",
			cfg:     config.Defaults(),
			patch:   &patches.Patch{Name: "p", VelocityCurve: "custom"},
			wantErr: true,
		},
		{
			name: "global custom gamma",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Curve: "custom", Gamma: 0.8,
			}}},
			patch:      nil,
			wantPrefix: "custom (γ=0.80, out 1..127)",
		},
		{
			// The global shorthand mirrors the per-patch one: gamma > 0
			// with no curve name means "custom", not "silently ignored".
			name: "global gamma alone implies custom",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Gamma: 2.5,
			}}},
			patch:      nil,
			wantPrefix: "custom (γ=2.50, out 1..127)",
		},
		{
			name: "global gamma alone with clamp",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Gamma: 0.8, OutMin: 10, OutMax: 100,
			}}},
			patch:      nil,
			wantPrefix: "custom (γ=0.80, out 10..100)",
		},
		{
			// A named preset with a stray gamma keeps the preset — the
			// shorthand only fills an EMPTY curve name.
			name: "global preset ignores stray gamma",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Curve: "soft", Gamma: 2.5,
			}}},
			patch:      nil,
			wantPrefix: "soft (γ=0.60, out 1..127)",
		},
		{
			name: "global points nil patch",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Points: [][]int{{0, 0}, {64, 90}, {127, 127}},
			}}},
			patch:      nil,
			wantPrefix: "points[3] (out 1..127)",
		},
		{
			name: "global points carry global clamps",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Points: [][]int{{0, 0}, {127, 127}}, OutMin: 10, OutMax: 100,
			}}},
			patch:      nil,
			wantPrefix: "points[2] (out 10..100)",
		},
		{
			name: "patch with no override inherits global points",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Points: [][]int{{0, 0}, {64, 90}, {127, 127}},
			}}},
			patch:      &patches.Patch{Name: "p"},
			wantPrefix: "points[3] (out 1..127)",
		},
		{
			// Full precedence chain, top rung: per-patch points beat the
			// patch's own curve/gamma AND everything global. (Both in one
			// scope is a Load error, but resolveVelocity stays deterministic
			// for code-built configs.)
			name: "patch points win over patch curve and global points",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Points: [][]int{{0, 0}, {127, 127}},
			}}},
			patch: &patches.Patch{Name: "p", VelocityCurve: "hard",
				VelocityPoints: [][]int{{0, 0}, {32, 40}, {64, 90}, {127, 127}}},
			// Per-patch overrides carry no clamp fields: defaults 1..127.
			wantPrefix: "points[4] (out 1..127)",
		},
		{
			name: "patch curve wins over global points",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Points: [][]int{{0, 0}, {64, 90}, {127, 127}},
			}}},
			patch:      &patches.Patch{Name: "p", VelocityCurve: "hard"},
			wantPrefix: "hard (γ=1.60, out 1..127)",
		},
		{
			name: "patch gamma wins over global points",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Points: [][]int{{0, 0}, {64, 90}, {127, 127}},
			}}},
			patch:      &patches.Patch{Name: "p", VelocityGamma: 2.5},
			wantPrefix: "custom (γ=2.50, out 1..127)",
		},
		{
			// resolveVelocity mirrors the doc'd within-scope precedence:
			// global points sit above global curve/gamma.
			name: "global points win over global curve",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Curve: "soft", Points: [][]int{{0, 0}, {127, 127}},
			}}},
			patch:      nil,
			wantPrefix: "points[2] (out 1..127)",
		},
		{
			name:    "patch malformed point pair errors",
			cfg:     config.Defaults(),
			patch:   &patches.Patch{Name: "p", VelocityPoints: [][]int{{0, 0}, {64}, {127, 127}}},
			wantErr: true,
		},
		{
			name: "global point value out of range errors",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Points: [][]int{{0, 0}, {64, 200}, {127, 127}},
			}}},
			patch:   nil,
			wantErr: true,
		},
		{
			name:    "patch non-monotonic points error",
			cfg:     config.Defaults(),
			patch:   &patches.Patch{Name: "p", VelocityPoints: [][]int{{0, 0}, {64, 90}, {127, 80}}},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			curve, err := resolveVelocity(tt.cfg.MIDI.Velocity, tt.patch)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveVelocity() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveVelocity() error = %v", err)
			}
			if got := curve.Describe(); !strings.HasPrefix(got, tt.wantPrefix) {
				t.Errorf("Describe() = %q, want prefix %q", got, tt.wantPrefix)
			}
			// v==0 must always pass through untouched (NoteOff semantics).
			if got := curve.Apply(0); got != 0 {
				t.Errorf("Apply(0) = %d, want 0", got)
			}
		})
	}
}

// TestPatchFollowerLevelTriggered pins the level-triggered contract:
// apply fires only when the CURRENT patch differs from the last applied
// name — regardless of how many (possibly dropped) events occurred in
// between — and repeated events for the same patch are no-ops.
func TestPatchFollowerLevelTriggered(t *testing.T) {
	var cur *patches.Patch
	var applied []string
	follow := newPatchFollower("", func() *patches.Patch { return cur }, func(p *patches.Patch) bool {
		if p == nil {
			applied = append(applied, "<nil>")
		} else {
			applied = append(applied, p.Name)
		}
		return true
	})

	follow() // no patch selected, last == "" — level already satisfied
	if len(applied) != 0 {
		t.Fatalf("no-change event applied something: %v", applied)
	}

	cur = &patches.Patch{Name: "a"}
	follow()
	follow() // same patch again: no re-apply
	if want := []string{"a"}; !equalStrings(applied, want) {
		t.Fatalf("after selecting a: applied %v, want %v", applied, want)
	}

	// Simulate a dropped "patch" event: the patch changed a -> b -> c but
	// the follower only sees one (unrelated) event afterwards. It must
	// still converge on c.
	cur = &patches.Patch{Name: "b"}
	cur = &patches.Patch{Name: "c"}
	follow()
	if want := []string{"a", "c"}; !equalStrings(applied, want) {
		t.Fatalf("after dropped events: applied %v, want %v", applied, want)
	}

	// Deselecting (current == nil) is a change back to "no patch".
	cur = nil
	follow()
	if want := []string{"a", "c", "<nil>"}; !equalStrings(applied, want) {
		t.Fatalf("after deselect: applied %v, want %v", applied, want)
	}
}

// TestPatchFollowerRetriesFailedApply pins the "resolved for" tracking:
// a failed apply must NOT record the new name, so the next event of any
// type retries instead of leaving stale state installed.
func TestPatchFollowerRetriesFailedApply(t *testing.T) {
	cur := &patches.Patch{Name: "a"}
	calls := 0
	ok := false
	follow := newPatchFollower("", func() *patches.Patch { return cur }, func(p *patches.Patch) bool {
		calls++
		return ok
	})

	follow()
	follow() // apply failed: retried on the next event
	if calls != 2 {
		t.Fatalf("failed apply not retried: %d calls, want 2", calls)
	}
	ok = true
	follow() // succeeds and records "a"
	follow() // level satisfied: no further calls
	if calls != 3 {
		t.Fatalf("after success: %d calls, want 3 (no re-apply once recorded)", calls)
	}
}

// Minimal pages seams for TestDispatchTransport: the transport paths
// (page cycling, player toggle) never touch controls' collaborators, so
// the Controls underneath can be built over nil audio/registry/store.
type nopScreen struct{}

func (nopScreen) SetDisplayText(_, _ string) error { return nil }

type nopPads struct{}

func (nopPads) SetPadColor(_, _ int, _ components.Color) error { return nil }

type countingPlayer struct{ toggles int }

func (p *countingPlayer) Toggle() (bool, string, bool) {
	p.toggles++
	return true, "clip", true
}

// TestDispatchTransport pins the §2.5 button mapping as shipped: Scene
// Up/Down cycle pages (with wraparound), Play toggles the audition
// player, and every other transport button is intentionally inert.
func TestDispatchTransport(t *testing.T) {
	ctl := controls.New(nil, nil, nil, nil, nil)
	pg := pages.New(ctl, nopScreen{}, nopPads{})
	pg.OnPatchChange("native") // unlock the synth pages
	plr := &countingPlayer{}
	pg.AttachPlayer(plr)

	dispatchTransport(pg, driver.TransportSceneDown)
	if idx, name := pg.CurrentPage(); idx != 1 || name != "OSC" {
		t.Fatalf("SceneDown: page %d %q, want 1 OSC", idx, name)
	}
	dispatchTransport(pg, driver.TransportSceneUp)
	if idx, _ := pg.CurrentPage(); idx != 0 {
		t.Fatalf("SceneUp: page %d, want 0", idx)
	}
	dispatchTransport(pg, driver.TransportSceneUp) // wraps to the last page
	if idx, name := pg.CurrentPage(); idx != 4 || name != "LFO/MOD" {
		t.Fatalf("SceneUp wrap: page %d %q, want 4 LFO/MOD", idx, name)
	}

	dispatchTransport(pg, driver.TransportPlay)
	if plr.toggles != 1 {
		t.Fatalf("Play: toggles = %d, want 1", plr.toggles)
	}

	// Everything else in the §2.5 table is unbound (see onDAWEvent).
	for _, b := range []driver.TransportButton{
		driver.TransportStop, driver.TransportRecord, driver.TransportLoop,
		driver.TransportRewind, driver.TransportFastForward,
		driver.TransportTrackLeft, driver.TransportTrackRight, driver.TransportShift,
	} {
		dispatchTransport(pg, b)
	}
	if idx, _ := pg.CurrentPage(); idx != 4 {
		t.Errorf("unbound buttons moved the page to %d", idx)
	}
	if plr.toggles != 1 {
		t.Errorf("unbound buttons toggled the player (%d)", plr.toggles)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestResolveVelocityClamp checks the global OutMin/OutMax ints reach the
// curve as a working clamp, not just in the label.
func TestResolveVelocityClamp(t *testing.T) {
	cfg := &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
		Curve: "linear", OutMin: 20, OutMax: 90,
	}}}
	curve, err := resolveVelocity(cfg.MIDI.Velocity, nil)
	if err != nil {
		t.Fatalf("resolveVelocity() error = %v", err)
	}
	if got := curve.Apply(127); got != 90 {
		t.Errorf("Apply(127) = %d, want 90 (out_max clamp)", got)
	}
	if got := curve.Apply(1); got != 20 {
		t.Errorf("Apply(1) = %d, want 20 (out_min clamp)", got)
	}
}

// TestVelocitySaveSurvivesPatchChange is the regression test for the
// "saved curve silently reverts" bug: PUT /api/velocity {save:true}
// wrote the config FILE, but the patch follower re-resolved curves from
// the BOOT-TIME config, so the next patch change reinstalled the boot
// curve for the rest of the session. With the mutable globalVelocity
// holder wired through web.Deps.SetGlobalVelocity, the follower must
// install the SAVED global curve after a patch change — while a
// session-only apply (save:false) still reverts to config, its
// documented behavior.
func TestVelocitySaveSurvivesPatchChange(t *testing.T) {
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	ctl := controls.New(logger, nil, nil, nil, nil)

	// Boot wiring, exactly as main does it: holder seeded from the boot
	// config (linear), the install closure over it, and the follower.
	gvel := newGlobalVelocity(config.VelocityConfig{})
	installVelocity := makeInstallVelocity(logger, ctl, gvel)
	cur := &patches.Patch{Name: "a"}
	if !installVelocity(cur) {
		t.Fatal("boot install failed")
	}
	bootLabel := ctl.VelocityLabel()
	if !strings.HasPrefix(bootLabel, "linear") {
		t.Fatalf("boot curve: expected linear, got %q", bootLabel)
	}
	follow := newPatchFollower(cur.Name, func() *patches.Patch { return cur }, installVelocity)

	// A web server over the same controls + holder, like main's Deps.
	dir := t.TempDir()
	path := filepath.Join(dir, "polyclav.toml")
	if err := os.WriteFile(path, []byte("[web]\nenabled = false\n"), 0o644); err != nil {
		t.Fatalf("seed config: %v", err)
	}
	srv := web.New(web.Deps{
		Logger:            logger,
		Controls:          ctl,
		ConfigPath:        path,
		SetGlobalVelocity: gvel.Set,
	})
	put := func(body string) {
		t.Helper()
		req := httptest.NewRequest("PUT", "/api/velocity", strings.NewReader(body))
		rec := httptest.NewRecorder()
		srv.Handler().ServeHTTP(rec, req)
		if rec.Code != 200 {
			t.Fatalf("PUT /api/velocity: status %d (body %s)", rec.Code, rec.Body.String())
		}
	}

	// Save a curve to the config file, then change patches: the follower
	// must re-install the SAVED curve, not the boot-time one.
	put(`{"curve": "soft", "save": true}`)
	cur = &patches.Patch{Name: "b"}
	follow()
	if got := ctl.VelocityLabel(); !strings.HasPrefix(got, "soft") {
		t.Fatalf("after save + patch change: curve %q, want the saved soft curve", got)
	}

	// Session-only apply: a patch change reverts to the config-resolved
	// curve (which is now the saved soft, NOT boot linear).
	put(`{"curve": "hard"}`)
	if got := ctl.VelocityLabel(); !strings.HasPrefix(got, "hard") {
		t.Fatalf("session apply: curve %q, want hard", got)
	}
	cur = &patches.Patch{Name: "c"}
	follow()
	if got := ctl.VelocityLabel(); !strings.HasPrefix(got, "soft") {
		t.Fatalf("after session apply + patch change: curve %q, want config-resolved soft", got)
	}

	// A per-patch override still wins over the (updated) global rung.
	cur = &patches.Patch{Name: "d", VelocityCurve: "hard"}
	follow()
	if got := ctl.VelocityLabel(); !strings.HasPrefix(got, "hard") {
		t.Fatalf("per-patch override: curve %q, want hard", got)
	}
}

// TestResolveVelocityPointsApply checks the resolved point curve actually
// interpolates — the config ints reach the velocity package as a working
// mapping, not just a label.
func TestResolveVelocityPointsApply(t *testing.T) {
	cfg := &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
		Points: [][]int{{0, 0}, {64, 90}, {127, 127}},
	}}}
	curve, err := resolveVelocity(cfg.MIDI.Velocity, nil)
	if err != nil {
		t.Fatalf("resolveVelocity() error = %v", err)
	}
	for in, want := range map[uint8]uint8{0: 0, 32: 45, 64: 90, 127: 127} {
		if got := curve.Apply(in); got != want {
			t.Errorf("Apply(%d) = %d, want %d", in, got, want)
		}
	}

	// Per-patch points replace — not compose with — the global set.
	p := &patches.Patch{Name: "p", VelocityPoints: [][]int{{0, 0}, {127, 127}}}
	curve, err = resolveVelocity(cfg.MIDI.Velocity, p)
	if err != nil {
		t.Fatalf("resolveVelocity(patch) error = %v", err)
	}
	if got := curve.Apply(64); got != 64 {
		t.Errorf("patch identity points: Apply(64) = %d, want 64", got)
	}
}
