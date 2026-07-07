package config

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"sort"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/mschulkind-oss/polyclav/internal/osc"
)

// knownNativeEngines is the set of valid `engine` strings for
// type="native" patches. Phase 1 ships only "minimoog"; new engines are
// added here as they land in audio-core (see docs/ROADMAP.md).
var knownNativeEngines = map[string]struct{}{
	"minimoog": {},
}

// IsKnownNativeEngine reports whether engine is a valid native synth
// engine name. Exposed for tests and any future CLI introspection.
func IsKnownNativeEngine(engine string) bool {
	_, ok := knownNativeEngines[engine]
	return ok
}

type Config struct {
	Soundfont SoundfontConfig  `toml:"soundfont"`
	Audio     AudioConfig      `toml:"audio"`
	MIDI      MIDIConfig       `toml:"midi"`
	OSC       OSCConfig        `toml:"osc"`
	Web       WebConfig        `toml:"web"`
	Patches   []PatchConfig    `toml:"patches"`
	Mastering *MasteringConfig `toml:"mastering"`
}

// AudioConfig tunes the audio output path. Optional; the zero value uses
// engine defaults.
type AudioConfig struct {
	// LatencyFrames requests the audio buffer size in frames (the
	// "quantum"). 0 = engine default (128, ~2.7 ms at 48 kHz). Clamped to
	// [16, 8192] in the audio core. This is a request: the effective buffer
	// never drops below what the platform supports (the PipeWire graph
	// quantum on Linux, the device's minimum buffer on macOS). Smaller =
	// lower latency but higher CPU and more xrun risk.
	LatencyFrames int `toml:"latency_frames"`
}

type SoundfontConfig struct {
	Path string `toml:"path"`
}

type MIDIConfig struct {
	// PortMatch is a substring (case-insensitive) matched against MIDI
	// input port names. Empty = first available. Defaults to "launchkey".
	PortMatch string `toml:"port_match"`
	// Velocity is the global default velocity curve applied to incoming
	// NoteOn velocities (see docs/VELOCITY_CURVES.md). The zero value
	// (Curve == "") means linear passthrough. Per-patch overrides live on
	// PatchConfig (velocity_curve / velocity_gamma) and win over this.
	Velocity VelocityConfig `toml:"velocity"`
}

// VelocityConfig is the [midi.velocity] block: the global default velocity
// curve. Curve is "", "linear", "soft", "hard", or "custom" ("" behaves as
// linear); curve = "custom" requires Gamma > 0. Gamma > 0 with Curve left
// empty is the "custom" shorthand — the same rule as the per-patch
// velocity_gamma field — normalized to Curve = "custom" at Load. OutMin/
// OutMax optionally clamp the mapped output (0..127; the velocity package
// applies its own 1/127 defaults when they are left at zero).
//
// Points is the v2 alternative curve shape: piecewise-linear [x, y]
// control points (docs/VELOCITY_CURVES.md "v2"), 2..16 pairs starting at
// [0, 0], ending at x = 127, xs strictly increasing, ys non-decreasing.
// Points and Curve/Gamma are mutually exclusive WITHIN a scope (setting
// both here — or both on one patch — is a load error), because "which of
// the two curves wins" inside one block has no right answer. ACROSS
// scopes normal precedence applies: per-patch points > per-patch
// curve/gamma > global points > global curve/gamma (resolved by the
// daemon). OutMin/OutMax still clamp point-curve output.
//
// Names, ranges, and point shapes are validated in Load;
// internal/velocity revalidates at construction — this package
// deliberately does not import it.
type VelocityConfig struct {
	Curve  string  `toml:"curve"`
	Gamma  float32 `toml:"gamma"`
	OutMin int     `toml:"out_min"`
	OutMax int     `toml:"out_max"`
	Points [][]int `toml:"points"`
}

// DefaultWebListen is the default listen address for the embedded web UI.
// Loopback is the security boundary — there is no auth layer (see
// docs/WEB_UI.md "Security model"); binding beyond localhost is an
// explicit, documented user opt-in.
const DefaultWebListen = "127.0.0.1:8666"

