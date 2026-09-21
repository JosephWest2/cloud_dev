package setup

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/sshkey"
)

type engine struct {
	options                          Options
	deps                             Dependencies
	input                            *bufio.Reader
	j                                Journal
	root, workspace, bundleDir, tofu string
	tofuDigest                       string
	bundle                           Bundle
	result                           Result
}

func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("system random source unavailable")
	}
	return hex.EncodeToString(b[:])
}
func absolute(path string) (string, error) {
	p, err := filepath.Abs(path)
	if err != nil {
		return "", invalid("cannot resolve setup path")
	}
	return p, safePath(p)
}

func Run(ctx context.Context, options Options, deps Dependencies) Result {
	r := Result{SchemaVersion: 1, Command: "setup", Completed: []string{}, Checks: []Check{}, Installation: "unverified", Foundation: "unverified", Scheduling: "unverified"}
	if deps.Now == nil {
		deps.Now = time.Now
	}
	if deps.Input == nil {
		deps.Input = os.Stdin
	}
	if deps.Output == nil {
		deps.Output = io.Discard
	}
	if deps.Run == nil {
		deps.Run = DefaultProcess(deps.Input, deps.Output)
	}
	if deps.Cloud == nil {
		deps.Cloud = AWSCloud{}
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return resultError(r, invalid("cannot locate user home"))
	}
	if deps.StateHome == "" {
		deps.StateHome = os.Getenv("XDG_STATE_HOME")
		if deps.StateHome == "" {
			deps.StateHome = filepath.Join(home, ".local", "state")
		}
	}
	if deps.DataHome == "" {
		deps.DataHome = os.Getenv("XDG_DATA_HOME")
		if deps.DataHome == "" {
			deps.DataHome = filepath.Join(home, ".local", "share")
		}
	}
	e := &engine{options: options, deps: deps, input: bufio.NewReader(deps.Input), root: filepath.Join(deps.StateHome, "devbox", "setup"), result: r}
	if err := safePath(e.root); err != nil {
		return resultError(r, err)
	}
	if options.StageTimeout == 0 {
		e.options.StageTimeout = 20 * time.Minute
	}
	if options.HealthTimeout == 0 {
		e.options.HealthTimeout = 15 * time.Minute
	}
	if options.Status != "" {
		if err = e.load(options.Status); err != nil {
			return resultError(r, err)
		}
		e.result.Recorded = true
		e.result.OK = true
		e.result.Code = "setup_recorded"
		e.result.Message = "recorded setup progress; no current AWS verification was performed"
		return e.finish()
	}
	if deps.Terminal == nil || !deps.Terminal() {
		return resultError(r, &Failure{"confirmation_required", "guided setup needs a terminal; run devbox setup interactively, or use setup status for recorded progress", 2})
	}
	unlock, err := lockDirectory(e.root)
	if err != nil {
		return resultError(r, err)
	}
	defer unlock()
	if options.Resume != "" {
		err = e.load(options.Resume)
	} else {
		err = e.choose(ctx)
	}
	if err == nil && deps.Authenticate != nil {
		err = deps.Authenticate(ctx, e.j.Inputs)
	}
	if err == nil {
		err = e.execute(ctx)
	}
	if err != nil {
		e.result = resultError(e.result, err)
	} else {
		e.result.OK = true
		e.result.Code = "setup_complete"
		e.result.Message = "requested setup completed; no worker was launched"
	}
	return e.finish()
}

func (e *engine) finish() Result {
	r := e.result
	r.SetupID = e.j.ID
	r.Version = e.j.Version
	in := e.j.Inputs
	r.Scope = Scope{in.Account, in.Region, in.Deployment, in.Owner}
	r.Phase = e.j.Phase
	if e.j.Completed != nil {
		r.Completed = append([]string{}, e.j.Completed...)
	}
	r.Partial = e.j.State == "in_progress" || (len(e.j.Completed) > 0 && !r.OK)
	if e.j.ID != "" {
		r.Recovery = "devbox setup --resume " + e.j.ID
	}
	if r.Recorded {
		r.Installation = "recorded"
		r.Foundation = "recorded"
		r.Scheduling = "recorded"
	}
	return r
}

