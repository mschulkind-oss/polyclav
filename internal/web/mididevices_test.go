package web

import (
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/config"
)

// fakeMIDIDevices implements MIDIDevices with an in-memory ignore list —
// no real rtmidi/hardware needed. GET's port enumeration is injected
// separately via Deps.MIDIPortLister (see newFixture's mod callback in
// each test below), so these tests never touch a real ALSA sequencer /
// CoreMIDI client -- doing so is exactly what broke this suite on a
// GitHub Actions runner with no ALSA sequencer device at all (works in
// a dev jail with real hardware access, 500s in plain CI).
type fakeMIDIDevices struct {
	mu     sync.Mutex
	match  string
	ignore []string
}

func (f *fakeMIDIDevices) Match() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.match
}

func (f *fakeMIDIDevices) Ignore() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.ignore...)
}

func (f *fakeMIDIDevices) SetIgnore(names []string) {
	f.mu.Lock()
	f.ignore = append([]string(nil), names...)
	f.mu.Unlock()
}

// newMIDIConfigFixture is newConfigFixture plus a fakeMIDIDevices wired
// into Deps.MIDIDevices, mirroring newConfigFixture's role for velocity.
func newMIDIConfigFixture(t *testing.T, content string) (*fixture, string, *fakeMIDIDevices) {
	t.Helper()
	md := &fakeMIDIDevices{}
	f, path := newConfigFixtureWith(t, content, func(d *Deps) { d.MIDIDevices = md })
	return f, path, md
}

func TestMIDIDevicesGetUnavailableWithoutDeps(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "GET", "/api/midi/devices", nil)
	wantStatus(t, rec, http.StatusServiceUnavailable)
}

func TestMIDIDevicesGetReportsMatch(t *testing.T) {
	// Empty Match (the default mode) is what actually exercises DAW-port
	// exclusion -- an explicit Match (e.g. "yamaha") deliberately bypasses
	// it (see classifyOne), so a non-matching DAW port there would show
	// as "restricted", not "daw". Assert the match field is reported
	// verbatim, and separately exercise DAW classification in default mode.
	md := &fakeMIDIDevices{match: "yamaha"}
	f := newFixture(t, func(d *Deps) {
		d.MIDIDevices = md
		d.MIDIPortLister = func() ([]string, error) {
			return []string{"Yamaha Keyboard MIDI In", "Some Other Synth"}, nil
		}
	})
	rec := f.do(t, "GET", "/api/midi/devices", nil)
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if m["match"] != "yamaha" {
		t.Errorf("expected match=yamaha, got %v", m)
	}
	devices, ok := m["devices"].([]any)
	if !ok || len(devices) != 2 {
		t.Fatalf("expected 2 devices, got %v", m["devices"])
	}
	first := devices[0].(map[string]any)
	if first["name"] != "Yamaha Keyboard MIDI In" || first["status"] != "notes" {
		t.Errorf("expected the Yamaha port classified as notes, got %v", first)
	}
	second := devices[1].(map[string]any)
	if second["name"] != "Some Other Synth" || second["status"] != "restricted" {
		t.Errorf("expected the non-matching port classified as restricted, got %v", second)
	}
}

// TestMIDIDevicesGetClassifiesDAWPortInDefaultMode covers the empty-Match
// mode's DAW-port exclusion specifically, complementing the explicit-Match
// case above.
func TestMIDIDevicesGetClassifiesDAWPortInDefaultMode(t *testing.T) {
	md := &fakeMIDIDevices{}
	f := newFixture(t, func(d *Deps) {
		d.MIDIDevices = md
		d.MIDIPortLister = func() ([]string, error) {
			return []string{"Yamaha Keyboard MIDI In", "Launchkey MK4 61 DAW In"}, nil
		}
	})
	rec := f.do(t, "GET", "/api/midi/devices", nil)
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	devices := m["devices"].([]any)
	second := devices[1].(map[string]any)
	if second["name"] != "Launchkey MK4 61 DAW In" || second["status"] != "daw" {
		t.Errorf("expected the DAW port classified as daw in default mode, got %v", second)
	}
}

