package controls

// The post-synth pedal chain (drive → chorus → tremolo → analog-delay).
// Every stage runs in the shared post-synth DSP chain in Rust, so it
// applies to every synth backend (soundfont, sfizz, LV2, CLAP, native) —
// the same backend-agnostic property as the drive pedal, master volume,
// reverb, and compressor. This file is the single registry of the chain:
// which params exist, their engine-unit ranges/defaults, and which audio
// setter each one drives. The web /api/chain endpoint, the SSE "chain"
// change, the state.Knob columns, and the patch-select restore all read
// from this one table so a new param is added in exactly one place.
//
// Enable model: there is NO Rust-side bypass. A stage is "disabled" by
// parking its GATE param (the one audible wet/amount/depth control) at 0
// in the engine while the stored value is preserved, so re-enabling
// restores it. Non-gate params (rates, times, feedback, depth where it
// is not the gate) are pushed at their stored value regardless — they
// are inaudible until the gate opens.

import (
	"errors"
	"strings"

	"github.com/mschulkind-oss/polyclav/internal/state"
)

// Taper is a param's UI mapping hint: how a normalized 0..1 slider
// position maps onto the engine-unit value. The audio engine always
// receives engine units; the taper only tells a UI how to distribute the
// range across a control (Exp = perceptually-even for rates/times).
type Taper int

const (
	TaperLinear Taper = iota
	TaperExp
)

// String renders the taper for the wire ("linear"/"exp").
func (t Taper) String() string {
	if t == TaperExp {
		return "exp"
	}
	return "linear"
}

// ChainParam is one tweakable knob of a chain stage. ID is the stable
// "<stage>.<leaf>" wire id (e.g. "chorus.rate_hz"); StateKey is the
// state.Knob column it persists through (matches
// state.Store.UpdatePatchKnob / knobField). Min/Max/Default are in engine
// units. Gate marks the param that a disabled stage parks at 0. set
// drives the audio engine for this param.
type ChainParam struct {
	ID       string
	StateKey string
	Label    string
	Unit     string
	Min      float32
	Max      float32
	Default  float32
	Taper    Taper
	Step     float32
	Gate     bool
	set      func(Audio, float32)
}

// ChainStage is one pedal: an id/label, a UI "kind" (rendering hint), the
// state stage key its enable flag persists through, and its params in
// display order.
type ChainStage struct {
	ID     string
	Label  string
	Kind   string
	Params []ChainParam
}

// chainStages is the registry — the single source of the chain's schema.
// Ranges/defaults mirror the Rust clamps documented on the audio setters
// (internal/audio/audio.go) and state.Defaults(); keep the three in
// lockstep. The stage/param order here is the canonical (schema) order;
// the user-visible order is state.PedalOrder (display-only).
var chainStages = []ChainStage{
	{
		ID: "drive", Label: "Drive", Kind: "drive",
		Params: []ChainParam{
			{ID: "drive.amount", StateKey: "drive_pedal", Label: "Amount", Unit: "",
				Min: 0, Max: 1, Default: 0, Taper: TaperLinear, Step: 0.01, Gate: true,
				set: func(a Audio, v float32) { a.SetDrivePedal(v) }},
		},
	},
	{
		ID: "chorus", Label: "Chorus", Kind: "chorus",
		Params: []ChainParam{
			{ID: "chorus.rate_hz", StateKey: "chorus_rate_hz", Label: "Rate", Unit: "Hz",
				Min: 0.02, Max: 5, Default: 0.8, Taper: TaperExp, Step: 0.01, Gate: false,
				set: func(a Audio, v float32) { a.SetChorusRateHz(v) }},
			{ID: "chorus.depth", StateKey: "chorus_depth", Label: "Depth", Unit: "",
				Min: 0, Max: 1, Default: 0, Taper: TaperLinear, Step: 0.01, Gate: false,
				set: func(a Audio, v float32) { a.SetChorusDepth(v) }},
			{ID: "chorus.mix", StateKey: "chorus_mix", Label: "Mix", Unit: "",
				Min: 0, Max: 1, Default: 0, Taper: TaperLinear, Step: 0.01, Gate: true,
				set: func(a Audio, v float32) { a.SetChorusMix(v) }},
		},
	},
	{
		ID: "tremolo", Label: "Tremolo", Kind: "tremolo",
		Params: []ChainParam{
			{ID: "tremolo.rate_hz", StateKey: "tremolo_rate_hz", Label: "Rate", Unit: "Hz",
				Min: 0.05, Max: 20, Default: 4, Taper: TaperExp, Step: 0.01, Gate: false,
				set: func(a Audio, v float32) { a.SetTremoloRateHz(v) }},
			{ID: "tremolo.depth", StateKey: "tremolo_depth", Label: "Depth", Unit: "",
				Min: 0, Max: 1, Default: 0, Taper: TaperLinear, Step: 0.01, Gate: true,
				set: func(a Audio, v float32) { a.SetTremoloDepth(v) }},
		},
	},
	{
		ID: "delay", Label: "Analog Delay", Kind: "delay",
		Params: []ChainParam{
			{ID: "delay.time_ms", StateKey: "delay_time_ms", Label: "Time", Unit: "ms",
				Min: 1, Max: 1000, Default: 300, Taper: TaperExp, Step: 1, Gate: false,
				set: func(a Audio, v float32) { a.SetAnalogDelayTimeMs(v) }},
			{ID: "delay.feedback", StateKey: "delay_feedback", Label: "Feedback", Unit: "",
				Min: 0, Max: 0.9, Default: 0, Taper: TaperLinear, Step: 0.01, Gate: false,
				set: func(a Audio, v float32) { a.SetAnalogDelayFeedback(v) }},
			{ID: "delay.mix", StateKey: "delay_mix", Label: "Mix", Unit: "",
				Min: 0, Max: 1, Default: 0, Taper: TaperLinear, Step: 0.01, Gate: true,
				set: func(a Audio, v float32) { a.SetAnalogDelayMix(v) }},
		},
	},
}

