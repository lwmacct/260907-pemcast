package etcdsource

import (
	"context"
	"fmt"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
)

// Snapshot is a linearizable view of every active pointer in one etcd prefix.
type Snapshot struct {
	Revision int64
	Active   map[string]string
}

// ActivePointer is one active-pointer value and its ModRevision.
type ActivePointer struct {
	Generation  string
	ModRevision int64
}

// PrefixSnapshot is a linearizable view of every pointer below one exact protocol active prefix.
type PrefixSnapshot struct {
	Revision int64
	Active   map[string]ActivePointer
}

// SnapshotActive retrieves every active pointer in one range read and returns
// the revision from which a lossless watch can continue.
func (c *Client) SnapshotActive(ctx context.Context) (Snapshot, error) {
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()

	response, err := c.client.Get(requestCtx, c.ActivePrefix(), clientv3.WithPrefix())
	if err != nil {
		return Snapshot{}, fmt.Errorf("read etcd active pointers: %w", err)
	}
	return snapshotFromResponse(response, c.ActivePrefix())
}

// SnapshotActivePrefix retrieves pointers below one explicitly supplied active prefix.
func (c *Client) SnapshotActivePrefix(ctx context.Context, activePrefix string) (PrefixSnapshot, error) {
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()

	response, err := c.client.Get(requestCtx, activePrefix, clientv3.WithPrefix())
	if err != nil {
		return PrefixSnapshot{}, fmt.Errorf("read active prefix %q: %w", activePrefix, err)
	}
	return prefixSnapshotFromResponse(response, activePrefix)
}

func snapshotFromResponse(response *clientv3.GetResponse, activePrefix string) (Snapshot, error) {
	active := make(map[string]string, len(response.Kvs))
	for _, kv := range response.Kvs {
		targetID := strings.TrimPrefix(string(kv.Key), activePrefix)
		generation := strings.TrimSpace(string(kv.Value))
		if !bundle.SafeName(targetID) || !bundle.SafeName(generation) {
			return Snapshot{}, fmt.Errorf("invalid active pointer %q=%q", kv.Key, kv.Value)
		}
		active[targetID] = generation
	}
	return Snapshot{Revision: response.Header.Revision, Active: active}, nil
}

func prefixSnapshotFromResponse(response *clientv3.GetResponse, activePrefix string) (PrefixSnapshot, error) {
	active := make(map[string]ActivePointer, len(response.Kvs))
	for _, kv := range response.Kvs {
		targetID := strings.TrimPrefix(string(kv.Key), activePrefix)
		generation := strings.TrimSpace(string(kv.Value))
		if !bundle.SafeName(targetID) || !bundle.SafeName(generation) {
			return PrefixSnapshot{}, fmt.Errorf("invalid active pointer %q=%q", kv.Key, kv.Value)
		}
		active[targetID] = ActivePointer{Generation: generation, ModRevision: kv.ModRevision}
	}
	return PrefixSnapshot{Revision: response.Header.Revision, Active: active}, nil
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
	certificates, leaf, err := manifest.ValidateFiles(files)
	if err != nil {
		return nil, fmt.Errorf("validate bundle semantics: %w", err)
	}
	return &bundle.Material{
		TargetID: targetID, Generation: generation, Revision: revision,
		Manifest: manifest, Files: files, Digest: digest,
		Certificates: certificates, Leaf: leaf,
	}, nil
}
