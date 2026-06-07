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
}
