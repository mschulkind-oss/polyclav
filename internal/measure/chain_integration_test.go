package measure

import (
	"fmt"
	"math"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/audio"
)

// ceilingLin is the limiter's default ceiling (-0.3 dBFS) in linear
// amplitude. The limiter clamps to it, so a sample at this level is the
// limiter riding its ceiling — the fallback working, not hard clipping.
var ceilingLin = float32(math.Pow(10, -0.3/20))

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
	lufs, rms, peak, crest, ceilFrac float32
}

// renderChain renders the integration phrase and measures it. ceilFrac is the
// fraction of output samples sitting on the limiter ceiling — how hard the
// fallback is working. The limiter clamps at the ceiling, so samples cannot
// exceed it; there is no hard clipping to measure.
func renderChain(t *testing.T, cp *audio.ChainParams) chainResult {
	t.Helper()
	s, err := audio.RenderOfflineEvents("native", "minimoog", "", cp, integrationPhrase(), 48000*2)
	if err != nil {
		t.Fatalf("render: %v", err)
	}
	var sumSq float64
	onCeiling := 0
	for _, v := range s {
		sumSq += float64(v) * float64(v)
		if v >= ceilingLin-1e-4 || v <= -ceilingLin+1e-4 {
			onCeiling++
		}
	}
	rms := float32(math.Sqrt(sumSq / float64(len(s))))
	peak := audio.MeasurePeakDBFS(s)
	return chainResult{
		lufs:     audio.MeasureLUFS(s),
		rms:      20 * float32(math.Log10(float64(rms))),
		peak:     peak,
		crest:    peak - 20*float32(math.Log10(float64(rms))),
		ceilFrac: float32(onCeiling) / float32(len(s)),
	}
}

func chainAt(master, comp float32) *audio.ChainParams {
	cp := audio.NewChainParams()
	cp.MasterVolume = master
	cp.MasteringAmount = comp
	return &cp
}

// TestChainMasterTurnsUpLoudness: the master is drive into the mastering
// compressor, so it must still turn up loudness and never down.
func TestChainMasterTurnsUpLoudness(t *testing.T) {
	steps := []float32{0.6, 0.7, 0.8, 1.0, 1.5, 2.0}
	prevV := steps[0]
	prev := renderChain(t, chainAt(prevV, 0.5))
	for _, m := range steps[1:] {
		got := renderChain(t, chainAt(m, 0.5))
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

// TestChainCompressorLevelsAndLifts: the threshold-tied makeup means the comp
// control is a leveling *and* loudness control. Turning it up must never lose
// loudness, must reduce the crest (squash the dynamics), and must not push the
// peak past the ceiling.
func TestChainCompressorLevelsAndLifts(t *testing.T) {
	steps := []float32{0, 0.25, 0.5, 0.75, 1.0}
	prev := renderChain(t, chainAt(1.0, steps[0]))
	for _, comp := range steps[1:] {
		got := renderChain(t, chainAt(1.0, comp))
		if got.lufs < prev.lufs-0.05 {
			t.Errorf("comp %.2f (%.2f LUFS) is quieter than the step below it (%.2f LUFS)",
				comp, got.lufs, prev.lufs)
		}
		if got.crest > prev.crest+0.05 {
			t.Errorf("comp %.2f crest %.2f dB is above the step below it (%.2f dB) — no squash",
				comp, got.crest, prev.crest)
		}
		if float64(got.peak) > 20*math.Log10(float64(ceilingLin))+1e-3 {
			t.Errorf("comp %.2f peak %.2f dBFS exceeded the ceiling", comp, got.peak)
		}
		prev = got
	}
	if gain := prev.lufs - renderChain(t, chainAt(1.0, 0)).lufs; gain < 8 {
		t.Errorf("comp 0 -> 1.0 added only %.2f dB of loudness, want at least 8", gain)
	}
}

// TestChainCeilingHolds: across the whole control range the fallback limiter
// must hold the ceiling cleanly — no sample past it, and none hard-clamped.
func TestChainCeilingHolds(t *testing.T) {
	ceilDB := 20 * math.Log10(float64(ceilingLin))
	for _, master := range []float32{1.0, 2.0, 4.0, 8.0} {
		for _, comp := range []float32{0, 0.5, 1.0} {
			got := renderChain(t, chainAt(master, comp))
			if float64(got.peak) > ceilDB+1e-2 {
				t.Errorf("master %.1f comp %.2f peak %.2f dBFS exceeded the ceiling %.2f",
					master, comp, got.peak, ceilDB)
			}
			if got.ceilFrac > 0.01 {
				t.Errorf("master %.1f comp %.2f pins %.2f%% of samples on the ceiling",
					master, comp, got.ceilFrac*100)
			}
		}
	}
}

// TestChainMasterCanStillReachTheCeiling: even with the compressor lifting
// hard, driving the master far enough must put the peak on the ceiling — the
// fallback is reachable, not bypassed.
func TestChainMasterCanStillReachTheCeiling(t *testing.T) {
	quiet := renderChain(t, chainAt(1.0, 0.5))
	loud := renderChain(t, chainAt(8.0, 0.5))
	if loud.lufs-quiet.lufs < 2 {
		t.Errorf("master 1.0 -> 8.0 added only %.2f dB of loudness, want at least 2",
			loud.lufs-quiet.lufs)
	}
	if loud.peak < -1.0 {
		t.Errorf("master 8.0 peak = %.2f dBFS; expected the ceiling to be reached", loud.peak)
	}
}

// TestChainNominalSettingsAreClean: the operating point a player lands on —
// compressor at half, master at unity — must be clean.
func TestChainNominalSettingsAreClean(t *testing.T) {
	got := renderChain(t, chainAt(1.0, 0.5))
	if got.ceilFrac > 0.001 {
		t.Errorf("comp 50%% + master 100%% rides the ceiling on %.2f%% of samples", got.ceilFrac*100)
	}
	if got.lufs < -24 {
		t.Errorf("comp 50%% + master 100%% is inaudibly quiet at %.2f LUFS", got.lufs)
	}
}

// TestChainDirectionalSummary prints the table the assertions above encode so
// a failure is easy to read.
func TestChainDirectionalSummary(t *testing.T) {
	fmt.Printf("%-26s %8s %8s %8s %8s %8s\n", "config", "LUFS", "RMS", "peak", "crest", "%ceil")
	for _, m := range []float32{1.0, 2.0, 4.0} {
		for _, comp := range []float32{0, 0.25, 0.5, 1.0} {
			r := renderChain(t, chainAt(m, comp))
			fmt.Printf("%-26s %8.2f %8.2f %8.2f %8.2f %7.2f%%\n",
				fmt.Sprintf("master=%.1f comp=%.2f", m, comp), r.lufs, r.rms, r.peak, r.crest, r.ceilFrac*100)
		}
	}
	for _, m := range []float32{4.0, 6.0, 8.0} {
		r := renderChain(t, chainAt(m, 0.5))
		fmt.Printf("%-26s %8.2f %8.2f %8.2f %8.2f %7.2f%%\n",
			fmt.Sprintf("master=%.1f comp=0.50", m), r.lufs, r.rms, r.peak, r.crest, r.ceilFrac*100)
	}
}
