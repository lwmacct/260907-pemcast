// Package upgrade migrates one etcd namespace from the previous pemcast
// protocol version to the current protocol version.
package upgrade

import (
	"context"
	"errors"
	"fmt"
	"path"
	"sort"
	"strings"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	"github.com/lwmacct/260907-pemcast/internal/keyspace"
	legacy "github.com/lwmacct/260907-pemcast/internal/upgrade/legacy"
)

// KV is the exact etcd surface needed by the v5-to-v6 migration.
type KV interface {
	Prefix() string
	Get(ctx context.Context, key string) (etcdsource.Value, error)
	GetAt(ctx context.Context, key string, revision int64) (etcdsource.Value, error)
	SnapshotActivePrefix(ctx context.Context, activePrefix string) (etcdsource.PrefixSnapshot, error)
	StageBundleIfAbsent(ctx context.Context, key, value string) (bool, error)
	SwapActiveFromSource(
		ctx context.Context,
		sourceKey string,
		source etcdsource.ActiveCondition,
		destinationKey, generation string,
		destination etcdsource.ActiveCondition,
	) (bool, error)
	DeletePrefix(ctx context.Context, prefix string) (int64, error)
}

// Options controls one prefix migration.
type Options struct {
	Prefix        string
	DefaultType   string
	TargetTypes   map[string]string
	DryRun        bool
	DeleteOld     bool
	ConfirmDelete bool
}

// TargetResult reports one target's migration outcome.
type TargetResult struct {
	TargetID         string `json:"target-id"`
	Type             string `json:"type"`
	SourceGeneration string `json:"source-generation"`
	ResultGeneration string `json:"result-generation"`
	Status           string `json:"status"`
}

// Result reports the complete prefix migration outcome.
type Result struct {
	Prefix      string         `json:"prefix"`
	Source      string         `json:"source"`
	Destination string         `json:"destination"`
	DryRun      bool           `json:"dry-run"`
	DeletedOld  bool           `json:"deleted-old"`
	DeletedKeys int64          `json:"deleted-keys"`
	Targets     []TargetResult `json:"targets"`
}

type targetPlan struct {
	id                string
	bundleType        string
	sourceKey         string
	sourceGeneration  string
	sourceModRevision int64
	bundleKey         string
	bundleValue       []byte
	resultGeneration  string
	alreadyMigrated   bool
}

type migrationPlan struct {
	prefix       string
	sourceRoot   string
	activePrefix string
	revision     int64
	targets      []targetPlan
}

// Upgrade validates and migrates every active v5 target below one prefix.
func Upgrade(ctx context.Context, kv KV, options Options) (Result, error) {
	if options.DeleteOld && !options.DryRun && !options.ConfirmDelete {
		return Result{}, fmt.Errorf("--delete-old-v5 also requires --yes")
	}
	keys, err := keyspace.NewKeys(options.Prefix)
	if err != nil {
		return Result{}, err
	}
	if kv.Prefix() != keys.Prefix() {
		return Result{}, fmt.Errorf(
			"upgrade client prefix %q does not match configured prefix %q",
			kv.Prefix(), keys.Prefix(),
		)
	}

	plan, err := buildPlan(ctx, kv, keys.Prefix(), options)
	if err != nil {
		return Result{}, err
	}
	result := Result{
		Prefix: keys.Prefix(), Source: "pemcast/v5", Destination: "pemcast/v6",
		DryRun: options.DryRun, DeletedOld: false,
	}
	for _, target := range plan.targets {
		status := "migrated"
		if target.alreadyMigrated {
			status = "already-migrated"
		}
		result.Targets = append(result.Targets, TargetResult{
			TargetID: target.id, Type: target.bundleType,
			SourceGeneration: target.sourceGeneration,
			ResultGeneration: target.resultGeneration, Status: status,
		})
	}
	if options.DryRun {
		for index := range result.Targets {
			if result.Targets[index].Status == "migrated" {
				result.Targets[index].Status = "would-migrate"
			}
		}
		return result, nil
	}

	if err := applyPlan(ctx, kv, plan); err != nil {
		return result, err
	}
	if err := verifyPlan(ctx, kv, plan); err != nil {
		return result, err
	}
	if options.DeleteOld {
		deleted, err := kv.DeletePrefix(ctx, plan.sourceRoot+"/")
		if err != nil {
			return result, err
		}
		result.DeletedOld = true
		result.DeletedKeys = deleted
	}
	return result, nil
}

