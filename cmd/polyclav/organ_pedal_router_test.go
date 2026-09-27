package main

import (
	"math"
	"os"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/clapcache"
	"github.com/mschulkind-oss/polyclav/internal/config"
	"github.com/mschulkind-oss/polyclav/internal/midi"
	"github.com/mschulkind-oss/polyclav/internal/patches"
)

func testPedalRouter() (*organPedalRouter, *fakeSetter, *patches.Patch) {
	p := organPatch()
	p.Name, p.Type, p.PluginID = "potato-keys", "clap", "com.littlepotato.keys"
	p.SwellPedal = true
	p.LaunchkeyOrgan.LeslieMode = "toggle"
	cache := clapcache.New()
	cache.Replace([]audio.ClapParamInfo{
		{ClapID: 201, Name: "Expression", MinValue: 0, MaxValue: 100, CurrentValue: 80},
		{ClapID: 202, Name: "Rotary", MinValue: 0, MaxValue: 3, CurrentValue: 2},
	})
	setter := &fakeSetter{}
	r := &organPedalRouter{registry: fakeOrganRegistry{cur: p, active: p, status: patches.LoadStatus{State: patches.LoadStateActive}}, cache: cache, setter: setter,
		expression: config.OrganExpressionConfig{Device: "Expression Pedal:Expression Pedal MIDI 1", CC: 11, Min: 0, Max: 127}}
	return r, setter, p
}

func TestOrganExpressionCalibratesAndDoesNotControlOtherPatches(t *testing.T) {
	r, set, p := testPedalRouter()
	for _, tc := range []struct {
		raw  byte
		want float64
	}{{0, 0}, {64, 100 * 64.0 / 127}, {127, 100}} {
		ev := midi.Event{SourcePort: "Expression Pedal:Expression Pedal MIDI 1 28:0", Kind: midi.ControlChange, CC: 11, Value: tc.raw}
		if !r.Handle(ev) || !set.called || set.id != 201 || math.Abs(set.value-tc.want) > 1e-9 {
			t.Fatalf("pedal %d: set %+v want %v", tc.raw, set, tc.want)
		}
		set.called = false
	}
	r.expression.Min, r.expression.Max = 20, 100
	for _, tc := range []struct {
		raw  byte
		want float64
	}{{0, 0}, {20, 0}, {60, 50}, {100, 100}, {127, 100}} {
		r.Handle(midi.Event{SourcePort: "Expression Pedal:Expression Pedal MIDI 1 28:0", Kind: midi.ControlChange, CC: 11, Value: tc.raw})
		if !set.called || math.Abs(set.value-tc.want) > 1e-9 {
			t.Fatalf("calibrated %d: got %v want %v", tc.raw, set.value, tc.want)
		}
		set.called = false
	}
	r.expression.Min, r.expression.Max = 127, 0 // reversed pedal orientation
	for _, tc := range []struct {
		raw  byte
		want float64
	}{{0, 100}, {127, 0}} {
		r.Handle(midi.Event{SourcePort: "Expression Pedal:Expression Pedal MIDI 1 28:0", Kind: midi.ControlChange, CC: 11, Value: tc.raw})
		if !set.called || set.value != tc.want {
			t.Fatalf("reversed %d: got %v want %v", tc.raw, set.value, tc.want)
		}
		set.called = false
	}
	// The dedicated pedal is not a generic volume or note source when a piano is selected.
	r.registry = fakeOrganRegistry{cur: &patches.Patch{Name: "piano"}, active: &patches.Patch{Name: "piano"}, status: patches.LoadStatus{State: patches.LoadStateActive}}
	if !r.Handle(midi.Event{SourcePort: "Expression Pedal:Expression Pedal MIDI 1 28:0", Kind: midi.ControlChange, CC: 11, Value: 127}) || set.called {
		t.Fatal("pedal controlled a piano")
	}
	if !r.Handle(midi.Event{SourcePort: "Expression Pedal:Expression Pedal MIDI 1 28:0", Kind: midi.NoteOn, Note: 60, Vel: 100}) {
		t.Fatal("pedal port note leaked to piano")
	}
	r.registry = fakeOrganRegistry{cur: p, active: p, status: patches.LoadStatus{State: patches.LoadStateActive}}
	if r.Handle(midi.Event{SourcePort: "Other MIDI", Kind: midi.ControlChange, CC: 11, Value: 127}) {
		t.Fatal("other device captured")
	}
	// An explicitly opted-in non-organ CLAP patch may also use a swell;
	// it never acquires Leslie or drawbar ownership as a side effect.
	other := &patches.Patch{Name: "harmonium", Type: "clap", SwellPedal: true}
	r.registry = fakeOrganRegistry{cur: other, active: other, status: patches.LoadStatus{State: patches.LoadStateActive}}
	set.called = false
	r.expression.Min, r.expression.Max = 0, 127
	r.Handle(midi.Event{SourcePort: "Expression Pedal:Expression Pedal MIDI 1 28:0", Kind: midi.ControlChange, CC: 11, Value: 127})
	if !set.called || set.id != 201 || set.value != 100 {
		t.Fatalf("opted-in swell: %+v", set)
	}
	if r.Handle(midi.Event{SourcePort: "Launchkey MK4 61 MIDI In", Kind: midi.ControlChange, CC: 64, Value: 127}) {
		t.Fatal("opted-in swell captured sustain")
	}
}

