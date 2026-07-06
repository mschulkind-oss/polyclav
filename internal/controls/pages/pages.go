// Package pages implements the Launchkey knob-page state machine
// (docs/ROADMAP.md §2): five named pages of eight knob slots each,
// mapping relative-encoder ticks onto internal/controls setters, with
// screen feedback and a pad row of page indicators.
//
// The package is deliberately driver-agnostic: it never imports
// internal/launchkey/driver. Hardware I/O goes through the two
// single-method seams below (cmd/polyclav adapts the launchkey
// reconciler; tests inject fakes), and transport-button decoding stays
// in main — the state machine only exposes NextPage/PrevPage/
// TogglePlay/HandleKnob. It lives under internal/controls because its
// one and only mutation path is *controls.Controls (the shared
// clamp → audio apply → state persist → hub publish pipeline); nesting
// here mirrors that dependency arrow and keeps internal/launchkey free
// of controls imports.
package pages

import (
	"fmt"
	"sync"

	"github.com/mschulkind-oss/polyclav/internal/controls"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/components"
)

// ScreenWriter is the slice of the Launchkey surface pages needs for the
// 2-line display. cmd/polyclav adapts the reconciler (adding its 800 ms
// restore-to-patch-name timer around every write); tests record writes.
type ScreenWriter interface {
	SetDisplayText(line1, line2 string) error
}

// PadWriter is the slice of the Launchkey surface pages needs for the
// page-indicator pads. Matches the reconciler's SetPadColor signature.
type PadWriter interface {
	SetPadColor(row, col int, color components.Color) error
}

// PlayerControl is the optional audition-player hook for the transport
// Play button (docs/ROADMAP.md §2.5 adapted — see the transport table in
// cmd/polyclav). Toggle restarts the last-used clip when stopped and
// stops it when playing; ok=false means nothing has been played yet this
// session (no clip to restart).
type PlayerControl interface {
	Toggle() (playing bool, clip string, ok bool)
}

// AdjustFunc applies one knob tick's worth of change through the
// controls layer. delta is already step-scaled (raw encoder ticks ×
// Slot.Step). display is the formatted post-clamp value for the screen's
// second line; ok=false means nothing was applied (no patch selected, or
// a native-only parameter while a non-native patch is current) and
// nothing should be shown — the pre-pages hardcoded-knob behavior.
type AdjustFunc func(ctl *controls.Controls, delta float32) (display string, ok bool)

// Slot is one of a page's eight encoder assignments. A zero Slot
// (Adjust == nil) is an intentionally unbound knob.
type Slot struct {
	Label  string  // screen line 1 on turn; ≤16 ASCII chars
	Step   float32 // per-tick delta handed to Adjust (see the step* constants)
	Adjust AdjustFunc
}

// PageDef is one knob page: the name flashed on page switch and the
// eight encoder slots, index 0 = physical knob 1.
type PageDef struct {
	Name  string
	Slots [8]Slot
}

// PageIndicatorRow is the pad row used for page indicators: row 1, the
// bottom row in the driver's DAW layout (notes 112–119). Row 0 (top,
// notes 96–103) stays the patch selector exactly as before — pages never
// touches it. Columns 0..len(pages)-1 light up; columns 5..7 are left
// unpainted for future per-page state pads (docs/ROADMAP.md §2.4).
const PageIndicatorRow = 1

// Page-indicator palette (docs/ROADMAP.md §2.4 names no exact indices
// for page indicators, so these are picked from the named Components
// palette): the active page burns orange, available pages sit dim white,
// and pages gated off by a non-native patch go dark.
const (
	padPageActive      = components.ColorVibrantOrange
	padPageAvailable   = components.ColorDimWhite
	padPageUnavailable = components.ColorOff
)

// Pages is the knob-page state machine. All methods are goroutine-safe:
// knob/transport events, the patch-change hub follower, and the
// reconnect repaint arrive on different goroutines.
type Pages struct {
	ctl    *controls.Controls
	screen ScreenWriter
	pads   PadWriter
	defs   []PageDef

	mu     sync.Mutex
	page   int  // current page index into defs
	native bool // current patch is a native synth (set via OnPatchChange)
	player PlayerControl
}

