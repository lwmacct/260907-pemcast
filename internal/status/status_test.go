package status

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/bundle"
	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/deploy"
	"github.com/lwmacct/260907-pemcast/internal/state"
)

func statusConfig(t *testing.T) (config.Config, string, string) {
	t.Helper()

	stateDir := filepath.Join(t.TempDir(), "state")
	outputRoot := filepath.Join(t.TempDir(), "tls")
	cfg := config.DefaultConfig()
	cfg.Agent.StateDir = stateDir
	cfg.Agent.Targets = []config.Target{{
		ID: "nginx",
		Output: config.Output{
			Root: outputRoot, CurrentLink: "current", DirectoryMode: config.FileMode("0700"),
			Mappings: []config.FileMapping{
				{Remote: "cert.pem", Local: "cert.pem", Mode: config.FileMode("0644")},
				{Remote: "key.pem", Local: "key.pem", Mode: config.FileMode("0600")},
			},
		},
		Validation: config.Validation{Certificate: "cert.pem", PrivateKey: "key.pem"},
	}}
	return cfg, stateDir, outputRoot
}

func TestBuildReportsMissingStateAndCurrentWithoutWriting(t *testing.T) {
	cfg, _, outputRoot := statusConfig(t)
	report := Build(cfg)

	require.Equal(t, cfg.Agent.StateDir, report.StateDir)
	require.Len(t, report.Targets, 1)
	require.Equal(t, "missing", report.Targets[0].StateStatus)
	require.Equal(t, "missing", report.Targets[0].Current.Status)
	require.Empty(t, report.Targets[0].Current.Error)
	require.NoDirExists(t, cfg.Agent.StateDir)
	require.NoDirExists(t, outputRoot)
}

func TestBuildReportsActiveTarget(t *testing.T) {
	cfg, stateDir, _ := statusConfig(t)
	target := cfg.Agent.Targets[0]
	store, err := state.New(stateDir)
	require.NoError(t, err)
	activatedAt := time.Now().UTC().Round(0)
	require.NoError(t, store.Save(target.ID, state.Target{
		Generation: "g1", Digest: "digest", Revision: 42, ActivatedAt: activatedAt,
	}))
	material := &bundle.Material{
		Digest: "digest",
		Files: map[string][]byte{
			"cert.pem": []byte("certificate"), "key.pem": []byte("private-key"),
		},
	}
	_, err = deploy.New().Activate(material, target.Output)
	require.NoError(t, err)

	report := Build(cfg)
	require.Equal(t, "present", report.Targets[0].StateStatus)
	require.Equal(t, "g1", report.Targets[0].State.Generation)
	require.Equal(t, "active", report.Targets[0].Current.Status)
	require.Equal(t, "digest", report.Targets[0].Current.LocalDigest)
	require.Contains(t, report.Targets[0].Current.ReleaseDir, "sha256-digest")

	data, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContains(t, string(data), "private-key")
	require.NotContains(t, string(data), "certificate")
}

func TestBuildReportsCorruptStateAndCurrent(t *testing.T) {
	cfg, stateDir, outputRoot := statusConfig(t)
	require.NoError(t, os.MkdirAll(stateDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(stateDir, "nginx.json"), []byte("{invalid"), 0o600))
	require.NoError(t, os.MkdirAll(outputRoot, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(outputRoot, "current"), nil, 0o600))

	report := Build(cfg)
	require.Equal(t, "error", report.Targets[0].StateStatus)
	require.NotEmpty(t, report.Targets[0].StateError)
	require.Equal(t, "invalid", report.Targets[0].Current.Status)
	require.NotEmpty(t, report.Targets[0].Current.Error)
}
