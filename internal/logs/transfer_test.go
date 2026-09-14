package logs

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
)

type transferStore struct {
	object execprotocol.Object
	err    error
	gets   int
	key    string
}

func (s *transferStore) Get(_ context.Context, key string) (execprotocol.Object, error) {
	s.gets++
	s.key = key
	return s.object, s.err
}
func (*transferStore) Put(context.Context, string, io.ReadSeeker, int64, string) error {
	panic("output retrieval must not write cloud objects")
}

type transferBody struct {
	io.Reader
	closes   int
	closeErr error
}

func (b *transferBody) Close() error { b.closes++; return b.closeErr }

func transferMetadata(data []byte) execprotocol.Stream {
	return execprotocol.Stream{Key: "validated/key/stdout", Bytes: int64(len(data)), SHA256: execprotocol.Digest(data), Upload: "complete"}
}

func TestCopyStreamExactBytesAndBoundedWrites(t *testing.T) {
	for _, data := range [][]byte{nil, {0, 255, 254, '\n', '\r', 0, 128}, bytes.Repeat([]byte{0, 255, 'x', '\n'}, 32*1024)} {
		t.Run(fmt.Sprint(len(data)), func(t *testing.T) {
			body := &transferBody{Reader: bytes.NewReader(data)}
			store := &transferStore{object: execprotocol.Object{Body: body, Size: int64(len(data))}}
			writer := &boundedTransferWriter{t: t}
			n, err := CopyStream(context.Background(), store, transferMetadata(data), writer)
			if err != nil || n != int64(len(data)) || !bytes.Equal(writer.Bytes(), data) || body.closes != 1 || store.gets != 1 || store.key != "validated/key/stdout" {
				t.Fatalf("copy = (%d, %v), output %d, closes %d, gets %d, key %q", n, err, writer.Len(), body.closes, store.gets, store.key)
			}
		})
	}
}

type boundedTransferWriter struct {
	bytes.Buffer
	t *testing.T
}

type cancelFinalWriter struct{ cancel context.CancelFunc }

func (w cancelFinalWriter) Write(p []byte) (int, error) { w.cancel(); return len(p), nil }

func TestCopyStreamCancellationInsideFinalWriteIsNotVerified(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := []byte("final bytes")
	body := &transferBody{Reader: &terminalTransferReader{data: data, err: io.EOF}}
	store := &transferStore{object: execprotocol.Object{Body: body, Size: int64(len(data))}}
	n, err := CopyStream(ctx, store, transferMetadata(data), cancelFinalWriter{cancel})
	if n != int64(len(data)) || !errors.Is(err, context.Canceled) || body.closes != 1 {
		t.Fatalf("canceled final write reported success: %d %v", n, err)
	}
}

func (w *boundedTransferWriter) Write(p []byte) (int, error) {
	if len(p) > transferBufferBytes {
		w.t.Fatalf("unbounded write of %d bytes", len(p))
	}
	return w.Buffer.Write(p)
}

// A synthetic stream exceeds inline output limits without retaining its body.
// Its reader rejects larger reads and its writer only retains a digest.
func TestCopyStreamLargeGeneratedBody(t *testing.T) {
	const size int64 = 64<<20 + 123
	expected := sha256.New()
	if _, err := io.Copy(expected, io.LimitReader(repeatingTransferReader{}, size)); err != nil {
		t.Fatal(err)
	}
	metadata := execprotocol.Stream{Key: "validated/key/stderr", Bytes: size, SHA256: hex.EncodeToString(expected.Sum(nil)), Upload: "complete"}
	body := &transferBody{Reader: io.LimitReader(boundedTransferReader{t}, size)}
	store := &transferStore{object: execprotocol.Object{Body: body, Size: size}}
	actual := sha256.New()
	n, err := CopyStream(context.Background(), store, metadata, actual)
	if err != nil || n != size || !bytes.Equal(actual.Sum(nil), expected.Sum(nil)) || body.closes != 1 {
		t.Fatalf("large copy = (%d, %v), closes %d", n, err, body.closes)
	}
}

type repeatingTransferReader struct{}

func (repeatingTransferReader) Read(p []byte) (int, error) {
	for i := range p {
		p[i] = 0xfd
	}
	return len(p), nil
}

