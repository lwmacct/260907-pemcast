// Package config implements configuration utility subcommands.
package config

import (
	"context"
	"fmt"
	"os"

	"github.com/lwmacct/251207-go-pkg-cfgm/pkg/cfgm"
	"github.com/urfave/cli/v3"

	appconfig "github.com/lwmacct/260907-pemcast/internal/config"
)

// Command groups configuration inspection and validation utilities.
var Command = &cli.Command{
	Name:  "config",
	Usage: "inspect and validate pemcast configuration",
	Commands: []*cli.Command{
		{
			Name:  "example",
			Usage: "write an example configuration to standard output",
			Action: func(context.Context, *cli.Command) error {
				data, err := cfgm.ExampleYAML(appconfig.ExampleConfig())
				if err != nil {
					return err
				}
				_, err = os.Stdout.Write(data)
				return err
			},
		},
		{
			Name:  "validate",
			Usage: "load and validate the effective agent configuration",
			Action: func(ctx context.Context, command *cli.Command) error {
				loaded, err := appconfig.LoadCommand(ctx, command.Root())
				if err != nil {
					return err
				}
				if err := loaded.Validate(); err != nil {
					return err
				}
				_, err = fmt.Fprintln(os.Stdout, "configuration is valid")
				return err
			},
		},
	},
}
