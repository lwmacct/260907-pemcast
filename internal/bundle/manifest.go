// Package bundle defines and validates the immutable pemcast/v1 bundle format.
package bundle

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json/v2"
	"fmt"
	"strings"
)

const SchemaV1 = "pemcast/v1"

// Manifest describes the expected content of an immutable bundle generation.
type Manifest struct {
	Schema string         `json:"schema"`
	Files  []ManifestFile `json:"files"`
	Pairs  []Pair         `json:"pairs"`
}

// ManifestFile authenticates one etcd value by content digest.
type ManifestFile struct {
	Name   string `json:"name"`
	Kind   string `json:"kind"`
	SHA256 string `json:"sha256"`
}

// Pair declares a certificate and private key that must parse together.
type Pair struct {
	Certificate string `json:"certificate"`
	PrivateKey  string `json:"private-key"`
}

// Material is a fully fetched and verified immutable bundle.
type Material struct {
	TargetID   string
	Generation string
	Revision   int64
	Manifest   Manifest
	Files      map[string][]byte
	Digest     string
}

// ParseManifest strictly parses and validates a manifest document.
func ParseManifest(data []byte) (Manifest, error) {
	var manifest Manifest
	if err := json.Unmarshal(data, &manifest, json.RejectUnknownMembers(true)); err != nil {
		return Manifest{}, fmt.Errorf("decode manifest: %w", err)
	}
	if err := manifest.Validate(); err != nil {
		return Manifest{}, err
	}
	return manifest, nil
}

// Validate checks manifest structure and digests without interpreting TLS data.
func (m Manifest) Validate() error {
	if m.Schema != SchemaV1 {
		return fmt.Errorf("unsupported manifest schema %q", m.Schema)
	}
	if len(m.Files) == 0 {
		return fmt.Errorf("manifest files are required")
	}
	if len(m.Pairs) == 0 {
		return fmt.Errorf("manifest certificate pairs are required")
	}
	files := make(map[string]struct{}, len(m.Files))
	for index, file := range m.Files {
		if !SafeName(file.Name) {
			return fmt.Errorf("manifest files[%d] has unsafe name %q", index, file.Name)
		}
		if _, exists := files[file.Name]; exists {
			return fmt.Errorf("manifest file %q is duplicated", file.Name)
		}
		files[file.Name] = struct{}{}
		digest, err := hex.DecodeString(file.SHA256)
		if err != nil || len(digest) != sha256.Size || file.SHA256 != strings.ToLower(file.SHA256) {
			return fmt.Errorf("manifest file %q has invalid sha256", file.Name)
		}
	}
	for index, pair := range m.Pairs {
		if _, ok := files[pair.Certificate]; !ok {
			return fmt.Errorf("manifest pairs[%d] references unknown certificate %q", index, pair.Certificate)
		}
		if _, ok := files[pair.PrivateKey]; !ok {
			return fmt.Errorf("manifest pairs[%d] references unknown private key %q", index, pair.PrivateKey)
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
