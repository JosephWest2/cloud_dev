package runner

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/execprotocol"
)

type memoryStore struct {
	objects             map[string][]byte
	submitted           time.Time
	puts                []string
	gets                []string
	deny, lost, partial string
}

func (s *memoryStore) Get(ctx context.Context, key string) (execprotocol.Object, error) {
	if ctx.Err() != nil {
		return execprotocol.Object{}, ctx.Err()
	}
	s.gets = append(s.gets, filepath.Base(key))
	b, ok := s.objects[key]
	if !ok {
		return execprotocol.Object{}, execprotocol.ErrNotFound
	}
	return execprotocol.Object{Body: io.NopCloser(bytes.NewReader(b)), Size: int64(len(b)), LastModified: s.submitted}, nil
}
func (s *memoryStore) Put(ctx context.Context, key string, body io.ReadSeeker, size int64, digest string) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	name := filepath.Base(key)
	s.puts = append(s.puts, name)
	if name == s.deny {
		return execprotocol.ErrDenied
	}
	if _, ok := s.objects[key]; ok {
		return execprotocol.ErrConflict
	}
	b, err := io.ReadAll(body)
	if err != nil || int64(len(b)) != size || execprotocol.Digest(b) != digest {
		return execprotocol.ErrCorrupt
	}
	if name == s.partial {
		s.objects[key] = append([]byte(nil), b[:len(b)/2]...)
		return execprotocol.ErrUnavailable
	}
	s.objects[key] = append([]byte(nil), b...)
	if name == s.lost {
		return execprotocol.ErrUnavailable
	}
	return nil
}

type runFixture struct {
	config  WorkerConfig
	input   Input
	deps    Dependencies
	store   *memoryStore
	calls   int
	request execprotocol.Record
	now     time.Time
}

