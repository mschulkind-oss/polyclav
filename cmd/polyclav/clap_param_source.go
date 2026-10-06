package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/clapcache"
	"github.com/mschulkind-oss/polyclav/internal/controls"
	"github.com/mschulkind-oss/polyclav/internal/controls/pages"
)

// clapParamSource exposes the active CLAP instance's parameters to the
// generic encoder browser (see pages.ParamSource). It is the plugin-side
// half of "browse whatever the instrument exposes": the host-owned
// MAIN page is the curated layer, and everything the plugin publishes
// follows it, 8 encoders at a time.
//
// It reads clapcache live, so values track plugin feedback, and writes
// through the same setter the organ fader router uses. Param identity is
// the CLAP id, never the name or enumeration position — Drive/Tone/Output
// Level repeat across modules in the Potato Keys example, which is exactly
// why the browser shows a disambiguating module prefix.
type clapParamSource struct {
	cache  *clapcache.Cache
	setter clapParamSetter
}

var _ pages.ParamSource = clapParamSource{}

// skipClapParam filters controls a player must not drive from the browser:
// read-only and hidden parameters are not encoders.
func skipClapParam(p audio.ClapParamInfo) bool {
	return p.Flags&audio.ClapParamIsReadonly != 0 || p.Flags&audio.ClapParamIsHidden != 0
}

func (s clapParamSource) Params() []pages.Param {
	if s.cache == nil || s.setter == nil {
		return nil
	}
	all := s.cache.All()
	out := make([]pages.Param, 0, len(all))
	for _, p := range all {
		if skipClapParam(p) {
			continue
		}
		id := p.ClapID
		out = append(out, pages.Param{
			Label: clapParamLabel(p),
			Step:  clapParamStep(p),
			Adjust: func(_ *controls.Controls, delta float32) (string, bool) {
				cur, ok := s.cache.Get(id)
				if !ok {
					return "", false
				}
				v := clampParamValue(cur.CurrentValue+float64(delta), cur.MinValue, cur.MaxValue)
				if err := s.setter.SetClapParam(id, v); err != nil {
					return "", false
				}
				s.cache.Update(id, v)
				return formatParamValue(v, cur.MinValue, cur.MaxValue), true
			},
		})
	}
	return out
}

// clapParamLabel is the screen name: "Module Name" when the plugin groups
// the parameter, otherwise just the name, capped to the 16-byte display.
func clapParamLabel(p audio.ClapParamInfo) string {
	name := strings.TrimSpace(p.Name)
	if name == "" {
		name = fmt.Sprintf("Param %d", p.ClapID)
	}
	if module := strings.TrimSpace(p.Module); module != "" && !strings.EqualFold(module, name) {
		name = module + " " + name
	}
	return truncateLabel(name, 16)
}

// clapParamStep is the per-detent delta in the parameter's own units. A
// stepped/enum parameter (or a small integer range the plugin did not
// flag) moves one unit; everything else sweeps its range in roughly one
// encoder rotation, matching the curated pages' ~127-detent feel.
func clapParamStep(p audio.ClapParamInfo) float32 {
	span := p.MaxValue - p.MinValue
	if span <= 0 || math.IsNaN(span) || math.IsInf(span, 0) {
		return 0
	}
	if p.Flags&(audio.ClapParamIsStepped|audio.ClapParamIsEnum) != 0 || span <= 16 {
		return 1
	}
	return float32(span / 127)
}

// clampParamValue clamps to a finite range; an unknown range passes the
// value through rather than inventing bounds.
func clampParamValue(v, min, max float64) float64 {
	if math.IsNaN(min) || math.IsNaN(max) || min > max {
		return v
	}
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

// truncateLabel caps a short display label at n bytes. Non-ASCII runes are
// made safe for the byte-oriented screen by never splitting one: a rune
// that would straddle the limit stops the label.
func truncateLabel(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := 0
	for i := range s {
		if i > n {
			break
		}
		cut = i
	}
	if cut == 0 {
		return ""
	}
	return s[:cut]
}
