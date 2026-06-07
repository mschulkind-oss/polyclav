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

// ReconcilerConfig configures the XR18 reachability reconciler.
type ReconcilerConfig struct {
	Host          string
	Port          int
	PollInterval  time.Duration
	Timeout       time.Duration
	MissThreshold int
}

// Reconciler tracks XR18 reachability via periodic /xinfo pings and
// proxies sends. Send is a no-op (returning nil) while absent so the
// mapper does not spew errors when the mixer is offline.
//
// Outgoing /xinfo pings are built by hand here; everything else still
// goes through the go-osc client.Send for symmetry with the rest of the
// package.
type Reconciler struct {
	logger *slog.Logger
	cfg    ReconcilerConfig
	client *Client

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
	return &Reconciler{
		logger: logger,
		cfg:    cfg,
		client: NewClient(cfg.Host, cfg.Port),
	}
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

// /xinfo OSC packet: address "/xinfo" padded + type-tag ",".
var xinfoPacket = []byte{'/', 'x', 'i', 'n', 'f', 'o', 0, 0, ',', 0, 0, 0}

// Run binds an ephemeral UDP socket and pings /xinfo every PollInterval.
// Returns nil on ctx cancel.
func (r *Reconciler) Run(ctx context.Context) error {
	// No host configured = OSC mixer control disabled. Skip all network
	// activity (no UDP bind, no /xinfo polling) so a fresh install on
	// someone else's LAN never pings a stranger's address. State stays
	// "absent", so Send remains a safe no-op.
	if r.cfg.Host == "" {
		r.logger.Info("xr18 OSC disabled (no host configured)")
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
		"poll", r.cfg.PollInterval, "timeout", r.cfg.Timeout, "miss_threshold", r.cfg.MissThreshold)

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
	if _, err := udp.WriteToUDP(xinfoPacket, target); err != nil {
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
