//go:build linux

package lifecycle

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

func TestReceiptLockAcrossProcessesAndReplacement(t *testing.T) {
	s, m, p, store, _ := setupUp(t)
	r, err := newReceipt(parameters(s.Scope, m, p, "smoke"))
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := store.Lock(context.Background(), r.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	if err = store.Save(r); err != nil {
		t.Fatal(err)
	}
	r.State = "dispatched"
	if err = store.Save(r); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^TestReceiptLockHelper$")
	cmd.Env = append(os.Environ(), "DEVBOX_TEST_LOCK="+filepath.Join(store.Dir, r.RequestID+".lock"))
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cross-process lock after atomic replacement: %v %s", err, output)
	}
}
func TestReceiptLockHelper(t *testing.T) {
	path := os.Getenv("DEVBOX_TEST_LOCK")
	if path == "" {
		t.Skip("subprocess helper")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	unlock, err := lockReceipt(ctx, path)
	if err == nil {
		unlock()
		t.Fatal("acquired lock held by parent after receipt replacement")
	}
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
}
