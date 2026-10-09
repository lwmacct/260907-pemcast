package etcdsource

import (
	"testing"

	"github.com/stretchr/testify/require"
	"go.etcd.io/etcd/api/v3/mvccpb"
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
