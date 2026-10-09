// Package bundle defines and validates the immutable pemcast/v5 bundle format.
package bundle

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"sort"
	"strings"
)

const SchemaV5 = "pemcast/v5"

const (
	KindCertificate = "certificate"
	KindPrivateKey  = "private-key"
	EncodingBase64  = "base64"
)

// MaxEncodedBundleBytes bounds the complete single etcd value. TLS material is
// small; larger artifacts belong in object storage rather than etcd.
const MaxEncodedBundleBytes = 1 << 20

// Manifest is one complete, immutable certificate bundle stored under a single etcd key.
type Manifest struct {
	Schema string         `json:"schema"`
	Files  []ManifestFile `json:"files"`
	Pairs  []Pair         `json:"pairs"`
}

// ManifestFile contains one inline file and its raw-content digest.
type ManifestFile struct {
	Name     string `json:"name"`
	Kind     string `json:"kind"`
	SHA256   string `json:"sha256"`
	Encoding string `json:"encoding"`
	Data     string `json:"data"`
}

// Pair declares a certificate and private key that must parse together.
type Pair struct {
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private-key"`
}

// Material is a fully decoded and verified immutable bundle.
type Material struct {
	TargetID   string
	Generation string
	Revision   int64
	Manifest   Manifest
	Files      map[string][]byte
	Digest     string
}

// NewTLSManifest builds a deterministic manifest for one certificate pair.
func NewTLSManifest(certificate, privateKey []byte, certificateName, privateKeyName string) (Manifest, string) {
	files := []ManifestFile{
		{
			Name: certificateName, Kind: KindCertificate, SHA256: hash(certificate),
			Encoding: EncodingBase64, Data: base64.StdEncoding.EncodeToString(certificate),
		},
		{
			Name: privateKeyName, Kind: KindPrivateKey, SHA256: hash(privateKey),
			Encoding: EncodingBase64, Data: base64.StdEncoding.EncodeToString(privateKey),
		},
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	manifest := Manifest{
		Schema: SchemaV5,
		Files:  files,
		Pairs: []Pair{{
			Certificate: certificateName,
			PrivateKey:  privateKeyName,
		}},
	}
	return manifest, ContentDigest(map[string][]byte{
		certificateName: certificate,
		privateKeyName:  privateKey,
	})
}

// Encode validates and serializes one complete bundle value.
func Encode(manifest Manifest) ([]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	data, err := json.Marshal(manifest)
	if err != nil {
		return nil, fmt.Errorf("encode bundle: %w", err)
	}
	if len(data) > MaxEncodedBundleBytes {
		return nil, fmt.Errorf("encoded bundle size %d exceeds %d bytes", len(data), MaxEncodedBundleBytes)
	}
	return data, nil
}

// Decode strictly parses, decodes, and verifies one complete bundle value.
func Decode(data []byte) (Manifest, map[string][]byte, string, error) {
	if len(data) > MaxEncodedBundleBytes {
		return Manifest{}, nil, "", fmt.Errorf("encoded bundle size %d exceeds %d bytes", len(data), MaxEncodedBundleBytes)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest, json.RejectUnknownMembers(true)); err != nil {
		return Manifest{}, nil, "", fmt.Errorf("decode bundle: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, nil, "", err
	}

	files := make(map[string][]byte, len(manifest.Files))
	for _, declared := range manifest.Files {
		content, err := base64.StdEncoding.DecodeString(declared.Data)
		if err != nil {
			return Manifest{}, nil, "", fmt.Errorf("bundle file %q has invalid base64 data", declared.Name)
		}
		if hash(content) != declared.SHA256 {
			return Manifest{}, nil, "", fmt.Errorf("bundle file %q sha256 mismatch", declared.Name)
		}
		files[declared.Name] = content
	}
	return manifest, files, ContentDigest(files), nil
}

// ParseManifest strictly parses and structurally validates a manifest without decoding inline data.
func ParseManifest(data []byte) (Manifest, error) {
	if len(data) > MaxEncodedBundleBytes {
		return Manifest{}, fmt.Errorf("encoded bundle size %d exceeds %d bytes", len(data), MaxEncodedBundleBytes)
	}
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest, json.RejectUnknownMembers(true)); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Validate checks manifest structure and metadata without decoding file data.
func (m Manifest) Validate() error {
	if m.Schema != SchemaV5 {
		return fmt.Errorf("unsupported manifest schema %q", m.Schema)
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("manifest files are required")
	}
	if len(m.Pairs) == 0 {
		return fmt.Errorf("manifest certificate pairs are required")
	}

	files := make(map[string]struct{}, len(m.Files))
	kinds := make(map[string]string, len(m.Files))
	for index, file := range m.Files {
		if !SafeName(file.Name) {
			return fmt.Errorf("manifest files[%d] has unsafe name %q", index, file.Name)
		}
		if _, exists := files[file.Name]; exists {
			return fmt.Errorf("manifest file %q is duplicated", file.Name)
		}
		files[file.Name] = struct{}{}
		if file.Kind != KindCertificate && file.Kind != KindPrivateKey {
			return fmt.Errorf("manifest files[%d] has invalid kind %q", index, file.Kind)
		}
		kinds[file.Name] = file.Kind
		digest, err := hex.DecodeString(file.SHA256)
		if err != nil || len(digest) != sha256.Size || file.SHA256 != strings.ToLower(file.SHA256) {
			return fmt.Errorf("manifest file %q has invalid sha256", file.Name)
		}
		if file.Encoding != EncodingBase64 {
			return fmt.Errorf("manifest file %q has unsupported encoding %q", file.Name, file.Encoding)
		}
	}
	for index, pair := range m.Pairs {
		if _, ok := files[pair.Certificate]; !ok {
			return fmt.Errorf("manifest pairs[%d] references unknown certificate %q", index, pair.Certificate)
		}
		if _, ok := files[pair.PrivateKey]; !ok {
			return fmt.Errorf("manifest pairs[%d] references unknown private key %q", index, pair.PrivateKey)
		}
		if kinds[pair.Certificate] != KindCertificate || kinds[pair.PrivateKey] != KindPrivateKey {
			return fmt.Errorf("manifest pairs[%d] has mismatched file kinds", index)
		}
	}
	return nil
}

// HasPair reports whether the manifest declares the selected certificate pair.
func (m Manifest) HasPair(certificate, privateKey string) bool {
	for _, pair := range m.Pairs {
		if pair.Certificate == certificate && pair.PrivateKey == privateKey {
			return true
		}
	}
	return false
}

// ContentDigest computes the protocol digest over sorted raw file names and raw-content SHA-256 values.
func ContentDigest(files map[string][]byte) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(SchemaV5))
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

// Generation returns the content-addressed generation name for a digest.
func Generation(digest string) string {
	return "sha256-" + digest
}

// ValidateGeneration checks that a pointer names the expected content digest.
func ValidateGeneration(generation, digest string) error {
	if generation != Generation(digest) {
		return fmt.Errorf("generation %q does not match bundle digest %q", generation, digest)
	}
	return nil
}

func hash(value []byte) string {
	sum := sha256.Sum256(value)
	return hex.EncodeToString(sum[:])
}

// SafeName reports whether value is a single safe etcd path component.
func SafeName(value string) bool {
	if len(value) == 0 || len(value) > 128 {
		return false
	}
	for index := range len(value) {
		character := value[index]
		switch {
		case character >= 'a' && character <= 'z':
		case character >= 'A' && character <= 'Z':
		case character >= '0' && character <= '9':
		case index > 0 && (character == '.' || character == '_' || character == '-'):
		default:
			return false
		}
	}
	return value != "." && value != ".."
}
