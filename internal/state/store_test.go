package state

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestStoreRoundTrip(t *testing.T) {
	store, err := New(t.TempDir())
	require.NoError(t, err)
	want := Target{Generation: "g1", Digest: "digest", Revision: 42, ActivatedAt: time.Now().UTC().Round(0), HookError: "failed"}
	require.NoError(t, store.Save("nginx", want))
	got, err := store.Load("nginx")
	require.NoError(t, err)
	require.Equal(t, want, got)
}

func TestStoreEnsureSecuresExistingStateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "state")
	require.NoError(t, os.MkdirAll(dir, 0o755))

	store, err := New(dir)
	require.NoError(t, err)
	require.NoError(t, store.Ensure())

	info, err := os.Stat(dir)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), info.Mode().Perm())
}

func TestNewDoesNotCreateStateDirectory(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "missing-state")
	if _, err := New(dir); err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("New() created state directory: stat error=%v", err)
	}

	store, err := New(dir)
	if err != nil {
		t.Fatalf("New() error = %v", err)
	}
	if err := store.Ensure(); err != nil {
		t.Fatalf("Ensure() error = %v", err)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("Stat() error = %v", err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("state directory mode=%o, want 700", got)
	}
}