// WebConfig is the [web] block. Disabled by default — same opt-in
// philosophy as osc mixer control (empty host = off).
type WebConfig struct {
	Enabled bool   `toml:"enabled"`
	Listen  string `toml:"listen"`
}

type OSCConfig struct {
	// XR18 is the legacy [osc.xr18] name for the OSC mixer block, kept
	// for back-compat (Tier 0 of docs/CONFIGURABILITY.md). All runtime
	// code reads this field: when [osc.mixer] is present, Load copies it
	// in here so there is exactly one source of truth downstream.
	XR18 XR18Config `toml:"xr18"`
	// Mixer is the preferred new name for the same block. Non-nil only
	// while decoding — Load folds it into XR18 (mixer wins if both are
	// set) and resets it to nil.
	Mixer *XR18Config `toml:"mixer"`
}

type XR18Config struct {
	Host string `toml:"host"`
	Port int    `toml:"port"`
	// Heartbeat is the OSC address polled to decide whether the mixer is
	// reachable. Pointer semantics: nil (key absent) → the "/xinfo"
	// default (X-Air status query); explicit "" → presence polling
	// disabled, sends become fire-and-forget UDP (for generic OSC targets
	// that won't answer X-Air pings). Resolution to the reconciler's
	// plain string happens at wiring time, not here.
	Heartbeat *string       `toml:"heartbeat"`
	Bindings  []osc.Binding `toml:"bindings"`
}

// MasteringConfig configures the final-stage DSP applied after the
// per-patch chain. Optional in polyclav.toml; defaults applied at startup
// when the [mastering] block is absent.
type MasteringConfig struct {
	CompAmount       float32 `toml:"comp_amount"`        // 0..1, 0 = bypass
	LimiterCeilingDB float32 `toml:"limiter_ceiling_db"` // dBFS, default -0.3
}

// PatchConfig is one [[patches]] entry in polyclav.toml. The patches package
// converts these into runtime patches.Patch values via patches.FromConfig.
// Type selects the backend: "soundfont" (default), "lv2", "clap", or
// "native" (pure-Rust analog-style synth; see docs/ROADMAP.md).
type PatchConfig struct {
	Name       string  `toml:"name"`
	Display    string  `toml:"display"`
	Soundfont  string  `toml:"soundfont"`
	PadColor   uint8   `toml:"pad_color"`   // 0..127 — Components palette index
	GainDB     float32 `toml:"gain_db"`     // dB trim applied at patch select (-24..+24 typical, 0 = unity)
	Type       string  `toml:"type"`        // patch backend type: "soundfont" (default), "lv2", "clap", or "native"
	PluginURI  string  `toml:"plugin_uri"`  // LV2 plugin URI (used when Type == "lv2")
	PluginPath string  `toml:"plugin_path"` // CLAP bundle path (used when Type == "clap")
	PluginID   string  `toml:"plugin_id"`   // CLAP plugin id (used when Type == "clap")
	Engine     string  `toml:"engine"`      // Native synth engine name, e.g. "minimoog" (used when Type == "native")

	// Per-patch velocity curve override — wins over [midi.velocity].
	// VelocityGamma > 0 with no VelocityCurve implies curve = "custom".
	// VelocityPoints is the point-curve override (same shape and
	// same-scope exclusivity rules as VelocityConfig.Points); it wins
	// over VelocityCurve/VelocityGamma in the daemon's precedence order.
	VelocityCurve  string  `toml:"velocity_curve"` // "linear" | "soft" | "hard" | "custom"; "" = inherit global
	VelocityGamma  float32 `toml:"velocity_gamma"` // required iff velocity_curve = "custom"
	VelocityPoints [][]int `toml:"velocity_points"`
}

const (
	PatchTypeSoundfont = "soundfont"
	PatchTypeLV2       = "lv2"
	PatchTypeCLAP      = "clap"
	PatchTypeNative    = "native"
)

func Defaults() *Config {
	return &Config{
		Soundfont: SoundfontConfig{Path: ""},
		MIDI:      MIDIConfig{PortMatch: "launchkey"},
		OSC: OSCConfig{
			XR18: XR18Config{
				// Empty host = OSC mixer control disabled by default. A
				// fresh install must opt in by setting osc.xr18.host in
				// polyclav.toml; otherwise the reconciler does no network
				// polling (see internal/osc/reconciler.go Run).
				Host: "",
				Port: 10024,
			},
		},
		// Web UI off by default; loopback listen is the no-auth security
		// boundary (docs/WEB_UI.md). MIDI.Velocity keeps its zero value:
		// Curve "" = linear passthrough.
		Web: WebConfig{Enabled: false, Listen: DefaultWebListen},
	}
}

