package etcdsource

import (
	"context"
	"fmt"
)

// Value is one exact etcd key value and its ModRevision.
type Value struct {
	Data        string
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
