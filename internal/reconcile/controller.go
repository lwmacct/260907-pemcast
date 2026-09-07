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
	"github.com/lwmacct/260907-pemcast/internal/state"
)

// Source is the remote capability needed by the controller.
type Source interface {
	FetchBundle(context.Context, string, string, int64) (*bundle.Material, error)
}

// Controller reconciles configured targets. Each target is serialized while
// different targets may execute concurrently.
type Controller struct {
	source    Source
	deployer  *deploy.Deployer
	hooks     *hook.Runner
	state     *state.Store
	targets   map[string]config.Target
	dryRun    bool
	semaphore chan struct{}
	locks     sync.Map
	logger    *slog.Logger
}

func New(source Source, deployer *deploy.Deployer, hooks *hook.Runner, store *state.Store, cfg config.Agent, logger *slog.Logger) *Controller {
	targets := make(map[string]config.Target, len(cfg.Targets))
	for _, target := range cfg.Targets {
		targets[target.ID] = target
	}
	return &Controller{
		source: source, deployer: deployer, hooks: hooks, state: store,
		targets: targets, dryRun: cfg.DryRun, semaphore: make(chan struct{}, cfg.MaxConcurrent), logger: logger,
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
	target, ok := c.targets[id]
	if !ok {
		return nil
	}
	lockValue, _ := c.locks.LoadOrStore(id, &sync.Mutex{})
	lock := lockValue.(*sync.Mutex)
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
	certPEM, ok := material.Files[target.Validation.Certificate]
	if !ok {
		return fmt.Errorf("target %q certificate file %q is absent", id, target.Validation.Certificate)
	}
	keyPEM, ok := material.Files[target.Validation.PrivateKey]
	if !ok {
		return fmt.Errorf("target %q private key file %q is absent", id, target.Validation.PrivateKey)
	}
	leaf, err := bundle.ValidateKeyPair(certPEM, keyPEM)
	if err != nil {
		return fmt.Errorf("validate target %q: %w", id, err)
	}
	if err := bundle.ValidateValidity(leaf, time.Now(), target.Validation.RejectExpired, target.Validation.MinimumValidity); err != nil {
		return fmt.Errorf("validate target %q: %w", id, err)
	}
	if !material.Manifest.HasPair(target.Validation.Certificate, target.Validation.PrivateKey) {
		return fmt.Errorf("target %q certificate/private-key pair is not declared by the manifest", id)
	}
	previous, err := c.state.Load(id)
	if err != nil {
		return err
	}
	if c.dryRun {
		c.logger.InfoContext(ctx, "bundle validated in dry-run mode", "target", id, "generation", generation, "digest", material.Digest)
		return nil
	}
	result, err := c.deployer.Activate(material, target.Output)
	if err != nil {
		return fmt.Errorf("deploy target %q: %w", id, err)
	}
	if !result.Changed && previous.HookError == "" {
		if previous.Generation != generation || previous.Digest != material.Digest {
			previous.Generation = generation
			previous.Digest = material.Digest
			previous.Revision = revision
			if err := c.state.Save(id, previous); err != nil {
				return err
			}
		}
		return nil
	}
	changed := make([]string, 0, len(target.Output.Mappings))
	for _, mapping := range target.Output.Mappings {
		changed = append(changed, mapping.Local)
	}
	releaseDir := result.ReleaseDir
	if releaseDir == "" {
		releaseDir = result.CurrentDir
	}
	event := hook.Event{
		TargetID: id, Generation: generation, PreviousGeneration: previous.Generation,
		EtcdRevision: revision, ReleaseDir: releaseDir, CurrentDir: result.CurrentDir,
		ChangedFiles: changed, BundleSHA256: material.Digest, ActivatedAt: time.Now(),
	}
	next := state.Target{Generation: generation, Digest: material.Digest, Revision: revision, ActivatedAt: event.ActivatedAt}
	if err := c.hooks.Run(ctx, target.Hook, event); err != nil {
		next.HookError = err.Error()
		if saveErr := c.state.Save(id, next); saveErr != nil {
			return fmt.Errorf("%v; save hook failure: %w", err, saveErr)
		}
		return fmt.Errorf("run target %q hook: %w", id, err)
	}
	if err := c.state.Save(id, next); err != nil {
		return err
	}
	c.logger.InfoContext(ctx, "certificate bundle activated", "target", id, "generation", generation, "digest", material.Digest, "not_after", leaf.NotAfter)
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
func (c *Controller) ReconcileSnapshot(ctx context.Context, snapshot etcdsource.Snapshot) error {
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
			if err := c.Reconcile(ctx, id, generation, snapshot.Revision); err != nil {
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
