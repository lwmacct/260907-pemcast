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

func TestActivateRejectsCorruptActiveRelease(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(t *testing.T, root string)
		message string
	}{
		{
			name: "digest marker",
			mutate: func(t *testing.T, root string) {
				require.NoError(t, os.WriteFile(filepath.Join(root, "current", ".pemcast-digest"), []byte("wrong\n"), 0o600))
			},
			message: "digest",
		},
		{
			name: "missing marker",
			mutate: func(t *testing.T, root string) {
				require.NoError(t, os.Remove(filepath.Join(root, "current", ".pemcast-digest")))
			},
			message: "read",
		},
		{
			name: "file content",
			mutate: func(t *testing.T, root string) {
				require.NoError(t, os.WriteFile(filepath.Join(root, "current", "cert"), []byte("changed"), 0o600))
			},
			message: "content mismatch",
		},
		{
			name: "file mode",
			mutate: func(t *testing.T, root string) {
				require.NoError(t, os.Chmod(filepath.Join(root, "current", "cert"), 0o644))
			},
			message: "mode mismatch",
		},
		{
			name: "extra file",
			mutate: func(t *testing.T, root string) {
				require.NoError(t, os.WriteFile(filepath.Join(root, "current", "extra"), nil, 0o600))
			},
			message: "unexpected entry",
		},
		{
			name: "extra directory",
			mutate: func(t *testing.T, root string) {
				require.NoError(t, os.Mkdir(filepath.Join(root, "current", "extra"), 0o700))
			},
			message: "unexpected directory",
		},
		{
			name: "symlink inside release",
			mutate: func(t *testing.T, root string) {
				path := filepath.Join(root, "current", "cert")
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink("../../../secret", path))
			},
			message: "not a regular file",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			output := config.Output{
				Root: root, CurrentLink: "current", RetainReleases: 1, DirectoryMode: config.FileMode("0700"),
				Mappings: []config.FileMapping{{Remote: "cert", Local: "cert", Mode: config.FileMode("0600")}},
			}
			material := &bundle.Material{Digest: "digest", Files: map[string][]byte{"cert": []byte("certificate")}}
			deployer := New()
			_, err := deployer.Activate(material, output)
			require.NoError(t, err)

			test.mutate(t, root)
			_, err = deployer.Activate(material, output)
			require.ErrorContains(t, err, test.message)
		})
	}
}

func TestActivateRejectsUnsafeCurrentPath(t *testing.T) {
	tests := []struct {
		name   string
		create func(t *testing.T, root string)
	}{
		{
			name: "regular file",
			create: func(t *testing.T, root string) {
				require.NoError(t, os.WriteFile(filepath.Join(root, "current"), nil, 0o600))
			},
		},
		{
			name: "absolute symlink",
			create: func(t *testing.T, root string) {
				require.NoError(t, os.Symlink("/tmp/escape", filepath.Join(root, "current")))
			},
		},
		{
			name: "parent escape",
			create: func(t *testing.T, root string) {
				require.NoError(t, os.Symlink("../escape", filepath.Join(root, "current")))
			},
		},
		{
			name: "non-content-addressed release",
			create: func(t *testing.T, root string) {
				require.NoError(t, os.Symlink(".pemcast/releases/latest", filepath.Join(root, "current")))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			require.NoError(t, os.MkdirAll(root, 0o700))
			test.create(t, root)
			output := config.Output{
				Root: root, CurrentLink: "current", DirectoryMode: config.FileMode("0700"),
				Mappings: []config.FileMapping{{Remote: "cert", Local: "cert", Mode: config.FileMode("0600")}},
			}
			_, err := New().Activate(&bundle.Material{
				Digest: "digest", Files: map[string][]byte{"cert": []byte("certificate")},
			}, output)
			require.Error(t, err)
		})
	}
}

func TestActivateDoesNotPruneActiveRelease(t *testing.T) {
	root := t.TempDir()
	output := config.Output{
		Root: root, CurrentLink: "current", RetainReleases: 0, DirectoryMode: config.FileMode("0700"),
		Mappings: []config.FileMapping{{Remote: "cert", Local: "cert", Mode: config.FileMode("0600")}},
	}
	deployer := New()
	for _, digest := range []string{"first", "second"} {
		_, err := deployer.Activate(&bundle.Material{Digest: digest, Files: map[string][]byte{"cert": []byte(digest)}}, output)
		require.NoError(t, err)
	}
	require.DirExists(t, filepath.Join(root, ".pemcast", "releases", "sha256-second"))
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
