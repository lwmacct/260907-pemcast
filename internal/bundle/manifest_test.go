package bundle

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json/v2"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestEncodeDecodeAndGeneration(t *testing.T) {
	certificate, privateKey := testKeyPair(t)
	manifest, digest := NewTLSManifest(TypeTLSServer, certificate, privateKey)
	encoded, err := Encode(manifest)
	require.NoError(t, err)
	require.LessOrEqual(t, len(encoded), MaxEncodedBundleBytes)

	decoded, files, decodedDigest, err := Decode(encoded)
	require.NoError(t, err)
	require.Equal(t, manifest, decoded)
	require.Equal(t, map[string][]byte{
		NameCertificateChain: certificate,
		NamePrivateKey:       privateKey,
	}, files)
	require.Equal(t, digest, decodedDigest)
	require.Equal(t, "sha256-"+digest, Generation(digest))
	require.NoError(t, ValidateGeneration(Generation(digest), digest))
}

func TestSemanticDigestIncludesTypeAndRole(t *testing.T) {
	certificate, privateKey := testKeyPair(t)
	manifest, digest := NewTLSManifest(TypeTLSServer, certificate, privateKey)
	require.Equal(t, digest, ManifestDigest(manifest, map[string][]byte{
		NameCertificateChain: certificate,
		NamePrivateKey:       privateKey,
	}))

	changedType := manifest
	changedType.Type = TypeTLSClient
	require.NotEqual(t, digest, ManifestDigest(changedType, map[string][]byte{
		NameCertificateChain: certificate,
		NamePrivateKey:       privateKey,
	}))

	changedRole := manifest
	for index := range changedRole.Files {
		if changedRole.Files[index].Name == NameCertificateChain {
			changedRole.Files[index].Role = RoleCACertificate
		}
	}
	require.NotEqual(t, digest, ManifestDigest(changedRole, map[string][]byte{
		NameCertificateChain: certificate,
		NamePrivateKey:       privateKey,
	}))
}

func TestContentDigestStableAcrossFileOrder(t *testing.T) {
	certificate, privateKey := testKeyPair(t)
	manifest, digest := NewTLSManifest(TypeTLSClient, certificate, privateKey)
	require.Len(t, manifest.Files, 2)
	manifest.Files[0], manifest.Files[1] = manifest.Files[1], manifest.Files[0]

	_, files, decodedDigest, err := Decode(mustJSON(t, manifest))
	require.NoError(t, err)
	require.Equal(t, digest, decodedDigest)
	require.Equal(t, digest, ManifestDigest(manifest, files))
}

func TestTrustBundleRequiresCACertificates(t *testing.T) {
	ca := testCertificate(t, true, nil)
	manifest, digest := NewTrustManifest(ca)
	encoded, err := Encode(manifest)
	require.NoError(t, err)
	_, files, decodedDigest, err := Decode(encoded)
	require.NoError(t, err)
	require.Equal(t, ca, files[NameCABundle])
	require.Equal(t, digest, decodedDigest)

	leaf, key := testKeyPair(t)
	_ = key
	invalid, _ := NewTrustManifest(leaf)
	_, err = Encode(invalid)
	require.ErrorContains(t, err, "not a CA certificate")
}

func TestDecodeRejectsMismatchedKeyPair(t *testing.T) {
	certificate, _ := testKeyPair(t)
	_, otherKey := testKeyPair(t)
	manifest, _ := NewTLSManifest(TypeTLSServer, certificate, otherKey)
	_, _, _, err := Decode(mustJSON(t, manifest))
	require.ErrorContains(t, err, "parse TLS key pair")
}

func TestIdentityRejectsWrongExtendedKeyUsage(t *testing.T) {
	certificate, privateKey := testKeyPairWithUsage(t, []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth})
	server, _ := NewTLSManifest(TypeTLSServer, certificate, privateKey)
	_, err := Encode(server)
	require.ErrorContains(t, err, "does not permit tls-server usage")

	client, _ := NewTLSManifest(TypeTLSClient, certificate, privateKey)
	_, err = Encode(client)
	require.NoError(t, err)
}

func TestIdentityRejectsNonLeafFirstChain(t *testing.T) {
	leaf, chain, privateKey := testSignedChain(t)
	ordered := append(append([]byte(nil), leaf...), chain...)
	manifest, _ := NewTLSManifest(TypeTLSServer, ordered, privateKey)
	_, err := Encode(manifest)
	require.NoError(t, err)

	reversed := append(append([]byte(nil), chain...), leaf...)
	manifest, _ = NewTLSManifest(TypeTLSServer, reversed, privateKey)
	_, err = Encode(manifest)
	require.ErrorContains(t, err, "certificate chain leaf is a CA certificate")

	unrelated := testCertificate(t, true, nil)
	manifest, _ = NewTLSManifest(TypeTLSServer, append(append([]byte(nil), leaf...), unrelated...), privateKey)
	_, err = Encode(manifest)
	require.ErrorContains(t, err, "certificate chain position 0 is not signed by position 1")
}

