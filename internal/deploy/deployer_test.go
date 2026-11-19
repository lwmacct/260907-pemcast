package deploy

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/config"
)

func identityOutput(root string) config.Output {
	return config.Output{Root: root, Files: map[string]config.OutputFile{
		"certificate-chain": {Name: "fullchain.pem", Mode: config.FileMode("0644")},
		"private-key":       {Name: "privkey.pem", Mode: config.FileMode("0600")},
	}}
}

func TestActivateSwitchesCompleteReleaseAndSkipsSameDigest(t *testing.T) {
	root := t.TempDir()
	output := identityOutput(root)
	material := &bundle.Material{Digest: "abc123", Files: map[string][]byte{
		"fullchain.pem": []byte("certificate"), "privkey.pem": []byte("private-key"),
	}}
	deployer := New()
	result, err := deployer.Activate(material, output)
	require.NoError(t, err)
	require.True(t, result.Changed)
	require.Equal(t, []byte("certificate"), mustRead(t, filepath.Join(root, "current", "fullchain.pem")))
	require.Equal(t, []byte("private-key"), mustRead(t, filepath.Join(root, "current", "privkey.pem")))
	info, err := os.Stat(filepath.Join(root, "current", "privkey.pem"))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), info.Mode().Perm())

	result, err = deployer.Activate(material, output)
	require.NoError(t, err)
	require.False(t, result.Changed)
}

func TestActivatePrunesInactiveReleases(t *testing.T) {
	root := t.TempDir()
	output := identityOutput(root)
	deployer := New()
	materials := map[string]*bundle.Material{
		"first":  {Digest: "first", Files: map[string][]byte{"fullchain.pem": []byte("one"), "privkey.pem": []byte("one-key")}},
		"second": {Digest: "second", Files: map[string][]byte{"fullchain.pem": []byte("two"), "privkey.pem": []byte("two-key")}},
		"third":  {Digest: "third", Files: map[string][]byte{"fullchain.pem": []byte("three"), "privkey.pem": []byte("three-key")}},
		"fourth": {Digest: "fourth", Files: map[string][]byte{"fullchain.pem": []byte("four"), "privkey.pem": []byte("four-key")}},
		"fifth":  {Digest: "fifth", Files: map[string][]byte{"fullchain.pem": []byte("five"), "privkey.pem": []byte("five-key")}},
	}
	for _, digest := range []string{"first", "second", "third", "fourth"} {
		_, err := deployer.Activate(materials[digest], output)
		require.NoError(t, err)
	}
	for index, digest := range []string{"first", "second", "third", "fourth"} {
		modTime := time.Now().Add(time.Duration(index) * time.Minute)
		path := filepath.Join(root, managedDirectory, "releases", "sha256-"+digest)
		require.NoError(t, os.Chtimes(path, modTime, modTime))
	}
	_, err := deployer.Activate(materials["fifth"], output)
	require.NoError(t, err)
	entries, err := os.ReadDir(filepath.Join(root, managedDirectory, "releases"))
	require.NoError(t, err)
	require.Len(t, entries, config.DefaultRetainReleases+1)
	require.DirExists(t, filepath.Join(root, managedDirectory, "releases", "sha256-fifth"))
	require.NoDirExists(t, filepath.Join(root, managedDirectory, "releases", "sha256-first"))
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
				require.NoError(t, os.WriteFile(filepath.Join(root, "current", "fullchain.pem"), []byte("changed"), 0o600))
			},
			message: "content mismatch",
		},
		{
			name: "file mode",
			mutate: func(t *testing.T, root string) {
				require.NoError(t, os.Chmod(filepath.Join(root, "current", "fullchain.pem"), 0o600))
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
				path := filepath.Join(root, "current", "fullchain.pem")
				require.NoError(t, os.Remove(path))
				require.NoError(t, os.Symlink("../../../secret", path))
			},
			message: "not a regular file",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			output := identityOutput(root)
			material := &bundle.Material{Digest: "digest", Files: map[string][]byte{
				"fullchain.pem": []byte("certificate"), "privkey.pem": []byte("private-key"),
			}}
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
			_, err := New().Activate(&bundle.Material{
				Digest: "digest", Files: map[string][]byte{
					"fullchain.pem": []byte("certificate"), "privkey.pem": []byte("private-key"),
				},
			}, identityOutput(root))
			require.Error(t, err)
		})
	}
}

func TestActivateDoesNotPruneActiveRelease(t *testing.T) {
	root := t.TempDir()
	output := identityOutput(root)
	deployer := New()
	for _, digest := range []string{"first", "second"} {
		_, err := deployer.Activate(&bundle.Material{
			Digest: digest,
			Files: map[string][]byte{
				"fullchain.pem": []byte(digest + "-cert"), "privkey.pem": []byte(digest + "-key"),
			},
		}, output)
		require.NoError(t, err)
	}
	require.DirExists(t, filepath.Join(root, ".pemcast", "releases", "sha256-second"))
}

