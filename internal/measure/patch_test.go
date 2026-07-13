package measure

import (
	"strings"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/patches"
)

// loadFixture is the shared setup for the end-to-end tests below: parse
// the short test phrase once, with a short release tail so the last
// note's envelope isn't cut off mid-decay.
func loadFixture(t *testing.T) ([]audio.OfflineMIDIEvent, int) {
	t.Helper()
	events, totalFrames, err := LoadMIDIFile("testdata/short_phrase.mid", SampleRate/2)
	if err != nil {
		t.Fatalf("LoadMIDIFile: %v", err)
	}
	return events, int(totalFrames)
}

// This is the real, end-to-end version of the ask: render the same
// short performance through several patches and confirm they're about
// as loud as each other. Only one native engine ("minimoog") exists in
// this environment (no bundled soundfont/sfz assets — those are
// bootstrap-downloaded user data, not repo fixtures), so this proves
// the framework itself is correct using two differently-gain-trimmed
// copies of the same engine: identically trimmed patches must pass,
// and a deliberately mismatched trim must be caught. The same
// MeasurePatch/CheckConsistent path works unchanged for soundfont/lv2/
// clap patches once real assets are available to point at.
func TestMeasurePatchesConsistentWhenGainMatched(t *testing.T) {
	events, totalFrames := loadFixture(t)

	ps := []patches.Patch{
		{Name: "moog-a", Type: "native", Engine: "minimoog", GainDB: 0},
		{Name: "moog-b", Type: "native", Engine: "minimoog", GainDB: 0},
	}
	ms, err := MeasurePatches(ps, events, totalFrames)
	if err != nil {
		t.Fatalf("MeasurePatches: %v", err)
	}
	if len(ms) != 2 {
		t.Fatalf("expected 2 measurements, got %d", len(ms))
	}
	for _, m := range ms {
		if m.LUFS == 0 {
			t.Errorf("%s: LUFS looks unset (exactly 0)", m.Label)
		}
	}

	ok, report := CheckConsistent(ms, 0.5)
	if !ok {
		t.Errorf("identically-trimmed copies of the same patch should be consistent:\n%s", report)
	}
}

func TestMeasurePatchesCatchesGainMismatch(t *testing.T) {
	events, totalFrames := loadFixture(t)

	ps := []patches.Patch{
		{Name: "moog-quiet", Type: "native", Engine: "minimoog", GainDB: 0},
		{Name: "moog-loud", Type: "native", Engine: "minimoog", GainDB: 12}, // deliberately mismatched
	}
	ms, err := MeasurePatches(ps, events, totalFrames)
	if err != nil {
		t.Fatalf("MeasurePatches: %v", err)
	}

	ok, report := CheckConsistent(ms, 3.0)
	if ok {
		t.Fatalf("a 12 dB gain mismatch should be caught at a 3 LU tolerance:\n%s", report)
	}
	if !strings.Contains(report, "moog-loud") {
		t.Errorf("report should name the louder patch: %s", report)
	}
}

func TestMeasurePatchUnknownEngineErrors(t *testing.T) {
	events, totalFrames := loadFixture(t)
	p := patches.Patch{Name: "bogus", Type: "native", Engine: "does-not-exist"}
	if _, err := MeasurePatch(p.Name, p, events, totalFrames); err == nil {
		t.Fatal("expected an error for an unknown native engine")
	}
}

func TestMeasurePatchMissingReferenceErrors(t *testing.T) {
	events, totalFrames := loadFixture(t)
	// Type defaults to "soundfont" but Soundfont is left empty.
	p := patches.Patch{Name: "no-soundfont"}
	if _, err := MeasurePatch(p.Name, p, events, totalFrames); err == nil {
		t.Fatal("expected an error for a patch with no soundfont path configured")
	}
}
