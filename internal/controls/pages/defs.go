package pages

import (
	"fmt"
	"math"

	"github.com/mschulkind-oss/polyclav/internal/controls"
)

// Per-tick knob steps. Every slot names one so the feel is explicit and
// testable. A full sweep of a [0,1] parameter takes ~127 detents,
// matching the pre-pages hardcoded knobs 1–4; envelope times move 25 ms
// per detent (fast spins already benefit from the encoder's own
// acceleration — |delta| > 1 per event — but a value-dependent/log
// taper per docs/ROADMAP.md §2.1 is a future refinement); stepped
// parameters (bend range, voice mode) move one unit per detent.
const (
	stepUnit      float32 = 1.0 / 127 // [0,1] params: full sweep ≈ one rotation
	stepResonance float32 = 0.0075    // [0,0.95]: same ~127-detent sweep
	stepEnvTime   float32 = 0.025     // 25 ms per detent (0.1 ms..10 s range)
	stepGlide     float32 = 0.05      // 50 ms per detent over [0,5] s
	stepDetune    float32 = 1         // 1 cent per detent (±100 c)
	stepLFORate   float32 = 0.1       // 0.1 Hz per detent (0.05..20 Hz)
	stepLFOPitch  float32 = 1         // 1 cent per detent (0..100 c)
	stepLFOCutoff float32 = 0.02      // 1/50 octave per detent (0..2 oct)
	stepInteger   float32 = 1         // bend-range semitones / voice-mode cycling
)