func buildPlan(ctx context.Context, kv KV, prefix string, options Options) (*migrationPlan, error) {
	sourceRoot := path.Join(prefix, legacy.ProtocolRoot)
	if sourceRoot == "/" || path.Clean(sourceRoot) != sourceRoot {
		return nil, fmt.Errorf("unsafe v5 protocol root %q", sourceRoot)
	}
	sourceActivePrefix := sourceRoot + "/active/"
	sourceBundlePrefix := sourceRoot + "/bundles/"
	destinationKeys, err := keyspace.NewKeys(prefix)
	if err != nil {
		return nil, err
	}

	unsupported, err := kv.SnapshotActivePrefix(ctx, path.Join(prefix, "/v4")+"/active/")
	if err != nil {
		return nil, fmt.Errorf("inspect unsupported v4 active data: %w", err)
	}
	if len(unsupported.Active) != 0 {
		return nil, fmt.Errorf("found active v4 targets below %q; only v5 can be upgraded to v6", prefix)
	}

	sourceSnapshot, err := kv.SnapshotActivePrefix(ctx, sourceActivePrefix)
	if err != nil {
		return nil, fmt.Errorf("snapshot v5 active pointers: %w", err)
	}
	destinationSnapshot, err := kv.SnapshotActivePrefix(ctx, destinationKeys.ActivePrefix())
	if err != nil {
		return nil, fmt.Errorf("snapshot v6 active pointers: %w", err)
	}
	if len(sourceSnapshot.Active) == 0 {
		if len(destinationSnapshot.Active) != 0 {
			return nil, fmt.Errorf("v5 active prefix is empty but v6 is already populated; upgrade is only v5-to-v6")
		}
		return nil, fmt.Errorf("no active v5 targets below %q", sourceActivePrefix)
	}
	for targetID := range destinationSnapshot.Active {
		if _, ok := sourceSnapshot.Active[targetID]; !ok {
			return nil, fmt.Errorf(
				"v6 target %q has no corresponding active v5 target; refuse to upgrade a mixed prefix",
				targetID,
			)
		}
	}
	for targetID := range options.TargetTypes {
		if _, ok := sourceSnapshot.Active[targetID]; !ok {
			return nil, fmt.Errorf("target type override %q does not reference an active v5 target", targetID)
		}
	}

	ids := make([]string, 0, len(sourceSnapshot.Active))
	for targetID := range sourceSnapshot.Active {
		ids = append(ids, targetID)
	}
	sort.Strings(ids)

	var validationErrors []error
	plan := &migrationPlan{
		prefix: prefix, sourceRoot: sourceRoot,
		activePrefix: sourceActivePrefix,
		revision:     sourceSnapshot.Revision,
	}
	for _, targetID := range ids {
		source := sourceSnapshot.Active[targetID]
		bundleType := options.DefaultType
		if override, exists := options.TargetTypes[targetID]; exists {
			bundleType = override
		}
		if bundleType != bundle.TypeTLSServer && bundleType != bundle.TypeTLSClient {
			validationErrors = append(validationErrors, fmt.Errorf(
				"target %q has missing or invalid destination type %q; use tls-server or tls-client",
				targetID, bundleType,
			))
			continue
		}

		sourceBundleKey := sourceBundlePrefix + targetID + "/" + source.Generation
		sourceValue, err := kv.GetAt(ctx, sourceBundleKey, sourceSnapshot.Revision)
		if err != nil {
			return nil, fmt.Errorf("read v5 bundle for target %q: %w", targetID, err)
		}
		if !sourceValue.Exists {
			return nil, fmt.Errorf("v5 bundle %q is missing at revision %d", sourceBundleKey, sourceSnapshot.Revision)
		}
		material, err := legacy.Decode(targetID, source.Generation, []byte(sourceValue.Data))
		if err != nil {
			return nil, fmt.Errorf("validate v5 target %q: %w", targetID, err)
		}
		_, encoded, digest, err := legacy.ConvertTLS(material, bundleType)
		if err != nil {
			validationErrors = append(validationErrors, err)
			continue
		}
		resultGeneration := bundle.Generation(digest)
		destinationPointer, exists := destinationSnapshot.Active[targetID]
		if exists && destinationPointer.Generation != resultGeneration {
			return nil, fmt.Errorf(
				"v6 target %q is active with generation %q but upgrade would produce %q",
				targetID, destinationPointer.Generation, resultGeneration,
			)
		}
		plan.targets = append(plan.targets, targetPlan{
			id: targetID, bundleType: bundleType,
			sourceKey:        sourceActivePrefix + targetID,
			sourceGeneration: source.Generation, sourceModRevision: source.ModRevision,
			bundleKey:   destinationKeys.BundleKey(targetID, resultGeneration),
			bundleValue: encoded, resultGeneration: resultGeneration,
			alreadyMigrated: exists,
		})
	}
	if err := errors.Join(validationErrors...); err != nil {
		return nil, err
	}
	return plan, nil
}

