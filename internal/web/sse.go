package web

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"
)

// heartbeatInterval is how often the SSE stream emits a comment line so
// idle-connection proxies don't kill the stream. A var (not const) so
// tests can shorten it.
var heartbeatInterval = 15 * time.Second

// handleEvents is GET /api/events: a Server-Sent Events stream. On
// connect it subscribes to the change hub FIRST, then sends an
// `event: snapshot` with the full /api/status payload — so a change that
// lands between snapshot assembly and the stream loop is replayed after
// the snapshot (harmless; deltas are idempotent state sets) instead of
// lost. Every subsequent controls.Change is forwarded as
// `event: <Type>` + `data: <JSON of Data>`. The subscription is dropped
// when the client disconnects (r.Context) or the server shuts down
// (Serve's BaseContext).
func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		writeErr(w, http.StatusInternalServerError, "streaming unsupported")
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.WriteHeader(http.StatusOK)

	ch, cancel := s.deps.Hub.Subscribe(64)
	defer cancel()

	if err := writeSSE(w, "snapshot", s.statusView()); err != nil {
		return
	}
	flusher.Flush()

	heartbeat := time.NewTicker(heartbeatInterval)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case c, ok := <-ch:
			if !ok {
				return
			}
			if err := writeSSE(w, c.Type, c.Data); err != nil {
				return
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := io.WriteString(w, ": heartbeat\n\n"); err != nil {
				return
			}
			flusher.Flush()
		}
	}
}

// writeSSE emits one SSE event frame. data is JSON-marshalled; a marshal
// failure degrades to `{}` rather than corrupting the stream framing.
func writeSSE(w io.Writer, event string, data any) error {
	b, err := json.Marshal(data)
	if err != nil {
		b = []byte("{}")
	}
	_, err = fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event, b)
	return err
}
