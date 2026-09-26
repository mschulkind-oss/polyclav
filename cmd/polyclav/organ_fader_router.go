package main

import (
	"fmt"
	"math"
	"strings"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/clapcache"
	"github.com/mschulkind-oss/polyclav/internal/launchkey/driver"
	"github.com/mschulkind-oss/polyclav/internal/midi"
	"github.com/mschulkind-oss/polyclav/internal/patches"
)

type organRegistry interface {
	Current() *patches.Patch
	Active() *patches.Patch
	Status() patches.LoadStatus
}

type clapParamSetter interface {
	SetClapParam(clapID uint32, value float64) error
}

type realClapParamSetter struct{}

func (realClapParamSetter) SetClapParam(clapID uint32, value float64) error {
	return audio.SetClapParam(clapID, value)
}

type dawFaderMapper interface{ Dispatch(midi.Event) }

type organScreen interface{ Show(line1, line2 string) }

type organScreenFunc func(line1, line2 string)

func (f organScreenFunc) Show(line1, line2 string) { f(line1, line2) }

type organFaderRouter struct {
	registry organRegistry
	cache    *clapcache.Cache
	setter   clapParamSetter
	mapper   dawFaderMapper
	screen   organScreen
}

func (r *organFaderRouter) HandleFader(e driver.FaderEvent) {
	if e.Index < 1 || e.Index > 9 {
		return
	}
	if r.routeOrganFader(e) {
		return
	}
	if r.mapper != nil {
		r.mapper.Dispatch(midi.Event{Kind: midi.ControlChange, Channel: 15, CC: byte(4 + e.Index), Value: e.Value})
	}
}

func (r *organFaderRouter) routeOrganFader(e driver.FaderEvent) bool {
	cur, active, status := r.registry.Current(), r.registry.Active(), r.registry.Status()
	if cur == nil || active == nil || cur.Name != active.Name || status.State != patches.LoadStateActive {
		return false
	}
	if patchType(cur.Type) != "clap" || !cur.LaunchkeyOrgan.Enabled || cur.LaunchkeyOrgan.Ownership != "organ" {
		return false
	}
	ids := cur.LaunchkeyOrgan.DrawbarClapIDs
	if len(ids) != 9 {
		r.show("ORGAN", "UNRESOLVED")
		return true
	}
	for i, id := range ids {
		param, ok := r.cache.Get(id)
		if !ok {
			r.show("ORGAN", fmt.Sprintf("MISSING %d", id))
			return true
		}
		if !looksLikeDrawbarParam(i+1, param) {
			r.show("ORGAN", fmt.Sprintf("CHECK %d", id))
			return true
		}
	}
	param, _ := r.cache.Get(ids[e.Index-1])
	value := scaleMIDI(e.Value, param.MinValue, param.MaxValue)
	if r.setter != nil {
		if err := r.setter.SetClapParam(param.ClapID, value); err != nil {
			r.show(drawbarLabel(e.Index), "SET FAILED")
			return true
		}
	}
	r.cache.Update(param.ClapID, value)
	r.show(drawbarLabel(e.Index), formatParamValue(value, param.MinValue, param.MaxValue))
	return true
}

func patchType(typ string) string {
	if typ == "" {
		return "soundfont"
	}
	return typ
}

func scaleMIDI(v byte, min, max float64) float64 {
	return min + (max-min)*(float64(v)/127.0)
}

func drawbarLabel(index int) string {
	labels := drawbarLabels()
	if index < 1 || index > len(labels) {
		return "drawbar"
	}
	return labels[index-1]
}

func drawbarLabels() []string {
	return []string{"16′ drawbar", "5⅓′ drawbar", "8′ drawbar", "4′ drawbar", "2⅔′ drawbar", "2′ drawbar", "1⅗′ drawbar", "1⅓′ drawbar", "1′ drawbar"}
}

func looksLikeDrawbarParam(index int, p audio.ClapParamInfo) bool {
	if p.MinValue > 0.0001 || p.MaxValue < 7.999 {
		return false
	}
	// Potato Keys labels its drawbars by footage alone (for example "16′").
	// Accept those exact labels without mistaking another numbered control for
	// a drawbar; other plugins can use explicitly named drawbar parameters.
	if strings.TrimSpace(p.Name) == strings.TrimSuffix(drawbarLabel(index), " drawbar") {
		return true
	}
	name := strings.ToLower(p.Name + " " + p.Module)
	return strings.Contains(name, "drawbar") && drawbarNameMatchesPosition(index, name)
}

func drawbarNameMatchesPosition(index int, name string) bool {
	switch index {
	case 1:
		return strings.Contains(name, "16")
	case 2:
		return strings.Contains(name, "5") && containsAny(name, "1/3", "⅓", "1_3")
	case 3:
		return strings.Contains(name, "8")
	case 4:
		return strings.Contains(name, "4")
	case 5:
		return strings.Contains(name, "2") && containsAny(name, "2/3", "⅔", "2_3")
	case 6:
		return strings.Contains(name, "2") && !containsAny(name, "2/3", "⅔", "2_3")
	case 7:
		return strings.Contains(name, "1") && containsAny(name, "3/5", "⅗", "3_5")
	case 8:
		return strings.Contains(name, "1") && containsAny(name, "1/3", "⅓", "1_3")
	case 9:
		return strings.Contains(name, "1") && !containsAny(name, "1/3", "⅓", "1_3", "3/5", "⅗", "3_5")
	default:
		return false
	}
}

func containsAny(s string, needles ...string) bool {
	for _, needle := range needles {
		if strings.Contains(s, needle) {
			return true
		}
	}
	return false
}

func formatParamValue(v, min, max float64) string {
	if min == 0 && max == 1 {
		return fmt.Sprintf("%.0f%%", math.Round(v*100))
	}
	if min == 0 && max == 8 {
		return fmt.Sprintf("%.0f", math.Round(v))
	}
	return strings.TrimRight(strings.TrimRight(fmt.Sprintf("%.3f", v), "0"), ".")
}

func (r *organFaderRouter) show(line1, line2 string) {
	if r.screen != nil {
		r.screen.Show(line1, line2)
	}
}
