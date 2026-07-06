package player

import (
	"bytes"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"

	gomidi "gitlab.com/gomidi/midi/v2"
	"gitlab.com/gomidi/midi/v2/smf"

	"github.com/mschulkind-oss/polyclav/internal/midi"
)

// writeSMF assembles an SMF from pre-built tracks and writes it to
// dir/name. Tests author files through the same gomidi smf package the
// loader reads with, so the fixtures exercise the real wire format
// (header, running status, VLQ deltas) rather than hand-rolled bytes.
func writeSMF(t *testing.T, dir, name string, format smf.TimeFormat, tracks ...smf.Track) string {
	t.Helper()
	s := smf.New()
	s.TimeFormat = format
	for _, tr := range tracks {
		tr.Close(0)
		if err := s.Add(tr); err != nil {
			t.Fatalf("smf.Add: %v", err)
		}
	}
	path := filepath.Join(dir, name)
	if err := s.WriteFile(path); err != nil {
		t.Fatalf("WriteFile(%s): %v", name, err)
	}
	return path
}

func wantEvents(t *testing.T, got, want []TimedEvent) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d events, want %d\ngot:  %+v\nwant: %+v", len(got), len(want), got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, got[i], want[i])
		}
	}
}

// TestLoadSMFTwoNotes is the basic contract: two sequential notes,
// authored on channel 3 with tpq 480, come back beat-accurate,
// paired, re-stamped to channel 0, with metadata filled per spec.
func TestLoadSMFTwoNotes(t *testing.T) {
	dir := t.TempDir()
	var tr smf.Track
	tr.Add(0, smf.MetaTempo(90))
	tr.Add(0, gomidi.NoteOn(3, 60, 100))
	tr.Add(480, gomidi.NoteOff(3, 60))
	tr.Add(0, gomidi.NoteOn(3, 64, 80))
	tr.Add(480, gomidi.NoteOff(3, 64))
	path := writeSMF(t, dir, "two.mid", smf.MetricTicks(480), tr)

	cd, err := loadSMFClip(path)
	if err != nil {
		t.Fatal(err)
	}

	wantEvents(t, cd.events, []TimedEvent{
		{Beat: 0, Ev: midi.Event{Kind: midi.NoteOn, Note: 60, Vel: 100}},
		{Beat: 1, Ev: midi.Event{Kind: midi.NoteOff, Note: 60}},
		{Beat: 1, Ev: midi.Event{Kind: midi.NoteOn, Note: 64, Vel: 80}},
		{Beat: 2, Ev: midi.Event{Kind: midi.NoteOff, Note: 64}},
	})

	info := cd.info
	if info.ID != "file:two" {
		t.Errorf("ID = %q, want %q", info.ID, "file:two")
	}
	if info.Name != "two.mid" {
		t.Errorf("Name = %q, want %q", info.Name, "two.mid")
	}
	if info.Description != "user clip" {
		t.Errorf("Description = %q, want %q", info.Description, "user clip")
	}
	if info.PolyOnly {
		t.Error("PolyOnly = true, want false")
	}
	if info.Beats != 2 {
		t.Errorf("Beats = %v, want 2", info.Beats)
	}
	// MetaTempo stores microseconds-per-quarter (integer), so the BPM
	// round-trips only approximately.
	if math.Abs(info.RefBPM-90) > 0.01 {
		t.Errorf("RefBPM = %v, want ~90", info.RefBPM)
	}
}

// TestLoadSMFMultiTrackMerge: notes from two tracks interleave on the
// merged beat timeline, with the off-before-on rule at shared beats.
func TestLoadSMFMultiTrackMerge(t *testing.T) {
	dir := t.TempDir()
	var t0 smf.Track
	t0.Add(0, gomidi.NoteOn(0, 60, 100))
	t0.Add(480, gomidi.NoteOff(0, 60)) // beat 1
	t0.Add(480, gomidi.NoteOn(0, 62, 100))
	t0.Add(480, gomidi.NoteOff(0, 62)) // beat 3
	var t1 smf.Track
	t1.Add(480, gomidi.NoteOn(1, 64, 100)) // beat 1
	t1.Add(480, gomidi.NoteOff(1, 64))     // beat 2
	path := writeSMF(t, dir, "merge.mid", smf.MetricTicks(480), t0, t1)

	cd, err := loadSMFClip(path)
	if err != nil {
		t.Fatal(err)
	}
	wantEvents(t, cd.events, []TimedEvent{
		{Beat: 0, Ev: midi.Event{Kind: midi.NoteOn, Note: 60, Vel: 100}},
		{Beat: 1, Ev: midi.Event{Kind: midi.NoteOff, Note: 60}},
		{Beat: 1, Ev: midi.Event{Kind: midi.NoteOn, Note: 64, Vel: 100}},
		{Beat: 2, Ev: midi.Event{Kind: midi.NoteOff, Note: 64}},
		{Beat: 2, Ev: midi.Event{Kind: midi.NoteOn, Note: 62, Vel: 100}},
		{Beat: 3, Ev: midi.Event{Kind: midi.NoteOff, Note: 62}},
	})
	if cd.info.Beats != 3 {
		t.Errorf("Beats = %v, want 3", cd.info.Beats)
	}
}