// chainParamRef locates a param plus the stage it belongs to (for the
// gate/enable check).
type chainParamRef struct {
	param   ChainParam
	stageID string
}

var (
	chainParamByID = map[string]chainParamRef{}
	chainStageByID = map[string]*ChainStage{}
)

func init() {
	for i := range chainStages {
		stg := &chainStages[i]
		chainStageByID[stg.ID] = stg
		for _, pm := range stg.Params {
			chainParamByID[pm.ID] = chainParamRef{param: pm, stageID: stg.ID}
		}
	}
}

// ErrUnknownChainParam is returned by SetChainParam for an id not in the
// registry; the web layer maps it to a per-field error.
var ErrUnknownChainParam = errors.New("unknown chain param")

// ErrUnknownChainStage is returned by SetChainEnable / SetPedalOrder for
// a stage id not in the registry.
var ErrUnknownChainStage = errors.New("unknown chain stage")

// ChainStages returns a read-only copy of the registry (the schema).
func ChainStages() []ChainStage {
	out := make([]ChainStage, len(chainStages))
	copy(out, chainStages)
	return out
}

// chainParamLeaf is the "<leaf>" half of a "<stage>.<leaf>" param id.
func chainParamLeaf(id string) string {
	if _, leaf, ok := strings.Cut(id, "."); ok {
		return leaf
	}
	return id
}

// knobEnable reads a stage's enable flag off a state.Knob.
func knobEnable(k state.Knob, stage string) bool {
	switch stage {
	case "drive":
		return k.DriveEnabled
	case "chorus":
		return k.ChorusEnabled
	case "tremolo":
		return k.TremoloEnabled
	case "delay":
		return k.DelayEnabled
	}
	return false
}

// SetChainParam sets one chain param (by "<stage>.<leaf>" id) for the
// current patch: clamp to the registry range, persist the value, push the
// EFFECTIVE value to the engine (0 when the param gates a disabled
// stage), and publish a "chain" change carrying the STORED value.
// Mirrors setKnob's lock/clamp/persist/publish discipline. Returns the
// clamped value; ErrUnknownChainParam for an unknown id, errNoPatch when
// no patch is selected.
func (c *Controls) SetChainParam(id string, v float32) (float32, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	ref, ok := chainParamByID[id]
	if !ok {
		return 0, ErrUnknownChainParam
	}
	cur := c.reg.Current()
	if cur == nil {
		return 0, errNoPatch
	}
	pm := ref.param
	v = clampRange(v, pm.Min, pm.Max)
	// Read the enable BEFORE the store update (only the float changes) so
	// the gate decision uses this patch's current enable flag.
	enabled := knobEnable(c.st.PatchKnob(cur.Name), ref.stageID)
	c.st.UpdatePatchKnob(cur.Name, pm.StateKey, v)
	eff := v
	if pm.Gate && !enabled {
		eff = 0
	}
	pm.set(c.audio, eff)
	c.publishChain(id, v, cur.Name)
	return v, nil
}