func (e *engine) say(format string, args ...any) error {
	message := fmt.Sprintf(format, args...)
	n, err := io.WriteString(e.deps.Output, message)
	if err != nil || n != len(message) {
		return fail("output_unavailable", "cannot write the full setup preview; no action approved")
	}
	return nil
}

func (e *engine) ask(ctx context.Context, label, defaultValue string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if defaultValue != "" {
		if err := e.say("%s [%s]: ", label, defaultValue); err != nil {
			return "", err
		}
	} else {
		if err := e.say("%s: ", label); err != nil {
			return "", err
		}
	}
	// Terminal reads are unblocked by the process supervisor on cancellation.
	type answer struct {
		s   string
		err error
	}
	ch := make(chan answer, 1)
	go func() { s, err := e.input.ReadString('\n'); ch <- answer{s, err} }()
	select {
	case <-ctx.Done():
		return "", ctx.Err()
	case a := <-ch:
		if a.err != nil || len(a.s) > 4096 {
			return "", &Failure{"confirmation_required", "setup requires a complete terminal response; EOF does not grant approval", 2}
		}
		s := strings.TrimSpace(a.s)
		if s == "" {
			s = defaultValue
		}
		if strings.ContainsAny(s, "\x00\r\n") {
			return "", invalid("invalid terminal input")
		}
		return s, nil
	}
}

func (e *engine) confirm(ctx context.Context, message string) (bool, error) {
	s, err := e.ask(ctx, message+" [y/N]", "")
	if err != nil {
		return false, err
	}
	return strings.EqualFold(s, "y") || strings.EqualFold(s, "yes"), nil
}

func (e *engine) approve(ctx context.Context, message string) error {
	ok, err := e.confirm(ctx, message)
	if err != nil {
		return err
	}
	if !ok {
		return &Failure{"confirmation_required", "action declined; recorded setup can be resumed later", 2}
	}
	return nil
}

func (e *engine) save() error {
	e.j.UpdatedAt = e.deps.Now().UTC()
	b, err := json.MarshalIndent(e.j, "", "  ")
	if err != nil {
		return err
	}
	return atomicWrite(filepath.Join(e.workspace, "setup.json"), append(b, '\n'))
}
func (e *engine) begin(phase string) error {
	if phase != "health" {
		if e.j.PendingMutation != "" && e.j.PendingMutation != phase {
			return recoveryError()
		}
		e.j.PendingMutation = phase
	}
	e.j.Phase = phase
	e.j.State = "in_progress"
	return e.save()
}
func (e *engine) complete(phase string) error {
	if !slices.Contains(e.j.Completed, phase) {
		e.j.Completed = append(e.j.Completed, phase)
	}
	if e.j.PendingMutation == phase || (phase == "migrated" && e.j.PendingMutation == "migrating") {
		e.j.PendingMutation = ""
	}
	e.j.Phase = phase
	e.j.State = "complete"
	if e.j.PendingMutation != "" {
		e.j.State = "in_progress"
	}
	return e.save()
}
func (e *engine) done(phase string) bool { return slices.Contains(e.j.Completed, phase) }