type boundedTransferReader struct{ t *testing.T }

func (r boundedTransferReader) Read(p []byte) (int, error) {
	if len(p) > transferBufferBytes {
		r.t.Fatalf("unbounded read of %d bytes", len(p))
	}
	return repeatingTransferReader{}.Read(p)
}

func TestCopyStreamRejectsUnverifiedContent(t *testing.T) {
	data := []byte("expected bytes")
	for _, tc := range []struct {
		name    string
		body    []byte
		size    int64
		hash    string
		wantN   int64
		wantOut []byte
	}{
		{"advertised short", data, int64(len(data) - 1), execprotocol.Digest(data), 0, nil},
		{"advertised extra", data, int64(len(data) + 1), execprotocol.Digest(data), 0, nil},
		{"advertised unknown", data, -1, execprotocol.Digest(data), 0, nil},
		{"short body", data[:5], int64(len(data)), execprotocol.Digest(data), 5, data[:5]},
		{"extra body", append(append([]byte(nil), data...), '!'), int64(len(data)), execprotocol.Digest(data), int64(len(data)), data},
		{"bad digest", data, int64(len(data)), strings.Repeat("0", 64), int64(len(data)), data},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &transferBody{Reader: bytes.NewReader(tc.body)}
			store := &transferStore{object: execprotocol.Object{Body: body, Size: tc.size}}
			metadata := transferMetadata(data)
			metadata.SHA256 = tc.hash
			var out bytes.Buffer
			n, err := CopyStream(context.Background(), store, metadata, &out)
			if err != execprotocol.ErrCorrupt || n != tc.wantN || !bytes.Equal(out.Bytes(), tc.wantOut) || body.closes != 1 {
				t.Fatalf("copy = (%d, %v), output %q, closes %d", n, err, out.Bytes(), body.closes)
			}
		})
	}
}

func TestCopyStreamRejectsInvalidMetadataBeforeGet(t *testing.T) {
	for _, change := range []func(*execprotocol.Stream){
		func(s *execprotocol.Stream) { s.Bytes = -1 },
		func(s *execprotocol.Stream) { s.Bytes = execprotocol.MaxStreamBytes + 1 },
		func(s *execprotocol.Stream) { s.SHA256 = "SECRET" },
		func(s *execprotocol.Stream) { s.Key = "" },
		func(s *execprotocol.Stream) { s.Upload = "pending" },
	} {
		store := &transferStore{}
		metadata := transferMetadata(nil)
		change(&metadata)
		if n, err := CopyStream(context.Background(), store, metadata, io.Discard); n != 0 || err != execprotocol.ErrCorrupt || store.gets != 0 {
			t.Fatalf("invalid metadata = (%d, %v), gets %d", n, err, store.gets)
		}
	}
}

func TestCopyStreamStorageErrorsAreSanitized(t *testing.T) {
	for _, known := range []error{execprotocol.ErrNotFound, execprotocol.ErrDenied, execprotocol.ErrUnavailable, execprotocol.ErrCorrupt, execprotocol.ErrCredentialsExpired, execprotocol.ErrCredentialsInvalid, execprotocol.ErrCredentialsUnavailable, execprotocol.ErrConflict, execprotocol.ErrUnsupportedSchema, context.Canceled, context.DeadlineExceeded} {
		t.Run(known.Error(), func(t *testing.T) {
			store := &transferStore{err: fmt.Errorf("SECRET provider stderr: %w", known)}
			if n, err := CopyStream(context.Background(), store, transferMetadata(nil), io.Discard); n != 0 || err != known || strings.Contains(err.Error(), "SECRET") {
				t.Fatalf("storage failure = (%d, %v)", n, err)
			}
		})
	}
	store := &transferStore{err: errors.New("SECRET provider output")}
	if _, err := CopyStream(context.Background(), store, transferMetadata(nil), io.Discard); err != execprotocol.ErrUnavailable {
		t.Fatalf("opaque storage failure = %v", err)
	}
}

type terminalTransferReader struct {
	data []byte
	err  error
}

func (r *terminalTransferReader) Read(p []byte) (int, error) {
	n := copy(p, r.data)
	r.data = r.data[n:]
	if len(r.data) == 0 {
		return n, r.err
	}
	return n, nil
}

