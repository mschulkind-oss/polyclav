// Package osc speaks OSC over UDP to a Behringer X-Air mixer at the
// configured X-Air host/port (port 10024 is the X-Air family default).
// Used by the Launchkey→OSC mapper to drive the mixer from
// pad/fader/button events.
//
// Protocol reference: Patrick-Gilles Maillot's unofficial "XAir OSC
// cheat-sheet" — the de-facto spec for the Behringer X-Air family (XR18,
// XR16, XR12 — same OSC API; UDP port 10024 vs. 10023 on the larger
// X32). PDF:
//
//	https://drive.google.com/file/d/1dLfUUf-wfjX_1iaPmvFkGBg4oo4aT1Ha/view
//
// Linked as "XAir Cheat-sheet" from Maillot's index page at
// https://sites.google.com/site/patrickmaillot/x32 . A secondary mirror
// lives at
// https://behringerwiki.musictribe.com/index.php?title=OSC_Remote_Protocol .
package osc

import (
	"fmt"

	goosc "github.com/hypebeast/go-osc/osc"
)

// Client wraps a UDP go-osc client targeting the XR18.
type Client struct {
	inner *goosc.Client
}

func NewClient(host string, port int) *Client {
	return &Client{inner: goosc.NewClient(host, port)}
}

// Send constructs a Message at addr with the given args and dispatches it.
// XR18 typically wants int32 for booleans (0/1) and float32 for 0..1 levels.
func (c *Client) Send(addr string, args ...any) error {
	if c == nil || c.inner == nil {
		return fmt.Errorf("osc client is nil")
	}
	msg := goosc.NewMessage(addr, args...)
	return c.inner.Send(msg)
}
