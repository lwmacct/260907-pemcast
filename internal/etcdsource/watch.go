package etcdsource

import (
	"context"
	"fmt"
	"strings"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
)

// ActiveEvent reports a put or deletion of one target active pointer.
type ActiveEvent struct {
	TargetID   string
	Generation string
	Revision   int64
	Deleted    bool
}

// WatchActive starts watching immediately after revision. The channel closes
// on context cancellation or an unrecoverable response such as compaction.
func (c *Client) WatchActive(ctx context.Context, revision int64) (<-chan ActiveEvent, <-chan error) {
	events := make(chan ActiveEvent)
	errors := make(chan error, 1)
	watchCtx := clientv3.WithRequireLeader(ctx)
	watch := c.client.Watch(watchCtx, c.activePrefix(), clientv3.WithPrefix(), clientv3.WithRev(revision), clientv3.WithProgressNotify())
	go func() {
		defer close(events)
		defer close(errors)
		for response := range watch {
			if err := response.Err(); err != nil {
				errors <- fmt.Errorf("watch etcd active pointers: %w", err)
				return
			}
			for _, event := range response.Events {
				id := strings.TrimPrefix(string(event.Kv.Key), c.activePrefix())
				if !bundle.SafeName(id) {
					errors <- fmt.Errorf("watch received unsafe target id %q", id)
					return
				}
				active := ActiveEvent{TargetID: id, Revision: event.Kv.ModRevision}
				if event.Type == clientv3.EventTypeDelete {
					active.Deleted = true
				} else {
					active.Generation = strings.TrimSpace(string(event.Kv.Value))
					if !bundle.SafeName(active.Generation) {
						errors <- fmt.Errorf("watch received unsafe generation %q", active.Generation)
						return
					}
				}
				select {
				case events <- active:
				case <-ctx.Done():
					return
				}
			}
		}
		if err := ctx.Err(); err == nil {
			errors <- fmt.Errorf("etcd watch channel closed")
		}
	}()
	return events, errors
}
