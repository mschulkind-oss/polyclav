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

// --- point curves (v2) -------------------------------------------------------

// identityPoints is the two-point identity mapping — the simplest valid
// point set and the baseline for interpolation-exactness checks.
var identityPoints = [][2]uint8{{0, 0}, {127, 127}}

func mustNewFromPoints(t *testing.T, points [][2]uint8, outMin, outMax uint8) Curve {
	t.Helper()
	c, err := NewFromPoints(points, outMin, outMax)
	if err != nil {
		t.Fatalf("NewFromPoints(%v, %d, %d) error: %v", points, outMin, outMax, err)
	}
	return c
}

func TestNewFromPointsErrors(t *testing.T) {
	tooMany := make([][2]uint8, 17)
	for i := range tooMany {
		tooMany[i] = [2]uint8{uint8(i * 7), uint8(i * 7)}
	}
	tooMany[16] = [2]uint8{127, 127}

	tests := []struct {
		name           string
		points         [][2]uint8
		outMin, outMax uint8
	}{
		{"nil points", nil, 0, 0},
		{"one point", [][2]uint8{{0, 0}}, 0, 0},
		{"seventeen points", tooMany, 0, 0},
		{"first point x nonzero", [][2]uint8{{1, 0}, {127, 127}}, 0, 0},
		{"first point y nonzero", [][2]uint8{{0, 5}, {127, 127}}, 0, 0},
		{"last x not 127", [][2]uint8{{0, 0}, {126, 127}}, 0, 0},
		{"xs equal", [][2]uint8{{0, 0}, {64, 60}, {64, 90}, {127, 127}}, 0, 0},
		{"xs decreasing", [][2]uint8{{0, 0}, {90, 90}, {64, 100}, {127, 127}}, 0, 0},
		{"ys decreasing", [][2]uint8{{0, 0}, {64, 90}, {127, 80}}, 0, 0},
		{"y above 127", [][2]uint8{{0, 0}, {127, 200}}, 0, 0},
		{"outMin > outMax", identityPoints, 100, 50},
		{"outMin > defaulted outMax", identityPoints, 200, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if _, err := NewFromPoints(tt.points, tt.outMin, tt.outMax); err == nil {
				t.Errorf("NewFromPoints(%v, %d, %d) = nil error, want error",
					tt.points, tt.outMin, tt.outMax)
			}
		})
	}
}

func TestNewFromPointsCounts(t *testing.T) {
	// 2 and 16 points are the inclusive bounds — both must construct.
	sixteen := make([][2]uint8, 16)
	for i := range sixteen {
		sixteen[i] = [2]uint8{uint8(i * 8), uint8(i * 8)}
	}
	sixteen[0] = [2]uint8{0, 0}
	sixteen[15] = [2]uint8{127, 127}
	mustNewFromPoints(t, identityPoints, 0, 0)
	mustNewFromPoints(t, sixteen, 0, 0)
}

func TestPointsApplyZeroIsAlwaysZero(t *testing.T) {
	curves := map[string]Curve{
		"identity": mustNewFromPoints(t, identityPoints, 0, 0),
		"lifted":   mustNewFromPoints(t, [][2]uint8{{0, 0}, {64, 90}, {127, 127}}, 0, 0),
		"floored":  mustNewFromPoints(t, identityPoints, 20, 0), // even with out_min raised
	}
	for name, c := range curves {
		if got := c.Apply(0); got != 0 {
			t.Errorf("%s: Apply(0) = %d, want 0 (NoteOn vel 0 is NoteOff semantics)", name, got)
		}
	}
}

func TestPointsApplyIdentity(t *testing.T) {
	c := mustNewFromPoints(t, identityPoints, 0, 0)
	for v := 0; v <= 127; v++ {
		if got := c.Apply(uint8(v)); got != uint8(v) {
			t.Errorf("identity points: Apply(%d) = %d, want %d", v, got, v)
		}
	}
}

