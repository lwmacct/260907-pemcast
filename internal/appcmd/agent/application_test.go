package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/config"
)

func dryRunConfig(t *testing.T) config.Config {
	t.Helper()

	cfg := config.ExampleConfig()
	cfg.Agent.StateDir = filepath.Join(t.TempDir(), "state")
	cfg.Agent.DryRun = true
	cfg.Agent.Etcd.Endpoints = []string{"http://127.0.0.1:1"}
	cfg.Agent.Etcd.DialTimeout = 10 * time.Millisecond
	cfg.Agent.Etcd.RequestTimeout = 10 * time.Millisecond
	cfg.Agent.Targets[0].Output.Root = filepath.Join(t.TempDir(), "tls")
	return cfg
}

func TestNewDryRunDoesNotCreateLocalDirectories(t *testing.T) {
	cfg := dryRunConfig(t)

	application, err := New(cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	defer func() { require.NoError(t, application.Close()) }()

	require.NoDirExists(t, cfg.Agent.StateDir)
	require.NoDirExists(t, cfg.Agent.Targets[0].Output.Root)

	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer cancel()
	_ = application.Run(ctx)

	require.NoDirExists(t, cfg.Agent.StateDir)
	require.NoDirExists(t, cfg.Agent.Targets[0].Output.Root)
}

func TestNewCreatesStateDirectoryOutsideDryRun(t *testing.T) {
	cfg := dryRunConfig(t)
	cfg.Agent.DryRun = false

	application, err := New(cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	defer func() { require.NoError(t, application.Close()) }()

	require.DirExists(t, cfg.Agent.StateDir)
	require.FileExists(t, filepath.Join(cfg.Agent.Targets[0].Output.Root, ".pemcast", "agent.lock"))
}

func TestNewRejectsCompetingProcessForSameOutputRoot(t *testing.T) {
	cfg := dryRunConfig(t)
	cfg.Agent.DryRun = false

	first, err := New(cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	defer func() { require.NoError(t, first.Close()) }()

	cfg.Agent.StateDir = filepath.Join(t.TempDir(), "second-state")
	_, err = New(cfg, slog.New(slog.DiscardHandler))
	require.ErrorContains(t, err, "already managed")

	require.NoError(t, first.Close())
	second, err := New(cfg, slog.New(slog.DiscardHandler))
	require.NoError(t, err)
	require.NoError(t, second.Close())
}
