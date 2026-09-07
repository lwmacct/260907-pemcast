package state

import (
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
