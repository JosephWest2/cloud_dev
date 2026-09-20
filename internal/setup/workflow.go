package setup

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
)

func (e *engine) execute(ctx context.Context) error {
	for _, entry := range os.Environ() {
		if strings.HasPrefix(entry, "AWS_ENDPOINT_URL") && !strings.HasSuffix(entry, "=") {
			return invalid("guided setup does not accept AWS endpoint overrides")
		}
	}
	if len(e.j.Transaction) > 0 {
		if err := publishChanges(e.j.Transaction); err != nil {
			return err
		}
		e.j.Transaction = nil
		if err := e.save(); err != nil {
			return err
		}
	}
	// Credentials remain in their original normal AWS files. Changing the selected
	// file locations on resume requires deliberate recovery, not silent fallback.
	home, _ := os.UserHomeDir()
	for key, want := range map[string]string{"AWS_CONFIG_FILE": e.j.Inputs.AWSConfigPath, "AWS_SHARED_CREDENTIALS_FILE": e.j.Inputs.AWSCredentialsPath} {
		got := os.Getenv(key)
		if got == "" {
			name := "config"
			if key == "AWS_SHARED_CREDENTIALS_FILE" {
				name = "credentials"
			}
			got = filepath.Join(home, ".aws", name)
		}
		got, err := absolute(got)
		if err != nil || got != want {
			return fail("credential_files_changed", "resume with the same selected AWS configuration and credentials file locations")
		}
	}
	if err := e.checkLocal(ctx); err != nil {
		return err
	}
	if e.j.Mode == "new" {
		if err := e.setupBundle(ctx); err != nil {
			return err
		}
	}
	if !e.done("prepared") {
		if err := e.prepare(ctx); err != nil {
			return err
		}
	} else {
		if err := e.prepareProfiles(ctx); err != nil {
			return err
		}
		if _, err := e.deps.Cloud.Caller(ctx, e.j.Inputs, e.j.Inputs.SetupProfile); err != nil {
			return err
		}
		if err := e.prepareKey(ctx); err != nil {
			return err
		}
	}
	var body []byte
	var err error
	if e.j.Mode == "new" {
		enabled := e.done("schedule") || e.j.PendingMutation == "schedule"
		if err = e.writeInputs(enabled); err != nil {
			return err
		}
		if err = e.bootstrap(ctx); err != nil {
			return err
		}
		if err = e.migrate(ctx); err != nil {
			return err
		}
		if err = e.spot(ctx); err != nil {
			return err
		}
		if !e.done("foundation") {
			remote, err := e.deps.Cloud.Bucket(ctx, e.j.Inputs, "foundation/"+e.j.Inputs.Deployment+"/terraform.tfstate")
			if err != nil {
				return err
			}
			if (remote == "present" && e.j.PendingMutation != "foundation") || (remote == "empty" && e.j.PendingMutation == "foundation") {
				return recoveryError()
			}
		}
		if err = e.initialize(ctx, "foundation", true); err != nil {
			return err
		}
		if !e.done("foundation") {
			if err = e.apply(ctx, "foundation", "foundation", false); err != nil {
				return err
			}
		}
		body, err = e.exportManifest(ctx)
	} else {
		body, err = readFile(e.j.Inputs.ManifestPath)
	}
	if err != nil {
		return err
	}
	m, err := e.configure(ctx, body)
	if err != nil {
		return err
	}
	if err = e.verify(ctx, m, true, time.Time{}); err != nil {
		return err
	}
	e.result.Foundation = "verified"
	if err = e.complete("verified"); err != nil {
		return err
	}
	if e.j.Mode == "connect" {
		if m.SchemaVersion < 6 {
			e.result.Foundation = "legacy_recovery_only"
			e.result.Scheduling = "unverified"
			if err = e.say("Legacy foundation connected for observation/recovery. New launches require a real v6 foundation upgrade.\n"); err != nil {
				return err
			}
		} else {
			c, err := config.DecodeCleanup(m.Cleanup, m)
			if err != nil {
				return err
			}
			if c.Schedule.State == "DISABLED" {
				e.result.Scheduling = "disabled"
			} else {
				if err = e.verify(ctx, m, false, time.Time{}); err != nil {
					e.result.Scheduling = "unhealthy"
					return err
				}
				e.result.Scheduling = "healthy"
			}
		}
		return e.handoff()
	}
	if e.j.EnableRequested == nil {
		yes, err := e.confirm(ctx, "Enable automatic expiry cleanup now? Recommended; it can terminate expired workers in this scope.")
		if err != nil {
			return err
		}
		e.j.EnableRequested = &yes
		if err = e.save(); err != nil {
			return err
		}
	}
	if !*e.j.EnableRequested {
		e.result.Scheduling = "disabled"
		if err = e.say("Foundation verified. Scheduled cleanup remains disabled. Resume this setup to offer enablement again.\n"); err != nil {
			return err
		}
		return e.handoff()
	}
	if !e.done("schedule") {
		if e.j.EnabledAfter.IsZero() {
			e.j.EnabledAfter = e.deps.Now().UTC()
			if err = e.save(); err != nil {
				return err
			}
		}
		if err = e.writeInputs(true); err != nil {
			return err
		}
		if err = e.apply(ctx, "foundation", "schedule", true); err != nil {
			return err
		}
		body, err = e.exportManifest(ctx)
		if err != nil {
			return err
		}
		m, err = e.configure(ctx, body)
		if err != nil {
			return err
		}
	}
	e.result.Scheduling = "pending"
	if err = e.begin("health"); err != nil {
		return err
	}
	health, cancel := context.WithTimeout(ctx, e.options.HealthTimeout)
	defer cancel()
	for {
		if err = e.verify(health, m, false, e.j.EnabledAfter); err == nil {
			break
		}
		if health.Err() != nil {
			return health.Err()
		}
		if err = e.say("Waiting for healthy alarms and a successful scheduled run after enablement; scheduling remains enabled.\n"); err != nil {
			return err
		}
		timer := time.NewTimer(30 * time.Second)
		select {
		case <-health.Done():
			timer.Stop()
			return health.Err()
		case <-timer.C:
		}
	}
	e.result.Scheduling = "healthy"
	if err = e.complete("healthy"); err != nil {
		return err
	}
	return e.handoff()
}

