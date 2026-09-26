package main

import (
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