// TestLoadSMFVelZeroIsNoteOff: the running-status idiom (NoteOn with
// velocity 0) parses as a NoteOff, so the note is properly paired and
// nothing dangles.
func TestLoadSMFVelZeroIsNoteOff(t *testing.T) {
	dir := t.TempDir()
	var tr smf.Track
	tr.Add(0, gomidi.NoteOn(2, 60, 100))
	tr.Add(480, gomidi.NoteOn(2, 60, 0))
	path := writeSMF(t, dir, "velzero.mid", smf.MetricTicks(480), tr)

	cd, err := loadSMFClip(path)
	if err != nil {
		t.Fatal(err)
	}
	wantEvents(t, cd.events, []TimedEvent{
		{Beat: 0, Ev: midi.Event{Kind: midi.NoteOn, Note: 60, Vel: 100}},
		{Beat: 1, Ev: midi.Event{Kind: midi.NoteOff, Note: 60}},
	})
	if cd.info.Beats != 1 {
		t.Errorf("Beats = %v, want 1", cd.info.Beats)
	}
}

// TestLoadSMFDanglingNoteOn: a NoteOn never closed in the file gets a
// synthesized NoteOff at the final beat, so loop wraps and Stop can
// never leak it.
func TestLoadSMFDanglingNoteOn(t *testing.T) {
	dir := t.TempDir()
	var tr smf.Track
	tr.Add(0, gomidi.NoteOn(0, 60, 100)) // never released in the file
	tr.Add(480, gomidi.NoteOn(0, 64, 90))
	tr.Add(480, gomidi.NoteOff(0, 64)) // beat 2 = final beat
	path := writeSMF(t, dir, "dangling.mid", smf.MetricTicks(480), tr)

	cd, err := loadSMFClip(path)
	if err != nil {
		t.Fatal(err)
	}
	wantEvents(t, cd.events, []TimedEvent{
		{Beat: 0, Ev: midi.Event{Kind: midi.NoteOn, Note: 60, Vel: 100}},
		{Beat: 1, Ev: midi.Event{Kind: midi.NoteOn, Note: 64, Vel: 90}},
		{Beat: 2, Ev: midi.Event{Kind: midi.NoteOff, Note: 64}},
		{Beat: 2, Ev: midi.Event{Kind: midi.NoteOff, Note: 60}}, // synthesized
	})
	if cd.info.Beats != 2 {
		t.Errorf("Beats = %v, want 2", cd.info.Beats)
	}
}

// TestLoadSMFBeatsMinimum: a degenerate file whose last event sits at
// beat 0 still reports Beats = 1 so the scheduler has a real loop
// length to work with.
func TestLoadSMFBeatsMinimum(t *testing.T) {
	dir := t.TempDir()
	var tr smf.Track
	tr.Add(0, gomidi.NoteOn(0, 60, 100))
	path := writeSMF(t, dir, "tiny.mid", smf.MetricTicks(480), tr)

	cd, err := loadSMFClip(path)
	if err != nil {
		t.Fatal(err)
	}
	wantEvents(t, cd.events, []TimedEvent{
		{Beat: 0, Ev: midi.Event{Kind: midi.NoteOn, Note: 60, Vel: 100}},
		{Beat: 0, Ev: midi.Event{Kind: midi.NoteOff, Note: 60}}, // synthesized at final beat
	})
	if cd.info.Beats != 1 {
		t.Errorf("Beats = %v, want 1 (minimum)", cd.info.Beats)
	}
}

