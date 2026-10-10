package prune

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
)

type fakeKV struct {
	mu             sync.Mutex
	prefix         string
	records        map[string]etcdsource.BundleRecord
	active         map[string]etcdsource.ActivePointer
	deleted        map[string]struct{}
	changeOnDelete bool
}

func newFakeKV(t *testing.T) *fakeKV {
	t.Helper()
	return &fakeKV{
		prefix:  "/pemcast",
		records: make(map[string]etcdsource.BundleRecord),
		active:  make(map[string]etcdsource.ActivePointer),
		deleted: make(map[string]struct{}),
	}
}

func (k *fakeKV) Prefix() string { return k.prefix }

func (k *fakeKV) SnapshotActivePrefix(_ context.Context, activePrefix string) (etcdsource.PrefixSnapshot, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	active := make(map[string]etcdsource.ActivePointer, len(k.active))
	for key, pointer := range k.active {
		active[strings.TrimPrefix(key, activePrefix)] = pointer
	}
	return etcdsource.PrefixSnapshot{Revision: 100, Active: active}, nil
}

func (k *fakeKV) ListBundleRecords(_ context.Context, bundlePrefix string) ([]etcdsource.BundleRecord, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	var records []etcdsource.BundleRecord
	for key, record := range k.records {
		if len(key) >= len(bundlePrefix) && key[:len(bundlePrefix)] == bundlePrefix {
			records = append(records, record)
		}
	}
	sort.Slice(records, func(i, j int) bool { return records[i].Key < records[j].Key })
	return records, nil
}

func (k *fakeKV) DeleteBundleIfActiveUnchanged(
	_ context.Context, bundleKey, activeKey string, expected etcdsource.ActiveCondition,
) (bool, error) {
	k.mu.Lock()
	defer k.mu.Unlock()
	if k.changeOnDelete {
		k.changeOnDelete = false
		pointer := k.active[activeKey]
		pointer.Generation = "sha256-" + "0"
		pointer.ModRevision++
		k.active[activeKey] = pointer
	}
	active, exists := k.active[activeKey]
	if !exists || active.Generation != expected.Generation || active.ModRevision != expected.ModRevision {
		return false, nil
	}
	record, exists := k.records[bundleKey]
	if !exists {
		return false, errors.New("bundle disappeared")
	}
	if active.Generation == record.Generation {
		return false, errors.New("refused to delete active bundle")
	}
	delete(k.records, bundleKey)
	k.deleted[bundleKey] = struct{}{}
	return true, nil
}

func TestPruneDryRunDeletesNothingAndClassifiesBundles(t *testing.T) {
	kv := newFakeKV(t)
	now := time.Now().UTC().Round(0)
	activeBundle := addIdentity(t, kv, "nginx", now.Add(30*24*time.Hour))
	expiredBundle := addIdentity(t, kv, "nginx", now.Add(-40*24*time.Hour))
	recentExpired := addIdentity(t, kv, "nginx", now.Add(-10*24*time.Hour))
	trustBundle := addTrust(t, kv, "trust")
	kv.active["/pemcast/v6/active/nginx"] = etcdsource.ActivePointer{
		Generation: activeBundle, ModRevision: 10,
	}

	result, err := Prune(t.Context(), kv, Options{Prefix: "/pemcast", Now: now, Retention: 30 * 24 * time.Hour})
	require.NoError(t, err)
	require.Len(t, result.Items, 4)
	require.Equal(t, 4, result.Scanned)
	require.Equal(t, 1, result.Eligible)
	require.Equal(t, 0, result.Deleted)
	require.Empty(t, kv.deleted)
	requireStatus(t, result, "nginx", activeBundle, "active")
	requireStatus(t, result, "nginx", expiredBundle, "would-delete")
	requireStatus(t, result, "nginx", recentExpired, "retained")
	requireStatus(t, result, "trust", trustBundle, "trust-skipped")
}

