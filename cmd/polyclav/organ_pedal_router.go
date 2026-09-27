package main

import (
	"math"
	"strings"
	"sync"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/clapcache"
	"github.com/mschulkind-oss/polyclav/internal/config"
	"github.com/mschulkind-oss/polyclav/internal/midi"
	"github.com/mschulkind-oss/polyclav/internal/patches"
)

// organPedalRouter handles dedicated swell input and Launchkey sustain
// without turning either into a global volume control. It is called before
// synth and mixer dispatch; true means the input has been consumed.
type organPedalRouter struct {
	registry   organRegistry
	cache      *clapcache.Cache
	setter     clapParamSetter
	expression config.OrganExpressionConfig
	mu         sync.Mutex
	held       bool // debounces repeated CC64 presses in toggle mode
	heldPatch  string
}

func (r *organPedalRouter) Handle(ev midi.Event) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.expression.Device != "" && ev.SourcePort != "" && strings.Contains(strings.ToLower(ev.SourcePort), strings.ToLower(r.expression.Device)) {
		// A dedicated pedal port is not a keyboard. Never pass its other
		// messages to a piano or to OSC mixer bindings.
		if ev.Kind == midi.ControlChange && int(ev.CC) == r.expression.CC {
			if r.activeSwell() && r.expression.Min != r.expression.Max {
				v := float64(int(ev.Value)-r.expression.Min) / float64(r.expression.Max-r.expression.Min)
				r.setNamedParam("Expression", 0, 100, math.Max(0, math.Min(1, v))*100)
			}
		}
		return true
	}
	if ev.Kind != midi.ControlChange || ev.CC != 64 || ev.SourcePort == "" || !strings.Contains(strings.ToLower(ev.SourcePort), "launchkey") || strings.Contains(strings.ToLower(ev.SourcePort), "daw") {
		return false
	}
	if !r.activeOrgan() {
		r.held = false
		r.heldPatch = ""
		return false
	}
	p := r.registry.Current()
	mode := p.LaunchkeyOrgan.LeslieMode
	if mode == "off" || mode == "" {
		r.held = false
		r.heldPatch = ""
		return false
	}
	param, ok := r.namedParam("Rotary", 0, 3)
	if !ok {
		// An organ without the expected control keeps its ordinary sustain.
		r.held = false
		r.heldPatch = ""
		return false
	}
	if r.heldPatch != p.Name {
		r.held = false
		r.heldPatch = p.Name
	}
	pressed := ev.Value >= 64
	if mode == "momentary" {
		// A compatible Rotary parameter uses Direct=0, Stop=1, Slow=2, Fast=3.
		value := 2.0
		if pressed {
			value = 3
		}
		r.setParam(param, value)
	} else if pressed && !r.held {
		next := 3.0
		if param.CurrentValue == 3 {
			next = 2
		}
		r.setParam(param, next)
	}
	r.held = pressed
	return true
}

func (r *organPedalRouter) activePatch() *patches.Patch {
	if r.registry != nil {
		cur, active, st := r.registry.Current(), r.registry.Active(), r.registry.Status()
		if cur != nil && active != nil && cur.Name == active.Name && st.State == patches.LoadStateActive && patchType(cur.Type) == "clap" {
			return cur
		}
	}
	return nil
}

func (r *organPedalRouter) activeOrgan() bool {
	p := r.activePatch()
	return p != nil && p.LaunchkeyOrgan.Enabled && p.LaunchkeyOrgan.Ownership == "organ"
}

func (r *organPedalRouter) activeSwell() bool {
	p := r.activePatch()
	return p != nil && p.SwellPedal
}

func (r *organPedalRouter) namedParam(name string, min, max float64) (audio.ClapParamInfo, bool) {
	if r.cache == nil {
		return audio.ClapParamInfo{}, false
	}
	var found audio.ClapParamInfo
	count := 0
	for _, p := range r.cache.All() {
		if p.Name == name {
			if p.MinValue != min || p.MaxValue != max {
				return audio.ClapParamInfo{}, false
			}
			found = p
			count++
		}
	}
	return found, count == 1
}

func (r *organPedalRouter) setNamedParam(name string, min, max, value float64) {
	if p, ok := r.namedParam(name, min, max); ok {
		r.setParam(p, value)
	}
}

func (r *organPedalRouter) setParam(p audio.ClapParamInfo, value float64) {
	if r.setter == nil {
		return
	}
	if err := r.setter.SetClapParam(p.ClapID, value); err == nil {
		r.cache.Update(p.ClapID, value)
	}
}
