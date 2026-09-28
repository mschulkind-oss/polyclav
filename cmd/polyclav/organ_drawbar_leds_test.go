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

func TestOtherButtonsNotHandledByDrawbarLEDs(t *testing.T) {
	leds := newOrganDrawbarLEDs()
	for btn := 2; btn <= 9; btn++ {
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
		components.ColorVibrantGreen, components.ColorVibrantGreen, components.ColorVibrantGreen,
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
		components.ColorVibrantGreen, components.ColorVibrantGreen, components.ColorVibrantGreen,
		components.ColorDimWhite, components.ColorDimWhite, components.ColorDimWhite, components.ColorDimWhite, components.ColorDimWhite,
		components.ColorVibrantYellow,
	}
	if perf != wantPerf {
		t.Fatalf("Performance colors = %v, want %v", perf, wantPerf)
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

	// Leslie toggles on: button 9 turns Vibrant Green, buttons 1..8 unchanged.
	leslieOn = true
	if err := sync.Sync(); err != nil {
		t.Fatalf("sync error: %v", err)
	}
	if setter.colors[8] != components.ColorVibrantGreen {
		t.Errorf("button 9 (Leslie on) = %v, want ColorVibrantGreen", setter.colors[8])
	}

	// Cycle mode: should update buttons 1..8 to Registers, button 9 remains Vibrant Green.
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
	if setter.colors[8] != components.ColorVibrantGreen {
		t.Errorf("button 9 should stay ColorVibrantGreen after mode cycle: %v", setter.colors[8])
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
	if setter.colors[8] != components.ColorVibrantGreen {
		t.Errorf("repaint button 9 = %v, want ColorVibrantGreen", setter.colors[8])
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

	// 3. Button 9 on organ: toggles Leslie, updates screen, and syncs button 9 LED to ColorVibrantGreen.
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
	if setter.colors[8] != components.ColorVibrantGreen {
		t.Fatalf("button 9 LED after Leslie toggle on = %v, want ColorVibrantGreen", setter.colors[8])
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
