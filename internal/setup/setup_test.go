package setup

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/foundation"
	"github.com/JosephWest2/cloud_dev/internal/testutil"
)

func mustWrite(t *testing.T, path string, body []byte) {
	t.Helper()
	if err := atomicWrite(path, body); err != nil {
		t.Fatal(err)
	}
}
func fixtureManifest(t *testing.T) config.Manifest {
	t.Helper()
	var m config.Manifest
	if err := json.Unmarshal([]byte(testutil.Manifest), &m); err != nil {
		t.Fatal(err)
	}
	m.SchemaVersion = 6
	m.Subnets = []config.Subnet{{ID: m.SubnetIDs[0], AvailabilityZone: "us-east-2a"}}
	p, err := config.LoadProfile("")
	if err != nil {
		t.Fatal(err)
	}
	for _, typ := range p.InstanceTypes {
		m.CompatiblePools = append(m.CompatiblePools, config.CompatiblePool{InstanceType: typ, Architecture: "x86_64", SubnetIDs: m.SubnetIDs})
	}
	img := m.Images["agent"]
	img.RootDisk = &config.RootDisk{SizeGB: 100, Type: "gp3", Encrypted: true, DeleteOnTermination: true}
	img.MinimumRootDiskGB = 8
	m.Images["agent"] = img
	m.LaunchLedger = &config.LaunchLedger{SchemaVersion: 1, Bucket: m.Results.Bucket, ExpectedBucketOwner: m.Account, Region: m.Region, Prefix: "launches/v2/123456789012/us-east-2/test/test-owner/", PolicySHA256: m.Results.PolicySHA256}
	return m
}

func fixtureBundle(t *testing.T, dir string) {
	t.Helper()
	b := Bundle{SchemaVersion: 1, Version: "0.0.0", SourceCommit: strings.Repeat("a", 40), Files: map[string]string{}}
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	for _, sub := range []string{"state-bootstrap", "foundation"} {
		entries, err := os.ReadDir(filepath.Join(root, "infra", sub))
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}
			name := entry.Name()
			if !(strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".example") || strings.HasSuffix(name, ".sh") || name == ".terraform.lock.hcl") {
				continue
			}
			rel := filepath.Join("infra", sub, name)
			body, err := os.ReadFile(filepath.Join(root, rel))
			if err != nil {
				t.Fatal(err)
			}
			mustWrite(t, filepath.Join(dir, rel), body)
			b.Files[rel] = hash(body)
		}
	}
	for _, rel := range []string{"bin/devbox-runner-linux-amd64", "bin/devbox-cleanup-linux-amd64.zip"} {
		body := []byte("offline artifact fixture")
		mustWrite(t, filepath.Join(dir, rel), body)
		b.Files[rel] = hash(body)
	}
	mustWrite(t, filepath.Join(dir, "bundle.json"), encodeFile(b))
}

type fakeCloud struct {
	t                                     *testing.T
	in                                    Inputs
	bucket, migrated, foundation, enabled bool
	calls                                 []string
	healthErr                             error
	caller                                string
	plans                                 map[string][]byte
	mutationHook                          func(string) error
}