// TestMIDIDevicesGetDegradesGracefullyOnEnumerationFailure pins the
// regression this fixes: no ALSA sequencer / CoreMIDI client available
// at all (not "zero ports", a hard enumeration error) must report an
// empty device list with 200, not a 500 -- matching
// internal/midiprobe's established graceful-degradation convention, and
// keeping the dashboard usable on a machine with no working MIDI
// subsystem.
func TestMIDIDevicesGetDegradesGracefullyOnEnumerationFailure(t *testing.T) {
	md := &fakeMIDIDevices{match: ""}
	f := newFixture(t, func(d *Deps) {
		d.MIDIDevices = md
		d.MIDIPortLister = func() ([]string, error) {
			return nil, errors.New("can't open default MIDI in: no ALSA sequencer")
		}
	})
	rec := f.do(t, "GET", "/api/midi/devices", nil)
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	devices, ok := m["devices"].([]any)
	if !ok || len(devices) != 0 {
		t.Errorf("expected an empty devices array, got %v", m["devices"])
	}
}

func TestMIDIDevicesPutUnavailableWithoutDeps(t *testing.T) {
	f := newFixture(t, nil)
	rec := f.do(t, "PUT", "/api/midi/devices", map[string]any{"ignore": []string{"x"}})
	wantStatus(t, rec, http.StatusServiceUnavailable)
}

func TestMIDIDevicesPutSessionOnlyAppliesLiveNoSave(t *testing.T) {
	md := &fakeMIDIDevices{}
	f := newFixture(t, func(d *Deps) { d.MIDIDevices = md })

	rec := f.do(t, "PUT", "/api/midi/devices", map[string]any{"ignore": []string{"Yamaha P-125"}})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if m["saved"] != false {
		t.Errorf("expected saved=false for a session-only PUT, got %v", m)
	}
	if got := md.Ignore(); len(got) != 1 || got[0] != "Yamaha P-125" {
		t.Errorf("SetIgnore must apply live regardless of save, got %v", got)
	}
}

func TestMIDIDevicesPutSaveNoConfigPath(t *testing.T) {
	md := &fakeMIDIDevices{}
	f := newFixture(t, func(d *Deps) { d.MIDIDevices = md })
	rec := f.do(t, "PUT", "/api/midi/devices", map[string]any{"ignore": []string{"x"}, "save": true})
	wantStatus(t, rec, http.StatusNotFound)
	if got := md.Ignore(); len(got) != 0 {
		t.Errorf("a failed save must not apply live either, got %v", got)
	}
}

func TestMIDIDevicesSaveAppendsNewMIDITable(t *testing.T) {
	// baseConfig ("[web]\nenabled = false\n") has no [midi] table at all.
	f, path, md := newMIDIConfigFixture(t, baseConfig)

	rec := f.do(t, "PUT", "/api/midi/devices", map[string]any{
		"ignore": []string{"Yamaha P-125"}, "save": true,
	})
	wantStatus(t, rec, http.StatusOK)
	m := decodeBody(t, rec)
	if m["saved"] != true {
		t.Errorf("expected saved=true, got %v", m)
	}

	text := readConfigFile(t, path)
	if !strings.HasPrefix(text, baseConfig) {
		t.Errorf("original content must be preserved, got %q", text)
	}
	if strings.Count(text, ignoreDevicesBeginMarker) != 1 || strings.Count(text, ignoreDevicesEndMarker) != 1 {
		t.Fatalf("expected exactly one managed block, got:\n%s", text)
	}
	if !strings.Contains(text, "[midi]") {
		t.Errorf("expected a new [midi] table to be created, got:\n%s", text)
	}

	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("saved config must survive config.Load: %v", err)
	}
	if len(cfg.MIDI.IgnoreDevices) != 1 || cfg.MIDI.IgnoreDevices[0] != "Yamaha P-125" {
		t.Errorf("loaded ignore_devices: unexpected %+v", cfg.MIDI.IgnoreDevices)
	}
	if got := md.Ignore(); len(got) != 1 || got[0] != "Yamaha P-125" {
		t.Errorf("expected the live ignore list applied too, got %v", got)
	}
	assertNoTempLitter(t, path)
}

