package driver

import (
	"bytes"
	"context"
	"errors"
	"io"
	"log/slog"
	"reflect"
	"strings"
	"testing"

	"github.com/mschulkind-oss/polyclav/internal/midi"
)

type modeOut struct {
	messages [][]byte
	err      error
}

func (p *modeOut) Open() error             { return nil }
func (p *modeOut) Close() error            { return nil }
func (p *modeOut) IsOpen() bool            { return true }
func (p *modeOut) Number() int             { return 0 }
func (p *modeOut) String() string          { return "test DAW out" }
func (p *modeOut) Underlying() interface{} { return nil }
func (p *modeOut) Send(msg []byte) error {
	p.messages = append(p.messages, append([]byte(nil), msg...))
	return p.err
}

func TestDAWLayoutReportsRestoreOrWarn(t *testing.T) {
	for _, tc := range []struct {
		name, mode      string
		cc, wrong, want byte
	}{
		{"pads", "pads", 29, 14, 2},
		{"encoders", "encoders", 30, 1, 2},
		{"faders", "faders", 31, 6, 1},
	} {
		for _, restore := range []bool{true, false} {
			t.Run(tc.name+map[bool]string{true: "/restore", false: "/warn"}[restore], func(t *testing.T) {
				var logs bytes.Buffer
				out := &modeOut{}
				d := &Driver{logger: slog.New(slog.NewTextHandler(&logs, nil)), out: out,
					events: make(chan Event, 1), restoreDAWLayout: restore}
				var raw []midi.RawEvent
				sink := func(ev midi.RawEvent) { raw = append(raw, ev) }
				for _, msg := range [][]byte{{0xB6, tc.cc, tc.want}, {0xB0, tc.cc, tc.wrong}, {0xB6, tc.cc, tc.wrong}, {0xB6, tc.cc, tc.want}} {
					d.handleInbound(context.Background(), "Launchkey MK4 DAW", msg, sink)
				}
				if len(raw) != 4 {
					t.Fatalf("raw stream lost mode messages: %d", len(raw))
				}
				if !strings.Contains(logs.String(), "launchkey unsupported") || !strings.Contains(logs.String(), tc.mode) {
					t.Fatalf("missing mode warning: %s", logs.String())
				}
				if restore {
					want := [][]byte{{0xB6, tc.cc, tc.want}}
					if tc.mode == "encoders" {
						want = [][]byte{{0xB6, 69, 127}, {0xB6, 30, 2}, {0xB6, 69, 127}, {0xB6, 69, 127}}
					}
					if !reflect.DeepEqual(out.messages, want) {
						t.Fatalf("restore messages = % X, want % X", out.messages, want)
					}
				} else if len(out.messages) != 0 {
					t.Fatalf("opt-out sent % X", out.messages)
				}
				select {
				case ev := <-d.events:
					t.Fatalf("mode report emitted control event: %#v", ev)
				default:
				}
			})
		}
	}
}

func TestEncoderRestoreReenablesRelativeOutput(t *testing.T) {
	out := &modeOut{}
	d := &Driver{logger: slog.New(slog.NewTextHandler(io.Discard, nil)), out: out, restoreDAWLayout: true}
	// Selecting Sends can reset relative output. Restore Plugin first, then
	// reenable CC 85..92; also recover when the player manually selects Plugin.
	d.handleModeReport([]byte{0xB6, 30, 4})
	want := [][]byte{{0xB6, 30, 2}, {0xB6, 69, 127}}
	if !reflect.DeepEqual(out.messages, want) {
		t.Fatalf("restore output = % X, want % X", out.messages, want)
	}
	out.messages = nil
	d.handleModeReport([]byte{0xB6, 30, 2})
	if !reflect.DeepEqual(out.messages, [][]byte{{0xB6, 69, 127}}) {
		t.Fatalf("Plugin return output = % X, want relative enable", out.messages)
	}
}

func TestDAWLayoutRestoreSendFailureIsLogged(t *testing.T) {
	var logs bytes.Buffer
	out := &modeOut{err: errors.New("device gone")}
	d := &Driver{logger: slog.New(slog.NewTextHandler(&logs, nil)), out: out,
		events: make(chan Event, 1), restoreDAWLayout: true}
	d.handleInbound(context.Background(), "Launchkey MK4 DAW", []byte{0xB6, 30, 4}, nil)
	if !strings.Contains(logs.String(), "device gone") {
		t.Fatalf("missing send error: %s", logs.String())
	}
}
