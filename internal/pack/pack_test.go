package pack

import (
	"crypto/ed25519"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"encoding/pem"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
)

func TestBuildProducesDeterministicV5Artifacts(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t)
	options := Options{
		TargetID:        "nginx",
		EtcdPrefix:      "/tenants/example/",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	}

	first, err := Build(options)
	require.NoError(t, err)
	second, err := Build(options)
	require.NoError(t, err)
	require.Equal(t, first, second)

	manifest, files, digest, err := bundle.Decode(first.Bundle)
	require.NoError(t, err)
	require.Equal(t, bundle.SchemaV5, manifest.Schema)
	require.Equal(t, digest, first.Metadata.BundleSHA256)
	require.Equal(t, bundle.Generation(digest), first.Metadata.Generation)
	require.Equal(t, hashBytes(first.Bundle), first.Metadata.BundleValueSHA256)
	require.Contains(t, files, "fullchain.pem")
	require.Contains(t, files, "privkey.pem")
	require.Equal(t, "/tenants/example", first.Metadata.EtcdPrefix)
	require.Equal(t, "/tenants/example/v5/active/nginx", first.Metadata.ActiveKey)
	require.Equal(
		t,
		"/tenants/example/v5/bundles/nginx/"+first.Metadata.Generation,
		first.Metadata.BundleKey,
	)

	condition := `create("/tenants/example/v5/bundles/nginx/` + first.Metadata.Generation + `") = "0"`
	require.True(t, strings.HasPrefix(string(first.StageTxn), condition))
	putLine := `put "/tenants/example/v5/bundles/nginx/` + first.Metadata.Generation + `" "`
	require.True(t, strings.Contains(string(first.StageTxn), putLine))
	require.True(t, strings.HasSuffix(string(first.StageTxn), "\"\n\n\n"))
}

func TestBuildRejectsMismatchedTLSKeyPair(t *testing.T) {
	certificatePath, _ := writeKeyPair(t)
	_, otherPrivateKeyPath := writeKeyPair(t)

	_, err := Build(Options{
		TargetID:        "nginx",
		EtcdPrefix:      "/pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  otherPrivateKeyPath,
	})
	require.ErrorContains(t, err, "parse TLS key pair")
}

