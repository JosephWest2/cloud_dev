// Package setup owns guided, explicitly approved foundation provisioning.
// OpenTofu owns infrastructure state; this package only consumes named outputs
// and the action metadata of saved plans.
package setup

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/foundation"
)

const SchemaVersion = 1
const TofuVersion = "1.12.6"
const DrainTimeout = 90 * time.Second

var labelPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,22}$`)
var profilePattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@-]{0,127}$`)
var accountPattern = regexp.MustCompile(`^[0-9]{12}$`)
var idPattern = regexp.MustCompile(`^[0-9a-f]{32}$`)
var digestPattern = regexp.MustCompile(`^[0-9a-f]{64}$`)

type Options struct {
	Version, ConfigPath, ManifestPath, BundlePath, Resume, Status, AWSProfile, Region string
	StageTimeout, HealthTimeout                                                       time.Duration
}

type Inputs struct {
	Account            string `json:"account"`
	Region             string `json:"region"`
	Deployment         string `json:"deployment"`
	Owner              string `json:"owner"`
	SourceProfile      string `json:"source_profile"`
	SetupProfile       string `json:"setup_profile"`
	OperatorProfile    string `json:"operator_profile"`
	Principal          string `json:"principal"`
	SSHKey             string `json:"ssh_key"`
	PublicKey          string `json:"public_key"`
	Bucket             string `json:"bucket"`
	AMI                string `json:"ami"`
	ConfigPath         string `json:"config_path"`
	ManifestPath       string `json:"manifest_path"`
	AWSConfigPath      string `json:"aws_config_path"`
	AWSCredentialsPath string `json:"aws_credentials_path"`
	ExistingOperator   bool   `json:"existing_operator"`
}

func (in Inputs) Config() config.Config {
	return config.Config{SchemaVersion: 1, ExpectedAccount: in.Account, Region: in.Region, Deployment: in.Deployment, Owner: in.Owner, AWSProfile: in.OperatorProfile, Manifest: in.ManifestPath, SSHIdentityFile: in.SSHKey, MaxCount: config.DefaultMaxCount}
}

type Journal struct {
	SchemaVersion   int          `json:"schema_version"`
	ID              string       `json:"id"`
	Version         string       `json:"version"`
	BundleDigest    string       `json:"bundle_digest"`
	TofuDigest      string       `json:"tofu_digest,omitempty"`
	Inputs          Inputs       `json:"inputs"`
	InputDigest     string       `json:"input_digest"`
	Mode            string       `json:"mode"`
	Phase           string       `json:"phase"`
	State           string       `json:"state"`
	PendingMutation string       `json:"pending_mutation,omitempty"`
	Completed       []string     `json:"completed"`
	UpdatedAt       time.Time    `json:"updated_at"`
	EnableRequested *bool        `json:"enable_requested,omitempty"`
	EnabledAfter    time.Time    `json:"enabled_after"`
	Transaction     []FileChange `json:"transaction,omitempty"`
}

type Result struct {
	SchemaVersion int      `json:"schema_version"`
	Command       string   `json:"command"`
	SetupID       string   `json:"setup_id,omitempty"`
	Version       string   `json:"cli_version,omitempty"`
	Scope         Scope    `json:"scope"`
	OK            bool     `json:"ok"`
	ExitCode      int      `json:"exit_code"`
	Code          string   `json:"code"`
	Message       string   `json:"message"`
	Phase         string   `json:"phase"`
	Partial       bool     `json:"partial"`
	Recorded      bool     `json:"recorded"`
	Completed     []string `json:"completed"`
	Installation  string   `json:"installation"`
	Foundation    string   `json:"foundation"`
	Scheduling    string   `json:"scheduling"`
	Recovery      string   `json:"recovery,omitempty"`
	Checks        []Check  `json:"checks"`
}

type Scope struct {
	Account    string `json:"account"`
	Region     string `json:"region"`
	Deployment string `json:"deployment"`
	Owner      string `json:"owner"`
}

type Check struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
}

type Failure struct {
	Code, Message string
	Exit          int
}

func (f *Failure) Error() string      { return f.Message }
func fail(code, message string) error { return &Failure{code, message, 1} }
func invalid(message string) error    { return &Failure{"setup_invalid", message, 2} }
func recoveryError() error {
	return fail("manual_recovery_required", "setup cannot prove one authoritative state; retain this workspace and follow the guided setup recovery guide")
}

type Request struct {
	Program     string
	Args        []string
	Dir         string
	Env         []string
	Timeout     time.Duration
	Interactive bool
	Migration   bool
}
type Response struct {
	Output   []byte
	ExitCode int
}
type RunProcess func(context.Context, Request) (Response, error)

type Cloud interface {
	Caller(context.Context, Inputs, string) (string, error)
	ResolvePrincipal(context.Context, Inputs, string) (string, error)
	Image(context.Context, Inputs) (string, error)
	SpotRole(context.Context, Inputs, bool) (bool, error)
	Bucket(context.Context, Inputs, string) (string, error)
	Verify(context.Context, Inputs, config.Manifest, bool, time.Time) []foundation.Check
}

type Dependencies struct {
	Run                 RunProcess
	Cloud               Cloud
	Input               io.Reader
	Output              io.Writer
	Terminal            func() bool
	Now                 func() time.Time
	StateHome, DataHome string
}

func resultError(r Result, err error) Result {
	r.OK = false
	r.ExitCode, r.Code, r.Message = 1, "setup_failed", "setup did not complete; retain the setup ID and resume after resolving the failed stage"
	var f *Failure
	if errors.As(err, &f) {
		r.ExitCode, r.Code, r.Message = f.Exit, f.Code, f.Message
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		r.ExitCode, r.Code, r.Message = 4, "setup_interrupted", "setup was interrupted; cloud operations may be partial; use the recovery command"
	}
	return r
}

func strictJSON(body []byte, target any) error {
	// Unknown journal/bundle fields must not silently change recovery semantics.
	d := json.NewDecoder(bytes.NewReader(body))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return invalid("invalid or unsupported setup data")
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return invalid("invalid trailing setup data")
	}
	return nil
}
