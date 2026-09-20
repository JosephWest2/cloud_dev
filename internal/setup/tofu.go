package setup

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
)

type planDocument struct {
	FormatVersion   string `json:"format_version"`
	ResourceChanges []struct {
		Address string `json:"address"`
		Mode    string `json:"mode"`
		Type    string `json:"type"`
		Change  struct {
			Actions []string                   `json:"actions"`
			Before  map[string]json.RawMessage `json:"before"`
			After   map[string]json.RawMessage `json:"after"`
		} `json:"change"`
	} `json:"resource_changes"`
}

var resourceAddress = regexp.MustCompile(`^aws_[a-z0-9_]+\.[a-z0-9_]+(?:\["[A-Za-z0-9_.-]+"\])?$`)
var resourceDeclaration = regexp.MustCompile(`(?m)^resource\s+"([a-z0-9_]+)"\s+"([a-z0-9_]+)"`)

func (e *engine) setupBundle(ctx context.Context) error {
	e.bundleDir = e.options.BundlePath
	if e.bundleDir == "" {
		e.bundleDir = filepath.Join(e.deps.DataHome, "devbox", "bundles", e.j.Version)
	}
	var err error
	e.bundleDir, err = absolute(e.bundleDir)
	if err != nil {
		return err
	}
	b, digest, err := validateBundle(e.bundleDir, e.j.Version)
	if err != nil {
		return err
	}
	if e.j.BundleDigest != "" && e.j.BundleDigest != digest {
		return fail("bundle_changed", "resume requires the original bundle bytes")
	}
	e.bundle = b
	e.j.BundleDigest = digest
	if err = e.save(); err != nil {
		return err
	}
	if err = copyBundle(e.bundleDir, e.workspace, b); err != nil {
		return err
	}
	e.tofu = filepath.Join(e.deps.DataHome, "devbox", "tools", "tofu-"+TofuVersion, "tofu")
	if _, err = os.Stat(e.tofu); err != nil {
		e.tofu, err = exec.LookPath("tofu")
		if err != nil {
			return fail("tofu_missing", "install OpenTofu 1.12.6 using the guided installer")
		}
	}
	e.tofu, err = filepath.Abs(e.tofu)
	if err != nil {
		return err
	}
	e.tofuDigest, err = hashFile(e.tofu)
	if err != nil {
		return err
	}
	if e.j.TofuDigest != "" && e.j.TofuDigest != e.tofuDigest {
		return fail("tofu_changed", "resume requires the original OpenTofu executable bytes")
	}
	e.j.TofuDigest = e.tofuDigest
	if err = e.save(); err != nil {
		return err
	}
	out, err := e.deps.Run(ctx, Request{Program: e.tofu, Args: []string{"version", "-json"}})
	if err != nil {
		return err
	}
	var version struct {
		Version string `json:"terraform_version"`
	}
	if out.ExitCode != 0 || json.Unmarshal(out.Output, &version) != nil || version.Version != TofuVersion {
		return fail("tofu_version", "guided provisioning requires exact OpenTofu 1.12.6")
	}
	return nil
}

func encodeFile(value any) []byte {
	b, _ := json.MarshalIndent(value, "", "  ")
	return append(b, '\n')
}

func (e *engine) generatedFiles(enabled bool) map[string][]byte {
	in := e.j.Inputs
	principalPattern := "^" + regexp.QuoteMeta(in.Principal) + "$"
	if strings.Contains(in.Principal, ":role/") {
		name := in.Principal[strings.LastIndex(in.Principal, "/")+1:]
		principalPattern = "^arn:aws:sts::" + in.Account + ":assumed-role/" + regexp.QuoteMeta(name) + "/[^/]+$"
	}
	condition := fmt.Sprintf("${self.account_id == %q && can(regex(%q, self.arn))}", in.Account, principalPattern)
	override := encodeFile(map[string]any{"provider": map[string]any{"aws": map[string]any{"profile": in.SetupProfile}}, "data": map[string]any{"aws_caller_identity": map[string]any{"devbox_setup": map[string]any{"lifecycle": map[string]any{"postcondition": []any{map[string]any{"condition": condition, "error_message": "Setup provider identity differs from the confirmed principal."}}}}}}})
	backend := func(key string) []byte {
		return []byte(fmt.Sprintf("bucket = %q\nkey = %q\nregion = %q\nallowed_account_ids = [%q]\nprofile = %q\n", in.Bucket, key, in.Region, in.Account, in.SetupProfile))
	}
	return map[string][]byte{
		"setup.tfrc": []byte("disable_checkpoint = true\nprovider_installation {\n  direct {}\n}\n"),
		"infra/state-bootstrap/setup_override.tf.json": override,
		"infra/foundation/setup_override.tf.json":      override,
		"infra/state-bootstrap/setup.tfvars.json":      encodeFile(map[string]any{"account_id": in.Account, "bucket_name": in.Bucket}),
		"infra/foundation/setup.tfvars.json":           encodeFile(map[string]any{"account_id": in.Account, "region": in.Region, "deployment": in.Deployment, "owner": in.Owner, "operator_principal_arn": in.Principal, "ami_id": in.AMI, "ssh_public_key": in.PublicKey, "cleanup_schedule_enabled": enabled}),
		"infra/state-bootstrap/backend.hcl":            backend("bootstrap/terraform.tfstate"),
		"infra/foundation/backend.hcl":                 backend("foundation/" + in.Deployment + "/terraform.tfstate"),
	}
}

