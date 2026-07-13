package measure

import (
	"math"
	"strings"
	"testing"
)

func TestCheckConsistentPasses(t *testing.T) {
	ms := []Measurement{
		{Label: "a", LUFS: -18.0},
		{Label: "b", LUFS: -18.5},
		{Label: "c", LUFS: -17.6},
	}
	ok, report := CheckConsistent(ms, 2.0)
	if !ok {
		t.Errorf("expected consistent within 2 LU, got report:\n%s", report)
	}
	if !strings.Contains(report, "a") || !strings.Contains(report, "LUFS") {
		t.Errorf("report missing expected content: %s", report)
	}
}

func TestCheckConsistentFlagsOutlier(t *testing.T) {
	ms := []Measurement{
		{Label: "quiet-piano", LUFS: -18.0},
		{Label: "also-quiet", LUFS: -18.3},
		{Label: "way-louder", LUFS: -6.0},
	}
	ok, report := CheckConsistent(ms, 2.0)
	if ok {
		t.Fatalf("expected inconsistency to be flagged, got ok report:\n%s", report)
	}
	if !strings.Contains(report, "way-louder") || !strings.Contains(report, "SPREAD OUTLIER") {
		t.Errorf("report should call out the outlier: %s", report)
	}
	// The two well-matched patches must NOT be flagged even though the
	// outlier is present — only way-louder should carry the marker.
	lines := strings.Split(report, "\n")
	for _, line := range lines {
		if strings.Contains(line, "quiet-piano") && strings.Contains(line, "OUTSIDE") {
			t.Errorf("quiet-piano should not be flagged: %s", line)
		}
	}
}

func TestCheckConsistentFlagsSilence(t *testing.T) {
	ms := []Measurement{
		{Label: "audible-1", LUFS: -18.0},
		{Label: "audible-2", LUFS: -18.2},
		{Label: "silent", LUFS: float32(math.Inf(-1))},
	}
	ok, report := CheckConsistent(ms, 2.0)
	if ok {
		t.Fatalf("a silent entry among audible ones must fail consistency, got:\n%s", report)
	}
	lines := strings.Split(report, "\n")
	for _, line := range lines {
		switch {
		case strings.Contains(line, "silent"):
			if !strings.Contains(line, "SILENT") {
				t.Errorf("silent entry should be flagged SILENT: %s", line)
			}
		case strings.Contains(line, "audible-1"), strings.Contains(line, "audible-2"):
			// The two well-matched audible entries (spread 0.2 LU,
			// well under the 2.0 tolerance) must not be flagged —
			// only the silent entry is responsible for the failure.
			if strings.Contains(line, "<--") {
				t.Errorf("audible entry should not be flagged: %s", line)
			}
			if strings.Contains(line, "Inf") {
				t.Errorf("audible entry's own value should not be non-finite: %s", line)
			}
		}
	}
}

func TestCheckGradualPasses(t *testing.T) {
	ms := []Measurement{
		{Label: "0.0", LUFS: -60.0},
		{Label: "0.1", LUFS: -30.0},
		{Label: "0.2", LUFS: -28.0},
		{Label: "0.3", LUFS: -26.5},
	}
	ok, report := CheckGradual(ms, 30.5)
	if !ok {
		t.Errorf("expected gradual sweep to pass, got:\n%s", report)
	}
}

// This is the exact shape of the original drive-pedal bug report: a
// tiny step at the bottom of a sweep already jumps to (near-)maximum
// loudness. CheckGradual must catch it.
func TestCheckGradualCatchesOnOffJump(t *testing.T) {
	ms := []Measurement{
		{Label: "amount=0.00", LUFS: -18.5},
		{Label: "amount=0.01", LUFS: -6.0}, // the reported bug: 1% already "very loud"
		{Label: "amount=0.02", LUFS: -5.9},
	}
	ok, report := CheckGradual(ms, 3.0)
	if ok {
		t.Fatalf("expected the on/off jump to be caught, got ok report:\n%s", report)
	}
	if !strings.Contains(report, "JUMP") {
		t.Errorf("report should flag the jump: %s", report)
	}
	if !strings.Contains(report, "amount=0.00") || !strings.Contains(report, "amount=0.01") {
		t.Errorf("report should name the two steps involved in the jump: %s", report)
	}
}

func TestCheckBoundedPasses(t *testing.T) {
	ms := []Measurement{
		{Label: "a", PeakDBFS: -3.0},
		{Label: "b", PeakDBFS: -0.5},
	}
	ok, report := CheckBounded(ms, 0.0)
	if !ok {
		t.Errorf("expected both under 0 dBFS to pass, got:\n%s", report)
	}
}

func TestCheckBoundedFlagsOver(t *testing.T) {
	ms := []Measurement{
		{Label: "safe", PeakDBFS: -1.0},
		{Label: "hot", PeakDBFS: 2.5},
	}
	ok, report := CheckBounded(ms, 0.0)
	if ok {
		t.Fatalf("expected the over-bound entry to be flagged, got ok report:\n%s", report)
	}
	if !strings.Contains(report, "hot") || !strings.Contains(report, "OVER") {
		t.Errorf("report should call out the over-bound entry: %s", report)
	}
}

func TestCheckConsistentEmptyIsOk(t *testing.T) {
	ok, _ := CheckConsistent(nil, 1.0)
	if !ok {
		t.Error("empty measurement set should not fail consistency")
	}
}

func TestCheckGradualSingleIsOk(t *testing.T) {
	ok, _ := CheckGradual([]Measurement{{Label: "only", LUFS: -20}}, 1.0)
	if !ok {
		t.Error("a single measurement has nothing to compare and should pass")
	}
}
