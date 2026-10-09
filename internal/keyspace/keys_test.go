package keyspace

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewKeysNormalizesAndValidatesPrefixes(t *testing.T) {
	keys, err := NewKeys(" /tenants/example/ ")
	require.NoError(t, err)
	require.Equal(t, "/tenants/example", keys.Prefix())
	require.Equal(t, "/tenants/example/v3", keys.Root())

	keys, err = NewKeys("/")
	require.NoError(t, err)
	require.Equal(t, "/", keys.Prefix())
	require.Equal(t, "/v3", keys.Root())

	for _, prefix := range []string{"", "pemcast", "/pemcast/../other", "/pemcast//"} {
		if prefix == "/pemcast//" {
			keys, err = NewKeys(prefix)
			require.NoError(t, err)
			require.Equal(t, "/pemcast", keys.Prefix())
			continue
		}
		_, err = NewKeys(prefix)
		require.Error(t, err, "prefix %q", prefix)
	}
}

func TestKeysUseTargetFirstV3Layout(t *testing.T) {
	keys, err := NewKeys("/tenants/example")
	require.NoError(t, err)

	require.Equal(t, "/tenants/example/v3/nginx/", keys.TargetPrefix("nginx"))
	require.Equal(t, "/tenants/example/v3/nginx/active", keys.ActiveKey("nginx"))
	require.Equal(
		t,
		"/tenants/example/v3/nginx/bundles/sha256-value",
		keys.BundleKey("nginx", "sha256-value"),
	)
}

func TestRootPrefixUsesV3Directly(t *testing.T) {
	keys, err := NewKeys("/")
	require.NoError(t, err)

	require.Equal(t, "/v3", keys.Root())
	require.Equal(t, "/v3/nginx/active", keys.ActiveKey("nginx"))
}