func TestCopyStreamRequiresEOFAndClosesOnEveryReadFailure(t *testing.T) {
	data := []byte("binary\x00\xff")
	for _, tc := range []struct {
		name string
		err  error
		want error
	}{
		{"full body and EOF", io.EOF, nil},
		{"full body then broken socket", errors.New("SECRET network"), execprotocol.ErrUnavailable},
		{"unexpected EOF", io.ErrUnexpectedEOF, execprotocol.ErrUnavailable},
		{"canceled body", fmt.Errorf("SECRET: %w", context.Canceled), context.Canceled},
		{"deadline body", fmt.Errorf("SECRET: %w", context.DeadlineExceeded), context.DeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			body := &transferBody{Reader: &terminalTransferReader{data: data, err: tc.err}}
			store := &transferStore{object: execprotocol.Object{Body: body, Size: int64(len(data))}}
			n, err := CopyStream(context.Background(), store, transferMetadata(data), io.Discard)
			if n != int64(len(data)) || err != tc.want || body.closes != 1 {
				t.Fatalf("read = (%d, %v), closes %d", n, err, body.closes)
			}
		})
	}
	body := &transferBody{Reader: bytes.NewReader(data), closeErr: errors.New("SECRET close")}
	store := &transferStore{object: execprotocol.Object{Body: body, Size: int64(len(data))}}
	if _, err := CopyStream(context.Background(), store, transferMetadata(data), io.Discard); err != execprotocol.ErrUnavailable || body.closes != 1 {
		t.Fatalf("close = %v, closes %d", err, body.closes)
	}
}

type failedTransferWriter struct {
	n   int
	err error
}

func (w failedTransferWriter) Write([]byte) (int, error) { return w.n, w.err }

func TestCopyStreamReportsOnlyActuallyWrittenBytes(t *testing.T) {
	data := []byte("longer than short write")
	for _, tc := range []struct {
		writer failedTransferWriter
		wantN  int64
	}{
		{failedTransferWriter{n: 4, err: errors.New("SECRET filesystem")}, 4},
		{failedTransferWriter{n: 4}, 4},
		{failedTransferWriter{n: -1}, 0},
		{failedTransferWriter{n: len(data) + 1}, 0},
	} {
		body := &transferBody{Reader: bytes.NewReader(data)}
		store := &transferStore{object: execprotocol.Object{Body: body, Size: int64(len(data))}}
		n, err := CopyStream(context.Background(), store, transferMetadata(data), tc.writer)
		if n != tc.wantN || err != ErrWrite || body.closes != 1 {
			t.Fatalf("write failure = (%d, %v), closes %d", n, err, body.closes)
		}
	}
}

type blockedTransferBody struct {
	reading chan struct{}
	closed  chan struct{}
	once    sync.Once
	closes  atomic.Int32
}

func (b *blockedTransferBody) Read([]byte) (int, error) {
	b.once.Do(func() { close(b.reading) })
	<-b.closed
	return 0, errors.New("SECRET interrupted socket")
}
func (b *blockedTransferBody) Close() error {
	if b.closes.Add(1) == 1 {
		close(b.closed)
	}
	return nil
}