func applyPlan(ctx context.Context, kv KV, plan *migrationPlan) error {
	for _, target := range plan.targets {
		created, err := kv.StageBundleIfAbsent(ctx, target.bundleKey, string(target.bundleValue))
		if err != nil {
			return fmt.Errorf("stage v6 bundle for target %q: %w", target.id, err)
		}
		if !created {
			existing, err := kv.Get(ctx, target.bundleKey)
			if err != nil {
				return fmt.Errorf("read existing v6 bundle for target %q: %w", target.id, err)
			}
			if !existing.Exists || existing.Data != string(target.bundleValue) {
				return fmt.Errorf("existing v6 bundle %q contains different bytes", target.bundleKey)
			}
		}
		if target.alreadyMigrated {
			continue
		}

		source, err := kv.Get(ctx, target.sourceKey)
		if err != nil {
			return fmt.Errorf("capture current v5 pointer for target %q: %w", target.id, err)
		}
		if !source.Exists || source.Data != target.sourceGeneration || source.ModRevision != target.sourceModRevision {
			return fmt.Errorf("v5 active pointer for target %q changed during upgrade; retry the upgrade", target.id)
		}
		destinationKey := destinationActiveKey(plan.prefix, target.id)
		destination, err := kv.Get(ctx, destinationKey)
		if err != nil {
			return fmt.Errorf("capture current v6 pointer for target %q: %w", target.id, err)
		}
		if destination.Exists {
			return fmt.Errorf("v6 active pointer for target %q appeared during upgrade", target.id)
		}
		updated, err := kv.SwapActiveFromSource(
			ctx, target.sourceKey, etcdsource.ActiveCondition{
				Generation: target.sourceGeneration, ModRevision: target.sourceModRevision, Exists: true,
			},
			destinationKey, target.resultGeneration, etcdsource.ActiveCondition{},
		)
		if err != nil {
			return fmt.Errorf("commit v6 pointer for target %q: %w", target.id, err)
		}
		if !updated {
			return fmt.Errorf("v5 or v6 active pointer changed during target %q commit; retry the upgrade", target.id)
		}
	}
	return nil
}

func verifyPlan(ctx context.Context, kv KV, plan *migrationPlan) error {
	sourceSnapshot, err := kv.SnapshotActivePrefix(ctx, plan.activePrefix)
	if err != nil {
		return fmt.Errorf("postverify v5 active pointers: %w", err)
	}
	destinationSnapshot, err := kv.SnapshotActivePrefix(ctx, destinationActivePrefix(plan.prefix))
	if err != nil {
		return fmt.Errorf("postverify v6 active pointers: %w", err)
	}
	if len(sourceSnapshot.Active) != len(plan.targets) || len(destinationSnapshot.Active) != len(plan.targets) {
		return fmt.Errorf("postverify found an unexpected active target set")
	}
	for _, target := range plan.targets {
		source, ok := sourceSnapshot.Active[target.id]
		if !ok || source.Generation != target.sourceGeneration || source.ModRevision != target.sourceModRevision {
			return fmt.Errorf("v5 active pointer for target %q changed after migration", target.id)
		}
		destination, ok := destinationSnapshot.Active[target.id]
		if !ok || destination.Generation != target.resultGeneration {
			return fmt.Errorf("v6 active pointer for target %q does not match migration result", target.id)
		}
		value, err := kv.Get(ctx, target.bundleKey)
		if err != nil {
			return fmt.Errorf("postverify v6 bundle for target %q: %w", target.id, err)
		}
		if !value.Exists || value.Data != string(target.bundleValue) {
			return fmt.Errorf("v6 bundle %q changed after migration", target.bundleKey)
		}
	}
	return nil
}

func destinationActivePrefix(prefix string) string {
	return path.Join(prefix, "/v6") + "/active/"
}

func destinationActiveKey(prefix, targetID string) string {
	return destinationActivePrefix(prefix) + targetID
}

// NormalizeTargetTypes parses repeated `target-id=type` CLI values.
func NormalizeTargetTypes(values []string) (map[string]string, error) {
	targetTypes := make(map[string]string, len(values))
	for _, value := range values {
		targetID, bundleType, found := strings.Cut(value, "=")
		if !found || !bundle.SafeName(targetID) {
			return nil, fmt.Errorf("invalid target type mapping %q; want target-id=tls-server|tls-client", value)
		}
		if bundleType != bundle.TypeTLSServer && bundleType != bundle.TypeTLSClient {
			return nil, fmt.Errorf("invalid target type %q for target %q", bundleType, targetID)
		}
		if _, exists := targetTypes[targetID]; exists {
			return nil, fmt.Errorf("target %q is specified more than once", targetID)
		}
		targetTypes[targetID] = bundleType
	}
	return targetTypes, nil
}
