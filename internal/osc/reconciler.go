package osc

import (
	"context"
	"fmt"
	"log/slog"
	"net"
	"sync"
	"sync/atomic"
	"time"
)

// ReconcilerConfig configures the OSC mixer reachability reconciler.
type ReconcilerConfig struct {
	Host string
	Port int
	// Heartbeat is the OSC address polled to detect mixer presence, e.g.
	// "/xinfo" for Behringer X-Air. "" means presence polling is DISABLED:
	// the target is treated as permanently reachable and sends go out
	// fire-and-forget (for generic OSC targets that won't answer pings).
	// There is no default here — callers wanting the X-Air behavior must
	// pass "/xinfo" explicitly (main wiring resolves the config pointer:
	// nil → "/xinfo", explicit "" → disabled).
	Heartbeat     string
	PollInterval  time.Duration
	Timeout       time.Duration
	MissThreshold int
}

// Reconciler tracks OSC mixer reachability via periodic heartbeat pings
// and proxies sends. Send is a no-op (returning nil) while absent so the
// mapper does not spew errors when the mixer is offline.
//
// Outgoing heartbeat pings are built by hand here (see oscPingPacket);
// everything else still goes through the go-osc client.Send for symmetry
// with the rest of the package.
type Reconciler struct {
	logger *slog.Logger
	cfg    ReconcilerConfig
	client *Client
	ping   []byte // prebuilt heartbeat packet; nil when polling is disabled

	state atomic.Int32 // 0 = absent, 1 = reachable

	missMu sync.Mutex
	misses int
}

// NewReconciler builds a Reconciler with sensible defaults applied.
func NewReconciler(logger *slog.Logger, cfg ReconcilerConfig) *Reconciler {
	if cfg.PollInterval == 0 {
		cfg.PollInterval = 5 * time.Second
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 3 * time.Second
	}
	if cfg.MissThreshold == 0 {
		cfg.MissThreshold = 3
	}
	r := &Reconciler{
		logger: logger,
		cfg:    cfg,
		client: NewClient(cfg.Host, cfg.Port),
	}
	if cfg.Heartbeat != "" {
		r.ping = oscPingPacket(cfg.Heartbeat)
	} else if cfg.Host != "" {
		// Heartbeat disabled but a target is configured: nothing will ever
		// probe, so nothing can flip the state — pin it to reachable from
		// construction and Send always forwards (fire-and-forget UDP).
		r.state.Store(1)
	}
	return r
}

// State returns "absent" or "reachable" for log lines.
func (r *Reconciler) State() string {
	if r.state.Load() == 1 {
		return "reachable"
	}
	return "absent"
}

// Send proxies the inner client; no-op + nil while absent (no retry-storm).
func (r *Reconciler) Send(addr string, args ...any) error {
	if r.state.Load() != 1 {
		return nil
	}
	return r.client.Send(addr, args...)
}

// oscPingPacket builds a minimal argument-less OSC message for the given
// address: the address string NUL-terminated and padded to a 4-byte
// boundary, followed by the type-tag string "," padded the same way.
// Invariant: oscPingPacket("/xinfo") is byte-identical to the hand-rolled
// /xinfo literal historically sent to X-Air mixers.
func oscPingPacket(addr string) []byte {
	n := len(addr) + 1 // address bytes + mandatory NUL terminator
	pad := (4 - n%4) % 4
	buf := make([]byte, n+pad+4) // padded address + ",\x00\x00\x00" type tag
	copy(buf, addr)
	buf[n+pad] = ','
	return buf
}

// Run binds an ephemeral UDP socket and pings the heartbeat address every
// PollInterval. Returns nil on ctx cancel.
func (r *Reconciler) Run(ctx context.Context) error {
	// No host configured = OSC mixer control disabled. Skip all network
	// activity (no UDP bind, no heartbeat polling) so a fresh install on
	// someone else's LAN never pings a stranger's address. State stays
	// "absent", so Send remains a safe no-op.
	if r.cfg.Host == "" {
		r.logger.Info("xr18 OSC disabled (no host configured)")
		return nil
	}

	// Host configured but heartbeat disabled (heartbeat = "" in config):
	// no UDP socket, no probes. State was pinned to reachable at
	// construction, so every Send forwards unconditionally.
	if r.cfg.Heartbeat == "" {
		r.logger.Info("mixer heartbeat disabled — sends are fire-and-forget",
			"host", r.cfg.Host, "port", r.cfg.Port)
		return nil
	}

	udp, err := net.ListenUDP("udp", &net.UDPAddr{IP: net.IPv4zero, Port: 0})
	if err != nil {
		return fmt.Errorf("osc reconciler: bind udp: %w", err)
	}
	defer udp.Close()

	target, err := net.ResolveUDPAddr("udp", fmt.Sprintf("%s:%d", r.cfg.Host, r.cfg.Port))
	if err != nil {
		return fmt.Errorf("osc reconciler: resolve target: %w", err)
	}

	r.logger.Info("xr18 reconciler start", "host", r.cfg.Host, "port", r.cfg.Port,
		"heartbeat", r.cfg.Heartbeat, "poll", r.cfg.PollInterval,
		"timeout", r.cfg.Timeout, "miss_threshold", r.cfg.MissThreshold)

	r.probe(udp, target)

	ticker := time.NewTicker(r.cfg.PollInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			r.probe(udp, target)
		}
	}
}

func (r *Reconciler) probe(udp *net.UDPConn, target *net.UDPAddr) {
	if _, err := udp.WriteToUDP(r.ping, target); err != nil {
		r.recordMiss()
		return
	}
	if err := udp.SetReadDeadline(time.Now().Add(r.cfg.Timeout)); err != nil {
		r.recordMiss()
		return
	}
	buf := make([]byte, 2048)
	n, _, err := udp.ReadFromUDP(buf)
	if err != nil || n == 0 {
		r.recordMiss()
		return
	}
	r.recordHit()
}

func (r *Reconciler) recordHit() {
	r.missMu.Lock()
	r.misses = 0
	prev := r.state.Swap(1)
	r.missMu.Unlock()
	if prev == 0 {
		r.logger.Info("xr18 reachable", "host", r.cfg.Host, "port", r.cfg.Port)
	}
}

func (r *Reconciler) recordMiss() {
	r.missMu.Lock()
	r.misses++
	flipped := false
	if r.misses >= r.cfg.MissThreshold {
		prev := r.state.Swap(0)
		flipped = (prev == 1)
	}
	r.missMu.Unlock()
	if flipped {
		r.logger.Warn("xr18 absent", "host", r.cfg.Host, "port", r.cfg.Port, "misses", r.cfg.MissThreshold)
	}
}
