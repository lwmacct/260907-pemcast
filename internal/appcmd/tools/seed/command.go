// Package seed implements the pemcast tools seed subcommand.
package seed

import (
	"context"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/deploy"
	"github.com/lwmacct/260907-pemcast/internal/pack"
	internalseed "github.com/lwmacct/260907-pemcast/internal/seed"
)

var Command = &cli.Command{
	Name:  "seed",
	Usage: "materialize a validated v6 pack locally without contacting etcd",
	Action: config.Manager.Action(func(ctx context.Context, command *cli.Command, cfg *config.Config) error {
		if err := cfg.Validate(); err != nil {
			return err
		}

		options := cfg.Tools.Seed
		targetID := options.TargetID
		var target config.Target
		for _, candidate := range cfg.Agent.Targets {
			if candidate.ID == targetID {
				target = candidate
				break
			}
		}
		if target.ID == "" {
			return fmt.Errorf("target %q is not configured", targetID)
		}

		result, err := pack.Read(options.PackDir)
		if err != nil {
			return err
		}
		materialOptions := internalseed.Options{
			Target: target, Pack: result, Prefix: cfg.Agent.Etcd.Prefix,
			Force: options.Force,
		}
		if _, err := internalseed.Material(materialOptions); err != nil {
			return err
		}

		locks, err := deploy.LockRoots([]config.Target{target})
		if err != nil {
			return err
		}
		defer func() {
			for _, lock := range locks {
				_ = lock.Close()
			}
		}()

		seeded, err := internalseed.Apply(materialOptions, deploy.New())
		if err != nil {
			return err
		}
		outcome := "activated"
		if !seeded.Changed {
			outcome = "no-op"
		}
		_, err = fmt.Fprintf(
			command.Root().Writer,
			"pemcast seed %s: %s %s\n",
			outcome, target.ID, seeded.Generation,
		)
		return err
	}),
}
