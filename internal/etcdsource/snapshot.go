package etcdsource

import (
	"context"
	"fmt"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
)

// Snapshot is a linearizable view of the configured active pointers at one etcd revision.
type Snapshot struct {
	Revision int64
	Active   map[string]string
}

// SnapshotTargets retrieves only the configured active pointers in one etcd
// transaction and returns the revision from which a lossless watch can continue.
func (c *Client) SnapshotTargets(ctx context.Context, targetIDs []string) (Snapshot, error) {
	targets, err := c.targetIDSet(targetIDs)
	if err != nil {
		return Snapshot{}, err
	}
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()

	operations := make([]clientv3.Op, 0, len(targetIDs))
	for _, targetID := range targetIDs {
		operations = append(operations, clientv3.OpGet(c.ActiveKey(targetID)))
	}
	response, err := c.client.Txn(requestCtx).Then(operations...).Commit()
	if err != nil {
		return Snapshot{}, fmt.Errorf("read etcd active pointers: %w", err)
	}
	return snapshotFromTxnResponse(response, targets)
}

func snapshotFromTxnResponse(
	response *clientv3.TxnResponse, targets map[string]string,
) (Snapshot, error) {
	if len(response.Responses) != len(targets) {
		return Snapshot{}, fmt.Errorf(
			"exact active-pointer transaction returned %d responses, want %d",
			len(response.Responses), len(targets),
		)
	}
	active := make(map[string]string, len(targets))
	for _, operation := range response.Responses {
		kvs := operation.GetResponseRange().Kvs
		if len(kvs) > 1 {
			return Snapshot{}, fmt.Errorf("exact active-pointer read returned %d keys", len(kvs))
		}
		if len(kvs) == 0 {
			continue
		}
		kv := kvs[0]
		targetID, ok := targets[string(kv.Key)]
		if !ok {
			return Snapshot{}, fmt.Errorf("exact active-pointer read returned unexpected key %q", kv.Key)
		}
		generation := strings.TrimSpace(string(kv.Value))
		if !bundle.SafeName(targetID) || !bundle.SafeName(generation) {
			return Snapshot{}, fmt.Errorf("invalid active pointer %q=%q", kv.Key, kv.Value)
		}
		active[targetID] = generation
	}
	return Snapshot{Revision: response.Header.Revision, Active: active}, nil
}

// FetchBundle reads one immutable single-key bundle at the requested revision.
func (c *Client) FetchBundle(ctx context.Context, targetID, generation string, revision int64) (*bundle.Material, error) {
	if !bundle.SafeName(targetID) || !bundle.SafeName(generation) {
		return nil, fmt.Errorf("unsafe target or generation")
	}
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()
	key := c.BundleKey(targetID, generation)
	options := []clientv3.OpOption{}
	if revision > 0 {
		options = append(options, clientv3.WithRev(revision))
	}
	response, err := c.client.Get(requestCtx, key, options...)
	if err != nil {
		return nil, fmt.Errorf("read etcd bundle %q: %w", key, err)
	}
	if len(response.Kvs) != 1 {
		return nil, fmt.Errorf("bundle %q is missing", key)
	}
	return materialFromValue(targetID, generation, response.Kvs[0].Value, response.Header.Revision)
}

func materialFromValue(targetID, generation string, value []byte, revision int64) (*bundle.Material, error) {
	manifest, files, digest, err := bundle.Decode(value)
	if err != nil {
		return nil, fmt.Errorf("decode bundle: %w", err)
	}
	if err := bundle.ValidateGeneration(generation, digest); err != nil {
		return nil, err
	}
	return &bundle.Material{
		TargetID:   targetID,
		Generation: generation,
		Revision:   revision,
		Manifest:   manifest,
		Files:      files,
		Digest:     digest,
	}, nil
}

func (c *Client) targetIDSet(targetIDs []string) (map[string]string, error) {
	if len(targetIDs) == 0 {
		return nil, fmt.Errorf("at least one target ID is required")
	}
	targets := make(map[string]string, len(targetIDs))
	for _, targetID := range targetIDs {
		if !bundle.SafeName(targetID) {
			return nil, fmt.Errorf("unsafe target id %q", targetID)
		}
		key := c.ActiveKey(targetID)
		if _, exists := targets[key]; exists {
			return nil, fmt.Errorf("target id %q is duplicated", targetID)
		}
		targets[key] = targetID
	}
	return targets, nil
}
