package upgrade

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"encoding/pem"
	"math/big"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	legacy "github.com/lwmacct/260907-pemcast/internal/upgrade/legacy"
)

type fakeValue struct {
	data        string
	modRevision int64
}

type fakeKV struct {
	prefix     string
	values     map[string]fakeValue
	deleted    map[string]int64
	afterStage func(targetID string)
}

func newFakeKV(t *testing.T) *fakeKV {
	t.Helper()

	return &fakeKV{
		prefix:  "/pemcast",
		values:  make(map[string]fakeValue),
		deleted: make(map[string]int64),
	}
}

func (k *fakeKV) Prefix() string { return k.prefix }

func (k *fakeKV) Get(_ context.Context, key string) (etcdsource.Value, error) {
	current, exists := k.values[key]
	if !exists {
		return etcdsource.Value{}, nil
	}
	return etcdsource.Value{Data: current.data, ModRevision: current.modRevision, Exists: true}, nil
}

func (k *fakeKV) GetAt(ctx context.Context, key string, _ int64) (etcdsource.Value, error) {
	return k.Get(ctx, key)
}

func (k *fakeKV) SnapshotActivePrefix(_ context.Context, activePrefix string) (etcdsource.PrefixSnapshot, error) {
	result := etcdsource.PrefixSnapshot{Revision: 100, Active: make(map[string]etcdsource.ActivePointer)}
	for key, current := range k.values {
		if !strings.HasPrefix(key, activePrefix) {
			continue
		}
		targetID := strings.TrimPrefix(key, activePrefix)
		result.Active[targetID] = etcdsource.ActivePointer{
			Generation: current.data, ModRevision: current.modRevision,
		}
	}
	return result, nil
}

func (k *fakeKV) StageBundleIfAbsent(_ context.Context, key, data string) (bool, error) {
	if _, exists := k.values[key]; exists {
		return false, nil
	}
	k.put(key, data)
	if k.afterStage != nil {
		targetID := strings.Split(strings.TrimPrefix(key, "/pemcast/v6/bundles/"), "/")[0]
		k.afterStage(targetID)
	}
	return true, nil
}

func (k *fakeKV) SwapActiveFromSource(
	_ context.Context,
	sourceKey string,
	source etcdsource.ActiveCondition,
	destinationKey, generation string,
	destination etcdsource.ActiveCondition,
) (bool, error) {
	current, exists := k.values[sourceKey]
	if !exists || current.data != source.Generation || current.modRevision != source.ModRevision {
		return false, nil
	}
	currentDestination, destinationExists := k.values[destinationKey]
	if destinationExists != destination.Exists {
		return false, nil
	}
	if destinationExists &&
		(currentDestination.data != destination.Generation || currentDestination.modRevision != destination.ModRevision) {
		return false, nil
	}
	k.put(destinationKey, generation)
	return true, nil
}

