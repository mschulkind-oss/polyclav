package main

import (
	"fmt"
	"sync"

	"github.com/mschulkind-oss/polyclav/internal/launchkey"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/driver"
)

// DrawbarColorMode selects how the nine fader button LEDs are colored on an
// active organ.
type DrawbarColorMode int

const (
	// DrawbarColorModeB3Standard matches the physical drawbar handles of a Hammond B3,
	// offering 5 vibrant selectable color schemes (cycled by button 2).
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

// B3ColorScheme defines the color palette for the default B3 drawbar mode.
type B3ColorScheme int

const (
	// B3ColorSchemeClassic uses traditional brown for 16'/5⅓', white for octaves,
	// and red for black mutation drawbars.
	B3ColorSchemeClassic B3ColorScheme = iota

	// B3ColorSchemeNeon uses vibrant orange for sub-octaves, cyan for octaves,
	// and pink for mutations.
	B3ColorSchemeNeon

	// B3ColorSchemeOcean uses bright yellow for sub-octaves, white for octaves,
	// and blue for mutations.
	B3ColorSchemeOcean

	// B3ColorSchemeTriad uses bright yellow for sub-octaves, cyan for octaves,
	// and red for mutations.
	B3ColorSchemeTriad

	// B3ColorSchemeCandy uses hot pink for sub-octaves, white for octaves,
	// and cyan for mutations.
	B3ColorSchemeCandy

	numB3ColorSchemes
)

// Name returns the display name of the B3 color scheme.
func (s B3ColorScheme) Name() string {
	switch s {
	case B3ColorSchemeClassic:
		return "CLASSIC"
	case B3ColorSchemeNeon:
		return "NEON"
	case B3ColorSchemeOcean:
		return "OCEAN"
	case B3ColorSchemeTriad:
		return "TRIAD"
	case B3ColorSchemeCandy:
		return "CANDY"
	default:
		return "UNKNOWN"
	}
}

// Colors returns the 9 button palette colors for this B3 color scheme.
func (s B3ColorScheme) Colors() [9]components.Color {
	switch s {
	case B3ColorSchemeClassic:
		return [9]components.Color{
			components.ColorBrown,       // 16'
			components.ColorBrown,       // 5⅓'
			components.ColorBrightWhite, // 8'
			components.ColorBrightWhite, // 4'
			components.ColorVibrantRed,  // 2⅔' (mutation)
			components.ColorBrightWhite, // 2'
			components.ColorVibrantRed,  // 1⅗' (mutation)
			components.ColorVibrantRed,  // 1⅓' (mutation)
			components.ColorBrightWhite, // 1'
		}
	case B3ColorSchemeNeon:
		return [9]components.Color{
			components.ColorVibrantOrange, // 16'
			components.ColorVibrantOrange, // 5⅓'
			components.ColorVibrantCyan,   // 8'
			components.ColorVibrantCyan,   // 4'
			components.ColorVibrantPink,   // 2⅔' (mutation)
			components.ColorVibrantCyan,   // 2'
			components.ColorVibrantPink,   // 1⅗' (mutation)
			components.ColorVibrantPink,   // 1⅓' (mutation)
			components.ColorVibrantCyan,   // 1'
		}
	case B3ColorSchemeOcean:
		return [9]components.Color{
			components.ColorVibrantYellow, // 16'
			components.ColorVibrantYellow, // 5⅓'
			components.ColorBrightWhite,   // 8'
			components.ColorBrightWhite,   // 4'
			components.ColorVibrantBlue,   // 2⅔' (mutation)
			components.ColorBrightWhite,   // 2'
			components.ColorVibrantBlue,   // 1⅗' (mutation)
			components.ColorVibrantBlue,   // 1⅓' (mutation)
			components.ColorBrightWhite,   // 1'
		}
	case B3ColorSchemeTriad:
		return [9]components.Color{
			components.ColorVibrantYellow, // 16'
			components.ColorVibrantYellow, // 5⅓'
			components.ColorVibrantCyan,   // 8'
			components.ColorVibrantCyan,   // 4'
			components.ColorVibrantRed,    // 2⅔' (mutation)
			components.ColorVibrantCyan,   // 2'
			components.ColorVibrantRed,    // 1⅗' (mutation)
			components.ColorVibrantRed,    // 1⅓' (mutation)
			components.ColorVibrantCyan,   // 1'
		}
	case B3ColorSchemeCandy:
		return [9]components.Color{
			components.ColorVibrantPink, // 16'
			components.ColorVibrantPink, // 5⅓'
			components.ColorBrightWhite, // 8'
			components.ColorBrightWhite, // 4'
			components.ColorVibrantCyan, // 2⅔' (mutation)
			components.ColorBrightWhite, // 2'
			components.ColorVibrantCyan, // 1⅗' (mutation)
			components.ColorVibrantCyan, // 1⅓' (mutation)
			components.ColorBrightWhite, // 1'
		}
	default:
		return B3ColorSchemeClassic.Colors()
	}
}

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
		return B3ColorSchemeClassic.Colors()
	case DrawbarColorModeRegisters:
		return [9]components.Color{
			components.ColorVibrantRed,  // 16' (Low)
			components.ColorVibrantRed,  // 5⅓' (Low)
			components.ColorVibrantRed,  // 8'  (Low)
			components.ColorGreen,       // 4'  (Mid)
			components.ColorGreen,       // 2⅔' (Mid)
			components.ColorGreen,       // 2'  (Mid)
			components.ColorVibrantCyan, // 1⅗' (High)
			components.ColorVibrantCyan, // 1⅓' (High)
			components.ColorVibrantCyan, // 1'  (High)
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
			components.ColorGreen,         // 16' (First 3)
			components.ColorGreen,         // 5⅓' (First 3)
			components.ColorGreen,         // 8'  (First 3)
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
	mu       sync.Mutex
	mode     DrawbarColorMode
	b3Scheme B3ColorScheme
}

func newOrganDrawbarLEDs() *organDrawbarLEDs {
	return &organDrawbarLEDs{
		mode:     DrawbarColorModeB3Standard,
		b3Scheme: B3ColorSchemeClassic,
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

func (o *organDrawbarLEDs) B3Scheme() B3ColorScheme {
	if o == nil {
		return B3ColorSchemeClassic
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.b3Scheme
}

func (o *organDrawbarLEDs) SetB3Scheme(s B3ColorScheme) {
	if o == nil {
		return
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if s >= 0 && s < numB3ColorSchemes {
		o.b3Scheme = s
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
	if o.mode == DrawbarColorModeB3Standard && o.b3Scheme != B3ColorSchemeClassic {
		l2 = "B3: " + o.b3Scheme.Name()
	}
	return o.mode, l1, l2
}

// CycleB3Scheme advances to the next B3 color scheme, activates B3Standard mode,
// and returns the active scheme with its screen display lines.
func (o *organDrawbarLEDs) CycleB3Scheme() (B3ColorScheme, string, string) {
	if o == nil {
		return B3ColorSchemeClassic, "B3 SCHEME", "1: CLASSIC"
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	o.mode = DrawbarColorModeB3Standard
	o.b3Scheme = (o.b3Scheme + 1) % numB3ColorSchemes
	line1 := "B3 SCHEME"
	line2 := fmt.Sprintf("%d: %s", int(o.b3Scheme)+1, o.b3Scheme.Name())
	return o.b3Scheme, line1, line2
}

// CurrentColors returns the 9 button colors for the active mode.
func (o *organDrawbarLEDs) CurrentColors() [9]components.Color {
	if o == nil {
		return B3ColorSchemeClassic.Colors()
	}
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.mode == DrawbarColorModeB3Standard {
		return o.b3Scheme.Colors()
	}
	return o.mode.Colors()
}

// HandleButton handles a fader button press:
// - Button 1 cycles the drawbar coloring mode (B3, Registers, Harmonics, Performance).
// - Button 2 cycles through the 5 color schemes for the default B3 drawbars.
func (o *organDrawbarLEDs) HandleButton(index int, pressed bool) (handled bool, line1, line2 string) {
	if o == nil {
		return false, "", ""
	}
	if index == 1 {
		if !pressed {
			return true, "", ""
		}
		_, line1, line2 = o.Cycle()
		return true, line1, line2
	}
	if index == 2 {
		if !pressed {
			return true, "", ""
		}
		_, line1, line2 = o.CycleB3Scheme()
		return true, line1, line2
	}
	return false, "", ""
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
// changes, when Leslie starts or stops, or when switching between organ and non-organ patches.
type organDrawbarSync struct {
	leds       *organDrawbarLEDs
	setter     faderButtonColorSetter
	isOrgan    func() bool
	isLeslieOn func() bool

	mu              sync.Mutex
	lastOrgan       bool
	lastMode        DrawbarColorMode
	lastB3Scheme    B3ColorScheme
	lastLeslieOn    bool
	lastInitialized bool
}

func newOrganDrawbarSync(leds *organDrawbarLEDs, setter faderButtonColorSetter, isOrgan func() bool, isLeslieOn func() bool) *organDrawbarSync {
	return &organDrawbarSync{
		leds:       leds,
		setter:     setter,
		isOrgan:    isOrgan,
		isLeslieOn: isLeslieOn,
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
// On organ patches, buttons 1–8 reflect the active drawbar color mode, while
// button 9 acts as a dedicated Leslie indicator (Green when running, Off when stopped).
func (s *organDrawbarSync) Sync() error {
	if s == nil || s.setter == nil {
		return nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()

	organ := s.isOrgan != nil && s.isOrgan()
	mode := s.leds.Mode()
	b3Scheme := s.leds.B3Scheme()
	leslieOn := s.isLeslieOn != nil && s.isLeslieOn()
	if s.lastInitialized && s.lastOrgan == organ && (!organ || (s.lastMode == mode && s.lastB3Scheme == b3Scheme && s.lastLeslieOn == leslieOn)) {
		return nil
	}

	if organ {
		colors := s.leds.CurrentColors()
		for i := 1; i <= 8; i++ {
			if err := s.setter.SetFaderButtonColor(i, colors[i-1]); err != nil {
				return err
			}
		}
		leslieColor := components.ColorOff
		if leslieOn {
			leslieColor = components.ColorGreen
		}
		if err := s.setter.SetFaderButtonColor(9, leslieColor); err != nil {
			return err
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
	s.lastB3Scheme = b3Scheme
	s.lastLeslieOn = leslieOn
	s.lastInitialized = true
	return nil
}

type leslieButtonHandler interface {
	HandleLeslieButton(pressed bool) bool
	LeslieOn() bool
}

// dispatchFaderButton routes DAW-mode fader buttons on an organ patch:
// - Button 1 cycles the drawbar button coloring mode (when organ is active).
// - Button 2 cycles the 5 color schemes for default B3 drawbars (when organ is active).
// - Button 9 toggles the Leslie rotary between Stop and Fast and syncs the status LED.
// Other buttons are left unhandled.
func dispatchFaderButton(e driver.FaderButtonEvent, isOrgan bool, drawbarLEDs *organDrawbarLEDs, drawbarSync *organDrawbarSync, leslie leslieButtonHandler, screen organScreen) bool {
	if (e.Index == 1 || e.Index == 2) && isOrgan {
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
		if e.Pressed {
			if drawbarSync != nil {
				_ = drawbarSync.Sync()
			}
			if screen != nil {
				status := "STOP"
				if leslie.LeslieOn() {
					status = "FAST"
				}
				screen.Show("LESLIE", status)
			}
		}
		return true
	}
	return false
}
