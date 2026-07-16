package pages

// HardwarePage is a serializable view of one knob page for the web hardware
// map — the page name and its eight slot labels ("" = an unbound knob).
type HardwarePage struct {
	Name  string   `json:"name"`
	Knobs []string `json:"knobs"`
}

// HardwareMap returns the Launchkey knob pages as read-only dictated data for
// the web "hardware map" reference screen (docs/PEDALBOARD_UI.md 3d): what each
// encoder on each page controls. The per-slot Adjust funcs are deliberately not
// exposed — this is a manual, not a control surface.
func HardwareMap() []HardwarePage {
	defs := pageDefs()
	out := make([]HardwarePage, len(defs))
	for i, d := range defs {
		knobs := make([]string, len(d.Slots))
		for j, s := range d.Slots {
			knobs[j] = s.Label
		}
		out[i] = HardwarePage{Name: d.Name, Knobs: knobs}
	}
	return out
}
