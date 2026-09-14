package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
)

func downConfirmationFixture() (config.Config, []lifecycle.Instance) {
	return config.Config{ExpectedAccount: "123456789012", Region: "us-east-2", Deployment: "test", Owner: "alice"}, []lifecycle.Instance{
		{ID: "i-12345678", Name: "first", Group: "smoke"},
		{ID: "i-87654321", Name: "second", Group: "other"},
	}
}

func TestDownConfirmationResponses(t *testing.T) {
	for _, tc := range []struct {
		name, input string
		approved    bool
		code        string
	}{
		{name: "y", input: "y\n", approved: true},
		{name: "yes", input: "yes\n", approved: true},
		{name: "case and whitespace", input: " \tYES \r\n", approved: true},
		{name: "n", input: "n\n"},
		{name: "no", input: "no\n"},
		{name: "blank", input: "\n"},
		{name: "other answer", input: "sure\n"},
		{name: "prefix", input: "yes please\n"},
		{name: "first line only", input: "no\nyes\n"},
		{name: "EOF", code: "confirmation_required"},
		{name: "unterminated y", input: "y", code: "confirmation_required"},
		{name: "unterminated yes", input: "yes", code: "confirmation_required"},
		{name: "unterminated no", input: "no", code: "confirmation_required"},
		{name: "oversized line", input: "yes" + strings.Repeat(" ", downConfirmationInputLimit) + "\n", code: "confirmation_required"},
		{name: "maximum complete line", input: "yes" + strings.Repeat(" ", downConfirmationInputLimit-4) + "\n", approved: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope, candidates := downConfirmationFixture()
			var output bytes.Buffer
			approved, err := newDownConfirmation(strings.NewReader(tc.input), &output, func() bool { return true })(context.Background(), scope, candidates, false)
			if approved != tc.approved {
				t.Fatalf("approved=%t, want %t (err=%v)", approved, tc.approved, err)
			}
			assertDownConfirmationError(t, err, tc.code)
			assertDownConfirmationPreview(t, output.String())
			if !strings.HasSuffix(output.String(), "Terminate these 2 instances? [y/N] ") {
				t.Fatalf("missing prompt after preview: %s", &output)
			}
		})
	}
}

func TestDownConfirmationNoninteractiveNeverReadsConsent(t *testing.T) {
	for _, empty := range []bool{false, true} {
		scope, candidates := downConfirmationFixture()
		if empty {
			candidates = nil
		}
		var output bytes.Buffer
		input := &downUnreadableInput{t: t}
		approved, err := newDownConfirmation(input, &output, func() bool { return false })(context.Background(), scope, candidates, false)
		if approved {
			t.Fatal("noninteractive use approved")
		}
		assertDownConfirmationError(t, err, "confirmation_required")
		if !strings.Contains(output.String(), "preview account=\"123456789012\"") || strings.Contains(output.String(), "[y/N]") {
			t.Fatalf("expected preview without prompt: %s", &output)
		}
		if empty && !strings.Contains(output.String(), "count=0\n") {
			t.Fatalf("empty selection was not previewed: %s", &output)
		}
	}
	// A pipe that already contains an affirmative answer is still ineligible.
	var output bytes.Buffer
	scope, candidates := downConfirmationFixture()
	approved, err := newDownConfirmation(strings.NewReader("yes\n"), &output, func() bool { return false })(context.Background(), scope, candidates, false)
	if approved {
		t.Fatal("piped yes approved")
	}
	assertDownConfirmationError(t, err, "confirmation_required")
}

func TestDownConfirmationYesStillPreviews(t *testing.T) {
	scope, candidates := downConfirmationFixture()
	var output bytes.Buffer
	approved, err := newDownConfirmation(&downUnreadableInput{t: t}, &output, func() bool {
		t.Fatal("--yes unnecessarily checked interactive input")
		return false
	})(context.Background(), scope, candidates, true)
	if !approved || err != nil {
		t.Fatalf("--yes failed: approved=%t err=%v", approved, err)
	}
	assertDownConfirmationPreview(t, output.String())
	if strings.Contains(output.String(), "[y/N]") {
		t.Fatal("--yes prompted")
	}
}

func TestDownConfirmationOutputFailureNeverApproves(t *testing.T) {
	for _, tc := range []struct {
		name  string
		yes   bool
		fail  int
		short bool
	}{
		{name: "preview", fail: 1},
		{name: "preview with yes", yes: true, fail: 1},
		{name: "short preview", yes: true, fail: 1, short: true},
		{name: "prompt", fail: 2},
		{name: "short prompt", fail: 2, short: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scope, candidates := downConfirmationFixture()
			output := &downFailingOutput{failAt: tc.fail, short: tc.short}
			approved, err := newDownConfirmation(&downUnreadableInput{t: t}, output, func() bool { return true })(context.Background(), scope, candidates, tc.yes)
			if approved {
				t.Fatal("output failure approved")
			}
			assertDownConfirmationError(t, err, "output_unavailable")
		})
	}
}

