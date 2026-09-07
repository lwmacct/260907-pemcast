package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseManifestAndVerifyFiles(t *testing.T) {
	contents := []byte("certificate")
	sum := sha256.Sum256(contents)
	manifest, err := ParseManifest([]byte(`{"schema":"pemcast/v1","files":[{"name":"fullchain.pem","kind":"certificate","sha256":"` + hex.EncodeToString(sum[:]) + `"},{"name":"privkey.pem","kind":"private-key","sha256":"` + hex.EncodeToString(sum[:]) + `"}],"pairs":[{"certificate":"fullchain.pem","private-key":"privkey.pem"}]}`))
	require.NoError(t, err)
	digest, err := VerifyFiles(manifest, map[string][]byte{"fullchain.pem": contents, "privkey.pem": contents})
	require.NoError(t, err)
	require.Len(t, digest, 64)
}

func TestParseManifestRejectsTraversalAndUnknownFields(t *testing.T) {
	sum := sha256.Sum256(nil)
	_, err := ParseManifest([]byte(`{"schema":"pemcast/v1","files":[{"name":"../key","kind":"private-key","sha256":"` + hex.EncodeToString(sum[:]) + `"}],"pairs":[{"certificate":"../key","private-key":"../key"}]}`))
	require.ErrorContains(t, err, "unsafe")
	_, err = ParseManifest([]byte(`{"schema":"pemcast/v1","files":[],"pairs":[],"extra":true}`))
	require.Error(t, err)
}

func TestVerifyFilesDetectsDigestMismatch(t *testing.T) {
	manifest := Manifest{
		Schema: SchemaV1,
		Files:  []ManifestFile{{Name: "file", SHA256: hex.EncodeToString(make([]byte, sha256.Size))}},
		Pairs:  []Pair{{Certificate: "file", PrivateKey: "file"}},
	}
	_, err := VerifyFiles(manifest, map[string][]byte{"file": []byte("changed")})
	require.ErrorContains(t, err, "sha256 mismatch")
}
