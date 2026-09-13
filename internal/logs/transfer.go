package logs

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"sync"

	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
)

var (
	ErrWrite       = errors.New("output write failed")
	ErrDestination = errors.New("output destinations must be distinct new files in usable directories")
)

const transferBufferBytes = 32 * 1024

// CopyStream copies exact bytes from an already identity-validated stream record.
// It verifies the advertised length, body length, SHA-256 and EOF using bounded
// memory. Its count is the number of bytes emitted, including on failure; bytes
// already sent to dst cannot be recalled if later verification fails.
func CopyStream(ctx context.Context, store execprotocol.Store, stream execprotocol.Stream, dst io.Writer) (written int64, resultErr error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if stream.Key == "" || stream.Bytes < 0 || stream.Bytes > execprotocol.MaxStreamBytes || !execprotocol.ValidSHA256(stream.SHA256) || stream.Upload != "complete" {
		return 0, execprotocol.ErrCorrupt
	}
	if dst == nil {
		return 0, ErrWrite
	}
	writer, release, err := prepareOutput(ctx, dst)
	if err != nil {
		return 0, err
	}
	defer release()
	obj, err := store.Get(ctx, stream.Key)
	if err != nil {
		if obj.Body != nil {
			_ = obj.Body.Close()
		}
		return 0, transferStorageError(ctx, err)
	}
	if obj.Body == nil {
		return 0, execprotocol.ErrCorrupt
	}
	var closeOnce sync.Once
	var closeErr error
	closeBody := func() { closeOnce.Do(func() { closeErr = obj.Body.Close() }) }
	// Closing a response body interrupts a blocked HTTP read on cancellation.
	// One close is shared with normal cleanup, including all validation failures.
	stopClose := context.AfterFunc(ctx, closeBody)
	defer func() {
		stopClose()
		closeBody()
		if resultErr == nil && closeErr != nil {
			resultErr = transferStorageError(ctx, closeErr)
		}
	}()
	if obj.Size != stream.Bytes {
		return 0, execprotocol.ErrCorrupt
	}
	hash := sha256.New()
	buffer := make([]byte, transferBufferBytes)
	emptyReads := 0
	for {
		if err := ctx.Err(); err != nil {
			return written, err
		}
		// Read at most one byte beyond the expected body. Extra bytes are never
		// written, and a known-empty stream still requires a real object and EOF.
		limit := min(int64(len(buffer)), stream.Bytes-written+1)
		n, readErr := obj.Body.Read(buffer[:limit])
		if n < 0 || n > int(limit) {
			return written, execprotocol.ErrUnavailable
		}
		if err := ctx.Err(); err != nil {
			return written, err
		}
		if n > 0 {
			emptyReads = 0
			valid := int(min(int64(n), stream.Bytes-written))
			if valid > 0 {
				_, _ = hash.Write(buffer[:valid])
				nw, writeErr := writer.Write(buffer[:valid])
				if nw < 0 || nw > valid {
					return written, ErrWrite
				}
				written += int64(nw)
				if err := ctx.Err(); err != nil {
					return written, err
				}
				if writeErr != nil || nw != valid {
					if err := ctx.Err(); err != nil {
						return written, err
					}
					// The poller's deadline can fire just before the context's
					// timer goroutine records DeadlineExceeded.
					if errors.Is(writeErr, os.ErrDeadlineExceeded) {
						return written, context.DeadlineExceeded
					}
					return written, ErrWrite
				}
			}
			if n > valid {
				return written, execprotocol.ErrCorrupt
			}
		} else if readErr == nil {
			emptyReads++
			if emptyReads >= 100 {
				return written, execprotocol.ErrUnavailable
			}
		}
		if readErr != nil {
			if !errors.Is(readErr, io.EOF) {
				return written, transferStorageError(ctx, readErr)
			}
			if written != stream.Bytes || hex.EncodeToString(hash.Sum(nil)) != stream.SHA256 {
				return written, execprotocol.ErrCorrupt
			}
			if err := ctx.Err(); err != nil {
				return written, err
			}
			return written, nil
		}
	}
}

