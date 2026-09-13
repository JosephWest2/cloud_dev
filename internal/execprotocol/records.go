package execprotocol

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

const MaxRecordBytes = 16 * 1024
const MaxStreamBytes int64 = 1 << 30

var ErrUnsupportedSchema = errors.New("unsupported command result schema")

type Scope struct {
	Account    string `json:"account"`
	Region     string `json:"region"`
	Deployment string `json:"deployment"`
	Owner      string `json:"owner"`
}
type ExecutionDocument struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	ContentSHA256 string `json:"content_sha256"`
	Step          string `json:"step"`
}
type Binding struct {
	CommandID     string            `json:"command_id"`
	Scope         Scope             `json:"scope"`
	InstanceID    string            `json:"instance_id"`
	Document      ExecutionDocument `json:"document"`
	RunnerSHA256  string            `json:"runner_sha256"`
	PayloadSHA256 string            `json:"payload_sha256"`
}
type Workload struct {
	Status   string `json:"status"`
	ExitCode *int   `json:"exit_code"`
	Signal   *int   `json:"signal"`
}
type Stream struct {
	Key    string `json:"key"`
	Bytes  int64  `json:"bytes"`
	SHA256 string `json:"sha256"`
	Upload string `json:"upload"`
}
type Streams struct {
	Stdout Stream `json:"stdout"`
	Stderr Stream `json:"stderr"`
}

// Record represents the six immutable metadata kinds. Validate and DecodeRecord
// reject fields belonging to a different kind; omitted workload numbers are null.
type Record struct {
	SchemaVersion int    `json:"schema_version"`
	Kind          string `json:"kind"`
	Binding
	RetentionDays int       `json:"retention_days,omitempty"`
	SSMCommandID  string    `json:"ssm_command_id,omitempty"`
	SubmittedAt   string    `json:"submitted_at,omitempty"`
	ExpiresAt     string    `json:"expires_at,omitempty"`
	StartedAt     string    `json:"started_at,omitempty"`
	FinishedAt    string    `json:"finished_at,omitempty"`
	Workload      *Workload `json:"workload,omitempty"`
	Capture       string    `json:"capture,omitempty"`
	Streams       *Streams  `json:"streams,omitempty"`
	Publication   string    `json:"publication,omitempty"`
	FinalizedAt   string    `json:"finalized_at,omitempty"`
}

var (
	commandIDPattern = regexp.MustCompile(`^dc1-[0-9a-f]{32}$`)
	ssmIDPattern     = regexp.MustCompile(`^[0-9a-f]{8}(-[0-9a-f]{4}){3}-[0-9a-f]{12}$`)
	hashPattern      = regexp.MustCompile(`^[0-9a-f]{64}$`)
	accountPattern   = regexp.MustCompile(`^[0-9]{12}$`)
	regionPattern    = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)
	labelPattern     = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,22}$`)
	instancePattern  = regexp.MustCompile(`^i-([0-9a-f]{8}|[0-9a-f]{17})$`)
	documentPattern  = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_-]{0,127}$`)
	versionPattern   = regexp.MustCompile(`^[1-9][0-9]*$`)
)