func (f *fakeCloud) Caller(_ context.Context, in Inputs, profile string) (string, error) {
	f.in = in
	f.calls = append(f.calls, "identity")
	if f.caller != "" {
		return f.caller, nil
	}
	return "arn:aws:iam::123456789012:user/setup", nil
}
func (f *fakeCloud) ResolvePrincipal(context.Context, Inputs, string) (string, error) {
	return "arn:aws:iam::123456789012:user/setup", nil
}
func (f *fakeCloud) Image(context.Context, Inputs) (string, error) { return "ami-12345678", nil }
func (f *fakeCloud) SpotRole(_ context.Context, in Inputs, create bool) (bool, error) {
	f.in = in
	f.calls = append(f.calls, "spot")
	return true, nil
}
func (f *fakeCloud) Bucket(_ context.Context, in Inputs, key string) (string, error) {
	f.in = in
	f.calls = append(f.calls, "bucket:"+key)
	if key == "" {
		if f.bucket {
			return "exists", nil
		}
		return "absent", nil
	}
	if key == "bootstrap/terraform.tfstate" && f.migrated {
		return "present", nil
	}
	if strings.HasPrefix(key, "foundation/") && f.foundation {
		return "present", nil
	}
	return "empty", nil
}
func (f *fakeCloud) Verify(_ context.Context, _ Inputs, _ config.Manifest, configurationOnly bool, after time.Time) []foundation.Check {
	f.calls = append(f.calls, "verify")
	if !configurationOnly {
		if after.IsZero() {
			f.t.Error("missing enablement evidence boundary")
		}
		if f.healthErr != nil {
			return []foundation.Check{{Name: "cleanup_recent_completion", Err: f.healthErr}}
		}
	}
	return []foundation.Check{{Name: "foundation_network"}}
}
func (f *fakeCloud) run(ctx context.Context, r Request) (Response, error) {
	if err := ctx.Err(); err != nil {
		return Response{}, err
	}
	a := r.Args
	if r.Program == "ssh" {
		return Response{}, nil
	}
	if r.Program == "session-manager-plugin" {
		return Response{Output: []byte("1.2.835.0")}, nil
	}
	if r.Program == "ssh-keygen" {
		m := fixtureManifest(f.t)
		return Response{Output: []byte(m.SSHPublicKey + "\n")}, nil
	}
	if filepath.Base(r.Program) == "aws" {
		if len(a) == 1 && a[0] == "--version" {
			return Response{Output: []byte("aws-cli/2.31.0")}, nil
		}
		return Response{Output: []byte(`{"Account":"123456789012","Arn":"arn:aws:iam::123456789012:user/setup"}`)}, nil
	}
	if r.Program == "cp" {
		b, err := os.ReadFile(a[len(a)-2])
		if err != nil {
			return Response{}, err
		}
		return Response{}, os.WriteFile(a[len(a)-1], b, 0600)
	}
	if r.Program == "sync" {
		return Response{}, nil
	}
	if a[0] == "version" {
		return Response{Output: []byte(`{"terraform_version":"1.12.6"}`)}, nil
	}
	if a[0] == "init" {
		if r.Migration {
			f.calls = append(f.calls, "migrate")
			if f.mutationHook != nil {
				if err := f.mutationHook("migrating"); err != nil {
					return Response{}, err
				}
			}
			f.migrated = true
		}
		return Response{}, nil
	}
	if a[0] == "plan" {
		name := ""
		for _, arg := range a {
			if strings.HasPrefix(arg, "-out=") {
				name = strings.TrimPrefix(arg, "-out=")
			}
		}
		if name == "" {
			if !f.migrated {
				return Response{ExitCode: 2}, nil
			}
			return Response{}, nil
		}
		var changes []any
		change := func(address, typ, action string, before, after any) any {
			return map[string]any{"address": address, "mode": "managed", "type": typ, "change": map[string]any{"actions": []string{action}, "before": before, "after": after}}
		}
		if filepath.Base(r.Dir) == "state-bootstrap" {
			if !f.bucket {
				changes = append(changes, change("aws_s3_bucket.state", "aws_s3_bucket", "create", nil, map[string]any{"bucket": f.in.Bucket}))
			}
		} else if !f.foundation {
			changes = append(changes, change("aws_vpc.devbox", "aws_vpc", "create", nil, map[string]any{}))
		} else if !f.enabled {
			changes = append(changes, change("aws_scheduler_schedule.cleanup", "aws_scheduler_schedule", "update", map[string]any{"state": "DISABLED"}, map[string]any{"state": "ENABLED"}))
		}
		body := encodeFile(map[string]any{"format_version": "1.2", "resource_changes": changes})
		path := filepath.Join(r.Dir, name)
		mustWrite(f.t, path, body)
		f.plans[path] = body
		return Response{ExitCode: 2}, nil
	}
	if a[0] == "show" {
		return Response{Output: f.plans[filepath.Join(r.Dir, a[2])]}, nil
	}
	if a[0] == "apply" {
		phase := "foundation"
		if filepath.Base(r.Dir) == "state-bootstrap" {
			phase = "bootstrap"
		} else if f.foundation {
			phase = "schedule"
		}
		f.calls = append(f.calls, "apply:"+phase)
		if f.mutationHook != nil {
			if err := f.mutationHook(phase); err != nil {
				return Response{}, err
			}
		}
		switch phase {
		case "bootstrap":
			f.bucket = true
			mustWrite(f.t, filepath.Join(r.Dir, "terraform.tfstate"), []byte("opaque offline state"))
		case "foundation":
			f.foundation = true
		case "schedule":
			f.enabled = true
		}
		return Response{}, nil
	}
	if a[0] == "output" {
		if a[2] == "bucket_name" {
			return Response{Output: encodeFile(f.in.Bucket)}, nil
		}
		return Response{Output: encodeFile(fixtureManifest(f.t))}, nil
	}
	f.t.Fatalf("unexpected tool: %+v", r)
	return Response{}, nil
}