func TestMIDIDevicesSaveSplicesIntoExistingMIDITable(t *testing.T) {
	base := "[midi]\nport_match = \"launchkey\"\n\n[web]\nenabled = false\n"
	f, path, _ := newMIDIConfigFixture(t, base)

	rec := f.do(t, "PUT", "/api/midi/devices", map[string]any{
		"ignore": []string{"Yamaha P-125"}, "save": true,
	})
	wantStatus(t, rec, http.StatusOK)

	text := readConfigFile(t, path)
	if strings.Count(text, "[midi]") != 1 {
		t.Fatalf("must not create a second [midi] table, got:\n%s", text)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("saved config must survive config.Load: %v", err)
	}
	if cfg.MIDI.PortMatch != "launchkey" {
		t.Errorf("existing port_match must be preserved, got %q", cfg.MIDI.PortMatch)
	}
	if len(cfg.MIDI.IgnoreDevices) != 1 || cfg.MIDI.IgnoreDevices[0] != "Yamaha P-125" {
		t.Errorf("loaded ignore_devices: unexpected %+v", cfg.MIDI.IgnoreDevices)
	}
	assertNoTempLitter(t, path)
}

func TestMIDIDevicesSaveReplacesExistingManagedBlock(t *testing.T) {
	f, path, _ := newMIDIConfigFixture(t, baseConfig)

	wantStatus(t, f.do(t, "PUT", "/api/midi/devices", map[string]any{
		"ignore": []string{"Yamaha P-125"}, "save": true,
	}), http.StatusOK)
	wantStatus(t, f.do(t, "PUT", "/api/midi/devices", map[string]any{
		"ignore": []string{"Other Synth", "Another One"}, "save": true,
	}), http.StatusOK)

	text := readConfigFile(t, path)
	if strings.Count(text, ignoreDevicesBeginMarker) != 1 || strings.Count(text, ignoreDevicesEndMarker) != 1 {
		t.Fatalf("expected the managed block to be replaced, not duplicated, got:\n%s", text)
	}
	if strings.Contains(text, "Yamaha P-125") {
		t.Errorf("stale ignore entry left behind:\n%s", text)
	}
	cfg, err := config.Load(path)
	if err != nil {
		t.Fatalf("re-saved config must survive config.Load: %v", err)
	}
	if len(cfg.MIDI.IgnoreDevices) != 2 {
		t.Errorf("loaded ignore_devices: unexpected %+v", cfg.MIDI.IgnoreDevices)
	}
	assertNoTempLitter(t, path)
}

func TestMIDIDevicesSaveRefusesUnmanagedSection(t *testing.T) {
	handWritten := "[midi]\nignore_devices = [\"Manual Synth\"]\n"
	f, path, md := newMIDIConfigFixture(t, handWritten)

	rec := f.do(t, "PUT", "/api/midi/devices", map[string]any{
		"ignore": []string{"Yamaha P-125"}, "save": true,
	})
	wantStatus(t, rec, http.StatusConflict)
	m := decodeBody(t, rec)
	if msg, _ := m["error"].(string); !strings.Contains(msg, "by hand") {
		t.Errorf("409 message should point at hand-editing, got %q", msg)
	}
	if got := readConfigFile(t, path); got != handWritten {
		t.Errorf("hand-written config must never be rewritten, got %q", got)
	}
	if got := md.Ignore(); len(got) != 0 {
		t.Errorf("a refused save must not apply live either, got %v", got)
	}
}

func TestUpsertIgnoreDevicesCorruptMarkers(t *testing.T) {
	_, err := upsertIgnoreDevices(ignoreDevicesBeginMarker+"\n[midi]\n", "block")
	if err == nil || !strings.Contains(err.Error(), "corrupted") {
		t.Errorf("expected corrupt-marker error, got %v", err)
	}
}

func TestUpsertIgnoreDevicesUnmanagedOutsideFence(t *testing.T) {
	orig := "[midi]\nignore_devices = [\"a\"]\n\n" +
		ignoreDevicesBeginMarker + "\nignore_devices = [\"b\"]\n" + ignoreDevicesEndMarker + "\n"
	if _, err := upsertIgnoreDevices(orig, "block"); err != errUnmanagedIgnoreDevices {
		t.Errorf("expected errUnmanagedIgnoreDevices for a second, hand-written entry, got %v", err)
	}
}

func TestMIDITableHeaderRegexDoesNotMatchSubTable(t *testing.T) {
	if midiTableHeaderRe.MatchString("[midi.velocity]\n") {
		t.Error("midiTableHeaderRe must not match [midi.velocity]")
	}
	if !midiTableHeaderRe.MatchString("[midi]\n") {
		t.Error("midiTableHeaderRe must match a bare [midi] header")
	}
}
