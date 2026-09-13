package execprotocol

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

func sampleRecord(kind string) Record {
	scope := Scope{"123456789012", "us-east-2", "test", "owner"}
	b := Binding{"dc1-0123456789abcdef0123456789abcdef", scope, "i-0123456789abcdef0", ExecutionDocument{"devbox-test-execution", "1", strings.Repeat("a", 64), "execute"}, strings.Repeat("b", 64), strings.Repeat("c", 64)}
	r := Record{SchemaVersion: 1, Kind: kind, Binding: b}
	if kind == "request" {
		r.RetentionDays = 30
		return r
	}
	r.SSMCommandID = "01234567-89ab-cdef-0123-456789abcdef"
	if kind == "acknowledgement" {
		return r
	}
	submitted := time.Date(2026, 9, 13, 12, 0, 0, 0, time.UTC)
	r.SubmittedAt = Timestamp(submitted)
	r.ExpiresAt = Timestamp(submitted.Add(30 * 24 * time.Hour))
	if kind == "started" {
		r.StartedAt = Timestamp(submitted.Add(time.Second))
		return r
	}
	r.FinishedAt = Timestamp(submitted.Add(2 * time.Second))
	exit := 4
	r.Workload = &Workload{Status: "exited", ExitCode: &exit}
	r.Capture = "complete"
	r.Streams = &Streams{Stdout: Stream{ObjectKey(scope, b.CommandID, "stdout"), 0, Digest(nil), "pending"}, Stderr: Stream{ObjectKey(scope, b.CommandID, "stderr"), 0, Digest(nil), "pending"}}
	if kind == "result" {
		r.Publication = "complete"
		r.FinalizedAt = Timestamp(submitted.Add(3 * time.Second))
		r.Streams.Stdout.Upload = "complete"
		r.Streams.Stderr.Upload = "complete"
	}
	return r
}
func TestRecordKindsAndCompleteSelfContainedResult(t *testing.T) {
	for _, kind := range []string{"request", "acknowledgement", "started", "outcome", "result"} {
		r := sampleRecord(kind)
		b, err := EncodeRecord(r)
		if err != nil {
			t.Fatal(kind, err)
		}
		got, err := DecodeRecord(b)
		if err != nil || !reflect.DeepEqual(got, r) {
			t.Fatalf("%s roundtrip %+v %v", kind, got, err)
		}
		var fields map[string]json.RawMessage
		_ = json.Unmarshal(b, &fields)
		for key := range fields {
			bad := map[string]json.RawMessage{}
			for k, v := range fields {
				bad[k] = v
			}
			delete(bad, key)
			raw, _ := json.Marshal(bad)
			if _, err := DecodeRecord(raw); err == nil {
				t.Fatalf("accepted missing %s.%s", kind, key)
			}
		}
	}
}
func TestRecordRejectsMalformedConflictingAndIncompleteClaims(t *testing.T) {
	r := sampleRecord("result")
	base, _ := EncodeRecord(r)
	for _, bad := range [][]byte{
		append(append([]byte(nil), base...), []byte(` {}`)...),
		bytes.Replace(base, []byte(`"scope":`), []byte(`"SCOPE":`), 1),
		bytes.Replace(base, []byte(`"account":`), []byte(`"ACCOUNT":`), 1),
		bytes.Replace(base, []byte(`"bytes":0`), []byte(`"bytes":null`), 1),
		bytes.Replace(base, []byte(`"signal":null`), []byte(`"signal":null,"signal":null`), 1),
		bytes.Replace(base, []byte(`"schema_version":1`), []byte(`"schema_version":2`), 1),
		bytes.Replace(base, []byte(`"status":"exited"`), []byte(`"status":"\ud800"`), 1),
		bytes.Repeat([]byte(" "), MaxRecordBytes+1),
	} {
		if _, err := DecodeRecord(bad); err == nil {
			t.Fatalf("accepted malformed record %s", bad)
		}
	}
	for _, change := range []func(*Record){
		func(r *Record) { r.Streams.Stdout.Key += "/../stderr" }, func(r *Record) { r.Streams.Stdout.Bytes = -1 }, func(r *Record) { r.Streams.Stdout.Bytes = MaxStreamBytes + 1 },
		func(r *Record) { r.Streams.Stdout.SHA256 = strings.Repeat("0", 64) }, func(r *Record) { r.Streams.Stdout.Upload = "failed" }, func(r *Record) { r.Capture = "incomplete" },
		func(r *Record) { r.Workload = &Workload{Status: "unknown"} }, func(r *Record) { n := 256; r.Workload.ExitCode = &n }, func(r *Record) { n := 9; r.Workload.Signal = &n },
		func(r *Record) { r.SubmittedAt = "2026-09-13T12:00:00+00:00" }, func(r *Record) { r.ExpiresAt = r.SubmittedAt }, func(r *Record) { r.RetentionDays = 30 },
	} {
		r := sampleRecord("result")
		change(&r)
		if _, err := EncodeRecord(r); err == nil {
			t.Fatalf("accepted invalid record %+v", r)
		}
	}
	r = sampleRecord("result")
	r.Capture = "incomplete"
	r.Publication = "incomplete"
	r.Streams.Stderr.Upload = "failed"
	if _, err := EncodeRecord(r); err != nil {
		t.Fatal("honest partial record rejected", err)
	}
}

func TestRecordUnsupportedSchemaRemainsDistinct(t *testing.T) {
	if _, err := DecodeRecord([]byte(`{"schema_version":2,"new_schema_field":true}`)); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("unsupported schema: %v", err)
	}
	s := &reconcileStore{data: []byte(`{"schema_version":2,"new_schema_field":true}`)}
	s.size = int64(len(s.data))
	if _, _, err := ReadRecord(context.Background(), s, "irrelevant"); !errors.Is(err, ErrUnsupportedSchema) {
		t.Fatalf("read lost unsupported schema: %v", err)
	}
}
