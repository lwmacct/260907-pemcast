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

	appconfig "github.com/lwmacct/260907-pemcast/internal/config"
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

func readFile(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
