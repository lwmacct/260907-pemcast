package publish

import (
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
	"github.com/urfave/cli/v3"

	appconfig "github.com/lwmacct/260907-pemcast/internal/config"
	pemcastpack "github.com/lwmacct/260907-pemcast/internal/pack"
)

func TestPublishCommandRejectsMissingPack(t *testing.T) {
	err := runCommand(t, filepath.Join(t.TempDir(), "missing-pack"), "")
	require.ErrorContains(t, err, "read pack metadata")
}

func TestPublishCommandRejectsPrefixMismatch(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t, "cli-publish")
	packDir := filepath.Join(t.TempDir(), "pack")
	result, err := pemcastpack.Build(pemcastpack.Options{
		Type:            "tls-server",
		TargetID:        "nginx",
		EtcdPrefix:      "/tenants/other",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	require.NoError(t, err)
	require.NoError(t, pemcastpack.Write(packDir, result))

	err = runCommand(t, packDir, "/pemcast")
	require.ErrorContains(t, err, "does not match configured prefix")
}

func runCommand(t *testing.T, packDir, etcdPrefix string) error {
	t.Helper()

	configurationPath := filepath.Join(t.TempDir(), "config.yaml")
	configuration := `agent:
  state-dir: /tmp/pemcast-publish-cli
  etcd:
    endpoints: ["http://127.0.0.1:1"]
    prefix: /pemcast
`
	require.NoError(t, os.WriteFile(configurationPath, []byte(configuration), 0o600))

	application := &cli.Command{
		Name:     "pemcast",
		Commands: []*cli.Command{Command},
	}
	appconfig.Manager.MustConfigure(application)
	arguments := []string{"pemcast", "--config", configurationPath, "publish", "--pack-dir", packDir}
	if etcdPrefix != "" {
		arguments = append(arguments, "--etcd-prefix", etcdPrefix)
	}
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