func ValidCommandID(id string) bool    { return commandIDPattern.MatchString(id) }
func ValidSSMCommandID(id string) bool { return ssmIDPattern.MatchString(id) }
func ValidSHA256(s string) bool        { return hashPattern.MatchString(s) }
func (s Scope) Validate() error {
	if !accountPattern.MatchString(s.Account) || !regionPattern.MatchString(s.Region) || !labelPattern.MatchString(s.Deployment) || !labelPattern.MatchString(s.Owner) {
		return errors.New("invalid result scope")
	}
	return nil
}
func BasePrefix(s Scope) string {
	return "results/v1/" + s.Account + "/" + s.Region + "/" + s.Deployment + "/" + s.Owner + "/"
}
func ObjectKey(s Scope, id, name string) string { return BasePrefix(s) + id + "/" + name }
func (b Binding) Validate() error {
	n, err := strconv.ParseInt(b.Document.Version, 10, 64)
	if b.Scope.Validate() != nil || !ValidCommandID(b.CommandID) || !instancePattern.MatchString(b.InstanceID) || !documentPattern.MatchString(b.Document.Name) || !versionPattern.MatchString(b.Document.Version) || err != nil || n <= 0 || b.Document.Step != "execute" || !ValidSHA256(b.Document.ContentSHA256) || !ValidSHA256(b.RunnerSHA256) || !ValidSHA256(b.PayloadSHA256) {
		return errors.New("invalid command result binding")
	}
	return nil
}
func Timestamp(t time.Time) string { return t.UTC().Format(time.RFC3339) }
func ParseTimestamp(s string) (time.Time, error) {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil || t.IsZero() || !strings.HasSuffix(s, "Z") {
		return time.Time{}, errors.New("invalid result timestamp")
	}
	return t, nil
}
func (w Workload) Validate() error {
	if w.Signal != nil && (*w.Signal < 1 || *w.Signal > 64) {
		return errors.New("invalid workload signal")
	}
	switch w.Status {
	case "exited":
		if w.ExitCode == nil || *w.ExitCode < 0 || *w.ExitCode > 255 || w.Signal != nil {
			return errors.New("invalid workload exit")
		}
	case "signaled":
		if w.ExitCode != nil || w.Signal == nil {
			return errors.New("invalid signaled workload")
		}
	case "execution_timeout", "capture_failed":
		if w.ExitCode != nil {
			return errors.New("non-exit workload cannot have an exit code")
		}
	case "runner_setup_failed", "unknown":
		if w.ExitCode != nil || w.Signal != nil {
			return errors.New("unknown process has no numeric status")
		}
	default:
		return errors.New("invalid workload status")
	}
	return nil
}
func (r Record) Validate() error {
	bad := errors.New("invalid command result record")
	if r.SchemaVersion != 1 || r.Binding.Validate() != nil {
		return bad
	}
	if r.Kind == "request" {
		if r.RetentionDays < 2 || r.RetentionDays > 365 || r.SSMCommandID != "" {
			return bad
		}
	} else if r.RetentionDays != 0 || !ValidSSMCommandID(r.SSMCommandID) {
		return bad
	}
	switch r.Kind {
	case "request", "acknowledgement":
		if r.SubmittedAt != "" || r.ExpiresAt != "" || r.StartedAt != "" || r.FinishedAt != "" || r.Workload != nil || r.Capture != "" || r.Streams != nil || r.Publication != "" || r.FinalizedAt != "" {
			return bad
		}
		return nil
	case "started", "outcome", "result":
	default:
		return bad
	}
	submitted, e1 := ParseTimestamp(r.SubmittedAt)
	expires, e2 := ParseTimestamp(r.ExpiresAt)
	lifetime := expires.Sub(submitted)
	if e1 != nil || e2 != nil || lifetime < 48*time.Hour || lifetime > 365*24*time.Hour || lifetime%(24*time.Hour) != 0 {
		return bad
	}
	if r.Kind == "started" {
		started, err := ParseTimestamp(r.StartedAt)
		if err != nil || started.Before(submitted) || !started.Before(expires) || r.FinishedAt != "" || r.Workload != nil || r.Capture != "" || r.Streams != nil || r.Publication != "" || r.FinalizedAt != "" {
			return bad
		}
		return nil
	}
	finished, err := ParseTimestamp(r.FinishedAt)
	if err != nil || finished.Before(submitted) || r.StartedAt != "" || r.Workload == nil || r.Workload.Validate() != nil || (r.Capture != "complete" && r.Capture != "incomplete") || r.Streams == nil {
		return bad
	}
	for name, s := range map[string]Stream{"stdout": r.Streams.Stdout, "stderr": r.Streams.Stderr} {
		if s.Key != ObjectKey(r.Scope, r.CommandID, name) || s.Bytes < 0 || s.Bytes > MaxStreamBytes || !ValidSHA256(s.SHA256) || (s.Upload != "pending" && s.Upload != "complete" && s.Upload != "failed") {
			return bad
		}
		if s.Bytes == 0 && s.SHA256 != Digest(nil) {
			return bad
		}
		if r.Kind == "outcome" && s.Upload != "pending" {
			return bad
		}
	}
	if r.Kind == "outcome" {
		if r.Publication != "" || r.FinalizedAt != "" {
			return bad
		}
		return nil
	}
	finalized, err := ParseTimestamp(r.FinalizedAt)
	if err != nil || finalized.Before(finished) || (r.Publication != "complete" && r.Publication != "incomplete") {
		return bad
	}
	if r.Publication == "complete" && (r.Workload.Status == "unknown" || r.Capture != "complete" || r.Streams.Stdout.Upload != "complete" || r.Streams.Stderr.Upload != "complete") {
		return bad
	}
	return nil
}