// New builds the state machine over the standard page table (see
// pageDefs). It starts on page 0 assuming a non-native patch; callers
// must invoke OnPatchChange with the current patch's type once known
// (and again on every patch change) to unlock the synth pages.
func New(ctl *controls.Controls, screen ScreenWriter, pads PadWriter) *Pages {
	return &Pages{ctl: ctl, screen: screen, pads: pads, defs: pageDefs()}
}

// AttachPlayer wires the optional audition-player toggle for the
// transport Play button. Without it TogglePlay is a silent no-op.
func (p *Pages) AttachPlayer(pc PlayerControl) {
	p.mu.Lock()
	p.player = pc
	p.mu.Unlock()
}

// HandleKnob routes one relative-encoder event (driver KnobEvent shape:
// index 1..8, signed tick delta) through the current page's slot. On a
// successful apply the screen shows "Label" / formatted value; unbound
// slots and refused applies (no patch / non-native gate) show nothing.
func (p *Pages) HandleKnob(index int, delta int8) {
	if index < 1 || index > 8 || delta == 0 {
		return
	}
	p.mu.Lock()
	page := p.page
	p.mu.Unlock()
	slot := p.defs[page].Slots[index-1]
	if slot.Adjust == nil {
		return
	}
	display, ok := slot.Adjust(p.ctl, float32(delta)*slot.Step)
	if !ok {
		return
	}
	_ = p.screen.SetDisplayText(slot.Label, display)
}

// NextPage advances to the next page (wrapping), flashes the page name,
// and repaints the indicators. While a non-native patch is selected only
// page 0 (MAIN) is live: the switch is refused and the screen shows
// "(native only)" instead (docs/ROADMAP.md §2.2 adapted — the synth
// pages drive parameters that do not exist off the native engine).
func (p *Pages) NextPage() { p.cycle(1) }

// PrevPage is NextPage's other direction.
func (p *Pages) PrevPage() { p.cycle(-1) }

func (p *Pages) cycle(dir int) {
	p.mu.Lock()
	if !p.native {
		name := p.defs[p.page].Name
		p.mu.Unlock()
		_ = p.screen.SetDisplayText("(native only)", name)
		return
	}
	n := len(p.defs)
	p.page = (p.page + dir + n) % n
	page := p.page
	p.mu.Unlock()
	_ = p.screen.SetDisplayText(p.defs[page].Name, fmt.Sprintf("Page %d/%d", page+1, n))
	p.paintPads(page, true)
}

// CurrentPage reports the active page's index and name.
func (p *Pages) CurrentPage() (index int, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.page, p.defs[p.page].Name
}

// OnPatchChange re-clamps page availability for the new patch type and
// refreshes the indicators. Leaving the native engine snaps back to page
// 0 (only MAIN is meaningful there); native→native switches keep the
// page (per-patch page persistence — ROADMAP §3.1's `page` field — is
// deferred with the rest of that schema).
func (p *Pages) OnPatchChange(patchType string) {
	native := patchType == "native"
	p.mu.Lock()
	p.native = native
	if !native {
		p.page = 0
	}
	page := p.page
	p.mu.Unlock()
	p.paintPads(page, native)
}

// RefreshPads repaints the page-indicator row from current state — the
// reconnect hook (pad LEDs reset when the device power-cycles).
func (p *Pages) RefreshPads() {
	p.mu.Lock()
	page, native := p.page, p.native
	p.mu.Unlock()
	p.paintPads(page, native)
}

func (p *Pages) paintPads(active int, native bool) {
	for i := range p.defs {
		c := padPageAvailable
		if !native && i != 0 {
			c = padPageUnavailable
		}
		if i == active {
			c = padPageActive
		}
		_ = p.pads.SetPadColor(PageIndicatorRow, i, c)
	}
}

// TogglePlay flips the audition player (transport Play button) and
// flashes the result. No-op without an attached PlayerControl.
func (p *Pages) TogglePlay() {
	p.mu.Lock()
	pc := p.player
	p.mu.Unlock()
	if pc == nil {
		return
	}
	playing, clip, ok := pc.Toggle()
	switch {
	case !ok:
		_ = p.screen.SetDisplayText("(no clip)", "")
	case playing:
		_ = p.screen.SetDisplayText("PLAY", clip)
	default:
		_ = p.screen.SetDisplayText("STOP", clip)
	}
}
