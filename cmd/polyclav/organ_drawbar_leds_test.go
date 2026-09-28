package main

import (
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/driver"
)

func TestDrawbarColorModesCycle(t *testing.T) {
	leds := newOrganDrawbarLEDs()
	if got := leds.Mode(); got != DrawbarColorModeB3Standard {
		t.Fatalf("initial mode = %v, want %v", got, DrawbarColorModeB3Standard)
	}

	expected := []struct {
		mode  DrawbarColorMode
		line1 string
		line2 string
	}{
		{DrawbarColorModeRegisters, "DRAWBARS", "LOW / MID / HIGH"},
		{DrawbarColorModeHarmonics, "DRAWBARS", "OCTAVE / MUTATION"},
		{DrawbarColorModePerformance, "DRAWBARS", "FIRST 3 + WHISTLER"},
		{DrawbarColorModeB3Standard, "DRAWBARS", "B3 STANDARD"},
	}

	for i, exp := range expected {
		handled, l1, l2 := leds.HandleButton(1, true)
		if !handled {
			t.Fatalf("step %d: HandleButton(1, true) was not handled", i)
		}
		if leds.Mode() != exp.mode {
			t.Fatalf("step %d: mode = %v, want %v", i, leds.Mode(), exp.mode)
		}
		if l1 != exp.line1 || l2 != exp.line2 {
			t.Fatalf("step %d: display = (%q, %q), want (%q, %q)", i, l1, l2, exp.line1, exp.line2)
		}
	}
}

func TestDrawbarButton1ReleaseIgnored(t *testing.T) {
	leds := newOrganDrawbarLEDs()
	handled, l1, l2 := leds.HandleButton(1, false)
	if !handled {
		t.Fatal("button release should be recognized as handled")
	}
	if l1 != "" || l2 != "" {
		t.Fatalf("release should not produce display text: (%q, %q)", l1, l2)
	}
	if leds.Mode() != DrawbarColorModeB3Standard {
		t.Fatalf("mode changed on release: %v", leds.Mode())
	}
}

func TestDrawbarButton2CyclesB3ColorSchemes(t *testing.T) {
	leds := newOrganDrawbarLEDs()
	if got := leds.B3Scheme(); got != B3ColorSchemeClassic {
		t.Fatalf("initial B3 scheme = %v, want %v", got, B3ColorSchemeClassic)
	}

	expected := []struct {
		scheme B3ColorScheme
		line1  string
		line2  string
	}{
		{B3ColorSchemeNeon, "B3 SCHEME", "2: NEON"},
		{B3ColorSchemeOcean, "B3 SCHEME", "3: OCEAN"},
		{B3ColorSchemeTriad, "B3 SCHEME", "4: TRIAD"},
		{B3ColorSchemeCandy, "B3 SCHEME", "5: CANDY"},
		{B3ColorSchemeClassic, "B3 SCHEME", "1: CLASSIC"},
	}

	for i, exp := range expected {
		handled, l1, l2 := leds.HandleButton(2, true)
		if !handled {
			t.Fatalf("step %d: HandleButton(2, true) was not handled", i)
		}
		if leds.B3Scheme() != exp.scheme {
			t.Fatalf("step %d: scheme = %v, want %v", i, leds.B3Scheme(), exp.scheme)
		}
		if l1 != exp.line1 || l2 != exp.line2 {
			t.Fatalf("step %d: display = (%q, %q), want (%q, %q)", i, l1, l2, exp.line1, exp.line2)
		}
		if leds.Mode() != DrawbarColorModeB3Standard {
			t.Fatalf("step %d: mode should remain B3Standard: %v", i, leds.Mode())
		}
		if leds.CurrentColors() != exp.scheme.Colors() {
			t.Fatalf("step %d: CurrentColors() != scheme.Colors()", i)
		}
	}
}

