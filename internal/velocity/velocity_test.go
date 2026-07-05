package velocity

import (
	"math"
	"strings"
	"testing"
)

func TestNewPresets(t *testing.T) {
	tests := []struct {
		name      string
		curve     string
		gamma     float32
		wantGamma float32
	}{
		{"empty defaults to linear", "", 0, 1.0},
		{"linear", "linear", 0, 1.0},
		{"soft", "soft", 0, 0.6},
		{"hard", "hard", 0, 1.6},
		{"custom uses given gamma", "custom", 0.8, 0.8},
		{"preset ignores stray gamma", "soft", 2.5, 0.6},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New(tt.curve, tt.gamma, 0, 0)
			if err != nil {
				t.Fatalf("New(%q, %v, 0, 0) error: %v", tt.curve, tt.gamma, err)
			}
			if c.gamma != tt.wantGamma {
				t.Errorf("gamma = %v, want %v", c.gamma, tt.wantGamma)
			}
			if c.outMin != 1 || c.outMax != 127 {
				t.Errorf("clamp defaults = %d..%d, want 1..127", c.outMin, c.outMax)
			}
		})
	}
}

func TestNewErrors(t *testing.T) {
	tests := []struct {
		name           string
		curve          string
		gamma          float32
		outMin, outMax uint8
	}{
		{"unknown curve name", "sigmoid", 0, 0, 0},
		{"custom with gamma zero", "custom", 0, 0, 0},
		{"custom with negative gamma", "custom", -1.5, 0, 0},
		{"custom with NaN gamma", "custom", float32(math.NaN()), 0, 0},
		{"outMin > outMax", "linear", 0, 100, 50},
		{"outMin > defaulted outMax", "linear", 0, 200, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := New(tt.curve, tt.gamma, tt.outMin, tt.outMax); err == nil {
				t.Errorf("New(%q, %v, %d, %d) = nil error, want error",
					tt.curve, tt.gamma, tt.outMin, tt.outMax)
			}
		})
	}
}

func TestNewClampDefaults(t *testing.T) {
	tests := []struct {
		name             string
		outMin, outMax   uint8
		wantMin, wantMax uint8
	}{
		{"both zero default to 1..127", 0, 0, 1, 127},
		{"outMin kept, outMax defaulted", 20, 0, 20, 127},
		{"outMin defaulted, outMax kept", 0, 100, 1, 100},
		{"both explicit", 10, 110, 10, 110},
		{"equal min and max allowed", 64, 64, 64, 64},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c, err := New("linear", 0, tt.outMin, tt.outMax)
			if err != nil {
				t.Fatalf("New error: %v", err)
			}
			if c.outMin != tt.wantMin || c.outMax != tt.wantMax {
				t.Errorf("clamp = %d..%d, want %d..%d", c.outMin, c.outMax, tt.wantMin, tt.wantMax)
			}
		})
	}
}

func TestApplyZeroIsAlwaysZero(t *testing.T) {
	curves := map[string]Curve{
		"linear":  mustNew(t, "linear", 0, 0, 0),
		"soft":    mustNew(t, "soft", 0, 0, 0),
		"hard":    mustNew(t, "hard", 0, 0, 0),
		"floored": mustNew(t, "linear", 0, 20, 0), // even with out_min raised
	}
	for name, c := range curves {
		if got := c.Apply(0); got != 0 {
			t.Errorf("%s: Apply(0) = %d, want 0 (NoteOn vel 0 is NoteOff semantics)", name, got)
		}
	}
}

