package agent

import (
	"context"
	"errors"
	"log/slog"
	"math/rand/v2"
	"sync"
	"time"

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/deploy"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	"github.com/lwmacct/260907-pemcast/internal/hook"
	"github.com/lwmacct/260907-pemcast/internal/reconcile"
)

// Application owns the agent runtime and its dependencies.
type Application struct {
	config   config.Agent
	runtimes []*sourceRuntime
	locks    []*deploy.RootLock
	logger   *slog.Logger
}

type sourceRuntime struct {
	name       string
	source     *etcdsource.Client
	controller *reconcile.Controller
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
		locks, err = deploy.LockRoots(cfg.Agent.AllTargets())
		if err != nil {
			return nil, err
		}
	}
	runtimeConfigs, err := cfg.Agent.SourceRuntimes()
	if err != nil {
		closeRootLocks(locks)
		return nil, err
	}
	runtimes := make([]*sourceRuntime, 0, len(runtimeConfigs))
	semaphore := make(chan struct{}, cfg.Agent.MaxConcurrent)
	for _, runtimeConfig := range runtimeConfigs {
		source, err := etcdsource.New(runtimeConfig.Etcd)
		if err != nil {
			closeSources(runtimes)
			closeRootLocks(locks)
			return nil, err
		}
		controllerConfig := reconcile.Config{
			Targets: runtimeConfig.Targets, DryRun: cfg.Agent.DryRun, MaxConcurrent: cfg.Agent.MaxConcurrent,
		}
		controller := reconcile.NewForSource(
			source, deploy.New(), hook.New(), controllerConfig, logger,
			semaphore, runtimeConfig.Name,
		)
		runtimes = append(runtimes, &sourceRuntime{
			name: runtimeConfig.Name, source: source, controller: controller,
		})
	}
	return &Application{config: cfg.Agent, runtimes: runtimes, locks: locks, logger: logger}, nil
}

func (a *Application) Close() error {
	sourceErrs := make([]error, 0, len(a.runtimes))
	for _, runtime := range a.runtimes {
		if err := runtime.source.Close(); err != nil && !errors.Is(err, context.Canceled) {
			sourceErrs = append(sourceErrs, err)
		}
	}
	var lockErrs []error
	for _, lock := range a.locks {
		if err := lock.Close(); err != nil {
			lockErrs = append(lockErrs, err)
		}
	}
	return errors.Join(append(sourceErrs, lockErrs...)...)
}

func closeSources(runtimes []*sourceRuntime) {
	for _, runtime := range runtimes {
		_ = runtime.source.Close()
	}
}

func closeRootLocks(locks []*deploy.RootLock) {
	for _, lock := range locks {
		_ = lock.Close()
	}
}

func (a *Application) Run(ctx context.Context) error {
	if a.config.Once {
		results := make([]error, len(a.runtimes))
		var wg sync.WaitGroup
		for index, runtime := range a.runtimes {
			wg.Go(func() {
				snapshot, err := runtime.source.SnapshotActive(ctx)
				if err == nil {
					err = runtime.controller.ReconcileSnapshot(ctx, snapshot, true)
				}
				results[index] = err
			})
		}
		wg.Wait()
		return errors.Join(results...)
	}

	results := make([]error, len(a.runtimes))
	var wg sync.WaitGroup
	for index, runtime := range a.runtimes {
		wg.Go(func() {
			results[index] = a.runWatch(ctx, runtime)
		})
	}
	wg.Wait()
	return errors.Join(results...)
}

func (a *Application) runWatch(ctx context.Context, runtime *sourceRuntime) error {
	var failures int
	for ctx.Err() == nil {
		snapshot, err := runtime.source.SnapshotActive(ctx)
		if err != nil {
			failures++
			a.logger.ErrorContext(ctx, "read active snapshot failed", "source", a.sourceLabel(runtime), "error", err)
			if !sleepContext(ctx, a.retryDelay(failures)) {
				return nil
			}
			continue
		}
		if err := runtime.controller.ReconcileSnapshot(ctx, snapshot, true); err != nil {
			a.logger.ErrorContext(ctx, "initial snapshot reconcile failed", "source", a.sourceLabel(runtime), "error", err)
		}
		failures = 0
		session := runtime.source.WatchActive(ctx, snapshot.Revision)
		progressTicker := time.NewTicker(a.config.Watch.ProgressInterval)
		livenessTimer := time.NewTimer(a.config.Watch.ProgressTimeout)
		hookNext, hookExists := runtime.controller.NextHookRetry()
		validityNext, validityExists := runtime.controller.NextValidityCheck()
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
							a.logger.WarnContext(ctx, "etcd watch ended", "source", a.sourceLabel(runtime), "error", err)
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
					if runtime.controller.HasTarget(event.TargetID) {
						var err error
						if event.Deleted {
							err = runtime.controller.HandleDelete(ctx, event.TargetID)
						} else {
							err = runtime.controller.Reconcile(ctx, event.TargetID, event.Generation, event.Revision)
						}
						if err != nil {
							a.logger.ErrorContext(
								ctx, "reconcile watch event failed",
								"source", a.sourceLabel(runtime), "target", event.TargetID, "error", err,
							)
						}
					}
					resetTimer(livenessTimer, time.Now().Add(a.config.Watch.ProgressTimeout))
					hookNext, _ := runtime.controller.NextHookRetry()
					validityNext, _ := runtime.controller.NextValidityCheck()
					resetDeadlineTimer(&hookTimer, &hookDeadline, hookNext)
					resetDeadlineTimer(&validityTimer, &validityDeadline, validityNext)
				}
			case err, ok := <-session.Errors():
				if ok && err != nil {
					a.logger.WarnContext(ctx, "etcd watch failed", "source", a.sourceLabel(runtime), "error", err)
				}
				restart = true
			case now := <-progressTicker.C:
				if err := session.RequestProgress(ctx); err != nil {
					a.logger.WarnContext(ctx, "request etcd watch progress failed", "source", a.sourceLabel(runtime), "now", now, "error", err)
				}
			case <-livenessTimer.C:
				a.logger.ErrorContext(ctx, "etcd watch liveness timeout", "source", a.sourceLabel(runtime))
				restart = true
			case now := <-hookDeadline:
				next := runtime.controller.RetryDueHooks(ctx, now)
				resetDeadlineTimer(&hookTimer, &hookDeadline, next)
			case now := <-validityDeadline:
				runtime.controller.CheckValidity(ctx, now)
				nextValidity, _ := runtime.controller.NextValidityCheck()
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

func (a *Application) sourceLabel(runtime *sourceRuntime) string {
	if runtime.name == "" {
		return "default"
	}
	return runtime.name
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