func (e *engine) configure(ctx context.Context, body []byte) (config.Manifest, error) {
	var m config.Manifest
	if json.Unmarshal(body, &m) != nil {
		return m, invalid("deployment output is not a manifest")
	}
	if e.j.Mode == "new" && m.SchemaVersion != 6 {
		return m, fail("manifest_invalid", "new setup requires the actual v6 foundation export")
	}
	in := e.j.Inputs
	if m.SSHPublicKey != in.PublicKey {
		return m, fail("ssh_key_mismatch", "selected private key does not match the foundation's public key")
	}
	old, err := optionalFile(in.ConfigPath)
	if err != nil {
		return m, err
	}
	updated, err := patchTOML(old, map[string]any{"schema_version": 1, "expected_account": in.Account, "region": in.Region, "deployment": in.Deployment, "owner": in.Owner, "aws_profile": in.OperatorProfile, "manifest": in.ManifestPath, "ssh_identity_file": in.SSHKey})
	if err != nil {
		return m, err
	}
	// Validate with the same parsers ordinary commands use, in the target config
	// directory so existing relative profile paths retain their meaning.
	if err = os.MkdirAll(filepath.Dir(in.ConfigPath), 0700); err != nil {
		return m, err
	}
	tmp, err := os.CreateTemp(filepath.Dir(in.ConfigPath), ".devbox-validate-*")
	if err != nil {
		return m, err
	}
	name := tmp.Name()
	tmp.Close()
	defer os.Remove(name)
	if err = atomicWrite(name, updated); err != nil {
		return m, err
	}
	c, err := config.Load(name, config.Overrides{AWSProfile: in.OperatorProfile})
	if err != nil {
		return m, invalid("generated configuration is incompatible with existing settings; review the preserved configuration")
	}
	p, err := config.LoadProfile(c.ProfileFile)
	if err != nil {
		return m, err
	}
	manifestStage := filepath.Join(e.workspace, "candidate-manifest.json")
	if err = atomicWrite(manifestStage, body); err != nil {
		return m, err
	}
	m, err = config.LoadManifest(manifestStage, c, p)
	if err != nil {
		return m, fail("manifest_invalid", "exported manifest failed normal schema, scope or profile validation")
	}
	awsConfig, err := optionalFile(in.AWSConfigPath)
	if err != nil {
		return m, err
	}
	role := m.Roles["operator"].ARN
	if !in.ExistingOperator {
		awsConfig, err = profileSection(awsConfig, in.OperatorProfile, map[string]string{"role_arn": role, "source_profile": in.SetupProfile, "role_session_name": "devbox-" + in.Deployment + "-" + in.Owner, "region": in.Region})
		if err != nil {
			return m, err
		}
	}
	if err = e.say("Configuration: account=%s region=%s deployment=%s owner=%s\n  operator profile=%s role=%s session=devbox-%s-%s\n  source profile=%s existing operator=%t\n  manifest=%q SSH private key=%q\n", in.Account, in.Region, in.Deployment, in.Owner, in.OperatorProfile, role, in.Deployment, in.Owner, in.SetupProfile, in.ExistingOperator, in.ManifestPath, in.SSHKey); err != nil {
		return m, err
	}
	files := map[string][]byte{in.ConfigPath: updated, in.AWSConfigPath: awsConfig}
	if e.j.Mode == "new" {
		files[in.ManifestPath] = body
	}
	if err = e.publish(ctx, files); err != nil {
		return m, err
	}
	return m, e.complete("published")
}

