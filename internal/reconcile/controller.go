// Package reconcile coordinates remote fetch, validation, local activation, and hooks.
package reconcile

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sort"
	"sync"
	"time"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/deploy"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	"github.com/lwmacct/260907-pemcast/internal/hook"
)

// Source is the remote capability needed by the controller.
type Source interface {
	FetchBundle(context.Context, string, string, int64) (*bundle.Material, error)
}

// Deployer activates verified material below one configured output root.
type Deployer interface {
	Activate(*bundle.Material, config.Output) (deploy.Result, error)
}

// HookRunner invokes one configured post-activation hook.
type HookRunner interface {
	Run(context.Context, config.Hook, hook.Event) error
}

type pendingHook struct {
	target      config.Target
	material    *bundle.Material
	event       hook.Event
	attempt     int
	nextRetry   time.Time
	lastFailure string
}

type validityState struct {
	generation      string
	notAfter        time.Time
	minimumValidity time.Duration
	rejectExpired   bool
}

// Controller reconciles configured targets. Each target is serialized while
// different targets may execute concurrently.
type Controller struct {
	source       Source
	deployer     Deployer
	hooks        HookRunner
	hookMu       sync.Mutex
	hookDue      map[string]*pendingHook
	validityMu   sync.Mutex
	validity     map[string]validityState
	targets      map[string]config.Target
	dryRun       bool
	semaphore    chan struct{}
	locks        sync.Map
	logger       *slog.Logger
	hookRetryMin time.Duration
	hookRetryMax time.Duration
}

func New(source Source, deployer Deployer, hooks HookRunner, cfg config.Agent, logger *slog.Logger) *Controller {
	targets := make(map[string]config.Target, len(cfg.Targets))
	for _, target := range cfg.Targets {
		targets[target.ID] = target
	}
	return &Controller{
		source: source, deployer: deployer, hooks: hooks,
		hookDue:  make(map[string]*pendingHook),
		validity: make(map[string]validityState),
		targets:  targets, dryRun: cfg.DryRun, semaphore: make(chan struct{}, cfg.MaxConcurrent),
		logger: logger, hookRetryMin: time.Second, hookRetryMax: 30 * time.Second,
	}
}

func (c *Controller) HasTarget(id string) bool { _, ok := c.targets[id]; return ok }

func (c *Controller) TargetIDs() []string {
	ids := make([]string, 0, len(c.targets))
	for id := range c.targets {
		ids = append(ids, id)
	}
	sort.Strings(ids)
	return ids
}

func (c *Controller) Reconcile(ctx context.Context, id, generation string, revision int64) error {
	return c.reconcile(ctx, id, generation, revision, false)
}

func (c *Controller) reconcile(
	ctx context.Context, id, generation string, revision int64, runUnchangedHook bool,
) error {
	target, ok := c.targets[id]
	if !ok {
		return nil
	}
	lockValue, _ := c.locks.LoadOrStore(id, &sync.Mutex{})
	lock, ok := lockValue.(*sync.Mutex)
	if !ok {
		return fmt.Errorf("target %q has an invalid reconcile lock", id)
	}
	lock.Lock()
	defer lock.Unlock()
	select {
	case c.semaphore <- struct{}{}:
		defer func() { <-c.semaphore }()
	case <-ctx.Done():
		return ctx.Err()
	}
	material, err := c.source.FetchBundle(ctx, id, generation, revision)
	if err != nil {
		return fmt.Errorf("fetch target %q generation %q: %w", id, generation, err)
	}
	if material.Manifest.Type != target.Type {
		return fmt.Errorf(
			"target %q expects bundle type %q but received %q",
			id, target.Type, material.Manifest.Type,
		)
	}
	if err := bundle.ValidatePolicy(material, bundle.Policy{
		Now: time.Now(), RejectExpired: target.Validation.RejectExpired,
		MinimumValidity: target.Validation.MinimumValidity, ServerNames: target.Validation.ServerNames,
	}); err != nil {
		return fmt.Errorf("validate target %q: %w", id, err)
	}
	c.recordValidity(target, generation, material)
	if c.dryRun {
		c.logger.InfoContext(ctx, "bundle validated in dry-run mode", "target", id, "generation", generation, "digest", material.Digest)
		return nil
	}
	result, err := c.deployer.Activate(material, target.Output)
	if err != nil {
		return fmt.Errorf("deploy target %q: %w", id, err)
	}
	if !result.Changed && !c.hasPendingHook(id) && !runUnchangedHook {
		return nil
	}
	event := hookEvent(target, generation, revision, material, result)
	if err := c.hooks.Run(ctx, target.Hook, event); err != nil {
		c.recordHookFailure(target, material, event, err)
		return fmt.Errorf("run target %q hook: %w", id, err)
	}
	c.clearHookFailure(id)
	c.logger.InfoContext(ctx, "certificate bundle activated", "target", id, "type", target.Type, "generation", generation, "digest", material.Digest)
	return nil
}

