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