func TestValidatePublicKeySupportsCommonAlgorithms(t *testing.T) {
	ecKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	require.NoError(t, err)
	require.NoError(t, validatePublicKey(&ecKey.PublicKey))

	rsaKey, err := rsa.GenerateKey(rand.Reader, 2048)
	require.NoError(t, err)
	require.NoError(t, validatePublicKey(&rsaKey.PublicKey))

	smallRSA, err := rsa.GenerateKey(rand.Reader, 1024)
	require.NoError(t, err)
	require.ErrorContains(t, validatePublicKey(&smallRSA.PublicKey), "shorter than 2048 bits")
}

func TestDecodeRejectsDigestAndEncodingMismatch(t *testing.T) {
	certificate, privateKey := testKeyPair(t)
	manifest, _ := NewTLSManifest(TypeTLSServer, certificate, privateKey)
	manifest.Files[0].Data = base64.StdEncoding.EncodeToString([]byte("changed"))
	_, _, _, err := Decode(mustJSON(t, manifest))
	require.ErrorContains(t, err, "sha256 mismatch")

	manifest, _ = NewTLSManifest(TypeTLSServer, certificate, privateKey)
	manifest.Files[0].Data = "not-base64!"
	_, _, _, err = Decode(mustJSON(t, manifest))
	require.ErrorContains(t, err, "invalid base64")

	manifest, _ = NewTLSManifest(TypeTLSServer, certificate, privateKey)
	manifest.Files[0].Encoding = "hex"
	_, _, _, err = Decode(mustJSON(t, manifest))
	require.ErrorContains(t, err, "unsupported encoding")
}

func TestParseManifestRejectsUnsafeAndInvalidStructure(t *testing.T) {
	_, err := ParseManifest([]byte(`{"schema":"pemcast/v6","extra":true}`))
	require.Error(t, err)

	certificate, privateKey := testKeyPair(t)
	manifest, _ := NewTLSManifest(TypeTLSServer, certificate, privateKey)
	manifest.Files[0].Name = "../key"
	_, err = ParseManifest(mustJSON(t, manifest))
	require.ErrorContains(t, err, "unsafe")

	manifest, _ = NewTLSManifest(TypeTLSServer, certificate, privateKey)
	manifest.Files[0].Role = "chain"
	_, err = ParseManifest(mustJSON(t, manifest))
	require.ErrorContains(t, err, "invalid role")

	manifest, _ = NewTLSManifest(TypeTLSServer, certificate, privateKey)
	manifest.Type = "other"
	_, err = ParseManifest(mustJSON(t, manifest))
	require.ErrorContains(t, err, "unsupported bundle type")
}

func TestValidateGenerationRejectsMismatch(t *testing.T) {
	require.ErrorContains(t, ValidateGeneration("sha256-other", "digest"), "does not match")
}

func testKeyPair(t *testing.T) ([]byte, []byte) {
	return testKeyPairWithUsage(t, nil)
}

func testKeyPairWithUsage(t *testing.T, usage []x509.ExtKeyUsage) ([]byte, []byte) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pemcast-test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		ExtKeyUsage:  usage,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func testCertificate(t *testing.T, isCA bool, usage []x509.ExtKeyUsage) []byte {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber:          big.NewInt(2),
		Subject:               pkix.Name{CommonName: "pemcast-test-ca"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  isCA,
		BasicConstraintsValid: true,
		ExtKeyUsage:           usage,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func testSignedChain(t *testing.T) (leaf []byte, chain []byte, privateKey []byte) {
	t.Helper()

	caPublic, caPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	caTemplate := &x509.Certificate{
		SerialNumber:          big.NewInt(4),
		Subject:               pkix.Name{CommonName: "pemcast-test-root"},
		NotBefore:             time.Now().Add(-time.Minute),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
	}
	caDER, err := x509.CreateCertificate(rand.Reader, caTemplate, caTemplate, caPublic, caPrivate)
	require.NoError(t, err)
	caCertificate, err := x509.ParseCertificate(caDER)
	require.NoError(t, err)

	leafPublic, leafPrivate, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	leafTemplate := &x509.Certificate{
		SerialNumber: big.NewInt(5),
		Subject:      pkix.Name{CommonName: "pemcast-test-leaf"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	leafDER, err := x509.CreateCertificate(rand.Reader, leafTemplate, caCertificate, leafPublic, caPrivate)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(leafPrivate)
	require.NoError(t, err)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: leafDER}),
		pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}),
		pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func mustJSON(t *testing.T, value any) []byte {
	t.Helper()

	data, err := json.Marshal(value)
	require.NoError(t, err)
	return data
}
