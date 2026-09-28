package publisher

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

type fakeKV struct {
	values    map[string]string
	putHook   func(key, value string) error
	activeKey string
}

var errSkipWrite = errors.New("skip write")

func (kv *fakeKV) Get(_ context.Context, key string) (string, bool, error) {
	value, exists := kv.values[key]
	return value, exists, nil
}

func (kv *fakeKV) PutIf(_ context.Context, key, value, expected string, expectedExists bool) (bool, error) {
	if kv.putHook != nil {
		if err := kv.putHook(key, value); err != nil {
			if errors.Is(err, errSkipWrite) {
				return true, nil
			}
			return false, err
		}
	}
	current, exists := kv.values[key]
	if exists != expectedExists || (exists && current != expected) {
		return false, nil
	}
	kv.values[key] = value
	return true, nil
}

func TestPublishRejectsExistingGenerationName(t *testing.T) {
	options, kv := publisherFixture(t)
	kv.values["/pemcast/v1/bundles/nginx/generation-1/files/fullchain.pem"] = "existing"
	err := Publish(t.Context(), kv, options)
	require.ErrorContains(t, err, "generation \"generation-1\" already exists")
	require.Equal(t, "old", kv.values[kv.activeKey])
}

func TestPublishActivatesExistingGenerationWithoutRewriting(t *testing.T) {
	options, kv := publisherFixture(t)
	require.NoError(t, Publish(t.Context(), kv, options))
	manifest := kv.values["/pemcast/v1/bundles/nginx/generation-1/manifest.json"]
	certificate := kv.values["/pemcast/v1/bundles/nginx/generation-1/files/fullchain.pem"]
	privateKey := kv.values["/pemcast/v1/bundles/nginx/generation-1/files/privkey.pem"]

	kv.values[kv.activeKey] = "newer"
	rollback := options
	rollback.PreviousGeneration = "newer"
	rollback.ActivateExisting = true
	rollback.CertificatePath = ""
	rollback.PrivateKeyPath = ""
	require.NoError(t, Publish(t.Context(), kv, rollback))

	require.Equal(t, "generation-1", kv.values[kv.activeKey])
	require.Equal(t, manifest, kv.values["/pemcast/v1/bundles/nginx/generation-1/manifest.json"])
	require.Equal(t, certificate, kv.values["/pemcast/v1/bundles/nginx/generation-1/files/fullchain.pem"])
	require.Equal(t, privateKey, kv.values["/pemcast/v1/bundles/nginx/generation-1/files/privkey.pem"])
}

func publisherFixture(t *testing.T) (Options, *fakeKV) {
	t.Helper()

	certificatePath, keyPath := writeKeyPair(t)
	return Options{
		RootPrefix: "/pemcast/v1", TargetID: "nginx", Generation: "generation-1",
		CertificatePath: certificatePath, PrivateKeyPath: keyPath,
		PreviousGeneration: "old", AllowMissingActive: true,
	}, &fakeKV{values: map[string]string{"/pemcast/v1/active/nginx": "old"}, activeKey: "/pemcast/v1/active/nginx"}
}

func writeKeyPair(t *testing.T) (string, string) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "publisher-test"},
		NotBefore: time.Now().Add(-time.Minute), NotAfter: time.Now().Add(time.Hour),
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)
	certificatePath := filepath.Join(t.TempDir(), "fullchain.pem")
	keyPath := filepath.Join(t.TempDir(), "privkey.pem")
	require.NoError(t, writeFileForTest(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER})))
	require.NoError(t, writeFileForTest(keyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})))
	return certificatePath, keyPath
}

func TestPublishWritesVerifiesThenMovesPointer(t *testing.T) {
	options, kv := publisherFixture(t)
	require.NoError(t, Publish(t.Context(), kv, options))
	require.Equal(t, "generation-1", kv.values[kv.activeKey])
	require.Contains(t, kv.values, "/pemcast/v1/bundles/nginx/generation-1/manifest.json")
	require.Contains(t, kv.values, "/pemcast/v1/bundles/nginx/generation-1/files/fullchain.pem")
	require.Contains(t, kv.values, "/pemcast/v1/bundles/nginx/generation-1/files/privkey.pem")
}

func writeFileForTest(path string, data []byte) error {
	return os.WriteFile(path, data, 0o600)
}

func TestPublishRejectsUnexpectedPreviousPointer(t *testing.T) {
	options, kv := publisherFixture(t)
	kv.values[kv.activeKey] = "different"
	err := Publish(t.Context(), kv, options)
	require.ErrorContains(t, err, "expected \"old\"")
	require.Equal(t, "different", kv.values[kv.activeKey])
}

func TestPublishValidatesKeyPairBeforeWriting(t *testing.T) {
	options, kv := publisherFixture(t)
	options.PrivateKeyPath = options.CertificatePath
	err := Publish(t.Context(), kv, options)
	require.ErrorContains(t, err, "parse TLS key pair")
	require.Equal(t, map[string]string{kv.activeKey: "old"}, kv.values)
}

func TestPublishRequiresExplicitMissingActiveApproval(t *testing.T) {
	options, kv := publisherFixture(t)
	delete(kv.values, kv.activeKey)
	options.PreviousGeneration = ""
	options.AllowMissingActive = false
	err := Publish(t.Context(), kv, options)
	require.ErrorContains(t, err, "--allow-missing-active")
	require.NotContains(t, kv.values, kv.activeKey)
}

func TestPublishAllowsMissingActivePointer(t *testing.T) {
	options, kv := publisherFixture(t)
	delete(kv.values, kv.activeKey)
	options.PreviousGeneration = ""
	options.AllowMissingActive = true
	require.NoError(t, Publish(t.Context(), kv, options))
	require.Equal(t, "generation-1", kv.values[kv.activeKey])
}

func TestPublishDoesNotMovePointerAfterPartialWriteFailure(t *testing.T) {
	options, kv := publisherFixture(t)
	kv.putHook = func(key, _ string) error {
		if strings.HasSuffix(key, "/privkey.pem") {
			return errors.New("write failed")
		}
		return nil
	}
	err := Publish(t.Context(), kv, options)
	require.ErrorContains(t, err, "write failed")
	require.Equal(t, "old", kv.values[kv.activeKey])
}

func TestPublishDoesNotMovePointerAfterHashMismatch(t *testing.T) {
	options, kv := publisherFixture(t)
	kv.putHook = func(key, value string) error {
		if strings.HasSuffix(key, "/fullchain.pem") {
			value += "corruption"
			kv.values[key] = value
			return errSkipWrite
		}
		return nil
	}
	err := Publish(t.Context(), kv, options)
	require.ErrorContains(t, err, "sha256 mismatch")
	require.Equal(t, "old", kv.values[kv.activeKey])
}

func TestPublishDoesNotMovePointerWhenActiveChangesDuringWrite(t *testing.T) {
	options, kv := publisherFixture(t)
	kv.putHook = func(key, _ string) error {
		if key == kv.activeKey {
			return nil
		}
		kv.values[kv.activeKey] = "changed"
		return nil
	}
	err := Publish(t.Context(), kv, options)
	require.ErrorContains(t, err, "active pointer changed during publication")
	require.Equal(t, "changed", kv.values[kv.activeKey])
}