// ExpandHome expands a leading `~` (or `~/...`) in path to the user's home
// directory. A bare "~" expands to the home directory itself. "~user" (a
// different user) is not supported and is returned unchanged. If
// os.UserHomeDir() fails or path doesn't start with "~", the input is
// returned verbatim — config load should never fail just because we can't
// expand a tilde.
func ExpandHome(path string) string {
	if path == "" || path[0] != '~' {
		return path
	}
	// "~user/..." (not us): unsupported, pass through.
	if len(path) > 1 && path[1] != '/' {
		return path
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return path
	}
	if path == "~" {
		return home
	}
	// path starts with "~/"
	return home + path[1:]
}

func Load(path string) (*Config, error) {
	cfg := Defaults()
	_, err := toml.DecodeFile(path, cfg)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return Defaults(), nil
		}
		return nil, fmt.Errorf("load config %q: %w", path, err)
	}
	// Expand leading `~` in any user-provided filesystem paths. The example
	// config uses `~/.local/share/polyclav/soundfonts/...` so it works for any
	// user without editing — keep this in lockstep with polyclav.example.toml.
	cfg.Soundfont.Path = ExpandHome(cfg.Soundfont.Path)
	for i := range cfg.Patches {
		cfg.Patches[i].Soundfont = ExpandHome(cfg.Patches[i].Soundfont)
	}
	for i := range cfg.Patches {
		c := &cfg.Patches[i]
		if c.Type == "" {
			c.Type = PatchTypeSoundfont
		}
		c.PluginPath = ExpandHome(c.PluginPath)
		switch c.Type {
		case PatchTypeSoundfont:
			if c.Soundfont == "" {
				return nil, fmt.Errorf("load config %q: patch %q: type=soundfont requires soundfont", path, c.Name)
			}
		case PatchTypeLV2:
			if c.PluginURI == "" {
				return nil, fmt.Errorf("load config %q: patch %q: type=lv2 requires plugin_uri", path, c.Name)
			}
		case PatchTypeCLAP:
			if c.PluginPath == "" || c.PluginID == "" {
				return nil, fmt.Errorf("load config %q: patch %q: type=clap requires plugin_path and plugin_id", path, c.Name)
			}
		case PatchTypeNative:
			if c.Engine == "" {
				return nil, fmt.Errorf("load config %q: patch %q: type=native requires engine", path, c.Name)
			}
		default:
			return nil, fmt.Errorf("load config %q: patch %q: unknown type %q (valid: soundfont, lv2, clap, native)", path, c.Name, c.Type)
		}
	}

	// [midi.velocity]: Gamma > 0 with the curve name omitted is the
	// "custom" shorthand, mirroring the per-patch velocity_gamma rule.
	// Normalize before validation so the custom-curve checks apply and
	// downstream consumers see one canonical spelling.
	if cfg.MIDI.Velocity.Curve == "" && cfg.MIDI.Velocity.Gamma > 0 {
		cfg.MIDI.Velocity.Curve = "custom"
	}

	// [osc.mixer] is the preferred name for the OSC mixer block;
	// [osc.xr18] is the legacy name kept for back-compat (Tier 0 of
	// docs/CONFIGURABILITY.md). If [osc.mixer] was set it wins wholesale —
	// fold it into XR18 so all downstream code keeps reading cfg.OSC.XR18.
	// Folded before validation so the heartbeat check below sees the
	// winning value; oscBlock records which spelling supplied it so error
	// messages point at the user's own block name.
	oscBlock := "osc.xr18"
	if cfg.OSC.Mixer != nil {
		oscBlock = "osc.mixer"
		cfg.OSC.XR18 = *cfg.OSC.Mixer
		if cfg.OSC.XR18.Port == 0 {
			// Mirror the [osc.xr18] default from Defaults() so the two
			// spellings behave identically when port is omitted.
			cfg.OSC.XR18.Port = 10024
		}
		cfg.OSC.Mixer = nil
	}

	// Setting validation — collect EVERY offender before failing so the
	// user fixes the config in one pass (the "errors not warnings" rule,
	// same philosophy as MissingDepsError).
	errs := velocityConfigErrors(cfg)
	errs = append(errs, heartbeatConfigErrors(cfg, oscBlock)...)
	if len(errs) > 0 {
		return nil, fmt.Errorf("load config %q: invalid settings:\n  - %s",
			path, strings.Join(errs, "\n  - "))
	}

	// Web: enabling the UI with an empty listen address falls back to the
	// loopback default rather than failing — localhost is the boundary.
	if cfg.Web.Enabled && cfg.Web.Listen == "" {
		cfg.Web.Listen = DefaultWebListen
	}

	return cfg, nil
}

