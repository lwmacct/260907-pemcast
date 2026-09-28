package publisher

import (
	"context"
	"fmt"
	"strings"

	"github.com/lwmacct/251207-go-pkg-cfgm/pkg/cfgm"
	"github.com/urfave/cli/v3"

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	appublisher "github.com/lwmacct/260907-pemcast/internal/publisher"
)

var Command = &cli.Command{
	Name:  "publisher",
	Usage: "publish a complete immutable certificate generation and move the active pointer last",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "target", Usage: "target ID", Required: true},
		&cli.StringFlag{Name: "generation", Usage: "new immutable generation name", Required: true},
		&cli.StringFlag{Name: "certificate", Usage: "path to fullchain.pem"},
		&cli.StringFlag{Name: "private-key", Usage: "path to privkey.pem"},
		&cli.StringFlag{Name: "previous-generation", Usage: "expected current active generation"},
		&cli.BoolFlag{Name: "allow-missing-active", Usage: "allow publication when the active pointer does not exist"},
		&cli.BoolFlag{Name: "activate-existing", Usage: "verify an existing immutable generation and move the pointer without rewriting it"},
	},
	Action: func(ctx context.Context, command *cli.Command) error {
		sources := make([]cfgm.Source, 0, 2)
		if path := strings.TrimSpace(command.Root().String("config")); path != "" {
			sources = append(sources, cfgm.File(path))
		}
		prefix := "PEMCAST_"
		if command.Root().IsSet("env-prefix") {
			prefix = command.Root().String("env-prefix")
		}
		if prefix != "" {
			sources = append(sources, cfgm.Env(prefix))
		}
		cfg, err := config.Manager.Load(ctx, sources...)
		if err != nil {
			return err
		}
		if err := cfg.Agent.ValidateCommon(); err != nil {
			return err
		}
		activateExisting := command.Bool("activate-existing")
		if !activateExisting &&
			(strings.TrimSpace(command.String("certificate")) == "" || strings.TrimSpace(command.String("private-key")) == "") {
			return fmt.Errorf("--certificate and --private-key are required unless --activate-existing is set")
		}

		client, err := etcdsource.New(cfg.Agent.Etcd, cfg.Agent.Watch.RootPrefix)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		options := appublisher.Options{
			RootPrefix:         cfg.Agent.Watch.RootPrefix,
			TargetID:           command.String("target"),
			Generation:         command.String("generation"),
			CertificatePath:    command.String("certificate"),
			PrivateKeyPath:     command.String("private-key"),
			PreviousGeneration: command.String("previous-generation"),
			AllowMissingActive: command.Bool("allow-missing-active"),
			ActivateExisting:   activateExisting,
		}
		if err := appublisher.Publish(ctx, client, options); err != nil {
			return err
		}
		_, err = fmt.Fprintf(command.Root().Writer, "published %s/%s\n", options.TargetID, options.Generation)
		return err
	},
}