func TestDownConfirmationInputFailureAndBound(t *testing.T) {
	scope, candidates := downConfirmationFixture()
	for _, source := range []io.Reader{downFailedInput{}, strings.NewReader("yes" + strings.Repeat(" ", 10*downConfirmationInputLimit) + "\n")} {
		input := &downCountingInput{Reader: source}
		approved, err := newDownConfirmation(input, io.Discard, func() bool { return true })(context.Background(), scope, candidates, false)
		if approved {
			t.Fatal("failed or oversized input approved")
		}
		assertDownConfirmationError(t, err, "confirmation_required")
		if input.read > downConfirmationInputLimit {
			t.Fatalf("read %d input bytes, limit %d", input.read, downConfirmationInputLimit)
		}
	}
}

func TestDownConfirmationCancellationStopsWaiting(t *testing.T) {
	for _, deadline := range []bool{false, true} {
		scope, candidates := downConfirmationFixture()
		input, writer := io.Pipe()
		t.Cleanup(func() { input.Close(); writer.Close() })
		started := make(chan struct{})
		reader := &downStartedInput{Reader: input, started: started}
		ctx, cancel := context.WithCancel(context.Background())
		want := context.Canceled
		if deadline {
			cancel()
			ctx, cancel = context.WithTimeout(context.Background(), 100*time.Millisecond)
			want = context.DeadlineExceeded
		}
		t.Cleanup(cancel)
		done := make(chan error, 1)
		go func() {
			approved, err := newDownConfirmation(reader, io.Discard, func() bool { return true })(ctx, scope, candidates, false)
			if approved {
				done <- errors.New("cancellation approved termination")
				return
			}
			done <- err
		}()
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("confirmation never started reading")
		}
		if !deadline {
			cancel()
		}
		select {
		case err := <-done:
			if !errors.Is(err, want) {
				t.Fatalf("got %v, want %v", err, want)
			}
		case <-time.After(time.Second):
			t.Fatal("confirmation remained blocked after cancellation")
		}
		writer.Close()
	}
}

func TestDownConfirmationCanceledContextCannotUseYes(t *testing.T) {
	scope, candidates := downConfirmationFixture()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	var output bytes.Buffer
	approved, err := newDownConfirmation(&downUnreadableInput{t: t}, &output, nil)(ctx, scope, candidates, true)
	if approved || !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled --yes: approved=%t err=%v", approved, err)
	}
	assertDownConfirmationPreview(t, output.String())
}

func TestDownConfirmationPreviewQuotesCandidateMetadata(t *testing.T) {
	scope, candidates := downConfirmationFixture()
	candidates[0].Name = "fake\nconfirmation\x1b[2J"
	var output bytes.Buffer
	approved, err := newDownConfirmation(nil, &output, nil)(context.Background(), scope, candidates, true)
	if !approved || err != nil {
		t.Fatal(err)
	}
	if strings.Contains(output.String(), "\x1b") || strings.Count(output.String(), "\n") != 3 || !strings.Contains(output.String(), `fake\nconfirmation\x1b[2J`) {
		t.Fatalf("unsafe preview metadata: %q", output.String())
	}
}

func assertDownConfirmationPreview(t *testing.T, output string) {
	t.Helper()
	want := "devbox: down --all preview account=\"123456789012\" region=\"us-east-2\" deployment=\"test\" owner=\"alice\" count=2\n" +
		"  instance_id=\"i-12345678\" name=\"first\" group=\"smoke\"\n" +
		"  instance_id=\"i-87654321\" name=\"second\" group=\"other\"\n"
	if !strings.HasPrefix(output, want) {
		t.Fatalf("missing exact frozen preview: %s", output)
	}
}

func assertDownConfirmationError(t *testing.T, err error, code string) {
	t.Helper()
	if code == "" {
		if err != nil {
			t.Fatal(err)
		}
		return
	}
	var failure *lifecycle.Failure
	if !errors.As(err, &failure) || failure.Code != code || strings.Contains(err.Error(), "SECRET") {
		t.Fatalf("got %v, want sanitized %s", err, code)
	}
}

type downUnreadableInput struct{ t *testing.T }

func (r *downUnreadableInput) Read([]byte) (int, error) {
	r.t.Error("confirmation unexpectedly read stdin")
	return 0, io.EOF
}

type downFailingOutput struct {
	writes, failAt int
	short          bool
}

func (w *downFailingOutput) Write(p []byte) (int, error) {
	w.writes++
	if w.writes == w.failAt {
		if w.short {
			return len(p) / 2, nil
		}
		return 0, errors.New("SECRET output failure")
	}
	return len(p), nil
}

type downFailedInput struct{}

func (downFailedInput) Read([]byte) (int, error) { return 0, errors.New("SECRET input failure") }

type downCountingInput struct {
	io.Reader
	read int
}

func (r *downCountingInput) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}

type downStartedInput struct {
	io.Reader
	started chan struct{}
}

func (r *downStartedInput) Read(p []byte) (int, error) {
	if r.started != nil {
		close(r.started)
		r.started = nil
	}
	return r.Reader.Read(p)
}
