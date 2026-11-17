package agent

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"time"

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/deploy"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	"github.com/lwmacct/260907-pemcast/internal/hook"
	"github.com/lwmacct/260907-pemcast/internal/reconcile"
)

// Application owns the agent runtime and its dependencies.
type Application struct {
	config     config.Agent
	source     *etcdsource.Client
	controller *reconcile.Controller
	locks      []*deploy.RootLock
	logger     *slog.Logger
}

func New(cfg config.Config, logger *slog.Logger) (*Application, error) {
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	if logger == nil {
		logger = slog.Default()
	}
	var locks []*deploy.RootLock
	var err error
	if !cfg.Agent.DryRun {
		locks, err = deploy.LockRoots(cfg.Agent.Targets)
		if err != nil {
			return nil, err
		}
	}
	source, err := etcdsource.New(cfg.Agent.Etcd)
	if err != nil {
		closeRootLocks(locks)
		return nil, err
	}
	controller := reconcile.New(source, deploy.New(), hook.New(), cfg.Agent, logger)
	return &Application{config: cfg.Agent, source: source, controller: controller, locks: locks, logger: logger}, nil
}

func (a *Application) Close() error {
	sourceErr := a.source.Close()
	var etcdCloseErr error
	if sourceErr != nil && !errors.Is(sourceErr, context.Canceled) {
		etcdCloseErr = sourceErr
	}
	var lockErrs []error
	for _, lock := range a.locks {
		if err := lock.Close(); err != nil {
			lockErrs = append(lockErrs, err)
		}
	}
	return errors.Join(append([]error{etcdCloseErr}, lockErrs...)...)
}

func closeRootLocks(locks []*deploy.RootLock) {
	for _, lock := range locks {
		_ = lock.Close()
	}
}

func (a *Application) Run(ctx context.Context) error {
	if a.config.Once {
		snapshot, err := a.source.SnapshotActive(ctx)
		if err != nil {
			return err
		}
		return a.controller.ReconcileSnapshot(ctx, snapshot, true)
	}
	return a.runWatch(ctx)
}

func (a *Application) runWatch(ctx context.Context) error {
	var failures int
	for ctx.Err() == nil {
		snapshot, err := a.source.SnapshotActive(ctx)
		if err != nil {
			failures++
			a.logger.ErrorContext(ctx, "read active snapshot failed", "error", err)
			if !sleepContext(ctx, a.retryDelay(failures)) {
				return nil
			}
			continue
		}
		if err := a.controller.ReconcileSnapshot(ctx, snapshot, true); err != nil {
			a.logger.ErrorContext(ctx, "initial snapshot reconcile failed", "error", err)
		}
		failures = 0
		session := a.source.WatchActive(ctx, snapshot.Revision)
		progressTicker := time.NewTicker(a.config.Watch.ProgressInterval)
		livenessTimer := time.NewTimer(a.config.Watch.ProgressTimeout)
		hookNext, hookExists := a.controller.NextHookRetry()
		validityNext, validityExists := a.controller.NextValidityCheck()
		hookTimer, hookDeadline := deadlineTimer(hookNext, hookExists)
		validityTimer, validityDeadline := deadlineTimer(validityNext, validityExists)
		restart := false
		for !restart {
			select {
			case <-ctx.Done():
				progressTicker.Stop()
				livenessTimer.Stop()
				stopDeadlineTimer(hookTimer)
				stopDeadlineTimer(validityTimer)
				_ = session.Close()
				return nil
			case update, ok := <-session.Updates():
				if !ok {
					select {
					case err := <-session.Errors():
						if err != nil {
							a.logger.WarnContext(ctx, "etcd watch ended", "error", err)
						}
					default:
					}
					restart = true
					continue
				}
				switch update.Kind {
				case etcdsource.WatchCreated, etcdsource.WatchProgress:
					resetTimer(livenessTimer, time.Now().Add(a.config.Watch.ProgressTimeout))
				case etcdsource.WatchEvent:
					event := update.Event
					if event.Revision <= snapshot.Revision {
						resetTimer(livenessTimer, time.Now().Add(a.config.Watch.ProgressTimeout))
						continue
					}
					if a.controller.HasTarget(event.TargetID) {
						var err error
						if event.Deleted {
							err = a.controller.HandleDelete(ctx, event.TargetID)
						} else {
							err = a.controller.Reconcile(ctx, event.TargetID, event.Generation, event.Revision)
						}
						if err != nil {
							a.logger.ErrorContext(
								ctx, "reconcile watch event failed",
								"target", event.TargetID, "error", err,
							)
						}
					}
					resetTimer(livenessTimer, time.Now().Add(a.config.Watch.ProgressTimeout))
					hookNext, _ := a.controller.NextHookRetry()
					validityNext, _ := a.controller.NextValidityCheck()
					resetDeadlineTimer(&hookTimer, &hookDeadline, hookNext)
					resetDeadlineTimer(&validityTimer, &validityDeadline, validityNext)
				}
			case err, ok := <-session.Errors():
				if ok && err != nil {
					a.logger.WarnContext(ctx, "etcd watch failed", "error", err)
				}
				restart = true
			case now := <-progressTicker.C:
				if err := session.RequestProgress(ctx); err != nil {
					a.logger.WarnContext(ctx, "request etcd watch progress failed", "now", now, "error", err)
				}
			case <-livenessTimer.C:
				a.logger.ErrorContext(ctx, "etcd watch liveness timeout")
				restart = true
			case now := <-hookDeadline:
				next := a.controller.RetryDueHooks(ctx, now)
				resetDeadlineTimer(&hookTimer, &hookDeadline, next)
			case now := <-validityDeadline:
				a.controller.CheckValidity(ctx, now)
				nextValidity, _ := a.controller.NextValidityCheck()
				resetDeadlineTimer(&validityTimer, &validityDeadline, nextValidity)
			}
		}
		progressTicker.Stop()
		livenessTimer.Stop()
		stopDeadlineTimer(hookTimer)
		stopDeadlineTimer(validityTimer)
		_ = session.Close()
		if !sleepContext(ctx, a.retryDelay(1)) {
			return nil
		}
	}
	return nil
}

func deadlineTimer(deadline time.Time, exists bool) (*time.Timer, <-chan time.Time) {
	if !exists || deadline.IsZero() {
		return nil, nil
	}
	timer := time.NewTimer(time.Until(deadline))
	return timer, timer.C
}

func stopDeadlineTimer(timer *time.Timer) {
	if timer != nil {
		timer.Stop()
	}
}

func resetDeadlineTimer(timer **time.Timer, channel *<-chan time.Time, deadline time.Time) {
	stopDeadlineTimer(*timer)
	if deadline.IsZero() {
		*timer = nil
		*channel = nil
		return
	}
	*timer = time.NewTimer(time.Until(deadline))
	*channel = (*timer).C
}

func resetTimer(timer *time.Timer, deadline time.Time) {
	if !timer.Stop() {
		select {
		case <-timer.C:
		default:
		}
	}
	timer.Reset(time.Until(deadline))
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
