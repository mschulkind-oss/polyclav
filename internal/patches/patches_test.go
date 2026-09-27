package patches

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/config"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
)

type fakeAudio struct {
	panicCalls        int
	calls             []string
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
	clapStateCalls    int
	lastClapState     []byte
	clapErr           error
	nativeEngine      string
	nativeCalls       int
	nativeErr         error
	generation        uint64
}

func (f *fakeAudio) Panic() {
	f.panicCalls++
	f.calls = append(f.calls, "panic")
}

func (f *fakeAudio) SetSoundfont(path string) {
	f.soundfont = path
	f.setCalls++
	f.calls = append(f.calls, "soundfont")
}

func (f *fakeAudio) ReloadSoundfont() error {
	f.reloadCalls++
	f.calls = append(f.calls, "reload")
	return f.reloadErr
}

func (f *fakeAudio) SetPatchGain(linear float32) {
	f.setPatchGainCalls++
	f.lastPatchGain = linear
}

func (f *fakeAudio) SetLv2Plugin(uri string) error {
	f.lv2Calls++
	f.calls = append(f.calls, "lv2")
	f.lv2URI = uri
	return f.lv2Err
}

func (f *fakeAudio) SetClapPlugin(bundlePath, pluginID string) error {
	f.clapCalls++
	f.calls = append(f.calls, "clap")
	f.clapPath = bundlePath
	f.clapID = pluginID
	return f.clapErr
}

func (f *fakeAudio) SetClapPluginWithState(bundlePath, pluginID string, stateBlob []byte) error {
	f.clapStateCalls++
	f.calls = append(f.calls, "clap-state")
	f.clapPath = bundlePath
	f.clapID = pluginID
	f.lastClapState = append([]byte(nil), stateBlob...)
	return f.clapErr
}

func (f *fakeAudio) SetNativePatch(engine string) error {
	f.nativeCalls++
	f.calls = append(f.calls, "native")
	f.nativeEngine = engine
	return f.nativeErr
}

func (f *fakeAudio) BackendGeneration() uint64 {
	if f.generation == 0 {
		f.generation = 1
	}
	return f.generation
}

func TestRegistryReadinessKeepsActivePatchOnAsyncFailure(t *testing.T) {
	dir := t.TempDir()
	a := makePatch(t, dir, "a", 10)
	b := makePatch(t, dir, "b", 11)
	fa := &fakeAudio{generation: 1}
	r := newWithBackend([]Patch{a, b}, fa)
	if err := r.SelectIndex(0); err != nil {
		t.Fatalf("select a: %v", err)
	}
	if !r.MarkActive(1) {
		t.Fatal("MarkActive(1) = false")
	}
	fa.generation = 2
	if err := r.SelectIndex(1); err != nil {
		t.Fatalf("select b: %v", err)
	}
	if active := r.Active(); active == nil || active.Name != "a" {
		t.Fatalf("active during load = %#v, want a", active)
	}
	if st := r.Status(); st.State != LoadStateLoading || st.Index != 1 || st.Generation != 2 {
		t.Fatalf("status during load = %#v", st)
	}
	if !r.MarkFailed(2, "loader failed") {
		t.Fatal("MarkFailed(2) = false")
	}
	if cur := r.Current(); cur == nil || cur.Name != "a" {
		t.Fatalf("current after failure = %#v, want a", cur)
	}
	if active := r.Active(); active == nil || active.Name != "a" {
		t.Fatalf("active after failure = %#v, want a", active)
	}
	if st := r.Status(); st.State != LoadStateFailed || !strings.Contains(st.Err, "loader failed") {
		t.Fatalf("status after failure = %#v", st)
	}
}

