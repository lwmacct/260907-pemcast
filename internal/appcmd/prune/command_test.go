package prune

import (
	"sync"
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/lwmacct/260907-pemcast/internal/config"
)

var (
	application = &cli.Command{
		Name:     "pemcast",
		Commands: []*cli.Command{Command},
	}
	configureOnce sync.Once
)

func TestPruneCommandRequiresExactlyOneMode(t *testing.T) {
	configureOnce.Do(func() { config.Manager.MustConfigure(application) })

	err := application.Run(t.Context(), []string{
		"pemcast", "prune", "--etcd-name", "default",
	})
	require.ErrorContains(t, err, "choose exactly one of --dry-run or --delete")

	err = application.Run(t.Context(), []string{
		"pemcast", "prune", "--etcd-name", "default", "--dry-run", "--delete",
	})
	require.ErrorContains(t, err, "choose exactly one of --dry-run or --delete")
}

func TestPruneCommandRequiresEtcdName(t *testing.T) {
	err := application.Run(t.Context(), []string{"pemcast", "prune", "--dry-run"})
	require.ErrorContains(t, err, "agent etcd name is required")
}