// SetChainEnable toggles a chain stage on/off for the current patch:
// persist the flag, then re-push the stage's gate param(s) at their
// stored value (on) or 0 (off) — the only audible effect of the toggle,
// since there is no Rust bypass. Publishes a "chain" change keyed
// "<stage>.enabled". Returns the applied flag; ErrUnknownChainStage for
// an unknown stage, errNoPatch when no patch is selected.
func (c *Controls) SetChainEnable(stage string, on bool) (bool, error) {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	stg, ok := chainStageByID[stage]
	if !ok {
		return false, ErrUnknownChainStage
	}
	cur := c.reg.Current()
	if cur == nil {
		return false, errNoPatch
	}
	c.st.UpdatePatchEnable(cur.Name, stage, on)
	k := c.st.PatchKnob(cur.Name)
	for _, pm := range stg.Params {
		if !pm.Gate {
			continue
		}
		v := knobField(k, pm.StateKey)
		if !on {
			v = 0
		}
		pm.set(c.audio, v)
	}
	c.publishChain(stage+".enabled", on, cur.Name)
	return on, nil
}

// fxOrderStages is the reorderable post-synth FX set, in engine slot order
// (the index IS the fx-order slot polyclav_dsp_set_fx_order expects). It
// includes comp and reverb — the pedalboard treats them as reorderable
// pedals even though their params live on the bus (compressor / reverb send).
var fxOrderStages = []string{"drive", "chorus", "tremolo", "delay", "comp", "reverb"}

// fxSlot maps an fx stage id to its engine slot index (0..5).
var fxSlot = func() map[string]int {
	m := make(map[string]int, len(fxOrderStages))
	for i, id := range fxOrderStages {
		m[id] = i
	}
	return m
}()

// packFxOrder encodes a full permutation of fxOrderStages as the six-nibble
// word polyclav_dsp_set_fx_order expects: the slot to apply at chain position
// p sits in bits [4p, 4p+4).
func packFxOrder(order []string) uint32 {
	var packed uint32
	for pos, id := range order {
		packed |= uint32(fxSlot[id]) << (4 * uint(pos))
	}
	return packed
}

// validFxOrder reports whether order is a full permutation of fxOrderStages.
func validFxOrder(order []string) bool {
	if len(order) != len(fxOrderStages) {
		return false
	}
	seen := make(map[string]bool, len(order))
	for _, id := range order {
		if _, ok := fxSlot[id]; !ok || seen[id] {
			return false
		}
		seen[id] = true
	}
	return true
}

// SetPedalOrder replaces the GLOBAL FX chain order and pushes it to the engine,
// actually reordering the six pedals in the signal path (the master tail stays
// fixed). Requires a full permutation of the six FX stage ids; persists it,
// packs it to the engine, and publishes a "chain" change keyed "order". Allowed
// with no patch selected (the order is global).
func (c *Controls) SetPedalOrder(order []string) error {
	c.applyMu.Lock()
	defer c.applyMu.Unlock()
	if !validFxOrder(order) {
		return ErrUnknownChainStage
	}
	if err := c.st.SetPedalOrder(order); err != nil {
		return err
	}
	c.audio.SetFxOrder(packFxOrder(order))
	c.hub.Publish(Change{Type: "chain", Data: map[string]any{
		"field": "order",
		"order": append([]string(nil), order...),
	}})
	return nil
}

// publishChain emits one "chain" change (a param set, an enable toggle).
// field is the param id or "<stage>.enabled"; value is the stored number
// or the bool.
func (c *Controls) publishChain(field string, value any, patch string) {
	c.hub.Publish(Change{Type: "chain", Data: map[string]any{
		"field": field,
		"value": value,
		"patch": patch,
	}})
}

