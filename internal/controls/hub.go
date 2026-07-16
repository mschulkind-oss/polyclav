// Package controls is the shared parameter layer between every polyclav
// control surface (Launchkey knobs/pads, the web UI, future OSC inputs)
// and the audio engine + state store. It exists so that all surfaces
// mutate params through ONE code path — clamp, apply to audio atomics,
// persist to state.toml, publish a change event — instead of each surface
// re-implementing the closures that historically lived in
// cmd/polyclav/main.go. See docs/WEB_UI.md, "The real work: a controls
// layer".
package controls

import "sync"

// Change is one observable state transition (a knob turn, a patch
// switch, a mastering tweak, a velocity-curve swap). Type is a coarse
// category subscribers can switch on ("params", "patch", "synth",
// "chain", "mastering", "velocity", "macros"); Data carries only the
// changed keys and their new values so subscribers (e.g. the web UI's SSE
// stream) can update incrementally without re-fetching a full snapshot.
// The SSE handler forwards any Type verbatim as the event name, so a new
// Type needs no plumbing there.
//
// The "chain" type covers the post-synth pedal chain
// (internal/controls/chain.go): a param set carries {field: "<stage>.
// <leaf>", value: number, patch}; a stage toggle {field: "<stage>.
// enabled", value: bool, patch}; a reorder {field: "order", order:
// [...]}. A patch switch's "patch" change also folds the whole chain
// block into Data["chain"] (mirroring Data["synth"]).
//
// The "macros" type carries the global 8-slot macro assignments after an
// edit: Data{macros: [{slot, target, name, min, max}, …]}. The backend
// only stores and broadcasts the assignments — the web drives the target
// params (see internal/controls/macros.go).
type Change struct {
	Type string         `json:"type"`
	Data map[string]any `json:"data,omitempty"`
}

// Hub fans Changes out to any number of subscribers. It is the
// change-notification piece the daemon lacked: when a Launchkey knob
// turns, the new value lands in the audio atomics and the state store,
// but nothing told a web page about it. All methods are goroutine-safe.
type Hub struct {
	mu   sync.Mutex
	subs map[chan Change]struct{}
}

// NewHub returns an empty Hub ready for Subscribe/Publish.
func NewHub() *Hub {
	return &Hub{subs: make(map[chan Change]struct{})}
}

// Subscribe registers a new subscriber and returns its receive channel
// plus a cancel func. cancel unsubscribes and closes the channel; it is
// idempotent (safe to call twice — e.g. from both an HTTP handler's
// defer and a shutdown path). buffer values below 1 are clamped to 1
// because the drop-oldest overflow policy in Publish needs at least one
// buffered slot to make progress without a waiting receiver.
func (h *Hub) Subscribe(buffer int) (<-chan Change, func()) {
	if buffer < 1 {
		buffer = 1
	}
	ch := make(chan Change, buffer)
	h.mu.Lock()
	h.subs[ch] = struct{}{}
	h.mu.Unlock()

	var once sync.Once
	cancel := func() {
		once.Do(func() {
			// Delete and close under the same lock Publish holds, so a
			// concurrent Publish can never send on the closed channel.
			h.mu.Lock()
			delete(h.subs, ch)
			close(ch)
			h.mu.Unlock()
		})
	}
	return ch, cancel
}

// Publish delivers c to every subscriber without ever blocking the
// caller. Overflow policy: DROP-OLDEST — when a subscriber's buffer is
// full, its oldest queued Change is discarded to make room for the new
// one, so a slow consumer always converges on the most recent state
// instead of replaying a stale backlog. Non-blocking matters because
// Publish sits on the MIDI/DAW event path: a stalled browser must never
// be able to stall a knob turn.
func (h *Hub) Publish(c Change) {
	h.mu.Lock()
	defer h.mu.Unlock()
	for ch := range h.subs {
		select {
		case ch <- c:
		default:
			// Buffer full: drop the oldest item. The receive can lose a
			// race against the consumer, but that also frees a slot.
			select {
			case <-ch:
			default:
			}
			// Room is guaranteed now — we hold the lock so no other
			// sender exists, and consumers only ever free slots. The
			// default arm is unreachable belt-and-braces that keeps the
			// publisher provably non-blocking.
			select {
			case ch <- c:
			default:
			}
		}
	}
}
