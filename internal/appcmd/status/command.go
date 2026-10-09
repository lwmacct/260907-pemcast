package status

import (
	"context"
	"encoding/json/jsontext"
	"encoding/json/v2"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	appconfig "github.com/lwmacct/260907-pemcast/internal/config"
	appstatus "github.com/lwmacct/260907-pemcast/internal/status"
)

var Command = &cli.Command{
	Name:  "status",
	Usage: "inspect local target state without contacting etcd",
	Flags: []cli.Flag{
		&cli.BoolFlag{Name: "json", Usage: "write a machine-readable JSON report"},
	},
	Action: func(ctx context.Context, command *cli.Command) error {
		cfg, err := appconfig.LoadCommand(ctx, command.Root())
		if err != nil {
			return err
		}
		if err := cfg.Validate(); err != nil {
			return err
		}
		report := appstatus.Build(*cfg)
		if command.Bool("json") {
			if err := json.MarshalWrite(os.Stdout, report,
				jsontext.WithIndentPrefix(""),
				jsontext.WithIndent("  "),
			); err != nil {
				return err
			}
			_, err := fmt.Fprintln(os.Stdout)
			return err
		}
		for _, target := range report.Targets {
			fmt.Printf("%s\tstate=%s\tcurrent=%s\n", target.ID, target.StateStatus, target.Current.Status)
			if target.StateError != "" {
				fmt.Printf("  state error: %s\n", target.StateError)
			}
			if target.Current.Error != "" {
				fmt.Printf("  current error: %s\n", target.Current.Error)
			}
		}
		return nil
	},
}