// restoreChain pushes every chain param to the engine at its effective
// value for the given stored knob block — the patch-select restore path
// (owns the drive-pedal push too). A gate param of a disabled stage is
// parked at 0; every other param is pushed at its stored value. Callers
// hold applyMu.
func (c *Controls) restoreChain(k state.Knob) {
	for i := range chainStages {
		stg := &chainStages[i]
		on := knobEnable(k, stg.ID)
		for _, pm := range stg.Params {
			v := knobField(k, pm.StateKey)
			if pm.Gate && !on {
				v = 0
			}
			pm.set(c.audio, v)
		}
	}
}

// chainChangeData builds the "chain" sub-map folded into a "patch"
// change: per stage { enabled, <leaf>: storedValue }. Mirrors the synth
// block's shape so SSE clients decode one atomic patch switch. Values are
// the STORED knob values (what the UI shows), not the gated effective
// values pushed to the engine.
func chainChangeData(k state.Knob) map[string]any {
	out := make(map[string]any, len(chainStages))
	for i := range chainStages {
		stg := &chainStages[i]
		m := map[string]any{"enabled": knobEnable(k, stg.ID)}
		for _, pm := range stg.Params {
			m[chainParamLeaf(pm.ID)] = knobField(k, pm.StateKey)
		}
		out[stg.ID] = m
	}
	return out
}

// ChainParamView is one param's schema + current value, for GET
// /api/chain.
type ChainParamView struct {
	ID      string  `json:"id"`
	Label   string  `json:"label"`
	Unit    string  `json:"unit"`
	Min     float32 `json:"min"`
	Max     float32 `json:"max"`
	Default float32 `json:"default"`
	Step    float32 `json:"step"`
	Taper   string  `json:"taper"`
	Gate    bool    `json:"gate"`
	Value   float32 `json:"value"`
}

// ChainStageView is one stage's schema + enable state + params.
type ChainStageView struct {
	ID      string           `json:"id"`
	Label   string           `json:"label"`
	Kind    string           `json:"kind"`
	Enabled bool             `json:"enabled"`
	Params  []ChainParamView `json:"params"`
}

// ChainSnapshot is the GET /api/chain payload: the schema+values for
// every stage (in canonical order), the current patch name, and the
// global display order the UI applies.
type ChainSnapshot struct {
	Patch  string           `json:"patch"`
	Order  []string         `json:"order"`
	Stages []ChainStageView `json:"stages"`
}

// ChainSnapshot assembles the chain view from the registry (schema), the
// current patch's stored knobs+enables, and the global pedal order. With
// no patch selected, Patch is "" and values are the registry defaults.
// Read-only (no applyMu), mirroring Snapshot().
func (c *Controls) ChainSnapshot() ChainSnapshot {
	out := ChainSnapshot{Order: c.pedalOrder()}
	k := state.Defaults()
	if cur := c.reg.Current(); cur != nil {
		out.Patch = cur.Name
		k = c.st.PatchKnob(cur.Name)
	}
	out.Stages = make([]ChainStageView, 0, len(chainStages))
	for i := range chainStages {
		stg := &chainStages[i]
		sv := ChainStageView{
			ID:      stg.ID,
			Label:   stg.Label,
			Kind:    stg.Kind,
			Enabled: knobEnable(k, stg.ID),
			Params:  make([]ChainParamView, 0, len(stg.Params)),
		}
		for _, pm := range stg.Params {
			sv.Params = append(sv.Params, ChainParamView{
				ID:      pm.ID,
				Label:   pm.Label,
				Unit:    pm.Unit,
				Min:     pm.Min,
				Max:     pm.Max,
				Default: pm.Default,
				Step:    pm.Step,
				Taper:   pm.Taper.String(),
				Gate:    pm.Gate,
				Value:   knobField(k, pm.StateKey),
			})
		}
		out.Stages = append(out.Stages, sv)
	}
	return out
}

// pedalOrder returns the stored global FX order, or the canonical order
// (drive → … → reverb) when the user has never reordered or the persisted
// value is not a full permutation (a hand-edited/downgraded state.toml must
// not zero-fill into a valid-but-wrong permutation at the boot push).
func (c *Controls) pedalOrder() []string {
	if o := c.st.PedalOrder(); validFxOrder(o) {
		return o
	}
	return append([]string(nil), fxOrderStages...)
}
