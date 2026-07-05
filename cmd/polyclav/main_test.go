package main

import (
	"strings"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/config"
	"github.com/mschulkind-oss/polyclav/internal/patches"
)

// TestResolveVelocity covers the precedence rules from
// docs/VELOCITY_CURVES.md: per-patch override fields win over the global
// [midi.velocity] block, and a patch gamma with no curve name means
// "custom".
func TestResolveVelocity(t *testing.T) {
	tests := []struct {
		name       string
		cfg        *config.Config
		patch      *patches.Patch
		wantErr    bool
		wantPrefix string // Describe() prefix, e.g. "soft (γ=0.60"
	}{
		{
			name:       "zero config nil patch is linear",
			cfg:        config.Defaults(),
			patch:      nil,
			wantPrefix: "linear (γ=1.00, out 1..127)",
		},
		{
			name: "global preset with clamp",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Curve: "soft", OutMin: 10, OutMax: 100,
			}}},
			patch:      nil,
			wantPrefix: "soft (γ=0.60, out 10..100)",
		},
		{
			name: "patch with no override inherits global",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Curve: "hard",
			}}},
			patch:      &patches.Patch{Name: "p"},
			wantPrefix: "hard (γ=1.60, out 1..127)",
		},
		{
			name: "patch curve name wins over global",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Curve: "soft", OutMin: 10, OutMax: 100,
			}}},
			patch: &patches.Patch{Name: "p", VelocityCurve: "hard"},
			// Per-patch overrides carry no clamp fields: defaults 1..127.
			wantPrefix: "hard (γ=1.60, out 1..127)",
		},
		{
			name:       "patch gamma alone implies custom",
			cfg:        config.Defaults(),
			patch:      &patches.Patch{Name: "p", VelocityGamma: 2.5},
			wantPrefix: "custom (γ=2.50, out 1..127)",
		},
		{
			name:    "patch custom without gamma errors",
			cfg:     config.Defaults(),
			patch:   &patches.Patch{Name: "p", VelocityCurve: "custom"},
			wantErr: true,
		},
		{
			name: "global custom gamma",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Curve: "custom", Gamma: 0.8,
			}}},
			patch:      nil,
			wantPrefix: "custom (γ=0.80, out 1..127)",
		},
		{
			// The global shorthand mirrors the per-patch one: gamma > 0
			// with no curve name means "custom", not "silently ignored".
			name: "global gamma alone implies custom",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Gamma: 2.5,
			}}},
			patch:      nil,
			wantPrefix: "custom (γ=2.50, out 1..127)",
		},
		{
			name: "global gamma alone with clamp",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Gamma: 0.8, OutMin: 10, OutMax: 100,
			}}},
			patch:      nil,
			wantPrefix: "custom (γ=0.80, out 10..100)",
		},
		{
			// A named preset with a stray gamma keeps the preset — the
			// shorthand only fills an EMPTY curve name.
			name: "global preset ignores stray gamma",
			cfg: &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
				Curve: "soft", Gamma: 2.5,
			}}},
			patch:      nil,
			wantPrefix: "soft (γ=0.60, out 1..127)",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			curve, err := resolveVelocity(tt.cfg, tt.patch)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("resolveVelocity() error = nil, want error")
				}
				return
			}
			if err != nil {
				t.Fatalf("resolveVelocity() error = %v", err)
			}
			if got := curve.Describe(); !strings.HasPrefix(got, tt.wantPrefix) {
				t.Errorf("Describe() = %q, want prefix %q", got, tt.wantPrefix)
			}
			// v==0 must always pass through untouched (NoteOff semantics).
			if got := curve.Apply(0); got != 0 {
				t.Errorf("Apply(0) = %d, want 0", got)
			}
		})
	}
}

// TestPatchFollowerLevelTriggered pins the level-triggered contract:
// apply fires only when the CURRENT patch differs from the last applied
// name — regardless of how many (possibly dropped) events occurred in
// between — and repeated events for the same patch are no-ops.
func TestPatchFollowerLevelTriggered(t *testing.T) {
	var cur *patches.Patch
	var applied []string
	follow := newPatchFollower("", func() *patches.Patch { return cur }, func(p *patches.Patch) bool {
		if p == nil {
			applied = append(applied, "<nil>")
		} else {
			applied = append(applied, p.Name)
		}
		return true
	})

	follow() // no patch selected, last == "" — level already satisfied
	if len(applied) != 0 {
		t.Fatalf("no-change event applied something: %v", applied)
	}

	cur = &patches.Patch{Name: "a"}
	follow()
	follow() // same patch again: no re-apply
	if want := []string{"a"}; !equalStrings(applied, want) {
		t.Fatalf("after selecting a: applied %v, want %v", applied, want)
	}

	// Simulate a dropped "patch" event: the patch changed a -> b -> c but
	// the follower only sees one (unrelated) event afterwards. It must
	// still converge on c.
	cur = &patches.Patch{Name: "b"}
	cur = &patches.Patch{Name: "c"}
	follow()
	if want := []string{"a", "c"}; !equalStrings(applied, want) {
		t.Fatalf("after dropped events: applied %v, want %v", applied, want)
	}

	// Deselecting (current == nil) is a change back to "no patch".
	cur = nil
	follow()
	if want := []string{"a", "c", "<nil>"}; !equalStrings(applied, want) {
		t.Fatalf("after deselect: applied %v, want %v", applied, want)
	}
}

// TestPatchFollowerRetriesFailedApply pins the "resolved for" tracking:
// a failed apply must NOT record the new name, so the next event of any
// type retries instead of leaving stale state installed.
func TestPatchFollowerRetriesFailedApply(t *testing.T) {
	cur := &patches.Patch{Name: "a"}
	calls := 0
	ok := false
	follow := newPatchFollower("", func() *patches.Patch { return cur }, func(p *patches.Patch) bool {
		calls++
		return ok
	})

	follow()
	follow() // apply failed: retried on the next event
	if calls != 2 {
		t.Fatalf("failed apply not retried: %d calls, want 2", calls)
	}
	ok = true
	follow() // succeeds and records "a"
	follow() // level satisfied: no further calls
	if calls != 3 {
		t.Fatalf("after success: %d calls, want 3 (no re-apply once recorded)", calls)
	}
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestResolveVelocityClamp checks the global OutMin/OutMax ints reach the
// curve as a working clamp, not just in the label.
func TestResolveVelocityClamp(t *testing.T) {
	cfg := &config.Config{MIDI: config.MIDIConfig{Velocity: config.VelocityConfig{
		Curve: "linear", OutMin: 20, OutMax: 90,
	}}}
	curve, err := resolveVelocity(cfg, nil)
	if err != nil {
		t.Fatalf("resolveVelocity() error = %v", err)
	}
	if got := curve.Apply(127); got != 90 {
		t.Errorf("Apply(127) = %d, want 90 (out_max clamp)", got)
	}
	if got := curve.Apply(1); got != 20 {
		t.Errorf("Apply(1) = %d, want 20 (out_min clamp)", got)
	}
}
