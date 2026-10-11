package measure

import (
	"fmt"
	"math"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/audio"
)

// integrationPhrase is the "real performance" the chain tests render: eight
// notes at a wide spread of velocities (a soft phrase, then a hard one) so the
// gain knobs are exercised the way playing actually exercises them, not with a
// single held chord.
func integrationPhrase() []audio.OfflineMIDIEvent {
	notes := []struct {
		frame uint32
		note  uint8
		vel   uint8
	}{
		{0, 60, 30}, {12000, 64, 60}, {24000, 67, 90}, {36000, 72, 120},
		{48000, 60, 45}, {60000, 64, 75}, {72000, 67, 105}, {84000, 72, 60},
	}
	var ev []audio.OfflineMIDIEvent
	for _, n := range notes {
		ev = append(ev,
			audio.OfflineMIDIEvent{Frame: n.frame, Kind: audio.OfflineNoteOn, Data1: n.note, Data2: uint16(n.vel)},
			audio.OfflineMIDIEvent{Frame: n.frame + 20000, Kind: audio.OfflineNoteOff, Data1: n.note},
		)
	}
	return ev
}

type chainResult struct {
	lufs, rms, peak, clipFrac float32
}

// renderChain renders the integration phrase and measures it. clipFrac is the
// fraction of samples pinned within 0.5 dB of full scale — a *hard clipping*
// proxy. Limiter gain reduction is deliberately not a failure here: a working
// master limiter engaging when you play loud is the design, not a fault. Only
// pinning (distortion) is bad.
func renderChain(t *testing.T, cp *audio.ChainParams) chainResult {
	t.Helper()
	s, err := audio.RenderOfflineEvents("native", "minimoog", "", cp, integrationPhrase(), 48000*2)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var sumSq float64
	const ceil = float32(0.944) // -0.5 dBFS
	pinned := 0
	for _, v := range s {
		sumSq += float64(v) * float64(v)
		if v > ceil || v < -ceil {
			pinned++
		}
	}
	rms := float32(math.Sqrt(sumSq / float64(len(s))))
	return chainResult{
		lufs:     audio.MeasureLUFS(s),
		rms:      20 * float32(math.Log10(float64(rms))),
		peak:     audio.MeasurePeakDBFS(s),
		clipFrac: float32(pinned) / float32(len(s)),
	}
}

func chainAt(master, comp float32) *audio.ChainParams {
	cp := audio.NewChainParams()
	cp.MasterVolume = master
	cp.MasteringAmount = comp
	return &cp
}

// TestChainLevelKnobTurnsUpLoudness: the master gain must actually turn up
// loudness, audibly at each step, and never down. This is the "80% is
// different from 70, different from 60" property for the loudness knob.
func TestChainLevelKnobTurnsUpLoudness(t *testing.T) {
	steps := []float32{0.6, 0.7, 0.8, 1.0, 1.5, 2.0}
	prevV := steps[0]
	prev := renderChain(t, chainAt(prevV, 1.0))
	for _, m := range steps[1:] {
		got := renderChain(t, chainAt(m, 1.0))
		if got.lufs < prev.lufs {
			t.Errorf("master %.2f (%.2f LUFS) is quieter than the step below it (%.2f LUFS)",
				m, got.lufs, prev.lufs)
		}
		if m <= 0.8 && got.lufs-prev.lufs < 0.2 {
			t.Errorf("master %.2f -> %.2f moved loudness only %.2f dB — a step must be audible",
				prevV, m, got.lufs-prev.lufs)
		}
		prevV, prev = m, got
	}
}

// TestChainMasterRangeReachesTheLimiter: the master must be able to drive the
// chain into the limiter — that IS where loudness comes from, and a working
// master limiter doing something when you play loud is the goal, not a fault.
// It must reach the limiter *cleanly*: holding the ceiling without hard
// clipping (only a small fraction of samples pinned at full scale).
func TestChainMasterRangeReachesTheLimiter(t *testing.T) {
	quiet := renderChain(t, chainAt(1.0, 0.25))
	loud := renderChain(t, chainAt(4.0, 0.25))
	if loud.lufs-quiet.lufs < 9 {
		t.Errorf("master 1.0 -> 4.0 added only %.2f dB of loudness, want at least 9",
			loud.lufs-quiet.lufs)
	}
	if loud.peak < -1.0 {
		t.Errorf("master 4.0 peak = %.2f dBFS; expected the limiter to be engaging near the ceiling", loud.peak)
	}
	if loud.clipFrac > 0.005 {
		t.Errorf("master 4.0 hard-clips: %.2f%% of samples pinned at full scale", loud.clipFrac*100)
	}
}

// TestChainCompressorNeverAddsClipping: the compressor is a dynamics control,
// not a gain stage. Turning it up must not lose loudness and must not push the
// chain into hard clipping on its own. (It is fine — expected — for a signal
// that is already loud to make the limiter work; this checks the compressor
// itself does not create the overload.)
func TestChainCompressorNeverAddsClipping(t *testing.T) {
	steps := []float32{0, 0.25, 0.5, 0.75, 1.0}
	prev := renderChain(t, chainAt(1.0, steps[0]))
	for _, comp := range steps[1:] {
		got := renderChain(t, chainAt(1.0, comp))
		if got.lufs < prev.lufs-0.05 {
			t.Errorf("comp %.2f (%.2f LUFS) is quieter than comp below it (%.2f LUFS)",
				comp, got.lufs, prev.lufs)
		}
		if got.clipFrac > 0.001 {
			t.Errorf("comp %.2f hard-clips at master 100%%: %.2f%% of samples pinned at full scale",
				comp, got.clipFrac*100)
		}
		if got.peak > prev.peak+1.0 {
			t.Errorf("comp %.2f raised the peak by %.2f dB (%.2f -> %.2f dBFS) — the compressor must not add level",
				comp, got.peak-prev.peak, prev.peak, got.peak)
		}
		prev = got
	}
}

// TestChainNominalSettingsAreClean: the operating point a player lands on —
// compressor and master both at 100% — must be clean and audible. The limiter
// may or may not be working there; that is not the test. Hard clipping is.
func TestChainNominalSettingsAreClean(t *testing.T) {
	got := renderChain(t, chainAt(1.0, 1.0))
	if got.clipFrac > 0.001 {
		t.Errorf("comp 100%% + master 100%% hard-clips: %.2f%% of samples at full scale", got.clipFrac*100)
	}
	if got.lufs < -24 {
		t.Errorf("comp 100%% + master 100%% is inaudibly quiet at %.2f LUFS", got.lufs)
	}
}

// TestChainDirectionalSummary prints the table the assertions above encode so
// a failure is easy to read.
func TestChainDirectionalSummary(t *testing.T) {
	fmt.Printf("%-26s %8s %8s %8s %8s\n", "config", "LUFS", "RMS", "peak", "%clip")
	for _, comp := range []float32{0, 0.5, 1.0} {
		r := renderChain(t, chainAt(1.0, comp))
		fmt.Printf("%-26s %8.2f %8.2f %8.2f %7.2f%%\n", fmt.Sprintf("comp=%.2f master=1.0", comp), r.lufs, r.rms, r.peak, r.clipFrac*100)
	}
	for _, m := range []float32{1.0, 2.0, 4.0, 8.0} {
		r := renderChain(t, chainAt(m, 1.0))
		fmt.Printf("%-26s %8.2f %8.2f %8.2f %7.2f%%\n", fmt.Sprintf("comp=1.0 master=%.2f", m), r.lufs, r.rms, r.peak, r.clipFrac*100)
	}
}