// pageDefs is the knob-page table — docs/ROADMAP.md §2.1 adapted to the
// parameter set the controls layer actually ships. §2.1 predates the
// controls layer, per-patch auto-persistence, and the web UI; the
// deviations, each with its reason:
//
//   - Page set is MAIN / OSC / FILTER / AMP / LFO/MOD instead of §2.1's
//     MIX / FILTER / AMP / LFO / MOD. Page 1 (MAIN) keeps today's knobs
//     1–4 exactly as shipped (Volume / Reverb / Comp / Cutoff) so muscle
//     memory survives the upgrade — this also settles §2.1's open
//     question and §5's locked decision (the global volume/reverb/comp
//     stay reachable from inside the synth UI) by making them page 1
//     rather than a trailing sixth page.
//   - §2.1's MIX page content (osc levels/detunes, noise, drive) lands
//     on page 2 (OSC), plus the shipped shared Pulse Width; per-osc
//     waveform/octave selection has no knob shape (it's a selector, not
//     a sweep) and waits for the §2.4 per-page state pads.
//   - FILTER gains the shipped Kbd Track on knob 8 (§2.1 put keytrack on
//     knob 4; both fit — ordering here groups the ADSR contiguously on
//     knobs 4–7 mirroring the AMP page).
//   - AMP carries the amp ADSR plus the shipped velocity routing
//     (Vel>Amp / Vel>Cutoff — §2.1's single "Velocity sens" knob split
//     into the two amounts the engine actually has) and a Drive alias;
//     §2.1's Volume-on-knob-1 is already on MAIN knob 1.
//   - LFO/MOD merges §2.1's LFO and MOD pages: the engine ships one LFO
//     (rate + three depths, no sync/shape/smoothing knobs yet), no
//     mod-wheel routing, and glide/bend-range from MOD. Voice Mode
//     (mono_legato/mono_retrig/poly via controls.SetSynthVoiceMode)
//     rides knob 7 — §2.4/§5 wanted it on a pad, but the pad row now
//     carries page indicators.
//   - Detune range is the shipped ±100 cents (§2.1 said ±50 with
//     pad-switchable octaves); bend range is the shipped 0..12 st
//     (§2.1 said ±1..12).
//   - §2.5's Record "save patch" knob-arm is OBSOLETE: the controls
//     layer persists every synth edit to state.toml automatically
//     (debounced), so there is nothing to arm — see the transport table
//     in cmd/polyclav.
//
// Non-native patches: only page 0 is live, and on it only the slots
// that route through the always-available knob setters (Volume, Reverb,
// Comp — slots 1–3). The rest return ok=false from their controls
// setters (ErrNoNativePatch / the AdjustCutoff gate) and show nothing,
// which is exactly the pre-pages knob-4 behavior.
func pageDefs() []PageDef {
	return []PageDef{
		{
			Name: "MAIN",
			Slots: [8]Slot{
				{Label: "Volume", Step: stepUnit, Adjust: adjVolume},
				{Label: "Reverb", Step: stepUnit, Adjust: adjReverb},
				{Label: "Comp", Step: stepUnit, Adjust: adjCompressor},
				{Label: "Cutoff", Step: stepUnit, Adjust: adjCutoff},
				{Label: "Resonance", Step: stepResonance, Adjust: adjResonance()},
				{Label: "Glide", Step: stepGlide, Adjust: adjGlide()},
				{Label: "Drive", Step: stepUnit, Adjust: adjDrive()},
				{}, // unbound
			},
		},
		{
			Name: "OSC",
			Slots: [8]Slot{
				{Label: "Osc1 Level", Step: stepUnit, Adjust: adjOscLevel(0)},
				{Label: "Osc1 Detune", Step: stepDetune, Adjust: adjOscDetune(0)},
				{Label: "Osc2 Level", Step: stepUnit, Adjust: adjOscLevel(1)},
				{Label: "Osc2 Detune", Step: stepDetune, Adjust: adjOscDetune(1)},
				{Label: "Osc3 Level", Step: stepUnit, Adjust: adjOscLevel(2)},
				{Label: "Osc3 Detune", Step: stepDetune, Adjust: adjOscDetune(2)},
				{Label: "Noise", Step: stepUnit, Adjust: adjNoise()},
				{Label: "Pulse Width", Step: stepUnit, Adjust: adjPulseWidth()},
			},
		},
		{
			Name: "FILTER",
			Slots: [8]Slot{
				{Label: "Cutoff", Step: stepUnit, Adjust: adjCutoff},
				{Label: "Resonance", Step: stepResonance, Adjust: adjResonance()},
				{Label: "Env Amount", Step: stepUnit, Adjust: adjFilterEnv(
					func(fe controls.FilterEnv) float32 { return fe.Amount },
					func(fe controls.FilterEnv, v float32) controls.FilterEnv { fe.Amount = v; return fe },
					formatPercent)},
				{Label: "F.Attack", Step: stepEnvTime, Adjust: adjFilterEnv(
					func(fe controls.FilterEnv) float32 { return fe.Attack },
					func(fe controls.FilterEnv, v float32) controls.FilterEnv { fe.Attack = v; return fe },
					formatSeconds)},
				{Label: "F.Decay", Step: stepEnvTime, Adjust: adjFilterEnv(
					func(fe controls.FilterEnv) float32 { return fe.Decay },
					func(fe controls.FilterEnv, v float32) controls.FilterEnv { fe.Decay = v; return fe },
					formatSeconds)},
				{Label: "F.Sustain", Step: stepUnit, Adjust: adjFilterEnv(
					func(fe controls.FilterEnv) float32 { return fe.Sustain },
					func(fe controls.FilterEnv, v float32) controls.FilterEnv { fe.Sustain = v; return fe },
					formatPercent)},
				{Label: "F.Release", Step: stepEnvTime, Adjust: adjFilterEnv(
					func(fe controls.FilterEnv) float32 { return fe.Release },
					func(fe controls.FilterEnv, v float32) controls.FilterEnv { fe.Release = v; return fe },
					formatSeconds)},
				{Label: "Kbd Track", Step: stepUnit, Adjust: adjKbdTrack()},
			},
		},
		{
			Name: "AMP",
			Slots: [8]Slot{
				{Label: "A.Attack", Step: stepEnvTime, Adjust: adjAmpEnv(
					func(ae controls.AmpEnv) float32 { return ae.Attack },
					func(ae controls.AmpEnv, v float32) controls.AmpEnv { ae.Attack = v; return ae },
					formatSeconds)},
				{Label: "A.Decay", Step: stepEnvTime, Adjust: adjAmpEnv(
					func(ae controls.AmpEnv) float32 { return ae.Decay },
					func(ae controls.AmpEnv, v float32) controls.AmpEnv { ae.Decay = v; return ae },
					formatSeconds)},
				{Label: "A.Sustain", Step: stepUnit, Adjust: adjAmpEnv(
					func(ae controls.AmpEnv) float32 { return ae.Sustain },
					func(ae controls.AmpEnv, v float32) controls.AmpEnv { ae.Sustain = v; return ae },
					formatPercent)},
				{Label: "A.Release", Step: stepEnvTime, Adjust: adjAmpEnv(
					func(ae controls.AmpEnv) float32 { return ae.Release },
					func(ae controls.AmpEnv, v float32) controls.AmpEnv { ae.Release = v; return ae },
					formatSeconds)},
				{Label: "Vel>Amp", Step: stepUnit, Adjust: adjVelRouting(
					func(vr controls.VelRouting) float32 { return vr.ToAmp },
					func(vr controls.VelRouting, v float32) controls.VelRouting { vr.ToAmp = v; return vr },
					formatPercent)},
				{Label: "Vel>Cutoff", Step: stepUnit, Adjust: adjVelRouting(
					func(vr controls.VelRouting) float32 { return vr.ToCutoff },
					func(vr controls.VelRouting, v float32) controls.VelRouting { vr.ToCutoff = v; return vr },
					formatPercent)},
				{Label: "Drive", Step: stepUnit, Adjust: adjDrive()},
				{}, // unbound
			},
		},
		{
			Name: "LFO/MOD",
			Slots: [8]Slot{
				{Label: "LFO Rate", Step: stepLFORate, Adjust: adjLFO(
					func(l controls.LFO) float32 { return l.RateHz },
					func(l controls.LFO, v float32) controls.LFO { l.RateHz = v; return l },
					formatRateHz)},
				{Label: "LFO>Pitch", Step: stepLFOPitch, Adjust: adjLFO(
					func(l controls.LFO) float32 { return l.ToPitchCents },
					func(l controls.LFO, v float32) controls.LFO { l.ToPitchCents = v; return l },
					formatCents)},
				{Label: "LFO>Cutoff", Step: stepLFOCutoff, Adjust: adjLFO(
					func(l controls.LFO) float32 { return l.ToCutoffOct },
					func(l controls.LFO, v float32) controls.LFO { l.ToCutoffOct = v; return l },
					formatOctaves)},
				{Label: "LFO>Amp", Step: stepUnit, Adjust: adjLFO(
					func(l controls.LFO) float32 { return l.ToAmp },
					func(l controls.LFO, v float32) controls.LFO { l.ToAmp = v; return l },
					formatPercent)},
				{Label: "Bend Range", Step: stepInteger, Adjust: adjBendRange},
				{Label: "Glide", Step: stepGlide, Adjust: adjGlide()},
				{Label: "Voice Mode", Step: stepInteger, Adjust: adjVoiceMode},
				{}, // unbound
			},
		},
	}
}

