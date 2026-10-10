package bundle

import (
	"fmt"
	"time"
)

// Policy controls target-local validity and hostname checks.
type Policy struct {
	Now             time.Time
	RejectExpired   bool
	MinimumValidity time.Duration
	ServerNames     []string
}

// ValidatePolicy applies one target's local certificate policy to decoded material.
func ValidatePolicy(material *Material, policy Policy) error {
	if material == nil {
		return fmt.Errorf("bundle material is nil")
	}
	if policy.Now.IsZero() {
		policy.Now = time.Now()
	}
	if len(material.Certificates) == 0 {
		return fmt.Errorf("bundle contains no parsed certificates")
	}
	for index, certificate := range material.Certificates {
		if certificate == nil {
			return fmt.Errorf("bundle certificate %d is nil", index)
		}
		if certificate.NotBefore.After(policy.Now) {
			return fmt.Errorf(
				"bundle certificate %d is not valid before %s",
				index, certificate.NotBefore.Format(time.RFC3339),
			)
		}
		if policy.RejectExpired && !certificate.NotAfter.After(policy.Now) {
			return fmt.Errorf(
				"bundle certificate %d expired at %s",
				index, certificate.NotAfter.Format(time.RFC3339),
			)
		}
		if policy.MinimumValidity > 0 && certificate.NotAfter.Sub(policy.Now) < policy.MinimumValidity {
			return fmt.Errorf(
				"bundle certificate %d validity %s is less than required %s",
				index, certificate.NotAfter.Sub(policy.Now).Round(time.Second), policy.MinimumValidity,
			)
		}
	}
	if material.Manifest.Type == TypeTLSServer {
		for index, name := range policy.ServerNames {
			if name == "" {
				return fmt.Errorf("server name %d is empty", index)
			}
			if material.Leaf == nil {
				return fmt.Errorf("TLS server bundle has no leaf certificate")
			}
			if err := material.Leaf.VerifyHostname(name); err != nil {
				return fmt.Errorf("certificate does not match server name %q: %w", name, err)
			}
		}
	}
	return nil
}