func TestCopyStreamCancellationClosesBlockedRead(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	body := &blockedTransferBody{reading: make(chan struct{}), closed: make(chan struct{})}
	store := &transferStore{object: execprotocol.Object{Body: body, Size: 3}}
	result := make(chan error, 1)
	go func() {
		n, err := CopyStream(ctx, store, transferMetadata([]byte("abc")), io.Discard)
		if n != 0 {
			result <- fmt.Errorf("unexpected count %d", n)
			return
		}
		result <- err
	}()
	select {
	case <-body.reading:
	case <-time.After(3 * time.Second):
		t.Fatal("copy did not begin reading")
	}
	cancel()
	select {
	case err := <-result:
		if err != context.Canceled || body.closes.Load() != 1 {
			t.Fatalf("cancellation = %v, closes %d", err, body.closes.Load())
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not interrupt the read")
	}
}

func TestExportStreamPublishesOnlyVerifiedPrivateFile(t *testing.T) {
	for _, data := range [][]byte{nil, bytes.Repeat([]byte{0, 255, '\n'}, 65536)} {
		dir := t.TempDir()
		dest := filepath.Join(dir, "stdout.bin")
		body := &transferBody{Reader: bytes.NewReader(data)}
		store := &transferStore{object: execprotocol.Object{Body: body, Size: int64(len(data))}}
		n, err := ExportStream(context.Background(), store, transferMetadata(data), dest)
		if err != nil || n != int64(len(data)) {
			t.Fatalf("export = (%d, %v)", n, err)
		}
		actual, err := os.ReadFile(dest)
		if err != nil || !bytes.Equal(actual, data) {
			t.Fatalf("exported bytes differ: %v", err)
		}
		info, err := os.Stat(dest)
		if err != nil || info.Mode().Perm() != 0600 {
			t.Fatalf("file mode = %v, %v", info, err)
		}
		assertTransferDirectory(t, dir, "stdout.bin")
	}
}

func TestExportStreamFailureLeavesNoDestinationOrTemporaryFile(t *testing.T) {
	data := []byte("expected")
	for _, tc := range []struct {
		name string
		body io.Reader
		err  error
		want error
	}{
		{"short", bytes.NewReader(data[:2]), nil, execprotocol.ErrCorrupt},
		{"extra", bytes.NewReader(append(append([]byte(nil), data...), '!')), nil, execprotocol.ErrCorrupt},
		{"checksum", bytes.NewReader([]byte("replaced")), nil, execprotocol.ErrCorrupt},
		{"transport", &terminalTransferReader{data: data[:2], err: errors.New("SECRET socket")}, nil, execprotocol.ErrUnavailable},
		{"denied", nil, fmt.Errorf("SECRET: %w", execprotocol.ErrDenied), execprotocol.ErrDenied},
		{"missing", nil, execprotocol.ErrNotFound, execprotocol.ErrNotFound},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			store := &transferStore{err: tc.err}
			if tc.body != nil {
				store.object = execprotocol.Object{Body: &transferBody{Reader: tc.body}, Size: int64(len(data))}
			}
			n, err := ExportStream(context.Background(), store, transferMetadata(data), filepath.Join(dir, "output.bin"))
			if n != 0 || err != tc.want {
				t.Fatalf("failed export = (%d, %v)", n, err)
			}
			assertTransferDirectory(t, dir)
		})
	}
}

type callbackTransferReader struct {
	io.Reader
	before func()
}

func (r *callbackTransferReader) Read(p []byte) (int, error) {
	if r.before != nil {
		r.before()
		r.before = nil
	}
	return r.Reader.Read(p)
}

func TestExportStreamDoesNotOverwriteRacingFileOrSymlink(t *testing.T) {
	for _, symlink := range []bool{false, true} {
		t.Run(fmt.Sprint(symlink), func(t *testing.T) {
			dir := t.TempDir()
			dest := filepath.Join(dir, "output.bin")
			target := filepath.Join(t.TempDir(), "target.bin")
			if err := os.WriteFile(target, []byte("existing target"), 0600); err != nil {
				t.Fatal(err)
			}
			data := []byte("downloaded bytes")
			body := &transferBody{Reader: &callbackTransferReader{Reader: bytes.NewReader(data), before: func() {
				var err error
				if symlink {
					err = os.Symlink(target, dest)
				} else {
					err = os.WriteFile(dest, []byte("racing bytes"), 0600)
				}
				if err != nil {
					t.Fatal(err)
				}
			}}}
			store := &transferStore{object: execprotocol.Object{Body: body, Size: int64(len(data))}}
			if n, err := ExportStream(context.Background(), store, transferMetadata(data), dest); n != 0 || err != ErrDestination {
				t.Fatalf("racing export = (%d, %v)", n, err)
			}
			out, err := os.ReadFile(dest)
			want := "racing bytes"
			if symlink {
				want = "existing target"
				if _, err := os.Readlink(dest); err != nil {
					t.Fatal("destination symlink was replaced")
				}
			}
			if err != nil || string(out) != want {
				t.Fatalf("existing entry changed: %q, %v", out, err)
			}
			assertTransferDirectory(t, dir, "output.bin")
		})
	}
}

