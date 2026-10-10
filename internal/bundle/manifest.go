// Package bundle defines and validates the immutable pemcast/v6 bundle format.
package bundle

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json/v2"
	"encoding/pem"
	"fmt"
	"sort"
	"strings"
)

const SchemaV6 = "pemcast/v6"

const (
	TypeTLSServer = "tls-server"
	TypeTLSClient = "tls-client"
	TypeTrust     = "trust"

	RoleCertificateChain = "certificate-chain"
	RolePrivateKey       = "private-key"
	RoleCACertificate    = "ca-certificate"

	NameCertificateChain = "fullchain.pem"
	NamePrivateKey       = "privkey.pem"
	NameCABundle         = "ca-bundle.pem"

	EncodingBase64 = "base64"
)

// MaxEncodedBundleBytes bounds the complete single etcd value. TLS material is
// small; larger artifacts belong in object storage rather than etcd.
const MaxEncodedBundleBytes = 1 << 20

// Manifest is one complete, immutable certificate bundle stored under a single etcd key.
type Manifest struct {
	Schema string         `json:"schema"`
	Type   string         `json:"type"`
	Files  []ManifestFile `json:"files"`
}

// ManifestFile contains one inline file, its semantic role, and raw-content digest.
type ManifestFile struct {
	Name     string `json:"name"`
	Role     string `json:"role"`
	SHA256   string `json:"sha256"`
	Encoding string `json:"encoding"`
	Data     string `json:"data"`
}

// Material is a fully decoded and verified immutable bundle.
type Material struct {
	TargetID     string
	Generation   string
	Revision     int64
	Manifest     Manifest
	Files        map[string][]byte
	Digest       string
	Certificates []*x509.Certificate
	Leaf         *x509.Certificate
}

