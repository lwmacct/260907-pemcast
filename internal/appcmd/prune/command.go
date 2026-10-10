// Package prune implements the pemcast prune subcommand.
package prune

import (
	"context"
	"fmt"
	"time"

	"github.com/urfave/cli/v3"

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	"github.com/lwmacct/260907-pemcast/internal/keyspace"
	"github.com/lwmacct/260907-pemcast/internal/prune"
)

// Command reports and optionally removes expired non-active identity bundles.
var Command = &cli.Command{
	Name:  "prune",
	Usage: "report or delete non-active identity bundles past certificate retention",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "etcd-prefix", Usage: "override agent.etcd.prefix for this operation"},
		&cli.DurationFlag{Name: "retention", Usage: "keep inactive identity bundles for this long after their earliest certificate NotAfter", Value: 720 * time.Hour},
		&cli.BoolFlag{Name: "dry-run", Usage: "report candidates without writing etcd"},
		&cli.BoolFlag{Name: "delete", Usage: "delete eligible non-active identity bundles with exact-key conditional transactions"},
	},
	Action: func(ctx context.Context, command *cli.Command) error {
		dryRun, delete := command.Bool("dry-run"), command.Bool("delete")
		if dryRun == delete {
			return fmt.Errorf("choose exactly one of --dry-run or --delete")
		}
		cfg, err := config.LoadEtcdCommand(ctx, command.Root(), config.PruneEtcdUserTemplate)
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

		client, err := etcdsource.New(cfg.Agent.Etcd)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		result, err := prune.Prune(ctx, client, prune.Options{
			Prefix: keys.Prefix(), Retention: command.Duration("retention"), Delete: delete,
		})
		if err != nil {
			return err
		}
		return printResult(command, result, dryRun)
	},
}

func printResult(command *cli.Command, result prune.Result, dryRun bool) error {
	mode := "delete"
	if dryRun {
		mode = "dry-run"
	}
	if _, err := fmt.Fprintf(
		command.Root().Writer,
		"pemcast prune %s: prefix=%s retention=%s scanned=%d eligible=%d deleted=%d\n",
		mode, result.Prefix, result.Retention, result.Scanned, result.Eligible, result.Deleted,
	); err != nil {
		return err
	}
	for _, item := range result.Items {
		notAfter := "-"
		if item.EarliestNotAfter != nil {
			notAfter = item.EarliestNotAfter.UTC().Format(time.RFC3339)
		}
		if _, err := fmt.Fprintf(
			command.Root().Writer,
			"  %s\t%s\t%s\t%s\t%t\t%s\n",
			item.TargetID, item.Type, item.Generation, notAfter, item.Eligible, item.Status,
		); err != nil {
			return err
		}
	}
	return nil
}