func TestPruneDeletesOnlyEligibleInactiveExactKey(t *testing.T) {
	kv := newFakeKV(t)
	now := time.Now().UTC().Round(0)
	activeBundle := addIdentity(t, kv, "nginx", now.Add(30*24*time.Hour))
	expiredBundle := addIdentity(t, kv, "nginx", now.Add(-31*24*time.Hour))
	recentExpired := addIdentity(t, kv, "nginx", now.Add(-29*24*time.Hour))
	kv.active["/pemcast/v6/active/nginx"] = etcdsource.ActivePointer{
		Generation: activeBundle, ModRevision: 10,
	}

	result, err := Prune(t.Context(), kv, Options{
		Prefix: "/pemcast", Now: now, Retention: 30 * 24 * time.Hour, Delete: true,
	})
	require.NoError(t, err)
	require.Equal(t, 1, result.Eligible)
	require.Equal(t, 1, result.Deleted)
	require.Contains(t, kv.deleted, fmt.Sprintf("/pemcast/v6/bundles/nginx/%s", expiredBundle))
	require.NotContains(t, kv.deleted, fmt.Sprintf("/pemcast/v6/bundles/nginx/%s", activeBundle))
	require.NotContains(t, kv.deleted, fmt.Sprintf("/pemcast/v6/bundles/nginx/%s", recentExpired))
}

func TestPruneRejectsActiveChangeAndInvalidInput(t *testing.T) {
	kv := newFakeKV(t)
	now := time.Now().UTC().Round(0)
	activeBundle := addIdentity(t, kv, "nginx", now.Add(30*24*time.Hour))
	expiredBundle := addIdentity(t, kv, "nginx", now.Add(-40*24*time.Hour))
	kv.active["/pemcast/v6/active/nginx"] = etcdsource.ActivePointer{
		Generation: activeBundle, ModRevision: 10,
	}
	kv.changeOnDelete = true

	_, err := Prune(t.Context(), kv, Options{
		Prefix: "/pemcast", Now: now, Retention: 30 * 24 * time.Hour, Delete: true,
	})
	require.ErrorContains(t, err, "active pointer changed")
	require.NotContains(t, kv.deleted, fmt.Sprintf("/pemcast/v6/bundles/nginx/%s", expiredBundle))

	_, err = Prune(t.Context(), newFakeKV(t), Options{Prefix: "/pemcast", Retention: -time.Second})
	require.ErrorContains(t, err, "retention must not be negative")
}

func requireStatus(t *testing.T, result Result, targetID, generation, status string) {
	t.Helper()
	for _, item := range result.Items {
		if item.TargetID == targetID && item.Generation == generation {
			require.Equal(t, status, item.Status)
			return
		}
	}
	t.Fatalf("result missing %s/%s", targetID, generation)
}

func addIdentity(t *testing.T, kv *fakeKV, targetID string, notAfter time.Time) string {
	t.Helper()
	certificate, privateKey := testKeyPair(t, notAfter)
	manifest, digest := bundle.NewTLSManifest(bundle.TypeTLSServer, certificate, privateKey)
	encoded, err := bundle.Encode(manifest)
	require.NoError(t, err)
	generation := bundle.Generation(digest)
	key := fmt.Sprintf("/pemcast/v6/bundles/%s/%s", targetID, generation)
	kv.records[key] = etcdsource.BundleRecord{
		Key: key, TargetID: targetID, Generation: generation,
		Data: encoded, ModRevision: 10,
	}
	return generation
}

func addTrust(t *testing.T, kv *fakeKV, targetID string) string {
	t.Helper()
	caPEM, err := testCA()
	require.NoError(t, err)
	manifest, digest := bundle.NewTrustManifest(caPEM)
	encoded, err := bundle.Encode(manifest)
	require.NoError(t, err)
	generation := bundle.Generation(digest)
	key := fmt.Sprintf("/pemcast/v6/bundles/%s/%s", targetID, generation)
	kv.records[key] = etcdsource.BundleRecord{
		Key: key, TargetID: targetID, Generation: generation,
		Data: encoded, ModRevision: 10,
	}
	return generation
}

func testKeyPair(t *testing.T, notAfter time.Time) ([]byte, []byte) {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "prune-test"},
		NotBefore:    notAfter.Add(-90 * 24 * time.Hour),
		NotAfter:     notAfter,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func testCA() ([]byte, error) {
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "prune-test-ca"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour),
		IsCA: true, BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	if err != nil {
		return nil, err
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), nil
}
