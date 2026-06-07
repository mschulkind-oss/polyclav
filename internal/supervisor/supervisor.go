// Package supervisor coordinates the hardware reconcilers. It owns one
// launchkey.Reconciler and one osc.Reconciler, runs each in its own
// goroutine, and logs aggregate state.
package supervisor

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/mschulkind-oss/polyclav/internal/launchkey"
	"github.com/mschulkind-oss/polyclav/internal/osc"
)

// Config bundles the two reconciler configs.
type Config struct {
	Launchkey launchkey.ReconcilerConfig
	XR18      osc.ReconcilerConfig
}

// Supervisor coordinates the device reconcilers.
type Supervisor struct {
	logger    *slog.Logger
	launchkey *launchkey.Reconciler
	xr18      *osc.Reconciler
}

// New builds a Supervisor with both reconcilers wired up but not yet
// running.
func New(logger *slog.Logger, cfg Config) *Supervisor {
	return &Supervisor{
		logger:    logger,
		launchkey: launchkey.NewReconciler(logger, cfg.Launchkey),
		xr18:      osc.NewReconciler(logger, cfg.XR18),
	}
}

// LaunchkeyState returns the current Launchkey reconciler state.
func (s *Supervisor) LaunchkeyState() string { return s.launchkey.State() }

// XR18State returns the current XR18 reconciler state.
func (s *Supervisor) XR18State() string { return s.xr18.State() }

// Launchkey returns the underlying reconciler (for SetTitle / SetPadColor proxies).
func (s *Supervisor) Launchkey() *launchkey.Reconciler { return s.launchkey }

// XR18 returns the underlying OSC reconciler (for its Send proxy).
func (s *Supervisor) XR18() *osc.Reconciler { return s.xr18 }

// Run blocks until ctx is done. Spawns both reconciler goroutines and
// logs aggregate state every 10s.
func (s *Supervisor) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		_ = s.launchkey.Run(ctx)
	}()
	go func() {
		defer wg.Done()
		_ = s.xr18.Run(ctx)
	}()

	ticker := time.NewTicker(10 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			wg.Wait()
			return nil
		case <-ticker.C:
			s.logger.Info("supervisor tick",
				"launchkey", s.launchkey.State(),
				"xr18", s.xr18.State(),
			)
		}
	}
}
