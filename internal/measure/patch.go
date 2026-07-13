package measure

import (
	"fmt"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/patches"
)

// Measurement is one point in a comparison or sweep: a label (a patch
// name, a knob position, whatever the caller is varying) plus the
// loudness and peak measured for it. Deliberately decoupled from how
// the samples were produced — checks.go operates only on this type, so
// any future renderer (a knob sweep, a hosted plugin, ...) can reuse
// the same checks by producing a []Measurement.
type Measurement struct {
	Label    string
	LUFS     float32
	PeakDBFS float32
}

// patchRef maps a patches.Patch to the (patchType, patchRef, pluginID)
// triple RenderOfflineEvents needs. Mirrors the Type-defaulting already
// used at the web layer (internal/web/server.go's patchesView): an
// empty Type means "soundfont".
func patchRef(p patches.Patch) (patchType, ref, pluginID string) {
	typ := p.Type
	if typ == "" {
		typ = "soundfont"
	}
	switch typ {
	case "native":
		return typ, p.Engine, ""
	case "lv2":
		return typ, p.PluginURI, ""
	case "clap":
		return typ, p.PluginPath, p.PluginID
	default:
		return "soundfont", p.Soundfont, ""
	}
}

// MeasurePatch renders `events` through patch `p` offline (no audio
// device) and returns its loudness/peak as a Measurement labeled
// `label`. p.GainDB is folded in additively rather than re-rendered at
// a different gain: LUFS and peak-dBFS are both log-power measures, so
// a linear gain trim of `db` decibels shifts either by exactly `db` —
// `20*log10(10^(db/20)) == db` — no re-render needed to account for
// per-patch gain matching. Equivalent to MeasurePatchWithChainParams
// with every chain param left at its engine default.
func MeasurePatch(label string, p patches.Patch, events []audio.OfflineMIDIEvent, nFrames int) (Measurement, error) {
	return MeasurePatchWithChainParams(label, p, nil, events, nFrames)
}

// MeasurePatchWithChainParams is MeasurePatch with additional
// backend-agnostic chain-effect overrides (drive pedal, analog delay,
// ...) applied before rendering — the general form behind sweep-based
// checks ("does the drive pedal's amount knob change loudness
// gradually", "does the delay's feedback stay bounded"), not just
// patch-to-patch comparison. cp may be nil (same as MeasurePatch).
func MeasurePatchWithChainParams(label string, p patches.Patch, cp *audio.ChainParams, events []audio.OfflineMIDIEvent, nFrames int) (Measurement, error) {
	typ, ref, pluginID := patchRef(p)
	if ref == "" {
		return Measurement{}, fmt.Errorf("measure patch %q: no %s reference configured", p.Name, typ)
	}

	samples, err := audio.RenderOfflineEvents(typ, ref, pluginID, cp, events, nFrames)
	if err != nil {
		return Measurement{}, fmt.Errorf("measure patch %q: %w", p.Name, err)
	}

	return Measurement{
		Label:    label,
		LUFS:     audio.MeasureLUFS(samples) + p.GainDB,
		PeakDBFS: audio.MeasurePeakDBFS(samples) + p.GainDB,
	}, nil
}

// MeasurePatches renders `events` through every patch in `ps` and
// returns their measurements in the same order. Stops at the first
// error — a patch that fails to load (missing soundfont file, unknown
// engine, ...) means the comparison can't proceed meaningfully, so
// this doesn't silently skip and report a partial picture.
func MeasurePatches(ps []patches.Patch, events []audio.OfflineMIDIEvent, nFrames int) ([]Measurement, error) {
	out := make([]Measurement, 0, len(ps))
	for _, p := range ps {
		m, err := MeasurePatch(p.Name, p, events, nFrames)
		if err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	return out, nil
}

// SweepChainParam renders the same patch/events once per value in
// `values`, applying `setValue` to a fresh audio.NewChainParams() each
// time (so only the field setValue touches is overridden — every other
// chain param stays at its engine default), and returns one
// Measurement per value, labeled with the value itself. Pairs directly
// with CheckGradual/CheckBounded/CheckConsistent to verify a
// chain-level effect's own knob — not just which patch renders —
// behaves smoothly across its whole range. This is the "same framework,
// more checks" extension point: any future chain-level effect just
// needs a `setValue` closure, no new rendering code.
func SweepChainParam(
	p patches.Patch,
	events []audio.OfflineMIDIEvent,
	nFrames int,
	values []float32,
	setValue func(*audio.ChainParams, float32),
) ([]Measurement, error) {
	out := make([]Measurement, 0, len(values))
	for _, v := range values {
		cp := audio.NewChainParams()
		setValue(&cp, v)
		label := fmt.Sprintf("%.4g", v)
		m, err := MeasurePatchWithChainParams(label, p, &cp, events, nFrames)
		if err != nil {
			return nil, fmt.Errorf("sweep chain param at value %v: %w", v, err)
		}
		out = append(out, m)
	}
	return out, nil
}
