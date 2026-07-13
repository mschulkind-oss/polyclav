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
// per-patch gain matching.
func MeasurePatch(label string, p patches.Patch, events []audio.OfflineMIDIEvent, nFrames int) (Measurement, error) {
	typ, ref, pluginID := patchRef(p)
	if ref == "" {
		return Measurement{}, fmt.Errorf("measure patch %q: no %s reference configured", p.Name, typ)
	}

	samples, err := audio.RenderOfflineEvents(typ, ref, pluginID, events, nFrames)
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
