package bundle

import (
	"encoding/base64"
	"encoding/json/v2"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func testManifest(t *testing.T) (Manifest, string) {
	t.Helper()

	manifest, digest := NewTLSManifest([]byte("certificate"), []byte("private-key"), "fullchain.pem", "privkey.pem")
	return manifest, digest
}

func TestEncodeDecodeAndGeneration(t *testing.T) {
	manifest, digest := testManifest(t)
	encoded, err := Encode(manifest)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), MaxEncodedBundleBytes)

	decoded, files, decodedDigest, err := Decode(encoded)
	require.NoError(t, err)
	require.Equal(t, manifest, decoded)
	require.Equal(t, map[string][]byte{
		"fullchain.pem": []byte("certificate"),
		"privkey.pem":   []byte("private-key"),
	}, files)
	require.Equal(t, digest, decodedDigest)
	require.Equal(t, "sha256-"+digest, Generation(digest))
	require.NoError(t, ValidateGeneration(Generation(digest), digest))
}

func TestContentDigestStableAcrossFileOrder(t *testing.T) {
	manifest, digest := testManifest(t)
	require.Len(t, manifest.Files, 2)
	manifest.Files[0], manifest.Files[1] = manifest.Files[1], manifest.Files[0]

	_, files, decodedDigest, err := Decode(mustJSON(t, manifest))
	require.NoError(t, err)
	require.Equal(t, digest, decodedDigest)
	require.Equal(t, digest, ContentDigest(files))
}

func TestDecodeRejectsDigestMismatch(t *testing.T) {
	manifest, _ := testManifest(t)
	manifest.Files[0].Data = base64.StdEncoding.EncodeToString([]byte("changed"))
	_, _, _, err := Decode(mustJSON(t, manifest))
	require.ErrorContains(t, err, "sha256 mismatch")
}

func TestDecodeRejectsInvalidBase64(t *testing.T) {
	manifest, _ := testManifest(t)
	manifest.Files[0].Data = "not-base64!"
	_, _, _, err := Decode(mustJSON(t, manifest))
	require.ErrorContains(t, err, "invalid base64")
}

func TestParseManifestRejectsTraversalAndUnknownFields(t *testing.T) {
	_, err := ParseManifest([]byte(`{"schema":"pemcast/v3","extra":true}`))
	require.Error(t, err)

	manifest, _ := testManifest(t)
	manifest.Files[0].Name = "../key"
	_, err = ParseManifest(mustJSON(t, manifest))
	require.ErrorContains(t, err, "unsafe")
}

func TestParseManifestRejectsInvalidAndMismatchedKinds(t *testing.T) {
	manifest, _ := testManifest(t)
	manifest.Files[0].Kind = "chain"
	_, err := ParseManifest(mustJSON(t, manifest))
	require.ErrorContains(t, err, "invalid kind")

	manifest, _ = testManifest(t)
	manifest.Pairs[0].Certificate, manifest.Pairs[0].PrivateKey =
		manifest.Pairs[0].PrivateKey, manifest.Pairs[0].Certificate
	_, err = ParseManifest(mustJSON(t, manifest))
	require.ErrorContains(t, err, "mismatched file kinds")
}

func TestDecodeRejectsUnsupportedEncoding(t *testing.T) {
	manifest, _ := testManifest(t)
	manifest.Files[0].Encoding = "hex"
	_, _, _, err := Decode(mustJSON(t, manifest))
	require.ErrorContains(t, err, "unsupported encoding")
}

func TestEncodeRejectsOversizedBundle(t *testing.T) {
	manifest, _ := testManifest(t)
	manifest.Files[0].Data = strings.Repeat("A", MaxEncodedBundleBytes)
	_, err := Encode(manifest)
	require.ErrorContains(t, err, "exceeds")
}

func TestValidateGenerationRejectsMismatch(t *testing.T) {
	require.ErrorContains(t, ValidateGeneration("sha256-other", "digest"), "does not match")
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}
