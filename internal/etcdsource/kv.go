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

func activeComparison(key string, expected ActiveCondition) []clientv3.Cmp {
	if !expected.Exists {
		return []clientv3.Cmp{clientv3.Compare(clientv3.CreateRevision(key), "=", 0)}
	}
	return []clientv3.Cmp{
		clientv3.Compare(clientv3.Value(key), "=", expected.Generation),
		clientv3.Compare(clientv3.ModRevision(key), "=", expected.ModRevision),
	}
}
