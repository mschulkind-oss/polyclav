package patches

import (
	"fmt"
	"math"
	"os"
	"sync"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/config"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
)

// Package patches owns a registry of selectable soundfont patches (entries
// pulled from polyclav.toml and displayed across the top-row Launchkey pads).
// Selecting a patch tells internal/audio to load the soundfont and reloads
// the engine. The package does NOT manage pad LEDs or MIDI dispatch — those
// live in driver/launchkey code that consumes Registry.All() and
// Registry.Current().
// Phase 1 extends the registry: a patch can be a soundfont (default),
// an LV2 plugin (by URI), or a CLAP plugin (by bundle path + plugin id).

// Patch is a single user-selectable patch entry — a soundfont, an LV2
// plugin, a CLAP plugin, or a native pure-Rust synth. The Type field
// selects the backend; the backend-specific fields (Soundfont,
// PluginURI, PluginPath/PluginID, Engine) are only consulted for that
// backend.
type Patch struct {
	Name       string           // internal id, e.g. "ydp-grand"
	Display    string           // shown on the Launchkey screen, e.g. "YDP Grand"
	Soundfont  string           // absolute path; .sf2/.sf3 -> oxisynth, .sfz -> sfizz
	PadColor   components.Color // palette index for the pad's lit state
	GainDB     float32          // dB trim applied to per-patch gain on select (0 = unity)
	Type       string           // "soundfont" (default), "lv2", "clap", or "native"
	PluginURI  string           // LV2 plugin URI (Type == "lv2")
	PluginPath string           // CLAP bundle path (Type == "clap")
	PluginID   string           // CLAP plugin id  (Type == "clap")
	Engine     string           // Native synth engine name (Type == "native"), e.g. "minimoog"

	// Per-patch velocity curve override (wins over [midi.velocity]); ""
	// inherits the global curve. Consumed by the daemon's patch-select
	// hook, not by this package — see docs/VELOCITY_CURVES.md.
	VelocityCurve string  // "linear" | "soft" | "hard" | "custom"; "" = inherit
	VelocityGamma float32 // > 0 with empty VelocityCurve implies "custom"
	// VelocityPoints mirrors config.PatchConfig.VelocityPoints: v2
	// piecewise-linear control points, already validated at config load.
	// Non-empty wins over VelocityCurve/VelocityGamma in the daemon's
	// precedence order; nil = no point override.
	VelocityPoints [][]int
}

// audioBackend is the slice of internal/audio that Registry needs. The default
// implementation calls into the real audio package; tests inject a fake.
type audioBackend interface {
	SetSoundfont(path string)
	ReloadSoundfont() error
	SetPatchGain(linear float32)
	SetLv2Plugin(uri string) error
	SetClapPlugin(bundlePath, pluginID string) error
	SetNativePatch(engine string) error
}

// realAudioBackend is the production wiring; calls audio.SetSoundfont and
// audio.ReloadSoundfont directly.
type realAudioBackend struct{}

func (realAudioBackend) SetSoundfont(path string)      { audio.SetSoundfont(path) }
func (realAudioBackend) ReloadSoundfont() error        { return audio.ReloadSoundfont() }
func (realAudioBackend) SetPatchGain(linear float32)   { audio.SetPatchGain(linear) }
func (realAudioBackend) SetLv2Plugin(uri string) error { return audio.SetLv2Plugin(uri) }
func (realAudioBackend) SetClapPlugin(bundlePath, pluginID string) error {
	return audio.SetClapPlugin(bundlePath, pluginID)
}
func (realAudioBackend) SetNativePatch(engine string) error { return audio.SetNativePatch(engine) }

// Registry holds the ordered list of patches and tracks which one is loaded.
// Methods are goroutine-safe via an internal mutex.
type Registry struct {
	mu      sync.Mutex
	patches []Patch
	current int // -1 == nothing loaded
	audio   audioBackend
}

// New builds a Registry from the given ordered patch list. The current
// selection starts unset (Current() returns nil) — call Select / SelectIndex
// to load the first patch.
func New(patches []Patch) *Registry {
	return &Registry{
		patches: append([]Patch(nil), patches...), // defensive copy
		current: -1,
		audio:   realAudioBackend{},
	}
}

// newWithBackend is the test seam — same as New but lets callers inject a fake
// audioBackend. Kept unexported so external callers can't accidentally bypass
// the real audio package.
func newWithBackend(patches []Patch, backend audioBackend) *Registry {
	r := New(patches)
	r.audio = backend
	return r
}

