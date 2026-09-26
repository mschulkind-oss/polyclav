package main

import (
	"math"
	"os"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/clapcache"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/driver"
	"github.com/mschulkind-oss/polyclav/internal/midi"
	"github.com/mschulkind-oss/polyclav/internal/patches"
)

type fakeOrganRegistry struct {
	cur, active *patches.Patch
	status      patches.LoadStatus
}

func (f fakeOrganRegistry) Current() *patches.Patch    { return f.cur }
func (f fakeOrganRegistry) Active() *patches.Patch     { return f.active }
func (f fakeOrganRegistry) Status() patches.LoadStatus { return f.status }

type fakeSetter struct {
	id     uint32
	value  float64
	called bool
}

func (f *fakeSetter) SetClapParam(id uint32, value float64) error {
	f.id, f.value, f.called = id, value, true
	return nil
}

type fakeMapper struct{ events []midi.Event }

func (f *fakeMapper) Dispatch(ev midi.Event) { f.events = append(f.events, ev) }

type fakeScreen struct{ line1, line2 string }

func (f *fakeScreen) Show(a, b string) { f.line1, f.line2 = a, b }

func TestOrganFaderDisabledRoutesLaunchkeyDAWFaderToMixerOSC(t *testing.T) {
	m := &fakeMapper{}
	r := &organFaderRouter{registry: fakeOrganRegistry{}, mapper: m}
	r.HandleFader(driver.FaderEvent{Index: 9, Value: 64})
	if len(m.events) != 1 {
		t.Fatalf("events = %d, want 1", len(m.events))
	}
	if ev := m.events[0]; ev.Kind != midi.ControlChange || ev.Channel != 15 || ev.CC != 13 || ev.Value != 64 {
		t.Fatalf("event = %+v", ev)
	}
}

func TestOrganFaderCapturesAllNineResolvedDrawbarsAndDoesNotRouteMixer(t *testing.T) {
	p := organPatch()
	cache := clapcache.New()
	cache.Replace(drawbarParams())
	setter, mapper, screen := &fakeSetter{}, &fakeMapper{}, &fakeScreen{}
	r := &organFaderRouter{registry: fakeOrganRegistry{cur: p, active: p, status: patches.LoadStatus{State: patches.LoadStateActive}}, cache: cache, setter: setter, mapper: mapper, screen: screen}
	r.HandleFader(driver.FaderEvent{Index: 1, Value: 127})
	if !setter.called || setter.id != 101 || setter.value != 8 {
		t.Fatalf("set = (%d,%v,%v), want (101,8,true)", setter.id, setter.value, setter.called)
	}
	if len(mapper.events) != 0 {
		t.Fatalf("mixer events = %v, want none", mapper.events)
	}
	if screen.line1 != "16′ drawbar" || screen.line2 != "8" {
		t.Fatalf("screen = %q/%q", screen.line1, screen.line2)
	}
}

func TestPotatoKeysAutoBindsExactFootageLabels(t *testing.T) {
	p := organPatch()
	p.PluginID = "com.littlepotato.keys"
	p.LaunchkeyOrgan.DrawbarClapIDs = nil
	params := drawbarParams()
	for i, label := range []string{"16′", "5⅓′", "8′", "4′", "2⅔′", "2′", "1⅗′", "1⅓′", "1′"} {
		params[i].Name = label
	}
	cache := clapcache.New()
	cache.Replace(params)
	setter, mapper := &fakeSetter{}, &fakeMapper{}
	r := &organFaderRouter{registry: fakeOrganRegistry{cur: p, active: p, status: patches.LoadStatus{State: patches.LoadStateActive}}, cache: cache, setter: setter, mapper: mapper}
	for i := 1; i <= 9; i++ {
		setter.called = false
		r.HandleFader(driver.FaderEvent{Index: i, Value: 127})
		if !setter.called || setter.id != uint32(100+i) || setter.value != 8 {
			t.Errorf("fader %d: set (%d,%v,%v)", i, setter.id, setter.value, setter.called)
		}
	}
	if len(mapper.events) != 0 {
		t.Fatalf("organ faders also moved the mixer: %v", mapper.events)
	}
}

