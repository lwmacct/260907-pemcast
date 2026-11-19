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
)

func statusConfig(t *testing.T) (config.Config, string) {
	t.Helper()

	outputRoot := filepath.Join(t.TempDir(), "tls")
	cfg := config.DefaultConfig()
	cfg.Agent.Targets = []config.Target{{
		ID:   "nginx",
		Etcd: "default",
		Type: bundle.TypeTLSServer,
		Output: config.Output{Root: outputRoot, Files: map[string]config.OutputFile{
			"certificate-chain": {Name: bundle.NameCertificateChain, Mode: config.FileMode("0644")},
			"private-key":       {Name: bundle.NamePrivateKey, Mode: config.FileMode("0600")},
		}},
	}}
	cfg.Agent.Etcds = []config.Source{{
		Name: "default", Prefix: "/pemcast", Endpoints: []string{"http://127.0.0.1:1"},
		DialTimeout: time.Second, RequestTimeout: time.Second,
	}}
	return cfg, outputRoot
}

func TestBuildReportsMissingCurrentWithoutWriting(t *testing.T) {
	cfg, outputRoot := statusConfig(t)
	report := Build(cfg)

	require.Len(t, report.Targets, 1)
	require.Equal(t, "default", report.Targets[0].Etcd)
	require.Equal(t, "missing", report.Targets[0].Current.Status)
	require.Empty(t, report.Targets[0].Current.Error)
	require.NoDirExists(t, outputRoot)
}

func TestBuildReportsActiveTarget(t *testing.T) {
	cfg, _ := statusConfig(t)
	target := cfg.Agent.Targets[0]
	material := &bundle.Material{
		Digest: "digest",
		Files: map[string][]byte{
			bundle.NameCertificateChain: []byte("certificate"), bundle.NamePrivateKey: []byte("private-key"),
		},
	}
	_, err := deploy.New().Activate(material, target.Output)
	require.NoError(t, err)

	report := Build(cfg)
	require.Equal(t, "active", report.Targets[0].Current.Status)
	require.Equal(t, "digest", report.Targets[0].Current.LocalDigest)
	require.Contains(t, report.Targets[0].Current.ReleaseDir, "sha256-digest")

	data, err := json.Marshal(report)
	require.NoError(t, err)
	require.NotContains(t, string(data), `"data"`)
}

func TestBuildReportsInvalidCurrent(t *testing.T) {
	cfg, outputRoot := statusConfig(t)
	require.NoError(t, os.MkdirAll(outputRoot, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(outputRoot, "current"), nil, 0o600))

	report := Build(cfg)
	require.Equal(t, "invalid", report.Targets[0].Current.Status)
	require.NotEmpty(t, report.Targets[0].Current.Error)
}
