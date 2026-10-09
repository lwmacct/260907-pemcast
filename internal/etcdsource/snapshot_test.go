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

func TestProtocolKeysUseTargetFirstV3Root(t *testing.T) {
	client := newClient(nil, time.Second)
	require.Equal(t, "/v3", ProtocolRoot)
	require.Equal(t, "/pemcast", client.Prefix())
	require.Equal(t, "/pemcast/v3/nginx/", client.TargetPrefix("nginx"))
	require.Equal(t, "/pemcast/v3/nginx/active", client.ActiveKey("nginx"))
	require.Equal(
		t,
		"/pemcast/v3/nginx/bundles/sha256-value",
		client.BundleKey("nginx", "sha256-value"),
	)

	customKeys, err := keyspace.NewKeys("/tenants/example")
	require.NoError(t, err)
	client.keys = customKeys
	require.Equal(t, "/tenants/example", client.Prefix())
	require.Equal(t, "/tenants/example/v3/nginx/active", client.ActiveKey("nginx"))
}

func TestTargetIDSetRejectsEmptyUnsafeAndDuplicateIDs(t *testing.T) {
	client := newClient(nil, time.Second)
	_, err := client.targetIDSet(nil)
	require.ErrorContains(t, err, "at least one target ID")

	_, err = client.targetIDSet([]string{"../unsafe"})
	require.ErrorContains(t, err, "unsafe target id")

	_, err = client.targetIDSet([]string{"nginx", "nginx"})
	require.ErrorContains(t, err, "duplicated")

	targets, err := client.targetIDSet([]string{"nginx", "api"})
	require.NoError(t, err)
	require.Equal(t, map[string]string{
		client.ActiveKey("nginx"): "nginx",
		client.ActiveKey("api"):   "api",
	}, targets)
}

func TestSnapshotFromTxnResponseKeepsConfiguredTargetsAtOneRevision(t *testing.T) {
	client := newClient(nil, time.Second)
	targets, err := client.targetIDSet([]string{"nginx", "missing"})
	require.NoError(t, err)
	response := txnResponse(42, []*mvccpb.KeyValue{{
		Key:         []byte(client.ActiveKey("nginx")),
		Value:       []byte("sha256-current"),
		ModRevision: 41,
	}, nil})

	snapshot, err := snapshotFromTxnResponse(response, targets)
	require.NoError(t, err)
	require.Equal(t, int64(42), snapshot.Revision)
	require.Equal(t, map[string]string{"nginx": "sha256-current"}, snapshot.Active)
}

func TestSnapshotFromTxnResponseRejectsUnexpectedAndUnsafeKeys(t *testing.T) {
	client := newClient(nil, time.Second)
	targets, err := client.targetIDSet([]string{"nginx"})
	require.NoError(t, err)

	response := txnResponse(42, []*mvccpb.KeyValue{{
		Key:   []byte(client.ActiveKey("other")),
		Value: []byte("sha256-current"),
	}})
	_, err = snapshotFromTxnResponse(response, targets)
	require.ErrorContains(t, err, "unexpected key")

	response = txnResponse(42, []*mvccpb.KeyValue{{
		Key:   []byte(client.ActiveKey("nginx")),
		Value: []byte("../unsafe"),
	}})
	_, err = snapshotFromTxnResponse(response, targets)
	require.ErrorContains(t, err, "invalid active pointer")
}

func TestSnapshotFromTxnResponseRejectsWrongResponseCount(t *testing.T) {
	client := newClient(nil, time.Second)
	targets, err := client.targetIDSet([]string{"nginx", "missing"})
	require.NoError(t, err)
	response := txnResponse(42, nil)

	_, err = snapshotFromTxnResponse(response, targets)
	require.ErrorContains(t, err, "want 2")
}

func txnResponse(revision int64, kvs []*mvccpb.KeyValue) *clientv3.TxnResponse {
	responses := make([]*pb.ResponseOp, 0, len(kvs))
	for _, kv := range kvs {
		responseKvs := []*mvccpb.KeyValue(nil)
		if kv != nil {
			responseKvs = []*mvccpb.KeyValue{kv}
		}
		responses = append(responses, &pb.ResponseOp{
			Response: &pb.ResponseOp_ResponseRange{
				ResponseRange: &pb.RangeResponse{Kvs: responseKvs},
			},
		})
	}
	return &clientv3.TxnResponse{
		Header:    &pb.ResponseHeader{Revision: revision},
		Responses: responses,
	}
}
