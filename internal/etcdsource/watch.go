package etcdsource

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

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

// WatchUpdateKind identifies the lifecycle or data update carried by a watch response.
type WatchUpdateKind string

const (
	WatchCreated  WatchUpdateKind = "created"
	WatchEvent    WatchUpdateKind = "event"
	WatchProgress WatchUpdateKind = "progress"
)

// WatchUpdate is one normalized etcd watch response.
type WatchUpdate struct {
	Kind     WatchUpdateKind
	Revision int64
	Event    ActiveEvent
}

// WatchSession owns one long-lived active-prefix watcher. The session keeps the
// watcher alive across transport reconnection; callers must Close it before
// starting a replacement snapshot/watch cycle.
type WatchSession struct {
	updates        chan WatchUpdate
	errors         chan error
	watchCtx       context.Context
	cancel         context.CancelFunc
	watcher        clientv3.Watcher
	requestTimeout time.Duration
	done           chan struct{}
	closeOnce      sync.Once
}

// Updates returns the normalized created, event, and progress stream.
func (s *WatchSession) Updates() <-chan WatchUpdate { return s.updates }

// Errors returns watch protocol failures. The channel closes when the session ends.
func (s *WatchSession) Errors() <-chan error { return s.errors }

// RequestProgress asks etcd to send a progress response for this watch stream
// as soon as possible. Unlike a KV range read, this does not create a new
// authenticated unary request on an idle client.
func (s *WatchSession) RequestProgress(ctx context.Context) error {
	if s == nil || s.watchCtx == nil {
		return fmt.Errorf("watch session is not initialized")
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-s.watchCtx.Done():
		return fmt.Errorf("watch session is closed")
	default:
	}
	requestCtx, cancel := context.WithTimeout(s.watchCtx, s.requestTimeout)
	defer cancel()
	if err := s.watcher.RequestProgress(requestCtx); err != nil {
		return fmt.Errorf("request watch progress: %w", err)
	}
	return nil
}

// Close cancels the watch and waits for its delivery goroutine to stop.
func (s *WatchSession) Close() error {
	if s == nil {
		return nil
	}
	var err error
	s.closeOnce.Do(func() {
		s.cancel()
		<-s.done
	})
	return err
}

// WatchActive starts watching at the inclusive revision. Only the contiguous
// active prefix is watched, so bundle writes do not generate agent events.
func (c *Client) WatchActive(ctx context.Context, revision int64) *WatchSession {
	watchCtx, cancel := context.WithCancel(ctx)
	watchCtx = clientv3.WithRequireLeader(watchCtx)
	session := &WatchSession{
		updates:        make(chan WatchUpdate),
		errors:         make(chan error, 1),
		watchCtx:       watchCtx,
		cancel:         cancel,
		watcher:        c.client.Watcher,
		requestTimeout: c.requestTimeout,
		done:           make(chan struct{}),
	}
	watch := c.client.Watch(
		watchCtx,
		c.ActivePrefix(),
		clientv3.WithPrefix(),
		clientv3.WithRev(revision),
		clientv3.WithCreatedNotify(),
		clientv3.WithProgressNotify(),
	)

	go func() {
		defer close(session.updates)
		defer close(session.errors)
		defer close(session.done)
		for response := range watch {
			updates, err := watchUpdates(c.ActivePrefix(), &response)
			if err != nil {
				session.sendError(fmt.Errorf("watch etcd active pointers: %w", err))
				return
			}
			for _, update := range updates {
				select {
				case session.updates <- update:
				case <-watchCtx.Done():
					return
				}
			}
		}
		if ctx.Err() == nil {
			session.sendError(fmt.Errorf("etcd watch channel closed"))
		}
	}()
	return session
}

func (s *WatchSession) sendError(err error) {
	select {
	case s.errors <- err:
	case <-s.watchCtx.Done():
	}
}

func watchUpdates(activePrefix string, response *clientv3.WatchResponse) ([]WatchUpdate, error) {
	if err := response.Err(); err != nil {
		return nil, err
	}
	revision := response.Header.GetRevision()
	switch {
	case response.Created:
		return []WatchUpdate{{Kind: WatchCreated, Revision: revision}}, nil
	case response.IsProgressNotify():
		return []WatchUpdate{{Kind: WatchProgress, Revision: revision}}, nil
	}
	updates := make([]WatchUpdate, 0, len(response.Events))
	for _, event := range response.Events {
		active, err := activeEvent(activePrefix, event)
		if err != nil {
			return nil, err
		}
		updates = append(updates, WatchUpdate{Kind: WatchEvent, Revision: active.Revision, Event: active})
	}
	return updates, nil
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