func newRunFixture(t *testing.T, exit int) *runFixture {
	t.Helper()
	f := &runFixture{now: time.Now().UTC().Truncate(time.Second)}
	f.config = WorkerConfig{SchemaVersion: 1, Account: "123456789012", Region: "us-east-2", Deployment: "test", Owner: "owner", Execution: config.Execution{Name: "devbox-test-execution", Version: "1", ContentSHA256: strings.Repeat("a", 64), Step: "execute", RunnerSHA256: strings.Repeat("b", 64), MinimumAgentVersion: "3.3.2746.0"}}
	f.config.Results = config.Results{SchemaVersion: 1, Bucket: "devbox-results-test", ExpectedBucketOwner: f.config.Account, Region: f.config.Region, Prefix: execprotocol.BasePrefix(f.config.Scope()), RetentionDays: 30, PolicySHA256: strings.Repeat("c", 64)}
	p, _ := execprotocol.NewPayload([]string{"program", "--json", ""}, "", 30)
	encoded, _ := execprotocol.EncodePayload(p)
	decoded, _ := base64.StdEncoding.DecodeString(encoded)
	f.input = Input{"dc1-0123456789abcdef0123456789abcdef", "01234567-89ab-cdef-0123-456789abcdef", encoded}
	f.request = execprotocol.Record{SchemaVersion: 1, Kind: "request", Binding: execprotocol.Binding{CommandID: f.input.CommandID, Scope: f.config.Scope(), InstanceID: "i-0123456789abcdef0", Document: execprotocol.ExecutionDocument{Name: f.config.Execution.Name, Version: "1", ContentSHA256: f.config.Execution.ContentSHA256, Step: "execute"}, RunnerSHA256: f.config.Execution.RunnerSHA256, PayloadSHA256: execprotocol.Digest(decoded)}, RetentionDays: 30}
	f.store = &memoryStore{objects: map[string][]byte{}, submitted: f.now.Add(-time.Minute)}
	f.saveRequest(t)
	dir := t.TempDir()
	out, diag := filepath.Join(dir, "stdout"), filepath.Join(dir, "stderr")
	if err := os.WriteFile(out, bytes.Repeat([]byte{0, 255, 'o'}, 30000), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(diag, nil, 0600); err != nil {
		t.Fatal(err)
	}
	f.deps = Dependencies{Store: f.store, Identity: func(context.Context) (Identity, error) {
		return Identity{f.config.Account, f.config.Region, f.request.InstanceID}, nil
	}, SelfSHA256: func() (string, error) { return f.config.Execution.RunnerSHA256, nil }, Now: func() time.Time { return f.now }, Execute: func(context.Context, context.Context, execprotocol.Payload) Captured {
		f.calls++
		return Captured{Workload: execprotocol.Workload{Status: "exited", ExitCode: intValue(exit)}, Capture: "complete", Stdout: out, Stderr: diag, FinishedAt: f.now, Remove: func() {}}
	}}
	return f
}
func (f *runFixture) key(name string) string {
	return execprotocol.ObjectKey(f.config.Scope(), f.input.CommandID, name)
}
func (f *runFixture) saveRequest(t *testing.T) {
	t.Helper()
	b, err := execprotocol.EncodeRecord(f.request)
	if err != nil {
		t.Fatal(err)
	}
	f.store.objects[f.key("request.json")] = b
}
func TestRunPublicationIndependentOfWorkloadFailure(t *testing.T) {
	for _, exit := range []int{0, 1, 2, 4, 255} {
		f := newRunFixture(t, exit)
		r := Run(context.Background(), f.config, f.input, f.deps)
		if r.ExitCode != 0 || r.Code != "runner_result_published" || f.calls != 1 {
			t.Fatalf("publication result %+v calls%d", r, f.calls)
		}
		if !reflect.DeepEqual(f.store.puts, []string{"started.json", "outcome.json", "stdout", "stderr", "result.json"}) {
			t.Fatalf("publication order %v", f.store.puts)
		}
		// Recovery requires only result and stream objects after removing all
		// request/start history, plus trusted storage scope and the public ID.
		delete(f.store.objects, f.key("request.json"))
		delete(f.store.objects, f.key("started.json"))
		delete(f.store.objects, f.key("outcome.json"))
		final, _, err := execprotocol.ReadRecord(context.Background(), f.store, f.key("result.json"))
		if err != nil {
			t.Fatal(err)
		}
		if final.Workload.ExitCode == nil || *final.Workload.ExitCode != exit || final.Publication != "complete" || final.SubmittedAt != execprotocol.Timestamp(f.store.submitted) || final.ExpiresAt != execprotocol.Timestamp(f.store.submitted.Add(30*24*time.Hour)) {
			t.Fatalf("lost workload or server retention %+v", final)
		}
		for _, stream := range []execprotocol.Stream{final.Streams.Stdout, final.Streams.Stderr} {
			data := f.store.objects[stream.Key]
			if int64(len(data)) != stream.Bytes || execprotocol.Digest(data) != stream.SHA256 {
				t.Fatal("stream integrity differs")
			}
		}
	}
}
func TestRunAmbiguousClaimNeverAuthorizesExecution(t *testing.T) {
	for _, mode := range []string{"lost", "conflict", "denied"} {
		f := newRunFixture(t, 0)
		switch mode {
		case "lost":
			f.store.lost = "started.json"
		case "conflict":
			f.store.objects[f.key("started.json")] = []byte("existing")
		case "denied":
			f.store.deny = "started.json"
		}
		r := Run(context.Background(), f.config, f.input, f.deps)
		if r.Code != "runner_claim_unconfirmed" || r.ExitCode != 1 || f.calls != 0 || !reflect.DeepEqual(f.store.puts, []string{"started.json"}) || !reflect.DeepEqual(f.store.gets, []string{"request.json"}) {
			t.Fatalf("unsafe %s claim: %+v puts%v gets%v calls%d", mode, r, f.store.puts, f.store.gets, f.calls)
		}
	}
}
func TestRunPublicationDenialPartialAndLostWrites(t *testing.T) {
	for _, tc := range []struct {
		name, deny, partial, lost string
		complete                  bool
	}{
		{"stdout-denied", "stdout", "", "", false}, {"stderr-denied", "stderr", "", "", false}, {"partial", "", "stdout", "", false}, {"result-denied", "result.json", "", "", false}, {"outcome-denied", "outcome.json", "", "", false}, {"stdout-lost", "", "", "stdout", true}, {"result-lost", "", "", "result.json", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := newRunFixture(t, 4)
			f.store.deny, f.store.partial, f.store.lost = tc.deny, tc.partial, tc.lost
			r := Run(context.Background(), f.config, f.input, f.deps)
			if (r.ExitCode == 0) != tc.complete || f.calls != 1 {
				t.Fatalf("publication %+v", r)
			}
			if r.Record == nil || r.Record.Workload.ExitCode == nil || *r.Record.Workload.ExitCode != 4 {
				t.Fatal("lost known workload outcome")
			}
			if b, ok := f.store.objects[f.key("result.json")]; ok {
				record, err := execprotocol.DecodeRecord(b)
				if err != nil || (record.Publication == "complete") != tc.complete {
					t.Fatalf("false completeness %+v %v", record, err)
				}
			}
		})
	}
}
func TestRunRejectsUntrustedExpiredAndLateRequests(t *testing.T) {
	for _, change := range []func(*runFixture){
		func(f *runFixture) { f.request.PayloadSHA256 = strings.Repeat("d", 64) }, func(f *runFixture) { f.request.Document.Version = "2" }, func(f *runFixture) { f.request.Scope.Owner = "another-owner" },
		func(f *runFixture) { f.store.submitted = f.now.Add(-31 * 24 * time.Hour) }, func(f *runFixture) { f.deps.PreparationDeadline = time.Now().Add(-time.Second) },
		func(f *runFixture) {
			f.deps.SelfSHA256 = func() (string, error) { return strings.Repeat("f", 64), nil }
		}, func(f *runFixture) {
			f.deps.Identity = func(context.Context) (Identity, error) { return Identity{}, errors.New("SECRET") }
		},
	} {
		f := newRunFixture(t, 0)
		change(f)
		f.saveRequest(t)
		r := Run(context.Background(), f.config, f.input, f.deps)
		if r.ExitCode != 1 || f.calls != 0 || len(f.store.puts) != 0 || strings.Contains(r.Code, "SECRET") {
			t.Fatalf("invalid request executed %+v calls%d", r, f.calls)
		}
	}
}

