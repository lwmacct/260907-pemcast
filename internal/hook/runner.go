// Package hook executes trusted local post-activation programs without a shell.
package hook

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/lwmacct/260907-pemcast/internal/config"
)

// Event is delivered to the hook as JSON on stdin and selected environment variables.
type Event struct {
	TargetID           string    `json:"target-id"`
	Generation         string    `json:"generation"`
	PreviousGeneration string    `json:"previous-generation"`
	EtcdRevision       int64     `json:"etcd-revision"`
	ReleaseDir         string    `json:"release-dir"`
	CurrentDir         string    `json:"current-dir"`
	ChangedFiles       []string  `json:"changed-files"`
	BundleSHA256       string    `json:"bundle-sha256"`
	ActivatedAt        time.Time `json:"activated-at"`
}

// Runner invokes hook executables.
type Runner struct{}

func New() *Runner { return &Runner{} }

func (r *Runner) Run(ctx context.Context, cfg config.Hook, event Event) error {
	if strings.TrimSpace(cfg.Path) == "" {
		return nil
	}
	hookCtx := ctx
	cancel := func() {}
	if cfg.Timeout > 0 {
		hookCtx, cancel = context.WithTimeout(ctx, cfg.Timeout)
	}
	defer cancel()
	payload, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("encode hook event: %w", err)
	}
	command := exec.CommandContext(hookCtx, cfg.Path, cfg.Args...) //nolint:gosec // Executable is trusted local operator configuration.
	command.Env = append(os.Environ(),
		"PEMCAST_TARGET="+event.TargetID,
		"PEMCAST_GENERATION="+event.Generation,
		"PEMCAST_PREVIOUS_GENERATION="+event.PreviousGeneration,
		"PEMCAST_ETCD_REVISION="+strconv.FormatInt(event.EtcdRevision, 10),
		"PEMCAST_RELEASE_DIR="+event.ReleaseDir,
		"PEMCAST_CURRENT_DIR="+event.CurrentDir,
		"PEMCAST_CHANGED_FILES="+strings.Join(event.ChangedFiles, ","),
		"PEMCAST_BUNDLE_SHA256="+event.BundleSHA256,
	)
	command.Stdin = strings.NewReader(string(payload) + "\n")
	output, err := command.CombinedOutput()
	if err != nil {
		if hookCtx.Err() != nil {
			return fmt.Errorf("hook timed out or was canceled: %w", hookCtx.Err())
		}
		return fmt.Errorf("hook failed: %w: %s", err, truncate(output, 4096))
	}
	return nil
}

func truncate(value []byte, limit int) string {
	if len(value) > limit {
		value = value[:limit]
	}
	return strings.TrimSpace(string(value))
}
