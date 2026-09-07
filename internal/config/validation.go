package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

func (t Target) Validate() error {
	if strings.TrimSpace(t.ID) == "" || strings.Contains(t.ID, "/") {
		return fmt.Errorf("id must be non-empty and contain no slash")
	}
	if t.DeletePolicy != "retain" && t.DeletePolicy != "fail" {
		return fmt.Errorf("delete-policy must be retain or fail")
	}
	if !filepath.IsAbs(t.Output.Root) {
		return fmt.Errorf("output.root must be absolute")
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
	if _, ok := remote[t.Validation.Certificate]; !ok {
		return fmt.Errorf("validation.certificate must reference an output mapping")
	}
	if _, ok := remote[t.Validation.PrivateKey]; !ok {
		return fmt.Errorf("validation.private-key must reference an output mapping")
	}
	if t.Validation.MinimumValidity < 0 {
		return fmt.Errorf("validation.minimum-validity must not be negative")
	}
	if t.Hook.Timeout < 0 {
		return fmt.Errorf("hook.timeout must not be negative")
	}
	return nil
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
