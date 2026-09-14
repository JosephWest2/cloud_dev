package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/doctor"
	"github.com/JosephWest2/cloud_dev/internal/lifecycle"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
)

func cliBatchConfig(t *testing.T, maximum int) string {
	t.Helper()
	testutil.IsolateAWS(t)
	path := testutil.Setup(t)
	data := testutil.Config
	if maximum > 0 {
		data += fmt.Sprintf("max_count = %d\n", maximum)
	}
	testutil.Write(t, path, data)
	c, err := config.Load(path, config.Overrides{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := config.LoadProfile("")
	if err != nil {
		t.Fatal(err)
	}
	var m config.Manifest
	if err = json.Unmarshal([]byte(testutil.Manifest), &m); err != nil {
		t.Fatal(err)
	}
	m.SchemaVersion = 5
	m.SubnetIDs = []string{"subnet-12345678", "subnet-87654321"}
	m.Subnets = []config.Subnet{{ID: m.SubnetIDs[0], AvailabilityZone: "us-east-2a"}, {ID: m.SubnetIDs[1], AvailabilityZone: "us-east-2b"}}
	for _, instanceType := range p.InstanceTypes {
		m.CompatiblePools = append(m.CompatiblePools, config.CompatiblePool{InstanceType: instanceType, Architecture: "x86_64", SubnetIDs: m.SubnetIDs})
	}
	image := m.Images["agent"]
	image.RootDisk = &config.RootDisk{SizeGB: p.DiskGB, Type: "gp3", Encrypted: true, DeleteOnTermination: true}
	image.MinimumRootDiskGB = 8
	m.Images["agent"] = image
	m.LaunchLedger = &config.LaunchLedger{SchemaVersion: 1, Bucket: m.Results.Bucket, ExpectedBucketOwner: m.Account, Region: m.Region, Prefix: config.LaunchLedgerPrefix(c), PolicySHA256: m.Results.PolicySHA256}
	encoded, err := json.Marshal(m)
	if err != nil {
		t.Fatal(err)
	}
	testutil.Write(t, filepath.Join(filepath.Dir(path), "deployment.json"), string(encoded))
	if _, err = config.LoadManifest(c.Manifest, c, p); err != nil {
		t.Fatal(err)
	}
	return path
}

func decodeCLIBatch(t *testing.T, data []byte) lifecycle.BatchResult {
	t.Helper()
	var result lifecycle.BatchResult
	decoder := json.NewDecoder(bytes.NewReader(data))
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("batch JSON: %v: %s", err, data)
	}
	if decoder.Decode(new(any)) != io.EOF {
		t.Fatalf("stdout contained more than one JSON result: %s", data)
	}
	if result.SchemaVersion != 2 || result.Command != "up" || result.Workers == nil || result.Attempts == nil || result.Errors == nil {
		t.Fatalf("invalid batch envelope: %+v", result)
	}
	return result
}

func TestPublicBatchLaunchSyntaxResolvesBeforeAWS(t *testing.T) {
	for _, tc := range []struct {
		name, group, base, market string
		args                      []string
		count                     int
	}{
		{name: "defaults", args: []string{"up", "agent"}, base: "agent", count: 1, market: "spot"},
		{name: "group default base", args: []string{"up", "agent", "--count", "2", "--group", "research"}, group: "research", base: "research", count: 2, market: "spot"},
		{name: "named group", args: []string{"up", "agent", "--name", "workers", "--group", "research", "--count=3"}, group: "research", base: "workers", count: 3, market: "spot"},
		{name: "on-demand group", args: []string{"up", "agent", "--on-demand", "--count", "2", "--group", "research"}, group: "research", base: "research", count: 2, market: "on-demand"},
		{name: "legacy syntax on v5", args: []string{"up", "agent", "--on-demand", "--name", "worker"}, base: "worker", count: 1, market: "on-demand"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			path := cliBatchConfig(t, 3)
			store := lifecycle.Store{Dir: t.TempDir()}
			calls := 0
			var out, diag bytes.Buffer
			deps := lifecycle.Dependencies{Store: &store, New: func(_ context.Context, c config.Config) (*lifecycle.Service, error) {
				calls++
				if c.MaxCount != 3 || c.ExpectedAccount != "123456789012" || c.Owner != "test-owner" || c.Region != "us-east-2" {
					t.Fatalf("incorrect resolved scope %+v", c)
				}
				return nil, errors.New("PRIVATE factory transport detail")
			}}
			args := append(append([]string{}, tc.args...), "--config", path, "--json")
			code := RunWithLifecycle(context.Background(), args, &out, &diag, doctor.Dependencies{}, deps)
			result := decodeCLIBatch(t, out.Bytes())
			plan := result.Plan
			if code != 1 || calls != 1 || result.OK || result.ExitCode != code || result.Code != "service_unavailable" || plan.SchemaVersion != 1 || plan.RequestedCount != tc.count || result.RequestedCount != tc.count || plan.Group != tc.group || plan.BaseName != tc.base || plan.Market != tc.market || plan.Profile != "agent" || len(plan.Choices) < 2 || result.RequestID == "" || plan.RequestID != result.RequestID {
				t.Fatalf("unresolved launch selection exit=%d calls=%d result=%+v", code, calls, result)
			}
			if strings.Contains(out.String()+diag.String(), "PRIVATE") {
				t.Fatal("raw AWS construction error leaked")
			}
		})
	}
}

