package doctor

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

func TestCanceledProbePreservesContextError(t *testing.T) {
	dir := t.TempDir()
	for _, name := range []string{"ssh", "session-manager-plugin"} {
		path := testutil.Write(t, filepath.Join(dir, name), "#!/bin/sh\nexit 0\n")
		if err := os.Chmod(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", dir)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	for _, check := range []func(context.Context) error{CheckSSH, CheckPlugin} {
		if err := check(ctx); !errors.Is(err, context.Canceled) {
			t.Fatalf("expected cancellation, not installation failure: %v", err)
		}
	}
}