func TestDrawbarButton2SwitchesToB3Mode(t *testing.T) {
	leds := newOrganDrawbarLEDs()
	leds.SetMode(DrawbarColorModeRegisters)
	handled, l1, l2 := leds.HandleButton(2, true)
	if !handled {
		t.Fatal("button 2 should be handled")
	}
	if leds.Mode() != DrawbarColorModeB3Standard {
		t.Fatalf("mode = %v, want B3Standard", leds.Mode())
	}
	if leds.B3Scheme() != B3ColorSchemeNeon {
		t.Fatalf("scheme = %v, want Neon", leds.B3Scheme())
	}
	if l1 != "B3 SCHEME" || l2 != "2: NEON" {
		t.Fatalf("display = (%q, %q)", l1, l2)
	}
}

func TestDrawbarButton2ReleaseIgnored(t *testing.T) {
	leds := newOrganDrawbarLEDs()
	handled, l1, l2 := leds.HandleButton(2, false)
	if !handled {
		t.Fatal("button 2 release should be recognized as handled")
	}
	if l1 != "" || l2 != "" {
		t.Fatalf("release should not produce display text: (%q, %q)", l1, l2)
	}
	if leds.B3Scheme() != B3ColorSchemeClassic {
		t.Fatalf("scheme changed on release: %v", leds.B3Scheme())
	}
}

func TestOtherButtonsNotHandledByDrawbarLEDs(t *testing.T) {
	leds := newOrganDrawbarLEDs()
	for btn := 3; btn <= 9; btn++ {
		handled, _, _ := leds.HandleButton(btn, true)
		if handled {
			t.Fatalf("button %d should not be handled by drawbarLEDs", btn)
		}
	}
}

func TestDrawbarColorModeColors(t *testing.T) {
	// 1. B3 Standard: [Brown, Brown, White, White, Red, White, Red, Red, White]
	b3 := DrawbarColorModeB3Standard.Colors()
	wantB3 := [9]components.Color{
		components.ColorBrown,
		components.ColorBrown,
		components.ColorBrightWhite,
		components.ColorBrightWhite,
		components.ColorVibrantRed,
		components.ColorBrightWhite,
		components.ColorVibrantRed,
		components.ColorVibrantRed,
		components.ColorBrightWhite,
	}
	if b3 != wantB3 {
		t.Fatalf("B3 colors = %v, want %v", b3, wantB3)
	}

	// 2. Registers (Low, Mid, High): 3 Red, 3 Green, 3 Cyan
	reg := DrawbarColorModeRegisters.Colors()
	wantReg := [9]components.Color{
		components.ColorVibrantRed, components.ColorVibrantRed, components.ColorVibrantRed,
		components.ColorGreen, components.ColorGreen, components.ColorGreen,
		components.ColorVibrantCyan, components.ColorVibrantCyan, components.ColorVibrantCyan,
	}
	if reg != wantReg {
		t.Fatalf("Registers colors = %v, want %v", reg, wantReg)
	}

	// 3. Harmonics: Octaves (1,3,4,6,9) = White, Mutations (2,5,7,8) = Orange
	harm := DrawbarColorModeHarmonics.Colors()
	wantHarm := [9]components.Color{
		components.ColorBrightWhite,
		components.ColorVibrantOrange,
		components.ColorBrightWhite,
		components.ColorBrightWhite,
		components.ColorVibrantOrange,
		components.ColorBrightWhite,
		components.ColorVibrantOrange,
		components.ColorVibrantOrange,
		components.ColorBrightWhite,
	}
	if harm != wantHarm {
		t.Fatalf("Harmonics colors = %v, want %v", harm, wantHarm)
	}

	// 4. Performance: First 3 (Green), Mid (Dim White), Whistler 9 (Yellow)
	perf := DrawbarColorModePerformance.Colors()
	wantPerf := [9]components.Color{
		components.ColorGreen, components.ColorGreen, components.ColorGreen,
		components.ColorDimWhite, components.ColorDimWhite, components.ColorDimWhite, components.ColorDimWhite, components.ColorDimWhite,
		components.ColorVibrantYellow,
	}
	if perf != wantPerf {
		t.Fatalf("Performance colors = %v, want %v", perf, wantPerf)
	}
}