// TestLoadSMFTempo covers the RefBPM rules: first tempo meta wins,
// later changes are ignored, absence means the SMF default of 120.
func TestLoadSMFTempo(t *testing.T) {
	dir := t.TempDir()

	t.Run("first-tempo-wins", func(t *testing.T) {
		var tr smf.Track
		tr.Add(0, smf.MetaTempo(100))
		tr.Add(0, gomidi.NoteOn(0, 60, 100))
		tr.Add(240, smf.MetaTempo(180)) // later change: ignored
		tr.Add(240, gomidi.NoteOff(0, 60))
		path := writeSMF(t, dir, "tempo.mid", smf.MetricTicks(480), tr)
		cd, err := loadSMFClip(path)
		if err != nil {
			t.Fatal(err)
		}
		if math.Abs(cd.info.RefBPM-100) > 0.01 {
			t.Errorf("RefBPM = %v, want ~100 (first tempo event)", cd.info.RefBPM)
		}
	})

	t.Run("no-tempo-defaults-120", func(t *testing.T) {
		var tr smf.Track
		tr.Add(0, gomidi.NoteOn(0, 60, 100))
		tr.Add(480, gomidi.NoteOff(0, 60))
		path := writeSMF(t, dir, "notempo.mid", smf.MetricTicks(480), tr)
		cd, err := loadSMFClip(path)
		if err != nil {
			t.Fatal(err)
		}
		if cd.info.RefBPM != 120 {
			t.Errorf("RefBPM = %v, want 120 (SMF default)", cd.info.RefBPM)
		}
	})
}

// TestLoadSMFSMPTERejected: SMPTE-timed files have no ticks-per-quarter
// to convert beats from — the loader must refuse them clearly.
func TestLoadSMFSMPTERejected(t *testing.T) {
	dir := t.TempDir()
	var tr smf.Track
	tr.Add(0, gomidi.NoteOn(0, 60, 100))
	tr.Add(25, gomidi.NoteOff(0, 60))
	path := writeSMF(t, dir, "smpte.mid", smf.SMPTE25(40), tr)

	_, err := loadSMFClip(path)
	if err == nil {
		t.Fatal("loadSMFClip(SMPTE file) returned nil error")
	}
	if !strings.Contains(err.Error(), "SMPTE") {
		t.Errorf("error %q does not mention SMPTE", err)
	}
}

