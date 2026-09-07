package config

import (
	"fmt"
	"io/fs"
	"strconv"
	"strings"
)

// FileMode is a JSON/CLI-friendly octal file permission.
type FileMode string

// ParseFileMode parses an octal permission such as 0600 or 755.
func ParseFileMode(value string) (FileMode, error) {
	if _, err := parseFileMode(value); err != nil {
		return "", err
	}
	return FileMode(normalizeFileMode(value)), nil
}

func parseFileMode(value string) (fs.FileMode, error) {
	trimmed := strings.TrimSpace(value)
	trimmed = strings.TrimPrefix(trimmed, "0o")
	if trimmed == "" {
		return 0, fmt.Errorf("file mode is empty")
	}
	parsed, err := strconv.ParseUint(trimmed, 8, 32)
	if err != nil {
		return 0, fmt.Errorf("parse octal file mode %q: %w", value, err)
	}
	mode := fs.FileMode(parsed)
	if mode&^fs.ModePerm != 0 {
		return 0, fmt.Errorf("file mode %q contains non-permission bits", value)
	}
	return mode, nil
}

func normalizeFileMode(value string) string {
	trimmed := strings.TrimPrefix(strings.TrimSpace(value), "0o")
	parsed, _ := strconv.ParseUint(trimmed, 8, 32)
	return fmt.Sprintf("%04o", fs.FileMode(parsed).Perm())
}

func (m FileMode) String() string { return string(m) }

// Perm returns the filesystem permission represented by m.
func (m FileMode) Perm() fs.FileMode {
	mode, _ := parseFileMode(string(m))
	return mode.Perm()
}

func (m FileMode) validate() error {
	_, err := parseFileMode(string(m))
	return err
}
