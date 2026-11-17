package etcdsource

import (
	"testing"

	"github.com/stretchr/testify/require"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	"go.etcd.io/etcd/api/v3/v3rpc/rpctypes"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/lwmacct/260907-pemcast/internal/keyspace"
)

func TestActiveEventAcceptsPutAndDelete(t *testing.T) {
	keys := keyspace.Default()
	put, err := activeEvent(keys.ActivePrefix(), &clientv3.Event{
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

	deleted, err := activeEvent(keys.ActivePrefix(), &clientv3.Event{
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
	_, err := activeEvent(keys.ActivePrefix(), &clientv3.Event{
		Type: clientv3.EventTypePut,
		Kv: &mvccpb.KeyValue{
			Key:         []byte(keys.BundlePrefix() + "nginx/sha256-value"),
			Value:       []byte("sha256-current"),
			ModRevision: 42,
		},
	})
	require.ErrorContains(t, err, "unsafe target id")

	_, err = activeEvent(keys.ActivePrefix(), &clientv3.Event{
		Type: clientv3.EventTypePut,
		Kv: &mvccpb.KeyValue{
			Key:         []byte(keys.ActiveKey("nginx")),
			Value:       []byte("../unsafe"),
			ModRevision: 42,
		},
	})
	require.ErrorContains(t, err, "unsafe generation")
}

func TestWatchUpdatesNormalizesLifecycleAndDataResponses(t *testing.T) {
	keys := keyspace.Default()

	created, err := watchUpdates(keys.ActivePrefix(), &clientv3.WatchResponse{
		Header:  &pb.ResponseHeader{Revision: 41},
		Created: true,
	})
	require.NoError(t, err)
	require.Equal(t, []WatchUpdate{{Kind: WatchCreated, Revision: 41}}, created)

	progress, err := watchUpdates(keys.ActivePrefix(), &clientv3.WatchResponse{
		Header: &pb.ResponseHeader{Revision: 42},
	})
	require.NoError(t, err)
	require.Equal(t, []WatchUpdate{{Kind: WatchProgress, Revision: 42}}, progress)

	event, err := watchUpdates(keys.ActivePrefix(), &clientv3.WatchResponse{
		Header: &pb.ResponseHeader{Revision: 43},
		Events: []*clientv3.Event{{
			Type: clientv3.EventTypePut,
			Kv: &mvccpb.KeyValue{
				Key:         []byte(keys.ActiveKey("nginx")),
				Value:       []byte("sha256-current"),
				ModRevision: 43,
			},
		}},
	})
	require.NoError(t, err)
	require.Equal(t, []WatchUpdate{{
		Kind: WatchEvent, Revision: 43,
		Event: ActiveEvent{TargetID: "nginx", Generation: "sha256-current", Revision: 43},
	}}, event)
}

func TestWatchUpdatesReturnsProtocolErrorsBeforeEvents(t *testing.T) {
	keys := keyspace.Default()
	response := &clientv3.WatchResponse{
		Header:   &pb.ResponseHeader{Revision: 44},
		Canceled: true,
		Events: []*clientv3.Event{{
			Type: clientv3.EventTypePut,
			Kv: &mvccpb.KeyValue{
				Key:         []byte(keys.ActiveKey("nginx")),
				Value:       []byte("sha256-current"),
				ModRevision: 44,
			},
		}},
	}

	_, err := watchUpdates(keys.ActivePrefix(), response)
	require.ErrorIs(t, err, rpctypes.ErrFutureRev)
}
