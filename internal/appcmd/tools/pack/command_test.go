package pack

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	appconfig "github.com/lwmacct/260907-pemcast/internal/config"
	pemcastpack "github.com/lwmacct/260907-pemcast/internal/pack"
)

func TestPackCommandCreatesPrivateArtifacts(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t, "cli-pack")
	outputDir := filepath.Join(t.TempDir(), "pack")

	err := runCommand(t, []string{
		"pemcast", "tools", "pack",
		"--target", "nginx",
		"--certificate", certificatePath,
		"--private-key", privateKeyPath,
		"--etcd-prefix", "/pemcast",
		"--output-dir", outputDir,
	})
	require.NoError(t, err)

	for _, name := range []string{"bundle.json", "metadata.json", "stage.txn"} {
		info, err := os.Stat(filepath.Join(outputDir, name))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	metadata := readFile(t, filepath.Join(outputDir, "metadata.json"))
	require.NotContains(t, string(metadata), `"data"`)
	require.FileExists(t, filepath.Join(outputDir, "bundle.json"))
}

func TestPackCommandBuildsTrustBundle(t *testing.T) {
	caPath := writeCACertificate(t)
	outputDir := filepath.Join(t.TempDir(), "trust-pack")

	err := runCommand(t, []string{
		"pemcast", "tools", "pack",
		"--type", "trust",
		"--target", "internal-ca",
		"--ca", caPath,
		"--etcd-prefix", "/pemcast",
		"--output-dir", outputDir,
	})
	require.NoError(t, err)

	result, err := pemcastpack.Read(outputDir)
	require.NoError(t, err)
	require.Equal(t, bundle.TypeTrust, result.Metadata.Type)
	manifest, files, digest, err := bundle.Decode(result.Bundle)
	require.NoError(t, err)
	require.Equal(t, digest, result.Metadata.BundleSHA256)
	require.Equal(t, bundle.TypeTrust, manifest.Type)
	require.Len(t, files, 1)
}

func TestPackCommandRejectsMismatchedKeyPairAndExistingOutput(t *testing.T) {
	certificatePath, _ := writeKeyPair(t, "cli-pack-a")
	_, otherPrivateKeyPath := writeKeyPair(t, "cli-pack-b")
	outputDir := filepath.Join(t.TempDir(), "pack")

	err := runCommand(t, []string{
		"pemcast", "tools", "pack",
		"--target", "nginx",
		"--certificate", certificatePath,
		"--private-key", otherPrivateKeyPath,
		"--output-dir", outputDir,
	})
	require.ErrorContains(t, err, "parse TLS key pair")
	require.NoDirExists(t, outputDir)

	validCertificatePath, validPrivateKeyPath := writeKeyPair(t, "cli-pack-valid")
	require.NoError(t, os.Mkdir(outputDir, 0o700))
	err = runCommand(t, []string{
		"pemcast", "tools", "pack",
		"--target", "nginx",
		"--certificate", validCertificatePath,
		"--private-key", validPrivateKeyPath,
		"--output-dir", outputDir,
	})
	require.ErrorContains(t, err, "already exists")
}

var (
	application = &cli.Command{
		Name:     "pemcast",
		Commands: []*cli.Command{{Name: "tools", Commands: []*cli.Command{Command}}},
	}
	configureOnce sync.Once
)

func runCommand(t *testing.T, arguments []string) error {
	t.Helper()

	configureOnce.Do(func() { appconfig.Manager.MustConfigure(application) })
	return application.Run(t.Context(), arguments)
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
	require.NoError(t, os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}), 0o600))
	require.NoError(t, os.WriteFile(privateKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certificatePath, privateKeyPath
}

func writeCACertificate(t *testing.T) string {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(time.Now().UnixNano()),
		Subject:               pkix.Name{CommonName: "cli-pack-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	require.NoError(t, err)
	path := filepath.Join(t.TempDir(), "ca-bundle.pem")
	require.NoError(t, os.WriteFile(path, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o600))
	return path
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