func TestExportStreamCancellationCleansTemporaryFile(t *testing.T) {
	dir := t.TempDir()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	data := []byte("cancel before publish")
	body := &transferBody{Reader: &callbackTransferReader{Reader: bytes.NewReader(data), before: cancel}}
	store := &transferStore{object: execprotocol.Object{Body: body, Size: int64(len(data))}}
	if n, err := ExportStream(ctx, store, transferMetadata(data), filepath.Join(dir, "output.bin")); n != 0 || err != context.Canceled {
		t.Fatalf("canceled export = (%d, %v)", n, err)
	}
	assertTransferDirectory(t, dir)
}

func TestValidateDestinationsRejectsExistingEntriesAndAliases(t *testing.T) {
	dir := t.TempDir()
	alias := filepath.Join(t.TempDir(), "alias")
	if err := os.Symlink(dir, alias); err != nil {
		t.Fatal(err)
	}
	existing := filepath.Join(dir, "existing")
	if err := os.WriteFile(existing, []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	dangling := filepath.Join(dir, "dangling")
	if err := os.Symlink(filepath.Join(dir, "missing-target"), dangling); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct{ first, second string }{
		{existing, ""}, {dangling, ""}, {dir, ""},
		{filepath.Join(dir, "missing", "output"), ""},
		{filepath.Join(dir, "new") + string(os.PathSeparator), ""},
		{filepath.Join(dir, "new"), filepath.Join(dir, "new")},
		{filepath.Join(dir, "new"), filepath.Join(alias, "new")},
		{filepath.Join(dir, "new"), filepath.Join(dir, ".", "new")},
	} {
		if err := ValidateDestinations(tc.first, tc.second); err != ErrDestination {
			t.Fatalf("destinations %q, %q = %v", tc.first, tc.second, err)
		}
		store := &transferStore{}
		if tc.second == "" {
			if n, err := ExportStream(context.Background(), store, transferMetadata(nil), tc.first); n != 0 || err != ErrDestination || store.gets != 0 {
				t.Fatalf("invalid destination export = (%d, %v), gets %d", n, err, store.gets)
			}
		}
	}
	if err := ValidateDestinations(filepath.Join(dir, "stdout"), filepath.Join(alias, "stderr")); err != nil {
		t.Fatalf("distinct destinations rejected: %v", err)
	}
	if err := ValidateDestinations("", ""); err != nil {
		t.Fatalf("status-only destinations rejected: %v", err)
	}
	assertTransferDirectory(t, dir, "dangling", "existing")
}

func TestDestinationsResolveSymlinksBeforeDotDot(t *testing.T) {
	dir := t.TempDir()
	realParent := filepath.Join(dir, "real")
	if err := os.MkdirAll(filepath.Join(realParent, "child"), 0700); err != nil {
		t.Fatal(err)
	}
	alias := filepath.Join(dir, "alias")
	if err := os.Symlink(filepath.Join(realParent, "child"), alias); err != nil {
		t.Fatal(err)
	}
	// Do not use filepath.Join here: its lexical cleaning would remove the very
	// path component whose filesystem meaning this test verifies.
	destination := alias + string(os.PathSeparator) + ".." + string(os.PathSeparator) + "output.bin"
	if err := ValidateDestinations(destination, filepath.Join(realParent, "output.bin")); err != ErrDestination {
		t.Fatalf("symlink/.. alias = %v", err)
	}
	data := []byte("verified")
	store := &transferStore{object: execprotocol.Object{Body: io.NopCloser(bytes.NewReader(data)), Size: int64(len(data))}}
	if n, err := ExportStream(context.Background(), store, transferMetadata(data), destination); n != int64(len(data)) || err != nil {
		t.Fatalf("symlink/.. export = (%d, %v)", n, err)
	}
	actual, err := os.ReadFile(filepath.Join(realParent, "output.bin"))
	if err != nil || !bytes.Equal(actual, data) {
		t.Fatalf("destination does not follow filesystem path semantics: %q, %v", actual, err)
	}
	if _, err := os.Lstat(filepath.Join(dir, "output.bin")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("lexically cleaned destination was created: %v", err)
	}
}

func assertTransferDirectory(t *testing.T, dir string, names ...string) {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != len(names) {
		t.Fatalf("directory has %d entries, expected %d: %v", len(entries), len(names), entries)
	}
	for i := range names {
		if entries[i].Name() != names[i] {
			t.Fatalf("unexpected entry %q, want %q", entries[i].Name(), names[i])
		}
	}
}
