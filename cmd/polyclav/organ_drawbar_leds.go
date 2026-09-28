package main

import (
	"sync"

	"github.com/mschulkind-oss/polyclav/internal/launchkey"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/driver"
)

// DrawbarColorMode selects how the nine fader button LEDs are colored on an
// active organ.
type DrawbarColorMode int

const (
	// DrawbarColorModeB3Standard matches the physical drawbar handles of a Hammond B3:
	// 16' (Brown), 5⅓' (Brown), 8' (White), 4' (White), 2⅔' (Black/Off),
	// 2' (White), 1⅗' (Black/Off), 1⅓' (Black/Off), 1' (White).
	DrawbarColorModeB3Standard DrawbarColorMode = iota

	// DrawbarColorModeRegisters groups drawbars into three 3-bar frequency registers:
	// Low/Bass (16', 5⅓', 8' - Red),
	// Mid/Body (4', 2⅔', 2' - Green),
	// High/Brilliance (1⅗', 1⅓', 1' - Cyan).
	DrawbarColorModeRegisters

	// DrawbarColorModeHarmonics separates consonant octave foundations from
	// harmonic mutations:
	// Foundations: 16', 8', 4', 2', 1' (White)
	// Mutations: 5⅓', 2⅔', 1⅗', 1⅓' (Orange).
	DrawbarColorModeHarmonics

	// DrawbarColorModePerformance highlights classic performance registrations:
	// "The First Three" jazz core: 16', 5⅓', 8' (Green),
	// Middle body / fill: 4', 2⅔', 2', 1⅗', 1⅓' (Dim White),
	// "The Whistler" top-end solo drawbar: 1' (Yellow).
	DrawbarColorModePerformance

	numDrawbarColorModes
)

// DisplayText returns the 2-line HUD message for the mode.
func (m DrawbarColorMode) DisplayText() (string, string) {
	switch m {
	case DrawbarColorModeB3Standard:
		return "DRAWBARS", "B3 STANDARD"
	case DrawbarColorModeRegisters:
		return "DRAWBARS", "LOW / MID / HIGH"
	case DrawbarColorModeHarmonics:
		return "DRAWBARS", "OCTAVE / MUTATION"
	case DrawbarColorModePerformance:
		return "DRAWBARS", "FIRST 3 + WHISTLER"
	default:
		return "DRAWBARS", "UNKNOWN"
	}
}

// Colors returns the 9 button palette colors for this mode.
func (m DrawbarColorMode) Colors() [9]components.Color {
	switch m {
	case DrawbarColorModeB3Standard:
		return [9]components.Color{
			components.ColorBrown,       // 16'
			components.ColorBrown,       // 5⅓'
			components.ColorBrightWhite, // 8'
			components.ColorBrightWhite, // 4'
			components.ColorOff,         // 2⅔'
			components.ColorBrightWhite, // 2'
			components.ColorOff,         // 1⅗'
			components.ColorOff,         // 1⅓'
			components.ColorBrightWhite, // 1'
		}
	case DrawbarColorModeRegisters:
		return [9]components.Color{
			components.ColorVibrantRed,   // 16' (Low)
			components.ColorVibrantRed,   // 5⅓' (Low)
			components.ColorVibrantRed,   // 8'  (Low)
			components.ColorVibrantGreen, // 4'  (Mid)
			components.ColorVibrantGreen, // 2⅔' (Mid)
			components.ColorVibrantGreen, // 2'  (Mid)
			components.ColorVibrantCyan,  // 1⅗' (High)
			components.ColorVibrantCyan,  // 1⅓' (High)
			components.ColorVibrantCyan,  // 1'  (High)
		}
	case DrawbarColorModeHarmonics:
		return [9]components.Color{
			components.ColorBrightWhite,   // 16' (Octave)
			components.ColorVibrantOrange, // 5⅓' (Mutation)
			components.ColorBrightWhite,   // 8'  (Octave)
			components.ColorBrightWhite,   // 4'  (Octave)
			components.ColorVibrantOrange, // 2⅔' (Mutation)
			components.ColorBrightWhite,   // 2'  (Octave)
			components.ColorVibrantOrange, // 1⅗' (Mutation)
			components.ColorVibrantOrange, // 1⅓' (Mutation)
			components.ColorBrightWhite,   // 1'  (Octave)
		}
	case DrawbarColorModePerformance:
		return [9]components.Color{
			components.ColorVibrantGreen,  // 16' (First 3)
			components.ColorVibrantGreen,  // 5⅓' (First 3)
			components.ColorVibrantGreen,  // 8'  (First 3)
			components.ColorDimWhite,      // 4'  (Mid body)
			components.ColorDimWhite,      // 2⅔' (Mid body)
			components.ColorDimWhite,      // 2'  (Mid body)
			components.ColorDimWhite,      // 1⅗' (Mid body)
			components.ColorDimWhite,      // 1⅓' (Mid body)
			components.ColorVibrantYellow, // 1'  (The Whistler)
		}
	default:
		return [9]components.Color{}
	}
}

// organDrawbarLEDs manages the active drawbar color mode and button events.
type organDrawbarLEDs struct {
	mu   sync.Mutex
	mode DrawbarColorMode
}