func (e *engine) verify(ctx context.Context, m config.Manifest, configurationOnly bool, after time.Time) error {
	bounded, cancel := context.WithTimeout(ctx, 4*time.Minute)
	defer cancel()
	checks := e.deps.Cloud.Verify(bounded, e.j.Inputs, m, configurationOnly, after)
	e.result.Checks = []Check{}
	ok := len(checks) > 0
	for _, check := range checks {
		name := check.Name
		if !regexpCheckName(name) {
			name = "verification"
		}
		e.result.Checks = append(e.result.Checks, Check{name, check.Err == nil})
		if check.Err != nil {
			ok = false
			if err := e.say("Check %s: not verified\n", name); err != nil {
				return err
			}
		}
	}
	if bounded.Err() != nil {
		return bounded.Err()
	}
	if !ok {
		return fail("verification_failed", "operator foundation or cleanup checks did not pass; fix the reported checks and resume; doctor retains full health requirements")
	}
	return nil
}

func regexpCheckName(name string) bool {
	if len(name) == 0 || len(name) > 64 {
		return false
	}
	for _, c := range name {
		if !(c >= 'a' && c <= 'z') && c != '_' {
			return false
		}
	}
	return true
}

func (e *engine) handoff() error {
	in := e.j.Inputs
	if err := e.say("Verify: devbox --config %q doctor --aws-profile %s --timeout 5m\n", in.ConfigPath, in.OperatorProfile); err != nil {
		return err
	}
	if e.result.Foundation != "legacy_recovery_only" {
		if err := e.say("First worker: devbox --config %q up agent --aws-profile %s\n", in.ConfigPath, in.OperatorProfile); err != nil {
			return err
		}
	}
	if e.result.Scheduling == "disabled" {
		e.j.EnableRequested = nil
	}
	return e.complete("finished")
}
