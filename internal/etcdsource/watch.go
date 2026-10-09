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

// WatchActive starts watching immediately after revision. Only the contiguous
// active prefix is watched, so bundle writes do not generate agent events.
func (c *Client) WatchActive(ctx context.Context, revision int64) (<-chan ActiveEvent, <-chan error) {
	events := make(chan ActiveEvent)
	errors := make(chan error, 1)
	watchCtx := clientv3.WithRequireLeader(ctx)
	watch := c.client.Watch(
		watchCtx,
		c.ActivePrefix(),
		clientv3.WithPrefix(),
		clientv3.WithRev(revision),
		clientv3.WithProgressNotify(),
	)

	go func() {
		defer close(events)
		defer close(errors)
		for response := range watch {
			if err := response.Err(); err != nil {
				errors <- fmt.Errorf("watch etcd active pointers: %w", err)
				return
			}
			for _, event := range response.Events {
				active, err := activeEvent(c.ActivePrefix(), event)
				if err != nil {
					errors <- err
					return
				}
				select {
				case events <- active:
				case <-ctx.Done():
					return
				}
			}
		}
		if ctx.Err() == nil {
			errors <- fmt.Errorf("etcd watch channel closed")
		}
	}()
	return events, errors
}

func activeEvent(activePrefix string, event *clientv3.Event) (ActiveEvent, error) {
	targetID := strings.TrimPrefix(string(event.Kv.Key), activePrefix)
	active := ActiveEvent{TargetID: targetID, Revision: event.Kv.ModRevision}
	if !bundle.SafeName(targetID) {
		return ActiveEvent{}, fmt.Errorf("watch received unsafe target id %q", targetID)
	}
	if event.Type == clientv3.EventTypeDelete {
		active.Deleted = true
		return active, nil
	}
	active.Generation = strings.TrimSpace(string(event.Kv.Value))
	if !bundle.SafeName(active.Generation) {
		return ActiveEvent{}, fmt.Errorf(
			"watch for target %q received unsafe generation %q",
			targetID,
			active.Generation,
		)
	}
	return active, nil
}