// adjust builds the shared AdjustFunc shape over Controls.AdjustSynth,
// the atomic read-modify-write primitive: INSIDE the writer lock, pick
// the block T off the live snapshot, nudge one field by the step-scaled
// delta, and write it back; the changed section then runs the standard
// clamp/apply/cache/persist/publish sequence. Reading the current value
// inside the critical section (rather than pre-reading Synth() and
// pushing a whole block through an absolute setter) means a concurrent
// web PATCH can never have a sibling-field edit silently reverted by a
// knob tick's stale snapshot. The display shows the field as actually
// applied (post-clamp).
func adjust[T any](
	sel func(*controls.SynthSnapshot) *T,
	get func(T) float32,
	with func(T, float32) T,
	format func(float32) string,
) AdjustFunc {
	return func(ctl *controls.Controls, delta float32) (string, bool) {
		applied, err := ctl.AdjustSynth(func(s *controls.SynthSnapshot) {
			blk := sel(s)
			*blk = with(*blk, get(*blk)+delta)
		})
		if err != nil {
			return "", false
		}
		return format(get(*sel(&applied))), true
	}
}

// adjustScalar is adjust for single-float parameters.
func adjustScalar(
	sel func(*controls.SynthSnapshot) *float32,
	format func(float32) string,
) AdjustFunc {
	return adjust(
		sel,
		func(v float32) float32 { return v },
		func(_, v float32) float32 { return v },
		format,
	)
}

