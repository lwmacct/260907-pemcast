package etcdsource

import (
	"context"
	"fmt"

	clientv3 "go.etcd.io/etcd/client/v3"
)

func (c *Client) requestContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(ctx, c.requestTimeout)
}

// Get reads one exact key as a string value.
func (c *Client) Get(ctx context.Context, key string) (string, bool, error) {
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()
	response, err := c.client.Get(requestCtx, key)
	if err != nil {
		return "", false, fmt.Errorf("read etcd key %q: %w", key, err)
	}
	if len(response.Kvs) == 0 {
		return "", false, nil
	}
	return string(response.Kvs[0].Value), true, nil
}

// PutIf writes value only when the expected pointer state still holds.
func (c *Client) PutIf(ctx context.Context, key, value, expected string, expectedExists bool) (bool, error) {
	requestCtx, cancel := c.requestContext(ctx)
	defer cancel()

	var comparison clientv3.Cmp
	if expectedExists {
		comparison = clientv3.Compare(clientv3.Value(key), "=", expected)
	} else {
		comparison = clientv3.Compare(clientv3.CreateRevision(key), "=", 0)
	}
	response, err := c.client.Txn(requestCtx).
		If(comparison).
		Then(clientv3.OpPut(key, value)).
		Commit()
	if err != nil {
		return false, fmt.Errorf("compare-and-swap etcd key %q: %w", key, err)
	}
	return response.Succeeded, nil
}
