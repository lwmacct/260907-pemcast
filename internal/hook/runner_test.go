package hook

import (
	"context"
	"os"
	"path/filepath"
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
