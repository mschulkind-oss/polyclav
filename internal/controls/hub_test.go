package controls

import (
	"fmt"
	"sync"
	"testing"
	"time"
)

func TestSubscribeReceivesPublished(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe(4)
	defer cancel()

	h.Publish(Change{Type: "params", Data: map[string]any{"field": "volume", "value": float32(0.8)}})

	select {
	case c := <-ch:
		if c.Type != "params" {
			t.Errorf("expected Type %q, got %q", "params", c.Type)
		}
		if got := c.Data["field"]; got != "volume" {
			t.Errorf("expected Data[field]=volume, got %v", got)
		}
	default:
		t.Fatal("expected a buffered change, got none")
	}
}

func TestMultiSubscriberFanOut(t *testing.T) {
	h := NewHub()
	ch1, cancel1 := h.Subscribe(8)
	defer cancel1()
	ch2, cancel2 := h.Subscribe(8)
	defer cancel2()
	ch3, cancel3 := h.Subscribe(8)
	defer cancel3()

	published := []string{"params", "patch", "mastering"}
	for _, typ := range published {
		h.Publish(Change{Type: typ})
	}

	for i, ch := range []<-chan Change{ch1, ch2, ch3} {
		for _, want := range published {
			select {
			case c := <-ch:
				if c.Type != want {
					t.Errorf("subscriber %d: expected Type %q, got %q", i, want, c.Type)
				}
			default:
				t.Fatalf("subscriber %d: missing change %q", i, want)
			}
		}
	}
}

func TestSlowSubscriberDropsOldestAndPublisherNeverBlocks(t *testing.T) {
	h := NewHub()
	const buffer = 4
	const published = 100
	ch, cancel := h.Subscribe(buffer)
	defer cancel()

	// Publish far more than the buffer holds while nothing reads. If
	// Publish ever blocked, done would never close and the timeout fires.
	done := make(chan struct{})
	go func() {
		for i := 0; i < published; i++ {
			h.Publish(Change{Type: fmt.Sprint(i)})
		}
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked on a full subscriber buffer")
	}

	// Drop-oldest: the buffer must hold exactly the NEWEST `buffer`
	// changes, in order.
	for i := published - buffer; i < published; i++ {
		select {
		case c := <-ch:
			if c.Type != fmt.Sprint(i) {
				t.Errorf("expected change %d, got %q", i, c.Type)
			}
		default:
			t.Fatalf("expected %d buffered changes, channel ran dry at %d", buffer, i)
		}
	}
	select {
	case c := <-ch:
		t.Errorf("expected empty buffer after draining, got %q", c.Type)
	default:
	}
}

func TestZeroBufferClampedToOne(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe(0)
	defer cancel()

	// With a raw unbuffered channel both publishes would block forever;
	// the clamp to 1 plus drop-oldest keeps the newest change.
	done := make(chan struct{})
	go func() {
		h.Publish(Change{Type: "first"})
		h.Publish(Change{Type: "second"})
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Publish blocked with buffer=0 subscription")
	}

	select {
	case c := <-ch:
		if c.Type != "second" {
			t.Errorf("expected newest change %q, got %q", "second", c.Type)
		}
	default:
		t.Fatal("expected one buffered change")
	}
}

func TestCancelIdempotentAndClosesChannel(t *testing.T) {
	h := NewHub()
	ch, cancel := h.Subscribe(2)
	h.Publish(Change{Type: "before"})

	cancel()
	cancel() // second call must be a no-op, not a double-close panic

	// Buffered item drains first, then the channel reads closed.
	c, ok := <-ch
	if !ok || c.Type != "before" {
		t.Errorf("expected buffered change before close, got %v ok=%v", c, ok)
	}
	if _, ok := <-ch; ok {
		t.Error("expected closed channel after cancel")
	}

	// Publishing after cancel must not panic (no send on closed channel).
	h.Publish(Change{Type: "after"})
}

func TestCanceledSubscriberStopsReceivingOthersUnaffected(t *testing.T) {
	h := NewHub()
	_, cancelA := h.Subscribe(4)
	chB, cancelB := h.Subscribe(4)
	defer cancelB()

	cancelA()
	h.Publish(Change{Type: "x"})

	select {
	case c := <-chB:
		if c.Type != "x" {
			t.Errorf("expected %q, got %q", "x", c.Type)
		}
	default:
		t.Fatal("remaining subscriber did not receive the change")
	}
}

func TestHubConcurrentPublishSubscribeCancel(t *testing.T) {
	h := NewHub()
	var wg sync.WaitGroup

	// Publishers hammer the hub while subscribers come and go; run under
	// -race to catch locking mistakes around subscribe/cancel/publish.
	for p := 0; p < 4; p++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 200; i++ {
				h.Publish(Change{Type: "params"})
			}
		}()
	}
	for s := 0; s < 4; s++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 50; i++ {
				ch, cancel := h.Subscribe(2)
				select {
				case <-ch:
				default:
				}
				cancel()
			}
		}()
	}
	wg.Wait()
}