func (c *Controller) HandleDelete(ctx context.Context, id string) error {
	target, ok := c.targets[id]
	if !ok || target.DeletePolicy == "retain" {
		c.logger.WarnContext(ctx, "remote active pointer deleted; retaining local release", "target", id)
		return nil
	}
	return fmt.Errorf("target %q active pointer was deleted", id)
}

// ReconcileSnapshot processes the configured intersection of one active snapshot.
func (c *Controller) ReconcileSnapshot(
	ctx context.Context, snapshot etcdsource.Snapshot, runUnchangedHooks bool,
) error {
	var wg sync.WaitGroup
	errorsByTarget := make(chan error, len(c.targets))
	for _, id := range c.TargetIDs() {
		generation, ok := snapshot.Active[id]
		if !ok {
			if err := c.HandleDelete(ctx, id); err != nil {
				errorsByTarget <- err
			}
			continue
		}
		wg.Go(func() {
			if err := c.reconcile(ctx, id, generation, snapshot.Revision, runUnchangedHooks); err != nil {
				errorsByTarget <- err
			}
		})
	}
	wg.Wait()
	close(errorsByTarget)
	var joined []error
	for err := range errorsByTarget {
		joined = append(joined, err)
	}
	return errors.Join(joined...)
}

func hookEvent(
	target config.Target, generation string, revision int64,
	material *bundle.Material, result deploy.Result,
) hook.Event {
	changed := make([]string, 0, len(target.Output.Mappings))
	for _, mapping := range target.Output.Mappings {
		changed = append(changed, mapping.Local)
	}
	releaseDir := result.ReleaseDir
	if releaseDir == "" {
		releaseDir = result.CurrentDir
	}
	return hook.Event{
		TargetID: target.ID, Generation: generation, EtcdRevision: revision,
		ReleaseDir: releaseDir, CurrentDir: result.CurrentDir, ChangedFiles: changed,
		BundleSHA256: material.Digest, ActivatedAt: time.Now(),
	}
}

func (c *Controller) hasPendingHook(id string) bool {
	c.hookMu.Lock()
	defer c.hookMu.Unlock()
	_, exists := c.hookDue[id]
	return exists
}

func (c *Controller) recordHookFailure(
	target config.Target, material *bundle.Material, event hook.Event, runErr error,
) {
	c.hookMu.Lock()
	defer c.hookMu.Unlock()
	pending := c.hookDue[target.ID]
	if pending == nil {
		pending = &pendingHook{}
		c.hookDue[target.ID] = pending
	}
	pending.target = target
	pending.material = material
	pending.event = event
	pending.attempt++
	pending.nextRetry = time.Now().Add(c.hookDelay(pending.attempt))
	pending.lastFailure = runErr.Error()
}

func (c *Controller) clearHookFailure(id string) {
	c.hookMu.Lock()
	defer c.hookMu.Unlock()
	delete(c.hookDue, id)
}

func (c *Controller) hookDelay(failures int) time.Duration {
	delay := c.hookRetryMin
	for range max(0, failures-1) {
		if delay >= c.hookRetryMax/2 {
			delay = c.hookRetryMax
			break
		}
		delay *= 2
	}
	return min(delay, c.hookRetryMax)
}

// NextHookRetry returns the earliest independent hook retry deadline.
func (c *Controller) NextHookRetry() (time.Time, bool) {
	c.hookMu.Lock()
	defer c.hookMu.Unlock()
	var next time.Time
	found := false
	for _, pending := range c.hookDue {
		if !found || pending.nextRetry.Before(next) {
			next = pending.nextRetry
			found = true
		}
	}
	return next, found
}

// RetryDueHooks re-verifies pending local releases and retries due hooks. It
// returns the next retry deadline.
func (c *Controller) RetryDueHooks(ctx context.Context, now time.Time) time.Time {
	ids := c.dueHookIDs(now)
	for _, id := range ids {
		c.retryHook(ctx, id, now)
	}
	next, _ := c.NextHookRetry()
	return next
}

func (c *Controller) dueHookIDs(now time.Time) []string {
	c.hookMu.Lock()
	defer c.hookMu.Unlock()
	ids := make([]string, 0, len(c.hookDue))
	for id, candidate := range c.hookDue {
		if !candidate.nextRetry.After(now) {
			ids = append(ids, id)
		}
	}
	sort.Strings(ids)
	return ids
}

