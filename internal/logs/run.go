// Package logs recovers durable command results independently of live workers.
package logs

import (
	"context"
	"errors"
	"io"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
	"github.com/JosephWest2/cloud_dev/internal/execution"
	"github.com/JosephWest2/cloud_dev/internal/identity"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

const DefaultTimeout = 20 * time.Second

type Options struct {
	CommandID, Stream, StdoutFile, StderrFile string
	Timeout                                   time.Duration
}

type Service struct {
	Store execprotocol.Store
	SSM   execution.InvocationAPI
}

type Dependencies struct {
	New func(context.Context, config.Config, config.Results) (*Service, error)
}

// Download describes only explicitly selected output. BytesWritten can be
// nonzero on a failed stdout copy; File is set only after a verified export.
type Download struct {
	Destination  string `json:"destination"`
	File         string `json:"file,omitempty"`
	BytesWritten int64  `json:"bytes_written"`
	Verification string `json:"verification"`
}

type Downloads struct {
	Stdout *Download `json:"stdout,omitempty"`
	Stderr *Download `json:"stderr,omitempty"`
}

type Result struct {
	execution.Result
	Encoding     string     `json:"encoding"`
	Verification string     `json:"verification"`
	Downloads    *Downloads `json:"downloads,omitempty"`
}

// New checks identity before constructing resource clients. Recovery loads only
// the retained storage descriptor: it never checks EC2, current runtime pins,
// launch profiles, SSH configuration, or a live foundation policy.
func New(ctx context.Context, c config.Config, storage config.Results) (*Service, error) {
	a, err := identity.Load(ctx, c)
	if err != nil {
		return nil, err
	}
	if err := identity.Verify(ctx, sts.NewFromConfig(a), c.ExpectedAccount); err != nil {
		return nil, err
	}
	return &Service{
		Store: execprotocol.S3Store{Client: s3.NewFromConfig(a), Bucket: storage.Bucket, ExpectedBucketOwner: storage.ExpectedBucketOwner, Prefix: storage.Prefix},
		SSM:   ssm.NewFromConfig(a),
	}, nil
}

func Run(ctx context.Context, path string, overrides config.Overrides, options Options, deps Dependencies, output io.Writer) Result {
	r := Result{Result: execution.Result{SchemaVersion: 1, Command: "logs"}, Encoding: "bytes", Verification: "not_downloaded"}
	finish := func(outcome, code, message string, exit int) Result {
		r.Outcome, r.Code, r.Message, r.ExitCode, r.OK = outcome, code, message, exit, exit == 0
		return r
	}
	bad := func(message string) Result { return finish("config_invalid", "usage_invalid", message, 2) }
	if !execprotocol.ValidCommandID(options.CommandID) {
		return bad("logs requires one public command ID in dc1- followed by 32 lowercase hexadecimal digits")
	}
	r.CommandID = options.CommandID
	if options.Timeout == 0 {
		options.Timeout = DefaultTimeout
	}
	if options.Timeout <= 0 || options.Timeout > 5*time.Minute {
		return bad("--timeout must be a positive duration through 5m")
	}
	if (options.Stream != "" && options.Stream != "stdout" && options.Stream != "stderr") ||
		(options.Stream != "" && (options.StdoutFile != "" || options.StderrFile != "")) {
		return bad("use --stream stdout|stderr or distinct --stdout-file/--stderr-file exports")
	}
	if err := ValidateDestinations(options.StdoutFile, options.StderrFile); err != nil {
		return bad("output destinations must be distinct new files in existing directories; existing files are never replaced")
	}
	if options.Stream != "" && output == nil {
		return finish("write_failed", "output_write_failed", "the selected output stream is unavailable", 1)
	}
	call, cancel := context.WithTimeout(ctx, options.Timeout)
	defer cancel()
	stopped := func() Result {
		if errors.Is(call.Err(), context.Canceled) {
			return finish("interrupted", "interrupted", "local result retrieval stopped; retry logs with the same command ID", 4)
		}
		return finish("timeout", "timeout", "local result retrieval deadline expired; retry logs with the same command ID and a longer --timeout", 4)
	}
	c, err := config.Load(path, overrides)
	if err != nil {
		return finish("config_invalid", "config_invalid", err.Error(), 2)
	}
	r.RecoveryCommand = config.CommandPrefix(path, c) + " logs " + options.CommandID
	storage, err := config.LoadResultManifest(c.Manifest, c)
	if err != nil {
		return finish("config_invalid", "result_manifest_invalid", "cannot validate the retained storage descriptor; select the original matching account, region, deployment and owner configuration", 2)
	}
	if call.Err() != nil {
		return stopped()
	}
	factory := deps.New
	if factory == nil {
		factory = New
	}
	service, err := factory(call, c, storage)
	if call.Err() != nil {
		return stopped()
	}
	if err != nil {
		var failure *identity.Failure
		if errors.As(err, &failure) {
			if failure.Code == "timeout" {
				return finish("timeout", "timeout", "AWS identity verification timed out; retry logs with the selected profile and a longer --timeout", 4)
			}
			return finish("unavailable", failure.Code, failure.Message, 1)
		}
		return finish("unavailable", "service_unavailable", "cannot prepare result retrieval; check the selected AWS profile and connectivity", 1)
	}
	if service == nil || service.Store == nil {
		return finish("unavailable", "service_unavailable", "durable result storage is unavailable", 1)
	}
	recovery := r.RecoveryCommand
	r.Result = execution.Recover(call, service.Store, service.SSM, execprotocol.Scope{Account: c.ExpectedAccount, Region: c.Region, Deployment: c.Deployment, Owner: c.Owner}, options.CommandID, execution.RecoverOptions{})
	r.RecoveryCommand = recovery
	if r.Outcome != "complete" || r.Streams == nil {
		return r
	}
	if options.Stream == "" && options.StdoutFile == "" && options.StderrFile == "" {
		return r
	}
	r.Downloads = &Downloads{}
	for _, selected := range []struct {
		name, file string
		stream     execprotocol.Stream
		slot       **Download
	}{
		{"stdout", options.StdoutFile, r.Streams.Stdout, &r.Downloads.Stdout},
		{"stderr", options.StderrFile, r.Streams.Stderr, &r.Downloads.Stderr},
	} {
		if selected.file == "" && options.Stream != selected.name {
			continue
		}
		download := &Download{Destination: "stdout", Verification: "failed"}
		*selected.slot = download
		var err error
		if selected.file != "" {
			download.Destination = "file"
			download.BytesWritten, err = ExportStream(call, service.Store, selected.stream, selected.file)
			if err == nil {
				download.File = selected.file
			}
		} else {
			download.BytesWritten, err = CopyStream(call, service.Store, selected.stream, output)
		}
		if err != nil {
			r.Verification = "failed"
			if call.Err() != nil {
				return stopped()
			}
			switch {
			case errors.Is(err, context.Canceled):
				return finish("interrupted", "interrupted", "local output retrieval stopped; any emitted stdout bytes are unverified", 4)
			case errors.Is(err, context.DeadlineExceeded):
				return finish("timeout", "timeout", "local output retrieval timed out; retry with a longer --timeout", 4)
			case errors.Is(err, execprotocol.ErrNotFound):
				return finish("incomplete", "output_missing", "an expected output object is missing; the recorded workload status remains known", 1)
			case errors.Is(err, execprotocol.ErrDenied):
				return finish("access_denied", "output_access_denied", "output access was denied; check the selected profile and scoped storage permissions", 1)
			case errors.Is(err, execprotocol.ErrCorrupt):
				return finish("corrupt", "output_corrupt", "output length, checksum or completion evidence differs; any emitted stdout bytes are unverified", 1)
			case errors.Is(err, ErrWrite), errors.Is(err, ErrDestination):
				return finish("write_failed", "output_write_failed", "cannot publish the selected output; check destination permissions and use new file paths", 1)
			case errors.Is(err, execprotocol.ErrCredentialsExpired):
				return finish("unavailable", "credentials_expired", "AWS credentials expired; refresh the selected profile and retry logs", 1)
			case errors.Is(err, execprotocol.ErrCredentialsInvalid):
				return finish("unavailable", "credentials_invalid", "AWS credentials were rejected; verify the selected profile and retry logs", 1)
			case errors.Is(err, execprotocol.ErrCredentialsUnavailable):
				return finish("unavailable", "credentials_unavailable", "AWS credentials could not be refreshed; sign in with the selected profile and retry logs", 1)
			default:
				return finish("unavailable", "output_unavailable", "output retrieval failed; retry logs with the same command ID after checking connectivity", 1)
			}
		}
		download.Verification = "verified"
	}
	r.Verification = "verified"
	return finish("complete", "complete", "all selected output streams were downloaded and verified against the complete result", 0)
}
