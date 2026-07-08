package web

import (
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/midiprobe"
)

// routesProbe registers the generic MIDI device reverse-engineering
// tool's endpoints: connect to an exact-named port pair, stream captured
// events, tag them with a label, probe with a Universal Identity
// Request, send arbitrary hex, and export everything as a portable JSON
// device profile. Every handler starts with the same nil-guard idiom as
// handlePlayerPlay: Deps.Probe is optional (constructed by main only when
// the web UI is enabled), so a nil Probe reports 503 rather than
// panicking.
func (s *Server) routesProbe() {
	s.mux.HandleFunc("GET /api/probe/ports", s.handleProbePorts)
	s.mux.HandleFunc("GET /api/probe/status", s.handleProbeStatus)
	s.mux.HandleFunc("POST /api/probe/connect", s.handleProbeConnect)
	s.mux.HandleFunc("POST /api/probe/disconnect", s.handleProbeDisconnect)
	s.mux.HandleFunc("GET /api/probe/events", s.handleProbeEvents)
	s.mux.HandleFunc("POST /api/probe/label", s.handleProbeLabel)
	s.mux.HandleFunc("POST /api/probe/identity", s.handleProbeIdentity)
	s.mux.HandleFunc("POST /api/probe/send", s.handleProbeSend)
	s.mux.HandleFunc("GET /api/probe/export", s.handleProbeExport)
}

// probeErrStatus maps midiprobe's sentinel errors to HTTP status codes,
// mirroring editor.go's errUnmanagedVelocity/errCorruptMarkers → 409/500
// dispatch via errors.Is rather than string matching.
func probeErrStatus(err error) int {
	switch {
	case errors.Is(err, midiprobe.ErrPortNotFound):
		return http.StatusNotFound
	case errors.Is(err, midiprobe.ErrAlreadyRunning),
		errors.Is(err, midiprobe.ErrNotRunning),
		errors.Is(err, midiprobe.ErrLabelInProgress):
		return http.StatusConflict
	default:
		return http.StatusInternalServerError
	}
}

func (s *Server) handleProbePorts(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Probe == nil {
		writeErr(w, http.StatusServiceUnavailable, "midi probe not available")
		return
	}
	ins, outs := s.deps.Probe.ListPorts()
	writeJSON(w, http.StatusOK, map[string][]string{"ins": ins, "outs": outs})
}

func (s *Server) handleProbeStatus(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Probe == nil {
		writeErr(w, http.StatusServiceUnavailable, "midi probe not available")
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Probe.Status())
}

type probeConnectBody struct {
	InPort    string `json:"inPort"`
	OutPort   string `json:"outPort"`
	BufferCap int    `json:"bufferCap"`
}

func (s *Server) handleProbeConnect(w http.ResponseWriter, r *http.Request) {
	if s.deps.Probe == nil {
		writeErr(w, http.StatusServiceUnavailable, "midi probe not available")
		return
	}
	var body probeConnectBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	if body.InPort == "" || body.OutPort == "" {
		writeErr(w, http.StatusBadRequest, "inPort and outPort are required")
		return
	}
	if err := s.deps.Probe.Start(body.InPort, body.OutPort, body.BufferCap); err != nil {
		writeErr(w, probeErrStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Probe.Status())
}

func (s *Server) handleProbeDisconnect(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Probe == nil {
		writeErr(w, http.StatusServiceUnavailable, "midi probe not available")
		return
	}
	if err := s.deps.Probe.Stop(); err != nil {
		writeErr(w, probeErrStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, s.deps.Probe.Status())
}

func (s *Server) handleProbeEvents(w http.ResponseWriter, r *http.Request) {
	if s.deps.Probe == nil {
		writeErr(w, http.StatusServiceUnavailable, "midi probe not available")
		return
	}
	var since uint64
	if v := r.URL.Query().Get("since"); v != "" {
		parsed, err := strconv.ParseUint(v, 10, 64)
		if err != nil {
			writeErr(w, http.StatusBadRequest, "since must be a non-negative integer")
			return
		}
		since = parsed
	}
	events := s.deps.Probe.Events(since)
	if events == nil {
		events = []midiprobe.Event{}
	}
	writeJSON(w, http.StatusOK, events)
}

type probeLabelBody struct {
	Label    string `json:"label"`
	WindowMs int    `json:"windowMs"`
}

func (s *Server) handleProbeLabel(w http.ResponseWriter, r *http.Request) {
	if s.deps.Probe == nil {
		writeErr(w, http.StatusServiceUnavailable, "midi probe not available")
		return
	}
	var body probeLabelBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	if strings.TrimSpace(body.Label) == "" {
		writeErr(w, http.StatusBadRequest, "label must not be empty")
		return
	}
	window := time.Duration(body.WindowMs) * time.Millisecond
	if err := s.deps.Probe.BeginLabel(body.Label, window); err != nil {
		writeErr(w, probeErrStatus(err), err.Error())
		return
	}
	st := s.deps.Probe.Status()
	writeJSON(w, http.StatusOK, map[string]any{
		"label":  st.LabelText,
		"endsAt": st.LabelEndsAt,
	})
}

type probeIdentityBody struct {
	Channel   *int `json:"channel"`
	TimeoutMs int  `json:"timeoutMs"`
}

func (s *Server) handleProbeIdentity(w http.ResponseWriter, r *http.Request) {
	if s.deps.Probe == nil {
		writeErr(w, http.StatusServiceUnavailable, "midi probe not available")
		return
	}
	var body probeIdentityBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	channel := 0x7F // "disregard channel" — the conventional default for this Universal SysEx message
	if body.Channel != nil {
		if *body.Channel < 0 || *body.Channel > 0x7F {
			writeErr(w, http.StatusBadRequest, "channel must be 0..127")
			return
		}
		channel = *body.Channel
	}
	timeout := time.Duration(body.TimeoutMs) * time.Millisecond
	result, err := s.deps.Probe.IdentityRequest(byte(channel), timeout)
	if err != nil {
		writeErr(w, probeErrStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, result)
}

type probeSendBody struct {
	Hex string `json:"hex"`
}

func (s *Server) handleProbeSend(w http.ResponseWriter, r *http.Request) {
	if s.deps.Probe == nil {
		writeErr(w, http.StatusServiceUnavailable, "midi probe not available")
		return
	}
	var body probeSendBody
	if err := decodeJSON(w, r, &body); err != nil {
		writeErr(w, http.StatusBadRequest, "bad JSON: "+err.Error())
		return
	}
	cleaned := strings.NewReplacer(" ", "", "\t", "", "\n", "").Replace(body.Hex)
	raw, err := hex.DecodeString(cleaned)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "hex: "+err.Error())
		return
	}
	if len(raw) == 0 {
		writeErr(w, http.StatusBadRequest, "hex must decode to at least one byte")
		return
	}
	if err := s.deps.Probe.SendRaw(raw); err != nil {
		writeErr(w, probeErrStatus(err), err.Error())
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"sent": true, "bytes": len(raw)})
}

func (s *Server) handleProbeExport(w http.ResponseWriter, _ *http.Request) {
	if s.deps.Probe == nil {
		writeErr(w, http.StatusServiceUnavailable, "midi probe not available")
		return
	}
	profile, err := s.deps.Probe.Export()
	if err != nil {
		writeErr(w, http.StatusNotFound, err.Error())
		return
	}
	fname := fmt.Sprintf("polyclav-midiprobe-%s.json", time.Now().UTC().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Content-Disposition", `attachment; filename="`+fname+`"`)
	writeJSON(w, http.StatusOK, profile)
}
