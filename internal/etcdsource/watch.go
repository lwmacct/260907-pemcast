package etcdsource

import (
	"context"
	"fmt"
	"sync"

	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
)

// ActiveEvent reports a put or deletion of one configured target active pointer.
type ActiveEvent struct {
	TargetID   string
	Generation string
	Revision   int64
	Deleted    bool
}

// WatchTargets starts one exact-key watcher per configured target. All watchers
// begin at the same revision, so events cannot be lost after a corresponding
// SnapshotTargets response.
func (c *Client) WatchTargets(
	ctx context.Context, targetIDs []string, revision int64,
) (<-chan ActiveEvent, <-chan error) {
	targets, err := c.targetIDSet(targetIDs)
	if err != nil {
		events := make(chan ActiveEvent)
		errors := make(chan error, 1)
		errors <- err
		close(events)
		close(errors)
		return events, errors
	}

	events := make(chan ActiveEvent)
	errors := make(chan error, len(targets))
	watchCtx := clientv3.WithRequireLeader(ctx)
	var workers sync.WaitGroup

	for _, targetID := range targetIDs {
		workers.Add(1)
		watch := c.client.Watch(
			watchCtx,
			c.ActiveKey(targetID),
			clientv3.WithRev(revision),
			clientv3.WithProgressNotify(),
		)
		go func(targetID string, watch clientv3.WatchChan) {
			defer workers.Done()
			c.watchTarget(ctx, watch, targetID, events, errors)
		}(targetID, watch)
	}

	go func() {
		workers.Wait()
		close(events)
		close(errors)
	}()
	return events, errors
}

func (c *Client) watchTarget(
	ctx context.Context,
	watch clientv3.WatchChan,
	targetID string,
	events chan<- ActiveEvent,
	errors chan<- error,
) {
	for response := range watch {
		if err := response.Err(); err != nil {
			errors <- fmt.Errorf("watch etcd active pointer for target %q: %w", targetID, err)
			return
		}
		for _, event := range response.Events {
			active, err := activeEvent(targetID, c.ActiveKey(targetID), event)
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
		errors <- fmt.Errorf("etcd watch channel closed for target %q", targetID)
	}
}

func activeEvent(targetID, activeKey string, event *clientv3.Event) (ActiveEvent, error) {
	active := ActiveEvent{TargetID: targetID, Revision: event.Kv.ModRevision}
	if string(event.Kv.Key) != activeKey {
		return ActiveEvent{}, fmt.Errorf(
			"watch for target %q received unexpected key %q",
			targetID,
			event.Kv.Key,
		)
	}
	if event.Type == clientv3.EventTypeDelete {
		active.Deleted = true
		return active, nil
	}
	active.Generation = string(event.Kv.Value)
	if !bundle.SafeName(active.Generation) {
		return ActiveEvent{}, fmt.Errorf(
			"watch for target %q received unsafe generation %q",
			targetID,
			active.Generation,
		)
	}
	return active, nil
}
