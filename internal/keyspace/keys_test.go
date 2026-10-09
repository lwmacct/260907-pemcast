package keyspace

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewKeysNormalizesAndValidatesPrefixes(t *testing.T) {
	keys, err := NewKeys(" /tenants/example/ ")
	require.NoError(t, err)
	require.Equal(t, "/tenants/example", keys.Prefix())
	require.Equal(t, "/tenants/example/v5", keys.Root())

	keys, err = NewKeys("/")
	require.NoError(t, err)
	require.Equal(t, "/", keys.Prefix())
	require.Equal(t, "/v5", keys.Root())

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

func TestKeysUseKindFirstV5Layout(t *testing.T) {
	keys, err := NewKeys("/tenants/example")
	require.NoError(t, err)

	require.Equal(t, "/tenants/example/v5/active/", keys.ActivePrefix())
	require.Equal(t, "/tenants/example/v5/active/nginx", keys.ActiveKey("nginx"))
	require.Equal(t, "/tenants/example/v5/bundles/", keys.BundlePrefix())
	require.Equal(t, "/tenants/example/v5/bundles/nginx/", keys.TargetBundlePrefix("nginx"))
	require.Equal(
		t,
		"/tenants/example/v5/bundles/nginx/sha256-value",
		keys.BundleKey("nginx", "sha256-value"),
	)
}

func TestRootPrefixUsesV5Directly(t *testing.T) {
	keys, err := NewKeys("/")
	require.NoError(t, err)

	require.Equal(t, "/v5", keys.Root())
	require.Equal(t, "/v5/active/nginx", keys.ActiveKey("nginx"))
	require.Equal(t, "/v5/bundles/nginx/sha256-value", keys.BundleKey("nginx", "sha256-value"))
}
