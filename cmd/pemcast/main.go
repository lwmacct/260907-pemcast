package main

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/lwmacct/251207-go-pkg-version/pkg/version"
	"github.com/urfave/cli/v3"

	agentcmd "github.com/lwmacct/260907-pemcast/internal/appcmd/agent"
	configcmd "github.com/lwmacct/260907-pemcast/internal/appcmd/config"
	"github.com/lwmacct/260907-pemcast/internal/config"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	app := &cli.Command{
		Name:     config.AppName,
		Usage:    "distribute TLS certificate files from etcd and run local reload hooks",
		Version:  version.AppVersion,
		Commands: []*cli.Command{agentcmd.Command, configcmd.Command, version.Command},
	}
	config.Manager.MustConfigure(app)
	if err := app.Run(ctx, os.Args); err != nil {
		slog.Error("pemcast exited", "error", err)
		_, _ = fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
