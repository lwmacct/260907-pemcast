package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
)

func (t Target) Validate() error {
	if strings.TrimSpace(t.ID) == "" || strings.Contains(t.ID, "/") {
		return fmt.Errorf("id must be non-empty and contain no slash")
	}
	if !validBundleType(t.Type) {
		return fmt.Errorf("type must be one of tls-server, tls-client, or trust")
	}
	if t.DeletePolicy != "retain" && t.DeletePolicy != "fail" {
		return fmt.Errorf("delete-policy must be retain or fail")
	}
	if !filepath.IsAbs(t.Output.Root) {
		return fmt.Errorf("output.root must be absolute")
	}
	if filepath.Clean(t.Output.Root) != t.Output.Root || t.Output.Root == string(filepath.Separator) {
		return fmt.Errorf("output.root must be a clean, absolute, non-root path")
	}
	if !safeBundleName(t.Output.CurrentLink) {
		return fmt.Errorf("output.current-link must be a safe file name")
	}
	if err := t.Output.DirectoryMode.validate(); err != nil {
		return fmt.Errorf("output.directory-mode is invalid: %w", err)
	}
	if t.Output.DirectoryMode.Perm() == 0 {
		return fmt.Errorf("output.directory-mode must not be zero")
	}
	if t.Output.RetainReleases < 0 {
		return fmt.Errorf("output.retain-releases must not be negative")
	}
	if len(t.Output.Mappings) == 0 {
		return fmt.Errorf("output.mappings is required")
	}
	remote := make(map[string]struct{}, len(t.Output.Mappings))
	local := make(map[string]struct{}, len(t.Output.Mappings))
	for index, mapping := range t.Output.Mappings {
		if !safeBundleName(mapping.Remote) {
			return fmt.Errorf("output.mappings[%d].remote must be a safe bundle file name", index)
		}
		if !safeRelativePath(mapping.Local) {
			return fmt.Errorf("output.mappings[%d].local must be a safe relative path", index)
		}
		if err := mapping.Mode.validate(); err != nil {
			return fmt.Errorf("output.mappings[%d].mode is invalid: %w", index, err)
		}
		if mapping.Mode.Perm() == 0 {
			return fmt.Errorf("output.mappings[%d].mode must not be zero", index)
		}
		if _, ok := remote[mapping.Remote]; ok {
			return fmt.Errorf("remote mapping %q is duplicated", mapping.Remote)
		}
		if _, ok := local[mapping.Local]; ok {
			return fmt.Errorf("local mapping %q is duplicated", mapping.Local)
		}
		remote[mapping.Remote] = struct{}{}
		local[mapping.Local] = struct{}{}
	}
	expected := expectedRemoteFiles(t.Type)
	if len(remote) != len(expected) {
		return fmt.Errorf("type %q requires exactly %d remote mappings", t.Type, len(expected))
	}
	for name := range expected {
		if _, ok := remote[name]; !ok {
			return fmt.Errorf("type %q is missing remote mapping %q", t.Type, name)
		}
	}
	if t.Validation.MinimumValidity < 0 {
		return fmt.Errorf("validation.minimum-validity must not be negative")
	}
	if t.Type != bundle.TypeTLSServer && len(t.Validation.ServerNames) > 0 {
		return fmt.Errorf("validation.server-names is only valid for tls-server targets")
	}
	seenServerName := make(map[string]struct{}, len(t.Validation.ServerNames))
	for index, name := range t.Validation.ServerNames {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("validation.server-names[%d] must not be empty", index)
		}
		if _, exists := seenServerName[name]; exists {
			return fmt.Errorf("validation server name %q is duplicated", name)
		}
		seenServerName[name] = struct{}{}
	}
	if t.Hook.Timeout < 0 {
		return fmt.Errorf("hook.timeout must not be negative")
	}
	for index, name := range t.Hook.PassEnvironment {
		if !validEnvironmentName(name) {
			return fmt.Errorf("hook.pass-environment[%d] must be a valid environment variable name", index)
		}
	}
	return nil
}

func validBundleType(value string) bool {
	return value == bundle.TypeTLSServer || value == bundle.TypeTLSClient || value == bundle.TypeTrust
}

func expectedRemoteFiles(bundleType string) map[string]struct{} {
	switch bundleType {
	case bundle.TypeTLSServer, bundle.TypeTLSClient:
		return map[string]struct{}{
			bundle.NameCertificateChain: {},
			bundle.NamePrivateKey:       {},
		}
	case bundle.TypeTrust:
		return map[string]struct{}{bundle.NameCABundle: {}}
	default:
		return nil
	}
}

func validEnvironmentName(name string) bool {
	if name == "" {
		return false
	}
	for index, character := range name {
		valid := character == '_' ||
			(character >= 'a' && character <= 'z') ||
			(character >= 'A' && character <= 'Z') ||
			(index > 0 && character >= '0' && character <= '9')
		if !valid {
			return false
		}
	}
	return true
}

func safeBundleName(value string) bool {
	return value != "" && value != "." && value != ".." && filepath.Base(value) == value && !strings.ContainsAny(value, `/\\`)
}

func safeRelativePath(value string) bool {
	if value == "" || filepath.IsAbs(value) || filepath.Clean(value) != value {
		return false
	}
	return value != "." && value != ".." && !strings.HasPrefix(value, ".."+string(filepath.Separator))
}
