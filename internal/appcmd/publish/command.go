// Package publish implements the pemcast publish subcommand.
package publish

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	"github.com/lwmacct/260907-pemcast/internal/keyspace"
	"github.com/lwmacct/260907-pemcast/internal/pack"
	"github.com/lwmacct/260907-pemcast/internal/publisher"
)

// Command applies one validated local v6 pack to etcd.
var Command = &cli.Command{
	Name:  "publish",
	Usage: "stage a v6 pack and commit its active pointer with etcd CAS",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "pack-dir", Usage: "directory containing bundle.json and metadata.json", Required: true},
		&cli.StringFlag{Name: "etcd-prefix", Usage: "override agent.etcd.prefix for this publication"},
	},
	Action: func(ctx context.Context, command *cli.Command) error {
		cfg, err := loadConfig(ctx, command)
		if err != nil {
			return err
		}
		if command.IsSet("etcd-prefix") {
			cfg.Agent.Etcd.Prefix = command.String("etcd-prefix")
		}
		if err := cfg.Agent.ValidateCommon(); err != nil {
			return err
		}
		result, err := pack.Read(command.String("pack-dir"))
		if err != nil {
			return err
		}
		keys, err := keyspace.NewKeys(cfg.Agent.Etcd.Prefix)
		if err != nil {
			return err
		}
		if result.Metadata.EtcdPrefix != keys.Prefix() {
			return fmt.Errorf(
				"pack etcd prefix %q does not match configured prefix %q",
				result.Metadata.EtcdPrefix, keys.Prefix(),
			)
		}
		client, err := etcdsource.New(cfg.Agent.Etcd)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		outcome, err := publisher.Publish(ctx, client, result)
		if err != nil {
			return err
		}
		_, err = fmt.Fprintf(
			command.Root().Writer,
			"pemcast v6 %s: %s %s\n",
			outcome, result.Metadata.TargetID, result.Metadata.Generation,
		)
		return err
	},
}

func loadConfig(ctx context.Context, command *cli.Command) (*config.Config, error) {
	cfg, err := config.LoadEtcdCommand(ctx, command.Root(), config.PublishEtcdUserTemplate)
	if err != nil {
		return nil, err
	}
	if err := cfg.Agent.ValidateCommon(); err != nil {
		return nil, err
	}
	return cfg, nil
}