// The knob 1–3 globals route through the existing relative adjusters
// (they read current from the state store, so deltas compose with web
// edits); ok=false means no patch is selected.

func adjVolume(ctl *controls.Controls, delta float32) (string, bool) {
	v, ok := ctl.AdjustVolume(delta)
	if !ok {
		return "", false
	}
	return formatPercent(v), true
}

func adjReverb(ctl *controls.Controls, delta float32) (string, bool) {
	v, ok := ctl.AdjustReverb(delta)
	if !ok {
		return "", false
	}
	return formatPercent(v), true
}

func adjCompressor(ctl *controls.Controls, delta float32) (string, bool) {
	v, ok := ctl.AdjustCompressor(delta)
	if !ok {
		return "", false
	}
	return formatPercent(v), true
}

// adjCutoff keeps the shipped knob-4 semantics: the 0..1 knob position
// lives in controls (log-taper to Hz there); ok=false off native patches.
func adjCutoff(ctl *controls.Controls, delta float32) (string, bool) {
	hz, ok := ctl.AdjustCutoff(delta)
	if !ok {
		return "", false
	}
	return formatHz(hz), true
}

func adjResonance() AdjustFunc {
	return adjustScalar(
		func(s *controls.SynthSnapshot) *float32 { return &s.Resonance },
		formatPercent)
}

func adjGlide() AdjustFunc {
	return adjustScalar(
		func(s *controls.SynthSnapshot) *float32 { return &s.Glide },
		formatSeconds)
}

func adjDrive() AdjustFunc {
	return adjustScalar(
		func(s *controls.SynthSnapshot) *float32 { return &s.Drive },
		formatPercent)
}

func adjNoise() AdjustFunc {
	return adjustScalar(
		func(s *controls.SynthSnapshot) *float32 { return &s.Noise },
		formatPercent)
}

func adjPulseWidth() AdjustFunc {
	return adjustScalar(
		func(s *controls.SynthSnapshot) *float32 { return &s.PulseWidth },
		formatPercent)
}

func adjKbdTrack() AdjustFunc {
	return adjustScalar(
		func(s *controls.SynthSnapshot) *float32 { return &s.KbdTrack },
		formatPercent)
}

func adjOscLevel(idx int) AdjustFunc {
	return adjust(
		func(s *controls.SynthSnapshot) *controls.OscParams { return &s.Oscs[idx] },
		func(o controls.OscParams) float32 { return o.Level },
		func(o controls.OscParams, v float32) controls.OscParams { o.Level = v; return o },
		formatPercent)
}

func adjOscDetune(idx int) AdjustFunc {
	return adjust(
		func(s *controls.SynthSnapshot) *controls.OscParams { return &s.Oscs[idx] },
		func(o controls.OscParams) float32 { return o.DetuneCents },
		func(o controls.OscParams, v float32) controls.OscParams { o.DetuneCents = v; return o },
		formatSignedCents)
}

func adjFilterEnv(
	get func(controls.FilterEnv) float32,
	with func(controls.FilterEnv, float32) controls.FilterEnv,
	format func(float32) string,
) AdjustFunc {
	return adjust(
		func(s *controls.SynthSnapshot) *controls.FilterEnv { return &s.FilterEnv },
		get, with, format)
}

func adjAmpEnv(
	get func(controls.AmpEnv) float32,
	with func(controls.AmpEnv, float32) controls.AmpEnv,
	format func(float32) string,
) AdjustFunc {
	return adjust(
		func(s *controls.SynthSnapshot) *controls.AmpEnv { return &s.AmpEnv },
		get, with, format)
}

