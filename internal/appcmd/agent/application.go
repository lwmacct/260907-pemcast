package agent

import (
	"context"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/deploy"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	"github.com/lwmacct/260907-pemcast/internal/hook"
	"github.com/lwmacct/260907-pemcast/internal/reconcile"
	"github.com/lwmacct/260907-pemcast/internal/state"
)

// Application owns the agent runtime and its dependencies.
type Application struct {
	config     config.Agent
	source     *etcdsource.Client
	controller *reconcile.Controller
	logger     *slog.Logger
}

func New(cfg config.Config, logger *slog.Logger) (*Application, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	store, err := state.New(cfg.Agent.StateDir)
	if err != nil {
		return nil, err
	}
	source, err := etcdsource.New(cfg.Agent.Etcd, cfg.Agent.Watch.RootPrefix)
	if err != nil {
		return nil, err
	}
	controller := reconcile.New(source, deploy.New(), hook.New(), store, cfg.Agent, logger)
	return &Application{config: cfg.Agent, source: source, controller: controller, logger: logger}, nil
}

func (a *Application) Close() error { return a.source.Close() }

func (a *Application) Run(ctx context.Context) error {
	if a.config.Once {
		snapshot, err := a.source.SnapshotActive(ctx)
		if err != nil {
			return err
		}
		return a.controller.ReconcileSnapshot(ctx, snapshot)
	}
	return a.runWatch(ctx)
}

func (a *Application) runWatch(ctx context.Context) error {
	var failures int
	for {
		if err := ctx.Err(); err != nil {
			return nil
		}
		snapshot, err := a.source.SnapshotActive(ctx)
		if err != nil {
			failures++
			a.logger.ErrorContext(ctx, "read active snapshot failed", "error", err)
			if !sleepContext(ctx, a.retryDelay(failures)) {
				return nil
			}
			continue
		}
		if err := a.controller.ReconcileSnapshot(ctx, snapshot); err != nil {
			a.logger.ErrorContext(ctx, "initial snapshot reconcile failed", "error", err)
		}
		failures = 0
		watchCtx, cancel := context.WithCancel(ctx)
		events, watchErrors := a.source.WatchActive(watchCtx, snapshot.Revision+1)
		var resync <-chan time.Time
		var timer *time.Timer
		if a.config.Watch.ResyncInterval > 0 {
			timer = time.NewTimer(a.config.Watch.ResyncInterval)
			resync = timer.C
		}
		restart := false
		for !restart {
			select {
			case <-ctx.Done():
				cancel()
				if timer != nil {
					timer.Stop()
				}
				return nil
			case <-resync:
				restart = true
			case event, ok := <-events:
				if !ok {
					select {
					case err := <-watchErrors:
						if err != nil {
							a.logger.WarnContext(ctx, "etcd watch ended", "error", err)
						}
					default:
					}
					restart = true
					continue
				}
				if !a.controller.HasTarget(event.TargetID) {
					continue
				}
				var err error
				if event.Deleted {
					err = a.controller.HandleDelete(ctx, event.TargetID)
				} else {
					err = a.controller.Reconcile(ctx, event.TargetID, event.Generation, event.Revision)
				}
				if err != nil {
					a.logger.ErrorContext(ctx, "reconcile watch event failed", "target", event.TargetID, "error", err)
				}
			case err, ok := <-watchErrors:
				if ok && err != nil {
					a.logger.WarnContext(ctx, "etcd watch failed", "error", err)
				}
				restart = true
			}
		}
		cancel()
		if timer != nil {
			timer.Stop()
		}
		if !sleepContext(ctx, a.retryDelay(1)) {
			return nil
		}
	}
}

func (a *Application) retryDelay(failures int) time.Duration {
	delay := a.config.Watch.RetryMin
	for range max(0, failures-1) {
		if delay >= a.config.Watch.RetryMax/2 {
			delay = a.config.Watch.RetryMax
			break
		}
		delay *= 2
	}
	if delay > a.config.Watch.RetryMax {
		delay = a.config.Watch.RetryMax
	}
	jitter := a.config.Watch.JitterRatio
	if jitter > 0 {
		delay -= time.Duration(rand.Float64() * jitter * float64(delay))
	}
	return delay
}

func sleepContext(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-timer.C:
		return true
	case <-ctx.Done():
		return false
	}
}
