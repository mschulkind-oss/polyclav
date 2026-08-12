// mididevices.go holds the MIDI-devices panel endpoints
// (docs/USER_GUIDE.md "[midi] — which keyboards send notes"):
// GET/PUT /api/midi/devices, the web-UI counterpart to `polyclav midi
// list` and [midi].allow_devices. Follows the exact save/session-only
// contract editor.go's velocity endpoint established — SetAllow always
// applies live; save additionally persists into polyclav.toml.
package web

import (
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strings"

	"github.com/mschulkind-oss/polyclav/internal/midi"
)

type midiDeviceJSON struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// handleMIDIDevicesGet reports every currently-connected MIDI input port
// with its live classification (see midi.PortStatus) — the same
// classification `polyclav midi list` prints, sharing midi.ClassifyPorts
// so the two surfaces can never disagree.
func (s *Server) handleMIDIDevicesGet(w http.ResponseWriter, _ *http.Request) {
	if s.deps.MIDIDevices == nil {
		writeErr(w, http.StatusServiceUnavailable, "midi devices not available")
		return
	}
	// Enumeration failure (no ALSA sequencer / CoreMIDI client available
	// at all -- distinct from "zero ports connected") degrades to an
	// empty list rather than a hard error, matching internal/midiprobe's
	// established graceful-degradation convention: a dashboard endpoint
	// should stay usable on a machine with no working MIDI subsystem, not
	// 500 just because this one signal is unavailable.
	names, err := s.deps.MIDIPortLister()
	if err != nil {
		s.deps.Logger.Warn("midi devices: enumerate ports failed, reporting none", "err", err)
		names = nil
	}
	allow := s.deps.MIDIDevices.Allow()
	infos := midi.ClassifyPorts(names, s.deps.MIDIDevices.Match(), allow)
	out := make([]midiDeviceJSON, len(infos))
	for i, info := range infos {
		out[i] = midiDeviceJSON{Name: info.Name, Status: string(info.Status)}
	}
	// allow is echoed back (not just the per-port statuses) so the panel
	// can render entries that name a device which isn't plugged in right
	// now — those have no port row to hang a checkbox off, but dropping
	// them from the UI's working set would silently delete them on the
	// next save.
	writeJSON(w, http.StatusOK, map[string]any{
		"devices": out,
		"match":   s.deps.MIDIDevices.Match(),
		"allow":   emptySliceIfNil(allow),
	})
}

type midiDevicesPutBody struct {
	Allow []string `json:"allow"`
	Save  bool     `json:"save"`
}

