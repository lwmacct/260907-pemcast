package seed

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"io"
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
	"github.com/lwmacct/260907-pemcast/internal/deploy"
	"github.com/lwmacct/260907-pemcast/internal/pack"
)

func TestSeedCommandMaterializesPackWithoutState(t *testing.T) {
	root := t.TempDir()
	packDir := writePack(t, root, "nginx", "nginx", 2*time.Hour)

	err := runCommand(t, root, packDir, false)
	require.NoError(t, err)
	require.FileExists(t, filepath.Join(root, "current", "fullchain.pem"))
	require.FileExists(t, filepath.Join(root, "current", "privkey.pem"))
	require.NoFileExists(t, filepath.Join(root, "state", "nginx.json"))
}

func TestSeedCommandRequiresForceAndLock(t *testing.T) {
	root := t.TempDir()
	first := writePack(t, root, "nginx", "nginx-first", 2*time.Hour)
	second := writePack(t, root, "nginx", "nginx-second", 3*time.Hour)

	require.NoError(t, runCommand(t, root, first, false))
	err := runCommand(t, root, second, false)
	require.ErrorContains(t, err, "retry with --force")

	configurationPath := writeConfiguration(t, root)
	target := configuredTarget(root)
	locks, err := deploy.LockRoots([]appconfig.Target{target})
	require.NoError(t, err)
	require.Len(t, locks, 1)
	err = runCommandWithConfiguration(t, configurationPath, second, true)
	require.ErrorContains(t, err, "already managed by another pemcast process")
	require.NoError(t, locks[0].Close())

	require.NoError(t, runCommand(t, root, second, true))
	require.DirExists(t, filepath.Join(root, ".pemcast", "releases", "sha256-"+packDigest(t, first)))
}

func TestSeedCommandRejectsPackTargetMismatch(t *testing.T) {
	root := t.TempDir()
	packDir := writePack(t, root, "other", "other", 2*time.Hour)

	err := runCommand(t, root, packDir, false)
	require.ErrorContains(t, err, `pack target "other" does not match requested target "nginx"`)
	require.NoDirExists(t, filepath.Join(root, ".pemcast"))
}

func runCommand(t *testing.T, root, packDir string, force bool) error {
	t.Helper()

	configurationPath := writeConfiguration(t, root)
	return runCommandWithConfiguration(t, configurationPath, packDir, force)
}

var (
	application = &cli.Command{
		Name: "pemcast", Writer: io.Discard,
		Commands: []*cli.Command{{Name: "tools", Commands: []*cli.Command{Command}}},
	}
	configureOnce sync.Once
)

func runCommandWithConfiguration(t *testing.T, configurationPath, packDir string, force bool) error {
	t.Helper()

	configureOnce.Do(func() { appconfig.Manager.MustConfigure(application) })
	arguments := []string{
		"pemcast", "--config", configurationPath,
		"tools", "seed", "--target", "nginx", "--pack-dir", packDir,
	}
	if force {
		arguments = append(arguments, "--force")
	}
	return application.Run(t.Context(), arguments)
}

func writeConfiguration(t *testing.T, root string) string {
	t.Helper()

	configurationPath := filepath.Join(t.TempDir(), "config.yaml")
	configuration := fmt.Sprintf(`agent:
  state-dir: %s/state
  etcd:
    endpoints: ["http://127.0.0.1:1"]
    prefix: /pemcast
  targets:
    - id: nginx
      type: tls-server
      delete-policy: retain
      output:
        root: %s
        current-link: current
        retain-releases: 3
        directory-mode: "0700"
        mappings:
          - remote: fullchain.pem
            local: fullchain.pem
            mode: "0644"
          - remote: privkey.pem
            local: privkey.pem
            mode: "0600"
      validation:
        reject-expired: true
        minimum-validity: 1h
`, filepath.Join(root, "state"), root)
	require.NoError(t, os.WriteFile(configurationPath, []byte(configuration), 0o600))
	return configurationPath
}

func configuredTarget(root string) appconfig.Target {
	return appconfig.Target{
		ID: "nginx", Type: bundle.TypeTLSServer, DeletePolicy: "retain",
		Output: appconfig.Output{
			Root: root, CurrentLink: "current", DirectoryMode: appconfig.FileMode("0700"),
			Mappings: []appconfig.FileMapping{
				{Remote: "fullchain.pem", Local: "fullchain.pem", Mode: appconfig.FileMode("0644")},
				{Remote: "privkey.pem", Local: "privkey.pem", Mode: appconfig.FileMode("0600")},
			},
		},
		Validation: appconfig.Validation{
			RejectExpired: true,
		},
	}
}

func writePack(t *testing.T, root, targetID, commonName string, lifetime time.Duration) string {
	t.Helper()

	certificatePath, privateKeyPath := writeKeyPair(t, commonName, lifetime)
	result, err := pack.Build(pack.Options{
		Type:     "tls-server",
		TargetID: targetID, EtcdPrefix: "/pemcast",
		CertificatePath: certificatePath, PrivateKeyPath: privateKeyPath,
	})
	require.NoError(t, err)
	packDir := filepath.Join(t.TempDir(), "pack")
	require.NoError(t, pack.Write(packDir, result))
	return packDir
}

func packDigest(t *testing.T, packDir string) string {
	t.Helper()

	result, err := pack.Read(packDir)
	require.NoError(t, err)
	return result.Metadata.BundleSHA256
}

func writeKeyPair(t *testing.T, commonName string, lifetime time.Duration) (string, string) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: commonName},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(lifetime),
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
