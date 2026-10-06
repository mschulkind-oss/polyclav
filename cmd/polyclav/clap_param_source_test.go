package main

import (
	"math"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/clapcache"
)

func clapParamSourceFor(params ...audio.ClapParamInfo) (clapParamSource, *clapcache.Cache, *fakeSetter) {
	cache := clapcache.New()
	cache.Replace(params)
	setter := &fakeSetter{}
	return clapParamSource{cache: cache, setter: setter}, cache, setter
}

func TestClapParamSourceSkipsReadonlyAndHidden(t *testing.T) {
	src, _, _ := clapParamSourceFor(
		audio.ClapParamInfo{ClapID: 1, Name: "Cutoff", MinValue: 0, MaxValue: 1},
		audio.ClapParamInfo{ClapID: 2, Name: "Meter", MinValue: 0, MaxValue: 1, Flags: audio.ClapParamIsReadonly},
		audio.ClapParamInfo{ClapID: 3, Name: "Secret", MinValue: 0, MaxValue: 1, Flags: audio.ClapParamIsHidden},
	)
	got := src.Params()
	if len(got) != 1 || got[0].Label != "Cutoff" {
		t.Fatalf("Params = %+v, want just Cutoff", got)
	}
}

func TestClapParamSourceAdjustWritesThroughSetterAndCache(t *testing.T) {
	src, cache, setter := clapParamSourceFor(
		audio.ClapParamInfo{ClapID: 7, Name: "16 drawbar", MinValue: 0, MaxValue: 8,
			CurrentValue: 4, Flags: audio.ClapParamIsStepped},
	)
	params := src.Params()
	if len(params) != 1 {
		t.Fatalf("Params = %d, want 1", len(params))
	}
	if params[0].Step != 1 {
		t.Fatalf("step = %v, want 1 for a stepped 0..8 parameter", params[0].Step)
	}

	display, ok := params[0].Adjust(nil, 1)
	if !ok {
		t.Fatal("Adjust ok=false, want true")
	}
	if !setter.called || setter.id != 7 || setter.value != 5 {
		t.Fatalf("setter = %+v, want id 7 value 5", setter)
	}
	if display != "5" {
		t.Errorf("display = %q, want %q", display, "5")
	}
	if p, _ := cache.Get(7); p.CurrentValue != 5 {
		t.Errorf("cache value = %v, want 5", p.CurrentValue)
	}

	// A further tick past the maximum clamps to the range, not beyond it.
	if _, ok := params[0].Adjust(nil, 10); !ok {
		t.Fatal("Adjust at the top ok=false, want true")
	}
	if setter.value != 8 {
		t.Errorf("clamped setter value = %v, want 8", setter.value)
	}
}

func TestClapParamLabelModulePrefixAndTruncation(t *testing.T) {
	tests := []struct {
		name string
		in   audio.ClapParamInfo
		want string
	}{
		{"plain", audio.ClapParamInfo{ClapID: 1, Name: "Cutoff"}, "Cutoff"},
		{"module", audio.ClapParamInfo{ClapID: 1, Module: "Filter", Name: "Drive"}, "Filter Drive"},
		{"module equals name", audio.ClapParamInfo{ClapID: 1, Module: "Drive", Name: "Drive"}, "Drive"},
		{"empty name", audio.ClapParamInfo{ClapID: 42}, "Param 42"},
		{"truncated", audio.ClapParamInfo{ClapID: 1, Name: "Sixteen Chars Maxi"}, "Sixteen Chars Ma"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := clapParamLabel(tt.in)
			if got != tt.want {
				t.Errorf("clapParamLabel = %q, want %q", got, tt.want)
			}
			if len(got) > 16 {
				t.Errorf("label %q is %d bytes, want <= 16", got, len(got))
			}
		})
	}
}

func TestClapParamStep(t *testing.T) {
	tests := []struct {
		name string
		in   audio.ClapParamInfo
		want float32
	}{
		{"stepped", audio.ClapParamInfo{MinValue: 0, MaxValue: 8, Flags: audio.ClapParamIsStepped}, 1},
		{"enum", audio.ClapParamInfo{MinValue: 0, MaxValue: 2, Flags: audio.ClapParamIsEnum}, 1},
		{"small integer range", audio.ClapParamInfo{MinValue: 0, MaxValue: 10}, 1},
		{"wide range", audio.ClapParamInfo{MinValue: 0, MaxValue: 100}, float32(100.0 / 127)},
		{"degenerate range", audio.ClapParamInfo{MinValue: 1, MaxValue: 1}, 0},
		{"unknown range", audio.ClapParamInfo{MinValue: math.NaN(), MaxValue: 1}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := clapParamStep(tt.in); math.Abs(float64(got-tt.want)) > 1e-6 {
				t.Errorf("clapParamStep = %v, want %v", got, tt.want)
			}
		})
	}
}
