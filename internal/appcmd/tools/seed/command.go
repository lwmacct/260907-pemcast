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
		prefix := ""
		var matches []config.Target
		prefixes := make(map[string]string, len(cfg.Agent.Etcds))
		for _, source := range cfg.Agent.Etcds {
			prefixes[source.Name] = source.Prefix
		}
		for _, candidate := range cfg.Agent.Targets {
			if options.Etcd != "" && candidate.Etcd != options.Etcd {
				continue
			}
			if candidate.ID == targetID {
				matches = append(matches, candidate)
			}
		}
		if options.Etcd == "" && len(matches) > 1 {
			return fmt.Errorf("target %q is configured by multiple etcds; pass --etcd", targetID)
		}
		if len(matches) != 1 {
			if options.Etcd == "" {
				return fmt.Errorf("target %q is not configured", targetID)
			}
			return fmt.Errorf("target %q for etcd %q is not configured", targetID, options.Etcd)
		}
		target = matches[0]
		prefix = prefixes[target.Etcd]

		result, err := pack.Read(options.PackDir)
		if err != nil {
			return err
		}
		materialOptions := internalseed.Options{
			Target: target, Pack: result, Prefix: prefix,
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