func TestRegistryReadinessIgnoresStaleGenerations(t *testing.T) {
	dir := t.TempDir()
	a := makePatch(t, dir, "a", 10)
	b := makePatch(t, dir, "b", 11)
	fa := &fakeAudio{generation: 7}
	r := newWithBackend([]Patch{a, b}, fa)
	if err := r.SelectIndex(0); err != nil {
		t.Fatalf("select a: %v", err)
	}
	if r.MarkActive(6) {
		t.Fatal("stale MarkActive returned true")
	}
	if active := r.Active(); active != nil {
		t.Fatalf("active after stale mark = %#v, want nil", active)
	}
	if !r.MarkActive(7) {
		t.Fatal("MarkActive(7) = false")
	}
	if active := r.Active(); active == nil || active.Name != "a" {
		t.Fatalf("active after current mark = %#v, want a", active)
	}
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

func TestFromConfigSwellOptIn(t *testing.T) {
	yes, no := true, false
	ps := FromConfig([]config.PatchConfig{
		{Name: "organ", Type: "clap", LaunchkeyOrgan: config.LaunchkeyOrganConfig{Enabled: true, Ownership: "organ"}},
		{Name: "opted-out organ", Type: "clap", LaunchkeyOrgan: config.LaunchkeyOrganConfig{Enabled: true, Ownership: "organ"}, SwellPedal: &no},
		{Name: "other swell", Type: "clap", SwellPedal: &yes},
		{Name: "piano", Type: "soundfont"},
	})
	for i, want := range []bool{true, false, true, false} {
		if ps[i].SwellPedal != want {
			t.Errorf("%s swell = %t, want %t", ps[i].Name, ps[i].SwellPedal, want)
		}
	}
}

func TestFromConfig(t *testing.T) {
	cfgs := []config.PatchConfig{
		{Name: "a", Display: "A", Soundfont: "/path/a.sf2", PadColor: 3},
		{Name: "b", Display: "B", Soundfont: "/path/b.sfz", PadColor: 41,
			VelocityCurve: "custom", VelocityGamma: 0.7},
		{Name: "c", Display: "C", Soundfont: "/path/c.sf2", PadColor: 5,
			VelocityPoints: [][]int{{0, 0}, {64, 90}, {127, 127}}},
	}

	ps := FromConfig(cfgs)
	if len(ps) != 3 {
		t.Fatalf("expected 3 patches, got %d", len(ps))
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
	if ps[1].VelocityPoints != nil {
		t.Errorf("patch[1] should carry no velocity points, got %v", ps[1].VelocityPoints)
	}

	wantPoints := [][]int{{0, 0}, {64, 90}, {127, 127}}
	if len(ps[2].VelocityPoints) != len(wantPoints) {
		t.Fatalf("expected patch[2].VelocityPoints %v, got %v", wantPoints, ps[2].VelocityPoints)
	}
	for i, pt := range ps[2].VelocityPoints {
		if len(pt) != 2 || pt[0] != wantPoints[i][0] || pt[1] != wantPoints[i][1] {
			t.Errorf("patch[2].VelocityPoints[%d] = %v, want %v", i, pt, wantPoints[i])
		}
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
	clapPath := filepath.Join(t.TempDir(), "Dexed.clap")
	if err := os.WriteFile(clapPath, []byte("fake clap"), 0o644); err != nil {
		t.Fatalf("write fake clap: %v", err)
	}
	p := Patch{
		Name:       "dexed-clap",
		Display:    "Dexed",
		Type:       "clap",
		PluginPath: clapPath,
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

func TestSelectIndexPanicsBeforeBackendSwitch(t *testing.T) {
	dir := t.TempDir()
	sf := makePatch(t, dir, "sf", components.ColorVibrantGreen)
	clapPath := filepath.Join(dir, "Potato Keys.clap")
	if err := os.WriteFile(clapPath, []byte("fake clap"), 0o644); err != nil {
		t.Fatalf("write fake clap: %v", err)
	}
	tests := []struct {
		name  string
		patch Patch
		want  []string
	}{
		{name: "soundfont", patch: sf, want: []string{"panic", "soundfont", "reload"}},
		{name: "lv2", patch: Patch{Name: "lv2", Type: "lv2", PluginURI: "urn:test"}, want: []string{"panic", "lv2"}},
		{name: "clap", patch: Patch{Name: "clap", Type: "clap", PluginPath: clapPath, PluginID: "com.test"}, want: []string{"panic", "clap"}},
		{name: "native", patch: Patch{Name: "native", Type: "native", Engine: "minimoog"}, want: []string{"panic", "native"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fa := &fakeAudio{}
			r := newWithBackend([]Patch{tt.patch}, fa)
			if err := r.SelectIndex(0); err != nil {
				t.Fatalf("SelectIndex: %v", err)
			}
			if got := strings.Join(fa.calls, ","); got != strings.Join(tt.want, ",") {
				t.Fatalf("call order = %v, want %v", fa.calls, tt.want)
			}
		})
	}
}

func TestSelectIndexClapMissingPathDoesNotMutateCurrentOrPanic(t *testing.T) {
	fa := &fakeAudio{}
	r := newWithBackend([]Patch{
		{Name: "bad", Type: "clap", PluginPath: filepath.Join(t.TempDir(), "missing.clap"), PluginID: "com.test"},
	}, fa)
	err := r.SelectIndex(0)
	if err == nil {
		t.Fatal("SelectIndex missing CLAP path succeeded")
	}
	if r.Current() != nil {
		t.Fatalf("current = %+v, want nil", r.Current())
	}
	if fa.panicCalls != 0 || fa.clapCalls != 0 || fa.setPatchGainCalls != 0 {
		t.Fatalf("backend touched on failed select: %+v", fa)
	}
}

func TestSelectWithClapStatePassesSavedBlob(t *testing.T) {
	dir := t.TempDir()
	plugin := filepath.Join(dir, "fixture.clap")
	if err := os.WriteFile(plugin, []byte("fake"), 0o644); err != nil {
		t.Fatal(err)
	}
	fa := &fakeAudio{}
	r := newWithBackend([]Patch{{Name: "organ", Type: "clap", PluginPath: plugin, PluginID: "com.test"}}, fa)
	blob := []byte{1, 2, 3}
	if err := r.SelectWithClapState("organ", blob); err != nil {
		t.Fatalf("SelectWithClapState: %v", err)
	}
	if fa.clapStateCalls != 1 || !bytes.Equal(fa.lastClapState, blob) {
		t.Fatalf("clap state calls=%d blob=%v", fa.clapStateCalls, fa.lastClapState)
	}
}