// validVelocityCurves is the accepted set for [midi.velocity].curve and the
// per-patch velocity_curve. "" means unset (linear for the global block;
// "inherit the global" for a patch). Kept in sync with internal/velocity,
// which revalidates at Curve construction — config validates names and
// ranges locally instead of importing it.
var validVelocityCurves = map[string]struct{}{
	"":       {},
	"linear": {},
	"soft":   {},
	"hard":   {},
	"custom": {},
}

// velocityConfigErrors collects every velocity-related config problem —
// global [midi.velocity] plus each patch's velocity_curve/velocity_gamma —
// as one human-readable line per offender. Load joins them into a single
// startup error.
func velocityConfigErrors(cfg *Config) []string {
	var errs []string

	v := cfg.MIDI.Velocity
	if _, ok := validVelocityCurves[v.Curve]; !ok {
		errs = append(errs, fmt.Sprintf("midi.velocity: unknown curve %q (valid: linear, soft, hard, custom)", v.Curve))
	}
	if v.Curve == "custom" && v.Gamma <= 0 {
		errs = append(errs, `midi.velocity: curve "custom" requires gamma > 0`)
	} else if v.Gamma < 0 {
		errs = append(errs, fmt.Sprintf("midi.velocity: gamma must be > 0 (got %g)", v.Gamma))
	}
	minInRange := v.OutMin >= 0 && v.OutMin <= 127
	maxInRange := v.OutMax >= 0 && v.OutMax <= 127
	if !minInRange {
		errs = append(errs, fmt.Sprintf("midi.velocity: out_min must be in 0..127 (got %d)", v.OutMin))
	}
	if !maxInRange {
		errs = append(errs, fmt.Sprintf("midi.velocity: out_max must be in 0..127 (got %d)", v.OutMax))
	}
	// 0 = unset (the velocity package fills its 1/127 defaults), so the
	// ordering constraint only applies when both ends are explicitly set.
	if minInRange && maxInRange && v.OutMin > 0 && v.OutMax > 0 && v.OutMin > v.OutMax {
		errs = append(errs, fmt.Sprintf("midi.velocity: out_min (%d) must be <= out_max (%d)", v.OutMin, v.OutMax))
	}
	if len(v.Points) > 0 {
		// Same-scope exclusivity: within one block "points or curve/gamma"
		// must be an either/or — otherwise which curve wins is ambiguous.
		// Gamma-only configs were normalized to Curve = "custom" above, so
		// checking Curve/Gamma here catches every gamma-shaped spelling.
		if v.Curve != "" || v.Gamma != 0 {
			errs = append(errs, "midi.velocity: points and curve/gamma are mutually exclusive — set one or the other")
		}
		errs = append(errs, velocityPointsErrors("midi.velocity", "points", v.Points)...)
	}

	for i := range cfg.Patches {
		p := &cfg.Patches[i]
		if _, ok := validVelocityCurves[p.VelocityCurve]; !ok {
			errs = append(errs, fmt.Sprintf("patch %q: unknown velocity_curve %q (valid: linear, soft, hard, custom)", p.Name, p.VelocityCurve))
		}
		if p.VelocityCurve == "custom" && p.VelocityGamma <= 0 {
			errs = append(errs, fmt.Sprintf("patch %q: velocity_curve %q requires velocity_gamma > 0", p.Name, "custom"))
		} else if p.VelocityGamma < 0 {
			errs = append(errs, fmt.Sprintf("patch %q: velocity_gamma must be > 0 (got %g)", p.Name, p.VelocityGamma))
		}
		// VelocityGamma > 0 with no VelocityCurve is valid shorthand: it
		// implies curve = "custom" (resolved by the velocity package).
		// The equivalent global shorthand was already normalized to
		// Curve = "custom" by Load before this function runs.
		if len(p.VelocityPoints) > 0 {
			if p.VelocityCurve != "" || p.VelocityGamma != 0 {
				errs = append(errs, fmt.Sprintf("patch %q: velocity_points and velocity_curve/velocity_gamma are mutually exclusive — set one or the other", p.Name))
			}
			errs = append(errs, velocityPointsErrors(fmt.Sprintf("patch %q", p.Name), "velocity_points", p.VelocityPoints)...)
		}
	}
	return errs
}

