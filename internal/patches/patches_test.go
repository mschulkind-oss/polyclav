package patches

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/config"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
)

type fakeAudio struct {
	soundfont         string
	setCalls          int
	reloadCalls       int
	reloadErr         error
	setPatchGainCalls int
	lastPatchGain     float32
	lv2URI            string
	lv2Calls          int
	lv2Err            error
	clapPath          string
	clapID            string
	clapCalls         int
	clapErr           error
	nativeEngine      string
	nativeCalls       int
	nativeErr         error
}

func (f *fakeAudio) SetSoundfont(path string) {
	f.soundfont = path
	f.setCalls++
}

func (f *fakeAudio) ReloadSoundfont() error {
	f.reloadCalls++
	return f.reloadErr
}

func (f *fakeAudio) SetPatchGain(linear float32) {
	f.setPatchGainCalls++
	f.lastPatchGain = linear
}

func (f *fakeAudio) SetLv2Plugin(uri string) error {
	f.lv2Calls++
	f.lv2URI = uri
	return f.lv2Err
}

func (f *fakeAudio) SetClapPlugin(bundlePath, pluginID string) error {
	f.clapCalls++
	f.clapPath = bundlePath
	f.clapID = pluginID
	return f.clapErr
}

func (f *fakeAudio) SetNativePatch(engine string) error {
	f.nativeCalls++
	f.nativeEngine = engine
	return f.nativeErr
}

func makePatch(t *testing.T, dir, name string, color components.Color) Patch {
	t.Helper()
	p := filepath.Join(dir, name+".sf2")
	if err := os.WriteFile(p, []byte("fake sf2"), 0o644); err != nil {
		t.Fatalf("write fake soundfont: %v", err)
	}
	return Patch{
		Name:      name,
		Display:   name + " display",
		Soundfont: p,
		PadColor:  color,
	}
}

func TestNewAndAllPreserveOrder(t *testing.T) {
	p1 := Patch{Name: "p1", Display: "P1", Soundfont: "/p1.sf2", PadColor: components.ColorVibrantRed}
	p2 := Patch{Name: "p2", Display: "P2", Soundfont: "/p2.sf2", PadColor: components.ColorVibrantGreen}
	p3 := Patch{Name: "p3", Display: "P3", Soundfont: "/p3.sf2", PadColor: components.ColorVibrantBlue}

	r := New([]Patch{p1, p2, p3})

	all := r.All()
	if len(all) != 3 {
		t.Fatalf("expected 3 patches, got %d", len(all))
	}
	if all[0].Name != "p1" || all[1].Name != "p2" || all[2].Name != "p3" {
		t.Errorf("patches not in expected order: %v, %v, %v", all[0].Name, all[1].Name, all[2].Name)
	}

	all[0].Name = "tampered"
	allAgain := r.All()
	if allAgain[0].Name != "p1" {
		t.Errorf("All() did not return a copy; original was modified")
	}
}

func TestCurrentEmptyRegistry(t *testing.T) {
	r := New(nil)
	if r.Current() != nil {
		t.Errorf("expected nil Current() for empty registry, got %v", r.Current())
	}
}

func TestCurrentBeforeAnySelection(t *testing.T) {
	p1 := Patch{Name: "p1", Display: "P1", Soundfont: "/p1.sf2", PadColor: components.ColorVibrantRed}
	r := New([]Patch{p1})
	if r.Current() != nil {
		t.Errorf("expected nil Current() before selection, got %v", r.Current())
	}
}

