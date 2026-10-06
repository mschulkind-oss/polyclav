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

// pageIndicatorCols is the number of bottom-row pads reserved for page
// indicators (columns 0–4, notes 112–116). Columns 5–7 (notes 117–119)
// stay free for the user's own OSC/mixer bindings, so the generic
// parameter browser shows at most this many page indicators and relies on
// the screen's "n/M" beyond that.
const pageIndicatorCols = 5

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
	page   int  // cursor into the browser list (see slotAtLocked)
	native bool // current patch is a native synth (set via OnPatchChange)
	player PlayerControl
	params ParamSource // optional instrument-owned parameters (see ParamSource)
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

// AttachParams wires the optional instrument-owned parameter list for the
// generic browser. Called once at startup; the source is read lazily on
// every page change and knob turn, so a backend that refreshes its list
// after a patch load needs no further call.
func (p *Pages) AttachParams(src ParamSource) {
	p.mu.Lock()
	p.params = src
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
	slot, ok := p.slotAtLocked(p.page, index-1)
	p.mu.Unlock()
	if !ok || slot.Adjust == nil {
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
		name := p.defs[0].Name
		p.mu.Unlock()
		_ = p.screen.SetDisplayText("(native only)", name)
		return
	}
	n := len(p.defs)
	p.page = (p.page + dir + n) % n
	page := p.page
	p.paintPadsLocked()
	p.mu.Unlock()
	_ = p.screen.SetDisplayText(p.defs[page].Name, fmt.Sprintf("Page %d/%d", page+1, n))
}

// NextParamPage advances the encoder-bank cursor one page through the
// full browser list and flashes the new page. The list is the curated host
// pages first (MAIN for a non-native patch, MAIN/OSC/FILTER/AMP/LFO-MOD
// for the native engine), then the active instrument's own parameters, 8
// per page. These are the two buttons beside the encoders, so unlike
// Scene ↑/↓ they reach the instrument parameters a plugin or engine
// exposes beyond the host's curated assignments.
func (p *Pages) NextParamPage() { p.stepBrowser(1) }

// PrevParamPage is NextParamPage's other direction.
func (p *Pages) PrevParamPage() { p.stepBrowser(-1) }

func (p *Pages) stepBrowser(dir int) {
	p.mu.Lock()
	n := p.pageCountLocked()
	p.page = (p.page + dir + n) % n
	page := p.page
	title, sub := p.browserFlashLocked(page)
	p.paintPadsLocked()
	p.mu.Unlock()
	_ = p.screen.SetDisplayText(title, sub)
}

// slotAtLocked resolves encoder idx (0-based) on browser page to its Slot,
// spanning the curated host pages first and the instrument parameter
// pages after them. Callers hold p.mu because it reads the live parameter
// list, which may change under it as a backend republishes.
func (p *Pages) slotAtLocked(page, idx int) (Slot, bool) {
	host := p.hostPageCountLocked()
	if page < host {
		if p.native {
			return p.defs[page].Slots[idx], true
		}
		return p.defs[0].Slots[idx], true
	}
	params := p.paramListLocked()
	i := (page-host)*8 + idx
	if i < 0 || i >= len(params) {
		return Slot{}, false
	}
	return Slot{Label: params[i].Label, Step: params[i].Step, Adjust: params[i].Adjust}, true
}

// hostPageCountLocked is how many curated pages the browser starts with.
func (p *Pages) hostPageCountLocked() int {
	if p.native {
		return len(p.defs)
	}
	return 1
}

func (p *Pages) paramListLocked() []Param {
	if p.params == nil {
		return nil
	}
	return p.params.Params()
}

func (p *Pages) paramPageCountLocked() int {
	return (len(p.paramListLocked()) + 7) / 8
}

func (p *Pages) pageCountLocked() int {
	return p.hostPageCountLocked() + p.paramPageCountLocked()
}

// browserTitleLocked names the page for CurrentPage and the browser flash:
// the curated page's own name, or PARAMS for an instrument-owned page.
func (p *Pages) browserTitleLocked(page int) string {
	host := p.hostPageCountLocked()
	if page < host {
		if p.native {
			return p.defs[page].Name
		}
		return p.defs[0].Name
	}
	return "PARAMS"
}

// browserFlashLocked is the two-line screen popup for a browser page:
// page name / position (position is within that page's own group).
func (p *Pages) browserFlashLocked(page int) (string, string) {
	host := p.hostPageCountLocked()
	if page < host {
		name := p.defs[0].Name
		if p.native {
			name = p.defs[page].Name
		}
		return name, fmt.Sprintf("Page %d/%d", page+1, host)
	}
	pp := page - host
	n := p.paramPageCountLocked()
	return "PARAMS", fmt.Sprintf("%d/%d", pp+1, n)
}

// CurrentPage reports the active page's index and name.
func (p *Pages) CurrentPage() (index int, name string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.page, p.browserTitleLocked(p.page)
}

// OnPatchChange re-clamps page availability for the new patch type and
// refreshes the indicators. Leaving the native engine snaps back to page
// 0 (only MAIN is meaningful there); native→native switches keep the
// page (per-patch page persistence — ROADMAP §3.1's `page` field — is
// deferred with the rest of that schema).
func (p *Pages) OnPatchChange(patchType string) {
	native := patchType == "native"
	p.mu.Lock()
	wasNative := p.native
	p.native = native
	// Snap home on any change out of the parameter domain: leaving the
	// native engine, or arriving at it from a non-native patch whose
	// parameter pages do not exist here. A cursor left beyond the new page
	// count also comes home. Native→native switches keep the page.
	if !native || !wasNative || p.page >= p.pageCountLocked() {
		p.page = 0
	}
	p.paintPadsLocked()
	p.mu.Unlock()
}

// RefreshPads repaints the page-indicator row from current state — the
// reconnect hook (pad LEDs reset when the device power-cycles).
func (p *Pages) RefreshPads() {
	p.mu.Lock()
	p.paintPadsLocked()
	p.mu.Unlock()
}

// paintPadsLocked repaints the page-indicator row from the LIVE state;
// callers hold p.mu. Painting inside the lock (instead of from values
// captured before releasing it) serializes paints with state changes,
// so the last paint always reflects the last cycle/OnPatchChange and a
// racing repaint can never overwrite fresh gating with stale colors.
// The PadWriter therefore must not call back into Pages (the reconciler
// adapter in cmd/polyclav does not).
func (p *Pages) paintPadsLocked() {
	host := p.hostPageCountLocked()
	if p.page >= host {
		// A generic parameter page: columns 0..N-1 show the position
		// inside the instrument's own parameters. Only the reserved
		// indicator columns are painted; beyond that the screen's "n/M"
		// carries the orientation.
		n := p.paramPageCountLocked()
		pp := p.page - host
		for col := 0; col < pageIndicatorCols; col++ {
			c := padPageUnavailable
			if col < n {
				c = padPageAvailable
			}
			if col == pp {
				c = padPageActive
			}
			_ = p.pads.SetPadColor(PageIndicatorRow, col, c)
		}
		return
	}
	for i := range p.defs {
		c := padPageAvailable
		if !p.native && i != 0 {
			c = padPageUnavailable
		}
		if i == p.page {
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