func TestRunCancellationStillPublishesKnownOrUnknownOutcome(t *testing.T) {
	for _, known := range []bool{false, true} {
		f := newRunFixture(t, 0)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		execute := f.deps.Execute
		f.deps.Execute = func(run, prepare context.Context, p execprotocol.Payload) Captured {
			captured := execute(run, prepare, p)
			cancel()
			select {
			case <-run.Done():
			case <-time.After(time.Second):
				t.Fatal("workload did not receive cancellation")
			}
			if !known {
				captured.Workload = execprotocol.Workload{Status: "unknown"}
			}
			return captured
		}
		r := Run(ctx, f.config, f.input, f.deps)
		if r.Record == nil || (r.ExitCode == 0) != known {
			t.Fatalf("cancellation erased publication: %+v", r)
		}
		if _, ok := f.store.objects[f.key("outcome.json")]; !ok {
			t.Fatal("outcome was not published after cancellation")
		}
		if _, ok := f.store.objects[f.key("result.json")]; !ok {
			t.Fatal("result was not published after cancellation")
		}
	}
	f := newRunFixture(t, 0)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	r := Run(ctx, f.config, f.input, f.deps)
	if r.ExitCode != 1 || f.calls != 0 || len(f.store.puts) != 0 {
		t.Fatal("canceled preparation dispatched workload")
	}
}