// ExportStream verifies output in a private adjacent temporary file, then
// publishes it with a no-overwrite hard link. Its count is zero on failure;
// partial downloads are never published as the requested destination.
func ExportStream(ctx context.Context, store execprotocol.Store, stream execprotocol.Stream, destination string) (int64, error) {
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	dest, err := resolveDestination(destination)
	if err != nil {
		return 0, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".devbox-output-*")
	if err != nil {
		return 0, ErrWrite
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	defer tmp.Close()
	written, err := CopyStream(ctx, store, stream, tmp)
	if err != nil {
		return 0, err
	}
	if err := tmp.Sync(); err != nil {
		return 0, ErrWrite
	}
	if err := tmp.Close(); err != nil {
		return 0, ErrWrite
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	// Link creates the new directory entry atomically and fails if any entry,
	// including a symlink, appeared after validation. It never replaces one.
	if err := os.Link(tmpName, dest); err != nil {
		if errors.Is(err, os.ErrExist) {
			return 0, ErrDestination
		}
		return 0, ErrWrite
	}
	return written, nil
}

// ValidateDestinations checks export arguments before any cloud operation. Empty
// values select no file. Existing entries (including dangling symlinks), parent
// aliases and unusable directories are rejected. Publication rechecks races.
func ValidateDestinations(stdoutFile, stderrFile string) error {
	paths := make([]string, 0, 2)
	for _, path := range []string{stdoutFile, stderrFile} {
		if path == "" {
			continue
		}
		resolved, err := resolveDestination(path)
		if err != nil {
			return err
		}
		paths = append(paths, resolved)
	}
	if len(paths) == 2 {
		if paths[0] == paths[1] {
			return ErrDestination
		}
		// SameFile also detects parent aliases through a bind mount that cannot
		// be resolved merely by evaluating symbolic links.
		if filepath.Base(paths[0]) == filepath.Base(paths[1]) {
			first, firstErr := os.Stat(filepath.Dir(paths[0]))
			second, secondErr := os.Stat(filepath.Dir(paths[1]))
			if firstErr != nil || secondErr != nil || os.SameFile(first, second) {
				return ErrDestination
			}
		}
	}
	for _, path := range paths {
		probe, err := os.CreateTemp(filepath.Dir(path), ".devbox-output-*")
		if err != nil {
			return ErrDestination
		}
		closeErr := probe.Close()
		removeErr := os.Remove(probe.Name())
		if closeErr != nil || removeErr != nil {
			return ErrDestination
		}
	}
	return nil
}

func resolveDestination(destination string) (string, error) {
	directory, name := filepath.Split(destination)
	if name == "" || name == "." || name == ".." {
		return "", ErrDestination
	}
	if directory == "" {
		directory = "."
	}
	// Resolve symlinks before cleaning any ".." component. Cleaning first would
	// change the destination of paths such as link-to-other-dir/../output.
	parent, err := filepath.EvalSymlinks(directory)
	if err != nil {
		return "", ErrDestination
	}
	parent, err = filepath.Abs(parent)
	if err != nil {
		return "", ErrDestination
	}
	dest := filepath.Join(parent, name)
	if _, err := os.Lstat(dest); !errors.Is(err, os.ErrNotExist) {
		return "", ErrDestination
	}
	info, err := os.Stat(parent)
	if err != nil || !info.IsDir() {
		return "", ErrDestination
	}
	return dest, nil
}

func transferStorageError(ctx context.Context, err error) error {
	if canceled := ctx.Err(); canceled != nil {
		return canceled
	}
	for _, known := range []error{
		context.Canceled, context.DeadlineExceeded,
		execprotocol.ErrNotFound, execprotocol.ErrDenied, execprotocol.ErrCorrupt,
		execprotocol.ErrUnavailable, execprotocol.ErrConflict, execprotocol.ErrUnsupportedSchema,
		execprotocol.ErrCredentialsExpired, execprotocol.ErrCredentialsInvalid, execprotocol.ErrCredentialsUnavailable,
	} {
		if errors.Is(err, known) {
			return known
		}
	}
	return execprotocol.ErrUnavailable
}