func (e *engine) load(id string) error {
	if !idPattern.MatchString(id) {
		return invalid("setup ID must be 32 lowercase hexadecimal characters")
	}
	e.workspace = filepath.Join(e.root, id)
	body, err := readFile(filepath.Join(e.workspace, "setup.json"))
	if err != nil {
		return fail("setup_missing", "cannot read that setup journal; retain any existing workspace")
	}
	if err = strictJSON(body, &e.j); err != nil {
		return err
	}
	if e.j.ID != id || e.j.SchemaVersion != 1 || (e.j.Mode != "new" && e.j.Mode != "connect") {
		return invalid("unsupported setup journal")
	}
	phases := []string{"preparing", "prepared", "bootstrap", "migrating", "migrated", "spot_role", "foundation", "published", "verified", "schedule", "health", "healthy", "finished"}
	if !slices.Contains(phases, e.j.Phase) || !slices.Contains([]string{"in_progress", "complete"}, e.j.State) {
		return invalid("invalid recorded setup phase")
	}
	seen := map[string]bool{}
	for _, phase := range e.j.Completed {
		if !slices.Contains(phases, phase) || seen[phase] {
			return invalid("invalid recorded completed stages")
		}
		seen[phase] = true
	}
	if e.j.PendingMutation != "" && !slices.Contains([]string{"bootstrap", "migrating", "spot_role", "foundation", "schedule"}, e.j.PendingMutation) {
		return invalid("invalid pending setup mutation")
	}
	if e.options.Status == "" && e.j.Version != e.options.Version {
		return fail("setup_version_mismatch", "resume with the CLI release recorded in setup.json; automatic journal or infrastructure upgrades are not supported")
	}
	in := e.j.Inputs
	if !accountPattern.MatchString(in.Account) || in.Region != "us-east-2" || !labelPattern.MatchString(in.Deployment) || !labelPattern.MatchString(in.Owner) || !profilePattern.MatchString(in.SourceProfile) || !profilePattern.MatchString(in.SetupProfile) || !profilePattern.MatchString(in.OperatorProfile) {
		return invalid("invalid recorded setup scope")
	}
	if err := validateInputPaths(in); err != nil {
		return err
	}
	if e.j.InputDigest != "" {
		b, _ := json.Marshal(in)
		if e.j.InputDigest != hash(b) {
			return fail("inputs_changed", "recorded setup inputs changed; do not modify an in-progress workspace")
		}
	}
	for _, change := range e.j.Transaction {
		if change.Path != in.ConfigPath && change.Path != in.ManifestPath && change.Path != in.AWSConfigPath {
			return invalid("unexpected configuration transaction target")
		}
		for _, p := range []string{change.Staged, change.Backup} {
			if !strings.HasPrefix(p, e.workspace+string(os.PathSeparator)) {
				return invalid("transaction path outside setup workspace")
			}
		}
	}
	return nil
}