func newOrganDrawbarLEDs() *organDrawbarLEDs {
	return &organDrawbarLEDs{
		mode: DrawbarColorModeB3Standard,
	}
}

func (o *organDrawbarLEDs) Mode() DrawbarColorMode {
	if o == nil {
		return DrawbarColorModeB3Standard
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.mode
}

func (o *organDrawbarLEDs) SetMode(m DrawbarColorMode) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if m >= 0 && m < numDrawbarColorModes {
		o.mode = m
	}
}

// Cycle advances to the next drawbar coloring mode and returns the new mode
// along with its screen display lines.
func (o *organDrawbarLEDs) Cycle() (DrawbarColorMode, string, string) {
	if o == nil {
		return DrawbarColorModeB3Standard, "DRAWBARS", "B3 STANDARD"
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.mode = (o.mode + 1) % numDrawbarColorModes
	l1, l2 := o.mode.DisplayText()
	return o.mode, l1, l2
}

// CurrentColors returns the 9 button colors for the active mode.
func (o *organDrawbarLEDs) CurrentColors() [9]components.Color {
	if o == nil {
		return DrawbarColorModeB3Standard.Colors()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.mode.Colors()
}

// HandleButton handles a fader button press. Button 1 cycles the coloring mode.
func (o *organDrawbarLEDs) HandleButton(index int, pressed bool) (handled bool, line1, line2 string) {
	if o == nil || index != 1 {
		return false, "", ""
	}
	if !pressed {
		return true, "", ""
	}
	_, line1, line2 = o.Cycle()
	return true, line1, line2
}

type faderButtonColorSetter interface {
	SetFaderButtonColor(index int, color components.Color) error
}

type launchkeyFaderButtonSetter struct {
	get func() *launchkey.Reconciler
}

func (s launchkeyFaderButtonSetter) SetFaderButtonColor(index int, color components.Color) error {
	if s.get == nil {
		return nil
	}
	lk := s.get()
	if lk == nil || lk.State() != "active" {
		return nil
	}
	return lk.SetFaderButtonColor(index, color)
}

// organDrawbarSync coordinates updating the hardware button LEDs when the mode
// changes or when switching between organ and non-organ patches.
type organDrawbarSync struct {
	leds    *organDrawbarLEDs
	setter  faderButtonColorSetter
	isOrgan func() bool

	mu              sync.Mutex
	lastOrgan       bool
	lastMode        DrawbarColorMode
	lastInitialized bool
}

func newOrganDrawbarSync(leds *organDrawbarLEDs, setter faderButtonColorSetter, isOrgan func() bool) *organDrawbarSync {
	return &organDrawbarSync{
		leds:    leds,
		setter:  setter,
		isOrgan: isOrgan,
	}
}

// Reset clears cached hardware state so the next Sync repaints all LEDs.
func (s *organDrawbarSync) Reset() {
	if s == nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lastInitialized = false
}

// Sync pushes the appropriate button LED colors if state has changed.
func (s *organDrawbarSync) Sync() error {
	if s == nil || s.setter == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	organ := s.isOrgan != nil && s.isOrgan()
	mode := s.leds.Mode()
	if s.lastInitialized && s.lastOrgan == organ && (!organ || s.lastMode == mode) {
		return nil
	}

	if organ {
		colors := s.leds.CurrentColors()
		for i := 1; i <= 9; i++ {
			if err := s.setter.SetFaderButtonColor(i, colors[i-1]); err != nil {
				return err
			}
		}
	} else {
		for i := 1; i <= 9; i++ {
			if err := s.setter.SetFaderButtonColor(i, components.ColorOff); err != nil {
				return err
			}
		}
	}

	s.lastOrgan = organ
	s.lastMode = mode
	s.lastInitialized = true
	return nil
}

type leslieButtonHandler interface {
	HandleLeslieButton(pressed bool) bool
	LeslieOn() bool
}

// dispatchFaderButton routes DAW-mode fader buttons on an organ patch:
// - Button 1 cycles the drawbar button coloring mode (when organ is active).
// - Button 9 toggles the Leslie rotary between Stop and Fast.
// Other buttons are left unhandled.
func dispatchFaderButton(e driver.FaderButtonEvent, isOrgan bool, drawbarLEDs *organDrawbarLEDs, drawbarSync *organDrawbarSync, leslie leslieButtonHandler, screen organScreen) bool {
	if e.Index == 1 && isOrgan {
		if drawbarLEDs != nil {
			handled, l1, l2 := drawbarLEDs.HandleButton(e.Index, e.Pressed)
			if handled && e.Pressed {
				if drawbarSync != nil {
					_ = drawbarSync.Sync()
				}
				if screen != nil {
					screen.Show(l1, l2)
				}
				return true
			}
		}
	}
	if e.Index == 9 && leslie != nil && leslie.HandleLeslieButton(e.Pressed) {
		if e.Pressed && screen != nil {
			status := "STOP"
			if leslie.LeslieOn() {
				status = "FAST"
			}
			screen.Show("LESLIE", status)
		}
		return true
	}
	return false
}