func fixtureEngine(t *testing.T) (*engine, *fakeCloud) {
	t.Helper()
	testutil.IsolateAWS(t)
	root := t.TempDir()
	bundle := filepath.Join(root, "bundle")
	fixtureBundle(t, bundle)
	aws := filepath.Join(root, "aws")
	mustWrite(t, aws, []byte("fixture"))
	if err := os.Chmod(aws, 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", root+":"+os.Getenv("PATH"))
	mustWrite(t, os.Getenv("AWS_CONFIG_FILE"), []byte("[profile source]\nregion=us-east-2\ncredential_process=fixture-credentials\n"))
	in := Inputs{Account: "123456789012", Region: "us-east-2", Deployment: "test", Owner: "test-owner", SourceProfile: "source", SetupProfile: "setup", OperatorProfile: "operator", Principal: "arn:aws:iam::123456789012:user/setup", SSHKey: filepath.Join(root, "key"), PublicKey: fixtureManifest(t).SSHPublicKey, Bucket: "devbox-state-test", AMI: "ami-12345678", ConfigPath: filepath.Join(root, "config.toml"), ManifestPath: filepath.Join(root, "deployment.json"), AWSConfigPath: os.Getenv("AWS_CONFIG_FILE"), AWSCredentialsPath: os.Getenv("AWS_SHARED_CREDENTIALS_FILE")}
	mustWrite(t, in.SSHKey, []byte("private fixture"))
	mustWrite(t, in.SSHKey+".pub", []byte(in.PublicKey))
	data := filepath.Join(root, "data")
	tofu := filepath.Join(data, "devbox/tools/tofu-1.12.6/tofu")
	mustWrite(t, tofu, []byte("fixture tofu"))
	f := &fakeCloud{t: t, plans: map[string][]byte{}}
	e := &engine{options: Options{Version: "0.0.0", BundlePath: bundle, StageTimeout: time.Second, HealthTimeout: time.Millisecond}, deps: Dependencies{Cloud: f, Run: f.run, Now: time.Now, Output: io.Discard, Input: strings.NewReader(strings.Repeat("yes\n", 50)), DataHome: data}, root: filepath.Join(root, "state"), result: Result{Checks: []Check{}, Completed: []string{}}}
	e.input = bufio.NewReader(e.deps.Input)
	e.j = Journal{SchemaVersion: 1, ID: newID(), Version: "0.0.0", Inputs: in, Mode: "new", Phase: "prepared", State: "complete", Completed: []string{"prepared"}}
	b, _ := json.Marshal(in)
	e.j.InputDigest = hash(b)
	e.workspace = filepath.Join(e.root, e.j.ID)
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	return e, f
}

func TestNewFoundationAllStagesAndDurableIntent(t *testing.T) {
	e, f := fixtureEngine(t)
	f.mutationHook = func(phase string) error {
		body, err := readFile(filepath.Join(e.workspace, "setup.json"))
		if err != nil {
			return err
		}
		var j Journal
		if err = strictJSON(body, &j); err != nil {
			return err
		}
		if j.PendingMutation != phase {
			return errors.New("mutation without durable intent")
		}
		return nil
	}
	if err := e.execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, step := range []string{"apply:bootstrap", "migrate", "apply:foundation", "apply:schedule"} {
		if !slices.Contains(f.calls, step) {
			t.Fatalf("missing %s: %v", step, f.calls)
		}
	}
	if e.result.Foundation != "verified" || e.result.Scheduling != "healthy" || !e.done("finished") {
		t.Fatal(e.finish())
	}
	if _, err := os.Stat(filepath.Join(e.workspace, "infra/state-bootstrap/pre-migration.recovery")); err != nil {
		t.Fatal("missing recovery copy", err)
	}
	before := len(f.calls)
	if err := e.execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.calls[before:] {
		if strings.HasPrefix(call, "apply:") || call == "migrate" {
			t.Fatal("completed setup repeated mutation", call)
		}
	}
}

func TestUncertainMigrationObservesRemoteBeforeCopy(t *testing.T) {
	e, f := fixtureEngine(t)
	once := true
	f.mutationHook = func(phase string) error {
		if phase == "migrating" && once {
			once = false
			f.migrated = true
			return context.DeadlineExceeded
		}
		return nil
	}
	if err := e.execute(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if e.j.PendingMutation != "migrating" {
		t.Fatal(e.j)
	}
	f.mutationHook = nil
	before := len(f.calls)
	if err := e.execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if slices.Contains(f.calls[before:], "migrate") {
		t.Fatal("stale local state was copied again")
	}
}

func TestUncertainFoundationMissingStateStops(t *testing.T) {
	e, f := fixtureEngine(t)
	f.mutationHook = func(phase string) error {
		if phase == "foundation" {
			return context.DeadlineExceeded
		}
		return nil
	}
	if err := e.execute(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	f.mutationHook = nil
	before := len(f.calls)
	err := e.execute(context.Background())
	var failure *Failure
	if !errors.As(err, &failure) || failure.Code != "manual_recovery_required" {
		t.Fatal(err)
	}
	if slices.Contains(f.calls[before:], "apply:foundation") {
		t.Fatal("uncertain apply blindly repeated")
	}
}

func TestHealthTimeoutLeavesEnabledAndResumeObserves(t *testing.T) {
	e, f := fixtureEngine(t)
	f.healthErr = errors.New("not yet observed")
	if err := e.execute(context.Background()); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if !f.enabled || !e.done("schedule") || e.result.Scheduling != "pending" {
		t.Fatal("lost enabled/pending outcome")
	}
	before := len(f.calls)
	f.healthErr = nil
	if err := e.execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	for _, call := range f.calls[before:] {
		if strings.HasPrefix(call, "apply:") {
			t.Fatal("health resume mutated", call)
		}
	}
}

func TestConnectExistingOperatorNoProvisioningOrProfileHop(t *testing.T) {
	e, f := fixtureEngine(t)
	m := fixtureManifest(t)
	m.SchemaVersion = 4
	m.Subnets = nil
	m.CompatiblePools = nil
	m.LaunchLedger = nil
	img := m.Images["agent"]
	img.RootDisk = nil
	img.MinimumRootDiskGB = 0
	m.Images["agent"] = img
	mustWrite(t, e.j.Inputs.ManifestPath, encodeFile(m))
	e.j.Mode = "connect"
	e.j.Completed = nil
	e.j.Inputs.Principal = ""
	e.j.InputDigest = ""
	f.caller = "arn:aws:sts::123456789012:assumed-role/devbox-operator/devbox-test-test-owner"
	if err := e.execute(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.j.Inputs.OperatorProfile != "source" || !e.j.Inputs.ExistingOperator {
		t.Fatal(e.j.Inputs)
	}
	for _, call := range f.calls {
		if call == "spot" || strings.HasPrefix(call, "apply:") || strings.HasPrefix(call, "bucket:") {
			t.Fatal("connection required provisioning", call)
		}
	}
	if e.result.Foundation != "legacy_recovery_only" {
		t.Fatal(e.result)
	}
}

func TestConfigurationTransactionRejectsConcurrentEditAndResumes(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "a")
	b := filepath.Join(root, "b")
	mustWrite(t, a, []byte("original"))
	mustWrite(t, b, []byte("old"))
	changes, err := stageChanges(filepath.Join(root, "transaction"), map[string][]byte{a: []byte("new-a"), b: []byte("new-b")})
	if err != nil {
		t.Fatal(err)
	}
	mustWrite(t, b, []byte("concurrent edit"))
	if publishChanges(changes) == nil {
		t.Fatal("overwrote concurrent edit")
	}
	got, _ := os.ReadFile(a)
	if string(got) != "original" {
		t.Fatal("published before checking all targets")
	}
	mustWrite(t, b, []byte("old"))
	mustWrite(t, a, []byte("new-a"))
	if err = publishChanges(changes); err != nil {
		t.Fatal(err)
	}
	for _, c := range changes {
		got, _ := fileDigest(c.Path)
		if got != c.After {
			t.Fatal("transaction not resumed")
		}
	}
}

func TestCredentialTopologyAndShadowing(t *testing.T) {
	for _, tc := range []struct {
		name, config, credentials string
		bad                       bool
	}{
		{"login", "[profile source]\nlogin_session=source\n", "", false},
		{"environment source", "[profile source]\nrole_arn=arn:aws:iam::123456789012:role/Source\ncredential_source=Environment\n", "", false},
		{"cycle", "[profile source]\nsource_profile=other\n[profile other]\nsource_profile=source\n", "", true},
		{"bridge recursion", "[profile source]\ncredential_process=aws configure export-credentials --profile setup\n", "", true},
		{"operator recursion", "[profile source]\nsource_profile=operator\n", "", true},
		{"shadow", "[profile source]\nlogin_session=source\n", "[setup]\naws_access_key_id=SECRET\n", true},
		{"shadow operator", "[profile source]\nlogin_session=source\n", "[operator]\naws_secret_access_key=SECRET\n", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := validateSourceProfiles([]byte(tc.config), []byte(tc.credentials), "source", "setup", "operator")
			if (err != nil) != tc.bad {
				t.Fatal(err)
			}
			if err != nil && strings.Contains(err.Error(), "SECRET") {
				t.Fatal("leaked credentials")
			}
		})
	}
}

func TestExactPrincipalAndEnvironmentPolicy(t *testing.T) {
	role := "arn:aws:iam::123456789012:role/path/operator"
	caller := "arn:aws:sts::123456789012:assumed-role/operator/devbox-test-owner"
	if !samePrincipal(caller, role, "devbox-test-owner") || samePrincipal(caller, role, "other") || samePrincipal(strings.Replace(caller, "operator/", "admin/", 1), role, "") {
		t.Fatal("principal check is too broad")
	}
	env := processEnv([]string{"AWS_PROFILE=admin", "AWS_ACCESS_KEY_ID=source", "AWS_SECRET_ACCESS_KEY=source-secret", "TF_CLI_ARGS=-lock=false", "AWS_ENDPOINT_URL=https://invalid", "AWS_CONFIG_FILE=/private/config"})
	joined := strings.Join(env, "\n")
	if strings.Contains(joined, "admin") || strings.Contains(joined, "-lock=false") || strings.Contains(joined, "https://invalid") || !strings.Contains(joined, "AWS_ACCESS_KEY_ID=source") || !strings.Contains(joined, "AWS_CONFIG_FILE=/private/config") {
		t.Fatal("incorrect environment policy")
	}
}

func TestPromptEOFAndOutputFailureNeverAuthorize(t *testing.T) {
	e, _ := fixtureEngine(t)
	e.input = bufio.NewReader(strings.NewReader("yes"))
	if err := e.approve(context.Background(), "Apply?"); err == nil {
		t.Fatal("EOF granted approval")
	}
	e.input = bufio.NewReader(strings.NewReader("yes\n"))
	e.deps.Output = brokenWriter{}
	if err := e.approve(context.Background(), "Apply?"); err == nil {
		t.Fatal("unseen preview granted approval")
	}
}

type brokenWriter struct{}

func (brokenWriter) Write([]byte) (int, error) { return 0, io.ErrClosedPipe }

func TestBundleAndPlanChangesRejected(t *testing.T) {
	e, _ := fixtureEngine(t)
	if err := e.setupBundle(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := e.writeInputs(false); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(e.workspace, "infra/foundation/evil_override.tf"), []byte("# extra configuration"))
	if e.verifyWorkspace(false) == nil {
		t.Fatal("accepted unlisted Terraform input")
	}
	cases := []string{
		`{"format_version":"1.2","resource_changes":[{"address":"aws_vpc.devbox","type":"aws_vpc","mode":"managed","change":{"actions":["delete","create"]}}]}`,
		`{"format_version":"1.2","resource_changes":[{"address":"aws_instance.worker","type":"aws_instance","mode":"managed","change":{"actions":["create"]}}]}`,
		`{"format_version":"1.2","resource_changes":[{"address":"aws_scheduler_schedule.cleanup","type":"aws_scheduler_schedule","mode":"managed","change":{"actions":["update"],"before":{"state":"DISABLED","name":"old"},"after":{"state":"ENABLED","name":"new"}}}]}`,
	}
	for _, body := range cases {
		if _, err := e.plannedActions([]byte(body), "foundation", true); err == nil {
			t.Fatal("accepted unsafe enablement plan")
		}
	}
}

func TestRealKeyCorrespondence(t *testing.T) {
	if _, err := exec.LookPath("ssh-keygen"); err != nil {
		t.Skip("OpenSSH unavailable")
	}
	e, _ := fixtureEngine(t)
	if err := os.Remove(e.j.Inputs.SSHKey); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(e.j.Inputs.SSHKey + ".pub"); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("ssh-keygen", "-t", "ed25519", "-N", "", "-f", e.j.Inputs.SSHKey)
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	e.deps.Run = DefaultProcess(strings.NewReader(""), io.Discard)
	if err := e.prepareKey(context.Background()); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, e.j.Inputs.SSHKey+".pub", []byte(fixtureManifest(t).SSHPublicKey))
	if err := e.prepareKey(context.Background()); err == nil {
		t.Fatal("mismatched public key accepted")
	}
}

func TestStatusAndNonTTYDoNotRunDependencies(t *testing.T) {
	called := false
	deps := Dependencies{StateHome: t.TempDir(), Run: func(context.Context, Request) (Response, error) { called = true; return Response{}, nil }, Terminal: func() bool { return false }}
	r := Run(context.Background(), Options{Version: "0.0.0"}, deps)
	if r.Code != "confirmation_required" || called {
		t.Fatal(r)
	}
	r = Run(context.Background(), Options{Version: "0.0.0", Status: strings.Repeat("a", 32)}, deps)
	if r.Code != "setup_missing" || called {
		t.Fatal(r)
	}
}

func TestTOMLAndProfilePreservation(t *testing.T) {
	old := []byte("# keep comment\nschema_version = 1\ndefault_ttl = \"4h\"\nowner = \"old\"\n")
	updated, err := patchTOML(old, map[string]any{"owner": "new"})
	if err != nil || !bytes.Contains(updated, []byte("# keep comment")) || !bytes.Contains(updated, []byte("default_ttl = \"4h\"")) {
		t.Fatal(err, string(updated))
	}
	aws := []byte("[profile unrelated]\ncredential_process=PRIVATE-COMMAND\n")
	updated, err = profileSection(aws, "managed", map[string]string{"region": "us-east-2"})
	if err != nil || !bytes.HasPrefix(updated, aws) {
		t.Fatal("changed unrelated profile")
	}
	if _, err = profileSection(updated, "managed", map[string]string{"region": "us-west-2"}); err == nil {
		t.Fatal("overwrote conflicting profile")
	}
}
