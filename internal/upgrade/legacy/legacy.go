// Package legacy decodes the canonical previous pemcast bundle format for the
// one-way v5-to-v6 upgrade path. It is not a runtime compatibility layer.
package legacy

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"sort"
	"strings"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
)

const (
	Schema = "pemcast/v5"
	// ProtocolRoot is the previous protocol root below the configured namespace.
	ProtocolRoot = "/v5"

	KindCertificate = "certificate"
	KindPrivateKey  = "private-key"
	EncodingBase64  = "base64"

	CertificateName = "fullchain.pem"
	PrivateKeyName  = "privkey.pem"
)

// Manifest is the strict shape of a canonical v5 bundle.
type Manifest struct {
	Schema string         `json:"schema"`
	Files  []ManifestFile `json:"files"`
	Pairs  []Pair         `json:"pairs"`
}

// ManifestFile contains one v5 inline file.
type ManifestFile struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	SHA256   string `json:"sha256"`
	Encoding string `json:"encoding"`
	Data     string `json:"data"`
}

// Pair declares one v5 certificate and private key.
type Pair struct {
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private-key"`
}

// Material is a decoded and verified canonical v5 bundle.
type Material struct {
	TargetID   string
	Generation string
	Manifest   Manifest
	Files      map[string][]byte
	Digest     string
}

// Decode verifies one canonical v5 bundle and its active generation.
func Decode(targetID, generation string, data []byte) (*Material, error) {
	if !bundle.SafeName(targetID) || !bundle.SafeName(generation) {
		return nil, fmt.Errorf("unsafe v5 target or generation")
	}
	if len(data) > bundle.MaxEncodedBundleBytes {
		return nil, fmt.Errorf(
			"v5 encoded bundle size %d exceeds %d bytes",
			len(data), bundle.MaxEncodedBundleBytes,
		)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest, json.RejectUnknownMembers(true)); err != nil {
		return nil, fmt.Errorf("decode v5 bundle: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return nil, err
	}

	files := make(map[string][]byte, len(manifest.Files))
	for _, declared := range manifest.Files {
		content, err := base64.StdEncoding.DecodeString(declared.Data)
		if err != nil {
			return nil, fmt.Errorf("v5 file %q has invalid base64 data", declared.Name)
		}
		if hash(content) != declared.SHA256 {
			return nil, fmt.Errorf("v5 file %q sha256 mismatch", declared.Name)
		}
		files[declared.Name] = content
	}
	digest := ContentDigest(files)
	if generation != bundle.Generation(digest) {
		return nil, fmt.Errorf(
			"v5 generation %q does not match bundle digest %q",
			generation, bundle.Generation(digest),
		)
	}
	return &Material{
		TargetID: targetID, Generation: generation, Manifest: manifest,
		Files: files, Digest: digest,
	}, nil
}

// ConvertTLS rebuilds canonical v5 material as a fully validated v6 identity.
func ConvertTLS(material *Material, bundleType string) (bundle.Manifest, []byte, string, error) {
	if material == nil {
		return bundle.Manifest{}, nil, "", fmt.Errorf("v5 material is nil")
	}
	if bundleType != bundle.TypeTLSServer && bundleType != bundle.TypeTLSClient {
		return bundle.Manifest{}, nil, "", fmt.Errorf(
			"v5 material can only convert to %q or %q",
			bundle.TypeTLSServer, bundle.TypeTLSClient,
		)
	}
	certificate, ok := material.Files[CertificateName]
	if !ok {
		return bundle.Manifest{}, nil, "", fmt.Errorf("v5 bundle is missing %q", CertificateName)
	}
	privateKey, ok := material.Files[PrivateKeyName]
	if !ok {
		return bundle.Manifest{}, nil, "", fmt.Errorf("v5 bundle is missing %q", PrivateKeyName)
	}

	manifest, digest := bundle.NewTLSManifest(bundleType, certificate, privateKey)
	encoded, err := bundle.Encode(manifest)
	if err != nil {
		return bundle.Manifest{}, nil, "", fmt.Errorf("convert target %q to v6: %w", material.TargetID, err)
	}
	return manifest, encoded, digest, nil
}

// Validate accepts only the canonical v5 product shape. The broader v5 manifest
// grammar could represent extra files or pairs, but those were never produced by
// pemcast tools pack and cannot be migrated without guessing their semantics.
func (m Manifest) Validate() error {
	if m.Schema != Schema {
		return fmt.Errorf("unsupported v5 manifest schema %q", m.Schema)
	}
	if len(m.Files) != 2 || len(m.Pairs) != 1 {
		return fmt.Errorf("v5 bundle must contain exactly one certificate/private-key pair")
	}

	files := make(map[string]ManifestFile, len(m.Files))
	for index, file := range m.Files {
		if !bundle.SafeName(file.Name) {
			return fmt.Errorf("v5 files[%d] has unsafe name %q", index, file.Name)
		}
		if _, exists := files[file.Name]; exists {
			return fmt.Errorf("v5 file %q is duplicated", file.Name)
		}
		if file.Encoding != EncodingBase64 {
			return fmt.Errorf("v5 file %q has unsupported encoding %q", file.Name, file.Encoding)
		}
		digest, err := hex.DecodeString(file.SHA256)
		if err != nil || len(digest) != sha256.Size || file.SHA256 != strings.ToLower(file.SHA256) {
			return fmt.Errorf("v5 file %q has invalid sha256", file.Name)
		}
		files[file.Name] = file
	}
	for _, name := range []string{CertificateName, PrivateKeyName} {
		file, ok := files[name]
		if !ok {
			return fmt.Errorf("v5 bundle is missing %q", name)
		}
		expectedKind := KindCertificate
		if name == PrivateKeyName {
			expectedKind = KindPrivateKey
		}
		if file.Kind != expectedKind {
			return fmt.Errorf("v5 file %q has invalid kind %q", name, file.Kind)
		}
	}

	pair := m.Pairs[0]
	if pair.Certificate != CertificateName || pair.PrivateKey != PrivateKeyName {
		return fmt.Errorf(
			"v5 pair must reference %q and %q",
			CertificateName, PrivateKeyName,
		)
	}
	return nil
}

// ContentDigest computes the historical v5 whole-bundle digest.
func ContentDigest(files map[string][]byte) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(Schema))
	_, _ = hasher.Write([]byte{0})

	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sum := sha256.Sum256(files[name])
		_, _ = hasher.Write([]byte(name))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write(sum[:])
	}
	return hex.EncodeToString(hasher.Sum(nil))
}

func hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}
