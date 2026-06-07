package osc

import (
	"log/slog"

	"github.com/mschulkind-oss/polyclav/internal/midi"
)

// Sender is the minimal OSC dispatch surface NewMapper needs. *Client
// satisfies it; so does the reconciler-wrapped client.
type Sender interface {
	Send(addr string, args ...any) error
}

// Mapper dispatches MIDI events into OSC sends per the configured bindings.
// Lookup is O(1) keyed by (source-kind, channel, controller-or-note).
type Mapper struct {
	client   Sender
	logger   *slog.Logger
	bindings map[bindKey]Binding
}

type bindKey struct {
	kind       byte
	channel    byte
	controller byte
}

func NewMapper(client Sender, logger *slog.Logger, bindings []Binding) *Mapper {
	m := &Mapper{
		client:   client,
		logger:   logger,
		bindings: make(map[bindKey]Binding, len(bindings)),
	}
	for _, b := range bindings {
		k, ok := keyFor(b)
		if !ok {
			logger.Warn("osc binding: unknown source_kind; skipping", "binding", b)
			continue
		}
		m.bindings[k] = b
	}
	return m
}

func keyFor(b Binding) (bindKey, bool) {
	switch b.SourceKind {
	case "cc":
		return bindKey{kind: 'c', channel: byte(b.Channel), controller: byte(b.Controller)}, true
	case "note":
		return bindKey{kind: 'n', channel: byte(b.Channel), controller: byte(b.Controller)}, true
	default:
		return bindKey{}, false
	}
}

func (m *Mapper) Dispatch(ev midi.Event) {
	if m == nil {
		return
	}
	var key bindKey
	var raw byte
	switch ev.Kind {
	case midi.ControlChange:
		key = bindKey{kind: 'c', channel: ev.Channel, controller: ev.CC}
		raw = ev.Value
	case midi.NoteOn:
		key = bindKey{kind: 'n', channel: ev.Channel, controller: ev.Note}
		raw = ev.Vel
	default:
		return
	}
	b, ok := m.bindings[key]
	if !ok {
		return
	}
	if err := m.send(b, raw); err != nil {
		m.logger.Warn("osc send", "addr", b.OSC, "err", err)
	}
}

func (m *Mapper) send(b Binding, raw byte) error {
	switch b.Transform {
	case "scalar", "":
		return m.client.Send(b.OSC, float32(raw)/127.0)
	case "press":
		v := int32(0)
		if raw > 0 {
			v = 1
		}
		return m.client.Send(b.OSC, v)
	default:
		m.logger.Warn("osc binding: unknown transform; skipping", "transform", b.Transform)
		return nil
	}
}
