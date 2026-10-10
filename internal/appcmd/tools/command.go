// Package tools groups pemcast's local offline utilities.
package tools

import (
	"github.com/urfave/cli/v3"

	packcmd "github.com/lwmacct/260907-pemcast/internal/appcmd/tools/pack"
	seedcmd "github.com/lwmacct/260907-pemcast/internal/appcmd/tools/seed"
)

// Command groups offline v6 bundle construction and local seeding utilities.
var Command = &cli.Command{
	Name:     "tools",
	Usage:    "local offline v6 certificate and release utilities",
	Commands: []*cli.Command{packcmd.Command, seedcmd.Command},
}
