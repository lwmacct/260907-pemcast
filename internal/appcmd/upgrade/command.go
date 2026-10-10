// Package upgrade implements the pemcast upgrade subcommand.
package upgrade

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	"github.com/lwmacct/260907-pemcast/internal/keyspace"
	"github.com/lwmacct/260907-pemcast/internal/upgrade"
)

// Command migrates the previous protocol below one etcd prefix to v6.
var Command = &cli.Command{
	Name:  "upgrade",
	Usage: "migrate active pemcast v5 data below one etcd prefix to v6",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "etcd-prefix", Usage: "override agent.etcd.prefix for this upgrade"},
		&cli.StringFlag{Name: "default-type", Usage: "v6 type for targets without --target-type (tls-server or tls-client)"},
		&cli.StringSliceFlag{Name: "target-type", Usage: "repeat target-id=tls-server|tls-client for per-target overrides"},
		&cli.BoolFlag{Name: "dry-run", Usage: "validate and print the migration plan without writing etcd"},
		&cli.BoolFlag{Name: "delete-old-v5", Usage: "delete the exact <prefix>/v5/ prefix after all targets pass postverification"},
		&cli.BoolFlag{Name: "yes", Usage: "confirm destructive options such as --delete-old-v5"},
	},
	Action: func(ctx context.Context, command *cli.Command) error {
		cfg, err := config.LoadEtcdCommand(ctx, command.Root(), config.UpgradeEtcdUserTemplate)
		if err != nil {
			return err
		}
		if command.IsSet("etcd-prefix") {
			cfg.Agent.Etcd.Prefix = command.String("etcd-prefix")
		}
		if err := cfg.Agent.ValidateCommon(); err != nil {
			return err
		}
		keys, err := keyspace.NewKeys(cfg.Agent.Etcd.Prefix)
		if err != nil {
			return err
		}
		targetTypes, err := upgrade.NormalizeTargetTypes(command.StringSlice("target-type"))
		if err != nil {
			return err
		}

		client, err := etcdsource.New(cfg.Agent.Etcd)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		result, err := upgrade.Upgrade(ctx, client, upgrade.Options{
			Prefix:        keys.Prefix(),
			DefaultType:   command.String("default-type"),
			TargetTypes:   targetTypes,
			DryRun:        command.Bool("dry-run"),
			DeleteOld:     command.Bool("delete-old-v5"),
			ConfirmDelete: command.Bool("yes"),
		})
		if err != nil {
			return err
		}
		return printResult(command, result)
	},
}

func printResult(command *cli.Command, result upgrade.Result) error {
	mode := "apply"
	if result.DryRun {
		mode = "dry-run"
	}
	if _, err := fmt.Fprintf(
		command.Root().Writer,
		"pemcast upgrade %s: %s %s -> %s targets=%d delete-old-v5=%v\n",
		mode, result.Prefix, result.Source, result.Destination, len(result.Targets), result.DeletedOld,
	); err != nil {
		return err
	}
	for _, target := range result.Targets {
		if _, err := fmt.Fprintf(
			command.Root().Writer,
			"  %s\t%s\t%s -> %s\t%s\n",
			target.TargetID, target.Type, target.SourceGeneration, target.ResultGeneration, target.Status,
		); err != nil {
			return err
		}
	}
	if result.DeletedOld {
		_, err := fmt.Fprintf(command.Root().Writer, "deleted %d old v5 keys\n", result.DeletedKeys)
		return err
	}
	return nil
}
