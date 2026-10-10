// Package pack implements the local pemcast tools pack subcommand.
package pack

import (
	"context"
	"encoding/json/v2"
	"fmt"

	"github.com/urfave/cli/v3"

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/pack"
)

// Command builds local publication artifacts without contacting etcd.
var Command = &cli.Command{
	Name:  "pack",
	Usage: "validate certificate material and build deterministic etcd v6 publication artifacts",
	Action: config.Manager.Action(func(_ context.Context, command *cli.Command, cfg *config.Config) error {
		options := cfg.Tools.Pack
		result, err := pack.Build(pack.Options{
			Type:            options.Type,
			TargetID:        options.TargetID,
			EtcdPrefix:      options.EtcdPrefix,
			CertificatePath: options.CertificatePath,
			PrivateKeyPath:  options.PrivateKeyPath,
			CAPath:          options.CAPath,
		})
		if err != nil {
			return err
		}
		if err := pack.Write(options.OutputDir, result); err != nil {
			return err
		}
		data, err := json.Marshal(result.Metadata)
		if err != nil {
			return fmt.Errorf("encode pack metadata: %w", err)
		}
		_, err = fmt.Fprintln(command.Root().Writer, string(data))
		return err
	}),
}
