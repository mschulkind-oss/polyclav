// mididevices.go holds the MIDI-devices panel endpoints
// (docs/USER_GUIDE.md "[midi] — which keyboards send notes"):
// GET/PUT /api/midi/devices, the web-UI counterpart to `polyclav midi
// list` and [midi].ignore_devices. Follows the exact save/session-only
// contract editor.go's velocity endpoint established — SetIgnore always
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
	infos := midi.ClassifyPorts(names, s.deps.MIDIDevices.Match(), s.deps.MIDIDevices.Ignore())
	out := make([]midiDeviceJSON, len(infos))
	for i, info := range infos {
		out[i] = midiDeviceJSON{Name: info.Name, Status: string(info.Status)}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"devices": out,
		"match":   s.deps.MIDIDevices.Match(),
	})
}

type midiDevicesPutBody struct {
	Ignore []string `json:"ignore"`
	Save   bool     `json:"save"`
}

// handleMIDIDevicesPut applies an updated ignore list immediately (live,
// regardless of save — the whole point of a running daemon exposing
// this at all) and, when save is true, additionally persists it into
// polyclav.toml's managed ignore_devices block. Save-then-apply order,
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
		if err := s.saveIgnoreDevicesBlock(body.Ignore); err != nil {
			var ve *configValidationError
			switch {
			case errors.Is(err, errUnmanagedIgnoreDevices) || errors.Is(err, errCorruptIgnoreMarkers):
				writeErr(w, http.StatusConflict, err.Error())
			case errors.As(err, &ve):
				writeErr(w, http.StatusConflict, "saving would produce an invalid config — edit polyclav.toml by hand: "+ve.msg)
			default:
				writeErr(w, http.StatusInternalServerError, err.Error())
			}
			return
		}
	}

	s.deps.MIDIDevices.SetIgnore(body.Ignore)
	writeJSON(w, http.StatusOK, map[string]any{"ignore": body.Ignore, "saved": body.Save})
}

// ---- managed ignore_devices line, inside the existing [midi] table ------
//
// Unlike [midi.velocity] (a wholly separate table path velocity.go can
// insert anywhere), ignore_devices is a key directly on MIDIConfig — it
// must land INSIDE the file's [midi] table, not a new sub-table, or
// config.Load would parse it into the wrong place. So this fences just
// the one key-value line, splices it right after an existing bare
// [midi] header if one exists, or appends a brand-new [midi] table at
// EOF if the file has none yet.

const (
	ignoreDevicesBeginMarker = "# BEGIN polyclav-managed ignore_devices (web UI — edits on this line are overwritten)"
	ignoreDevicesEndMarker   = "# END polyclav-managed ignore_devices"
)

var (
	errUnmanagedIgnoreDevices = errors.New("polyclav.toml already has a hand-written ignore_devices under [midi] — edit the config file by hand instead of saving from the web UI")
	errCorruptIgnoreMarkers   = errors.New("the managed ignore_devices markers in polyclav.toml are corrupted (one of BEGIN/END is missing) — repair the config file by hand")
)

// midiTableHeaderRe matches a bare `[midi]` table header line — NOT
// `[midi.velocity]` or any other sub-table (the `\s*\]` right after
// `midi` requires nothing but whitespace before the closing bracket).
var midiTableHeaderRe = regexp.MustCompile(`(?m)^\[\s*midi\s*\][ \t]*(?:#.*)?$`)

// unmanagedIgnoreDevicesRe matches a bare ignore_devices key anywhere in
// the file — used (against the text OUTSIDE our own markers) to refuse
// clobbering a hand-written one, mirroring unmanagedVelocityRe.
var unmanagedIgnoreDevicesRe = regexp.MustCompile(`(?m)^\s*ignore_devices\s*=`)

// renderIgnoreDevicesBlock renders ignore as the marker-fenced line,
// without a trailing newline (callers add their own line breaks the
// same way renderVelocityBlock's callers do).
func renderIgnoreDevicesBlock(ignore []string) string {
	quoted := make([]string, len(ignore))
	for i, n := range ignore {
		quoted[i] = fmt.Sprintf("%q", n)
	}
	var b strings.Builder
	b.WriteString(ignoreDevicesBeginMarker + "\n")
	fmt.Fprintf(&b, "ignore_devices = [%s]\n", strings.Join(quoted, ", "))
	b.WriteString(ignoreDevicesEndMarker)
	return b.String()
}

// upsertIgnoreDevices replaces the existing managed line in orig with
// block, or splices it into an existing bare [midi] table, or appends a
// brand-new [midi] table at EOF when the file has neither. A
// hand-written ignore_devices outside the fence refuses with
// errUnmanagedIgnoreDevices — never silently clobbered.
func upsertIgnoreDevices(orig, block string) (string, error) {
	bi := strings.Index(orig, ignoreDevicesBeginMarker)
	ei := strings.Index(orig, ignoreDevicesEndMarker)
	switch {
	case bi >= 0 && ei > bi:
		outside := orig[:bi] + orig[ei+len(ignoreDevicesEndMarker):]
		if unmanagedIgnoreDevicesRe.MatchString(outside) {
			return "", errUnmanagedIgnoreDevices
		}
		return orig[:bi] + block + orig[ei+len(ignoreDevicesEndMarker):], nil
	case bi < 0 && ei < 0:
		if unmanagedIgnoreDevicesRe.MatchString(orig) {
			return "", errUnmanagedIgnoreDevices
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
		return "", errCorruptIgnoreMarkers
	}
}

// saveIgnoreDevicesBlock persists ignore into ConfigPath's managed line,
// going through the same temp-validate-rename path as PUT /api/config
// and the velocity save (saveValidatedConfig — see its comment for why
// runValidate=false here too: an ignore-list edit never touches
// [[patches]] and must not be blocked by an already-missing soundfont).
// cfgMu is held across the whole read → merge → rename, same reason as
// saveVelocityBlock: a concurrent PUT /api/config must not slip a write
// in between our read and our rename.
func (s *Server) saveIgnoreDevicesBlock(ignore []string) error {
	s.cfgMu.Lock()
	defer s.cfgMu.Unlock()
	orig, err := os.ReadFile(s.deps.ConfigPath)
	if err != nil {
		return fmt.Errorf("read config: %w", err)
	}
	merged, err := upsertIgnoreDevices(string(orig), renderIgnoreDevicesBlock(ignore))
	if err != nil {
		return err
	}
	return s.saveValidatedConfig([]byte(merged), false)
}
