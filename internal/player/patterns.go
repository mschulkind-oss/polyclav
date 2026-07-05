// The seven built-in diagnostic patterns (docs/AUDITION.md §1): coded,
// zero assets, each purpose-built to expose one setting. Everything is
// deterministic — no randomness — so A/B tweaks of a setting are a fair
// comparison. All events are on channel 0 (the Event zero value), and
// every NoteOn has its NoteOff inside the clip so loop wraps never leak
// held notes.
// (Package doc lives in player.go.)

package player

import (
	"cmp"
	"slices"

	"github.com/mschulkind-oss/polyclav/internal/midi"
)

// builders is the clip library in the stable order Player.Clips()
// exposes. Order is part of the contract — UIs index this list.
var builders = []func() ([]TimedEvent, ClipInfo){
	velRamp,
	sustainChord,
	arp,
	bassRiff,
	chromatic,
	staccato,
	burst,
}

// addNote appends a NoteOn/NoteOff pair. Building every note as a pair
// is what keeps clips self-contained (the player's loop-seam NoteOff
// safety net stays a no-op).
func addNote(evs []TimedEvent, onBeat, offBeat float64, note, vel byte) []TimedEvent {
	return append(evs,
		TimedEvent{Beat: onBeat, Ev: midi.Event{Kind: midi.NoteOn, Note: note, Vel: vel}},
		TimedEvent{Beat: offBeat, Ev: midi.Event{Kind: midi.NoteOff, Note: note}},
	)
}

// sortEvents orders a built pattern by beat; at equal beats NoteOff
// sorts before NoteOn so a re-struck pitch releases before it
// re-triggers. The sort is stable so chords keep their build order.
func sortEvents(evs []TimedEvent) []TimedEvent {
	slices.SortStableFunc(evs, func(a, b TimedEvent) int {
		if c := cmp.Compare(a.Beat, b.Beat); c != 0 {
			return c
		}
		return int(kindRank(a.Ev.Kind)) - int(kindRank(b.Ev.Kind))
	})
	return evs
}

func kindRank(k midi.Kind) int {
	if k == midi.NoteOff {
		return 0
	}
	return 1
}

// velRamp: middle C repeated with velocity stepping 1, 8, 16, …, 120,
// 127 and back down, one note per half-beat, each held a quarter-beat.
// The fixed steps make velocity-curve layer boundaries audible as you
// drag the curve — you hear exactly which step crosses a layer.
func velRamp() ([]TimedEvent, ClipInfo) {
	up := []byte{1}
	for v := 8; v <= 120; v += 8 {
		up = append(up, byte(v))
	}
	up = append(up, 127)
	vels := slices.Clone(up)
	for i := len(up) - 2; i >= 0; i-- { // mirror down, without repeating the 127 peak
		vels = append(vels, up[i])
	}

	var evs []TimedEvent
	for i, v := range vels {
		start := float64(i) * 0.5
		evs = addNote(evs, start, start+0.25, 60, v)
	}
	return sortEvents(evs), ClipInfo{
		ID:          "vel-ramp",
		Name:        "Velocity Ramp",
		Description: "Middle C with velocity stepping 1→127→1 — loop it while dragging the velocity curve to hear each layer boundary move.",
		Beats:       float64(len(vels)) * 0.5,
		RefBPM:      100,
	}
}

// sustainChord: a Cmaj9 voicing held 8 beats, then 8 beats of silence.
// The long tail-into-silence is the point: reverb decay, mastering-comp
// release, and limiter ceiling are all easiest to judge against nothing.
func sustainChord() ([]TimedEvent, ClipInfo) {
	var evs []TimedEvent
	for _, n := range []byte{48, 60, 64, 67, 71, 74} {
		evs = addNote(evs, 0, 8, n, 96)
	}
	return sortEvents(evs), ClipInfo{
		ID:          "sustain-chord",
		Name:        "Sustain Chord",
		Description: "Cmaj9 held 8 beats then 8 beats of silence — judge reverb tail, mastering comp, and limiter ceiling against the gap.",
		PolyOnly:    true,
		Beats:       16,
		RefBPM:      100,
	}
}

