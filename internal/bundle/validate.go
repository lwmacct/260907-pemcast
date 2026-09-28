package bundle

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"time"
)

// ValidateKeyPair verifies that certificate and key form a valid TLS pair and returns the leaf certificate.
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
		return fmt.Errorf("TLS leaf certificate is not valid before %s", leaf.NotAfter.Format(time.RFC3339))
	}
	if rejectExpired && !leaf.NotAfter.After(now) {
		return fmt.Errorf("TLS leaf certificate expired at %s", leaf.NotAfter.Format(time.RFC3339))
	}
	if minimum > 0 && leaf.NotAfter.Sub(now) < minimum {
		return fmt.Errorf("TLS leaf certificate validity %s is less than required %s", leaf.NotAfter.Sub(now).Round(time.Second), minimum)
	}
	return nil
}
