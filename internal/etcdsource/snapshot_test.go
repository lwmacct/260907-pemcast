package etcdsource

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
)

func TestMaterialFromValueDecodesAndValidatesGeneration(t *testing.T) {
	certificate := []byte("certificate")
	privateKey := []byte("private-key")
	manifest, digest := bundle.NewTLSManifest(certificate, privateKey, "fullchain.pem", "privkey.pem")
	encoded, err := bundle.Encode(manifest)
	require.NoError(t, err)

	generation := bundle.Generation(digest)
	material, err := materialFromValue("nginx", generation, encoded, 42)
	require.NoError(t, err)
	require.Equal(t, "nginx", material.TargetID)
	require.Equal(t, generation, material.Generation)
	require.Equal(t, int64(42), material.Revision)
	require.Equal(t, digest, material.Digest)
	require.Equal(t, certificate, material.Files["fullchain.pem"])
	require.Equal(t, privateKey, material.Files["privkey.pem"])
}

func TestMaterialFromValueRejectsPointerDigestMismatch(t *testing.T) {
	manifest, _ := bundle.NewTLSManifest([]byte("certificate"), []byte("private-key"), "fullchain.pem", "privkey.pem")
	encoded, err := bundle.Encode(manifest)
	require.NoError(t, err)

	_, err = materialFromValue("nginx", "sha256-other", encoded, 42)
	require.ErrorContains(t, err, "does not match")
}

func TestMaterialFromValueRejectsMalformedBundle(t *testing.T) {
	_, err := materialFromValue("nginx", "sha256-value", []byte(`{invalid`), 42)
	require.ErrorContains(t, err, "decode bundle")
}

func TestProtocolKeysUseFixedV2Root(t *testing.T) {
	require.Equal(t, "/pemcast/v2/active/", ActivePrefix())
	require.Equal(t, "/pemcast/v2/active/nginx", ActiveKey("nginx"))
	require.Equal(t, "/pemcast/v2/bundles/nginx/sha256-value", BundleKey("nginx", "sha256-value"))
}