func TestApplyEndpointsAndClamps(t *testing.T) {
	tests := []struct {
		name           string
		curve          string
		gamma          float32
		outMin, outMax uint8
		in             uint8
		want           uint8
	}{
		{"linear identity mid", "linear", 0, 0, 0, 64, 64},
		{"linear 127 to outMax default", "linear", 0, 0, 0, 127, 127},
		{"soft 127 to outMax default", "soft", 0, 0, 0, 127, 127},
		{"hard 127 to outMax default", "hard", 0, 0, 0, 127, 127},
		{"127 to capped outMax", "linear", 0, 0, 100, 127, 100},
		{"soft 127 to capped outMax", "soft", 0, 0, 90, 127, 90},
		{"outMin floors quiet note", "linear", 0, 20, 0, 1, 20},
		{"outMin floors hard-curve quiet note", "hard", 0, 20, 0, 5, 20},
		{"v=1 never below 1", "custom", 3.0, 0, 0, 1, 1},
		{"soft lifts middle", "soft", 0, 0, 0, 64, 84},      // round(127*(64/127)^0.6)
		{"hard suppresses middle", "hard", 0, 0, 0, 64, 42}, // round(127*(64/127)^1.6)
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := mustNew(t, tt.curve, tt.gamma, tt.outMin, tt.outMax)
			if got := c.Apply(tt.in); got != tt.want {
				t.Errorf("Apply(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestApplyClampRangeRespected(t *testing.T) {
	// Every non-zero input must land inside [outMin, outMax] for every curve.
	for _, curve := range []string{"soft", "linear", "hard"} {
		c := mustNew(t, curve, 0, 20, 100)
		for v := 1; v <= 127; v++ {
			got := c.Apply(uint8(v))
			if got < 20 || got > 100 {
				t.Fatalf("%s: Apply(%d) = %d, outside clamp 20..100", curve, v, got)
			}
		}
	}
}

func TestApplyMonotonic(t *testing.T) {
	// Exhaustive monotonicity over the full 7-bit input range for
	// representative gammas, per the design doc's test plan.
	for _, gamma := range []float32{0.3, 0.6, 1.0, 1.6, 3.0} {
		c := mustNew(t, "custom", gamma, 0, 0)
		prev := c.Apply(0)
		if prev != 0 {
			t.Fatalf("γ=%v: Apply(0) = %d, want 0", gamma, prev)
		}
		for v := 1; v <= 127; v++ {
			got := c.Apply(uint8(v))
			if got < 1 {
				t.Fatalf("γ=%v: Apply(%d) = %d, below 1", gamma, v, got)
			}
			if got < prev {
				t.Fatalf("γ=%v: Apply(%d) = %d < Apply(%d) = %d, not monotonic", gamma, v, got, v-1, prev)
			}
			prev = got
		}
	}
}

func TestLinearIsIdentity(t *testing.T) {
	c := Linear()
	if got := c.Apply(0); got != 0 {
		t.Errorf("Linear().Apply(0) = %d, want 0", got)
	}
	for v := 1; v <= 127; v++ {
		if got := c.Apply(uint8(v)); got != uint8(v) {
			t.Errorf("Linear().Apply(%d) = %d, want %d", v, got, v)
		}
	}
}

func TestDescribe(t *testing.T) {
	tests := []struct {
		name           string
		curve          string
		gamma          float32
		outMin, outMax uint8
		want           string
	}{
		{"soft", "soft", 0, 0, 0, "soft (γ=0.60, out 1..127)"},
		{"linear", "linear", 0, 0, 0, "linear (γ=1.00, out 1..127)"},
		{"empty resolves to linear", "", 0, 0, 0, "linear (γ=1.00, out 1..127)"},
		{"hard with clamps", "hard", 0, 20, 100, "hard (γ=1.60, out 20..100)"},
		{"custom", "custom", 0.8, 0, 0, "custom (γ=0.80, out 1..127)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := mustNew(t, tt.curve, tt.gamma, tt.outMin, tt.outMax)
			if got := c.Describe(); got != tt.want {
				t.Errorf("Describe() = %q, want %q", got, tt.want)
			}
		})
	}

	t.Run("Linear() describes as linear", func(t *testing.T) {
		if got := Linear().Describe(); !strings.HasPrefix(got, "linear ") {
			t.Errorf("Linear().Describe() = %q, want linear prefix", got)
		}
	})
}

func mustNew(t *testing.T, curve string, gamma float32, outMin, outMax uint8) Curve {
	t.Helper()
	c, err := New(curve, gamma, outMin, outMax)
	if err != nil {
		t.Fatalf("New(%q, %v, %d, %d) error: %v", curve, gamma, outMin, outMax, err)
	}
	return c
}
