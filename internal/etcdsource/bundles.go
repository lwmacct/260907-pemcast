package etcdsource

import (
	"context"
	"fmt"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
)

// BundleRecord is one exact immutable bundle key and its complete value.
type BundleRecord struct {
	Key         string
	TargetID    string
	Generation  string
	Data        []byte
	ModRevision int64
}

// ListBundleRecords returns every currently existing bundle below one
// explicitly supplied prefix. It validates the target/generation path shape and
// fetches each complete value by exact key.
func (c *Client) ListBundleRecords(ctx context.Context, bundlePrefix string) ([]BundleRecord, error) {
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()

	response, err := c.client.Get(requestCtx, bundlePrefix, clientv3.WithPrefix(), clientv3.WithKeysOnly())
	if err != nil {
		return nil, fmt.Errorf("list etcd bundles below %q: %w", bundlePrefix, err)
	}

	keys := make([]string, 0, len(response.Kvs))
	for _, kv := range response.Kvs {
		key := string(kv.Key)
		targetID, generation, found := strings.Cut(strings.TrimPrefix(key, bundlePrefix), "/")
		if !found || !bundle.SafeName(targetID) || !bundle.SafeName(generation) {
			return nil, fmt.Errorf("invalid bundle key %q below %q", key, bundlePrefix)
		}
		keys = append(keys, key)
	}

	records := make([]BundleRecord, 0, len(keys))
	for _, key := range keys {
		value, err := c.Get(ctx, key)
		if err != nil {
			return nil, fmt.Errorf("read listed bundle %q: %w", key, err)
		}
		if !value.Exists {
			continue
		}
		targetID, generation, _ := strings.Cut(strings.TrimPrefix(key, bundlePrefix), "/")
		records = append(records, BundleRecord{
			Key: key, TargetID: targetID, Generation: generation,
			Data: []byte(value.Data), ModRevision: value.ModRevision,
		})
	}
	return records, nil
}

// DeleteBundleIfActiveUnchanged deletes one exact bundle key only while the
// captured active pointer state still holds. It never deletes an active pointer.
func (c *Client) DeleteBundleIfActiveUnchanged(
	ctx context.Context,
	bundleKey string,
	activeKey string,
	expected ActiveCondition,
) (bool, error) {
	if !expected.Exists {
		return false, fmt.Errorf("prune requires an existing captured active pointer")
	}
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()

	response, err := c.client.Txn(requestCtx).
		If(activeComparison(activeKey, expected)...).
		Then(clientv3.OpDelete(bundleKey)).
		Commit()
	if err != nil {
		return false, fmt.Errorf("conditionally delete etcd bundle %q: %w", bundleKey, err)
	}
	return response.Succeeded, nil
}