func TestPublicBatchCountCapPrecedesAWSAndProfileReads(t *testing.T) {
	for _, maximum := range []int{0, 2} {
		t.Run(fmt.Sprint(maximum), func(t *testing.T) {
			path := cliBatchConfig(t, maximum)
			count := maximum + 1
			if maximum == 0 {
				count = config.DefaultMaxCount + 1
			}
			// The configured count limit is independent of a mutable profile or
			// the availability of AWS services.
			data := testutil.Config + "profile_file='missing-profile.toml'\n"
			if maximum > 0 {
				data += fmt.Sprintf("max_count=%d\n", maximum)
			}
			testutil.Write(t, path, data)
			var out, diag bytes.Buffer
			code := RunWithLifecycle(context.Background(), []string{"up", "agent", "--count", fmt.Sprint(count), "--group", "research", "--config", path, "--json"}, &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{New: func(context.Context, config.Config) (*lifecycle.Service, error) {
				t.Fatal("over-limit selection reached AWS")
				return nil, nil
			}})
			result := decodeCLIBatch(t, out.Bytes())
			if code != 2 || result.ExitCode != 2 || result.Code != "count_invalid" || result.OK || len(result.Workers) != 0 || result.Plan.SchemaVersion != 0 {
				t.Fatalf("count was not checked first: %+v", result)
			}
		})
	}
}

func TestPublicSharedRecoverySyntaxUsesSchemaTwo(t *testing.T) {
	request, attempt := strings.Repeat("a", 32), strings.Repeat("b", 32)
	for _, args := range [][]string{{"up", "--resume", request}, {"up", "--retry-missing", request, "--after", attempt}} {
		t.Run(strings.Join(args, " "), func(t *testing.T) {
			path := cliBatchConfig(t, 2)
			store := lifecycle.Store{Dir: t.TempDir()}
			calls := 0
			var out, diag bytes.Buffer
			code := RunWithLifecycle(context.Background(), append(args, "--config", path, "--json"), &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{Store: &store, New: func(context.Context, config.Config) (*lifecycle.Service, error) {
				calls++
				return nil, errors.New("PRIVATE factory failure")
			}})
			result := decodeCLIBatch(t, out.Bytes())
			if code != 1 || calls != 1 || result.RequestID != request || result.Code != "service_unavailable" || strings.Contains(out.String()+diag.String(), "PRIVATE") {
				t.Fatalf("shared recovery parsing failed: exit=%d calls=%d result=%+v", code, calls, result)
			}
		})
	}
}

type groupCLIInventory struct {
	*ec2.Client
	t     *testing.T
	calls int
}

