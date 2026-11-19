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
	expectedRoles := expectedOutputRoles(t.Type)
	if len(expectedRoles) == 0 {
		return fmt.Errorf("type must be one of tls-server, tls-client, or trust")
	}
	if len(t.Output.Files) != len(expectedRoles) {
		return fmt.Errorf("type %q requires exactly %d output files", t.Type, len(expectedRoles))
	}
	for role := range t.Output.Files {
		if _, ok := expectedRoles[role]; !ok {
			return fmt.Errorf("output.files role %q is not valid for type %q", role, t.Type)
		}
	}
	names := make(map[string]struct{}, len(t.Output.Files))
	for role, file := range t.Output.Files {
		if !safeBundleName(file.Name) {
			return fmt.Errorf("output.files[%s].name must be a safe file name", role)
		}
		if file.Name == DefaultCurrentLink || file.Name == ".pemcast" {
			return fmt.Errorf("output.files[%s].name %q is reserved", role, file.Name)
		}
		if err := file.Mode.validate(); err != nil {
			return fmt.Errorf("output.files[%s].mode is invalid: %w", role, err)
		}
		if file.Mode.Perm() == 0 {
			return fmt.Errorf("output.files[%s].mode must not be zero", role)
		}
		if _, exists := names[file.Name]; exists {
			return fmt.Errorf("output file name %q is duplicated", file.Name)
		}
		names[file.Name] = struct{}{}
	}
	if t.Validation.MinimumValidity <= 0 {
		return fmt.Errorf("validation.minimum-validity must be positive")
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

func expectedOutputRoles(bundleType string) map[string]struct{} {
	switch bundleType {
	case bundle.TypeTLSServer, bundle.TypeTLSClient:
		return map[string]struct{}{"certificate-chain": {}, "private-key": {}}
	case bundle.TypeTrust:
		return map[string]struct{}{"ca-bundle": {}}
	default:
		return nil
	}
}

func normalizeOutputFiles(bundleType string, files map[string]OutputFile) map[string]OutputFile {
	defaults := defaultOutputFiles(bundleType)
	if defaults == nil {
		if files == nil {
			return nil
		}
		result := make(map[string]OutputFile, len(files))
		for role, file := range files {
			result[role] = file
		}
		return result
	}
	result := make(map[string]OutputFile, len(defaults))
	for role, file := range files {
		if _, exists := defaults[role]; !exists {
			result[role] = file
		}
	}
	for role, resolved := range defaults {
		if override, exists := files[role]; exists {
			if override.Name != "" {
				resolved.Name = override.Name
			}
			if override.Mode != "" {
				resolved.Mode = override.Mode
			}
		}
		result[role] = resolved
	}
	return result
}

func defaultOutputFiles(bundleType string) map[string]OutputFile {
	switch bundleType {
	case bundle.TypeTLSServer, bundle.TypeTLSClient:
		return map[string]OutputFile{
			"certificate-chain": {Name: bundle.NameCertificateChain, Mode: FileMode("0644")},
			"private-key":       {Name: bundle.NamePrivateKey, Mode: FileMode("0600")},
		}
	case bundle.TypeTrust:
		return map[string]OutputFile{
			"ca-bundle": {Name: bundle.NameCABundle, Mode: FileMode("0644")},
		}
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
