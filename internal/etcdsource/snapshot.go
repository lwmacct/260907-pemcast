package etcdsource

import (
	"context"
	"fmt"
	"path"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
)

// Snapshot is a linearizable view of all active pointers at one etcd revision.
type Snapshot struct {
	Revision int64
	Active   map[string]string
}

// SnapshotActive retrieves every active pointer and the revision from which a lossless watch can continue.
func (c *Client) SnapshotActive(ctx context.Context) (Snapshot, error) {
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()
	prefix := c.activePrefix()
	response, err := c.client.Get(requestCtx, prefix, clientv3.WithPrefix())
	if err != nil {
		return Snapshot{}, fmt.Errorf("read etcd active pointers: %w", err)
	}
	active := make(map[string]string, len(response.Kvs))
	for _, kv := range response.Kvs {
		id := strings.TrimPrefix(string(kv.Key), prefix)
		generation := strings.TrimSpace(string(kv.Value))
		if !bundle.SafeName(id) || !bundle.SafeName(generation) {
			return Snapshot{}, fmt.Errorf("invalid active pointer %q=%q", kv.Key, kv.Value)
		}
		active[id] = generation
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
	key := c.bundleKey(targetID, generation)
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

func (c *Client) activePrefix() string { return ActivePrefix() }

func (c *Client) bundleKey(targetID, generation string) string {
	return BundleKey(targetID, generation)
}

// ActivePrefix returns the fixed prefix watched for target pointers.
func ActivePrefix() string { return ProtocolRoot + "/active/" }

// ActiveKey returns one target's active pointer key.
func ActiveKey(targetID string) string { return path.Join(ProtocolRoot, "active", targetID) }

// BundleKey returns one immutable single-key bundle path.
func BundleKey(targetID, generation string) string {
	return path.Join(ProtocolRoot, "bundles", targetID, generation)
}