// velocityPointsErrors validates one piecewise-linear point set (global
// `points` or per-patch `velocity_points`) against the v2 constraints
// from docs/VELOCITY_CURVES.md: 2..16 [x, y] pairs, first [0, 0] (a
// NoteOn with velocity 0 must stay NoteOff), last x = 127 (full input
// coverage), xs strictly increasing, ys non-decreasing (monotonic).
// prefix/field make the messages match the user's own spelling. Same
// all-offenders style as the rest of the velocity checks, except that
// the ordering/endpoint checks are skipped when any pair is malformed
// or out of range — they would only add noise about values the user
// already has to rewrite.
func velocityPointsErrors(prefix, field string, pts [][]int) []string {
	var errs []string
	if len(pts) < 2 || len(pts) > 16 {
		errs = append(errs, fmt.Sprintf("%s: %s must have 2..16 [x, y] pairs (got %d)", prefix, field, len(pts)))
	}
	pairsOK := true
	for i, pt := range pts {
		if len(pt) != 2 {
			errs = append(errs, fmt.Sprintf("%s: %s[%d] must be an [x, y] pair (got %d values)", prefix, field, i, len(pt)))
			pairsOK = false
			continue
		}
		if pt[0] < 0 || pt[0] > 127 {
			errs = append(errs, fmt.Sprintf("%s: %s[%d] x must be in 0..127 (got %d)", prefix, field, i, pt[0]))
			pairsOK = false
		}
		if pt[1] < 0 || pt[1] > 127 {
			errs = append(errs, fmt.Sprintf("%s: %s[%d] y must be in 0..127 (got %d)", prefix, field, i, pt[1]))
			pairsOK = false
		}
	}
	if !pairsOK || len(pts) < 2 {
		return errs
	}
	if pts[0][0] != 0 || pts[0][1] != 0 {
		errs = append(errs, fmt.Sprintf("%s: %s[0] must be [0, 0] (got [%d, %d])", prefix, field, pts[0][0], pts[0][1]))
	}
	if last := pts[len(pts)-1]; last[0] != 127 {
		errs = append(errs, fmt.Sprintf("%s: %s last x must be 127 (got %d)", prefix, field, last[0]))
	}
	for i := 1; i < len(pts); i++ {
		if pts[i][0] <= pts[i-1][0] {
			errs = append(errs, fmt.Sprintf("%s: %s[%d] x (%d) must be > previous x (%d)", prefix, field, i, pts[i][0], pts[i-1][0]))
		}
		if pts[i][1] < pts[i-1][1] {
			errs = append(errs, fmt.Sprintf("%s: %s[%d] y (%d) must be >= previous y (%d)", prefix, field, i, pts[i][1], pts[i-1][1]))
		}
	}
	return errs
}

