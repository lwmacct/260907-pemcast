package publisher

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	"github.com/lwmacct/260907-pemcast/internal/keyspace"
	"github.com/lwmacct/260907-pemcast/internal/pack"
)

func TestPublishInitialNoOpAndUpdate(t *testing.T) {
	first, second := packFixture(t, "nginx", "a.example", "b.example")
	kv := newFakeKV("/pemcast")

	outcome, err := Publish(t.Context(), kv, first)
	require.NoError(t, err)
	require.Equal(t, OutcomePublished, outcome)
	require.Equal(t, first.Metadata.Generation, kv.values[kv.ActiveKey("nginx")].Data)
	require.Equal(t, string(first.Bundle), kv.values[kv.BundleKey("nginx", first.Metadata.Generation)].Data)

	outcome, err = Publish(t.Context(), kv, first)
	require.NoError(t, err)
	require.Equal(t, OutcomeNoOp, outcome)

	outcome, err = Publish(t.Context(), kv, second)
	require.NoError(t, err)
	require.Equal(t, OutcomePublished, outcome)
	require.Equal(t, second.Metadata.Generation, kv.values[kv.ActiveKey("nginx")].Data)
	require.Contains(t, kv.values, kv.BundleKey("nginx", first.Metadata.Generation))
	require.Contains(t, kv.values, kv.BundleKey("nginx", second.Metadata.Generation))
}

func TestPublishRejectsExistingBundleMismatch(t *testing.T) {
	result := packFixtureOne(t, "nginx", "a.example")
	kv := newFakeKV("/pemcast")
	kv.values[result.Metadata.BundleKey] = etcdsource.Value{
		Data:        "different bytes",
		ModRevision: 1,
		Exists:      true,
	}

	_, err := Publish(t.Context(), kv, result)
	require.ErrorContains(t, err, "different bytes")
}

func TestPublishRejectsStaleActiveCAS(t *testing.T) {
	first, second := packFixture(t, "nginx", "a.example", "b.example")
	kv := newFakeKV("/pemcast")
	_, err := Publish(t.Context(), kv, first)
	require.NoError(t, err)

	kv.failSwap = true
	_, err = Publish(t.Context(), kv, second)
	require.ErrorContains(t, err, "active pointer changed during publication")
	require.Equal(t, first.Metadata.Generation, kv.values[kv.ActiveKey("nginx")].Data)
}

func TestPublishRejectsPrefixMismatch(t *testing.T) {
	result := packFixtureOne(t, "nginx", "a.example")
	kv := newFakeKV("/tenants/other")

	_, err := Publish(t.Context(), kv, result)
	require.ErrorContains(t, err, "does not match configured prefix")
}

func TestPublishRejectsUnsafeRemoteActivePointer(t *testing.T) {
	result := packFixtureOne(t, "nginx", "a.example")
	kv := newFakeKV("/pemcast")
	kv.values[result.Metadata.ActiveKey] = etcdsource.Value{
		Data:        "../unsafe",
		ModRevision: 7,
		Exists:      true,
	}

	_, err := Publish(t.Context(), kv, result)
	require.ErrorContains(t, err, "unsafe")
}

type fakeKV struct {
	prefix   string
	keys     keyspace.Keys
	values   map[string]etcdsource.Value
	revision int64
	failSwap bool
}

func newFakeKV(prefix string) *fakeKV {
	return &fakeKV{
		prefix: prefix,
		keys:   keyspace.Default(),
		values: make(map[string]etcdsource.Value),
	}
}

func (kv *fakeKV) Prefix() string { return kv.prefix }

func (kv *fakeKV) ActiveKey(targetID string) string {
	return kv.keys.ActiveKey(targetID)
}

func (kv *fakeKV) BundleKey(targetID, generation string) string {
	return kv.keys.BundleKey(targetID, generation)
}

func (kv *fakeKV) Get(_ context.Context, key string) (etcdsource.Value, error) {
	return kv.values[key], nil
}

func (kv *fakeKV) StageBundleIfAbsent(_ context.Context, key, value string) (bool, error) {
	if _, exists := kv.values[key]; exists {
		return false, nil
	}
	kv.put(key, value)
	return true, nil
}

func (kv *fakeKV) SwapActive(
	_ context.Context,
	activeKey, generation string,
	expected etcdsource.ActiveCondition,
) (bool, error) {
	if kv.failSwap {
		return false, nil
	}
	current := kv.values[activeKey]
	if current.Exists != expected.Exists {
		return false, nil
	}
	if expected.Exists && (current.Data != expected.Generation || current.ModRevision != expected.ModRevision) {
		return false, nil
	}
	kv.put(activeKey, generation)
	return true, nil
}

func (kv *fakeKV) put(key, value string) {
	kv.revision++
	kv.values[key] = etcdsource.Value{Data: value, ModRevision: kv.revision, Exists: true}
}

func packFixture(t *testing.T, target string, commonNames ...string) (pack.Result, pack.Result) {
	t.Helper()

	require.Len(t, commonNames, 2)
	return packFixtureOne(t, target, commonNames[0]), packFixtureOne(t, target, commonNames[1])
}

func packFixtureOne(t *testing.T, target, commonName string) pack.Result {
	t.Helper()

	certificatePath, privateKeyPath := writeKeyPair(t, commonName)
	result, err := pack.Build(pack.Options{
		TargetID:        target,
		EtcdPrefix:      "/pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	require.NoError(t, err)
	return result
}

func writeKeyPair(t *testing.T, commonName string) (string, string) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)

	directory := t.TempDir()
	certificatePath := filepath.Join(directory, "fullchain.pem")
	privateKeyPath := filepath.Join(directory, "privkey.pem")
	certificatePEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	require.NoError(t, os.WriteFile(certificatePath, certificatePEM, 0o600))
	require.NoError(t, os.WriteFile(privateKeyPath, keyPEM, 0o600))
	return certificatePath, privateKeyPath
}
