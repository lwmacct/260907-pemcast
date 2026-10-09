package pack

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
)

func TestBuildProducesDeterministicV5Artifacts(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t)
	options := Options{
		TargetID:        "nginx",
		EtcdPrefix:      "/tenants/example/",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	}

	first, err := Build(options)
	require.NoError(t, err)
	second, err := Build(options)
	require.NoError(t, err)
	require.Equal(t, first, second)

	manifest, files, digest, err := bundle.Decode(first.Bundle)
	require.NoError(t, err)
	require.Equal(t, bundle.SchemaV5, manifest.Schema)
	require.Equal(t, digest, first.Metadata.BundleSHA256)
	require.Equal(t, bundle.Generation(digest), first.Metadata.Generation)
	require.Equal(t, hashBytes(first.Bundle), first.Metadata.BundleValueSHA256)
	require.Contains(t, files, "fullchain.pem")
	require.Contains(t, files, "privkey.pem")
	require.Equal(t, "/tenants/example", first.Metadata.EtcdPrefix)
	require.Equal(t, "/tenants/example/v5/active/nginx", first.Metadata.ActiveKey)
	require.Equal(
		t,
		"/tenants/example/v5/bundles/nginx/"+first.Metadata.Generation,
		first.Metadata.BundleKey,
	)

	condition := `create("/tenants/example/v5/bundles/nginx/` + first.Metadata.Generation + `") = "0"`
	require.True(t, strings.HasPrefix(string(first.StageTxn), condition))
	putLine := `put "/tenants/example/v5/bundles/nginx/` + first.Metadata.Generation + `" "`
	require.True(t, strings.Contains(string(first.StageTxn), putLine))
	require.True(t, strings.HasSuffix(string(first.StageTxn), "\"\n\n\n"))
}

func TestBuildRejectsMismatchedTLSKeyPair(t *testing.T) {
	certificatePath, _ := writeKeyPair(t)
	_, otherPrivateKeyPath := writeKeyPair(t)

	_, err := Build(Options{
		TargetID:        "nginx",
		EtcdPrefix:      "/pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  otherPrivateKeyPath,
	})
	require.ErrorContains(t, err, "parse TLS key pair")
}

func TestBuildRejectsUnsafeTargetAndPrefix(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t)

	_, err := Build(Options{
		TargetID:        "../nginx",
		EtcdPrefix:      "/pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	require.ErrorContains(t, err, "unsafe")

	_, err = Build(Options{
		TargetID:        "nginx",
		EtcdPrefix:      "pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	require.ErrorContains(t, err, "must be absolute")
}

func TestWriteCreatesPrivateArtifactsAndRefusesReplacement(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t)
	result, err := Build(Options{
		TargetID:        "nginx",
		EtcdPrefix:      "/pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	require.NoError(t, err)

	outputDir := filepath.Join(t.TempDir(), "pack")
	require.NoError(t, Write(outputDir, result))
	for _, name := range []string{"bundle.json", "metadata.json", "stage.txn"} {
		info, err := os.Stat(filepath.Join(outputDir, name))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	require.Equal(t, result.Bundle, mustRead(t, filepath.Join(outputDir, "bundle.json")))
	require.Equal(t, result.StageTxn, mustRead(t, filepath.Join(outputDir, "stage.txn")))

	err = Write(outputDir, result)
	require.ErrorContains(t, err, "already exists")
}

func TestWriteRejectsUnsafeOutputDirectory(t *testing.T) {
	err := Write("relative-output", Result{})
	require.ErrorContains(t, err, "must be a clean absolute non-root path")

	err = Write(string(filepath.Separator), Result{})
	require.ErrorContains(t, err, "must be a clean absolute non-root path")
}

func writeKeyPair(t *testing.T) (string, string) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pack-test"},
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

func mustRead(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
