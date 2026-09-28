// Package hook executes trusted local post-activation programs without a shell.
package hook

import (
	"context"
	"encoding/json/v2"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	command.Env = hookEnvironment(cfg.PassEnvironment, event)
	command.Stdin = strings.NewReader(string(payload) + "\n")
	output := newLimitedBuffer(4096)
	command.Stdout = output
	command.Stderr = output
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	command.Cancel = func() error { return killProcessGroup(command) }
	command.WaitDelay = 100 * time.Millisecond
	runErr := command.Run()
	if runErr != nil {
		if hookCtx.Err() != nil {
			return fmt.Errorf("hook timed out or was canceled: %w: %s", hookCtx.Err(), output.truncatedString())
		}
		return fmt.Errorf("hook failed: %w: %s", runErr, output.truncatedString())
	}
	return nil
}

func killProcessGroup(command *exec.Cmd) error {
	if command.Process == nil {
		return nil
	}
	return syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}

func hookEnvironment(pass []string, event Event) []string {
	values := map[string]string{
		"PATH": "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin",
	}
	for _, name := range pass {
		if value, exists := os.LookupEnv(name); exists {
			values[name] = value
		}
	}
	values["PEMCAST_TARGET"] = event.TargetID
	values["PEMCAST_GENERATION"] = event.Generation
	values["PEMCAST_PREVIOUS_GENERATION"] = event.PreviousGeneration
	values["PEMCAST_ETCD_REVISION"] = strconv.FormatInt(event.EtcdRevision, 10)
	values["PEMCAST_RELEASE_DIR"] = event.ReleaseDir
	values["PEMCAST_CURRENT_DIR"] = event.CurrentDir
	values["PEMCAST_CHANGED_FILES"] = strings.Join(event.ChangedFiles, ",")
	values["PEMCAST_BUNDLE_SHA256"] = event.BundleSHA256

	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		environment = append(environment, name+"="+values[name])
	}
	return environment
}

type limitedBuffer struct {
	mu        sync.Mutex
	data      []byte
	limit     int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(chunk []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	remaining := b.limit - len(b.data)
	if remaining <= 0 {
		b.truncated = true
		return len(chunk), nil
	}
	if len(chunk) > remaining {
		b.data = append(b.data, chunk[:remaining]...)
		b.truncated = true
	} else {
		b.data = append(b.data, chunk...)
	}
	return len(chunk), nil
}

func (b *limitedBuffer) truncatedString() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	value := strings.TrimSpace(string(b.data))
	if b.truncated {
		value += "\n[output truncated]"
	}
	return value
}