func (e *engine) choose(ctx context.Context) error {
	entries, _ := os.ReadDir(e.root)
	for _, entry := range entries {
		if !entry.IsDir() || !idPattern.MatchString(entry.Name()) {
			continue
		}
		body, err := readFile(filepath.Join(e.root, entry.Name(), "setup.json"))
		if err != nil {
			continue
		}
		var j Journal
		if strictJSON(body, &j) != nil || slices.Contains(j.Completed, "finished") {
			continue
		}
		ok, err := e.confirm(ctx, "Resume unfinished setup "+entry.Name()+"?")
		if err != nil {
			return err
		}
		if ok {
			return e.load(entry.Name())
		}
	}
	mode := "new"
	manifest := e.options.ManifestPath
	if manifest == "" {
		choice, err := e.ask(ctx, "Create a new foundation or connect an existing manifest (new/connect)", "new")
		if err != nil {
			return err
		}
		mode = choice
		if mode == "connect" {
			manifest, err = e.ask(ctx, "Trusted deployment manifest path", "")
			if err != nil {
				return err
			}
		}
	} else {
		mode = "connect"
	}
	if mode != "new" && mode != "connect" {
		return invalid("choose new or connect")
	}
	path := e.options.ConfigPath
	if path == "" {
		var err error
		path, err = config.DefaultPath()
		if err != nil {
			return err
		}
	}
	path, err := absolute(path)
	if err != nil {
		return err
	}
	var previous config.Config
	if _, err := readFile(path); err == nil {
		previous, err = config.Load(path, config.Overrides{AWSProfile: e.options.AWSProfile})
		if err != nil {
			return invalid("existing devbox configuration is invalid; repair it or choose another --config path")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	var m config.Manifest
	if mode == "connect" {
		manifest, err = absolute(manifest)
		if err != nil {
			return err
		}
		raw, err := readFile(manifest)
		if err != nil {
			return fail("manifest_unavailable", "cannot read the trusted manifest")
		}
		if json.Unmarshal(raw, &m) != nil {
			return invalid("invalid manifest")
		}
	}
	in := Inputs{Region: "us-east-2", ConfigPath: path, ManifestPath: filepath.Join(filepath.Dir(path), "deployment.json")}
	account := previous.ExpectedAccount
	deployment := previous.Deployment
	owner := previous.Owner
	if mode == "connect" {
		account = m.Account
		deployment = m.Deployment
		owner = m.Owner
		in.ManifestPath = manifest
	}
	if deployment == "" {
		deployment = "personal-dev"
	}
	if in.Account, err = e.ask(ctx, "Expected AWS account (12 digits)", account); err != nil {
		return err
	}
	if in.Deployment, err = e.ask(ctx, "Deployment (stable, at most 23 characters)", deployment); err != nil {
		return err
	}
	if in.Owner, err = e.ask(ctx, "Owner (stable, at most 23 characters)", owner); err != nil {
		return err
	}
	if !accountPattern.MatchString(in.Account) || !labelPattern.MatchString(in.Deployment) || !labelPattern.MatchString(in.Owner) {
		return invalid("invalid account, deployment or owner")
	}
	if e.options.Region != "" && e.options.Region != "us-east-2" {
		return invalid("setup currently supports us-east-2 only")
	}
	source := e.options.AWSProfile
	if source == "" {
		source = os.Getenv("AWS_PROFILE")
	}
	if source == "" {
		source = previous.AWSProfile
	}
	if source == "" {
		source = "default"
	}
	if in.SourceProfile, err = e.ask(ctx, "Authenticated AWS source profile", source); err != nil {
		return err
	}
	in.SetupProfile = "devbox-" + in.Deployment + "-" + in.Owner + "-setup"
	if in.OperatorProfile, err = e.ask(ctx, "Operator profile name", "devbox-"+in.Deployment+"-"+in.Owner+"-operator"); err != nil {
		return err
	}
	if !profilePattern.MatchString(in.SourceProfile) || !profilePattern.MatchString(in.OperatorProfile) || (mode == "new" && (in.SourceProfile == in.SetupProfile || in.SourceProfile == in.OperatorProfile || in.SetupProfile == in.OperatorProfile)) {
		return invalid("source, setup and operator profiles must be distinct valid names")
	}
	home, _ := os.UserHomeDir()
	in.AWSConfigPath = os.Getenv("AWS_CONFIG_FILE")
	if in.AWSConfigPath == "" {
		in.AWSConfigPath = filepath.Join(home, ".aws", "config")
	}
	in.AWSCredentialsPath = os.Getenv("AWS_SHARED_CREDENTIALS_FILE")
	if in.AWSCredentialsPath == "" {
		in.AWSCredentialsPath = filepath.Join(home, ".aws", "credentials")
	}
	for _, p := range []*string{&in.AWSConfigPath, &in.AWSCredentialsPath} {
		*p, err = absolute(*p)
		if err != nil {
			return err
		}
	}
	key := previous.SSHIdentityFile
	if key == "" {
		key = filepath.Join(home, ".ssh", "devbox-"+in.Deployment+"-"+in.Owner)
	}
	if in.SSHKey, err = e.ask(ctx, "Dedicated Ed25519 private key path", key); err != nil {
		return err
	}
	in.SSHKey, err = absolute(in.SSHKey)
	if err != nil {
		return err
	}
	if err = validateInputPaths(in); err != nil {
		return err
	}
	id := newID()
	if mode == "new" {
		in.Bucket = "devbox-state-" + in.Account + "-" + id[:12]
	}
	e.j = Journal{SchemaVersion: 1, ID: id, Version: e.options.Version, Inputs: in, Mode: mode, Phase: "preparing", State: "in_progress", Completed: []string{}}
	e.workspace = filepath.Join(e.root, id)
	if err = e.say("Setup %s: account=%s region=%s deployment=%s owner=%s\n", id, in.Account, in.Region, in.Deployment, in.Owner); err != nil {
		return err
	}
	if err = e.approve(ctx, "Use this scope and prepare local setup files?"); err != nil {
		return err
	}
	return e.save()
}

func (e *engine) publish(ctx context.Context, files map[string][]byte) error {
	if len(e.j.Transaction) > 0 {
		if err := publishChanges(e.j.Transaction); err != nil {
			return err
		}
		e.j.Transaction = nil
		if err := e.save(); err != nil {
			return err
		}
	}
	changes, err := stageChanges(filepath.Join(e.workspace, "transactions", newID()), files)
	if err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	for _, c := range changes {
		if err = e.say("Write %q (previous=%s, new=%s; private backup retained)\n", c.Path, c.Before, c.After); err != nil {
			return err
		}
	}
	if err = e.approve(ctx, "Publish these local configuration changes?"); err != nil {
		return err
	}
	e.j.Transaction = changes
	if err = e.save(); err != nil {
		return err
	}
	if err = publishChanges(changes); err != nil {
		return err
	}
	e.j.Transaction = nil
	return e.save()
}

func optionalFile(path string) ([]byte, error) {
	b, err := readFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	return b, err
}

func (e *engine) prepareProfiles(ctx context.Context) error {
	if e.j.Mode == "connect" {
		body, err := readFile(e.j.Inputs.ManifestPath)
		if err != nil {
			return err
		}
		var m config.Manifest
		if json.Unmarshal(body, &m) != nil {
			return invalid("invalid existing manifest")
		}
		caller, err := e.deps.Cloud.Caller(ctx, e.j.Inputs, e.j.Inputs.SourceProfile)
		if err == nil && samePrincipal(caller, m.Roles["operator"].ARN, "devbox-"+e.j.Inputs.Deployment+"-"+e.j.Inputs.Owner) {
			e.j.Inputs.ExistingOperator = true
			e.j.Inputs.SetupProfile = e.j.Inputs.SourceProfile
			e.j.Inputs.OperatorProfile = e.j.Inputs.SourceProfile
			return e.save()
		}
		if e.j.Inputs.ExistingOperator && e.j.Inputs.SourceProfile == e.j.Inputs.OperatorProfile {
			return fail("operator_identity", "existing operator profile no longer resolves to the expected role/session")
		}
	}
	in := e.j.Inputs
	if in.SourceProfile == in.SetupProfile {
		return invalid("source profile must differ from a newly generated setup bridge")
	}
	awsPath, err := exec.LookPath("aws")
	if err != nil {
		return fail("aws_cli_missing", "install AWS CLI v2 with the guided installer")
	}
	awsPath, err = filepath.Abs(awsPath)
	if err != nil {
		return err
	}
	probe, err := e.deps.Run(ctx, Request{Program: awsPath, Args: []string{"--version"}})
	if err != nil {
		return err
	}
	if probe.ExitCode != 0 || !strings.HasPrefix(string(probe.Output), "aws-cli/2.") {
		return fail("aws_cli_version", "guided setup requires AWS CLI v2")
	}
	old, err := optionalFile(in.AWSConfigPath)
	if err != nil {
		return err
	}
	credentials, err := optionalFile(in.AWSCredentialsPath)
	if err != nil {
		return err
	}
	if err = validateSourceProfiles(old, credentials, in.SourceProfile, in.SetupProfile, in.OperatorProfile); err != nil {
		return err
	}
	source, err := e.deps.Run(ctx, Request{Program: awsPath, Args: []string{"--profile", in.SourceProfile, "--region", in.Region, "sts", "get-caller-identity", "--output", "json"}, Timeout: 30 * time.Second})
	if err != nil {
		return err
	}
	var sourceIdentity struct {
		Account string
		Arn     string
	}
	if source.ExitCode != 0 || json.Unmarshal(source.Output, &sourceIdentity) != nil || sourceIdentity.Account != in.Account || sourceIdentity.Arn == "" {
		return fail("source_identity", "AWS CLI source profile did not resolve to the confirmed account; authenticate it and retry")
	}
	// A process bridge works for all normal AWS CLI profile sources, including
	// browser login. Its output flows only to SDK/provider credential consumers.
	command := strconv.Quote(awsPath) + " configure export-credentials --profile " + in.SourceProfile + " --format process"
	updated, err := profileSection(old, in.SetupProfile, map[string]string{"credential_process": command, "region": in.Region})
	if err != nil {
		return err
	}
	if err = e.say("AWS profile %s: credential_process invokes %q for source profile %s; region=%s. No credential values are written.\n", in.SetupProfile, awsPath, in.SourceProfile, in.Region); err != nil {
		return err
	}
	if err = e.publish(ctx, map[string][]byte{in.AWSConfigPath: updated}); err != nil {
		return err
	}
	bridgeCaller, err := e.deps.Cloud.Caller(ctx, in, in.SetupProfile)
	if err != nil {
		return err
	}
	if !sameSourceCaller(sourceIdentity.Arn, bridgeCaller) {
		return fail("bridge_identity", "credential bridge resolves to a different identity than the selected source profile")
	}
	return nil
}

func (e *engine) prepareKey(ctx context.Context) error {
	in := &e.j.Inputs
	if err := safePath(in.SSHKey); err != nil {
		return err
	}
	st, err := os.Lstat(in.SSHKey)
	if errors.Is(err, os.ErrNotExist) {
		if e.j.Mode == "connect" {
			return fail("ssh_key_missing", "connecting requires the existing private key matching the foundation; generating a new key cannot authorize access")
		}
		if err = e.approve(ctx, "Generate the dedicated SSH key at "+strconv.Quote(in.SSHKey)+"? Choose a passphrase in ssh-keygen."); err != nil {
			return err
		}
		if err = e.save(); err != nil {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(in.SSHKey), 0700); err != nil {
			return err
		}
		out, err := e.deps.Run(ctx, Request{Program: "ssh-keygen", Args: []string{"-t", "ed25519", "-f", in.SSHKey, "-C", "devbox"}, Interactive: true, Timeout: e.options.StageTimeout})
		if err != nil {
			return err
		}
		if out.ExitCode != 0 {
			return fail("ssh_key_unavailable", "key generation did not finish; existing private keys will be preserved on resume")
		}
		st, err = os.Lstat(in.SSHKey)
	}
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return fail("ssh_key_permissions", "private key must be a regular file accessible only to its owner")
	}
	if stat, ok := st.Sys().(*syscall.Stat_t); ok && int(stat.Uid) != os.Getuid() {
		return fail("ssh_key_permissions", "private key must belong to the current user")
	}
	derived, err := e.deps.Run(ctx, Request{Program: "ssh-keygen", Args: []string{"-y", "-f", in.SSHKey}, Interactive: true, Timeout: e.options.StageTimeout})
	if err != nil {
		return err
	}
	fields := strings.Fields(string(derived.Output))
	if derived.ExitCode != 0 || len(fields) < 2 {
		return fail("ssh_key_unavailable", "cannot derive the public key; unlock the selected key and retry")
	}
	pub := strings.Join(fields[:2], " ")
	if _, err = sshkey.Parse(pub); err != nil {
		return fail("ssh_key_invalid", "selected key must be Ed25519")
	}
	if e.j.InputDigest != "" && pub != in.PublicKey {
		return fail("ssh_key_mismatch", "selected key changed since setup preparation; restore the recorded key before resuming")
	}
	existing, err := optionalFile(in.SSHKey + ".pub")
	if err != nil {
		return err
	}
	if len(existing) > 0 {
		f := strings.Fields(string(existing))
		if len(f) < 2 || strings.Join(f[:2], " ") != pub {
			return fail("ssh_key_mismatch", "private key does not match its adjacent public key; preserve both files and select the correct key")
		}
	} else {
		if err = e.save(); err != nil {
			return err
		}
		if err = atomicWrite(in.SSHKey+".pub", []byte(pub+"\n")); err != nil {
			return err
		}
	}
	in.PublicKey = pub
	return e.save()
}

func (e *engine) checkLocal(ctx context.Context) error {
	if _, err := os.Lstat("/usr/local/sessionmanagerplugin/seelog.xml"); err == nil || !errors.Is(err, os.ErrNotExist) {
		return fail("plugin_logging", "Session Manager logging configuration is present; disable logging at /usr/local/sessionmanagerplugin/seelog.xml before setup")
	}
	for _, probe := range []struct {
		program string
		args    []string
	}{{"ssh", []string{"-V"}}, {"session-manager-plugin", []string{"--version"}}} {
		out, err := e.deps.Run(ctx, Request{Program: probe.program, Args: probe.args, Timeout: 5 * time.Second})
		if err != nil {
			return err
		}
		if out.ExitCode != 0 {
			return fail("local_tools_missing", "repair OpenSSH and Session Manager plugin with the guided installer")
		}
		if probe.program == "session-manager-plugin" && !pluginVersionOK(strings.TrimSpace(string(out.Output))) {
			return fail("plugin_version", "Session Manager plugin must be at least 1.2.764.0")
		}
	}
	// Preserve doctor behavior; setup additionally verifies the plugin version.
	e.result.Installation = "verified"
	return nil
}

func pluginVersionOK(s string) bool {
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	want := []int{1, 2, 764, 0}
	cmp := 0
	for i, p := range parts {
		n, err := strconv.Atoi(p)
		if err != nil || n < 0 {
			return false
		}
		if cmp == 0 {
			if n > want[i] {
				cmp = 1
			}
			if n < want[i] {
				cmp = -1
			}
		}
	}
	return cmp >= 0
}

func (e *engine) prepare(ctx context.Context) error {
	if err := e.prepareProfiles(ctx); err != nil {
		return err
	}
	caller, err := e.deps.Cloud.Caller(ctx, e.j.Inputs, e.j.Inputs.SetupProfile)
	if err != nil {
		return err
	}
	principal := ""
	if e.j.Mode == "new" {
		principal, err = e.deps.Cloud.ResolvePrincipal(ctx, e.j.Inputs, caller)
		if err != nil {
			return err
		}
	} else {
		body, err := readFile(e.j.Inputs.ManifestPath)
		if err != nil {
			return err
		}
		var m config.Manifest
		if json.Unmarshal(body, &m) != nil {
			return invalid("invalid existing manifest")
		}
		if samePrincipal(caller, m.Roles["operator"].ARN, "devbox-"+e.j.Inputs.Deployment+"-"+e.j.Inputs.Owner) {
			e.j.Inputs.ExistingOperator = true
			e.j.Inputs.OperatorProfile = e.j.Inputs.SetupProfile
			if err = e.save(); err != nil {
				return err
			}
		}
	}
	if e.j.Inputs.Principal != "" && e.j.Inputs.Principal != principal {
		return fail("principal_mismatch", "source principal changed during setup")
	}
	if e.j.Inputs.Principal == "" && principal != "" {
		if err = e.approve(ctx, "Use source IAM principal "+principal+"?"); err != nil {
			return err
		}
		e.j.Inputs.Principal = principal
		if err = e.save(); err != nil {
			return err
		}
	}
	if err = e.prepareKey(ctx); err != nil {
		return err
	}
	if e.j.Mode == "new" && e.j.Inputs.AMI == "" {
		e.j.Inputs.AMI, err = e.deps.Cloud.Image(ctx, e.j.Inputs)
		if err != nil {
			return err
		}
		if err = e.say("Pinned Canonical Ubuntu 24.04 x86-64 image: %s\n", e.j.Inputs.AMI); err != nil {
			return err
		}
	}
	b, _ := json.Marshal(e.j.Inputs)
	e.j.InputDigest = hash(b)
	return e.complete("prepared")
}
