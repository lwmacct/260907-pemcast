// Package publisher applies validated local v6 packs to etcd.
package publisher

import (
	"context"
	"fmt"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	"github.com/lwmacct/260907-pemcast/internal/pack"
)

// Outcome reports whether publication changed the active pointer.
type Outcome string

const (
	OutcomePublished Outcome = "published"
	OutcomeNoOp      Outcome = "no-op"
)

// KV is the exact etcd surface required by the v6 staged publisher.
type KV interface {
	Prefix() string
	ActiveKey(targetID string) string
	BundleKey(targetID, generation string) string
	Get(ctx context.Context, key string) (etcdsource.Value, error)
	StageBundleIfAbsent(ctx context.Context, bundleKey, bundleValue string) (bool, error)
	SwapActive(
		ctx context.Context,
		activeKey, generation string,
		expected etcdsource.ActiveCondition,
	) (bool, error)
}

// Publish stages an immutable bundle and commits its active pointer with CAS.
func Publish(ctx context.Context, kv KV, result pack.Result) (Outcome, error) {
	if err := pack.Validate(result); err != nil {
		return "", err
	}
	metadata := result.Metadata
	if metadata.EtcdPrefix != kv.Prefix() {
		return "", fmt.Errorf(
			"pack etcd prefix %q does not match configured prefix %q",
			metadata.EtcdPrefix, kv.Prefix(),
		)
	}
	if metadata.ActiveKey != kv.ActiveKey(metadata.TargetID) {
		return "", fmt.Errorf("pack active key does not match configured keyspace")
	}
	if metadata.BundleKey != kv.BundleKey(metadata.TargetID, metadata.Generation) {
		return "", fmt.Errorf("pack bundle key does not match configured keyspace")
	}

	created, err := kv.StageBundleIfAbsent(ctx, metadata.BundleKey, string(result.Bundle))
	if err != nil {
		return "", err
	}
	if !created {
		if err := requireExistingBundle(ctx, kv, metadata.BundleKey, result.Bundle); err != nil {
			return "", err
		}
	}

	expected, err := captureActive(ctx, kv, metadata.ActiveKey)
	if err != nil {
		return "", err
	}
	if expected.Exists && expected.Generation == metadata.Generation {
		return OutcomeNoOp, nil
	}
	updated, err := kv.SwapActive(ctx, metadata.ActiveKey, metadata.Generation, expected)
	if err != nil {
		return "", err
	}
	if !updated {
		return "", fmt.Errorf("active pointer changed during publication; retry with the same pack")
	}
	return OutcomePublished, nil
}

func requireExistingBundle(ctx context.Context, kv KV, key string, expected []byte) error {
	value, err := kv.Get(ctx, key)
	if err != nil {
		return fmt.Errorf("read existing bundle: %w", err)
	}
	if !value.Exists {
		return fmt.Errorf("existing bundle %q disappeared after staging", key)
	}
	if value.Data != string(expected) {
		return fmt.Errorf("content-addressed generation %q contains different bytes", key)
	}
	return nil
}

func captureActive(ctx context.Context, kv KV, key string) (etcdsource.ActiveCondition, error) {
	value, err := kv.Get(ctx, key)
	if err != nil {
		return etcdsource.ActiveCondition{}, fmt.Errorf("read active pointer: %w", err)
	}
	if value.Exists && !bundle.SafeName(value.Data) {
		return etcdsource.ActiveCondition{}, fmt.Errorf("active pointer %q is unsafe", value.Data)
	}
	return etcdsource.ActiveCondition{
		Generation:  value.Data,
		ModRevision: value.ModRevision,
		Exists:      value.Exists,
	}, nil
}
