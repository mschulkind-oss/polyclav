package pages

// Param is one instrument-native parameter exposed by a backend that has
// no host-curated page layout — the unit of the generic parameter browser
// (docs/design/instrument-control-surface.md, "an explicitly labeled
// generic parameter browser"). The backend supplies the label, the
// per-detent step in its own units, and the adjust closure; the host
// decides only which physical encoder drives it.
//
// Adjust is called with a *controls.Controls for symmetry with Slot, but a
// backend-owned parameter (a CLAP id, for example) is free to ignore it
// and write through its own setter. It returns the formatted post-clamp
// value for the screen, or ok=false when nothing was applied.
type Param struct {
	Label  string
	Step   float32
	Adjust AdjustFunc
}

// ParamSource supplies the active instrument's own parameter list to the
// generic browser. Params is consulted lazily — on every page change and
// knob turn — so a backend that republishes its parameter set after a
// patch load needs no explicit notification. Returning nil or an empty
// list means the active backend exposes no browsable parameters, and the
// browser is just the curated host pages.
//
// Implementations must be safe to call while Pages holds its lock and must
// not call back into Pages, ScreenWriter or PadWriter.
type ParamSource interface {
	Params() []Param
}
