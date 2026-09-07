// Package agent implements the pemcast agent subcommand.
package agent

import (
	"context"
	"log/slog"

	"github.com/urfave/cli/v3"

	"github.com/lwmacct/260907-pemcast/internal/config"
)

// Command starts the certificate synchronization agent.
var Command = &cli.Command{
	Name:  "agent",
	Usage: "watch etcd and atomically synchronize local TLS certificate files",
	Action: config.Manager.Action(func(ctx context.Context, _ *cli.Command, cfg *config.Config) error {
		application, err := New(*cfg, slog.Default())
		if err != nil {
			return err
		}
		defer application.Close()
		return application.Run(ctx)
	}),
}