// TestLoadUserClipsScan is the directory contract: *.mid and *.midi
// load in name order after the built-ins, unparseable files are
// warned about and skipped without failing the scan, and non-MIDI
// files and subdirectories are ignored.
func TestLoadUserClipsScan(t *testing.T) {
	dir := t.TempDir()
	var trB smf.Track
	trB.Add(0, gomidi.NoteOn(0, 62, 100))
	trB.Add(480, gomidi.NoteOff(0, 62))
	writeSMF(t, dir, "b.mid", smf.MetricTicks(480), trB)
	var trA smf.Track
	trA.Add(0, gomidi.NoteOn(0, 60, 100))
	trA.Add(480, gomidi.NoteOff(0, 60))
	writeSMF(t, dir, "a.midi", smf.MetricTicks(480), trA)
	if err := os.WriteFile(filepath.Join(dir, "junk.mid"), []byte("not a midi file"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "readme.txt"), []byte("ignored"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(filepath.Join(dir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	var trSub smf.Track
	trSub.Add(0, gomidi.NoteOn(0, 70, 100))
	trSub.Add(480, gomidi.NoteOff(0, 70))
	writeSMF(t, filepath.Join(dir, "sub"), "nested.mid", smf.MetricTicks(480), trSub)

	var logBuf bytes.Buffer
	s := &recSink{}
	p := New(slog.New(slog.NewTextHandler(&logBuf, nil)), s.push)
	builtins := len(p.Clips())

	loaded, err := p.LoadUserClips(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 2 {
		t.Fatalf("loaded = %d, want 2", loaded)
	}

	clips := p.Clips()
	if len(clips) != builtins+2 {
		t.Fatalf("Clips() has %d entries, want %d", len(clips), builtins+2)
	}
	// Built-ins keep their positions; files follow, sorted by name.
	if clips[builtins].ID != "file:a" || clips[builtins+1].ID != "file:b" {
		t.Errorf("file clip order = %q, %q; want file:a, file:b", clips[builtins].ID, clips[builtins+1].ID)
	}
	if !strings.Contains(logBuf.String(), "junk.mid") {
		t.Errorf("no warning logged for junk.mid; log:\n%s", logBuf.String())
	}
}

// TestLoadUserClipsMissingDir: a fresh install has no clips dir — that
// is not an error, just zero clips.
func TestLoadUserClipsMissingDir(t *testing.T) {
	p, _ := newTestPlayer()
	loaded, err := p.LoadUserClips(filepath.Join(t.TempDir(), "does-not-exist"))
	if err != nil {
		t.Fatalf("LoadUserClips(missing dir) error = %v, want nil", err)
	}
	if loaded != 0 {
		t.Fatalf("loaded = %d, want 0", loaded)
	}
}

// TestLoadUserClipsDuplicateID: dup.mid and dup.midi both map to ID
// "file:dup" — the later file (by name order) wins, the clip list
// keeps a single entry, and a warning is logged.
func TestLoadUserClipsDuplicateID(t *testing.T) {
	dir := t.TempDir()
	var tr1 smf.Track
	tr1.Add(0, gomidi.NoteOn(0, 60, 100))
	tr1.Add(480, gomidi.NoteOff(0, 60))
	writeSMF(t, dir, "dup.mid", smf.MetricTicks(480), tr1)
	var tr2 smf.Track
	tr2.Add(0, gomidi.NoteOn(0, 72, 100))
	tr2.Add(480, gomidi.NoteOff(0, 72))
	writeSMF(t, dir, "dup.midi", smf.MetricTicks(480), tr2)

	var logBuf bytes.Buffer
	s := &recSink{}
	p := New(slog.New(slog.NewTextHandler(&logBuf, nil)), s.push)
	loaded, err := p.LoadUserClips(dir)
	if err != nil {
		t.Fatal(err)
	}
	if loaded != 2 {
		t.Fatalf("loaded = %d, want 2 (both files parsed)", loaded)
	}

	n := 0
	for _, c := range p.Clips() {
		if c.ID == "file:dup" {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("Clips() has %d file:dup entries, want 1", n)
	}
	// Last wins: dup.midi sorts after dup.mid, so note 72 is registered.
	if got := p.byID["file:dup"].events[0].Ev.Note; got != 72 {
		t.Errorf("registered clip plays note %d, want 72 (dup.midi, last wins)", got)
	}
	if !strings.Contains(logBuf.String(), "file:dup") {
		t.Errorf("no duplicate-ID warning logged; log:\n%s", logBuf.String())
	}
}

// TestPlayUserClipEndToEnd loads a file clip and plays it to natural
// end through a recording sink at max tempo: the sink must receive
// exactly the file's note stream (channel-0, in order, balanced), and
// the transport must come back stopped.
func TestPlayUserClipEndToEnd(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var tr smf.Track
	tr.Add(0, smf.MetaTempo(240))
	tr.Add(0, gomidi.NoteOn(5, 60, 100))
	tr.Add(96, gomidi.NoteOff(5, 60))
	tr.Add(0, gomidi.NoteOn(5, 64, 90))
	tr.Add(96, gomidi.NoteOff(5, 64))
	writeSMF(t, dir, "song.mid", smf.MetricTicks(96), tr)

	p, s := newTestPlayer()
	loaded, err := p.LoadUserClips(dir)
	if err != nil || loaded != 1 {
		t.Fatalf("LoadUserClips = (%d, %v), want (1, nil)", loaded, err)
	}
	states := make(chan State, 32)
	p.OnChange(func(st State) { states <- st })

	// ~240 BPM reference at 2.0x → 2 beats ≈ 250 ms of wall time.
	if err := p.Play("file:song", false, 2.0); err != nil {
		t.Fatal(err)
	}
	if st := recvState(t, states, "play state"); !st.Playing || st.ClipID != "file:song" {
		t.Fatalf("play OnChange = %+v, want playing file:song", st)
	}
	if st := recvState(t, states, "natural end"); st.Playing {
		t.Fatalf("natural-end OnChange still playing: %+v", st)
	}

	want := []midi.Event{
		{Kind: midi.NoteOn, Note: 60, Vel: 100},
		{Kind: midi.NoteOff, Note: 60},
		{Kind: midi.NoteOn, Note: 64, Vel: 90},
		{Kind: midi.NoteOff, Note: 64},
	}
	got := s.events()
	if len(got) != len(want) {
		t.Fatalf("sink received %d events, want %d: %+v", len(got), len(want), got)
	}
	for i, ev := range got {
		if ev != want[i] {
			t.Fatalf("event %d = %+v, want %+v", i, ev, want[i])
		}
	}
}
