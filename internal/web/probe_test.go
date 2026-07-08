package web

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/midiprobe"
)

// ---- nil-Probe guard (matches TestPlayerEndpointsNilPlayer's pattern) ----

func TestProbeEndpointsNilProbe(t *testing.T) {
	f := newFixture(t, nil) // Deps.Probe left nil
	for _, tc := range []struct{ method, path string }{
		{"GET", "/api/probe/ports"},
		{"GET", "/api/probe/status"},
		{"POST", "/api/probe/connect"},
		{"POST", "/api/probe/disconnect"},
		{"GET", "/api/probe/events"},
		{"POST", "/api/probe/label"},
		{"POST", "/api/probe/identity"},
		{"POST", "/api/probe/send"},
		{"GET", "/api/probe/export"},
	} {
		rec := f.do(t, tc.method, tc.path, nil)
		wantStatus(t, rec, http.StatusServiceUnavailable)
	}
}

// newProbeFixture wires a real *midiprobe.Session (never Start'ed unless a
// test explicitly connects it) — mirrors newTestPlayer's "real domain
// object with harmless behavior," not a faked interface, since
// web.Deps.Probe is a concrete *midiprobe.Session like Deps.Player.
func newProbeFixture(t *testing.T) (*fixture, *midiprobe.Session) {
	t.Helper()
	probe := midiprobe.NewSession(nil, nil)
	f := newFixture(t, func(d *Deps) { d.Probe = probe })
	return f, probe
}

func TestProbePortsAndStatusWithNoConnection(t *testing.T) {
	f, _ := newProbeFixture(t)

	rec := f.do(t, "GET", "/api/probe/ports", nil)
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if _, ok := m["ins"]; !ok {
		t.Errorf("ports response missing ins: %v", m)
	}
	if _, ok := m["outs"]; !ok {
		t.Errorf("ports response missing outs: %v", m)
	}

	rec = f.do(t, "GET", "/api/probe/status", nil)
	wantStatus(t, rec, http.StatusOK)
	m = decodeBody(t, rec)
	if m["active"] != false {
		t.Errorf("status active = %v, want false before any connect", m["active"])
	}
}

func TestProbeConnectValidation(t *testing.T) {
	f, _ := newProbeFixture(t)

	wantStatus(t, f.do(t, "POST", "/api/probe/connect", `{"inPort":`), http.StatusBadRequest)
	wantStatus(t, f.do(t, "POST", "/api/probe/connect", map[string]any{"inPort": "", "outPort": ""}), http.StatusBadRequest)
	// A port name that plainly doesn't exist -> 404 (ErrPortNotFound),
	// exercised without needing any real MIDI hardware present or absent.
	rec := f.do(t, "POST", "/api/probe/connect", map[string]any{
		"inPort": "definitely-not-a-real-port-xyz", "outPort": "definitely-not-a-real-port-xyz",
	})
	wantStatus(t, rec, http.StatusNotFound)
}

func TestProbeNotRunningEndpoints(t *testing.T) {
	f, _ := newProbeFixture(t)

	wantStatus(t, f.do(t, "POST", "/api/probe/disconnect", nil), http.StatusConflict)
	wantStatus(t, f.do(t, "POST", "/api/probe/label", map[string]any{"label": "Knob 1"}), http.StatusConflict)
	wantStatus(t, f.do(t, "POST", "/api/probe/identity", map[string]any{}), http.StatusConflict)
	wantStatus(t, f.do(t, "POST", "/api/probe/send", map[string]any{"hex": "F0F7"}), http.StatusConflict)
}

func TestProbeLabelValidation(t *testing.T) {
	f, _ := newProbeFixture(t)
	wantStatus(t, f.do(t, "POST", "/api/probe/label", map[string]any{"label": ""}), http.StatusBadRequest)
	wantStatus(t, f.do(t, "POST", "/api/probe/label", `{"label":`), http.StatusBadRequest)
}

func TestProbeIdentityChannelValidation(t *testing.T) {
	f, _ := newProbeFixture(t)
	wantStatus(t, f.do(t, "POST", "/api/probe/identity", map[string]any{"channel": 200}), http.StatusBadRequest)
	wantStatus(t, f.do(t, "POST", "/api/probe/identity", `{"channel":`), http.StatusBadRequest)
}

