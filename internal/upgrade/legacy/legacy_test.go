package legacy

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json/v2"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
)

func TestDecodeAndConvertCanonicalV5(t *testing.T) {
	for _, testCase := range []struct {
		usage []x509.ExtKeyUsage
		typ   string
	}{
		{[]x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, bundle.TypeTLSServer},
		{[]x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}, bundle.TypeTLSClient},
	} {
		certificate, privateKey := testKeyPair(t, testCase.usage)
		encoded := encodeV5(t, certificate, privateKey)
		generation, digest := v5Generation(t, encoded)

		material, err := Decode("nginx", generation, encoded)
		require.NoError(t, err)
		require.Equal(t, digest, material.Digest)
		require.Equal(t, certificate, material.Files[CertificateName])
		require.Equal(t, privateKey, material.Files[PrivateKeyName])

		manifest, converted, convertedDigest, err := ConvertTLS(material, testCase.typ)
		require.NoError(t, err)
		require.Equal(t, bundle.SchemaV6, manifest.Schema)
		require.Equal(t, testCase.typ, manifest.Type)
		require.NotEqual(t, generation, bundle.Generation(convertedDigest))

		decodedManifest, files, decodedDigest, err := bundle.Decode(converted)
		require.NoError(t, err)
		require.Equal(t, manifest, decodedManifest)
		require.Equal(t, certificate, files[bundle.NameCertificateChain])
		require.Equal(t, privateKey, files[bundle.NamePrivateKey])
		require.Equal(t, convertedDigest, decodedDigest)
	}
}

func TestDecodeRejectsTamperingAndMismatchedGeneration(t *testing.T) {
	certificate, privateKey := testKeyPair(t, nil)
	encoded := encodeV5(t, certificate, privateKey)
	generation, _ := v5Generation(t, encoded)

	_, err := Decode("nginx", "sha256-other", encoded)
	require.ErrorContains(t, err, "does not match bundle digest")

	var manifest Manifest
	require.NoError(t, json.Unmarshal(encoded, &manifest))
	manifest.Files[0].Data = base64.StdEncoding.EncodeToString([]byte("changed"))
	tampered, err := json.Marshal(manifest)
	require.NoError(t, err)
	_, err = Decode("nginx", generation, tampered)
	require.ErrorContains(t, err, "sha256 mismatch")
}

func TestValidateRejectsNonCanonicalV5ProductShape(t *testing.T) {
	certificate, privateKey := testKeyPair(t, nil)
	manifest := buildManifest(t, certificate, privateKey)

	nonCanonical := manifest
	nonCanonical.Files = append(nonCanonical.Files, ManifestFile{
		Name: "extra.pem", Kind: KindCertificate, Encoding: EncodingBase64,
		SHA256: "0", Data: "",
	})
	err := nonCanonical.Validate()
	require.ErrorContains(t, err, "exactly one certificate/private-key pair")

	wrongNames := manifest
	wrongNames.Pairs[0].Certificate, wrongNames.Pairs[0].PrivateKey =
		wrongNames.Pairs[0].PrivateKey, wrongNames.Pairs[0].Certificate
	err = wrongNames.Validate()
	require.ErrorContains(t, err, "v5 pair must reference")

	_, _, _, err = ConvertTLS(&Material{Files: map[string][]byte{
		CertificateName: certificate, PrivateKeyName: privateKey,
	}}, bundle.TypeTrust)
	require.ErrorContains(t, err, "can only convert")
}

func testKeyPair(t *testing.T, usage []x509.ExtKeyUsage) ([]byte, []byte) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(time.Now().UnixNano()),
		Subject:      pkix.Name{CommonName: "legacy-v5-test"},
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

func buildManifest(t *testing.T, certificate, privateKey []byte) Manifest {
	t.Helper()

	return Manifest{
		Schema: Schema,
		Files: []ManifestFile{
			{
				Name: CertificateName, Kind: KindCertificate, Encoding: EncodingBase64,
				SHA256: hash(certificate), Data: base64.StdEncoding.EncodeToString(certificate),
			},
			{
				Name: PrivateKeyName, Kind: KindPrivateKey, Encoding: EncodingBase64,
				SHA256: hash(privateKey), Data: base64.StdEncoding.EncodeToString(privateKey),
			},
		},
		Pairs: []Pair{{Certificate: CertificateName, PrivateKey: PrivateKeyName}},
	}
}

func encodeV5(t *testing.T, certificate, privateKey []byte) []byte {
	t.Helper()

	data, err := json.Marshal(buildManifest(t, certificate, privateKey))
	require.NoError(t, err)
	return data
}

func v5Generation(t *testing.T, encoded []byte) (string, string) {
	t.Helper()

	var manifest Manifest
	require.NoError(t, json.Unmarshal(encoded, &manifest))
	files := make(map[string][]byte, len(manifest.Files))
	for _, file := range manifest.Files {
		content, err := base64.StdEncoding.DecodeString(file.Data)
		require.NoError(t, err)
		files[file.Name] = content
	}
	digest := ContentDigest(files)
	return bundle.Generation(digest), digest
}
