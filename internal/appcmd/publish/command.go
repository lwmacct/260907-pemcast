package publish

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"strings"

	"github.com/lwmacct/251207-go-pkg-cfgm/pkg/cfgm"
	"github.com/urfave/cli/v3"

	"github.com/lwmacct/260907-pemcast/internal/config"
	"github.com/lwmacct/260907-pemcast/internal/etcdsource"
	"github.com/lwmacct/260907-pemcast/internal/publisher"
)

var Command = &cli.Command{
	Name:     "publish",
	Usage:    "create and activate immutable content-addressed certificate generations",
	Commands: []*cli.Command{inspectCommand, planCommand, applyCommand, activateCommand},
}

var inspectCommand = &cli.Command{
	Name:  "inspect",
	Usage: "inspect the remote active pointer without changing it",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "target", Usage: "target ID", Required: true},
	},
	Action: func(ctx context.Context, command *cli.Command) error {
		cfg, err := loadConfig(ctx, command)
		if err != nil {
			return err
		}
		client, err := etcdsource.New(cfg.Agent.Etcd)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		state, err := publisher.Inspect(ctx, client, command.String("target"))
		if err != nil {
			return err
		}
		data, err := json.Marshal(state)
		if err != nil {
			return fmt.Errorf("encode active state: %w", err)
		}
		_, err = fmt.Fprintln(os.Stdout, string(data))
		return err
	},
}

var planCommand = &cli.Command{
	Name:  "plan",
	Usage: "validate local material and capture an explicit remote expected state",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "target", Usage: "target ID", Required: true},
		&cli.StringFlag{Name: "certificate", Usage: "path to fullchain.pem", Required: true},
		&cli.StringFlag{Name: "private-key", Usage: "path to privkey.pem", Required: true},
		&cli.StringFlag{Name: "expected-active-generation", Usage: "generation that the active pointer must currently contain"},
		&cli.BoolFlag{Name: "initial", Usage: "require the active pointer to be absent"},
		&cli.StringFlag{Name: "output", Usage: "write the plan to this file instead of standard output"},
	},
	Action: func(ctx context.Context, command *cli.Command) error {
		cfg, err := loadConfig(ctx, command)
		if err != nil {
			return err
		}
		client, err := etcdsource.New(cfg.Agent.Etcd)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		plan, err := publisher.CreatePlan(ctx, client, publisher.PlanOptions{
			TargetID:                 command.String("target"),
			CertificatePath:          command.String("certificate"),
			PrivateKeyPath:           command.String("private-key"),
			ExpectedActiveGeneration: command.String("expected-active-generation"),
			Initial:                  command.Bool("initial"),
		})
		if err != nil {
			return err
		}
		data, err := json.Marshal(plan)
		if err != nil {
			return fmt.Errorf("encode publish plan: %w", err)
		}
		output := strings.TrimSpace(command.String("output"))
		if output == "" {
			_, err = fmt.Fprintln(os.Stdout, string(data))
			return err
		}
		return os.WriteFile(output, append(data, '\n'), 0o600)
	},
}

var applyCommand = &cli.Command{
	Name:  "apply",
	Usage: "apply a publish plan with an etcd atomic transaction",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "plan", Usage: "path created by pemcast publish plan", Required: true},
	},
	Action: func(ctx context.Context, command *cli.Command) error {
		cfg, err := loadConfig(ctx, command)
		if err != nil {
			return err
		}
		data, err := os.ReadFile(command.String("plan"))
		if err != nil {
			return fmt.Errorf("read publish plan: %w", err)
		}
		plan, err := publisher.DecodePlan(data)
		if err != nil {
			return err
		}
		client, err := etcdsource.New(cfg.Agent.Etcd)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()
		if err := publisher.Apply(ctx, client, plan); err != nil {
			return err
		}
		_, err = fmt.Fprintf(os.Stdout, "published %s/%s\n", plan.TargetID, plan.Generation)
		return err
	},
}

var activateCommand = &cli.Command{
	Name:  "activate",
	Usage: "verify an existing immutable generation and move the active pointer by ModRevision CAS",
	Flags: []cli.Flag{
		&cli.StringFlag{Name: "target", Usage: "target ID", Required: true},
		&cli.StringFlag{Name: "generation", Usage: "existing content-addressed generation", Required: true},
		&cli.StringFlag{Name: "expected-active-generation", Usage: "generation that the active pointer must currently contain"},
		&cli.Int64Flag{Name: "expected-active-mod-revision", Usage: "ModRevision captured from etcd for the expected active pointer"},
		&cli.BoolFlag{Name: "initial", Usage: "require the active pointer to be absent"},
	},
	Action: func(ctx context.Context, command *cli.Command) error {
		cfg, err := loadConfig(ctx, command)
		if err != nil {
			return err
		}
		initial := command.Bool("initial")
		expectedRevision := command.Int64("expected-active-mod-revision")
		if !initial && expectedRevision == 0 {
			return fmt.Errorf("--expected-active-mod-revision is required unless --initial is set")
		}
		client, err := etcdsource.New(cfg.Agent.Etcd)
		if err != nil {
			return err
		}
		defer func() { _ = client.Close() }()

		target := command.String("target")
		generation := command.String("generation")
		if err := publisher.Activate(ctx, client, publisher.ActivateOptions{
			TargetID:                  target,
			Generation:                generation,
			ExpectedActiveGeneration:  command.String("expected-active-generation"),
			ExpectedActiveModRevision: expectedRevision,
			Initial:                   initial,
		}); err != nil {
			return err
		}
		_, err = fmt.Fprintf(os.Stdout, "activated %s/%s\n", target, generation)
		return err
	},
}

func loadConfig(ctx context.Context, command *cli.Command) (*config.Config, error) {
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
		return nil, err
	}
	if err := cfg.Agent.ValidateCommon(); err != nil {
		return nil, err
	}
	return cfg, nil
}