func TestActivateCreatesStableLinks(t *testing.T) {
	root := t.TempDir()
	output := identityOutput(root)
	certificate := output.Files["certificate-chain"]
	certificate.Name = "tls.crt"
	output.Files["certificate-chain"] = certificate
	privateKey := output.Files["private-key"]
	privateKey.Name = "tls.key"
	output.Files["private-key"] = privateKey
	deployer := New()
	_, err := deployer.Activate(&bundle.Material{
		Digest: "digest", Files: map[string][]byte{
			"fullchain.pem": []byte("cert"), "privkey.pem": []byte("key"),
		},
	}, output)
	require.NoError(t, err)

	link, err := os.Readlink(filepath.Join(root, "tls.crt"))
	require.NoError(t, err)
	require.Equal(t, "current/fullchain.pem", link)
	require.Equal(t, []byte("cert"), mustRead(t, filepath.Join(root, "tls.crt")))

	link, err = os.Readlink(filepath.Join(root, "tls.key"))
	require.NoError(t, err)
	require.Equal(t, "current/privkey.pem", link)
	require.Equal(t, []byte("key"), mustRead(t, filepath.Join(root, "tls.key")))

	_, err = os.Lstat(filepath.Join(root, "fullchain.pem"))
	require.ErrorIs(t, err, os.ErrNotExist, "canonical bundle names are not public aliases")
}

func TestActivateStableLinksIdempotent(t *testing.T) {
	root := t.TempDir()
	output := identityOutput(root)
	deployer := New()
	material := &bundle.Material{Digest: "one", Files: map[string][]byte{
		"fullchain.pem": []byte("first"), "privkey.pem": []byte("first-key"),
	}}
	_, err := deployer.Activate(material, output)
	require.NoError(t, err)

	info1, err := os.Lstat(filepath.Join(root, "fullchain.pem"))
	require.NoError(t, err)

	_, err = deployer.Activate(material, output)
	require.NoError(t, err)

	info2, err := os.Lstat(filepath.Join(root, "fullchain.pem"))
	require.NoError(t, err)
	require.Equal(t, info1.ModTime(), info2.ModTime(), "unchanged digest should not replace existing correct link")
}

func TestActivateStableLinksRepairMissing(t *testing.T) {
	root := t.TempDir()
	output := identityOutput(root)
	deployer := New()
	material := &bundle.Material{Digest: "one", Files: map[string][]byte{
		"fullchain.pem": []byte("data"), "privkey.pem": []byte("data-key"),
	}}
	_, err := deployer.Activate(material, output)
	require.NoError(t, err)
	require.NoError(t, os.Remove(filepath.Join(root, "fullchain.pem")))

	_, err = deployer.Activate(material, output)
	require.NoError(t, err)

	link, err := os.Readlink(filepath.Join(root, "fullchain.pem"))
	require.NoError(t, err)
	require.Equal(t, "current/fullchain.pem", link)
}

func TestActivateChangedGenerationReplacesStableLinks(t *testing.T) {
	root := t.TempDir()
	output := identityOutput(root)
	deployer := New()
	first := &bundle.Material{Digest: "one", Files: map[string][]byte{
		"fullchain.pem": []byte("one"), "privkey.pem": []byte("one-key"),
	}}
	second := &bundle.Material{Digest: "two", Files: map[string][]byte{
		"fullchain.pem": []byte("two"), "privkey.pem": []byte("two-key"),
	}}
	_, err := deployer.Activate(first, output)
	require.NoError(t, err)
	before, err := os.Lstat(filepath.Join(root, "fullchain.pem"))
	require.NoError(t, err)

	_, err = deployer.Activate(second, output)
	require.NoError(t, err)
	after, err := os.Lstat(filepath.Join(root, "fullchain.pem"))
	require.NoError(t, err)
	require.NotEqual(t, before.ModTime(), after.ModTime(), "changed generation must replace the public alias")
	require.Equal(t, []byte("two"), mustRead(t, filepath.Join(root, "fullchain.pem")))
}

func TestActivateRemovesStaleManagedPublicAlias(t *testing.T) {
	root := t.TempDir()
	output := identityOutput(root)
	deployer := New()
	material := &bundle.Material{Digest: "one", Files: map[string][]byte{
		"fullchain.pem": []byte("certificate"), "privkey.pem": []byte("private-key"),
	}}
	_, err := deployer.Activate(material, output)
	require.NoError(t, err)

	certificate := output.Files["certificate-chain"]
	certificate.Name = "tls.crt"
	output.Files["certificate-chain"] = certificate
	_, err = deployer.Activate(material, output)
	require.NoError(t, err)
	require.NoFileExists(t, filepath.Join(root, "fullchain.pem"))
	require.FileExists(t, filepath.Join(root, "tls.crt"))
}

func TestActivateRejectsUnmanagedOutputRootEntry(t *testing.T) {
	root := t.TempDir()
	output := identityOutput(root)
	require.NoError(t, os.MkdirAll(root, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(root, "business.txt"), nil, 0o600))

	_, err := New().Activate(&bundle.Material{Digest: "one", Files: map[string][]byte{
		"fullchain.pem": []byte("certificate"), "privkey.pem": []byte("private-key"),
	}}, output)
	require.ErrorContains(t, err, "unexpected unmanaged entry")
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}
