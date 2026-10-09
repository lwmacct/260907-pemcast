// Package pack implements the local pemcast pack subcommand.
package pack

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"

	"github.com/urfave/cli/v3"

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/pack"
)

// Command builds local publication artifacts without contacting etcd.
var Command = &cli.Command{
	Name:  "pack",
	Usage: "validate a TLS pair and build deterministic etcd v5 publication artifacts",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "target", Usage: "target ID", Required: true},
		&cli.StringFlag{Name: "certificate", Usage: "path to fullchain.pem", Required: true},
		&cli.StringFlag{Name: "private-key", Usage: "path to privkey.pem", Required: true},
		&cli.StringFlag{Name: "etcd-prefix", Usage: "etcd namespace prefix placed before /v5", Value: config.DefaultEtcdPrefix},
		&cli.StringFlag{Name: "output-dir", Usage: "new absolute directory for bundle.json, metadata.json, and stage.txn", Required: true},
	},
	Action: func(_ context.Context, command *cli.Command) error {
		result, err := pack.Build(pack.Options{
			TargetID:        command.String("target"),
			EtcdPrefix:      command.String("etcd-prefix"),
			CertificatePath: command.String("certificate"),
			PrivateKeyPath:  command.String("private-key"),
		})
		if err != nil {
			return err
		}
		if err := pack.Write(command.String("output-dir"), result); err != nil {
			return err
		}
		data, err := json.Marshal(result.Metadata)
		if err != nil {
			return fmt.Errorf("encode pack metadata: %w", err)
		}
		_, err = fmt.Fprintln(os.Stdout, string(data))
		return err
	},
}
