package upgrade

import (
	"testing"

	"github.com/stretchr/testify/require"
	"github.com/urfave/cli/v3"

	"github.com/lwmacct/260907-pemcast/internal/config"
)

func TestUpgradeCommandRequiresEtcdName(t *testing.T) {
	application := &cli.Command{Name: "pemcast", Commands: []*cli.Command{Command}}
	config.Manager.MustConfigure(application)

	err := application.Run(t.Context(), []string{
		"pemcast", "upgrade", "--default-type", "tls-server", "--dry-run",
	})
	require.ErrorContains(t, err, "etcd-name")
}
