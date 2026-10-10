package bundle

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestValidatePolicyChecksValidityAndServerNames(t *testing.T) {
	material := testServerMaterial(t, []string{"example.com"})
	now := time.Now()

	require.NoError(t, ValidatePolicy(material, Policy{
		Now: now, RejectExpired: true, MinimumValidity: time.Minute, ServerNames: []string{"example.com"},
	}))
	require.ErrorContains(t, ValidatePolicy(material, Policy{
		Now: now, ServerNames: []string{"other.example"},
	}), "does not match server name")
	require.ErrorContains(t, ValidatePolicy(material, Policy{
		Now: material.Leaf.NotAfter.Add(time.Minute), RejectExpired: true,
	}), "expired")
	require.ErrorContains(t, ValidatePolicy(material, Policy{
		Now: now, MinimumValidity: 2 * time.Hour,
	}), "less than required")
}

func TestValidatePolicyChecksEveryTrustCertificate(t *testing.T) {
	first := testCertificate(t, true, nil)
	second := testCertificate(t, true, nil)
	manifest, digest := NewTrustManifest(append(append([]byte(nil), first...), second...))
	_, files, _, err := Decode(mustJSON(t, manifest))
	require.NoError(t, err)
	certificates, _, err := manifest.ValidateFiles(files)
	require.NoError(t, err)
	material := &Material{
		Manifest: manifest, Files: files, Digest: digest, Certificates: certificates,
	}

	now := time.Now()
	require.NoError(t, ValidatePolicy(material, Policy{Now: now, RejectExpired: true}))

	certificates[1].NotAfter = now.Add(-time.Minute)
	require.ErrorContains(t, ValidatePolicy(material, Policy{Now: now, RejectExpired: true}), "certificate 1 expired")
}

func testServerMaterial(t *testing.T, names []string) *Material {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(3),
		Subject:      pkix.Name{CommonName: "policy-test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
		DNSNames:     names,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	require.NoError(t, err)
	certificate := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)
	key := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})

	manifest, digest := NewTLSManifest(TypeTLSServer, certificate, key)
	_, files, _, err := Decode(mustJSON(t, manifest))
	require.NoError(t, err)
	certificates, leaf, err := manifest.ValidateFiles(files)
	require.NoError(t, err)
	return &Material{
		Manifest: manifest, Files: files, Digest: digest,
		Certificates: certificates, Leaf: leaf,
	}
}
