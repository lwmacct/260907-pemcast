package deploy

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLockRootsExcludesCompetingProcess(t *testing.T) {
	root := t.TempDir()

	locks, err := LockRoots([]string{root})
	require.NoError(t, err)
	require.Len(t, locks, 1)
	require.FileExists(t, filepath.Join(root, ".pemcast", "agent.lock"))

	process := exec.Command(os.Args[0], "-test.run=^TestLockRootsHelperProcess$")
	process.Env = append(os.Environ(), "PEMCAST_LOCK_TEST_ROOT="+root)
	require.NoError(t, process.Run())

	require.NoError(t, locks[0].Close())
	locksAgain, err := LockRoots([]string{root})
	require.NoError(t, err)
	require.NoError(t, locksAgain[0].Close())
}

func TestLockRootsHelperProcess(t *testing.T) {
	root := os.Getenv("PEMCAST_LOCK_TEST_ROOT")
	if root == "" {
		return
	}
	if _, err := LockRoots([]string{root}); err == nil {
		os.Exit(1)
	}
	os.Exit(0)
}

func TestLockRootsSecondCallerFails(t *testing.T) {
	root := t.TempDir()
	locks, err := LockRoots([]string{root})
	require.NoError(t, err)
	defer func() { require.NoError(t, locks[0].Close()) }()

	_, err = LockRoots([]string{root})
	require.ErrorContains(t, err, "already managed")
}

func TestLockRootsCleansUpPartialMultiRootAcquisition(t *testing.T) {
	first := t.TempDir()
	second := t.TempDir()
	third := t.TempDir()
	secondLock, err := lockRoot(second)
	require.NoError(t, err)
	defer func() { require.NoError(t, secondLock.Close()) }()
	thirdLock, err := lockRoot(third)
	require.NoError(t, err)
	defer func() { require.NoError(t, thirdLock.Close()) }()

	_, err = LockRoots([]string{first, second, third})
	require.ErrorContains(t, err, second)

	firstLock, err := lockRoot(first)
	require.NoError(t, err, "partial acquisition was not released")
	require.NoError(t, firstLock.Close())
}
