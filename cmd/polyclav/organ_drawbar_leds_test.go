package main

import (
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
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
	// 1. B3 Standard: [Brown, Brown, White, White, Black, White, Black, Black, White]
	b3 := DrawbarColorModeB3Standard.Colors()
	wantB3 := [9]components.Color{
		components.ColorBrown,
		components.ColorBrown,
		components.ColorBrightWhite,
		components.ColorBrightWhite,
		components.ColorOff,
		components.ColorBrightWhite,
		components.ColorOff,
		components.ColorOff,
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
