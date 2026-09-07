package deploy

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/config"
)

func TestActivateSwitchesCompleteReleaseAndSkipsSameDigest(t *testing.T) {
	root := t.TempDir()
	output := config.Output{
		Root: root, CurrentLink: "current", RetainReleases: 1, DirectoryMode: config.FileMode("0700"),
		Mappings: []config.FileMapping{
			{Remote: "fullchain.pem", Local: "fullchain.pem", Mode: config.FileMode("0644")},
			{Remote: "privkey.pem", Local: "nested/privkey.pem", Mode: config.FileMode("0600")},
		},
	}
	material := &bundle.Material{Digest: "abc123", Files: map[string][]byte{
		"fullchain.pem": []byte("certificate"), "privkey.pem": []byte("private-key"),
	}}
	deployer := New()
	result, err := deployer.Activate(material, output)
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, []byte("certificate"), mustRead(t, filepath.Join(root, "current", "fullchain.pem")))
	require.Equal(t, []byte("private-key"), mustRead(t, filepath.Join(root, "current", "nested", "privkey.pem")))
	info, err := os.Stat(filepath.Join(root, "current", "nested", "privkey.pem"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	result, err = deployer.Activate(material, output)
	require.NoError(t, err)
	require.False(t, result.Changed)
}

func TestActivatePrunesInactiveReleases(t *testing.T) {
	root := t.TempDir()
	output := config.Output{
		Root: root, CurrentLink: "current", RetainReleases: 0, DirectoryMode: config.FileMode("0700"),
		Mappings: []config.FileMapping{{Remote: "cert", Local: "cert", Mode: config.FileMode("0600")}},
	}
	deployer := New()
	_, err := deployer.Activate(&bundle.Material{Digest: "first", Files: map[string][]byte{"cert": []byte("one")}}, output)
	require.NoError(t, err)
	_, err = deployer.Activate(&bundle.Material{Digest: "second", Files: map[string][]byte{"cert": []byte("two")}}, output)
	require.NoError(t, err)
	entries, err := os.ReadDir(filepath.Join(root, managedDirectory, "releases"))
	require.NoError(t, err)
	require.Len(t, entries, 1)
	require.Equal(t, "sha256-second", entries[0].Name())
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