func (k *fakeKV) DeletePrefix(_ context.Context, prefix string) (int64, error) {
	var keys []string
	for key := range k.values {
		if strings.HasPrefix(key, prefix) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	for _, key := range keys {
		k.deleted[key] = k.values[key].modRevision
		delete(k.values, key)
	}
	return int64(len(keys)), nil
}

func (k *fakeKV) put(key, data string) {
	current, exists := k.values[key]
	if exists && current.data == data {
		return
	}
	k.values[key] = fakeValue{data: data, modRevision: current.modRevision + 1}
}

func TestUpgradeDryRunDoesNotWrite(t *testing.T) {
	kv := newFakeKV(t)
	seedV5Target(t, kv, "nginx", "server")

	result, err := Upgrade(t.Context(), kv, Options{
		Prefix: "/pemcast", DefaultType: bundle.TypeTLSServer,
		DryRun: true, DeleteOld: true,
	})
	require.NoError(t, err)
	require.True(t, result.DryRun)
	require.False(t, result.DeletedOld)
	require.Len(t, result.Targets, 1)
	require.Equal(t, "would-migrate", result.Targets[0].Status)
	require.NotEqual(t, result.Targets[0].SourceGeneration, result.Targets[0].ResultGeneration)

	requireV5Intact(t, kv, "nginx", result.Targets[0].SourceGeneration)
	require.NotContains(t, kv.values, "/pemcast/v6/active/nginx")
	require.NotContains(t, kv.values, destinationBundleKey(result.Targets[0]))
	require.Empty(t, kv.deleted)
}

func TestUpgradeRequiresConfirmationToDeleteOldDuringApply(t *testing.T) {
	kv := newFakeKV(t)
	seedV5Target(t, kv, "nginx", "server")

	_, err := Upgrade(t.Context(), kv, Options{
		Prefix: "/pemcast", DefaultType: bundle.TypeTLSServer, DeleteOld: true,
	})
	require.ErrorContains(t, err, "--delete-old-v5 also requires --yes")
	requireV5Intact(t, kv, "nginx", legacyGeneration(t, kv, "nginx"))
	require.NotContains(t, kv.values, "/pemcast/v6/active/nginx")
}

func TestUpgradeMigratesAndIsIdempotent(t *testing.T) {
	kv := newFakeKV(t)
	seedV5Target(t, kv, "nginx", "server")

	result, err := Upgrade(t.Context(), kv, Options{
		Prefix: "/pemcast", DefaultType: bundle.TypeTLSServer,
	})
	require.NoError(t, err)
	require.False(t, result.DeletedOld)
	require.Equal(t, "migrated", result.Targets[0].Status)
	require.Equal(t, result.Targets[0].ResultGeneration, kv.values["/pemcast/v6/active/nginx"].data)
	require.Equal(t, string(expectedV6Bundle(t, kv, result.Targets[0])), kv.values[destinationBundleKey(result.Targets[0])].data)
	requireV5Intact(t, kv, "nginx", result.Targets[0].SourceGeneration)

	again, err := Upgrade(t.Context(), kv, Options{
		Prefix: "/pemcast", DefaultType: bundle.TypeTLSServer,
	})
	require.NoError(t, err)
	require.Equal(t, "already-migrated", again.Targets[0].Status)
	require.Equal(t, int64(1), kv.values["/pemcast/v6/active/nginx"].modRevision)
}

func TestUpgradeUsesTargetOverridesAndCanDeleteOldAfterPostverify(t *testing.T) {
	kv := newFakeKV(t)
	seedV5Target(t, kv, "nginx", "server")
	seedV5Target(t, kv, "etcd-client", "client")
	targetTypes, err := NormalizeTargetTypes([]string{"etcd-client=tls-client"})
	require.NoError(t, err)

	result, err := Upgrade(t.Context(), kv, Options{
		Prefix: "/pemcast", DefaultType: bundle.TypeTLSServer, TargetTypes: targetTypes,
		DeleteOld: true, ConfirmDelete: true,
	})
	require.NoError(t, err)
	require.True(t, result.DeletedOld)
	require.GreaterOrEqual(t, result.DeletedKeys, int64(4))
	require.NotContains(t, kv.values, "/pemcast/v5/active/nginx")
	require.NotContains(t, kv.values, "/pemcast/v5/bundles/nginx/"+result.Targets[0].SourceGeneration)
	require.Contains(t, kv.values, "/pemcast/v6/active/nginx")
	require.Contains(t, kv.values, "/pemcast/v6/active/etcd-client")
}

func TestUpgradeRejectsSourceChangeDuringCommit(t *testing.T) {
	kv := newFakeKV(t)
	seedV5Target(t, kv, "nginx", "server")
	sourceGeneration := legacyGeneration(t, kv, "nginx")
	kv.afterStage = func(targetID string) {
		kv.put("/pemcast/v5/active/"+targetID, "sha256-"+strings.Repeat("1", 64))
	}

	_, err := Upgrade(t.Context(), kv, Options{Prefix: "/pemcast", DefaultType: bundle.TypeTLSServer})
	require.ErrorContains(t, err, "changed during upgrade")
	require.NotContains(t, kv.values, "/pemcast/v6/active/nginx")
	require.Contains(t, kv.values, destinationBundleKeyForGeneration(sourceGeneration, t, kv, "nginx"))
	require.NotEqual(t, sourceGeneration, kv.values["/pemcast/v5/active/nginx"].data)
}

func TestUpgradeRejectsDivergentV6ActivePointer(t *testing.T) {
	kv := newFakeKV(t)
	seedV5Target(t, kv, "nginx", "server")
	kv.put("/pemcast/v6/active/nginx", "sha256-"+strings.Repeat("2", 64))

	_, err := Upgrade(t.Context(), kv, Options{Prefix: "/pemcast", DefaultType: bundle.TypeTLSServer})
	require.ErrorContains(t, err, "but upgrade would produce")
}

func TestUpgradeRejectsMissingAndInvalidTypeMapping(t *testing.T) {
	kv := newFakeKV(t)
	seedV5Target(t, kv, "nginx", "server")

	_, err := Upgrade(t.Context(), kv, Options{Prefix: "/pemcast"})
	require.ErrorContains(t, err, "missing or invalid destination type")

	_, err = Upgrade(t.Context(), kv, Options{Prefix: "/pemcast", DefaultType: bundle.TypeTrust})
	require.ErrorContains(t, err, "missing or invalid destination type")

	_, err = NormalizeTargetTypes([]string{"nginx"})
	require.ErrorContains(t, err, "invalid target type mapping")
	_, err = NormalizeTargetTypes([]string{"nginx=trust"})
	require.ErrorContains(t, err, "invalid target type")

	targetTypes, err := NormalizeTargetTypes([]string{"missing=tls-client"})
	require.NoError(t, err)
	_, err = Upgrade(t.Context(), kv, Options{
		Prefix: "/pemcast", DefaultType: bundle.TypeTLSServer, TargetTypes: targetTypes,
	})
	require.ErrorContains(t, err, "does not reference an active v5 target")
}

func TestUpgradeRejectsUnsupportedAndMixedProtocolState(t *testing.T) {
	kv := newFakeKV(t)
	kv.put("/pemcast/v4/active/nginx", "sha256-old")
	_, err := Upgrade(t.Context(), kv, Options{Prefix: "/pemcast", DefaultType: bundle.TypeTLSServer})
	require.ErrorContains(t, err, "only v5 can be upgraded")

	kv = newFakeKV(t)
	kv.put("/pemcast/v6/active/new", "sha256-new")
	_, err = Upgrade(t.Context(), kv, Options{Prefix: "/pemcast", DefaultType: bundle.TypeTLSServer})
	require.ErrorContains(t, err, "only v5-to-v6")

	_, err = Upgrade(t.Context(), newFakeKV(t), Options{Prefix: "/pemcast", DefaultType: bundle.TypeTLSServer})
	require.ErrorContains(t, err, "no active v5 targets")
}

func seedV5Target(t *testing.T, kv *fakeKV, targetID, usage string) {
	t.Helper()

	certificate, privateKey := testKeyPair(t, usage)
	files := map[string][]byte{
		legacy.CertificateName: certificate,
		legacy.PrivateKeyName:  privateKey,
	}
	manifest := legacy.Manifest{
		Schema: legacy.Schema,
		Files: []legacy.ManifestFile{
			{
				Name: legacy.CertificateName, Kind: legacy.KindCertificate,
				Encoding: legacy.EncodingBase64, SHA256: hashBytes(certificate),
				Data: base64.StdEncoding.EncodeToString(certificate),
			},
			{
				Name: legacy.PrivateKeyName, Kind: legacy.KindPrivateKey,
				Encoding: legacy.EncodingBase64, SHA256: hashBytes(privateKey),
				Data: base64.StdEncoding.EncodeToString(privateKey),
			},
		},
		Pairs: []legacy.Pair{{
			Certificate: legacy.CertificateName, PrivateKey: legacy.PrivateKeyName,
		}},
	}
	encoded, err := json.Marshal(manifest)
	require.NoError(t, err)
	generation := bundle.Generation(legacy.ContentDigest(files))
	kv.put("/pemcast/v5/active/"+targetID, generation)
	kv.put("/pemcast/v5/bundles/"+targetID+"/"+generation, string(encoded))
}

func expectedV6Bundle(t *testing.T, kv *fakeKV, target TargetResult) []byte {
	t.Helper()

	sourceGeneration := kv.values["/pemcast/v5/active/"+target.TargetID].data
	sourceData := kv.values["/pemcast/v5/bundles/"+target.TargetID+"/"+sourceGeneration].data
	material, err := legacy.Decode(target.TargetID, sourceGeneration, []byte(sourceData))
	require.NoError(t, err)
	_, encoded, _, err := legacy.ConvertTLS(material, target.Type)
	require.NoError(t, err)
	return encoded
}

func destinationBundleKey(target TargetResult) string {
	return "/pemcast/v6/bundles/" + target.TargetID + "/" + target.ResultGeneration
}

func destinationBundleKeyForGeneration(sourceGeneration string, t *testing.T, kv *fakeKV, targetID string) string {
	t.Helper()

	for key, current := range kv.values {
		if strings.HasPrefix(key, "/pemcast/v6/bundles/"+targetID+"/") && current.data != "" {
			return key
		}
	}
	t.Fatalf("no staged v6 bundle for %q", targetID)
	return ""
}

func legacyGeneration(t *testing.T, kv *fakeKV, targetID string) string {
	t.Helper()

	return kv.values["/pemcast/v5/active/"+targetID].data
}

func requireV5Intact(t *testing.T, kv *fakeKV, targetID, generation string) {
	t.Helper()

	require.Equal(t, generation, kv.values["/pemcast/v5/active/"+targetID].data)
	require.Contains(t, kv.values, "/pemcast/v5/bundles/"+targetID+"/"+generation)
}

func testKeyPair(t *testing.T, usage string) ([]byte, []byte) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	var extended []x509.ExtKeyUsage
	if usage == "server" {
		extended = []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}
	} else {
		extended = []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}
	}
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "upgrade-test-" + usage},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  extended,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