// heartbeatConfigErrors validates the OSC mixer heartbeat's address
// form. The pointer semantics stay untouched: nil (key absent) means
// the "/xinfo" default and explicit "" means polling disabled — both
// pass. Any other value must look like an OSC address: a leading "/"
// and no spaces. block is the TOML block the value came from
// ("osc.xr18" or "osc.mixer") so the message matches the user's own
// spelling. Same all-offenders style as velocityConfigErrors.
func heartbeatConfigErrors(cfg *Config, block string) []string {
	hb := cfg.OSC.XR18.Heartbeat
	if hb == nil || *hb == "" {
		return nil
	}
	var errs []string
	if !strings.HasPrefix(*hb, "/") {
		errs = append(errs, fmt.Sprintf("%s: heartbeat %q must start with \"/\" (an OSC address, e.g. \"/xinfo\")", block, *hb))
	}
	if strings.Contains(*hb, " ") {
		errs = append(errs, fmt.Sprintf("%s: heartbeat %q must not contain spaces", block, *hb))
	}
	return errs
}

// MissingDep is one patch's failure to resolve a runtime dependency
// (soundfont file, CLAP plugin bundle, or unknown native engine).
// Validate collects all failures into a MissingDepsError so the user
// sees the full set in one pass instead of fixing them one at a time.
type MissingDep struct {
	PatchName string // [[patches]].name from polyclav.toml
	PatchType string // "soundfont" | "clap" | "native" (other types currently never fail Validate)
	Path      string // failing filesystem path (empty for non-filesystem checks)
	Reason    string // short human reason — used in the formatted error
}

// MissingDepsError is returned by Validate when one or more patches
// reference dependencies that don't resolve. main.go matches on this
// type to render a multi-line, human-readable startup error and exit 1
// (see "functioning config or refuse" in the startup story).
type MissingDepsError struct {
	ConfigPath string
	Missing    []MissingDep
}

func (e *MissingDepsError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d patches reference missing files:\n", len(e.Missing))
	for _, m := range e.Missing {
		switch m.PatchType {
		case PatchTypeSoundfont:
			fmt.Fprintf(&b, "  - %q: soundfont file not found: %s\n", m.PatchName, m.Path)
		case PatchTypeCLAP:
			fmt.Fprintf(&b, "  - %q: CLAP plugin not found: %s\n", m.PatchName, m.Path)
		case PatchTypeNative:
			fmt.Fprintf(&b, "  - %q: %s\n", m.PatchName, m.Reason)
		default:
			fmt.Fprintf(&b, "  - %q: %s\n", m.PatchName, m.Reason)
		}
	}
	return strings.TrimRight(b.String(), "\n")
}

// Validate runs the strict dependency check over a Config that has
// already been parsed and type-validated by Load. Each [[patches]]
// entry's external dependencies are resolved against the filesystem
// (for soundfont and CLAP types) or the known-engines set (for native).
// LV2 patches are not checked here: LV2 URIs are abstract identifiers
// resolved by the plugin host at instantiation time, not paths on disk.
//
// All failures are collected before returning so the user sees the
// complete picture in one pass — see the "Errors not warnings" rule in
// the startup story.
func Validate(cfg *Config) error {
	if cfg == nil {
		return nil
	}
	var missing []MissingDep
	for _, p := range cfg.Patches {
		switch p.Type {
		case PatchTypeSoundfont:
			if _, err := os.Stat(p.Soundfont); err != nil {
				missing = append(missing, MissingDep{
					PatchName: p.Name,
					PatchType: PatchTypeSoundfont,
					Path:      p.Soundfont,
					Reason:    "soundfont file not found",
				})
			}
		case PatchTypeCLAP:
			if _, err := os.Stat(p.PluginPath); err != nil {
				missing = append(missing, MissingDep{
					PatchName: p.Name,
					PatchType: PatchTypeCLAP,
					Path:      p.PluginPath,
					Reason:    "CLAP plugin not found",
				})
			}
		case PatchTypeNative:
			if !IsKnownNativeEngine(p.Engine) {
				engines := make([]string, 0, len(knownNativeEngines))
				for e := range knownNativeEngines {
					engines = append(engines, e)
				}
				sort.Strings(engines)
				missing = append(missing, MissingDep{
					PatchName: p.Name,
					PatchType: PatchTypeNative,
					Reason: fmt.Sprintf("unknown native engine %q (known: %s)",
						p.Engine, strings.Join(engines, ", ")),
				})
			}
		case PatchTypeLV2:
			// LV2 URIs are resolved by the host at instantiation time.
			// No filesystem check is meaningful here — Load() already
			// ensured the URI is non-empty.
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return &MissingDepsError{Missing: missing}
}
