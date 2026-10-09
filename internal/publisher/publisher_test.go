package publisher

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
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
)

type fakeKV struct {
	values     map[string]etcdsource.Value
	revision   int64
	failAtomic bool
}

func (kv *fakeKV) Get(_ context.Context, key string) (etcdsource.Value, error) {
	return kv.values[key], nil
}

func (kv *fakeKV) CreateBundleAndSwapActive(
	_ context.Context,
	bundleKey, bundleValue, activeKey, generation string,
	expected etcdsource.ActiveCondition,
) (bool, error) {
	if kv.failAtomic {
		return false, errors.New("transaction failed")
	}
	if existing, exists := kv.values[bundleKey]; exists && existing.Exists {
		return false, nil
	}
	if !activeMatches(kv.values[activeKey], expected) {
		return false, nil
	}
	kv.commit(map[string]string{bundleKey: bundleValue, activeKey: generation})
	return true, nil
}

func (kv *fakeKV) SwapActive(_ context.Context, activeKey, generation string, expected etcdsource.ActiveCondition) (bool, error) {
	if !activeMatches(kv.values[activeKey], expected) {
		return false, nil
	}
	kv.commit(map[string]string{activeKey: generation})
	return true, nil
}

func (kv *fakeKV) commit(changes map[string]string) {
	kv.revision++
	for key, value := range changes {
		kv.values[key] = etcdsource.Value{Data: value, ModRevision: kv.revision, Exists: true}
	}
}

func activeMatches(actual etcdsource.Value, expected etcdsource.ActiveCondition) bool {
	if actual.Exists != expected.Exists {
		return false
	}
	if !expected.Exists {
		return true
	}
	return actual.Data == expected.Generation && actual.ModRevision == expected.ModRevision
}

func writeKeyPair(t *testing.T) (string, string) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "publisher-test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)

	dir := t.TempDir()
	certificatePath := filepath.Join(dir, "fullchain.pem")
	privateKeyPath := filepath.Join(dir, "privkey.pem")
	require.NoError(t, os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}), 0o600))
	require.NoError(t, os.WriteFile(privateKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certificatePath, privateKeyPath
}

func planFixture(t *testing.T, activeGeneration string, activeRevision int64) (PlanOptions, Plan, *fakeKV) {
	t.Helper()

	certificatePath, privateKeyPath := writeKeyPair(t)
	kv := &fakeKV{values: make(map[string]etcdsource.Value), revision: activeRevision}
	if activeGeneration != "" {
		kv.values[etcdsource.ActiveKey("nginx")] = etcdsource.Value{
			Data: activeGeneration, ModRevision: activeRevision, Exists: true,
		}
	}
	options := PlanOptions{
		TargetID:                 "nginx",
		CertificatePath:          certificatePath,
		PrivateKeyPath:           privateKeyPath,
		ExpectedActiveGeneration: activeGeneration,
		Initial:                  activeGeneration == "",
	}
	plan, err := CreatePlan(t.Context(), kv, options)
	require.NoError(t, err)
	return options, plan, kv
}

func TestCreatePlanRequiresExplicitExpectedState(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t)
	kv := &fakeKV{values: make(map[string]etcdsource.Value)}
	_, err := CreatePlan(t.Context(), kv, PlanOptions{
		TargetID: "nginx", CertificatePath: certificatePath, PrivateKeyPath: privateKeyPath,
	})
	require.ErrorContains(t, err, "exactly one of --initial or --expected-active-generation")
}

func TestInspectReportsActivePointer(t *testing.T) {
	kv := &fakeKV{values: map[string]etcdsource.Value{
		etcdsource.ActiveKey("nginx"): {Data: "sha256-current", ModRevision: 42, Exists: true},
	}}

	state, err := Inspect(t.Context(), kv, "nginx")
	require.NoError(t, err)
	require.Equal(t, ActiveState{
		TargetID: "nginx",
		Active:   Active{Exists: true, Generation: "sha256-current", ModRevision: 42},
	}, state)
}

