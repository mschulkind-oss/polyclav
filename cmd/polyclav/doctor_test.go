package main

import (
	"strings"
	"testing"
)

// join concatenates lines with newlines so a substring check reads
// naturally against the doctor helpers' multi-line output.
func join(lines []string) string {
	return strings.Join(lines, "\n")
}

func TestPluginHostLinesReflectsMacOSNarrowerSurface(t *testing.T) {
	linux := join(pluginHostLines("linux"))
	if !strings.Contains(linux, "ok    LV2 plugin host") || !strings.Contains(linux, "ok    CLAP plugin host") {
		t.Errorf("linux plugin host lines should report both built in, got:\n%s", linux)
	}

	darwin := join(pluginHostLines("darwin"))
	if strings.Contains(darwin, "ok    LV2") || strings.Contains(darwin, "ok    CLAP") {
		t.Errorf("darwin must not claim LV2/CLAP are built in (they're Linux-only in the build), got:\n%s", darwin)
	}
	if !strings.Contains(darwin, "not built for macOS") {
		t.Errorf("darwin plugin host lines should explain why, got:\n%s", darwin)
	}
}

func TestSfizzInstallHintIsConcretePerPlatform(t *testing.T) {
	darwin := join(sfizzInstallHint("darwin"))
	if !strings.Contains(darwin, "brew") && !strings.Contains(darwin, "no Homebrew formula") {
		t.Errorf("darwin hint should explain the Homebrew situation, got:\n%s", darwin)
	}
	if !strings.Contains(darwin, "github.com/sfztools/sfizz/releases") {
		t.Errorf("darwin hint should point at the release tarball, got:\n%s", darwin)
	}
	if strings.Contains(darwin, "your distro's package") || strings.Contains(darwin, "your package manager") {
		t.Errorf("darwin hint must not fall back to the generic Linux phrasing, got:\n%s", darwin)
	}

	linux := join(sfizzInstallHint("linux"))
	for _, want := range []string{"apt install", "dnf install", "AUR"} {
		if !strings.Contains(linux, want) {
			t.Errorf("linux hint missing %q, got:\n%s", want, linux)
		}
	}
	if strings.Contains(linux, "your distro's package manager") {
		t.Errorf("linux hint should name concrete package managers, not the old generic phrasing, got:\n%s", linux)
	}

	// An unrecognized GOOS must still return something actionable, not panic.
	other := join(sfizzInstallHint("windows"))
	if !strings.Contains(other, "github.com/sfztools/sfizz") {
		t.Errorf("fallback hint should still point somewhere useful, got:\n%s", other)
	}
}

func TestRequiredLibsFooterNamesPlatformLibs(t *testing.T) {
	darwin := join(requiredLibsFooter("darwin"))
	if !strings.Contains(darwin, "CoreAudio") || !strings.Contains(darwin, "CoreMIDI") {
		t.Errorf("darwin footer should name CoreAudio/CoreMIDI, got:\n%s", darwin)
	}
	if strings.Contains(darwin, "PipeWire") {
		t.Errorf("darwin footer should not mention Linux-only libs, got:\n%s", darwin)
	}

	linux := join(requiredLibsFooter("linux"))
	if !strings.Contains(linux, "PipeWire") || !strings.Contains(linux, "ALSA") || !strings.Contains(linux, "lilv") {
		t.Errorf("linux footer should name PipeWire/ALSA/lilv, got:\n%s", linux)
	}
}
