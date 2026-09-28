package hook

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/lwmacct/260907-pemcast/internal/config"
)

func TestRunDeliversEnvironmentAndJSON(t *testing.T) {
	dir := t.TempDir()
	output := filepath.Join(dir, "output")
	script := filepath.Join(dir, "hook.sh")
	err := os.WriteFile(script, []byte("#!/bin/sh\nprintf '%s\\n' \"$PEMCAST_TARGET:$PEMCAST_GENERATION\" > \"$1\"\ncat >> \"$1\"\n"), 0o700)
	require.NoError(t, err)
	event := Event{TargetID: "nginx", Generation: "generation-1", BundleSHA256: "digest", ActivatedAt: time.Now()}
	err = New().Run(context.Background(), config.Hook{Path: script, Args: []string{output}, Timeout: time.Second}, event)
	require.NoError(t, err)
	data, err := os.ReadFile(output)
	require.NoError(t, err)
	require.Contains(t, string(data), "nginx:generation-1")
	require.Contains(t, string(data), `"target-id":"nginx"`)
}

func TestRunUsesMinimalEnvironmentWithExplicitPassthrough(t *testing.T) {
	t.Setenv("PEMCAST_TEST_SECRET", "secret-value")
	t.Setenv("PEMCAST_TEST_ALLOWED", "allowed-value")
	t.Setenv("HOME", "/home/should-not-pass")

	dir := t.TempDir()
	output := filepath.Join(dir, "environment")
	script := filepath.Join(dir, "hook.sh")
	content := "#!/bin/sh\nenv | sort > \"$1\"\n"
	require.NoError(t, os.WriteFile(script, []byte(content), 0o700))

	event := Event{TargetID: "nginx", Generation: "g1"}
	err := New().Run(context.Background(), config.Hook{
		Path: script, Args: []string{output}, PassEnvironment: []string{"PEMCAST_TEST_ALLOWED"}, Timeout: time.Second,
	}, event)
	require.NoError(t, err)

	data, err := os.ReadFile(output)
	require.NoError(t, err)
	environment := string(data)
	require.Contains(t, environment, "PEMCAST_TEST_ALLOWED=allowed-value")
	require.Contains(t, environment, "PEMCAST_TARGET=nginx")
	require.Contains(t, environment, "PATH=")
	require.NotContains(t, environment, "PEMCAST_TEST_SECRET=secret-value")
	require.NotContains(t, environment, "HOME=/home/should-not-pass")
}

func TestRunLimitsCollectedFailureOutput(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "hook.sh")
	content := "#!/bin/sh\nyes hook-output | head -c 10000\nexit 42\n"
	require.NoError(t, os.WriteFile(script, []byte(content), 0o700))

	err := New().Run(context.Background(), config.Hook{Path: script, Timeout: time.Second}, Event{})
	require.Error(t, err)
	require.ErrorContains(t, err, "exit status 42")
	require.Less(t, len(err.Error()), 4300)
	require.True(t, strings.Contains(err.Error(), "[output truncated]"))
}

func TestRunTimeoutKillsProcessGroup(t *testing.T) {
	dir := t.TempDir()
	pids := filepath.Join(dir, "pids")
	script := filepath.Join(dir, "hook.sh")
	content := "#!/bin/sh\necho $$ > \"$1\"\nsleep 30 &\necho $! >> \"$1\"\nwait\n"
	require.NoError(t, os.WriteFile(script, []byte(content), 0o700))

	err := New().Run(context.Background(), config.Hook{Path: script, Args: []string{pids}, Timeout: 250 * time.Millisecond}, Event{})
	require.ErrorContains(t, err, "hook timed out")
	require.ErrorContains(t, err, "context deadline exceeded")

	data, err := os.ReadFile(pids)
	require.NoError(t, err)
	fields := strings.Fields(string(data))
	require.Len(t, fields, 2)
	for _, field := range fields {
		var pid int
		_, err := fmt.Sscan(field, &pid)
		require.NoError(t, err)
		require.Positive(t, pid)
		require.Eventually(t, func() bool {
			return syscall.Kill(pid, 0) != nil
		}, time.Second, 10*time.Millisecond, "process %d survived process-group termination", pid)
	}
}
