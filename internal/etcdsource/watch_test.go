package etcdsource

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/lwmacct/260907-pemcast/internal/keyspace"
)

func TestActiveEventAcceptsExactPutAndDelete(t *testing.T) {
	keys := keyspace.Default()
	put, err := activeEvent("nginx", keys.ActiveKey("nginx"), &clientv3.Event{
		Type: clientv3.EventTypePut,
		Kv: &mvccpb.KeyValue{
			Key:         []byte(keys.ActiveKey("nginx")),
			Value:       []byte("sha256-current"),
			ModRevision: 42,
		},
	})
	require.NoError(t, err)
	require.Equal(t, ActiveEvent{
		TargetID: "nginx", Generation: "sha256-current", Revision: 42,
	}, put)

	deleted, err := activeEvent("nginx", keys.ActiveKey("nginx"), &clientv3.Event{
		Type: clientv3.EventTypeDelete,
		Kv: &mvccpb.KeyValue{
			Key:         []byte(keys.ActiveKey("nginx")),
			ModRevision: 43,
		},
	})
	require.NoError(t, err)
	require.Equal(t, ActiveEvent{
		TargetID: "nginx", Revision: 43, Deleted: true,
	}, deleted)
}

func TestActiveEventRejectsUnexpectedAndUnsafeValues(t *testing.T) {
	keys := keyspace.Default()
	_, err := activeEvent("nginx", keys.ActiveKey("nginx"), &clientv3.Event{
		Type: clientv3.EventTypePut,
		Kv: &mvccpb.KeyValue{
			Key:         []byte(keys.ActiveKey("other")),
			Value:       []byte("sha256-current"),
			ModRevision: 42,
		},
	})
	require.ErrorContains(t, err, "unexpected key")

	_, err = activeEvent("nginx", keys.ActiveKey("nginx"), &clientv3.Event{
		Type: clientv3.EventTypePut,
		Kv: &mvccpb.KeyValue{
			Key:         []byte(keys.ActiveKey("nginx")),
			Value:       []byte("../unsafe"),
			ModRevision: 42,
		},
	})
	require.ErrorContains(t, err, "unsafe generation")
}

func TestWatchTargetsRejectsInvalidTargetsWithoutWatcher(t *testing.T) {
	client := newClient(nil, time.Second)
	events, errors := client.WatchTargets(t.Context(), []string{"../unsafe"}, 1)
	_, ok := <-events
	require.False(t, ok)
	require.ErrorContains(t, <-errors, "unsafe target id")
}
