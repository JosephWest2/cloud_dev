package setup

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"
)

// Explicit profiles are passed in argv, SDK options, provider and backend
// configuration. Keep source credentials available for a profile explicitly
// declaring credential_source=Environment; they cannot override those selectors.
func processEnv(environ []string) []string {
	result := []string{}
	for _, item := range environ {
		key, _, _ := strings.Cut(item, "=")
		if strings.HasPrefix(key, "TF_") || strings.HasPrefix(key, "TOFU_") || strings.HasPrefix(key, "AWS_ENDPOINT_URL") {
			continue
		}
		switch key {
		case "AWS_PROFILE", "AWS_DEFAULT_PROFILE", "AWS_REGION", "AWS_DEFAULT_REGION", "AWS_ROLE_ARN", "AWS_ROLE_SESSION_NAME", "AWS_WEB_IDENTITY_TOKEN_FILE", "AWS_CONTAINER_CREDENTIALS_RELATIVE_URI", "AWS_CONTAINER_CREDENTIALS_FULL_URI", "AWS_CONTAINER_AUTHORIZATION_TOKEN", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE", "AWS_PAGER", "AWS_CLI_AUTO_PROMPT", "AWS_MAX_ATTEMPTS":
			continue
		}
		result = append(result, item)
	}
	return append(result, "AWS_REGION=us-east-2", "AWS_DEFAULT_REGION=us-east-2", "AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off", "AWS_MAX_ATTEMPTS=1")
}

type capture struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	stdin     io.WriteCloser
	answered  bool
	approved  bool
	migration bool
}

const migrationPrompt = "Do you want to copy existing state to the new backend?"

func (c *capture) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.buf.Len()+len(p) > 32<<20 {
		return 0, errors.New("process output limit")
	}
	n, _ := c.buf.Write(p)
	if c.migration && !c.answered && strings.Contains(c.buf.String(), "Enter a value:") {
		s := c.buf.String()
		// The exact empty-destination description is pinned to OpenTofu 1.12.6.
		expected := "Pre-existing state was found while migrating the previous \"local\" backend to the\n  newly configured \"s3\" backend. No existing state was found in the newly\n  configured \"s3\" backend. Do you want to copy this state to the new \"s3\"\n  backend? Enter \"yes\" to copy and \"no\" to start with an empty state."
		answer := ""
		if strings.Contains(s, migrationPrompt) && strings.Contains(s, expected) && strings.Count(s, "Enter a value:") == 1 {
			answer = "yes\n"
			c.approved = true
		}
		c.answered = true
		if answer == "" {
			_ = c.stdin.Close()
			return 0, errors.New("unrecognized migration prompt")
		}
		if _, err := io.WriteString(c.stdin, answer); err != nil {
			return 0, err
		}
		_ = c.stdin.Close()
	}
	return n, nil
}

func DefaultProcess(input io.Reader, terminal io.Writer) RunProcess {
	return func(ctx context.Context, r Request) (Response, error) {
		if r.Timeout <= 0 {
			r.Timeout = 30 * time.Second
		}
		bounded, cancel := context.WithTimeout(ctx, r.Timeout)
		defer cancel()
		cmd := exec.CommandContext(bounded, r.Program, r.Args...)
		// Noninteractive tools have a separate group, so terminal Ctrl-C reaches
		// the worker only. Its cancellation sends the tool exactly one interrupt.
		cleanup := isolateProcess(cmd, !r.Interactive)
		defer cleanup()
		cmd.Dir = r.Dir
		cmd.Env = r.Env
		if cmd.Env == nil {
			cmd.Env = processEnv(os.Environ())
		}
		cmd.Cancel = func() error {
			if cmd.Process == nil {
				return os.ErrProcessDone
			}
			return cmd.Process.Signal(os.Interrupt)
		}
		cmd.WaitDelay = DrainTimeout
		out := &capture{migration: r.Migration}
		cmd.Stdout = out
		cmd.Stderr = io.Discard
		if r.Interactive {
			cmd.Stdin = input
			cmd.Stderr = terminal
		}
		if r.Migration {
			p, err := cmd.StdinPipe()
			if err != nil {
				return Response{}, fail("process_unavailable", "cannot prepare state migration")
			}
			out.stdin = p
			defer p.Close()
		}
		err := cmd.Run()
		out.mu.Lock()
		body := append([]byte(nil), out.buf.Bytes()...)
		migrationApproved := out.approved
		out.mu.Unlock()
		if bounded.Err() != nil {
			return Response{}, bounded.Err()
		}
		if r.Migration && !migrationApproved {
			return Response{}, fail("migration_confirmation", "OpenTofu did not request the expected empty-destination migration; no copy was approved")
		}
		code := 0
		if err != nil {
			var exit *exec.ExitError
			if !errors.As(err, &exit) {
				return Response{}, fail("process_unavailable", "cannot run required setup tool; check installation")
			}
			code = exit.ExitCode()
		}
		return Response{body, code}, nil
	}
}
