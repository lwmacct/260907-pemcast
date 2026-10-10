package etcdsource

import (
	"context"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"
)

// Value is one exact etcd key value and its ModRevision.
type Value struct {
	Data        string
	ModRevision int64
	Exists      bool
}

// ActiveCondition is one captured active pointer state used for CAS.
type ActiveCondition struct {
	Generation  string
	ModRevision int64
	Exists      bool
}

func (c *Client) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.requestTimeout)
}

// Get reads one exact key.
func (c *Client) Get(ctx context.Context, key string) (Value, error) {
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()
	response, err := c.client.Get(requestCtx, key)
	if err != nil {
		return Value{}, fmt.Errorf("read etcd key %q: %w", key, err)
	}
	if len(response.Kvs) == 0 {
		return Value{}, nil
	}
	return Value{
		Data:        string(response.Kvs[0].Value),
		ModRevision: response.Kvs[0].ModRevision,
		Exists:      true,
	}, nil
}

// GetAt reads one exact key at a historical etcd revision.
func (c *Client) GetAt(ctx context.Context, key string, revision int64) (Value, error) {
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()

	options := []clientv3.OpOption{}
	if revision > 0 {
		options = append(options, clientv3.WithRev(revision))
	}
	response, err := c.client.Get(requestCtx, key, options...)
	if err != nil {
		return Value{}, fmt.Errorf("read etcd key %q: %w", key, err)
	}
	if len(response.Kvs) == 0 {
		return Value{}, nil
	}
	return Value{
		Data:        string(response.Kvs[0].Value),
		ModRevision: response.Kvs[0].ModRevision,
		Exists:      true,
	}, nil
}

// StageBundleIfAbsent writes an immutable bundle only when its key is absent.
func (c *Client) StageBundleIfAbsent(ctx context.Context, key, value string) (bool, error) {
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()

	response, err := c.client.Txn(requestCtx).
		If(clientv3.Compare(clientv3.CreateRevision(key), "=", 0)).
		Then(clientv3.OpPut(key, value)).
		Commit()
	if err != nil {
		return false, fmt.Errorf("stage etcd bundle: %w", err)
	}
	return response.Succeeded, nil
}

// SwapActive moves the pointer only when the captured active state still holds.
func (c *Client) SwapActive(
	ctx context.Context,
	activeKey, generation string,
	expected ActiveCondition,
) (bool, error) {
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()

	response, err := c.client.Txn(requestCtx).
		If(activeComparison(activeKey, expected)...).
		Then(clientv3.OpPut(activeKey, generation)).
		Commit()
	if err != nil {
		return false, fmt.Errorf("compare-and-swap etcd active pointer: %w", err)
	}
	return response.Succeeded, nil
}

// SwapActiveFromSource moves a destination pointer only while both the source
// and captured destination states still hold. The upgrade command uses this to
// avoid converting a stale v5 active generation.
func (c *Client) SwapActiveFromSource(
	ctx context.Context,
	sourceKey string,
	source ActiveCondition,
	destinationKey, generation string,
	destination ActiveCondition,
) (bool, error) {
	if !source.Exists {
		return false, fmt.Errorf("upgrade source condition must reference an existing active pointer")
	}
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()

	comparisons := append(
		activeComparison(sourceKey, source),
		activeComparison(destinationKey, destination)...,
	)
	response, err := c.client.Txn(requestCtx).
		If(comparisons...).
		Then(clientv3.OpPut(destinationKey, generation)).
		Commit()
	if err != nil {
		return false, fmt.Errorf("compare-and-swap upgrade active pointer: %w", err)
	}
	return response.Succeeded, nil
}

// DeletePrefix deletes exactly one explicitly supplied prefix and reports the deleted key count.
func (c *Client) DeletePrefix(ctx context.Context, prefix string) (int64, error) {
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()

	response, err := c.client.Delete(requestCtx, prefix, clientv3.WithPrefix())
	if err != nil {
		return 0, fmt.Errorf("delete etcd prefix %q: %w", prefix, err)
	}
	return response.Deleted, nil
}

func activeComparison(key string, expected ActiveCondition) []clientv3.Cmp {
	if !expected.Exists {
		return []clientv3.Cmp{clientv3.Compare(clientv3.CreateRevision(key), "=", 0)}
	}
	return []clientv3.Cmp{
		clientv3.Compare(clientv3.Value(key), "=", expected.Generation),
		clientv3.Compare(clientv3.ModRevision(key), "=", expected.ModRevision),
	}
}