func (e *engine) writeInputs(enabled bool) error {
	for rel, body := range e.generatedFiles(enabled) {
		path := filepath.Join(e.workspace, rel)
		old, err := optionalFile(path)
		if err != nil {
			return err
		}
		if len(old) > 0 && string(old) != string(body) {
			// Only the intentionally reviewed scheduling phase changes inputs.
			prior := e.generatedFiles(!enabled)[rel]
			if rel != "infra/foundation/setup.tfvars.json" || string(old) != string(prior) {
				return fail("inputs_changed", "generated setup files changed outside the guided workflow")
			}
		}
		if err = atomicWrite(path, body); err != nil {
			return err
		}
	}
	return nil
}

func (e *engine) verifyWorkspace(enabled bool) error {
	_, digest, err := validateBundle(e.bundleDir, e.j.Version)
	if err != nil || digest != e.j.BundleDigest {
		return fail("bundle_changed", "bundle changed after preparation")
	}
	for rel, want := range e.bundle.Files {
		got, err := hashFile(filepath.Join(e.workspace, rel))
		if err != nil || got != want {
			return fail("workspace_changed", "infrastructure or runtime artifacts changed; refuse to apply")
		}
	}
	generated := e.generatedFiles(enabled)
	for rel, want := range generated {
		got, err := readFile(filepath.Join(e.workspace, rel))
		if err != nil || string(got) != string(want) {
			return fail("inputs_changed", "setup inputs or credential profile changed after review")
		}
	}
	// Reject extra configuration and automatic variable files, even if the
	// intended files retain their hashes. Provider scratch directories are ignored.
	for _, root := range []string{"infra/state-bootstrap", "infra/foundation"} {
		entries, err := os.ReadDir(filepath.Join(e.workspace, root))
		if err != nil {
			return err
		}
		for _, entry := range entries {
			name := entry.Name()
			rel := root + "/" + name
			if strings.HasSuffix(name, ".tf") || strings.HasSuffix(name, ".tf.json") || strings.HasSuffix(name, ".auto.tfvars") || strings.HasSuffix(name, ".auto.tfvars.json") {
				if e.bundle.Files[rel] != "" || generated[rel] != nil {
					continue
				}
				if rel == "infra/state-bootstrap/backend.tf" {
					want, err := readFile(filepath.Join(e.workspace, root, "backend.tf.example"))
					if err != nil {
						return err
					}
					got, err := readFile(filepath.Join(e.workspace, rel))
					if err == nil && string(want) == string(got) {
						continue
					}
				}
				return fail("workspace_changed", "unexpected infrastructure configuration in setup workspace")
			}
		}
	}
	return nil
}

func (e *engine) tofuRun(ctx context.Context, dir string, migration bool, args ...string) (Response, error) {
	got, err := hashFile(e.tofu)
	if err != nil || got != e.tofuDigest {
		return Response{}, fail("tofu_changed", "OpenTofu executable changed after preparation; no command dispatched")
	}
	env := processEnv(os.Environ())
	env = append(env, "AWS_CONFIG_FILE="+e.j.Inputs.AWSConfigPath, "AWS_SHARED_CREDENTIALS_FILE="+e.j.Inputs.AWSCredentialsPath, "TF_CLI_CONFIG_FILE="+filepath.Join(e.workspace, "setup.tfrc"))
	response, err := e.deps.Run(ctx, Request{Program: e.tofu, Args: args, Dir: dir, Env: env, Timeout: e.options.StageTimeout, Migration: migration})
	if err != nil {
		return response, err
	}
	if response.ExitCode != 0 && response.ExitCode != 2 {
		return response, fail("tofu_failed", "OpenTofu did not complete; retain its private workspace and resume with fresh review; check setup permissions, quotas and authentication")
	}
	return response, nil
}