// handleMIDIDevicesPut applies an updated allowlist immediately (live,
// regardless of save — the whole point of a running daemon exposing
// this at all) and, when save is true, additionally persists it into
// polyclav.toml's managed allow_devices block. Save-then-apply order,
// same as velocity: a request that fails to save must not leave a
// half-applied state.
func (s *Server) handleMIDIDevicesPut(w http.ResponseWriter, r *http.Request) {
	if s.deps.MIDIDevices == nil {
		writeErr(w, http.StatusServiceUnavailable, "midi devices not available")
		return
	}
	var body midiDevicesPutBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}

	if body.Save {
		if s.deps.ConfigPath == "" {
			writeErr(w, http.StatusNotFound, "config file not available; cannot save")
			return
		}
		if err := s.saveAllowDevicesBlock(body.Allow); err != nil {
			var ve *configValidationError
			switch {
			case errors.Is(err, errUnmanagedAllowDevices) || errors.Is(err, errCorruptAllowMarkers):
				writeErr(w, http.StatusConflict, err.Error())
			case errors.As(err, &ve):
				writeErr(w, http.StatusConflict, "saving would produce an invalid config — edit polyclav.toml by hand: "+ve.msg)
			default:
				writeErr(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
	}

	s.deps.MIDIDevices.SetAllow(body.Allow)
	writeJSON(w, http.StatusOK, map[string]any{"allow": emptySliceIfNil(body.Allow), "saved": body.Save})
}

// emptySliceIfNil keeps the JSON shape a `[]`, never `null` — the panel
// treats the allowlist as an array it can map over unconditionally, and
// "no devices selected" is a real, expected state here (it's the default
// on a fresh install), not an error to be signalled with null.
func emptySliceIfNil(in []string) []string {
	if in == nil {
		return []string{}
	}
	return in
}

// ---- managed allow_devices line, inside the existing [midi] table -------
//
// Unlike [midi.velocity] (a wholly separate table path velocity.go can
// insert anywhere), allow_devices is a key directly on MIDIConfig — it
// must land INSIDE the file's [midi] table, not a new sub-table, or
// config.Load would parse it into the wrong place. So this fences just
// the one key-value line, splices it right after an existing bare
// [midi] header if one exists, or appends a brand-new [midi] table at
// EOF if the file has none yet.

const (
	allowDevicesBeginMarker = "# BEGIN polyclav-managed allow_devices (web UI — edits on this line are overwritten)"
	allowDevicesEndMarker   = "# END polyclav-managed allow_devices"
)

var (
	errUnmanagedAllowDevices = errors.New("polyclav.toml already has a hand-written allow_devices under [midi] — edit the config file by hand instead of saving from the web UI")
	errCorruptAllowMarkers   = errors.New("the managed allow_devices markers in polyclav.toml are corrupted (one of BEGIN/END is missing) — repair the config file by hand")
)

// midiTableHeaderRe matches a bare `[midi]` table header line — NOT
// `[midi.velocity]` or any other sub-table (the `\s*\]` right after
// `midi` requires nothing but whitespace before the closing bracket).
var midiTableHeaderRe = regexp.MustCompile(`(?m)^\[\s*midi\s*\][ \t]*(?:#.*)?$`)

// unmanagedAllowDevicesRe matches a bare allow_devices key anywhere in
// the file — used (against the text OUTSIDE our own markers) to refuse
// clobbering a hand-written one, mirroring unmanagedVelocityRe.
var unmanagedAllowDevicesRe = regexp.MustCompile(`(?m)^\s*allow_devices\s*=`)

// renderAllowDevicesBlock renders allow as the marker-fenced line,
// without a trailing newline (callers add their own line breaks the
// same way renderVelocityBlock's callers do).
func renderAllowDevicesBlock(allow []string) string {
	quoted := make([]string, len(allow))
	for i, n := range allow {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	var b strings.Builder
	b.WriteString(allowDevicesBeginMarker + "\n")
	fmt.Fprintf(&b, "allow_devices = [%s]\n", strings.Join(quoted, ", "))
	b.WriteString(allowDevicesEndMarker)
	return b.String()
}

// upsertAllowDevices replaces the existing managed line in orig with
// block, or splices it into an existing bare [midi] table, or appends a
// brand-new [midi] table at EOF when the file has neither. A
// hand-written allow_devices outside the fence refuses with
// errUnmanagedAllowDevices — never silently clobbered.
func upsertAllowDevices(orig, block string) (string, error) {
	bi := strings.Index(orig, allowDevicesBeginMarker)
	ei := strings.Index(orig, allowDevicesEndMarker)
	switch {
	case bi >= 0 && ei > bi:
		outside := orig[:bi] + orig[ei+len(allowDevicesEndMarker):]
		if unmanagedAllowDevicesRe.MatchString(outside) {
			return "", errUnmanagedAllowDevices
		}
		return orig[:bi] + block + orig[ei+len(allowDevicesEndMarker):], nil
	case bi < 0 && ei < 0:
		if unmanagedAllowDevicesRe.MatchString(orig) {
			return "", errUnmanagedAllowDevices
		}
		loc := midiTableHeaderRe.FindStringIndex(orig)
		if loc == nil {
			trimmed := strings.TrimRight(orig, "\n")
			if trimmed == "" {
				return "[midi]\n" + block + "\n", nil
			}
			return trimmed + "\n\n[midi]\n" + block + "\n", nil
		}
		insertAt := loc[1]
		if insertAt < len(orig) && orig[insertAt] == '\n' {
			insertAt++
		}
		return orig[:insertAt] + block + "\n" + orig[insertAt:], nil
	default:
		return "", errCorruptAllowMarkers
	}
}

// saveAllowDevicesBlock persists allow into ConfigPath's managed line,
// going through the same temp-validate-rename path as PUT /api/config
// and the velocity save (saveValidatedConfig — see its comment for why
// runValidate=false here too: a device-selection edit never touches
// [[patches]] and must not be blocked by an already-missing soundfont).
// cfgMu is held across the whole read → merge → rename, same reason as
// saveVelocityBlock: a concurrent PUT /api/config must not slip a write
// in between our read and our rename.
func (s *Server) saveAllowDevicesBlock(allow []string) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	orig, err := os.ReadFile(s.deps.ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	merged, err := upsertAllowDevices(string(orig), renderAllowDevicesBlock(allow))
	if err != nil {
		return err
	}
	return s.saveValidatedConfig([]byte(merged), false)
}
