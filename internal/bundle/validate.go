package bundle

import (
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"fmt"
	"sort"
	"time"
)

// VerifyFiles checks every fetched value against the manifest and computes a
// stable digest over the whole content set.
func VerifyFiles(manifest Manifest, files map[string][]byte) (string, error) {
	hasher := sha256.New()
	ordered := append([]ManifestFile(nil), manifest.Files...)
	sort.Slice(ordered, func(i, j int) bool { return ordered[i].Name < ordered[j].Name })
	declaredNames := make(map[string]struct{}, len(ordered))
	for _, declared := range ordered {
		declaredNames[declared.Name] = struct{}{}
		content, ok := files[declared.Name]
		if !ok {
			return "", fmt.Errorf("bundle file %q is missing", declared.Name)
		}
		sum := sha256.Sum256(content)
		if hex.EncodeToString(sum[:]) != declared.SHA256 {
			return "", fmt.Errorf("bundle file %q sha256 mismatch", declared.Name)
		}
		_, _ = hasher.Write([]byte(declared.Name))
		_, _ = hasher.Write([]byte{0})
		_, _ = hasher.Write(sum[:])
	}
	for name := range files {
		if _, ok := declaredNames[name]; !ok {
			return "", fmt.Errorf("bundle file %q is not declared in the manifest", name)
		}
	}
	return hex.EncodeToString(hasher.Sum(nil)), nil
}

// ValidateKeyPair verifies that certificate and key form a valid TLS pair and
// returns the parsed leaf certificate.
func ValidateKeyPair(certPEM, keyPEM []byte) (*x509.Certificate, error) {
	pair, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		return nil, fmt.Errorf("parse TLS key pair: %w", err)
	}
	if len(pair.Certificate) == 0 {
		return nil, fmt.Errorf("TLS certificate chain is empty")
	}
	leaf, err := x509.ParseCertificate(pair.Certificate[0])
	if err != nil {
		return nil, fmt.Errorf("parse TLS leaf certificate: %w", err)
	}
	return leaf, nil
}

// ValidateValidity applies target-specific leaf lifetime policy.
func ValidateValidity(leaf *x509.Certificate, now time.Time, rejectExpired bool, minimum time.Duration) error {
	if leaf == nil {
		return fmt.Errorf("TLS leaf certificate is nil")
	}
	if leaf.NotBefore.After(now) {
		return fmt.Errorf("TLS leaf certificate is not valid before %s", leaf.NotBefore.Format(time.RFC3339))
	}
	if rejectExpired && !leaf.NotAfter.After(now) {
		return fmt.Errorf("TLS leaf certificate expired at %s", leaf.NotAfter.Format(time.RFC3339))
	}
	if minimum > 0 && leaf.NotAfter.Sub(now) < minimum {
		return fmt.Errorf("TLS leaf certificate validity %s is less than required %s", leaf.NotAfter.Sub(now).Round(time.Second), minimum)
	}
	return nil
}