func (api *groupCLIInventory) DescribeInstances(_ context.Context, in *ec2.DescribeInstancesInput, _ ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	api.calls++
	filters := map[string]string{}
	for _, filter := range in.Filters {
		if len(filter.Values) != 1 {
			api.t.Fatalf("unexpected filter %+v", filter)
		}
		filters[aws.ToString(filter.Name)] = filter.Values[0]
	}
	if len(in.InstanceIds) != 0 || len(filters) != 4 || filters["tag:ManagedBy"] != "devbox" || filters["tag:Deployment"] != "test" || filters["tag:Owner"] != "test-owner" || filters["tag:Group"] != "research" {
		api.t.Fatalf("group selector lost between CLI and AWS: %+v", in)
	}
	return &ec2.DescribeInstancesOutput{}, nil
}

func TestPublicGroupListReachesScopedCloudInventory(t *testing.T) {
	path := cliBatchConfig(t, 2)
	api := &groupCLIInventory{t: t}
	var out, diag bytes.Buffer
	code := RunWithLifecycle(context.Background(), []string{"ls", "--group", "research", "--config", path, "--json"}, &out, &diag, doctor.Dependencies{}, lifecycle.Dependencies{New: func(_ context.Context, c config.Config) (*lifecycle.Service, error) {
		return &lifecycle.Service{API: api, Scope: c}, nil
	}})
	var result lifecycle.Result
	decoder := json.NewDecoder(&out)
	if err := decoder.Decode(&result); err != nil || decoder.Decode(new(any)) != io.EOF {
		t.Fatalf("invalid group inventory JSON: %v", err)
	}
	if code != 0 || !result.OK || result.SchemaVersion != 2 || result.Command != "ls" || result.Status != "inventory" || api.calls != 1 || result.Instances == nil || diag.Len() != 0 {
		t.Fatalf("group inventory result=%+v exit=%d calls=%d stderr=%s", result, code, api.calls, &diag)
	}
}

func cliBatchEmissionResult(status string, exit int, missing *int) lifecycle.BatchResult {
	request, attempt := strings.Repeat("a", 32), strings.Repeat("b", 32)
	return lifecycle.BatchResult{SchemaVersion: 2, Command: "up", OK: exit == 0, ExitCode: exit, Code: status, Message: "Controlled batch result.", BatchOutcome: lifecycle.BatchOutcome{
		RequestID: request, Status: status, RequestedCount: 2, FulfilledCount: 2, ReadyCount: 2, MissingCount: missing,
		Plan:     lifecycle.LaunchPlan{SchemaVersion: 1, RequestID: request, Profile: "agent", Region: "us-east-2", Group: "research", BaseName: "workers", Market: "spot", RequestedCount: 2},
		Attempts: []lifecycle.AttemptOutcome{{AttemptID: attempt, RequestedCount: 2, FulfilledCount: 2, Status: "complete", InstanceIDs: []string{"i-0123456789abcdef0", "i-0123456789abcdef1"}, MissingCount: missing, Errors: []lifecycle.ResourceError{}}},
		Workers: []lifecycle.WorkerOutcome{
			{Instance: lifecycle.Instance{ID: "i-0123456789abcdef0", Name: "workers-i-0123456789abcdef0", Group: "research", BaseName: "workers", AttemptID: attempt, RequestID: request, Type: "c7i.2xlarge", SubnetID: "subnet-12345678", AvailabilityZone: "us-east-2a", Market: "spot", State: "running", SSM: "online", Bootstrap: "complete", Readiness: "ready", Volumes: []lifecycle.Volume{{ID: "vol-0123456789abcdef0", Root: true, Deletion: "not_observed"}}}, Status: "allocated"},
			{Instance: lifecycle.Instance{ID: "i-0123456789abcdef1", Name: "workers-i-0123456789abcdef1", Group: "research", BaseName: "workers", AttemptID: attempt, RequestID: request, Type: "c7i.2xlarge", SubnetID: "subnet-87654321", AvailabilityZone: "us-east-2b", Market: "spot", State: "running", SSM: "online", Bootstrap: "complete", Readiness: "ready", Volumes: []lifecycle.Volume{{ID: "vol-0123456789abcdef1", Root: true, Deletion: "not_observed"}}}, Status: "allocated"},
		},
		Errors: []lifecycle.ResourceError{}, ResumeCommand: "devbox --config config.toml up --resume " + request,
	}}
}

