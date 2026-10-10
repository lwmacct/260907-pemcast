package seed

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

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/deploy"
	"github.com/lwmacct/260907-pemcast/internal/pack"
)

func TestApplySeedsAndReusesSameGeneration(t *testing.T) {
	root := t.TempDir()
	target := testTarget(root, 0)
	result := buildPack(t, target.ID, time.Hour)

	seeded, err := Apply(Options{Target: target, Pack: result, Prefix: "/pemcast", Now: time.Now()}, deploy.New())
	require.NoError(t, err)
	require.True(t, seeded.Changed)
	require.Equal(t, result.Metadata.Generation, seeded.Generation)
	require.FileExists(t, filepath.Join(root, "current", "fullchain.pem"))
	require.NoFileExists(t, filepath.Join(root, "state", target.ID+".json"))

	again, err := Apply(Options{Target: target, Pack: result, Prefix: "/pemcast", Now: time.Now()}, deploy.New())
	require.NoError(t, err)
	require.False(t, again.Changed)
	require.Equal(t, seeded.Generation, again.Generation)
}

func TestApplyRequiresForceForDifferentGeneration(t *testing.T) {
	root := t.TempDir()
	target := testTarget(root, 0)
	first := buildPack(t, target.ID, time.Hour)
	second := buildPack(t, target.ID, 2*time.Hour)

	_, err := Apply(Options{Target: target, Pack: first, Prefix: "/pemcast", Now: time.Now()}, deploy.New())
	require.NoError(t, err)

	_, err = Apply(Options{Target: target, Pack: second, Prefix: "/pemcast", Now: time.Now()}, deploy.New())
	require.ErrorContains(t, err, "retry with --force")

	forced, err := Apply(
		Options{Target: target, Pack: second, Prefix: "/pemcast", Force: true, Now: time.Now()},
		deploy.New(),
	)
	require.NoError(t, err)
	require.True(t, forced.Changed)
	require.Equal(t, second.Metadata.Generation, forced.Generation)
	require.DirExists(t, filepath.Join(root, ".pemcast", "releases", "sha256-"+first.Metadata.BundleSHA256))
}

func TestApplyRejectsTargetPrefixAndValidityMismatch(t *testing.T) {
	root := t.TempDir()
	target := testTarget(root, time.Hour)
	wrongTarget := buildPack(t, "other", time.Hour)
	_, err := Apply(Options{Target: target, Pack: wrongTarget, Prefix: "/pemcast", Now: time.Now()}, deploy.New())
	require.ErrorContains(t, err, "does not match requested target")

	wrongPrefix := buildPack(t, target.ID, time.Hour)
	_, err = Apply(Options{Target: target, Pack: wrongPrefix, Prefix: "/other", Now: time.Now()}, deploy.New())
	require.ErrorContains(t, err, "does not match configured prefix")

	shortValidity := buildPack(t, target.ID, 30*time.Minute)
	_, err = Apply(Options{Target: target, Pack: shortValidity, Prefix: "/pemcast", Now: time.Now()}, deploy.New())
	require.ErrorContains(t, err, "validity")
	require.NoDirExists(t, filepath.Join(root, ".pemcast"))
}

func testTarget(root string, minimumValidity time.Duration) config.Target {
	return config.Target{
		ID: "nginx", DeletePolicy: "retain",
		Output: config.Output{
			Root: root, CurrentLink: "current", RetainReleases: 3, DirectoryMode: config.FileMode("0700"),
			Mappings: []config.FileMapping{
				{Remote: "fullchain.pem", Local: "fullchain.pem", Mode: config.FileMode("0644")},
				{Remote: "privkey.pem", Local: "privkey.pem", Mode: config.FileMode("0600")},
			},
		},
		Validation: config.Validation{
			Certificate: "fullchain.pem", PrivateKey: "privkey.pem",
			RejectExpired: true, MinimumValidity: minimumValidity,
		},
	}
}

func buildPack(t *testing.T, targetID string, lifetime time.Duration) pack.Result {
	t.Helper()

	certificatePath, privateKeyPath := writeKeyPair(t, targetID, lifetime)
	result, err := pack.Build(pack.Options{
		TargetID: targetID, EtcdPrefix: "/pemcast",
		CertificatePath: certificatePath, PrivateKeyPath: privateKeyPath,
	})
	require.NoError(t, err)
	return result
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