func TestSelectByNameAppliesPatch(t *testing.T) {
	dir := t.TempDir()
	p1 := makePatch(t, dir, "alpha", components.ColorVibrantBlue)
	p2 := makePatch(t, dir, "beta", components.ColorVibrantRed)

	fa := &fakeAudio{}
	r := newWithBackend([]Patch{p1, p2}, fa)

	if err := r.Select("beta"); err != nil {
		t.Fatalf("Select(beta) failed: %v", err)
	}

	if fa.soundfont != p2.Soundfont {
		t.Errorf("expected soundfont %q, got %q", p2.Soundfont, fa.soundfont)
	}
	if fa.setCalls != 1 {
		t.Errorf("expected 1 SetSoundfont call, got %d", fa.setCalls)
	}
	if fa.reloadCalls != 1 {
		t.Errorf("expected 1 ReloadSoundfont call, got %d", fa.reloadCalls)
	}

	cur := r.Current()
	if cur == nil {
		t.Fatal("expected non-nil Current() after selection")
	}
	if cur.Name != "beta" {
		t.Errorf("expected current patch name %q, got %q", "beta", cur.Name)
	}
}

func TestSelectUnknownNameReturnsError(t *testing.T) {
	dir := t.TempDir()
	p1 := makePatch(t, dir, "alpha", components.ColorVibrantBlue)
	fa := &fakeAudio{}
	r := newWithBackend([]Patch{p1}, fa)

	err := r.Select("ghost")
	if err == nil {
		t.Fatal("expected error for unknown patch name, got nil")
	}
	if !strings.Contains(err.Error(), `patch "ghost" not found`) {
		t.Errorf("error message does not contain expected text: %v", err)
	}
}

func TestSelectMissingSoundfontReturnsError(t *testing.T) {
	p := Patch{Name: "x", Soundfont: "/nope/does/not/exist.sf2"}
	fa := &fakeAudio{}
	r := newWithBackend([]Patch{p}, fa)

	err := r.Select("x")
	if err == nil {
		t.Fatal("expected error for missing soundfont, got nil")
	}
	if fa.setCalls != 0 {
		t.Errorf("expected 0 SetSoundfont calls, got %d", fa.setCalls)
	}
	if fa.reloadCalls != 0 {
		t.Errorf("expected 0 ReloadSoundfont calls, got %d", fa.reloadCalls)
	}
	if r.Current() != nil {
		t.Errorf("expected nil Current() after failed selection, got %v", r.Current())
	}
}

func TestSelectReloadErrorPropagates(t *testing.T) {
	dir := t.TempDir()
	p := makePatch(t, dir, "x", 0)
	fa := &fakeAudio{reloadErr: errors.New("boom")}
	r := newWithBackend([]Patch{p}, fa)

	err := r.Select("x")
	if err == nil {
		t.Fatal("expected error for reload failure, got nil")
	}
	if !strings.Contains(err.Error(), "boom") {
		t.Errorf("error message does not contain expected text: %v", err)
	}
	if r.Current() != nil {
		t.Errorf("expected nil Current() after failed reload, got %v", r.Current())
	}
}

func TestSelectIndexBounds(t *testing.T) {
	dir := t.TempDir()
	p1 := makePatch(t, dir, "p1", components.ColorVibrantRed)
	fa := &fakeAudio{}
	r := newWithBackend([]Patch{p1}, fa)

	for _, idx := range []int{-1, 1, 99} {
		err := r.SelectIndex(idx)
		if err == nil {
			t.Errorf("expected error for index %d, got nil", idx)
		}
	}

	if err := r.SelectIndex(0); err != nil {
		t.Errorf("SelectIndex(0) failed: %v", err)
	}
}

