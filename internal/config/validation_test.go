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

func TestValidateRejectsUncleanRoot(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agent.StateDir = t.TempDir()
	cfg.Agent.Targets = []Target{validTarget("nginx", filepath.Join(t.TempDir(), "tls")+string(filepath.Separator))}
	require.ErrorContains(t, cfg.Validate(), "clean, absolute, non-root path")
}

func TestValidateRejectsInvalidHookEnvironmentName(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agent.StateDir = t.TempDir()
	target := validTarget("nginx", filepath.Join(t.TempDir(), "tls"))
	target.Hook.PassEnvironment = []string{"BAD-NAME"}
	cfg.Agent.Targets = []Target{target}
	require.ErrorContains(t, cfg.Validate(), "valid environment variable name")
}

func TestValidateRejectsOverlappingOutputRoots(t *testing.T) {
	parent := filepath.Join(t.TempDir(), "tls")
	cfg := DefaultConfig()
	cfg.Agent.StateDir = t.TempDir()
	cfg.Agent.Targets = []Target{
		validTarget("one", parent),
		validTarget("two", filepath.Join(parent, "nested")),
	}
	require.ErrorContains(t, cfg.Validate(), "overlap")

	cfg.Agent.Targets[1].Output.Root = parent + "-two"
	require.NoError(t, cfg.Validate())
}

func TestValidateCommonRejectsUnsafeEtcdPrefix(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Agent.Etcd.Prefix = "../unsafe"

	err := cfg.Agent.ValidateCommon()
	require.ErrorContains(t, err, "etcd prefix")
}

func validTarget(id, root string) Target {
	return Target{
		ID: id, DeletePolicy: "retain",
		Output: Output{
			Root: root, CurrentLink: "current", RetainReleases: 1, DirectoryMode: FileMode("0700"),
			Mappings: []FileMapping{
				{Remote: "cert.pem", Local: "cert.pem", Mode: FileMode("0644")},
				{Remote: "key.pem", Local: "key.pem", Mode: FileMode("0600")},
			},
		},
		Validation: Validation{Certificate: "cert.pem", PrivateKey: "key.pem"},
	}
}
