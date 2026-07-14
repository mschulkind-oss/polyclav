package midiprobe

import (
	"strings"
	"testing"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/midi"
)

// TestRealConnLoopback exercises the PRODUCTION openConn/realConn path (not
// the fake) against a genuine MIDI round trip, using ALSA's built-in "Midi
// Through" virtual patchbay port on Linux (routes anything sent to its
// output back out its own input — no physical hardware or manual setup
// needed) — the ONLY port name this test will ever touch.
//
// This is deliberately best-effort: it is the ONLY test in this package
// that opens a real MIDI port, and environments differ (a minimal
// container might lack the ALSA sequencer's virtual through client
// entirely; macOS/CoreMIDI has no equivalent built-in loopback). Every
// failure path skips rather than fails, so this never blocks CI on a
// machine without a working loopback — it only adds confidence where one
// exists. It specifically closes the gap flagged as the biggest residual
// risk in the port design: whether drivers.ListenConfig{SysEx: true}
// genuinely delivers raw SysEx bytes end-to-end through rtmidi/ALSA, not
// just in the fake.
//
// Matching by name (not just "any port present in both the in and out
// lists") is load-bearing, not cosmetic: a REAL MIDI device (a keyboard,
// a mixer, anything USB-MIDI-class-compliant) also normally exposes both
// an in and an out port, so "bidirectional" alone does not mean "software
// loopback." internal/web's equivalent test (probe_test.go) once used
// exactly that looser check with no name filter and a missing early
// break, and on a machine with real hardware attached it silently
// connected to and sent a live message to whatever real device happened
// to sort last in the port list — confirmed 2026-07-11, audible output on
// a real Launchkey/XR18/CASIO setup during a routine `go test ./...` run.
// Never loosen this back to a bare "in == out" match.
//
// A second, distinct leak: even targeting the exact-right "Midi Through"
// port, a shared-ALSA-bus dev jail can have a real polyclav daemon on the
// host also listening to it (default config treats every non-DAW port as
// an ordinary keyboard) — that daemon renders this test's NoteOn/SysEx as
// if someone played it. Fixed at the source, not in this test:
// internal/midi.looksLikeLoopbackPort excludes "Midi Through" from the
// default note-sending set, the same way looksLikeDAWPort excludes a
// Launchkey's DAW port. Confirmed 2026-07.
func TestRealConnLoopback(t *testing.T) {
	ins, err := midi.PortNames()
	if err != nil {
		t.Skipf("enumerate midi ins: %v", err)
	}
	outs, err := midi.OutPortNames()
	if err != nil {
		t.Skipf("enumerate midi outs: %v", err)
	}
	if len(ins) == 0 || len(outs) == 0 {
		t.Skipf("no MIDI ports available to test against (ins=%v outs=%v)", ins, outs)
	}

	name := ""
	for _, in := range ins {
		if !strings.Contains(strings.ToLower(in), "midi through") {
			continue
		}
		for _, out := range outs {
			if in == out {
				name = in
				break
			}
		}
		if name != "" {
			break
		}
	}
	if name == "" {
		t.Skip(`no port named "Midi Through" found in both the input and output lists -- ` +
			"this test only ever targets that specific known-safe virtual loopback, never real hardware")
	}

	conn, err := openConn(name, name)
	if err != nil {
		t.Skipf("could not open %q for loopback: %v", name, err)
	}
	defer conn.Close()

	received := make(chan []byte, 8)
	stop, err := conn.Listen(func(raw []byte) {
		received <- append([]byte(nil), raw...)
	})
	if err != nil {
		t.Skipf("could not listen on %q: %v", name, err)
	}
	defer stop()

	// Give the ALSA subscription a moment to establish before sending —
	// there is an inherent tiny race between Listen registering and the
	// port becoming a live subscriber.
	time.Sleep(50 * time.Millisecond)

	t.Run("channel-voice message round-trips", func(t *testing.T) {
		noteOn := []byte{0x90, 60, 100}
		if err := conn.Send(noteOn); err != nil {
			t.Skipf("send failed: %v", err)
		}
		select {
		case got := <-received:
			if string(got) != string(noteOn) {
				t.Errorf("loopback returned % x, want % x", got, noteOn)
			}
		case <-time.After(2 * time.Second):
			t.Skip("no loopback observed within timeout — port may not actually self-loop")
		}
	})

	t.Run("raw SysEx round-trips with ListenConfig SysEx true", func(t *testing.T) {
		sysex := []byte{0xF0, 0x00, 0x20, 0x6B, 0x01, 0x02, 0xF7}
		if err := conn.Send(sysex); err != nil {
			t.Skipf("send failed: %v", err)
		}
		select {
		case got := <-received:
			if string(got) != string(sysex) {
				t.Errorf("loopback returned % x, want % x (SysEx bytes must round-trip verbatim)", got, sysex)
			}
		case <-time.After(2 * time.Second):
			t.Skip("no SysEx loopback observed within timeout")
		}
	})
}
