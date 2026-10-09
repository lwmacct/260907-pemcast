package etcdsource

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	pb "go.etcd.io/etcd/api/v3/etcdserverpb"
	"go.etcd.io/etcd/api/v3/mvccpb"
	clientv3 "go.etcd.io/etcd/client/v3"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/keyspace"
)

func TestMaterialFromValueDecodesAndValidatesGeneration(t *testing.T) {
	certificate := []byte("certificate")
	privateKey := []byte("private-key")
	manifest, digest := bundle.NewTLSManifest(certificate, privateKey, "fullchain.pem", "privkey.pem")
	encoded, err := bundle.Encode(manifest)
	require.NoError(t, err)

	generation := bundle.Generation(digest)
	material, err := materialFromValue("nginx", generation, encoded, 42)
	require.NoError(t, err)
	require.Equal(t, "nginx", material.TargetID)
	require.Equal(t, generation, material.Generation)
	require.Equal(t, int64(42), material.Revision)
	require.Equal(t, digest, material.Digest)
	require.Equal(t, certificate, material.Files["fullchain.pem"])
	require.Equal(t, privateKey, material.Files["privkey.pem"])
}

func TestMaterialFromValueRejectsPointerDigestMismatch(t *testing.T) {
	manifest, _ := bundle.NewTLSManifest([]byte("certificate"), []byte("private-key"), "fullchain.pem", "privkey.pem")
	encoded, err := bundle.Encode(manifest)
	require.NoError(t, err)

	_, err = materialFromValue("nginx", "sha256-other", encoded, 42)
	require.ErrorContains(t, err, "does not match")
}

func TestMaterialFromValueRejectsMalformedBundle(t *testing.T) {
	_, err := materialFromValue("nginx", "sha256-value", []byte(`{invalid`), 42)
	require.ErrorContains(t, err, "decode bundle")
}

func TestProtocolKeysUseKindFirstV5Root(t *testing.T) {
	client := newClient(nil, time.Second)
	require.Equal(t, "/v5", ProtocolRoot)
	require.Equal(t, "/pemcast", client.Prefix())
	require.Equal(t, "/pemcast/v5/active/", client.ActivePrefix())
	require.Equal(t, "/pemcast/v5/active/nginx", client.ActiveKey("nginx"))
	require.Equal(t, "/pemcast/v5/bundles/", client.BundlePrefix())
	require.Equal(t, "/pemcast/v5/bundles/nginx/", client.TargetBundlePrefix("nginx"))
	require.Equal(
		t,
		"/pemcast/v5/bundles/nginx/sha256-value",
		client.BundleKey("nginx", "sha256-value"),
	)

	customKeys, err := keyspace.NewKeys("/tenants/example")
	require.NoError(t, err)
	client.keys = customKeys
	require.Equal(t, "/tenants/example", client.Prefix())
	require.Equal(t, "/tenants/example/v5/active/nginx", client.ActiveKey("nginx"))
	require.Equal(
		t,
		"/tenants/example/v5/bundles/nginx/sha256-value",
		client.BundleKey("nginx", "sha256-value"),
	)
}

func TestSnapshotFromResponseKeepsAllTargetsAtOneRevision(t *testing.T) {
	client := newClient(nil, time.Second)
	response := rangeResponse(42, []*mvccpb.KeyValue{
		{
			Key:         []byte(client.ActiveKey("nginx")),
			Value:       []byte("sha256-current"),
			ModRevision: 41,
		},
		{
			Key:         []byte(client.ActiveKey("api")),
			Value:       []byte("sha256-api"),
			ModRevision: 40,
		},
	})

	snapshot, err := snapshotFromResponse(response, client.ActivePrefix())
	require.NoError(t, err)
	require.Equal(t, int64(42), snapshot.Revision)
	require.Equal(t, map[string]string{
		"nginx": "sha256-current",
		"api":   "sha256-api",
	}, snapshot.Active)
}

func TestSnapshotFromResponseRejectsUnsafeKeysAndGenerations(t *testing.T) {
	client := newClient(nil, time.Second)
	response := rangeResponse(42, []*mvccpb.KeyValue{{
		Key:   []byte(client.ActivePrefix() + "../unsafe"),
		Value: []byte("sha256-current"),
	}})
	_, err := snapshotFromResponse(response, client.ActivePrefix())
	require.ErrorContains(t, err, "invalid active pointer")

	response = rangeResponse(42, []*mvccpb.KeyValue{{
		Key:   []byte(client.ActiveKey("nginx")),
		Value: []byte("../unsafe"),
	}})
	_, err = snapshotFromResponse(response, client.ActivePrefix())
	require.ErrorContains(t, err, "invalid active pointer")
}

func rangeResponse(revision int64, kvs []*mvccpb.KeyValue) *clientv3.GetResponse {
	return &clientv3.GetResponse{
		Header: &pb.ResponseHeader{Revision: revision},
		Kvs:    kvs,
	}
}