func (e *engine) initialize(ctx context.Context, root string, remote bool) error {
	dir := filepath.Join(e.workspace, "infra", root)
	args := []string{"init", "-input=false", "-no-color", "-lockfile=readonly"}
	if remote {
		args = append(args, "-backend-config=backend.hcl")
	}
	response, err := e.tofuRun(ctx, dir, false, args...)
	if err != nil {
		return err
	}
	if response.ExitCode != 0 {
		return fail("tofu_failed", "OpenTofu initialization failed")
	}
	return nil
}

func (e *engine) plannedActions(body []byte, root string, enable bool) ([]string, error) {
	var p planDocument
	if json.Unmarshal(body, &p) != nil || !strings.HasPrefix(p.FormatVersion, "1.") {
		return nil, fail("plan_invalid", "cannot decode the saved OpenTofu plan action format")
	}
	allowed := map[string]bool{}
	for rel := range e.bundle.Files {
		if !strings.HasPrefix(rel, "infra/"+root+"/") || !strings.HasSuffix(rel, ".tf") {
			continue
		}
		b, err := readFile(filepath.Join(e.workspace, rel))
		if err != nil {
			return nil, err
		}
		for _, m := range resourceDeclaration.FindAllStringSubmatch(string(b), -1) {
			allowed[m[1]+"."+m[2]] = true
		}
	}
	actions := []string{}
	for _, r := range p.ResourceChanges {
		if r.Mode == "data" {
			continue
		}
		if r.Mode != "managed" || !resourceAddress.MatchString(r.Address) {
			return nil, fail("plan_invalid", "unexpected managed resource address")
		}
		base, _, _ := strings.Cut(r.Address, "[")
		if !allowed[base] || slices.Contains([]string{"aws_instance", "aws_eip", "aws_nat_gateway", "aws_vpc_endpoint"}, r.Type) {
			return nil, fail("plan_invalid", "plan includes a resource outside the foundation bundle")
		}
		a := r.Change.Actions
		if len(a) != 1 || !slices.Contains([]string{"no-op", "create", "update"}, a[0]) {
			return nil, fail("destructive_plan", "guided setup refuses deletion or replacement; use reviewed manual recovery")
		}
		if a[0] == "no-op" {
			continue
		}
		if enable {
			if r.Address != "aws_scheduler_schedule.cleanup" || a[0] != "update" || string(r.Change.Before["state"]) != "\"DISABLED\"" || string(r.Change.After["state"]) != "\"ENABLED\"" {
				return nil, fail("schedule_drift", "enablement plan changes more than the expected schedule; inspect a manual foundation plan")
			}
			delete(r.Change.Before, "state")
			delete(r.Change.After, "state")
			if string(encodeFile(r.Change.Before)) != string(encodeFile(r.Change.After)) {
				return nil, fail("schedule_drift", "schedule attributes changed beyond enablement")
			}
		}
		actions = append(actions, a[0]+" "+r.Address)
	}
	slices.Sort(actions)
	return actions, nil
}

