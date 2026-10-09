package deploy

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/config"
)

func testLockTarget(root string) config.Target {
	return config.Target{
		Output: config.Output{
			Root:          root,
			DirectoryMode: config.FileMode("0700"),
		},
	}
}

func TestLockRootsExcludesCompetingProcess(t *testing.T) {
	root := t.TempDir()

	locks, err := LockRoots([]config.Target{testLockTarget(root)})
	require.NoError(t, err)
	require.Len(t, locks, 1)
	require.FileExists(t, filepath.Join(root, ".pemcast", "agent.lock"))

	process := exec.Command(os.Args[0], "-test.run=^TestLockRootsHelperProcess$")
	process.Env = append(os.Environ(), "PEMCAST_LOCK_TEST_ROOT="+root)
	require.NoError(t, process.Run())

	require.NoError(t, locks[0].Close())
	locksAgain, err := LockRoots([]config.Target{testLockTarget(root)})
	require.NoError(t, err)
	require.NoError(t, locksAgain[0].Close())
}

func TestLockRootsHelperProcess(t *testing.T) {
	root := os.Getenv("PEMCAST_LOCK_TEST_ROOT")
	if root == "" {
		return
	}
	if _, err := LockRoots([]config.Target{testLockTarget(root)}); err == nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestLockRootsSecondCallerFails(t *testing.T) {
	root := t.TempDir()
	locks, err := LockRoots([]config.Target{testLockTarget(root)})
	require.NoError(t, err)
	defer func() { require.NoError(t, locks[0].Close()) }()

	_, err = LockRoots([]config.Target{testLockTarget(root)})
	require.ErrorContains(t, err, "already managed")
}

func TestLockRootsCleansUpPartialMultiRootAcquisition(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	third := t.TempDir()
	secondLock, err := lockRoot(second, 0o700)
	require.NoError(t, err)
	defer func() { require.NoError(t, secondLock.Close()) }()
	thirdLock, err := lockRoot(third, 0o700)
	require.NoError(t, err)
	defer func() { require.NoError(t, thirdLock.Close()) }()

	_, err = LockRoots([]config.Target{testLockTarget(first), testLockTarget(second), testLockTarget(third)})
	require.ErrorContains(t, err, second)

	firstLock, err := lockRoot(first, 0o700)
	require.NoError(t, err, "partial acquisition was not released")
	require.NoError(t, firstLock.Close())
}

func TestLockRootCreatesAndSecuresConfiguredDirectories(t *testing.T) {
	parent := t.TempDir()
	root := filepath.Join(parent, "output")
	require.NoError(t, os.MkdirAll(root, 0o755))
	require.NoError(t, os.Chmod(root, 0o755))

	lock, err := lockRoot(root, 0o700)
	require.NoError(t, err)
	defer func() { require.NoError(t, lock.Close()) }()

	rootInfo, err := os.Stat(root)
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o700), rootInfo.Mode().Perm())

	managedInfo, err := os.Stat(filepath.Join(root, ".pemcast"))
	require.NoError(t, err)
	require.Equal(t, fs.FileMode(0o700), managedInfo.Mode().Perm())
}