func TestB3ColorSchemes(t *testing.T) {
	tests := []struct {
		scheme B3ColorScheme
		name   string
		colors [9]components.Color
	}{
		{
			scheme: B3ColorSchemeClassic,
			name:   "CLASSIC",
			colors: [9]components.Color{
				components.ColorBrown, components.ColorBrown,
				components.ColorBrightWhite, components.ColorBrightWhite, components.ColorVibrantRed,
				components.ColorBrightWhite, components.ColorVibrantRed, components.ColorVibrantRed,
				components.ColorBrightWhite,
			},
		},
		{
			scheme: B3ColorSchemeNeon,
			name:   "NEON",
			colors: [9]components.Color{
				components.ColorVibrantOrange, components.ColorVibrantOrange,
				components.ColorVibrantCyan, components.ColorVibrantCyan, components.ColorVibrantPink,
				components.ColorVibrantCyan, components.ColorVibrantPink, components.ColorVibrantPink,
				components.ColorVibrantCyan,
			},
		},
		{
			scheme: B3ColorSchemeOcean,
			name:   "OCEAN",
			colors: [9]components.Color{
				components.ColorVibrantYellow, components.ColorVibrantYellow,
				components.ColorBrightWhite, components.ColorBrightWhite, components.ColorVibrantBlue,
				components.ColorBrightWhite, components.ColorVibrantBlue, components.ColorVibrantBlue,
				components.ColorBrightWhite,
			},
		},
		{
			scheme: B3ColorSchemeTriad,
			name:   "TRIAD",
			colors: [9]components.Color{
				components.ColorNeutralYellow, components.ColorNeutralYellow,
				components.ColorNeutralCyan, components.ColorNeutralCyan, components.ColorNeutralRed,
				components.ColorNeutralCyan, components.ColorNeutralRed, components.ColorNeutralRed,
				components.ColorNeutralCyan,
			},
		},
		{
			scheme: B3ColorSchemeCandy,
			name:   "CANDY",
			colors: [9]components.Color{
				components.ColorVibrantPink, components.ColorVibrantPink,
				components.ColorBrightWhite, components.ColorBrightWhite, components.ColorVibrantCyan,
				components.ColorBrightWhite, components.ColorVibrantCyan, components.ColorVibrantCyan,
				components.ColorBrightWhite,
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.scheme.Name() != tc.name {
				t.Fatalf("name = %q, want %q", tc.scheme.Name(), tc.name)
			}
			if tc.scheme.Colors() != tc.colors {
				t.Fatalf("colors = %v, want %v", tc.scheme.Colors(), tc.colors)
			}
		})
	}
}

type fakeButtonColorSetter struct {
	colors [9]components.Color
	counts [9]int
}

func (f *fakeButtonColorSetter) SetFaderButtonColor(index int, color components.Color) error {
	if index >= 1 && index <= 9 {
		f.colors[index-1] = color
		f.counts[index-1]++
	}
	return nil
}