// NewTLSManifest builds a deterministic server or client identity manifest.
func NewTLSManifest(bundleType string, certificate, privateKey []byte) (Manifest, string) {
	files := []ManifestFile{
		{
			Name: NameCertificateChain, Role: RoleCertificateChain, SHA256: hash(certificate),
			Encoding: EncodingBase64, Data: base64.StdEncoding.EncodeToString(certificate),
		},
		{
			Name: NamePrivateKey, Role: RolePrivateKey, SHA256: hash(privateKey),
			Encoding: EncodingBase64, Data: base64.StdEncoding.EncodeToString(privateKey),
		},
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	manifest := Manifest{Schema: SchemaV6, Type: bundleType, Files: files}
	return manifest, ManifestDigest(manifest, map[string][]byte{
		NameCertificateChain: certificate,
		NamePrivateKey:       privateKey,
	})
}

// NewTrustManifest builds a deterministic CA trust bundle.
func NewTrustManifest(certificate []byte) (Manifest, string) {
	manifest := Manifest{
		Schema: SchemaV6,
		Type:   TypeTrust,
		Files: []ManifestFile{{
			Name: NameCABundle, Role: RoleCACertificate, SHA256: hash(certificate),
			Encoding: EncodingBase64, Data: base64.StdEncoding.EncodeToString(certificate),
		}},
	}
	return manifest, ManifestDigest(manifest, map[string][]byte{NameCABundle: certificate})
}

// Encode validates and serializes one complete bundle value.
func Encode(manifest Manifest) ([]byte, error) {
	_, err := decodeManifestFiles(manifest)
	if err != nil {
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
	files, err := decodeManifestFiles(manifest)
	if err != nil {
		return Manifest{}, nil, "", err
	}
	return manifest, files, ManifestDigest(manifest, files), nil
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
	if m.Schema != SchemaV6 {
		return fmt.Errorf("unsupported manifest schema %q", m.Schema)
	}
	expected, ok := expectedFiles(m.Type)
	if !ok {
		return fmt.Errorf("unsupported bundle type %q", m.Type)
	}
	if len(m.Files) != len(expected) {
		return fmt.Errorf("bundle type %q requires exactly %d files", m.Type, len(expected))
	}

	seen := make(map[string]string, len(m.Files))
	for index, file := range m.Files {
		if !SafeName(file.Name) {
			return fmt.Errorf("manifest files[%d] has unsafe name %q", index, file.Name)
		}
		if _, exists := seen[file.Name]; exists {
			return fmt.Errorf("manifest file %q is duplicated", file.Name)
		}
		if file.Role != expected[file.Name] {
			return fmt.Errorf("manifest file %q has invalid role %q for bundle type %q", file.Name, file.Role, m.Type)
		}
		seen[file.Name] = file.Role
		digest, err := hex.DecodeString(file.SHA256)
		if err != nil || len(digest) != sha256.Size || file.SHA256 != strings.ToLower(file.SHA256) {
			return fmt.Errorf("manifest file %q has invalid sha256", file.Name)
		}
		if file.Encoding != EncodingBase64 {
			return fmt.Errorf("manifest file %q has unsupported encoding %q", file.Name, file.Encoding)
		}
	}
	return nil
}

// ValidateFiles verifies decoded bytes against the bundle's semantic type.
func (m Manifest) ValidateFiles(files map[string][]byte) ([]*x509.Certificate, *x509.Certificate, error) {
	if err := m.Validate(); err != nil {
		return nil, nil, err
	}
	if len(files) != len(m.Files) {
		return nil, nil, fmt.Errorf("decoded file set does not match manifest")
	}
	for _, declared := range m.Files {
		content, ok := files[declared.Name]
		if !ok || hash(content) != declared.SHA256 {
			return nil, nil, fmt.Errorf("decoded file %q does not match manifest", declared.Name)
		}
	}

	switch m.Type {
	case TypeTLSServer, TypeTLSClient:
		return validateIdentity(m.Type, files[NameCertificateChain], files[NamePrivateKey])
	case TypeTrust:
		certificates, err := validateTrust(files[NameCABundle])
		return certificates, nil, err
	default:
		return nil, nil, fmt.Errorf("unsupported bundle type %q", m.Type)
	}
}

// ManifestDigest computes the semantic protocol digest over bundle type and each file's name, role, and raw content.
func ManifestDigest(manifest Manifest, files map[string][]byte) string {
	hasher := sha256.New()
	_, _ = hasher.Write([]byte(manifest.Schema))
	_, _ = hasher.Write([]byte{0})
	_, _ = hasher.Write([]byte(manifest.Type))
	_, _ = hasher.Write([]byte{0})

	declarations := make(map[string]string, len(manifest.Files))
	for _, file := range manifest.Files {
		declarations[file.Name] = file.Role
	}
	names := make([]string, 0, len(files))
	for name := range files {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sum := sha256.Sum256(files[name])
		_, _ = hasher.Write([]byte(name))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write([]byte(declarations[name]))
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

func decodeManifestFiles(manifest Manifest) (map[string][]byte, error) {
	if err := manifest.Validate(); err != nil {
		return nil, err
	}
	files := make(map[string][]byte, len(manifest.Files))
	for _, declared := range manifest.Files {
		content, err := base64.StdEncoding.DecodeString(declared.Data)
		if err != nil {
			return nil, fmt.Errorf("bundle file %q has invalid base64 data", declared.Name)
		}
		if hash(content) != declared.SHA256 {
			return nil, fmt.Errorf("bundle file %q sha256 mismatch", declared.Name)
		}
		files[declared.Name] = content
	}
	if _, _, err := manifest.ValidateFiles(files); err != nil {
		return nil, err
	}
	return files, nil
}

func expectedFiles(bundleType string) (map[string]string, bool) {
	switch bundleType {
	case TypeTLSServer, TypeTLSClient:
		return map[string]string{
			NameCertificateChain: RoleCertificateChain,
			NamePrivateKey:       RolePrivateKey,
		}, true
	case TypeTrust:
		return map[string]string{NameCABundle: RoleCACertificate}, true
	default:
		return nil, false
	}
}

func validateIdentity(bundleType string, certificatePEM, privateKeyPEM []byte) ([]*x509.Certificate, *x509.Certificate, error) {
	certificates, err := parseCertificates(certificatePEM)
	if err != nil {
		return nil, nil, fmt.Errorf("certificate chain: %w", err)
	}
	leaf := certificates[0]
	if leaf.IsCA {
		return nil, nil, fmt.Errorf("certificate chain leaf is a CA certificate")
	}
	for index := 1; index < len(certificates); index++ {
		if !certificates[index].IsCA {
			return nil, nil, fmt.Errorf("certificate chain position %d is not a CA certificate", index)
		}
	}
	for index := 0; index+1 < len(certificates); index++ {
		if err := certificates[index].CheckSignatureFrom(certificates[index+1]); err != nil {
			return nil, nil, fmt.Errorf("certificate chain position %d is not signed by position %d", index, index+1)
		}
	}

	normalizedKey, err := normalizePrivateKey(privateKeyPEM)
	if err != nil {
		return nil, nil, err
	}
	pair, err := tls.X509KeyPair(certificatePEM, normalizedKey)
	if err != nil {
		return nil, nil, fmt.Errorf("parse TLS key pair: %w", err)
	}
	if pair.Leaf == nil || !pair.Leaf.Equal(leaf) {
		return nil, nil, fmt.Errorf("TLS leaf does not match parsed certificate chain")
	}
	if err := validatePublicKey(leaf.PublicKey); err != nil {
		return nil, nil, err
	}

	var wanted x509.ExtKeyUsage
	switch bundleType {
	case TypeTLSServer:
		wanted = x509.ExtKeyUsageServerAuth
	case TypeTLSClient:
		wanted = x509.ExtKeyUsageClientAuth
	}
	if len(leaf.ExtKeyUsage) > 0 && !containsKeyUsage(leaf.ExtKeyUsage, wanted) {
		return nil, nil, fmt.Errorf("certificate leaf does not permit %s usage", bundleType)
	}
	return certificates, leaf, nil
}

func validateTrust(certificatePEM []byte) ([]*x509.Certificate, error) {
	certificates, err := parseCertificates(certificatePEM)
	if err != nil {
		return nil, fmt.Errorf("CA bundle: %w", err)
	}
	for index, certificate := range certificates {
		if !certificate.IsCA {
			return nil, fmt.Errorf("CA bundle certificate %d is not a CA certificate", index)
		}
	}
	return certificates, nil
}

func parseCertificates(data []byte) ([]*x509.Certificate, error) {
	rest := data
	certificates := []*x509.Certificate{}
	for {
		block, remainder := pem.Decode(rest)
		if block == nil {
			if strings.TrimSpace(string(rest)) != "" {
				return nil, fmt.Errorf("contains non-PEM certificate data")
			}
			break
		}
		if block.Type != "CERTIFICATE" {
			return nil, fmt.Errorf("contains %q PEM block", block.Type)
		}
		certificate, err := x509.ParseCertificate(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("parse PEM certificate: %w", err)
		}
		certificates = append(certificates, certificate)
		rest = remainder
	}
	if len(certificates) == 0 {
		return nil, fmt.Errorf("contains no certificates")
	}
	return certificates, nil
}

func normalizePrivateKey(data []byte) ([]byte, error) {
	block, rest := pem.Decode(data)
	if block == nil {
		return nil, fmt.Errorf("private key contains no PEM data")
	}
	if block.Type != "PRIVATE KEY" && !strings.HasSuffix(block.Type, " PRIVATE KEY") {
		return nil, fmt.Errorf("private key contains invalid PEM block type %q", block.Type)
	}
	if strings.TrimSpace(string(rest)) != "" {
		return nil, fmt.Errorf("private key contains multiple PEM blocks or trailing data")
	}
	for name := range block.Headers {
		if name == "Proc-Type" || name == "DEK-Info" {
			return nil, fmt.Errorf("encrypted private keys are not supported")
		}
	}
	return pem.EncodeToMemory(block), nil
}

func validatePublicKey(public any) error {
	switch key := public.(type) {
	case *rsa.PublicKey:
		if key.N.BitLen() < 2048 {
			return fmt.Errorf("RSA public key is shorter than 2048 bits")
		}
	case *ecdsa.PublicKey:
		switch key.Curve {
		case elliptic.P256(), elliptic.P384(), elliptic.P521():
		default:
			return fmt.Errorf("unsupported ECDSA curve %q", key.Curve)
		}
	case ed25519.PublicKey:
	default:
		return fmt.Errorf("unsupported public key type %T", public)
	}
	return nil
}

func containsKeyUsage(usages []x509.ExtKeyUsage, wanted x509.ExtKeyUsage) bool {
	for _, usage := range usages {
		if usage == wanted {
			return true
		}
	}
	return false
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