// arp: a one-bar Am7 arpeggio in sixteenths. Neutral, melodic material
// for general patch character, envelope feel, and quick patch A/B.
func arp() ([]TimedEvent, ClipInfo) {
	cycle := []byte{57, 60, 64, 67, 69, 67, 64, 60}
	var evs []TimedEvent
	for i := 0; i < 16; i++ {
		start := float64(i) * 0.25
		evs = addNote(evs, start, start+0.25, cycle[i%len(cycle)], 88)
	}
	return sortEvents(evs), ClipInfo{
		ID:          "arp",
		Name:        "Arpeggio",
		Description: "One-bar Am7 arpeggio in 16ths — general patch character, envelope feel, patch A/B.",
		Beats:       4,
		RefBPM:      100,
	}
}

// bassRiff: a two-bar riff around A1, mono-friendly by design so it
// works on the mono-legato native engine — the clip to sweep the native
// synth cutoff over. Notes hold until 0.05 beats before the next so
// legato transitions are audible without overlap.
func bassRiff() ([]TimedEvent, ClipInfo) {
	notes := []byte{33, 33, 36, 33, 40, 38, 36, 31}
	onsets := []float64{0, 0.75, 1, 1.75, 2, 2.75, 3, 3.5}
	var evs []TimedEvent
	for bar := 0; bar < 2; bar++ {
		shift := float64(bar) * 4
		for i, n := range notes {
			next := shift + 4 // last note of the bar ends just before the next bar (or the loop wrap)
			if i+1 < len(onsets) {
				next = shift + onsets[i+1]
			}
			evs = addNote(evs, shift+onsets[i], next-0.05, n, 100)
		}
	}
	return sortEvents(evs), ClipInfo{
		ID:          "bass-riff",
		Name:        "Bass Riff",
		Description: "Two-bar low-register riff, mono-friendly — sweep the native synth cutoff over it.",
		Beats:       8,
		RefBPM:      100,
	}
}

// chromatic: every note 21..108 ascending at fixed velocity. Fixed
// velocity is deliberate: any level change you hear between adjacent
// notes is a sample-layer seam, per-register imbalance, or high-note
// aliasing — not the material.
func chromatic() ([]TimedEvent, ClipInfo) {
	var evs []TimedEvent
	i := 0
	for n := byte(21); n <= 108; n++ {
		start := float64(i) * 0.25
		evs = addNote(evs, start, start+0.125, n, 80)
		i++
	}
	return sortEvents(evs), ClipInfo{
		ID:          "chromatic",
		Name:        "Chromatic Walk",
		Description: "Every note 21–108 at fixed velocity — expose sample-layer seams, register balance, and high-note aliasing.",
		Beats:       float64(i) * 0.25,
		RefBPM:      100,
	}
}

// staccato: one note repeated with the gap shrinking (1 → 0.5 → 0.25 →
// 0.125 beats, eight hits each), notes 40% of the gap. The accelerating
// transient train makes attack/release settings and compressor pumping
// obvious.
func staccato() ([]TimedEvent, ClipInfo) {
	var evs []TimedEvent
	beat := 0.0
	for _, interval := range []float64{1, 0.5, 0.25, 0.125} {
		for hit := 0; hit < 8; hit++ {
			evs = addNote(evs, beat, beat+interval*0.4, 72, 96)
			beat += interval
		}
	}
	return sortEvents(evs), ClipInfo{
		ID:          "staccato",
		Name:        "Staccato Accelerando",
		Description: "Short notes with shrinking gaps — hear attack/release transients and compressor pumping.",
		Beats:       beat,
		RefBPM:      100,
	}
}

// burstChords is a fixed random-LOOKING table (hand-scattered voicings,
// no RNG) so the polyphony/CPU stress test is reproducible run to run.
var burstChords = [8][5]byte{
	{52, 59, 64, 71, 79},
	{48, 55, 63, 70, 84},
	{50, 57, 66, 74, 81},
	{45, 53, 62, 69, 77},
	{47, 56, 65, 72, 83},
	{49, 58, 61, 76, 86},
	{44, 54, 67, 73, 80},
	{51, 60, 68, 75, 88},
}

// burst: a dense five-note chord on every beat 0..7, each held 0.9
// beats. Polyphony stress, CPU headroom, and limiter behavior under
// sustained density.
func burst() ([]TimedEvent, ClipInfo) {
	var evs []TimedEvent
	for b, chord := range burstChords {
		start := float64(b)
		for _, n := range chord {
			evs = addNote(evs, start, start+0.9, n, 110)
		}
	}
	return sortEvents(evs), ClipInfo{
		ID:          "burst",
		Name:        "Chord Burst",
		Description: "Dense five-note chords every beat — polyphony stress, CPU headroom, limiter behavior.",
		PolyOnly:    true,
		Beats:       8,
		RefBPM:      100,
	}
}
