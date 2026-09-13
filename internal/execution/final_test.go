package execution

import (
	"bytes"
	"context"
	"errors"
	"io"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
)

type finalStore struct {
	get   func(context.Context, string) (execprotocol.Object, error)
	reads []string
}

func (s *finalStore) Get(ctx context.Context, key string) (execprotocol.Object, error) {
	s.reads = append(s.reads, key)
	return s.get(ctx, key)
}

func (*finalStore) Put(context.Context, string, io.ReadSeeker, int64, string) error {
	panic("final observation must never write or redispatch")
}

func finalRecord() (execprotocol.Record, Submission) {
	binding := execprotocol.Binding{
		CommandID:    "dc1-0123456789abcdef0123456789abcdef",
		Scope:        execprotocol.Scope{Account: "123456789012", Region: "us-east-2", Deployment: "test", Owner: "owner"},
		InstanceID:   "i-0123456789abcdef0",
		Document:     execprotocol.ExecutionDocument{Name: "devbox-test-execution", Version: "1", ContentSHA256: strings.Repeat("a", 64), Step: "execute"},
		RunnerSHA256: strings.Repeat("b", 64), PayloadSHA256: strings.Repeat("c", 64),
	}
	ssmID := "01234567-89ab-cdef-0123-456789abcdef"
	submitted := time.Now().UTC().Truncate(time.Second).Add(-time.Minute)
	exit := 0
	r := execprotocol.Record{
		SchemaVersion: 1, Kind: "result", Binding: binding, SSMCommandID: ssmID,
		SubmittedAt: execprotocol.Timestamp(submitted), ExpiresAt: execprotocol.Timestamp(submitted.Add(30 * 24 * time.Hour)),
		FinishedAt: execprotocol.Timestamp(submitted.Add(time.Second)), FinalizedAt: execprotocol.Timestamp(submitted.Add(2 * time.Second)),
		Workload: &execprotocol.Workload{Status: "exited", ExitCode: &exit}, Capture: "complete", Publication: "complete",
		Streams: &execprotocol.Streams{
			Stdout: execprotocol.Stream{Key: execprotocol.ObjectKey(binding.Scope, binding.CommandID, "stdout"), Bytes: 0, SHA256: execprotocol.Digest(nil), Upload: "complete"},
			Stderr: execprotocol.Stream{Key: execprotocol.ObjectKey(binding.Scope, binding.CommandID, "stderr"), Bytes: 0, SHA256: execprotocol.Digest(nil), Upload: "complete"},
		},
	}
	return r, Submission{Binding: binding, SSMCommandID: ssmID}
}

func finalObject(data []byte) execprotocol.Object {
	return execprotocol.Object{Body: io.NopCloser(bytes.NewReader(data)), Size: int64(len(data)), LastModified: time.Now()}
}

