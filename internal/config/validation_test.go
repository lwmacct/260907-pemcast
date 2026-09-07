package config

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateTarget(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agent.StateDir = t.TempDir()
	cfg.Agent.Targets = []Target{{
		ID: "nginx", DeletePolicy: "retain",
		Output: Output{
			Root: filepath.Join(t.TempDir(), "tls"), CurrentLink: "current", RetainReleases: 3, DirectoryMode: FileMode("0700"),
			Mappings: []FileMapping{{Remote: "cert.pem", Local: "cert.pem", Mode: FileMode("0644")}, {Remote: "key.pem", Local: "key.pem", Mode: FileMode("0600")}},
		},
		Validation: Validation{Certificate: "cert.pem", PrivateKey: "key.pem"},
	}}
	require.NoError(t, cfg.Validate())
	cfg.Agent.Targets[0].Output.Mappings[1].Local = "../key.pem"
	require.ErrorContains(t, cfg.Validate(), "safe relative path")
}
