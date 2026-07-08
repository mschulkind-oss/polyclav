package main

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/config"
)

// sfzPatchNames returns the names of [[patches]] whose soundfont is an .sfz
// file — i.e. patches that need libsfizz. Used by the startup warning and by
// `polyclav doctor` to report what degrades when libsfizz is absent.
func sfzPatchNames(cfg *config.Config) []string {
	if cfg == nil {
		return nil
	}
	var out []string
	for _, p := range cfg.Patches {
		if (p.Type == config.PatchTypeSoundfont || p.Type == "") &&
			strings.HasSuffix(strings.ToLower(p.Soundfont), ".sfz") {
			out = append(out, p.Name)
		}
	}
	return out
}

// runDoctor prints a capability/environment report and exits 0. It probes the
// optional sfizz backend and analyses the config's patches so the user can see
// at a glance what works and what would be silent.
func runDoctor(args []string) int {
	fs := flag.NewFlagSet("doctor", flag.ExitOnError)
	configPath := fs.String("config", "", "path to polyclav.toml (default: XDG config dir)")
	_ = fs.Parse(args)

	out := os.Stdout
	fmt.Fprintln(out, "polyclav doctor")
	fmt.Fprintln(out)

	// --- Audio backends ---
	sfizzOK := audio.SfizzAvailable()
	fmt.Fprintln(out, "Audio backends:")
	fmt.Fprintln(out, "  ok    oxisynth (SF2/SF3)       built in")
	fmt.Fprintln(out, "  ok    native synth (minimoog)  built in")
	for _, line := range pluginHostLines(runtime.GOOS) {
		fmt.Fprintln(out, line)
	}
	if sfizzOK {
		fmt.Fprintln(out, "  ok    sfizz (SFZ)              available (libsfizz loaded)")
	} else {
		fmt.Fprintln(out, "  MISS  sfizz (SFZ)              not available — .sfz patches will be silent")
		for _, line := range sfizzInstallHint(runtime.GOOS) {
			fmt.Fprintln(out, line)
		}
	}
	fmt.Fprintln(out)
	for _, line := range requiredLibsFooter(runtime.GOOS) {
		fmt.Fprintln(out, line)
	}
	fmt.Fprintln(out)

	// --- Config ---
	path := *configPath
	if path == "" {
		if cfgDir, err := os.UserConfigDir(); err == nil {
			path = filepath.Join(cfgDir, "polyclav", "polyclav.toml")
		}
	}
	switch {
	case path == "":
		fmt.Fprintln(out, "Config: (could not resolve a config path)")
	case fileMissing(path):
		fmt.Fprintf(out, "Config: %s (not present yet — run `polyclav` once to seed it)\n", path)
	default:
		reportConfig(out, path, sfizzOK)
	}

	// --- Soundfonts dir ---
	fmt.Fprintln(out)
	sfDir := config.ExpandHome(defaultSoundfontDest)
	if st, err := os.Stat(sfDir); err == nil && st.IsDir() {
		fmt.Fprintf(out, "Soundfonts: %s (present)\n", sfDir)
	} else {
		fmt.Fprintf(out, "Soundfonts: %s (missing — run `polyclav bootstrap`)\n", sfDir)
	}

	return 0
}

// pluginHostLines reports LV2/CLAP plugin-host availability. This is a
// build-time platform decision, not a runtime dependency: on macOS,
// livi/lilv (LV2) and clack-host (CLAP) are excluded from the build
// entirely (audio-core/Cargo.toml's cfg(target_os) split), so there is
// nothing to "install" — the parameter is runtime.GOOS, injected so tests
// can exercise both branches regardless of the OS actually running them.
func pluginHostLines(goos string) []string {
	if goos == "darwin" {
		return []string{
			"  n/a   LV2 plugin host          not built for macOS (Linux-only in this build)",
			"  n/a   CLAP plugin host         not built for macOS (Linux-only in this build)",
		}
	}
	return []string{
		"  ok    LV2 plugin host          built in (lilv linked)",
		"  ok    CLAP plugin host         built in (clack-host)",
	}
}

// sfizzInstallHint gives a concrete, platform-specific fix for a missing
// libsfizz — not a generic "your package manager" punt. macOS's surface is
// narrow enough to name exactly (no Homebrew formula, but sfztools' own
// release tarball works — see audio-core/src/sfizz_sys.rs and
// docs/MACOS_PORT.md for how dyld finds it); Linux gets the real package
// names per distro family instead of a vague pointer, matching
// docs/INSTALL.md's system-libraries table.
func sfizzInstallHint(goos string) []string {
	switch goos {
	case "darwin":
		return []string{
			"        -> install sfizz (no Homebrew formula yet):",
			"             download the latest sfizz-*-macos.tar.gz from",
			"             https://github.com/sfztools/sfizz/releases and extract it to",
			"             /usr/local, e.g.: sudo tar xzf sfizz-*-macos.tar.gz -C /",
		}
	case "linux":
		return []string{
			"        -> install sfizz:",
			"             Debian/Ubuntu: sudo apt install libsfizz-dev  (third-party repo)",
			"             Fedora:        sudo dnf install sfizz-devel",
			"             Arch:          yay -S sfizz  (AUR)",
			"             or build from source: https://github.com/sfztools/sfizz",
		}
	default:
		return []string{
			"        -> install sfizz for your platform: https://github.com/sfztools/sfizz",
		}
	}
}

// requiredLibsFooter names the platform-specific libraries/frameworks
// that must have loaded for this binary to have started at all.
func requiredLibsFooter(goos string) []string {
	if goos == "darwin" {
		return []string{
			"  (You're seeing this report, so the binary's required frameworks —",
			"   CoreAudio, CoreMIDI — loaded OK; otherwise it would not have started.)",
		}
	}
	return []string{
		"  (You're seeing this report, so the binary's required libs — PipeWire,",
		"   ALSA, lilv — loaded OK; otherwise it would not have started.)",
	}
}

func fileMissing(path string) bool {
	_, err := os.Stat(path)
	return err != nil
}

func reportConfig(out io.Writer, path string, sfizzOK bool) {
	cfg, err := config.Load(path)
	if err != nil {
		fmt.Fprintf(out, "Config: %s\n  ERROR loading: %v\n", path, err)
		return
	}

	var sf2, sfz, native, lv2, clap int
	for _, p := range cfg.Patches {
		switch p.Type {
		case config.PatchTypeNative:
			native++
		case config.PatchTypeLV2:
			lv2++
		case config.PatchTypeCLAP:
			clap++
		default: // soundfont (explicit or implied)
			if strings.HasSuffix(strings.ToLower(p.Soundfont), ".sfz") {
				sfz++
			} else {
				sf2++
			}
		}
	}
	fmt.Fprintf(out, "Config: %s\n", path)
	fmt.Fprintf(out, "  %d patches: %d SF2/SF3, %d SFZ, %d native, %d LV2, %d CLAP\n",
		len(cfg.Patches), sf2, sfz, native, lv2, clap)

	if names := sfzPatchNames(cfg); len(names) > 0 && !sfizzOK {
		fmt.Fprintf(out, "\n  ! %d SFZ patch(es) will NOT play without libsfizz:\n", len(names))
		for _, n := range names {
			fmt.Fprintf(out, "      - %s\n", n)
		}
		fmt.Fprintln(out, "    SF2/SF3, native, and plugin patches are unaffected.")
	}
}