func (e *engine) apply(ctx context.Context, root, phase string, enabled bool) error {
	if err := e.verifyWorkspace(enabled); err != nil {
		return err
	}
	if _, err := e.deps.Cloud.Caller(ctx, e.j.Inputs, e.j.Inputs.SetupProfile); err != nil {
		return err
	}
	dir := filepath.Join(e.workspace, "infra", root)
	planName := "setup-" + newID() + ".tfplan"
	response, err := e.tofuRun(ctx, dir, false, "plan", "-input=false", "-no-color", "-lock-timeout=30s", "-var-file=setup.tfvars.json", "-out="+planName, "-detailed-exitcode")
	if err != nil {
		return err
	}
	if response.ExitCode != 0 && response.ExitCode != 2 {
		return fail("tofu_failed", "foundation plan did not complete")
	}
	shown, err := e.tofuRun(ctx, dir, false, "show", "-json", planName)
	if err != nil {
		return err
	}
	actions, err := e.plannedActions(shown.Output, root, phase == "schedule")
	if err != nil {
		return err
	}
	if err = e.say("%s plan: account=%s region=%s deployment=%s owner=%s\n", phase, e.j.Inputs.Account, e.j.Inputs.Region, e.j.Inputs.Deployment, e.j.Inputs.Owner); err != nil {
		return err
	}
	for _, action := range actions {
		if err = e.say("  %s\n", action); err != nil {
			return err
		}
	}
	if err = e.say("No worker is launched. State, result storage and cleanup/evidence services may incur charges.\nPrivate saved plan: %q\n", filepath.Join(dir, planName)); err != nil {
		return err
	}
	planDigest, err := hashFile(filepath.Join(dir, planName))
	if err != nil {
		return err
	}
	if err = e.approve(ctx, "Apply this exact saved "+phase+" plan?"); err != nil {
		return err
	}
	if err = e.verifyWorkspace(enabled); err != nil {
		return err
	}
	got, err := hashFile(filepath.Join(dir, planName))
	if err != nil || got != planDigest {
		return fail("plan_changed", "saved plan changed after approval")
	}
	if _, err = e.deps.Cloud.Caller(ctx, e.j.Inputs, e.j.Inputs.SetupProfile); err != nil {
		return err
	}
	if err = e.begin(phase); err != nil {
		return err
	}
	response, err = e.tofuRun(ctx, dir, false, "apply", "-input=false", "-no-color", "-lock-timeout=30s", planName)
	if err != nil {
		return err
	}
	if response.ExitCode != 0 {
		return fail("apply_uncertain", "apply did not complete; resume to refresh and review the remaining changes")
	}
	return e.complete(phase)
}

func (e *engine) bootstrap(ctx context.Context) error {
	if e.done("bootstrap") {
		return nil
	}
	dir := filepath.Join(e.workspace, "infra", "state-bootstrap")
	// Never create an already existing bucket from a fresh local state. A saved
	// state file's existence is only a recovery prerequisite, not proof of ownership.
	state, err := e.deps.Cloud.Bucket(ctx, e.j.Inputs, "")
	if err != nil {
		return err
	}
	_, localErr := os.Lstat(filepath.Join(dir, "terraform.tfstate"))
	if state == "absent" && e.j.PendingMutation == "bootstrap" {
		return recoveryError()
	}
	if state != "absent" && localErr != nil {
		return recoveryError()
	}
	if state == "absent" && localErr == nil {
		return recoveryError()
	}
	if err = e.initialize(ctx, "state-bootstrap", false); err != nil {
		return err
	}
	return e.apply(ctx, "state-bootstrap", "bootstrap", false)
}

func (e *engine) migrate(ctx context.Context) error {
	if e.done("migrated") {
		return e.verifyRemoteBootstrap(ctx)
	}
	state, err := e.deps.Cloud.Bucket(ctx, e.j.Inputs, "bootstrap/terraform.tfstate")
	if err != nil {
		return err
	}
	if state == "empty" {
		dir := filepath.Join(e.workspace, "infra", "state-bootstrap")
		// OpenTofu owns state bytes, including this native backup operation.
		// Preserve the original file in place; init keeps its pre-migration backup.
		st, err := os.Lstat(filepath.Join(dir, "terraform.tfstate"))
		if err != nil || !st.Mode().IsRegular() || st.Size() == 0 {
			return recoveryError()
		}
		if err = e.approve(ctx, "Migrate bootstrap state to the verified empty S3 state key with native locking?"); err != nil {
			return err
		}
		if err = e.begin("migrating"); err != nil {
			return err
		}
		recovery := filepath.Join(dir, "pre-migration.recovery")
		if err = safePath(recovery); err != nil {
			return err
		}
		if _, err = os.Lstat(recovery); errors.Is(err, os.ErrNotExist) {
			out, err := e.deps.Run(ctx, Request{Program: "cp", Args: []string{"--preserve=mode", "--no-clobber", "--", filepath.Join(dir, "terraform.tfstate"), recovery}})
			if err != nil {
				return err
			}
			if out.ExitCode != 0 {
				return recoveryError()
			}
		} else if err != nil {
			return err
		}
		out, err := e.deps.Run(ctx, Request{Program: "sync", Args: []string{"-f", recovery}})
		if err != nil {
			return err
		}
		if out.ExitCode != 0 {
			return recoveryError()
		}
		backend, err := readFile(filepath.Join(dir, "backend.tf.example"))
		if err != nil {
			return err
		}
		if err = atomicWrite(filepath.Join(dir, "backend.tf"), backend); err != nil {
			return err
		}
		if _, err = e.deps.Cloud.Caller(ctx, e.j.Inputs, e.j.Inputs.SetupProfile); err != nil {
			return err
		}
		response, err := e.tofuRun(ctx, dir, true, "init", "-migrate-state", "-no-color", "-lockfile=readonly", "-lock-timeout=30s", "-backend-config=backend.hcl")
		if err != nil {
			return err
		}
		if response.ExitCode != 0 {
			return recoveryError()
		}
	} else if state != "present" {
		return recoveryError()
	}
	if err = e.verifyRemoteBootstrap(ctx); err != nil {
		return err
	}
	return e.complete("migrated")
}

