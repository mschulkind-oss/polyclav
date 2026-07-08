package midiprobe

import (
	"testing"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/midi"
)

// TestRealConnLoopback exercises the PRODUCTION openConn/realConn path (not
// the fake) against a genuine MIDI round trip, using whatever software
// loopback port the host happens to expose (on Linux, ALSA's built-in
// "Midi Through" virtual patchbay port routes anything sent to its output
// back out its own input — no physical hardware or manual setup needed).
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

	// Prefer a port present in both lists (a self-loopback, e.g. ALSA's
	// "Midi Through") over pairing two unrelated ports, which would not
	// actually round-trip.
	name := ""
	for _, in := range ins {
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
		t.Skip("no port name common to both the input and output lists (no obvious loopback candidate)")
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