func TestInspectReportsMissingPointer(t *testing.T) {
	kv := &fakeKV{values: map[string]etcdsource.Value{}}

	state, err := Inspect(t.Context(), kv, "nginx")
	require.NoError(t, err)
	require.False(t, state.Active.Exists)
	require.Empty(t, state.Active.Generation)
	require.Zero(t, state.Active.ModRevision)
}

func TestInspectRejectsUnsafePointer(t *testing.T) {
	kv := &fakeKV{values: map[string]etcdsource.Value{
		etcdsource.ActiveKey("nginx"): {Data: "../unsafe", ModRevision: 42, Exists: true},
	}}

	_, err := Inspect(t.Context(), kv, "nginx")
	require.ErrorContains(t, err, `active pointer "../unsafe" is unsafe`)
}

func TestCreatePlanRejectsUnexpectedActiveGeneration(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t)
	kv := &fakeKV{values: map[string]etcdsource.Value{
		etcdsource.ActiveKey("nginx"): {Data: "sha256-current", ModRevision: 10, Exists: true},
	}}
	_, err := CreatePlan(t.Context(), kv, PlanOptions{
		TargetID: "nginx", CertificatePath: certificatePath, PrivateKeyPath: privateKeyPath,
		ExpectedActiveGeneration: "sha256-other",
	})
	require.ErrorContains(t, err, "expected \"sha256-other\"")
}

func TestApplyInitialAtomicallyCreatesBundleAndPointer(t *testing.T) {
	_, plan, kv := planFixture(t, "", 0)
	require.NoError(t, Apply(t.Context(), kv, plan))

	active := kv.values[etcdsource.ActiveKey("nginx")]
	require.True(t, active.Exists)
	require.Equal(t, plan.Generation, active.Data)
	remote := kv.values[etcdsource.BundleKey("nginx", plan.Generation)]
	require.True(t, remote.Exists)
	_, files, digest, err := bundle.Decode([]byte(remote.Data))
	require.NoError(t, err)
	require.Equal(t, plan.BundleSHA256, digest)
	require.NotEmpty(t, files["fullchain.pem"])
}

func TestApplyRejectsLocalMaterialChangedAfterPlan(t *testing.T) {
	_, plan, kv := planFixture(t, "", 0)
	require.NoError(t, os.WriteFile(plan.CertificatePath, []byte("changed"), 0o600))

	require.ErrorContains(t, Apply(t.Context(), kv, plan), "changed after publish plan")
	require.Empty(t, kv.values[etcdsource.ActiveKey("nginx")])
	require.Empty(t, kv.values[etcdsource.BundleKey("nginx", plan.Generation)])
}

func TestApplyRejectsActiveModRevisionChange(t *testing.T) {
	_, plan, kv := planFixture(t, "sha256-old", 10)
	kv.values[etcdsource.ActiveKey("nginx")] = etcdsource.Value{
		Data: "sha256-old", ModRevision: 11, Exists: true,
	}
	require.ErrorContains(t, Apply(t.Context(), kv, plan), "remote state changed")
	require.Empty(t, kv.values[etcdsource.BundleKey("nginx", plan.Generation)])
	require.Equal(t, int64(11), kv.values[etcdsource.ActiveKey("nginx")].ModRevision)
}

func TestApplyAtomicFailureWritesNeitherKey(t *testing.T) {
	_, plan, kv := planFixture(t, "", 0)
	kv.failAtomic = true
	err := Apply(t.Context(), kv, plan)
	require.ErrorContains(t, err, "transaction failed")
	require.Empty(t, kv.values[etcdsource.ActiveKey("nginx")])
	require.Empty(t, kv.values[etcdsource.BundleKey("nginx", plan.Generation)])
}

func TestApplyUsesExistingIdenticalBundleWithoutRewriting(t *testing.T) {
	_, plan, kv := planFixture(t, "sha256-old", 10)
	manifest, _ := bundle.NewTLSManifest(
		mustRead(t, plan.CertificatePath), mustRead(t, plan.PrivateKeyPath), "fullchain.pem", "privkey.pem",
	)
	encoded := string(mustEncode(t, manifest))
	kv.values[etcdsource.BundleKey("nginx", plan.Generation)] = etcdsource.Value{
		Data: encoded, ModRevision: 9, Exists: true,
	}

	require.NoError(t, Apply(t.Context(), kv, plan))
	require.Equal(t, encoded, kv.values[etcdsource.BundleKey("nginx", plan.Generation)].Data)
	require.Equal(t, plan.Generation, kv.values[etcdsource.ActiveKey("nginx")].Data)
}

