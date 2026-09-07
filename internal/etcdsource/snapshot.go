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

// SnapshotActive retrieves every active pointer and the revision from which a
// lossless watch can continue.
func (c *Client) SnapshotActive(ctx context.Context) (Snapshot, error) {
	requestCtx, cancel := context.WithTimeout(ctx, c.requestTimeout)
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

// FetchBundle reads one immutable generation at a single etcd revision.
func (c *Client) FetchBundle(ctx context.Context, targetID, generation string, revision int64) (*bundle.Material, error) {
	if !bundle.SafeName(targetID) || !bundle.SafeName(generation) {
		return nil, fmt.Errorf("unsafe target or generation")
	}
	requestCtx, cancel := context.WithTimeout(ctx, c.requestTimeout)
	defer cancel()
	prefix := c.bundlePrefix(targetID, generation)
	options := []clientv3.OpOption{clientv3.WithPrefix()}
	if revision > 0 {
		options = append(options, clientv3.WithRev(revision))
	}
	response, err := c.client.Get(requestCtx, prefix, options...)
	if err != nil {
		return nil, fmt.Errorf("read etcd bundle %s/%s: %w", targetID, generation, err)
	}
	var manifestBytes []byte
	files := make(map[string][]byte)
	for _, kv := range response.Kvs {
		relative := strings.TrimPrefix(string(kv.Key), prefix)
		switch {
		case relative == "manifest.json":
			manifestBytes = append([]byte(nil), kv.Value...)
		case strings.HasPrefix(relative, "files/"):
			name := strings.TrimPrefix(relative, "files/")
			if !bundle.SafeName(name) {
				return nil, fmt.Errorf("bundle contains unsafe file key %q", relative)
			}
			if len(kv.Value) > maxBundleFileBytes {
				return nil, fmt.Errorf("bundle file %q exceeds %d bytes", name, maxBundleFileBytes)
			}
			files[name] = append([]byte(nil), kv.Value...)
		default:
			return nil, fmt.Errorf("bundle contains unknown key %q", relative)
		}
	}
	if len(manifestBytes) == 0 {
		return nil, fmt.Errorf("bundle manifest is missing")
	}
	manifest, err := bundle.ParseManifest(manifestBytes)
	if err != nil {
		return nil, err
	}
	digest, err := bundle.VerifyFiles(manifest, files)
	if err != nil {
		return nil, err
	}
	return &bundle.Material{
		TargetID: targetID, Generation: generation, Revision: response.Header.Revision,
		Manifest: manifest, Files: files, Digest: digest,
	}, nil
}

func (c *Client) activePrefix() string { return c.rootPrefix + "/active/" }

func (c *Client) bundlePrefix(targetID, generation string) string {
	return path.Join(c.rootPrefix, "bundles", targetID, generation) + "/"
}