func TestBuildRejectsUnsafeTargetAndPrefix(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t)

	_, err := Build(Options{
		TargetID:        "../nginx",
		EtcdPrefix:      "/pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	require.ErrorContains(t, err, "unsafe")

	_, err = Build(Options{
		TargetID:        "nginx",
		EtcdPrefix:      "pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	require.ErrorContains(t, err, "must be absolute")
}

func TestWriteCreatesPrivateArtifactsAndRefusesReplacement(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t)
	result, err := Build(Options{
		TargetID:        "nginx",
		EtcdPrefix:      "/pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	require.NoError(t, err)

	outputDir := filepath.Join(t.TempDir(), "pack")
	require.NoError(t, Write(outputDir, result))
	for _, name := range []string{"bundle.json", "metadata.json", "stage.txn"} {
		info, err := os.Stat(filepath.Join(outputDir, name))
		require.NoError(t, err)
		require.Equal(t, os.FileMode(0o600), info.Mode().Perm())
	}
	require.Equal(t, result.Bundle, mustRead(t, filepath.Join(outputDir, "bundle.json")))
	require.Equal(t, result.StageTxn, mustRead(t, filepath.Join(outputDir, "stage.txn")))

	err = Write(outputDir, result)
	require.ErrorContains(t, err, "already exists")
}

func TestWriteRejectsUnsafeOutputDirectory(t *testing.T) {
	err := Write("relative-output", Result{})
	require.ErrorContains(t, err, "must be a clean absolute non-root path")

	err = Write(string(filepath.Separator), Result{})
	require.ErrorContains(t, err, "must be a clean absolute non-root path")
}

func TestWriteRejectsInvalidPack(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t)
	result, err := Build(Options{
		TargetID:        "nginx",
		EtcdPrefix:      "/pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	require.NoError(t, err)
	result.Bundle = append([]byte(nil), result.Bundle...)
	result.Bundle[len(result.Bundle)-2]++

	outputDir := filepath.Join(t.TempDir(), "pack")
	err = Write(outputDir, result)
	require.ErrorContains(t, err, "refuse invalid pack")
	require.NoDirExists(t, outputDir)
}

func TestWriteRebuildsMissingStageTransaction(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t)
	result, err := Build(Options{
		TargetID:        "nginx",
		EtcdPrefix:      "/pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	require.NoError(t, err)
	result.StageTxn = nil

	outputDir := filepath.Join(t.TempDir(), "pack")
	require.NoError(t, Write(outputDir, result))
	require.Equal(t, []byte(stageTransaction(result.Metadata.BundleKey, result.Bundle)), mustRead(t, filepath.Join(outputDir, "stage.txn")))
}

func TestWriteRejectsMismatchedStageTransaction(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t)
	result, err := Build(Options{
		TargetID:        "nginx",
		EtcdPrefix:      "/pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	require.NoError(t, err)
	result.StageTxn = []byte("invalid transaction\n")

	outputDir := filepath.Join(t.TempDir(), "pack")
	err = Write(outputDir, result)
	require.ErrorContains(t, err, "stage transaction does not match")
	require.NoDirExists(t, outputDir)
}

func TestReadValidatesOfficialPackWithoutRequiringStageTransaction(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t)
	result, err := Build(Options{
		TargetID:        "nginx",
		EtcdPrefix:      "/pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	require.NoError(t, err)

	directory := t.TempDir()
	metadata, err := json.Marshal(result.Metadata)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(filepath.Join(directory, "bundle.json"), result.Bundle, 0o600))
	require.NoError(t, os.WriteFile(filepath.Join(directory, "metadata.json"), metadata, 0o600))

	read, err := Read(directory)
	require.NoError(t, err)
	require.Equal(t, result.Metadata, read.Metadata)
	require.Equal(t, result.Bundle, read.Bundle)
	require.Nil(t, read.StageTxn)
}

func TestReadRejectsTamperedBundleAndUnknownMetadataField(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t)
	result, err := Build(Options{
		TargetID:        "nginx",
		EtcdPrefix:      "/pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	require.NoError(t, err)

	tampered := result
	tampered.Bundle = append([]byte(nil), result.Bundle...)
	tampered.Bundle[len(tampered.Bundle)-2]++
	err = Validate(tampered)
	require.ErrorContains(t, err, "bundle-value-sha256")

	directory := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(directory, "bundle.json"), result.Bundle, 0o600))
	invalidMetadata := `{"schema":"pemcast-pack/v5","extra":true}`
	require.NoError(t, os.WriteFile(filepath.Join(directory, "metadata.json"), []byte(invalidMetadata), 0o600))
	_, err = Read(directory)
	require.ErrorContains(t, err, "unknown")
}

func TestValidateRejectsInconsistentMetadata(t *testing.T) {
	certificatePath, privateKeyPath := writeKeyPair(t)
	result, err := Build(Options{
		TargetID:        "nginx",
		EtcdPrefix:      "/pemcast",
		CertificatePath: certificatePath,
		PrivateKeyPath:  privateKeyPath,
	})
	require.NoError(t, err)

	wrongDigest := result
	wrongDigest.Metadata.BundleSHA256 = strings.Repeat("0", 64)
	err = Validate(wrongDigest)
	require.ErrorContains(t, err, "bundle digest")

	wrongKey := result
	wrongKey.Metadata.BundleKey += "-wrong"
	err = Validate(wrongKey)
	require.ErrorContains(t, err, "bundle key")

	wrongFileHash := result
	wrongCertificateMetadata := wrongFileHash.Metadata.Files["fullchain.pem"]
	wrongCertificateMetadata.SHA256 = strings.Repeat("0", 64)
	wrongFileHash.Metadata.Files["fullchain.pem"] = wrongCertificateMetadata
	err = Validate(wrongFileHash)
	require.ErrorContains(t, err, "metadata does not match bundle file")

	wrongPair := result
	wrongPair.Bundle, err = replaceCertificateBytes(wrongPair.Bundle, []byte("not-a-certificate"))
	require.NoError(t, err)
	wrongPair.Metadata.BundleValueSHA256 = hashBytes(wrongPair.Bundle)
	wrongPair.Metadata.EncodedSize = len(wrongPair.Bundle)
	manifest, _, _, err := bundle.Decode(wrongPair.Bundle)
	require.NoError(t, err)
	for _, file := range manifest.Files {
		if file.Name == "fullchain.pem" {
			wrongCertificateMetadata := wrongPair.Metadata.Files[file.Name]
			wrongCertificateMetadata.SHA256 = file.SHA256
			wrongPair.Metadata.Files[file.Name] = wrongCertificateMetadata
		}
	}
	wrongPair.Metadata.BundleSHA256 = bundle.ContentDigest(map[string][]byte{
		"fullchain.pem": []byte("not-a-certificate"),
		"privkey.pem":   mustRead(t, privateKeyPath),
	})
	wrongPair.Metadata.Generation = bundle.Generation(wrongPair.Metadata.BundleSHA256)
	wrongPair.Metadata.BundleKey = "/pemcast/v5/bundles/nginx/" + wrongPair.Metadata.Generation
	err = Validate(wrongPair)
	require.ErrorContains(t, err, "TLS pair is invalid")
}

func writeKeyPair(t *testing.T) (string, string) {
	t.Helper()

	public, private, err := ed25519.GenerateKey(rand.Reader)
	require.NoError(t, err)
	template := &x509.Certificate{
		SerialNumber: big.NewInt(1),
		Subject:      pkix.Name{CommonName: "pack-test"},
		NotBefore:    time.Now().Add(-time.Minute),
		NotAfter:     time.Now().Add(time.Hour),
	}
	certificateDER, err := x509.CreateCertificate(rand.Reader, template, template, public, private)
	require.NoError(t, err)
	keyDER, err := x509.MarshalPKCS8PrivateKey(private)
	require.NoError(t, err)

	dir := t.TempDir()
	certificatePath := filepath.Join(dir, "fullchain.pem")
	privateKeyPath := filepath.Join(dir, "privkey.pem")
	require.NoError(t, os.WriteFile(certificatePath, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: certificateDER}), 0o600))
	require.NoError(t, os.WriteFile(privateKeyPath, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER}), 0o600))
	return certificatePath, privateKeyPath
}

func mustRead(t *testing.T, path string) []byte {
	t.Helper()

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	return data
}

func hashBytes(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

func replaceCertificateBytes(encoded []byte, certificate []byte) ([]byte, error) {
	var manifest bundle.Manifest
	if err := json.Unmarshal(encoded, &manifest); err != nil {
		return nil, err
	}
	for index := range manifest.Files {
		if manifest.Files[index].Name == "fullchain.pem" {
			manifest.Files[index].Data = base64.StdEncoding.EncodeToString(certificate)
			manifest.Files[index].SHA256 = hashBytes(certificate)
		}
	}
	return json.Marshal(manifest)
}