func (c *Controller) retryHook(ctx context.Context, id string, now time.Time) {
	lockValue, _ := c.locks.LoadOrStore(id, &sync.Mutex{})
	lock, ok := lockValue.(*sync.Mutex)
	if !ok {
		c.logger.ErrorContext(ctx, "hook retry target lock is invalid", "target", id)
		return
	}
	lock.Lock()
	defer lock.Unlock()

	select {
	case c.semaphore <- struct{}{}:
		defer func() { <-c.semaphore }()
	case <-ctx.Done():
		return
	}

	c.hookMu.Lock()
	pending, exists := c.hookDue[id]
	if !exists || pending.nextRetry.After(now) {
		c.hookMu.Unlock()
		return
	}
	c.hookMu.Unlock()

	result, err := c.deployer.Activate(pending.material, pending.target.Output)
	if err != nil {
		pending.attempt++
		pending.nextRetry = time.Now().Add(c.hookDelay(pending.attempt))
		pending.lastFailure = err.Error()
		c.logger.ErrorContext(ctx, "verify release before hook retry failed", "target", id, "error", err)
		return
	}
	event := pending.event
	if result.ReleaseDir != "" {
		event.ReleaseDir = result.ReleaseDir
	}
	event.CurrentDir = result.CurrentDir
	if err := c.hooks.Run(ctx, pending.target.Hook, event); err != nil {
		pending.attempt++
		pending.nextRetry = time.Now().Add(c.hookDelay(pending.attempt))
		pending.lastFailure = err.Error()
		c.logger.ErrorContext(ctx, "hook retry failed", "target", id, "error", err)
		return
	}
	c.clearHookFailure(id)
	c.logger.InfoContext(
		ctx, "certificate bundle hook synchronized",
		"target", id, "generation", event.Generation, "digest", pending.material.Digest,
	)
}

func (c *Controller) recordValidity(target config.Target, generation string, material *bundle.Material) {
	if target.Validation.MinimumValidity <= 0 && !target.Validation.RejectExpired {
		c.validityMu.Lock()
		defer c.validityMu.Unlock()
		delete(c.validity, target.ID)
		return
	}
	if len(material.Certificates) == 0 {
		return
	}
	notAfter := material.Certificates[0].NotAfter
	for _, certificate := range material.Certificates[1:] {
		if certificate.NotAfter.Before(notAfter) {
			notAfter = certificate.NotAfter
		}
	}
	state := validityState{
		generation: generation, notAfter: notAfter,
		minimumValidity: target.Validation.MinimumValidity,
		rejectExpired:   target.Validation.RejectExpired,
	}
	c.validityMu.Lock()
	defer c.validityMu.Unlock()
	c.validity[target.ID] = state
}

// NextValidityCheck returns the earliest local certificate policy boundary.
func (c *Controller) NextValidityCheck() (time.Time, bool) {
	c.validityMu.Lock()
	defer c.validityMu.Unlock()
	var next time.Time
	found := false
	for _, state := range c.validity {
		boundary := state.notAfter
		if state.minimumValidity > 0 {
			boundary = boundary.Add(-state.minimumValidity)
		}
		if !found || boundary.Before(next) {
			next = boundary
			found = true
		}
	}
	return next, found
}

// CheckValidity reports one local warning when a cached active generation
// crosses its target policy boundary. It intentionally does not contact etcd.
func (c *Controller) CheckValidity(ctx context.Context, now time.Time) {
	type dueState struct {
		id    string
		state validityState
	}
	var due []dueState
	c.validityMu.Lock()
	for id, state := range c.validity {
		boundary := state.notAfter
		if state.minimumValidity > 0 {
			boundary = boundary.Add(-state.minimumValidity)
		}
		if !now.Before(boundary) {
			due = append(due, dueState{id: id, state: state})
			delete(c.validity, id)
		}
	}
	c.validityMu.Unlock()
	sort.Slice(due, func(i, j int) bool { return due[i].id < due[j].id })
	for _, item := range due {
		remaining := item.state.notAfter.Sub(now).Round(time.Second)
		if item.state.minimumValidity > 0 {
			c.logger.ErrorContext(
				ctx, "certificate remaining validity is below policy",
				"target", item.id, "generation", item.state.generation,
				"remaining", remaining, "minimum", item.state.minimumValidity,
			)
			continue
		}
		c.logger.ErrorContext(
			ctx, "certificate expired",
			"target", item.id, "generation", item.state.generation,
			"expired-at", item.state.notAfter,
		)
	}
}