func TestProbeSendHexValidation(t *testing.T) {
	f, _ := newProbeFixture(t)
	wantStatus(t, f.do(t, "POST", "/api/probe/send", map[string]any{"hex": "not hex zz"}), http.StatusBadRequest)
	wantStatus(t, f.do(t, "POST", "/api/probe/send", map[string]any{"hex": ""}), http.StatusBadRequest)
	// Whitespace-separated hex bytes must be accepted (the natural way a
	// user pastes SysEx bytes) — this fails with 409 (not running) rather
	// than 400, proving the hex itself parsed fine.
	rec := f.do(t, "POST", "/api/probe/send", map[string]any{"hex": "F0 00 20 29 F7"})
	wantStatus(t, rec, http.StatusConflict)
}

func TestProbeEventsSinceParamValidation(t *testing.T) {
	f, _ := newProbeFixture(t)
	wantStatus(t, f.do(t, "GET", "/api/probe/events?since=notanumber", nil), http.StatusBadRequest)
	rec := f.do(t, "GET", "/api/probe/events", nil)
	wantStatus(t, rec, http.StatusOK)
}

func TestProbeExportNothingCaptured(t *testing.T) {
	f, _ := newProbeFixture(t)
	wantStatus(t, f.do(t, "GET", "/api/probe/export", nil), http.StatusNotFound)
}

// TestProbeFullLoopbackThroughHTTP drives the ENTIRE HTTP surface against a
// real MIDI connection, using whatever software loopback port the host
// exposes (see internal/midiprobe's TestRealConnLoopback for why ALSA's
// "Midi Through" qualifies on Linux). Skip-guarded on any failure so it
// never blocks CI on a machine without one (e.g. macOS, where CoreMIDI has
// no built-in equivalent) — it only adds confidence where a loopback
// exists.
func TestProbeFullLoopbackThroughHTTP(t *testing.T) {
	f, probe := newProbeFixture(t)

	ins, outs := probe.ListPorts()
	if len(ins) == 0 || len(outs) == 0 {
		t.Skipf("no MIDI ports to test against (ins=%v outs=%v)", ins, outs)
	}
	name := ""
	for _, in := range ins {
		for _, out := range outs {
			if in == out {
				name = in
			}
		}
	}
	if name == "" {
		t.Skip("no self-loopback port name found")
	}

	rec := f.do(t, "POST", "/api/probe/connect", map[string]any{"inPort": name, "outPort": name})
	if rec.Code != http.StatusOK {
		t.Skipf("connect to %q failed: %d %s", name, rec.Code, rec.Body.String())
	}
	m := decodeBody(t, rec)
	if m["active"] != true {
		t.Fatalf("status after connect = %v", m)
	}

	if rec := f.do(t, "POST", "/api/probe/label", map[string]any{"label": "Test Knob", "windowMs": 500}); rec.Code != http.StatusOK {
		t.Fatalf("label: %d %s", rec.Code, rec.Body.String())
	}

	// Send a CC to ourselves via the raw-send endpoint; the loopback
	// should deliver it back to our own listener within the label window.
	if rec := f.do(t, "POST", "/api/probe/send", map[string]any{"hex": "B0144C"}); rec.Code != http.StatusOK {
		t.Fatalf("send: %d %s", rec.Code, rec.Body.String())
	}

	var events []midiprobe.Event
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		rec := f.do(t, "GET", "/api/probe/events", nil)
		wantStatus(t, rec, http.StatusOK)
		events = probe.Events(0)
		if len(events) > 0 {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if len(events) == 0 {
		t.Skip("no loopback event observed within timeout — port may not actually self-loop")
	}
	if events[0].Label != "Test Knob" {
		t.Errorf("captured event label = %q, want %q", events[0].Label, "Test Knob")
	}

	rec = f.do(t, "GET", "/api/probe/export", nil)
	wantStatus(t, rec, http.StatusOK)
	if cd := rec.Header().Get("Content-Disposition"); cd == "" || cd[:10] != "attachment" {
		t.Errorf("export Content-Disposition = %q, want an attachment", cd)
	}
	var profile midiprobe.DeviceProfile
	if err := json.Unmarshal(rec.Body.Bytes(), &profile); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if len(profile.Events) == 0 {
		t.Error("exported profile has no events")
	}

	wantStatus(t, f.do(t, "POST", "/api/probe/disconnect", nil), http.StatusOK)
}