func adjVelRouting(
	get func(controls.VelRouting) float32,
	with func(controls.VelRouting, float32) controls.VelRouting,
	format func(float32) string,
) AdjustFunc {
	return adjust(
		func(s *controls.SynthSnapshot) *controls.VelRouting { return &s.VelRouting },
		get, with, format)
}

func adjLFO(
	get func(controls.LFO) float32,
	with func(controls.LFO, float32) controls.LFO,
	format func(float32) string,
) AdjustFunc {
	return adjust(
		func(s *controls.SynthSnapshot) *controls.LFO { return &s.LFO },
		get, with, format)
}

// adjBendRange moves in whole semitones: the current value is rounded
// onto the integer grid first so a fractional web-set value snaps into
// step on the first detent.
func adjBendRange(ctl *controls.Controls, delta float32) (string, bool) {
	applied, err := ctl.AdjustSynth(func(s *controls.SynthSnapshot) {
		s.BendRange = float32(math.Round(float64(s.BendRange))) + delta
	})
	if err != nil {
		return "", false
	}
	return formatSemitones(applied.BendRange), true
}

// voiceModes is the cycle order for the Voice Mode slot — the engine's
// three allocation modes (controls.SetSynthVoiceMode's vocabulary).
var voiceModes = [...]string{"mono_legato", "mono_retrig", "poly"}

func voiceModeLabel(mode string) string {
	switch mode {
	case "mono_legato":
		return "Mono Legato"
	case "mono_retrig":
		return "Mono Retrig"
	case "poly":
		return "Poly"
	}
	return mode
}

// adjVoiceMode cycles mono_legato → mono_retrig → poly (wrapping both
// directions). One detent = one step regardless of encoder acceleration
// (only delta's sign is used): skipping states on a 3-way selector
// would make it unlandable at speed.
func adjVoiceMode(ctl *controls.Controls, delta float32) (string, bool) {
	if delta == 0 {
		return "", false
	}
	applied, err := ctl.AdjustSynth(func(s *controls.SynthSnapshot) {
		i := 0
		for j, m := range voiceModes {
			if m == s.VoiceMode {
				i = j
				break
			}
		}
		n := len(voiceModes)
		if delta > 0 {
			i = (i + 1) % n
		} else {
			i = (i + n - 1) % n
		}
		s.VoiceMode = voiceModes[i]
	})
	if err != nil {
		return "", false
	}
	return voiceModeLabel(applied.VoiceMode), true
}

// Screen value formatters. The display is 16 ASCII chars per line
// (driver.SetDisplayText truncates and space-coerces beyond that), so
// everything below stays short and 7-bit clean.

func formatPercent(v float32) string {
	return fmt.Sprintf("%d%%", int(v*100+0.5))
}

// formatHz matches the pre-pages cutoff rendering: kHz with one decimal
// above 1 kHz, integer Hz below.
func formatHz(hz float32) string {
	if hz >= 1000.0 {
		return fmt.Sprintf("%.1f kHz", hz/1000.0)
	}
	return fmt.Sprintf("%d Hz", int(hz+0.5))
}

func formatSeconds(s float32) string {
	if s < 1.0 {
		return fmt.Sprintf("%d ms", int(s*1000+0.5))
	}
	return fmt.Sprintf("%.2f s", s)
}

// formatSignedCents renders bipolar detune ("+7 c" / "-7 c").
func formatSignedCents(v float32) string {
	return fmt.Sprintf("%+d c", int(math.Round(float64(v))))
}

// formatCents renders the unipolar LFO pitch depth ("7 c").
func formatCents(v float32) string {
	return fmt.Sprintf("%d c", int(v+0.5))
}

func formatOctaves(v float32) string {
	return fmt.Sprintf("%.2f oct", v)
}

func formatRateHz(v float32) string {
	return fmt.Sprintf("%.1f Hz", v)
}

func formatSemitones(v float32) string {
	return fmt.Sprintf("%.0f st", v)
}