func TestPointsApplyInterpolation(t *testing.T) {
	tests := []struct {
		name           string
		points         [][2]uint8
		outMin, outMax uint8
		in             uint8
		want           uint8
	}{
		// [[0,0],[64,90],[127,127]]: first segment slope 90/64, second 37/63.
		{"knot hit exact", [][2]uint8{{0, 0}, {64, 90}, {127, 127}}, 0, 0, 64, 90},
		{"first segment midpoint", [][2]uint8{{0, 0}, {64, 90}, {127, 127}}, 0, 0, 32, 45},
		{"first segment rounds", [][2]uint8{{0, 0}, {64, 90}, {127, 127}}, 0, 0, 33, 46},   // 33*90/64 = 46.406 → 46
		{"second segment rounds", [][2]uint8{{0, 0}, {64, 90}, {127, 127}}, 0, 0, 96, 109}, // 90 + 32*37/63 = 108.79 → 109
		{"endpoint 127 to last y", [][2]uint8{{0, 0}, {64, 90}, {127, 127}}, 0, 0, 127, 127},
		{"endpoint 127 capped y", [][2]uint8{{0, 0}, {64, 60}, {127, 100}}, 0, 0, 127, 100},
		{"127 clamped to outMax", [][2]uint8{{0, 0}, {64, 90}, {127, 127}}, 0, 100, 127, 100},
		{"outMin floors quiet note", identityPoints, 20, 0, 1, 20},
		// Flat-at-zero segment: interpolated 0 for a played note must be
		// lifted to 1 (a NoteOn may never turn into a NoteOff).
		{"flat zero segment lifts to 1", [][2]uint8{{0, 0}, {100, 0}, {127, 127}}, 0, 0, 50, 1},
		{"v=1 never below 1", identityPoints, 0, 0, 1, 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := mustNewFromPoints(t, tt.points, tt.outMin, tt.outMax)
			if got := c.Apply(tt.in); got != tt.want {
				t.Errorf("Apply(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}

func TestPointsApplyMonotonic(t *testing.T) {
	// Exhaustive monotonicity over the full 7-bit input range for several
	// point sets, per the design doc's test plan.
	sixteen := make([][2]uint8, 16)
	for i := range sixteen {
		sixteen[i] = [2]uint8{uint8(i * 8), uint8((i * i) / 2)}
	}
	sixteen[15] = [2]uint8{127, 120}
	sets := map[string][][2]uint8{
		"identity":     identityPoints,
		"lifted":       {{0, 0}, {64, 90}, {127, 127}},
		"suppressed":   {{0, 0}, {64, 40}, {127, 127}},
		"flat then up": {{0, 0}, {100, 0}, {127, 127}},
		"step-ish":     {{0, 0}, {63, 1}, {64, 120}, {127, 127}},
		"sixteen":      sixteen,
	}
	for name, points := range sets {
		c := mustNewFromPoints(t, points, 0, 0)
		if got := c.Apply(0); got != 0 {
			t.Fatalf("%s: Apply(0) = %d, want 0", name, got)
		}
		prev := uint8(0)
		for v := 1; v <= 127; v++ {
			got := c.Apply(uint8(v))
			if got < 1 {
				t.Fatalf("%s: Apply(%d) = %d, below 1", name, v, got)
			}
			if v > 1 && got < prev {
				t.Fatalf("%s: Apply(%d) = %d < Apply(%d) = %d, not monotonic", name, v, got, v-1, prev)
			}
			prev = got
		}
	}
}

func TestPointsApplyClampRangeRespected(t *testing.T) {
	// Every non-zero input must land inside [outMin, outMax].
	c := mustNewFromPoints(t, [][2]uint8{{0, 0}, {64, 90}, {127, 127}}, 20, 100)
	for v := 1; v <= 127; v++ {
		got := c.Apply(uint8(v))
		if got < 20 || got > 100 {
			t.Fatalf("Apply(%d) = %d, outside clamp 20..100", v, got)
		}
	}
}

func TestNewFromPointsDefensiveCopy(t *testing.T) {
	// The curve must not alias the caller's slice — patch selection hands
	// config-owned slices in, and a later mutation must not warp the
	// installed curve.
	points := [][2]uint8{{0, 0}, {64, 90}, {127, 127}}
	c := mustNewFromPoints(t, points, 0, 0)
	points[1] = [2]uint8{64, 0}
	if got := c.Apply(64); got != 90 {
		t.Errorf("Apply(64) = %d after caller mutation, want 90 (defensive copy)", got)
	}
}

func TestPointsDescribe(t *testing.T) {
	tests := []struct {
		name           string
		points         [][2]uint8
		outMin, outMax uint8
		want           string
	}{
		{"two points default clamp", identityPoints, 0, 0, "points[2] (out 1..127)"},
		{"four points with clamp", [][2]uint8{{0, 0}, {32, 20}, {64, 90}, {127, 127}}, 20, 100, "points[4] (out 20..100)"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := mustNewFromPoints(t, tt.points, tt.outMin, tt.outMax)
			if got := c.Describe(); got != tt.want {
				t.Errorf("Describe() = %q, want %q", got, tt.want)
			}
		})
	}
}
