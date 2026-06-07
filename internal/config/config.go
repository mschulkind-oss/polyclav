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
	MIDI      MIDIConfig       `toml:"midi"`
	OSC       OSCConfig        `toml:"osc"`
	Patches   []PatchConfig    `toml:"patches"`
	Mastering *MasteringConfig `toml:"mastering"`
}

type SoundfontConfig struct {
	Path string `toml:"path"`
}

type MIDIConfig struct {
	// PortMatch is a substring (case-insensitive) matched against MIDI
	// input port names. Empty = first available. Defaults to "launchkey".
	PortMatch string `toml:"port_match"`
}

type OSCConfig struct {
	XR18 XR18Config `toml:"xr18"`
}

type XR18Config struct {
	Host     string        `toml:"host"`
	Port     int           `toml:"port"`
	Bindings []osc.Binding `toml:"bindings"`
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
	return cfg, nil
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
