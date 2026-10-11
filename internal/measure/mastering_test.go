package measure

import (
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/audio"
	"github.com/mschulkind-oss/polyclav/internal/patches"
)

// TestMasteringCompAmountAddsLoudnessMonotonically pins the mastering
// compressor as a *loudness* control: turning it up must never make the mix
// quieter. The pre-fix auto-makeup (`-threshold/2`) under-compensated the
// threshold drop, so 100% came out ~2 dB quieter than 75% while clipping
// harder — the "compression all the way up isn't louder" report. The fix
// recovers the reduction at a 0 dBFS reference, so loudness rises with amount
// and 100% is a genuine hard leveler.
func TestMasteringCompAmountAddsLoudnessMonotonically(t *testing.T) {
	p := patches.Patch{Name: "moog", Type: "native", Engine: "minimoog"}
	events := []audio.OfflineMIDIEvent{
		{Frame: 0, Kind: audio.OfflineNoteOn, Data1: 48, Data2: 100},
		{Frame: 0, Kind: audio.OfflineNoteOn, Data1: 60, Data2: 100},
		{Frame: 0, Kind: audio.OfflineNoteOn, Data1: 64, Data2: 100},
		{Frame: 0, Kind: audio.OfflineNoteOn, Data1: 67, Data2: 100},
	}
	ms, err := SweepChainParam(p, events, 48000*2, []float32{0, 0.25, 0.5, 0.75, 1.0},
		func(cp *audio.ChainParams, v float32) { cp.MasteringAmount = v })
	if err != nil {
		t.Fatalf("sweep mastering amount: %v", err)
	}

	for i := 1; i < len(ms); i++ {
		if ms[i].LUFS < ms[i-1].LUFS-0.05 {
			t.Errorf("comp %s (%.2f LUFS) is quieter than comp %s (%.2f LUFS) — amount must not lose loudness",
				ms[i].Label, ms[i].LUFS, ms[i-1].Label, ms[i-1].LUFS)
		}
	}
	if gain := ms[len(ms)-1].LUFS - ms[0].LUFS; gain < 5 {
		t.Errorf("comp 0 -> 1.0 added only %.2f dB of loudness, want at least 5", gain)
	}
}