// All returns the ordered patch list. The returned slice is a copy — callers
// can hold onto it without races against Select.
func (r *Registry) All() []Patch {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]Patch, len(r.patches))
	copy(out, r.patches)
	return out
}

// Current returns a pointer to the currently selected patch, or nil if the
// registry is empty or nothing has been selected yet. The returned pointer
// references a fresh copy; safe to read without locking.
func (r *Registry) Current() *Patch {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.current < 0 || r.current >= len(r.patches) {
		return nil
	}
	p := r.patches[r.current]
	return &p
}

// Select finds a patch by Name and applies it. Returns an error if the name
// is unknown, if the soundfont file is missing, or if the audio engine
// rejects the reload.
func (r *Registry) Select(name string) error {
	r.mu.Lock()
	idx := -1
	for i, p := range r.patches {
		if p.Name == name {
			idx = i
			break
		}
	}
	r.mu.Unlock()
	if idx < 0 {
		return fmt.Errorf("patch %q not found", name)
	}
	return r.SelectIndex(idx)
}

// SelectIndex applies the patch at position i (0-based). Returns an error if
// i is out of bounds, if the soundfont file is missing, or if the audio
// engine rejects the reload.
func (r *Registry) SelectIndex(i int) error {
	r.mu.Lock()
	if i < 0 || i >= len(r.patches) {
		r.mu.Unlock()
		return fmt.Errorf("patch index %d out of range [0, %d)", i, len(r.patches))
	}
	p := r.patches[i]
	backend := r.audio
	r.mu.Unlock()

	switch p.Type {
	case "", "soundfont":
		// Stat the soundfont before touching the audio engine — better error
		// message than whatever oxisynth/sfizz produce on missing files, and we
		// avoid clearing the previous patch on a typo'd path.
		if _, err := os.Stat(p.Soundfont); err != nil {
			return fmt.Errorf("patch %q: soundfont %q: %w", p.Name, p.Soundfont, err)
		}
		backend.SetSoundfont(p.Soundfont)
		if err := backend.ReloadSoundfont(); err != nil {
			return fmt.Errorf("patch %q: reload soundfont: %w", p.Name, err)
		}
	case "lv2":
		if p.PluginURI == "" {
			return fmt.Errorf("patch %q: type=lv2 missing plugin_uri", p.Name)
		}
		if err := backend.SetLv2Plugin(p.PluginURI); err != nil {
			return fmt.Errorf("patch %q: set lv2 plugin: %w", p.Name, err)
		}
	case "clap":
		if p.PluginPath == "" || p.PluginID == "" {
			return fmt.Errorf("patch %q: type=clap missing plugin_path or plugin_id", p.Name)
		}
		if err := backend.SetClapPlugin(p.PluginPath, p.PluginID); err != nil {
			return fmt.Errorf("patch %q: set clap plugin: %w", p.Name, err)
		}
	case "native":
		if p.Engine == "" {
			return fmt.Errorf("patch %q: type=native missing engine", p.Name)
		}
		if err := backend.SetNativePatch(p.Engine); err != nil {
			return fmt.Errorf("patch %q: set native patch: %w", p.Name, err)
		}
	default:
		return fmt.Errorf("patch %q: unknown type %q", p.Name, p.Type)
	}

	// Push per-patch gain. 0 dB -> 1.0 linear (unity).
	linear := float32(math.Pow(10.0, float64(p.GainDB)/20.0))
	backend.SetPatchGain(linear)

	r.mu.Lock()
	r.current = i
	r.mu.Unlock()
	return nil
}

// FromConfig converts the TOML-decoded []config.PatchConfig into the Patch
// model the registry expects. PadColor is mapped 1:1; any uint8 (0..255) is
// accepted at decode time but only 0..127 are valid Components palette
// indices — out-of-range values render as off on the device.
func FromConfig(cfgs []config.PatchConfig) []Patch {
	out := make([]Patch, 0, len(cfgs))
	for _, c := range cfgs {
		out = append(out, Patch{
			Name:           c.Name,
			Display:        c.Display,
			Soundfont:      c.Soundfont,
			PadColor:       components.Color(c.PadColor),
			GainDB:         c.GainDB,
			Type:           c.Type,
			PluginURI:      c.PluginURI,
			PluginPath:     c.PluginPath,
			PluginID:       c.PluginID,
			Engine:         c.Engine,
			VelocityCurve:  c.VelocityCurve,
			VelocityGamma:  c.VelocityGamma,
			VelocityPoints: c.VelocityPoints,
		})
	}
	return out
}