func encodedFinal(t *testing.T, r execprotocol.Record) []byte {
	t.Helper()
	b, err := execprotocol.EncodeRecord(r)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func assertFinalIdentity(t *testing.T, r Result, sub Submission) {
	t.Helper()
	if r.SchemaVersion != 1 || r.Command != "exec" || r.CommandID != sub.Binding.CommandID || r.SSMCommandID != sub.SSMCommandID || r.InstanceID != sub.Binding.InstanceID {
		t.Fatalf("lost command identity: %+v", r)
	}
}

func TestWaitFinalWorkloadAndPublicationPrecedence(t *testing.T) {
	for _, tc := range []struct {
		name, status, capture, publication string
		exit, signal, wantExit             int
		wantOutcome                        string
	}{
		{"exit zero empty streams", "exited", "complete", "complete", 0, 0, 0, "remote_exit"},
		{"exit one", "exited", "complete", "complete", 1, 0, 1, "remote_exit"},
		{"exit two", "exited", "complete", "complete", 2, 0, 2, "remote_exit"},
		{"exit four", "exited", "complete", "complete", 4, 0, 4, "remote_exit"},
		{"exit 255", "exited", "complete", "complete", 255, 0, 255, "remote_exit"},
		{"signal", "signaled", "complete", "complete", 0, 15, 143, "remote_signal"},
		{"execution timeout", "execution_timeout", "complete", "complete", 0, 9, 4, "execution_timeout"},
		{"execution timeout incomplete", "execution_timeout", "incomplete", "incomplete", 0, 9, 4, "execution_timeout"},
		{"capture failure preserves exit", "exited", "incomplete", "incomplete", 4, 0, 1, "result_incomplete"},
		{"upload failure overrides success", "exited", "complete", "incomplete", 0, 0, 1, "result_incomplete"},
		{"capture failure", "capture_failed", "incomplete", "incomplete", 0, 9, 1, "result_incomplete"},
		{"runner setup failure", "runner_setup_failed", "complete", "complete", 0, 0, 1, "setup_failed"},
		{"unknown workload", "unknown", "complete", "incomplete", 0, 0, 1, "result_incomplete"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record, sub := finalRecord()
			record.Workload = &execprotocol.Workload{Status: tc.status}
			if tc.status == "exited" {
				record.Workload.ExitCode = &tc.exit
			}
			if tc.signal != 0 {
				record.Workload.Signal = &tc.signal
			}
			record.Capture, record.Publication = tc.capture, tc.publication
			if tc.publication == "incomplete" {
				record.Streams.Stderr.Upload = "failed"
			}
			data := encodedFinal(t, record)
			store := &finalStore{get: func(context.Context, string) (execprotocol.Object, error) { return finalObject(data), nil }}
			got := WaitFinal(context.Background(), store, sub, 0)
			assertFinalIdentity(t, got, sub)
			if got.Outcome != tc.wantOutcome || got.Code != tc.wantOutcome || got.ExitCode != tc.wantExit || got.OK != (tc.wantOutcome == "remote_exit" && tc.wantExit == 0) {
				t.Fatalf("wrong workload result: %+v", got)
			}
			if !reflect.DeepEqual(got.Workload, record.Workload) || !reflect.DeepEqual(got.Streams, record.Streams) || got.Capture != record.Capture || got.Publication != record.Publication {
				t.Fatalf("lost trusted metadata: %+v", got)
			}
			if len(store.reads) != 1 || store.reads[0] != execprotocol.ObjectKey(sub.Binding.Scope, sub.Binding.CommandID, "result.json") {
				t.Fatalf("observation must only read final metadata: %v", store.reads)
			}
		})
	}
}

func TestWaitFinalRejectsMismatchedBindings(t *testing.T) {
	for _, tc := range []struct {
		name   string
		change func(*execprotocol.Record)
	}{
		{"instance", func(r *execprotocol.Record) { r.InstanceID = "i-00000000" }},
		{"document name", func(r *execprotocol.Record) { r.Document.Name += "-other" }},
		{"document version", func(r *execprotocol.Record) { r.Document.Version = "2" }},
		{"document content", func(r *execprotocol.Record) { r.Document.ContentSHA256 = strings.Repeat("d", 64) }},
		{"runner", func(r *execprotocol.Record) { r.RunnerSHA256 = strings.Repeat("d", 64) }},
		{"payload", func(r *execprotocol.Record) { r.PayloadSHA256 = strings.Repeat("d", 64) }},
		{"SSM command", func(r *execprotocol.Record) { r.SSMCommandID = "00000000-0000-0000-0000-000000000000" }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			record, sub := finalRecord()
			tc.change(&record)
			data := encodedFinal(t, record)
			store := &finalStore{get: func(context.Context, string) (execprotocol.Object, error) { return finalObject(data), nil }}
			got := WaitFinal(context.Background(), store, sub, 0)
			assertFinalIdentity(t, got, sub)
			if got.Outcome != "result_corrupt" || got.ExitCode != 1 || got.OK || got.Workload != nil || got.Streams != nil || got.Publication != "" {
				t.Fatalf("trusted mismatched metadata: %+v", got)
			}
		})
	}
}

