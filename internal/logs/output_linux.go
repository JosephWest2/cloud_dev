//go:build linux

package logs

import (
	"context"
	"io"
	"os"
	"strconv"
	"syscall"
	"time"
)

// Inherited stdout can be a blocking, unpollable descriptor. Reopen pipes and
// character devices through procfs to obtain an independent nonblocking open
// file description. Dup+SetNonblock would alter the parent's shared flags.
// Regular files keep their original descriptor, offset and append semantics.
// No writer goroutine or borrowed stdout descriptor is left behind on timeout.
func prepareOutput(ctx context.Context, dst io.Writer) (io.Writer, func(), error) {
	noop := func() {}
	file, ok := dst.(*os.File)
	if !ok {
		// Internal non-file sinks must return from Write themselves; context is
		// checked immediately afterward. The CLI always supplies an os.File.
		return dst, noop, nil
	}
	info, err := file.Stat()
	if err != nil {
		return nil, noop, ErrWrite
	}
	if info.Mode().IsRegular() {
		return file, noop, nil
	}
	owned := false
	if info.Mode()&(os.ModeNamedPipe|os.ModeCharDevice) != 0 {
		raw, rawErr := file.SyscallConn()
		if rawErr != nil {
			return nil, noop, ErrWrite
		}
		var opened *os.File
		var openErr error
		// Fd() can itself switch a Go-managed pipe to blocking mode. Control
		// reads the descriptor number without modifying its shared flags.
		err = raw.Control(func(fd uintptr) {
			flags, _, flagErr := syscall.Syscall(syscall.SYS_FCNTL, fd, syscall.F_GETFL, 0)
			if flagErr != 0 || flags&syscall.O_ACCMODE == syscall.O_RDONLY {
				openErr = syscall.EBADF
				return
			}
			opened, openErr = os.OpenFile("/proc/self/fd/"+strconv.FormatUint(uint64(fd), 10), os.O_WRONLY|syscall.O_NONBLOCK|syscall.O_NOCTTY, 0)
		})
		if err != nil || openErr != nil {
			if opened != nil {
				_ = opened.Close()
			}
			return nil, noop, ErrWrite
		}
		file = opened
		owned = true
	}
	deadline, _ := ctx.Deadline()
	if err := file.SetWriteDeadline(deadline); err != nil {
		if owned && info.Mode()&os.ModeCharDevice != 0 {
			// Some character devices (including /dev/null) cannot join epoll.
			// The owned descriptor is still nonblocking: it returns an error
			// instead of waiting indefinitely if it cannot accept a write.
			return file, func() { _ = file.Close() }, nil
		}
		if owned {
			_ = file.Close()
		}
		return nil, noop, ErrWrite
	}
	finished := make(chan struct{})
	stop := context.AfterFunc(ctx, func() {
		_ = file.SetWriteDeadline(time.Now())
		close(finished)
	})
	return file, func() {
		if !stop() {
			<-finished
		}
		if owned {
			_ = file.Close()
		} else {
			_ = file.SetWriteDeadline(time.Time{})
		}
	}, nil
}