func TestPotatoKeysAutoBindingRejectsAmbiguousOrInvalidParams(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func([]audio.ClapParamInfo) []audio.ClapParamInfo
	}{
		{"missing", func(p []audio.ClapParamInfo) []audio.ClapParamInfo { return p[:8] }},
		{"duplicate name", func(p []audio.ClapParamInfo) []audio.ClapParamInfo {
			duplicate := p[0]
			duplicate.ClapID = 110
			return append(p, duplicate)
		}},
		{"duplicate id", func(p []audio.ClapParamInfo) []audio.ClapParamInfo { p[1].ClapID = p[0].ClapID; return p }},
		{"bad range", func(p []audio.ClapParamInfo) []audio.ClapParamInfo { p[2].MaxValue = 100; return p }},
		{"wrong name", func(p []audio.ClapParamInfo) []audio.ClapParamInfo { p[0].Name = "Drive"; return p }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := organPatch()
			p.PluginID = "com.littlepotato.keys"
			p.LaunchkeyOrgan.DrawbarClapIDs = nil
			params := drawbarParams()
			for i, label := range []string{"16′", "5⅓′", "8′", "4′", "2⅔′", "2′", "1⅗′", "1⅓′", "1′"} {
				params[i].Name = label
			}
			cache := clapcache.New()
			cache.Replace(tc.edit(params))
			setter, mapper, screen := &fakeSetter{}, &fakeMapper{}, &fakeScreen{}
			r := &organFaderRouter{registry: fakeOrganRegistry{cur: p, active: p, status: patches.LoadStatus{State: patches.LoadStateActive}}, cache: cache, setter: setter, mapper: mapper, screen: screen}
			r.HandleFader(driver.FaderEvent{Index: 9, Value: 127})
			if setter.called || len(mapper.events) != 0 || screen.line2 == "" {
				t.Fatalf("invalid auto binding: setter=%v mixer=%v screen=%+v", setter.called, mapper.events, screen)
			}
		})
	}
}

func TestOrganFaderMissingParamCapturesWithoutPartialMixerRoute(t *testing.T) {
	p := organPatch()
	cache := clapcache.New()
	cache.Replace([]audio.ClapParamInfo{{ClapID: 101, Name: "16 drawbar", MinValue: 0, MaxValue: 8}})
	setter, mapper, screen := &fakeSetter{}, &fakeMapper{}, &fakeScreen{}
	r := &organFaderRouter{registry: fakeOrganRegistry{cur: p, active: p, status: patches.LoadStatus{State: patches.LoadStateActive}}, cache: cache, setter: setter, mapper: mapper, screen: screen}
	r.HandleFader(driver.FaderEvent{Index: 1, Value: 127})
	if setter.called || len(mapper.events) != 0 {
		t.Fatalf("setter called=%v mixer=%v, want no partial route", setter.called, mapper.events)
	}
	if screen.line2 == "" {
		t.Fatalf("screen missing unresolved feedback")
	}
}

func TestOrganFaderRejectsWrongDrawbarPositionWithoutMixerRoute(t *testing.T) {
	p := organPatch()
	cache := clapcache.New()
	params := drawbarParams()
	params[1].Name = "16 drawbar"
	cache.Replace(params)
	setter, mapper, screen := &fakeSetter{}, &fakeMapper{}, &fakeScreen{}
	r := &organFaderRouter{registry: fakeOrganRegistry{cur: p, active: p, status: patches.LoadStatus{State: patches.LoadStateActive}}, cache: cache, setter: setter, mapper: mapper, screen: screen}
	r.HandleFader(driver.FaderEvent{Index: 2, Value: 127})
	if setter.called || len(mapper.events) != 0 {
		t.Fatalf("setter called=%v mixer=%v, want rejected capture without mixer route", setter.called, mapper.events)
	}
	if screen.line2 != "CHECK 102" {
		t.Fatalf("screen = %q/%q, want CHECK 102", screen.line1, screen.line2)
	}
}