func TestWaitFinalRejectsInvalidRecords(t *testing.T) {
	record, sub := finalRecord()
	base := encodedFinal(t, record)
	outcome := record
	outcome.Kind, outcome.Publication, outcome.FinalizedAt = "outcome", "", ""
	streams := *record.Streams
	streams.Stdout.Upload, streams.Stderr.Upload = "pending", "pending"
	outcome.Streams = &streams
	for _, tc := range []struct {
		name, outcome string
		data          []byte
	}{
		{"empty", "result_corrupt", nil},
		{"malformed", "result_corrupt", []byte(`{"SECRET":`)},
		{"unknown field", "result_corrupt", bytes.Replace(base, []byte(`"kind":`), []byte(`"SECRET":true,"kind":`), 1)},
		{"duplicate field", "result_corrupt", bytes.Replace(base, []byte(`"schema_version":1`), []byte(`"schema_version":1,"schema_version":1`), 1)},
		{"case alias", "result_corrupt", bytes.Replace(base, []byte(`"scope":`), []byte(`"SCOPE":`), 1)},
		{"unknown schema", "unsupported_schema", []byte(`{"schema_version":2,"new_schema_field":true}`)},
		{"trailing object", "result_corrupt", append(append([]byte{}, base...), []byte(` {}`)...)},
		{"oversized", "result_corrupt", bytes.Repeat([]byte(" "), execprotocol.MaxRecordBytes+1)},
		{"outcome in result object", "result_corrupt", encodedFinal(t, outcome)},
		{"wrong command key", "result_corrupt", bytes.ReplaceAll(base, []byte(sub.Binding.CommandID), []byte("dc1-00000000000000000000000000000000"))},
		{"wrong scope key", "result_corrupt", bytes.ReplaceAll(base, []byte("123456789012"), []byte("000000000000"))},
	} {
		t.Run(tc.name, func(t *testing.T) {
			store := &finalStore{get: func(context.Context, string) (execprotocol.Object, error) { return finalObject(tc.data), nil }}
			got := WaitFinal(context.Background(), store, sub, 0)
			assertFinalIdentity(t, got, sub)
			if got.Outcome != tc.outcome || got.Code != tc.outcome || got.ExitCode != 1 || got.OK || got.Workload != nil || got.Streams != nil || strings.Contains(got.Message, "SECRET") {
				t.Fatalf("invalid or unredacted result: %+v", got)
			}
		})
	}
	t.Run("truncated metadata body", func(t *testing.T) {
		store := &finalStore{get: func(context.Context, string) (execprotocol.Object, error) {
			object := finalObject(base)
			object.Size++
			return object, nil
		}}
		got := WaitFinal(context.Background(), store, sub, 0)
		if got.Outcome != "result_corrupt" || got.ExitCode != 1 || got.Workload != nil {
			t.Fatalf("accepted truncated metadata: %+v", got)
		}
	})
}

func TestWaitFinalExpiredRetainsOnlyValidatedMetadata(t *testing.T) {
	record, sub := finalRecord()
	submitted := time.Now().UTC().Truncate(time.Second).Add(-31 * 24 * time.Hour)
	record.SubmittedAt = execprotocol.Timestamp(submitted)
	record.ExpiresAt = execprotocol.Timestamp(submitted.Add(30 * 24 * time.Hour))
	record.FinishedAt = execprotocol.Timestamp(submitted.Add(time.Second))
	record.FinalizedAt = execprotocol.Timestamp(submitted.Add(2 * time.Second))
	data := encodedFinal(t, record)
	store := &finalStore{get: func(context.Context, string) (execprotocol.Object, error) { return finalObject(data), nil }}
	got := WaitFinal(context.Background(), store, sub, 0)
	assertFinalIdentity(t, got, sub)
	if got.Outcome != "expired" || got.ExitCode != 1 || got.OK || !reflect.DeepEqual(got.Workload, record.Workload) {
		t.Fatalf("expired result: %+v", got)
	}
}