func EncodeRecord(r Record) ([]byte, error) {
	if err := r.Validate(); err != nil {
		return nil, err
	}
	b, err := json.Marshal(r)
	if err != nil || len(b) > MaxRecordBytes {
		return nil, errors.New("result record exceeds encoding limits")
	}
	return b, nil
}

// StrictJSON rejects duplicate object keys, invalid Unicode and trailing data at
// every nesting level. The typed decoder additionally rejects unknown fields.
func StrictJSON(b []byte, dst any) error {
	bad := errors.New("invalid JSON fields or encoding")
	if !utf8.Valid(b) || !validEscapedUnicode(b) {
		return bad
	}
	d := json.NewDecoder(bytes.NewReader(b))
	var visit func() error
	visit = func() error {
		t, err := d.Token()
		if err != nil {
			return bad
		}
		delim, ok := t.(json.Delim)
		if !ok {
			return nil
		}
		if delim != '{' && delim != '[' {
			return bad
		}
		seen := map[string]bool{}
		for d.More() {
			if delim == '{' {
				key, err := d.Token()
				if err != nil {
					return bad
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return bad
				}
				seen[name] = true
			}
			if err := visit(); err != nil {
				return err
			}
		}
		_, err = d.Token()
		return err
	}
	if visit() != nil || d.Decode(new(any)) != io.EOF {
		return bad
	}
	d = json.NewDecoder(bytes.NewReader(b))
	d.DisallowUnknownFields()
	if d.Decode(dst) != nil {
		return bad
	}
	return nil
}

func requiredFields(b []byte, names string, nullable string) error {
	var fields map[string]json.RawMessage
	if json.Unmarshal(b, &fields) != nil || fields == nil {
		return errors.New("required result object missing")
	}
	allowed := strings.Fields(names)
	if len(fields) != len(allowed) {
		return errors.New("unexpected result fields")
	}
	for _, name := range allowed {
		v, ok := fields[name]
		if !ok || (bytes.Equal(bytes.TrimSpace(v), []byte("null")) && !strings.Contains(" "+nullable+" ", " "+name+" ")) {
			return errors.New("required result field missing")
		}
	}
	return nil
}
func DecodeRecord(b []byte) (Record, error) {
	var r Record
	bad := errors.New("invalid command result record")
	if len(b) > MaxRecordBytes {
		return Record{}, bad
	}
	var envelope map[string]json.RawMessage
	if StrictJSON(b, &envelope) != nil || envelope == nil {
		return Record{}, bad
	}
	var schema int
	if value, ok := envelope["schema_version"]; !ok || bytes.Equal(bytes.TrimSpace(value), []byte("null")) || json.Unmarshal(value, &schema) != nil {
		return Record{}, bad
	}
	if schema != 1 {
		return Record{}, ErrUnsupportedSchema
	}
	if StrictJSON(b, &r) != nil {
		return Record{}, bad
	}
	fields := "schema_version kind command_id scope instance_id document runner_sha256 payload_sha256"
	switch r.Kind {
	case "request":
		fields += " retention_days"
	case "acknowledgement":
		fields += " ssm_command_id"
	case "started":
		fields += " ssm_command_id submitted_at expires_at started_at"
	case "outcome":
		fields += " ssm_command_id submitted_at expires_at finished_at workload capture streams"
	case "result":
		fields += " ssm_command_id submitted_at expires_at finished_at workload capture streams publication finalized_at"
	default:
		return Record{}, bad
	}
	if requiredFields(b, fields, "") != nil {
		return Record{}, bad
	}
	var raw map[string]json.RawMessage
	_ = json.Unmarshal(b, &raw)
	if requiredFields(raw["scope"], "account region deployment owner", "") != nil || requiredFields(raw["document"], "name version content_sha256 step", "") != nil {
		return Record{}, bad
	}
	if r.Workload != nil {
		if requiredFields(raw["workload"], "status exit_code signal", "exit_code signal") != nil || requiredFields(raw["streams"], "stdout stderr", "") != nil {
			return Record{}, bad
		}
		var streams map[string]json.RawMessage
		_ = json.Unmarshal(raw["streams"], &streams)
		for _, name := range []string{"stdout", "stderr"} {
			if requiredFields(streams[name], "key bytes sha256 upload", "") != nil {
				return Record{}, bad
			}
		}
	}
	if err := r.Validate(); err != nil {
		return Record{}, err
	}
	return r, nil
}
