package web

import (
	"bufio"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/controls"
)

// sseEvent is one parsed `event:`/`data:` frame.
type sseEvent struct {
	name string
	data string
}

// readSSEEvent scans lines until a complete event frame (terminated by a
// blank line) arrives, skipping comment/heartbeat lines. The caller's
// request context deadline bounds the wait: on timeout the body read
// errors, Scan returns false, and the test fails here.
func readSSEEvent(t *testing.T, sc *bufio.Scanner) sseEvent {
	t.Helper()
	var ev sseEvent
	for sc.Scan() {
		line := sc.Text()
		switch {
		case strings.HasPrefix(line, ":"): // comment (heartbeat)
		case strings.HasPrefix(line, "event: "):
			ev.name = strings.TrimPrefix(line, "event: ")
		case strings.HasPrefix(line, "data: "):
			ev.data = strings.TrimPrefix(line, "data: ")
		case line == "":
			if ev.name != "" || ev.data != "" {
				return ev
			}
		}
	}
	t.Fatalf("SSE stream ended while waiting for an event: %v", sc.Err())
	return ev
}

// openSSE connects to /api/events on a live test server and returns a
// scanner over the stream plus the client-side cancel.
func openSSE(t *testing.T, ts *httptest.Server, timeout time.Duration) (*bufio.Scanner, context.CancelFunc) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ts.URL+"/api/events", nil)
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("GET /api/events: %v", err)
	}
	t.Cleanup(func() { _ = resp.Body.Close() })
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status: expected 200, got %d", resp.StatusCode)
	}
	if ct := resp.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("content-type: expected text/event-stream, got %q", ct)
	}
	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	return sc, cancel
}

func TestSSESnapshotThenChanges(t *testing.T) {
	f := newFixture(t, func(d *Deps) {
		d.Devices = fakeDevices{lk: "connected", xr: "disabled"}
	})
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()

	sc, cancel := openSSE(t, ts, 10*time.Second)
	defer cancel()

	// 1. Initial snapshot: the full /api/status payload.
	ev := readSSEEvent(t, sc)
	if ev.name != "snapshot" {
		t.Fatalf("first event: expected snapshot, got %q", ev.name)
	}
	var snap map[string]any
	if err := json.Unmarshal([]byte(ev.data), &snap); err != nil {
		t.Fatalf("snapshot data: %v", err)
	}
	if snap["version"] != "test-1" {
		t.Errorf("snapshot version: expected test-1, got %v", snap["version"])
	}
	if dev := snap["devices"].(map[string]any); dev["launchkey"] != "connected" {
		t.Errorf("snapshot devices: unexpected %v", dev)
	}
	if _, ok := snap["params"].(map[string]any); !ok {
		t.Errorf("snapshot params: expected object, got %v", snap["params"])
	}
	if list, ok := snap["patches"].([]any); !ok || len(list) != 2 {
		t.Errorf("snapshot patches: expected 2 entries, got %v", snap["patches"])
	}

	// 2. A raw hub publish arrives as event: <Type> / data: <Data>.
	f.hub.Publish(controls.Change{Type: "params", Data: map[string]any{
		"field": "volume", "value": 0.5, "patch": "salamander",
	}})
	ev = readSSEEvent(t, sc)
	if ev.name != "params" {
		t.Fatalf("expected params event, got %q", ev.name)
	}
	var data map[string]any
	if err := json.Unmarshal([]byte(ev.data), &data); err != nil {
		t.Fatalf("params data: %v", err)
	}
	if data["field"] != "volume" || data["value"].(float64) != 0.5 {
		t.Errorf("params data: unexpected %v", data)
	}

	// 3. A controls-layer action flows end-to-end onto the stream.
	if err := f.ctrl.SelectPatch("moog"); err != nil {
		t.Fatalf("SelectPatch: %v", err)
	}
	ev = readSSEEvent(t, sc)
	if ev.name != "patch" {
		t.Fatalf("expected patch event, got %q", ev.name)
	}
	if err := json.Unmarshal([]byte(ev.data), &data); err != nil {
		t.Fatalf("patch data: %v", err)
	}
	if data["name"] != "moog" {
		t.Errorf("patch data: expected name=moog, got %v", data)
	}
}

func TestSSEClientCancelUnsubscribes(t *testing.T) {
	f := newFixture(t, nil)
	ts := httptest.NewServer(f.srv.Handler())

	sc, cancel := openSSE(t, ts, 10*time.Second)
	if ev := readSSEEvent(t, sc); ev.name != "snapshot" {
		t.Fatalf("expected snapshot, got %q", ev.name)
	}

	// Client goes away. The handler must notice (r.Context()) and return,
	// running its deferred hub unsubscribe.
	cancel()

	// ts.Close blocks until every outstanding request handler returns —
	// if the SSE handler leaked, this times out.
	done := make(chan struct{})
	go func() { ts.Close(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("SSE handler did not exit after client disconnect")
	}

	// The handler exited, so its deferred cancel() removed+closed the
	// subscription; a publish now must neither panic nor block.
	f.hub.Publish(controls.Change{Type: "params", Data: map[string]any{"field": "volume"}})
}

func TestSSEHeartbeat(t *testing.T) {
	old := heartbeatInterval
	heartbeatInterval = 20 * time.Millisecond
	t.Cleanup(func() { heartbeatInterval = old })

	f := newFixture(t, nil)
	ts := httptest.NewServer(f.srv.Handler())
	defer ts.Close()

	sc, cancel := openSSE(t, ts, 5*time.Second)
	defer cancel()
	if ev := readSSEEvent(t, sc); ev.name != "snapshot" {
		t.Fatalf("expected snapshot, got %q", ev.name)
	}

	// With no changes published, the next stream bytes must be a comment
	// line — the proxy-keepalive heartbeat.
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, ":") {
			return // heartbeat observed
		}
		if line != "" {
			t.Fatalf("expected a heartbeat comment, got %q", line)
		}
	}
	t.Fatalf("stream ended without a heartbeat: %v", sc.Err())
}

// noFlushWriter hides the recorder's Flush method so the handler sees a
// ResponseWriter without http.Flusher support.
type noFlushWriter struct{ http.ResponseWriter }

func TestSSEFlusherUnsupported(t *testing.T) {
	f := newFixture(t, nil)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/events", nil)
	f.srv.Handler().ServeHTTP(noFlushWriter{rec}, req)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("expected 500 without http.Flusher, got %d", rec.Code)
	}
}