// Run with POLYCLAV_KEYS_CLAP_PATH pointing at a Linux Potato Keys build.
// The binary is optional so the ordinary suite remains self-contained.
func TestPotatoKeysPluginOfflineAndLaunchkeyDrawbars(t *testing.T) {
	path := os.Getenv("POLYCLAV_KEYS_CLAP_PATH")
	if path == "" {
		t.Skip("set POLYCLAV_KEYS_CLAP_PATH to exercise the installed Potato Keys plugin")
	}
	params, err := audio.DiscoverClapParams(path, "com.littlepotato.keys")
	if err != nil {
		t.Fatal(err)
	}
	wantNames := [9]string{"16′", "5⅓′", "8′", "4′", "2⅔′", "2′", "1⅗′", "1⅓′", "1′"}
	ids := make([]uint32, 9)
	for i, name := range wantNames {
		for _, param := range params {
			if param.Name == name {
				ids[i] = param.ClapID
			}
		}
		if ids[i] == 0 {
			t.Fatalf("missing drawbar %q in %v", name, params)
		}
	}
	p := &patches.Patch{Name: "potato-keys", Type: "clap", PluginID: "com.littlepotato.keys", LaunchkeyOrgan: patches.LaunchkeyOrgan{Enabled: true, Ownership: "organ"}}
	cache := clapcache.New()
	cache.Replace(params)
	setter, mapper := &fakeSetter{}, &fakeMapper{}
	r := &organFaderRouter{registry: fakeOrganRegistry{cur: p, active: p, status: patches.LoadStatus{State: patches.LoadStateActive}}, cache: cache, setter: setter, mapper: mapper}
	for i, id := range ids {
		setter.called = false
		r.HandleFader(driver.FaderEvent{Index: i + 1, Value: 127})
		if !setter.called || setter.id != id || setter.value != 8 {
			t.Errorf("fader %d: set (%d,%v,%v), want (%d,8,true)", i+1, setter.id, setter.value, setter.called, id)
		}
	}
	if len(mapper.events) != 0 {
		t.Fatalf("organ faders also sent mixer events: %v", mapper.events)
	}
	for _, tc := range []struct {
		name   string
		events []audio.OfflineMIDIEvent
		want   bool
	}{
		{name: "silence", want: false},
		{name: "note", events: []audio.OfflineMIDIEvent{{Frame: 0, Kind: audio.OfflineNoteOn, Data1: 60, Data2: 100}, {Frame: 24000, Kind: audio.OfflineNoteOff, Data1: 60}}, want: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			samples, err := audio.RenderOfflineEvents("clap", path, "com.littlepotato.keys", nil, tc.events, 48000)
			if err != nil {
				t.Fatal(err)
			}
			peak := float32(0)
			for _, sample := range samples {
				if math.IsNaN(float64(sample)) || math.IsInf(float64(sample), 0) {
					t.Fatal("non-finite CLAP output")
				}
				if sample < -peak || sample > peak {
					peak = float32(math.Abs(float64(sample)))
				}
			}
			if (peak > 0.001) != tc.want {
				t.Fatalf("peak = %g, want audible=%v", peak, tc.want)
			}
		})
	}
}

func drawbarParams() []audio.ClapParamInfo {
	return []audio.ClapParamInfo{
		{ClapID: 101, Name: "16 drawbar", MinValue: 0, MaxValue: 8},
		{ClapID: 102, Name: "5 1/3 drawbar", MinValue: 0, MaxValue: 8},
		{ClapID: 103, Name: "8 drawbar", MinValue: 0, MaxValue: 8},
		{ClapID: 104, Name: "4 drawbar", MinValue: 0, MaxValue: 8},
		{ClapID: 105, Name: "2 2/3 drawbar", MinValue: 0, MaxValue: 8},
		{ClapID: 106, Name: "2 drawbar", MinValue: 0, MaxValue: 8},
		{ClapID: 107, Name: "1 3/5 drawbar", MinValue: 0, MaxValue: 8},
		{ClapID: 108, Name: "1 1/3 drawbar", MinValue: 0, MaxValue: 8},
		{ClapID: 109, Name: "1 drawbar", MinValue: 0, MaxValue: 8},
	}
}

func organPatch() *patches.Patch {
	return &patches.Patch{Name: "organ", Type: "clap", LaunchkeyOrgan: patches.LaunchkeyOrgan{Enabled: true, Ownership: "organ", DrawbarClapIDs: []uint32{101, 102, 103, 104, 105, 106, 107, 108, 109}}}
}