func (e *engine) verifyRemoteBootstrap(ctx context.Context) error {
	state, err := e.deps.Cloud.Bucket(ctx, e.j.Inputs, "bootstrap/terraform.tfstate")
	if err != nil {
		return err
	}
	if state != "present" {
		return recoveryError()
	}
	dir := filepath.Join(e.workspace, "verify-bootstrap", newID())
	if err = os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	for rel := range e.bundle.Files {
		if !strings.HasPrefix(rel, "infra/state-bootstrap/") {
			continue
		}
		name := strings.TrimPrefix(rel, "infra/state-bootstrap/")
		if strings.Contains(name, "/") {
			continue
		}
		body, err := readFile(filepath.Join(e.workspace, rel))
		if err != nil {
			return err
		}
		if err = atomicWrite(filepath.Join(dir, name), body); err != nil {
			return err
		}
	}
	for _, name := range []string{"backend.hcl", "setup.tfvars.json", "setup_override.tf.json"} {
		body, err := readFile(filepath.Join(e.workspace, "infra/state-bootstrap", name))
		if err != nil {
			return err
		}
		if err = atomicWrite(filepath.Join(dir, name), body); err != nil {
			return err
		}
	}
	backend, err := readFile(filepath.Join(dir, "backend.tf.example"))
	if err != nil {
		return err
	}
	if err = atomicWrite(filepath.Join(dir, "backend.tf"), backend); err != nil {
		return err
	}
	if _, err = e.tofuRun(ctx, dir, false, "init", "-input=false", "-no-color", "-lockfile=readonly", "-backend-config=backend.hcl"); err != nil {
		return err
	}
	out, err := e.tofuRun(ctx, dir, false, "output", "-json", "bucket_name")
	if err != nil {
		return err
	}
	var bucket string
	if json.Unmarshal(out.Output, &bucket) != nil || bucket != e.j.Inputs.Bucket {
		return recoveryError()
	}
	plan, err := e.tofuRun(ctx, dir, false, "plan", "-input=false", "-no-color", "-lock-timeout=30s", "-var-file=setup.tfvars.json", "-detailed-exitcode")
	if err != nil {
		return err
	}
	if plan.ExitCode != 0 {
		return recoveryError()
	}
	return nil
}

func (e *engine) spot(ctx context.Context) error {
	exists, err := e.deps.Cloud.SpotRole(ctx, e.j.Inputs, false)
	if err != nil {
		return err
	}
	if e.done("spot_role") {
		if !exists {
			return fail("spot_role_missing", "previously verified Spot role is missing; resolve it before resuming")
		}
		return nil
	}
	if !exists {
		if err = e.approve(ctx, "Create the shared account EC2 Spot service-linked role? It will not be deleted by setup."); err != nil {
			return err
		}
		if err = e.begin("spot_role"); err != nil {
			return err
		}
		exists, err = e.deps.Cloud.SpotRole(ctx, e.j.Inputs, true)
		if err != nil {
			return err
		}
		if !exists {
			return fail("spot_role_uncertain", "Spot role creation could not be verified")
		}
	}
	return e.complete("spot_role")
}

func (e *engine) exportManifest(ctx context.Context) ([]byte, error) {
	out, err := e.tofuRun(ctx, filepath.Join(e.workspace, "infra/foundation"), false, "output", "-json", "deployment_manifest")
	if err != nil {
		return nil, err
	}
	if out.ExitCode != 0 {
		return nil, fail("manifest_unavailable", "cannot export the named deployment manifest")
	}
	return out.Output, nil
}
