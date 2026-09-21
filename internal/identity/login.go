package identity

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/smithy-go/logging"
)

type loginFailureKey struct{}

// PrepareLogin refreshes a known browser credential source before command deadlines
// start. Callers must require an interactive terminal and exclude machine output.
// A failed login is carried to Load so each command retains its own result schema.
func PrepareLogin(ctx context.Context, c config.Config, input io.Reader, terminal io.Writer) context.Context {
	err := RefreshLogin(ctx, c, input, terminal)
	if err != nil {
		if ctx.Err() != nil || err == context.DeadlineExceeded || err == context.Canceled {
			err = safeError(err)
		}
		return context.WithValue(ctx, loginFailureKey{}, err)
	}
	return ctx
}

// RefreshLogin is the interactive preflight for setup, which owns its result schema.
func RefreshLogin(ctx context.Context, c config.Config, input io.Reader, terminal io.Writer) error {
	return prepareLogin(ctx, c, input, terminal, runLoginProcess)
}

type loginProcess func(context.Context, []string, io.Reader, io.Writer) (string, error)

// Accept only the export-credentials bridge emitted by setup (and its documented
// unquoted form), never arbitrary shell commands or a guessed --profile argument.
var bridgeCommand = regexp.MustCompile(`^(?:aws|/[^\s"']*/aws|"/[^"\r\n]*/aws") configure export-credentials --profile ([A-Za-z0-9_.@-]+) --format process$`)

func loginSource(ctx context.Context, name string) (string, bool) {
	seen := map[string]bool{}
	for len(seen) < 16 && name != "" && !seen[name] {
		seen[name] = true
		p, err := awsconfig.LoadSharedConfigProfile(ctx, name, func(o *awsconfig.LoadSharedConfigOptions) {
			o.Logger = logging.Nop{}
			if path := os.Getenv("AWS_CONFIG_FILE"); path != "" {
				o.ConfigFiles = []string{path}
			}
			if path := os.Getenv("AWS_SHARED_CREDENTIALS_FILE"); path != "" {
				o.CredentialsFiles = []string{path}
			}
		})
		if err != nil {
			return "", false
		}
		if p.Credentials.HasKeys() || p.CredentialSource != "" || p.WebIdentityTokenFile != "" {
			return "", false
		}
		if p.SourceProfileName != "" {
			name = p.SourceProfileName
			continue
		}
		if p.CredentialProcess != "" {
			match := bridgeCommand.FindStringSubmatch(p.CredentialProcess)
			if match == nil {
				return "", false
			}
			name = match[1]
			continue
		}
		if p.LoginSession != "" {
			return name, false
		}
		if p.SSOSessionName != "" || p.SSOStartURL != "" {
			return name, true
		}
		return "", false
	}
	return "", false
}

func prepareLogin(ctx context.Context, c config.Config, input io.Reader, terminal io.Writer, run loginProcess) error {
	// Do not change the implicit SDK/environment credential chain.
	if c.AWSProfile == "" || ctx.Err() != nil {
		return nil
	}
	source, sso := loginSource(ctx, c.AWSProfile)
	if source == "" {
		return nil
	}
	probe, cancel := context.WithTimeout(ctx, 15*time.Second)
	diagnostic, err := run(probe, []string{"configure", "export-credentials", "--profile", source, "--format", "process"}, nil, nil)
	canceled := probe.Err() != nil
	cancel()
	if err == nil || canceled {
		return nil
	}
	// A network, permission, configuration or missing-tool error is not a reason
	// to start login. Provider diagnostics are classified here, never printed.
	message := strings.ToLower(diagnostic)
	needsLogin := strings.Contains(message, "your session has expired") ||
		strings.Contains(message, "the sso session associated with this profile has expired") ||
		strings.Contains(message, "error loading sso token") ||
		strings.Contains(message, "error loading login session token") ||
		strings.Contains(message, "please reauthenticate with your new password")
	if !needsLogin {
		return nil
	}
	if _, err := fmt.Fprintf(terminal, "devbox: AWS session needs renewal; opening browser login for profile %q.\n", source); err != nil {
		return &Failure{"authentication_failed", "cannot display AWS login instructions"}
	}
	args := []string{"login", "--profile", source}
	if sso {
		args = append([]string{"sso"}, args...)
	}
	login, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	if _, err := run(login, args, input, terminal); err != nil {
		if login.Err() != nil {
			return login.Err()
		}
		return &Failure{"authentication_failed", "AWS login did not complete; retry the command or authenticate the source profile manually"}
	}
	return nil
}

func runLoginProcess(ctx context.Context, args []string, input io.Reader, terminal io.Writer) (string, error) {
	path, err := exec.LookPath("aws")
	if err != nil {
		return "", err
	}
	// Resolve explicitly and execute argv directly, with no shell evaluation.
	path, err = filepath.Abs(path)
	if err != nil {
		return "", err
	}
	cmd := exec.CommandContext(ctx, path, args...)
	cmd.Env = append(os.Environ(), "AWS_PAGER=", "AWS_CLI_AUTO_PROMPT=off")
	cmd.WaitDelay = time.Second
	if terminal != nil {
		cmd.Stdin, cmd.Stdout, cmd.Stderr = input, terminal, terminal
		return "", cmd.Run()
	}
	// export-credentials stdout contains credentials; never capture or display it.
	var diagnostic bytes.Buffer
	cmd.Stderr = &diagnostic
	err = cmd.Run()
	return diagnostic.String(), err
}