func TestFromConfig(t *testing.T) {
	cfgs := []config.PatchConfig{
		{Name: "a", Display: "A", Soundfont: "/path/a.sf2", PadColor: 3},
		{Name: "b", Display: "B", Soundfont: "/path/b.sfz", PadColor: 41,
			VelocityCurve: "custom", VelocityGamma: 0.7},
	}

	ps := FromConfig(cfgs)
	if len(ps) != 2 {
		t.Fatalf("expected 2 patches, got %d", len(ps))
	}

	if ps[0].Name != "a" {
		t.Errorf("expected patch[0].Name %q, got %q", "a", ps[0].Name)
	}
	if ps[0].PadColor != components.Color(3) {
		t.Errorf("expected patch[0].PadColor %d, got %d", 3, ps[0].PadColor)
	}
	if ps[0].VelocityCurve != "" || ps[0].VelocityGamma != 0 {
		t.Errorf("patch[0] should carry zero velocity override, got curve=%q gamma=%g",
			ps[0].VelocityCurve, ps[0].VelocityGamma)
	}

	if ps[1].PadColor != components.ColorVibrantBlue {
		t.Errorf("expected patch[1].PadColor %d (VibrantBlue), got %d", components.ColorVibrantBlue, ps[1].PadColor)
	}
	if ps[1].VelocityCurve != "custom" {
		t.Errorf("expected patch[1].VelocityCurve %q, got %q", "custom", ps[1].VelocityCurve)
	}
	if ps[1].VelocityGamma != 0.7 {
		t.Errorf("expected patch[1].VelocityGamma 0.7, got %g", ps[1].VelocityGamma)
	}

	psNil := FromConfig(nil)
	if len(psNil) != 0 {
		t.Errorf("expected empty slice for nil input, got %d elements", len(psNil))
	}
}

func TestSelectIndexLv2Patch(t *testing.T) {
	p := Patch{
		Name:      "dexed-lv2",
		Display:   "Dexed",
		Type:      "lv2",
		PluginURI: "https://github.com/asb2m10/dexed",
		GainDB:    0.0,
	}
	fa := &fakeAudio{}
	r := newWithBackend([]Patch{p}, fa)

	if err := r.SelectIndex(0); err != nil {
		t.Fatalf("SelectIndex(0) failed: %v", err)
	}
	if fa.lv2Calls != 1 {
		t.Errorf("expected 1 SetLv2Plugin call, got %d", fa.lv2Calls)
	}
	if fa.lv2URI != p.PluginURI {
		t.Errorf("expected lv2 uri %q, got %q", p.PluginURI, fa.lv2URI)
	}
	if fa.setCalls != 0 || fa.reloadCalls != 0 {
		t.Errorf("soundfont path should NOT be touched for lv2 patch (set=%d reload=%d)", fa.setCalls, fa.reloadCalls)
	}
	if fa.setPatchGainCalls != 1 {
		t.Errorf("expected 1 SetPatchGain call (gain applies to plugin patches), got %d", fa.setPatchGainCalls)
	}
	cur := r.Current()
	if cur == nil || cur.Name != "dexed-lv2" {
		t.Errorf("expected current patch %q, got %v", "dexed-lv2", cur)
	}
}

func TestSelectIndexClapPatch(t *testing.T) {
	p := Patch{
		Name:       "dexed-clap",
		Display:    "Dexed",
		Type:       "clap",
		PluginPath: "/nix/store/fake-dexed/lib/clap/Dexed.clap",
		PluginID:   "com.asb2m10.dexed",
		GainDB:     -3.0,
	}
	fa := &fakeAudio{}
	r := newWithBackend([]Patch{p}, fa)

	if err := r.SelectIndex(0); err != nil {
		t.Fatalf("SelectIndex(0) failed: %v", err)
	}
	if fa.clapCalls != 1 {
		t.Errorf("expected 1 SetClapPlugin call, got %d", fa.clapCalls)
	}
	if fa.clapPath != p.PluginPath {
		t.Errorf("expected clap path %q, got %q", p.PluginPath, fa.clapPath)
	}
	if fa.clapID != p.PluginID {
		t.Errorf("expected clap id %q, got %q", p.PluginID, fa.clapID)
	}
	if fa.setCalls != 0 || fa.reloadCalls != 0 {
		t.Errorf("soundfont path should NOT be touched for clap patch (set=%d reload=%d)", fa.setCalls, fa.reloadCalls)
	}
	if fa.setPatchGainCalls != 1 {
		t.Errorf("expected 1 SetPatchGain call, got %d", fa.setPatchGainCalls)
	}
	want := float32(0.7079458)
	if fa.lastPatchGain < want*0.99 || fa.lastPatchGain > want*1.01 {
		t.Errorf("expected lastPatchGain ≈ %.4f, got %.4f", want, fa.lastPatchGain)
	}
}