func TestEmitBatchPreservesOneEnvelopeOutcomesAndRecoveryIDs(t *testing.T) {
	zero, one := 0, 1
	for _, tc := range []struct {
		status  string
		exit    int
		missing *int
	}{
		{"ready", 0, &zero},
		{"partial_capacity", 3, &one},
		{"allocation_unknown", 1, nil},
		{"readiness_failed", 3, &zero},
		{"operation_timeout", 4, &zero},
	} {
		t.Run(tc.status, func(t *testing.T) {
			result := cliBatchEmissionResult(tc.status, tc.exit, tc.missing)
			if tc.status == "partial_capacity" {
				result.FulfilledCount, result.ReadyCount = 1, 1
				result.Workers = result.Workers[:1]
				result.RetryCommand = "devbox up --retry-missing " + result.RequestID + " --after " + result.Attempts[0].AttemptID
			}
			if tc.status == "readiness_failed" || tc.status == "operation_timeout" {
				result.ReadyCount = 1
				result.Workers[1].Readiness = "unknown"
			}
			var out, diag bytes.Buffer
			code := emitBatch(result, "devbox --config config.toml", true, &out, &diag)
			decoded := decodeCLIBatch(t, out.Bytes())
			if code != tc.exit || decoded.ExitCode != tc.exit || decoded.Status != tc.status || decoded.ReadyCount != result.ReadyCount || decoded.FulfilledCount != result.FulfilledCount || len(decoded.Workers) != len(result.Workers) || (decoded.MissingCount == nil) != (tc.missing == nil) {
				t.Fatalf("batch emission changed outcome exit=%d result=%+v", code, decoded)
			}
			if tc.exit == 0 && diag.Len() != 0 {
				t.Fatalf("successful output had diagnostics: %s", &diag)
			}
			if tc.exit != 0 {
				for _, worker := range result.Workers {
					if !strings.Contains(diag.String(), worker.ID) || !strings.Contains(diag.String(), worker.Volumes[0].ID) {
						t.Fatalf("lost recovery IDs: %s", &diag)
					}
				}
				if !strings.Contains(diag.String(), result.ResumeCommand) || result.RetryCommand != "" && !strings.Contains(diag.String(), result.RetryCommand) {
					t.Fatalf("lost recovery command: %s", &diag)
				}
			}
		})
	}
}

type failingBatchOutput struct{ bytes.Buffer }

func (w *failingBatchOutput) Write(data []byte) (int, error) {
	n := min(7, len(data))
	w.Buffer.Write(data[:n])
	return n, errors.New("PRIVATE output device detail")
}

func TestEmitBatchOutputFailureRetainsEveryIdentity(t *testing.T) {
	zero := 0
	for _, jsonMode := range []bool{true, false} {
		t.Run(fmt.Sprint(jsonMode), func(t *testing.T) {
			result := cliBatchEmissionResult("ready", 0, &zero)
			out := &failingBatchOutput{}
			var diag bytes.Buffer
			code := emitBatch(result, "devbox --config config.toml", jsonMode, out, &diag)
			if code != 1 || !strings.Contains(diag.String(), "cannot write command result") || strings.Contains(diag.String()+out.String(), "PRIVATE") {
				t.Fatalf("output failure was not sanitized exit=%d stderr=%s", code, &diag)
			}
			for _, worker := range result.Workers {
				if !strings.Contains(diag.String(), worker.ID) || !strings.Contains(diag.String(), worker.Volumes[0].ID) || !strings.Contains(diag.String(), "devbox --config config.toml down "+worker.ID) {
					t.Fatalf("missing recovery after output failure: %s", &diag)
				}
			}
			if !strings.Contains(diag.String(), result.ResumeCommand) {
				t.Fatalf("missing request recovery after output failure: %s", &diag)
			}
		})
	}
}
