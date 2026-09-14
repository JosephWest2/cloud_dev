//go:build linux

package logs

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
)

func outputFlags(t *testing.T, file *os.File) uintptr {
	t.Helper()
	raw, err := file.SyscallConn()
	if err != nil {
		t.Fatal(err)
	}
	var flags uintptr
	var flagErr syscall.Errno
	if err := raw.Control(func(fd uintptr) { flags, _, flagErr = syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0) }); err != nil || flagErr != 0 {
		t.Fatalf("read descriptor flags: %v %v", err, flagErr)
	}
	return flags
}

func TestCopyStreamBlockedInheritedPipeHonorsDeadlineAndCancel(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		reader, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer reader.Close()
		defer writer.Close()
		// Fd intentionally makes this test-owned open description blocking,
		// matching inherited stdout. The production adapter must not do that.
		duplicate, err := syscall.Dup(int(writer.Fd()))
		if err != nil {
			t.Fatal(err)
		}
		inherited := os.NewFile(uintptr(duplicate), "inherited-stdout")
		defer inherited.Close()
		before := outputFlags(t, writer)
		if before&syscall.O_NONBLOCK != 0 {
			t.Fatal("probe is not a blocking inherited descriptor")
		}
		ctx, cancel := context.WithCancel(context.Background())
		var want error = context.Canceled
		if deadline {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 50*time.Millisecond)
			want = context.DeadlineExceeded
		} else {
			time.AfterFunc(50*time.Millisecond, cancel)
		}
		defer cancel()
		data := bytes.Repeat([]byte("x"), 1<<20)
		store := &transferStore{object: execprotocol.Object{Body: io.NopCloser(bytes.NewReader(data)), Size: int64(len(data))}}
		type outcome struct {
			count int64
			err   error
		}
		done := make(chan outcome, 1)
		go func() { n, err := CopyStream(ctx, store, transferMetadata(data), inherited); done <- outcome{n, err} }()
		var got outcome
		select {
		case got = <-done:
		case <-time.After(time.Second):
			_ = reader.Close()
			<-done
			t.Fatal("blocked stdout ignored retrieval deadline/cancellation")
		}
		if !errors.Is(got.err, want) || got.count >= int64(len(data)) || outputFlags(t, writer) != before || outputFlags(t, inherited) != before {
			t.Fatalf("timeout or shared descriptor changed: %+v", got)
		}
		_ = inherited.Close()
		_ = writer.Close()
		written, err := io.ReadAll(reader)
		if err != nil || int64(len(written)) != got.count || !bytes.Equal(written, data[:len(written)]) {
			t.Fatal("partial byte count does not match the actual pipe")
		}
	}
}

func TestCopyStreamRegularOutputPreservesOffsetAndAppend(t *testing.T) {
	for _, appendMode := range []bool{false, true} {
		path := filepath.Join(t.TempDir(), "redirected-output")
		if err := os.WriteFile(path, []byte("prefix"), 0600); err != nil {
			t.Fatal(err)
		}
		flags := os.O_WRONLY
		if appendMode {
			flags |= os.O_APPEND
		}
		file, err := os.OpenFile(path, flags, 0)
		if err != nil {
			t.Fatal(err)
		}
		defer file.Close()
		if !appendMode {
			if _, err := file.Seek(0, io.SeekEnd); err != nil {
				t.Fatal(err)
			}
		}
		data := []byte{0, 255, '\n'}
		store := &transferStore{object: execprotocol.Object{Body: io.NopCloser(bytes.NewReader(data)), Size: int64(len(data))}}
		if n, err := CopyStream(context.Background(), store, transferMetadata(data), file); err != nil || n != int64(len(data)) {
			t.Fatalf("regular output failed: %d %v", n, err)
		}
		got, err := os.ReadFile(path)
		if err != nil || !bytes.Equal(got, append([]byte("prefix"), data...)) {
			t.Fatal("stdout reopen changed file offset or append semantics")
		}
	}
}

func TestOutputAdapterClosesOwnedHandleAndPreservesOriginal(t *testing.T) {
	reader, writer, err := os.Pipe()
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	defer writer.Close()
	before := outputFlags(t, writer)
	prepared, release, err := prepareOutput(context.Background(), writer)
	if err != nil {
		t.Fatal(err)
	}
	file, ok := prepared.(*os.File)
	if !ok || file == writer {
		t.Fatal("adapter borrowed the parent's descriptor")
	}
	release()
	if _, err := file.Stat(); !errors.Is(err, os.ErrClosed) || outputFlags(t, writer) != before {
		t.Fatal("owned handle leaked or shared flags changed")
	}
	if _, release, err := prepareOutput(context.Background(), reader); err == nil {
		release()
		t.Fatal("adapter upgraded a read-only pipe to writable")
	}
	_ = reader.Close()
	store := &transferStore{object: execprotocol.Object{Body: io.NopCloser(bytes.NewReader([]byte("x"))), Size: 1}}
	if _, err := CopyStream(context.Background(), store, transferMetadata([]byte("x")), writer); !errors.Is(err, ErrWrite) {
		t.Fatalf("closed pipe must fail without SIGPIPE: %v", err)
	}
}