// The installed plugin is optional in CI; this checks the actual labels,
// ranges and rotary default without starting an audio device.
func TestPotatoKeysPedalParametersOffline(t *testing.T) {
	path := os.Getenv("POLYCLAV_KEYS_CLAP_PATH")
	if path == "" {
		t.Skip("set POLYCLAV_KEYS_CLAP_PATH to inspect the installed Potato Keys plugin")
	}
	params, err := audio.DiscoverClapParams(path, "com.littlepotato.keys")
	if err != nil {
		t.Fatal(err)
	}
	cache := clapcache.New()
	cache.Replace(params)
	r := &organPedalRouter{cache: cache}
	if _, ok := r.namedParam("Expression", 0, 100); !ok {
		t.Fatal("missing 0–100 Expression parameter")
	}
	rotary, ok := r.namedParam("Rotary", 0, 3)
	if !ok || rotary.DefaultValue != 2 {
		t.Fatalf("Rotary parameter = %+v, found=%t", rotary, ok)
	}
}

func TestLeslieFaderButtonTracksRotary(t *testing.T) {
	r, set, p := testPedalRouter()
	if !r.LeslieOn() {
		t.Fatal("slow rotary is running and should light LED")
	}
	if !r.HandleLeslieButton(true) || !set.called || set.id != 202 || set.value != 1 || r.LeslieOn() {
		t.Fatalf("first press should stop rotary: %+v", set)
	}
	set.called = false
	if !r.HandleLeslieButton(true) || set.called {
		t.Fatal("repeated press toggled")
	}
	if !r.HandleLeslieButton(false) || set.called {
		t.Fatal("release changed rotary")
	}
	if !r.HandleLeslieButton(true) || !set.called || set.value != 3 || !r.LeslieOn() {
		t.Fatal("second press should select fast")
	}
	// The LED follows plugin feedback and sustain changes, not just button presses.
	r.cache.Update(202, 1)
	if r.LeslieOn() {
		t.Fatal("plugin stop feedback not reflected")
	}
	r.cache.Update(202, 2)
	if !r.LeslieOn() {
		t.Fatal("plugin slow feedback not reflected")
	}
	r.HandleLeslieButton(false)
	p.LaunchkeyOrgan.LeslieMode = "off" // disables sustain, not the button
	if !r.HandleLeslieButton(true) || set.value != 1 || r.LeslieOn() {
		t.Fatal("button should work with sustain mode off")
	}
	r.registry = fakeOrganRegistry{}
	set.called = false
	if r.HandleLeslieButton(true) || r.LeslieOn() || set.called {
		t.Fatal("inactive patch must not control Leslie or light LED")
	}
}

func TestLeslieSustainToggleMomentaryAndOff(t *testing.T) {
	r, set, p := testPedalRouter()
	ev := midi.Event{SourcePort: "Launchkey MK4 61:Launchkey MK4 61 MIDI In", Kind: midi.ControlChange, CC: 64, Value: 127}
	if !r.Handle(ev) || !set.called || set.id != 202 || set.value != 3 {
		t.Fatalf("toggle press %+v", set)
	}
	set.called = false
	r.Handle(ev) // repeated down must not toggle
	if set.called {
		t.Fatal("repeated press toggled")
	}
	ev.Value = 0
	if !r.Handle(ev) || set.called {
		t.Fatal("toggle release changed rotary")
	}
	ev.Value = 127
	if !r.Handle(ev) || set.value != 2 {
		t.Fatalf("second press should select slow, got %v", set.value)
	}
	p.LaunchkeyOrgan.LeslieMode = "momentary"
	ev.Value = 0
	r.Handle(ev)
	ev.Value = 127
	r.Handle(ev)
	if set.value != 3 {
		t.Fatalf("momentary press got %v", set.value)
	}
	ev.Value = 0
	r.Handle(ev)
	if set.value != 2 {
		t.Fatalf("momentary release got %v", set.value)
	}
	p.LaunchkeyOrgan.LeslieMode = "off"
	if r.Handle(ev) {
		t.Fatal("off captured sustain")
	}
	p.LaunchkeyOrgan.LeslieMode = "toggle"
	r.cache.Replace([]audio.ClapParamInfo{{ClapID: 201, Name: "Expression", MinValue: 0, MaxValue: 100}})
	set.called = false
	if r.Handle(ev) || set.called {
		t.Fatal("organ without a compatible Rotary parameter swallowed sustain")
	}
	if r.Handle(midi.Event{SourcePort: "Expression Pedal:Expression Pedal MIDI 1 28:0", Kind: midi.ControlChange, CC: 64, Value: 127}) == false {
		t.Fatal("dedicated pedal CC leaked")
	}
}
