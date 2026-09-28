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

// ActiveCondition is an expected pointer state captured while building a publish plan.
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

// CreateBundleAndSwapActive atomically creates a new bundle and moves the active
// pointer only when both the bundle is absent and the captured active state holds.
func (c *Client) CreateBundleAndSwapActive(
	ctx context.Context,
	bundleKey, bundleValue, activeKey, generation string,
	expected ActiveCondition,
) (bool, error) {
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()

	conditions := append(
		[]clientv3.Cmp{clientv3.Compare(clientv3.CreateRevision(bundleKey), "=", 0)},
		activeComparison(activeKey, expected)...,
	)
	response, err := c.client.Txn(requestCtx).
		If(conditions...).
		Then(
			clientv3.OpPut(bundleKey, bundleValue),
			clientv3.OpPut(activeKey, generation),
		).
		Commit()
	if err != nil {
		return false, fmt.Errorf("atomic create bundle and swap active: %w", err)
	}
	return response.Succeeded, nil
}

// SwapActive moves the pointer only when the captured active value and ModRevision still hold.
func (c *Client) SwapActive(ctx context.Context, activeKey, generation string, expected ActiveCondition) (bool, error) {
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

func activeComparison(key string, expected ActiveCondition) []clientv3.Cmp {
	if !expected.Exists {
		return []clientv3.Cmp{clientv3.Compare(clientv3.CreateRevision(key), "=", 0)}
	}
	return []clientv3.Cmp{
		clientv3.Compare(clientv3.Value(key), "=", expected.Generation),
		clientv3.Compare(clientv3.ModRevision(key), "=", expected.ModRevision),
	}
}