func TestApplyRejectsExistingGenerationWithDifferentContent(t *testing.T) {
	_, plan, kv := planFixture(t, "sha256-old", 10)
	kv.values[etcdsource.BundleKey("nginx", plan.Generation)] = etcdsource.Value{
		Data: "different", ModRevision: 9, Exists: true,
	}
	err := Apply(t.Context(), kv, plan)
	require.ErrorContains(t, err, "contains different data")
	require.Equal(t, "sha256-old", kv.values[etcdsource.ActiveKey("nginx")].Data)
}

func TestActivateVerifiesRemoteBundleAndUsesModRevisionCAS(t *testing.T) {
	_, initialPlan, kv := planFixture(t, "", 0)
	require.NoError(t, Apply(t.Context(), kv, initialPlan))
	current := kv.values[etcdsource.ActiveKey("nginx")]
	require.NoError(t, Activate(t.Context(), kv, ActivateOptions{
		TargetID: "nginx", Generation: initialPlan.Generation,
		ExpectedActiveGeneration:  current.Data,
		ExpectedActiveModRevision: current.ModRevision,
	}))

	kv.values[etcdsource.ActiveKey("nginx")] = etcdsource.Value{
		Data: current.Data, ModRevision: current.ModRevision + 1, Exists: true,
	}
	err := Activate(t.Context(), kv, ActivateOptions{
		TargetID: "nginx", Generation: initialPlan.Generation,
		ExpectedActiveGeneration:  current.Data,
		ExpectedActiveModRevision: current.ModRevision,
	})
	require.ErrorContains(t, err, "active pointer ModRevision is 2, expected 1")
}

func TestActivateRejectsMissingGeneration(t *testing.T) {
	kv := &fakeKV{values: map[string]etcdsource.Value{
		etcdsource.ActiveKey("nginx"): {Data: "sha256-old", ModRevision: 10, Exists: true},
	}}
	err := Activate(t.Context(), kv, ActivateOptions{
		TargetID: "nginx", Generation: "sha256-missing",
		ExpectedActiveGeneration:  "sha256-old",
		ExpectedActiveModRevision: 10,
	})
	require.ErrorContains(t, err, "does not exist")
}

func TestActivateRejectsRemoteKeyPairMismatch(t *testing.T) {
	_, initialPlan, kv := planFixture(t, "", 0)
	require.NoError(t, Apply(t.Context(), kv, initialPlan))
	certificate := mustRead(t, initialPlan.CertificatePath)
	manifest, digest := bundle.NewTLSManifest(certificate, certificate, "fullchain.pem", "privkey.pem")
	encoded := string(mustEncode(t, manifest))
	generation := bundle.Generation(digest)
	kv.values[etcdsource.BundleKey("nginx", generation)] = etcdsource.Value{
		Data: encoded, ModRevision: 1, Exists: true,
	}

	err := Activate(t.Context(), kv, ActivateOptions{
		TargetID: "nginx", Generation: generation,
		ExpectedActiveGeneration:  initialPlan.Generation,
		ExpectedActiveModRevision: kv.values[etcdsource.ActiveKey("nginx")].ModRevision,
	})
	require.ErrorContains(t, err, "parse TLS key pair")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func mustEncode(t *testing.T, manifest bundle.Manifest) []byte {
	t.Helper()

	data, err := bundle.Encode(manifest)
	require.NoError(t, err)
	return data
}

func TestPlanSchemaAndPaths(t *testing.T) {
	_, plan, _ := planFixture(t, "sha256-old", 10)
	require.Equal(t, "pemcast-publish/v2", PlanSchema)
	require.Equal(t, PlanSchema, plan.Schema)
	require.True(t, filepath.IsAbs(plan.CertificatePath))
	require.True(t, filepath.IsAbs(plan.PrivateKeyPath))
	require.Equal(t, fmt.Sprintf("sha256-%s", plan.BundleSHA256), plan.Generation)
}
