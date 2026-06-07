package osc

// Binding maps a single MIDI control event to an OSC dispatch.
//
//	[[osc.xr18.bindings]]
//	source_kind = "cc"        # "cc" (only kind supported for v0)
//	channel     = 16          # 1..16, MIDI channel
//	controller  = 13          # CC number (or Note number for source_kind="note")
//	osc         = "/lr/mix/fader"
//	transform   = "scalar"    # "scalar" -> float32 in 0.0..1.0
type Binding struct {
	SourceKind string `toml:"source_kind"`
	Channel    int    `toml:"channel"`
	Controller int    `toml:"controller"`
	OSC        string `toml:"osc"`
	Transform  string `toml:"transform"`
}