func TestSelectIndexLv2MissingURI(t *testing.T) {
	p := Patch{Name: "broken-lv2", Type: "lv2"}
	fa := &fakeAudio{}
	r := newWithBackend([]Patch{p}, fa)

	err := r.SelectIndex(0)
	if err == nil {
		t.Fatal("expected error for lv2 patch missing plugin_uri, got nil")
	}
	if fa.lv2Calls != 0 {
		t.Errorf("expected 0 SetLv2Plugin calls, got %d", fa.lv2Calls)
	}
	if r.Current() != nil {
		t.Errorf("expected nil Current() after failed selection, got %v", r.Current())
	}
}

func TestSelectIndexClapMissingFields(t *testing.T) {
	cases := []struct {
		name  string
		patch Patch
	}{
		{"no path", Patch{Name: "x", Type: "clap", PluginID: "id"}},
		{"no id", Patch{Name: "x", Type: "clap", PluginPath: "/p.clap"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fa := &fakeAudio{}
			r := newWithBackend([]Patch{tc.patch}, fa)
			if err := r.SelectIndex(0); err == nil {
				t.Fatal("expected error, got nil")
			}
			if fa.clapCalls != 0 {
				t.Errorf("expected 0 SetClapPlugin calls, got %d", fa.clapCalls)
			}
		})
	}
}

func TestSelectIndexUnknownType(t *testing.T) {
	p := Patch{Name: "weird", Type: "vst3"}
	fa := &fakeAudio{}
	r := newWithBackend([]Patch{p}, fa)

	err := r.SelectIndex(0)
	if err == nil {
		t.Fatal("expected error for unknown patch type, got nil")
	}
	if !strings.Contains(err.Error(), `unknown type "vst3"`) {
		t.Errorf("error message should mention unknown type: %v", err)
	}
}

func TestSelectIndexNativePatch(t *testing.T) {
	p := Patch{
		Name:    "moog-bass-native",
		Display: "Moog (native)",
		Type:    "native",
		Engine:  "minimoog",
		GainDB:  0.0,
	}
	fa := &fakeAudio{}
	r := newWithBackend([]Patch{p}, fa)

	if err := r.SelectIndex(0); err != nil {
		t.Fatalf("SelectIndex(0) failed: %v", err)
	}
	if fa.nativeCalls != 1 {
		t.Errorf("expected 1 SetNativePatch call, got %d", fa.nativeCalls)
	}
	if fa.nativeEngine != "minimoog" {
		t.Errorf("expected native engine %q, got %q", "minimoog", fa.nativeEngine)
	}
	if fa.setCalls != 0 || fa.reloadCalls != 0 {
		t.Errorf("soundfont path should NOT be touched for native patch (set=%d reload=%d)", fa.setCalls, fa.reloadCalls)
	}
	if fa.lv2Calls != 0 || fa.clapCalls != 0 {
		t.Errorf("plugin paths should NOT be touched for native patch (lv2=%d clap=%d)", fa.lv2Calls, fa.clapCalls)
	}
	if fa.setPatchGainCalls != 1 {
		t.Errorf("expected 1 SetPatchGain call (gain applies to native patches too), got %d", fa.setPatchGainCalls)
	}
	cur := r.Current()
	if cur == nil || cur.Name != "moog-bass-native" {
		t.Errorf("expected current patch %q, got %v", "moog-bass-native", cur)
	}
}

func TestSelectIndexNativeMissingEngine(t *testing.T) {
	p := Patch{Name: "broken-native", Type: "native"}
	fa := &fakeAudio{}
	r := newWithBackend([]Patch{p}, fa)

	err := r.SelectIndex(0)
	if err == nil {
		t.Fatal("expected error for native patch missing engine, got nil")
	}
	if fa.nativeCalls != 0 {
		t.Errorf("expected 0 SetNativePatch calls, got %d", fa.nativeCalls)
	}
	if r.Current() != nil {
		t.Errorf("expected nil Current() after failed selection, got %v", r.Current())
	}
}