func TestOrganDrawbarSync(t *testing.T) {
	leds := newOrganDrawbarLEDs()
	setter := &fakeButtonColorSetter{}
	isOrgan := false
	leslieOn := false
	sync := newOrganDrawbarSync(leds, setter, func() bool { return isOrgan }, func() bool { return leslieOn })

	// Initially not an organ: should turn off buttons.
	if err := sync.Sync(); err != nil {
		t.Fatalf("sync error: %v", err)
	}
	for i, c := range setter.colors {
		if c != components.ColorOff {
			t.Errorf("button %d = %v, want ColorOff", i+1, c)
		}
	}

	// Repeated sync without change should do nothing.
	setter.counts = [9]int{}
	if err := sync.Sync(); err != nil {
		t.Fatalf("sync error: %v", err)
	}
	if setter.counts != [9]int{} {
		t.Fatalf("repeated sync sent updates: %v", setter.counts)
	}

	// Switch to organ with Leslie off: should send B3 colors for 1..8 and ColorOff for 9.
	isOrgan = true
	if err := sync.Sync(); err != nil {
		t.Fatalf("sync error: %v", err)
	}
	wantB3 := DrawbarColorModeB3Standard.Colors()
	for i := 0; i < 8; i++ {
		if setter.colors[i] != wantB3[i] {
			t.Errorf("button %d = %v, want %v", i+1, setter.colors[i], wantB3[i])
		}
	}
	if setter.colors[8] != components.ColorOff {
		t.Errorf("button 9 (Leslie off) = %v, want ColorOff", setter.colors[8])
	}

	// Leslie toggles on: button 9 turns Green, buttons 1..8 unchanged.
	leslieOn = true
	if err := sync.Sync(); err != nil {
		t.Fatalf("sync error: %v", err)
	}
	if setter.colors[8] != components.ColorGreen {
		t.Errorf("button 9 (Leslie on) = %v, want ColorGreen", setter.colors[8])
	}

	// Cycle mode: should update buttons 1..8 to Registers, button 9 remains Green.
	leds.Cycle()
	if err := sync.Sync(); err != nil {
		t.Fatalf("sync error: %v", err)
	}
	wantReg := DrawbarColorModeRegisters.Colors()
	for i := 0; i < 8; i++ {
		if setter.colors[i] != wantReg[i] {
			t.Errorf("button %d = %v, want %v", i+1, setter.colors[i], wantReg[i])
		}
	}
	if setter.colors[8] != components.ColorGreen {
		t.Errorf("button 9 should stay ColorGreen after mode cycle: %v", setter.colors[8])
	}

	// Reset: forces repaint.
	sync.Reset()
	setter.colors = [9]components.Color{}
	if err := sync.Sync(); err != nil {
		t.Fatalf("sync error: %v", err)
	}
	for i := 0; i < 8; i++ {
		if setter.colors[i] != wantReg[i] {
			t.Errorf("repaint button %d = %v, want %v", i+1, setter.colors[i], wantReg[i])
		}
	}
	if setter.colors[8] != components.ColorGreen {
		t.Errorf("repaint button 9 = %v, want ColorGreen", setter.colors[8])
	}

	// Cycle B3 scheme: switches back to B3 standard with Neon scheme.
	leds.CycleB3Scheme()
	if err := sync.Sync(); err != nil {
		t.Fatalf("sync error: %v", err)
	}
	wantNeon := B3ColorSchemeNeon.Colors()
	for i := 0; i < 8; i++ {
		if setter.colors[i] != wantNeon[i] {
			t.Errorf("neon button %d = %v, want %v", i+1, setter.colors[i], wantNeon[i])
		}
	}
	if setter.colors[8] != components.ColorGreen {
		t.Errorf("button 9 should stay ColorGreen after scheme cycle: %v", setter.colors[8])
	}

	// Switch away from organ: should turn off buttons.
	isOrgan = false
	if err := sync.Sync(); err != nil {
		t.Fatalf("sync error: %v", err)
	}
	for i, c := range setter.colors {
		if c != components.ColorOff {
			t.Errorf("button %d = %v, want ColorOff after leaving organ", i+1, c)
		}
	}
}

type fakeLeslieHandler struct {
	handled bool
	on      bool
}

func (f *fakeLeslieHandler) HandleLeslieButton(pressed bool) bool {
	if pressed {
		f.on = !f.on
	}
	return f.handled
}

func (f *fakeLeslieHandler) LeslieOn() bool {
	return f.on
}