func TestWaitFinalMissingRetriesWithoutMutation(t *testing.T) {
	record, sub := finalRecord()
	data := encodedFinal(t, record)
	reads := 0
	store := &finalStore{get: func(context.Context, string) (execprotocol.Object, error) {
		reads++
		if reads < 3 {
			return execprotocol.Object{}, execprotocol.ErrNotFound
		}
		return finalObject(data), nil
	}}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := WaitFinal(ctx, store, sub, time.Nanosecond)
	if !got.OK || reads != 3 {
		t.Fatalf("eventual finalization: %+v reads=%d", got, reads)
	}
}

func TestWaitFinalReadFailuresAndContextBoundaries(t *testing.T) {
	_, sub := finalRecord()
	for _, err := range []error{execprotocol.ErrDenied, execprotocol.ErrUnavailable, errors.New("SECRET")} {
		store := &finalStore{get: func(context.Context, string) (execprotocol.Object, error) { return execprotocol.Object{}, err }}
		got := WaitFinal(context.Background(), store, sub, 0)
		assertFinalIdentity(t, got, sub)
		if got.Outcome != "api_failed" || got.ExitCode != 1 || got.OK || len(store.reads) != 1 || strings.Contains(got.Message, "SECRET") {
			t.Fatalf("read failure: %+v", got)
		}
	}
	t.Run("missing under deadline", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		store := &finalStore{get: func(context.Context, string) (execprotocol.Object, error) {
			return execprotocol.Object{}, execprotocol.ErrNotFound
		}}
		got := WaitFinal(ctx, store, sub, time.Hour)
		assertFinalIdentity(t, got, sub)
		if got.Outcome != "observation_timeout" || got.ExitCode != 4 || got.Workload != nil || len(store.reads) > 1 {
			t.Fatalf("bounded missing observation: %+v", got)
		}
	})
	t.Run("cancel before read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		store := &finalStore{get: func(context.Context, string) (execprotocol.Object, error) {
			t.Fatal("read after cancellation")
			return execprotocol.Object{}, nil
		}}
		got := WaitFinal(ctx, store, sub, 0)
		if got.Outcome != "interrupted" || got.ExitCode != 4 {
			t.Fatalf("cancelled observation: %+v", got)
		}
	})
	t.Run("cancel during read", func(t *testing.T) {
		ctx, cancel := context.WithCancel(context.Background())
		store := &finalStore{get: func(context.Context, string) (execprotocol.Object, error) {
			cancel()
			return execprotocol.Object{}, execprotocol.ErrUnavailable
		}}
		got := WaitFinal(ctx, store, sub, 0)
		if got.Outcome != "interrupted" || got.ExitCode != 4 {
			t.Fatalf("lost read cancellation: %+v", got)
		}
	})
	t.Run("deadline while read blocked", func(t *testing.T) {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
		defer cancel()
		store := &finalStore{get: func(ctx context.Context, _ string) (execprotocol.Object, error) {
			<-ctx.Done()
			return execprotocol.Object{}, ctx.Err()
		}}
		got := WaitFinal(ctx, store, sub, 0)
		if got.Outcome != "observation_timeout" || got.ExitCode != 4 {
			t.Fatalf("unbounded read: %+v", got)
		}
	})
	t.Run("validated finalization wins simultaneous cancellation", func(t *testing.T) {
		record, sub := finalRecord()
		data := encodedFinal(t, record)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		store := &finalStore{get: func(context.Context, string) (execprotocol.Object, error) { cancel(); return finalObject(data), nil }}
		got := WaitFinal(ctx, store, sub, 0)
		if !got.OK || got.Outcome != "remote_exit" {
			t.Fatalf("discarded validated finalization: %+v", got)
		}
	})
}