func TestDispatchFaderButton(t *testing.T) {
	leds := newOrganDrawbarLEDs()
	setter := &fakeButtonColorSetter{}
	isOrgan := true
	leslie := &fakeLeslieHandler{handled: true, on: false}
	sync := newOrganDrawbarSync(leds, setter, func() bool { return isOrgan }, leslie.LeslieOn)
	screen := &fakeScreen{}

	// Initial sync sets button 9 to ColorOff (leslie is off).
	_ = sync.Sync()
	if setter.colors[8] != components.ColorOff {
		t.Fatalf("initial button 9 = %v, want ColorOff", setter.colors[8])
	}

	// 1. Button 1 on organ: cycles mode, calls sync and screen.
	ev1 := driver.FaderButtonEvent{Index: 1, Pressed: true}
	if !dispatchFaderButton(ev1, isOrgan, leds, sync, leslie, screen) {
		t.Fatal("button 1 on organ should be handled")
	}
	if leds.Mode() != DrawbarColorModeRegisters {
		t.Fatalf("mode = %v, want Registers", leds.Mode())
	}
	if screen.line1 != "DRAWBARS" || screen.line2 != "LOW / MID / HIGH" {
		t.Fatalf("screen = (%q, %q)", screen.line1, screen.line2)
	}

	// 2. Button 1 when not an organ: should return false, no mode change.
	isOrgan = false
	screen.line1, screen.line2 = "", ""
	if dispatchFaderButton(ev1, isOrgan, leds, sync, leslie, screen) {
		t.Fatal("button 1 when not an organ should not be handled")
	}
	if leds.Mode() != DrawbarColorModeRegisters {
		t.Fatalf("mode changed when not an organ: %v", leds.Mode())
	}

	// 2b. Button 2 on organ: cycles B3 color scheme, calls sync and screen.
	isOrgan = true
	ev2 := driver.FaderButtonEvent{Index: 2, Pressed: true}
	if !dispatchFaderButton(ev2, isOrgan, leds, sync, leslie, screen) {
		t.Fatal("button 2 on organ should be handled")
	}
	if leds.Mode() != DrawbarColorModeB3Standard {
		t.Fatalf("mode = %v, want B3Standard", leds.Mode())
	}
	if leds.B3Scheme() != B3ColorSchemeNeon {
		t.Fatalf("scheme = %v, want Neon", leds.B3Scheme())
	}
	if screen.line1 != "B3 SCHEME" || screen.line2 != "2: NEON" {
		t.Fatalf("screen = (%q, %q), want (B3 SCHEME, 2: NEON)", screen.line1, screen.line2)
	}
	wantNeon := B3ColorSchemeNeon.Colors()
	for i := 0; i < 8; i++ {
		if setter.colors[i] != wantNeon[i] {
			t.Errorf("button %d = %v, want %v", i+1, setter.colors[i], wantNeon[i])
		}
	}

	// 2c. Button 2 when not an organ: should return false, no scheme change.
	isOrgan = false
	screen.line1, screen.line2 = "", ""
	if dispatchFaderButton(ev2, isOrgan, leds, sync, leslie, screen) {
		t.Fatal("button 2 when not an organ should not be handled")
	}
	if leds.B3Scheme() != B3ColorSchemeNeon {
		t.Fatalf("scheme changed when not an organ: %v", leds.B3Scheme())
	}

	// 3. Button 9 on organ: toggles Leslie, updates screen, and syncs button 9 LED to ColorGreen.
	isOrgan = true
	ev9 := driver.FaderButtonEvent{Index: 9, Pressed: true}
	if !dispatchFaderButton(ev9, isOrgan, leds, sync, leslie, screen) {
		t.Fatal("button 9 should be handled")
	}
	if !leslie.on {
		t.Fatal("leslie should have toggled on")
	}
	if screen.line1 != "LESLIE" || screen.line2 != "FAST" {
		t.Fatalf("screen = (%q, %q), want (LESLIE, FAST)", screen.line1, screen.line2)
	}
	if setter.colors[8] != components.ColorGreen {
		t.Fatalf("button 9 LED after Leslie toggle on = %v, want ColorGreen", setter.colors[8])
	}

	// Button 9 second press: toggles off, LED turns ColorOff.
	if !dispatchFaderButton(ev9, isOrgan, leds, sync, leslie, screen) {
		t.Fatal("button 9 should be handled")
	}
	if leslie.on {
		t.Fatal("leslie should have toggled off")
	}
	if screen.line1 != "LESLIE" || screen.line2 != "STOP" {
		t.Fatalf("screen = (%q, %q), want (LESLIE, STOP)", screen.line1, screen.line2)
	}
	if setter.colors[8] != components.ColorOff {
		t.Fatalf("button 9 LED after Leslie toggle off = %v, want ColorOff", setter.colors[8])
	}

	// 4. Other button (e.g. Button 5): returns false.
	ev5 := driver.FaderButtonEvent{Index: 5, Pressed: true}
	if dispatchFaderButton(ev5, isOrgan, leds, sync, leslie, screen) {
		t.Fatal("button 5 should not be handled")
	}
}
