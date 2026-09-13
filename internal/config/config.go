// Package config defines the versioned user, workload, and deployment contracts.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/JosephWest2/cloud_dev/internal/sshkey"
	"github.com/JosephWest2/cloud_dev/profiles"
	"github.com/pelletier/go-toml/v2"
)

type Config struct {
	SchemaVersion   int    `toml:"schema_version"`
	ExpectedAccount string `toml:"expected_account"`
	Region          string `toml:"region"`
	Deployment      string `toml:"deployment"`
	Owner           string `toml:"owner"`
	AWSProfile      string `toml:"aws_profile"`
	Manifest        string `toml:"manifest"`
	ProfileFile     string `toml:"profile_file"`
	SSHIdentityFile string `toml:"ssh_identity_file"`
}

type Overrides struct{ AWSProfile, Region string }

type Profile struct {
	SchemaVersion int      `toml:"schema_version"`
	Name          string   `toml:"name"`
	Market        string   `toml:"market"`
	InstanceTypes []string `toml:"instance_types"`
	Image         string   `toml:"image"`
	DiskGB        int      `toml:"disk_gb"`
}

// Manifest is exported by the foundation, never derived from OpenTofu state by the CLI.
type Manifest struct {
	SchemaVersion      int              `json:"schema_version"`
	Account            string           `json:"account"`
	Region             string           `json:"region"`
	Deployment         string           `json:"deployment"`
	Owner              string           `json:"owner"`
	VPCID              string           `json:"vpc_id"`
	SubnetIDs          []string         `json:"subnet_ids"`
	SecurityGroupID    string           `json:"security_group_id"`
	InstanceProfileARN string           `json:"instance_profile_arn"`
	RouteTableID       string           `json:"route_table_id"`
	InternetGatewayID  string           `json:"internet_gateway_id"`
	DevelopmentUser    string           `json:"development_user"`
	BootstrapSHA256    string           `json:"bootstrap_sha256"`
	SSHPublicKey       string           `json:"ssh_public_key"`
	Readiness          Document         `json:"readiness"`
	Execution          Execution        `json:"execution"`
	Results            Results          `json:"results"`
	Roles              map[string]Role  `json:"roles"`
	Images             map[string]Image `json:"images"`
}

type Document struct {
	Name          string `json:"name"`
	Version       string `json:"version"`
	ContentSHA256 string `json:"content_sha256"`
}

// Execution pins the separate noninteractive runner document and installed binary.
type Execution struct {
	Name                string `json:"name"`
	Version             string `json:"version"`
	ContentSHA256       string `json:"content_sha256"`
	Step                string `json:"step"`
	RunnerSHA256        string `json:"runner_sha256"`
	MinimumAgentVersion string `json:"minimum_agent_version"`
}

// Results is sufficient trusted storage configuration for completed recovery.
// Retrieving results must not require the associated instance or launch profile.
type Results struct {
	SchemaVersion       int    `json:"schema_version"`
	Bucket              string `json:"bucket"`
	ExpectedBucketOwner string `json:"expected_bucket_owner"`
	Region              string `json:"region"`
	Prefix              string `json:"prefix"`
	RetentionDays       int    `json:"retention_days"`
	PolicySHA256        string `json:"policy_sha256"`
}

type Role struct {
	ARN          string `json:"arn"`
	TrustSHA256  string `json:"trust_sha256"`
	PolicyName   string `json:"policy_name"`
	PolicySHA256 string `json:"policy_sha256"`
}

type Image struct {
	UbuntuRelease         string `json:"ubuntu_release"`
	OwnerAccount          string `json:"owner_account"`
	Name                  string `json:"name"`
	RootDeviceName        string `json:"root_device_name"`
	AMIID                 string `json:"ami_id"`
	Architecture          string `json:"architecture"`
	LaunchTemplateID      string `json:"launch_template_id"`
	LaunchTemplateVersion string `json:"launch_template_version"`
}

var (
	accountRE = regexp.MustCompile(`^[0-9]{12}$`)
	regionRE  = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)
	labelRE   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)
	typeRE    = regexp.MustCompile(`^[a-z0-9-]+\.[a-z0-9-]+$`)
	versionRE = regexp.MustCompile(`^[1-9][0-9]*$`)
)

func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", errors.New("cannot locate user configuration directory; pass --config")
	}
	return filepath.Join(dir, "devbox", "config.toml"), nil
}

// Parser errors and user values are deliberately not included in diagnostics.
func decodeTOML(data []byte, target any) error {
	if err := toml.NewDecoder(bytes.NewReader(data)).DisallowUnknownFields().Decode(target); err != nil {
		return errors.New("invalid TOML or unknown fields; compare with the documented schema and remove credentials")
	}
	return nil
}

func Load(path string, overrides Overrides) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return c, errors.New("configuration missing; copy examples/config.toml to the user configuration directory or pass --config, then set your AWS scope")
	}
	if err != nil {
		return c, errors.New("cannot read configuration; check the --config path and file permissions")
	}
	if err = decodeTOML(data, &c); err != nil {
		return c, err
	}
	if c.SchemaVersion != 1 {
		return c, errors.New("unsupported configuration schema_version; use version 1")
	}
	if overrides.Region != "" {
		c.Region = overrides.Region
	}
	if overrides.AWSProfile != "" {
		c.AWSProfile = overrides.AWSProfile
	} else if p := os.Getenv("AWS_PROFILE"); p != "" {
		c.AWSProfile = p
	}
	if !accountRE.MatchString(c.ExpectedAccount) {
		return c, errors.New("expected_account must be a quoted 12-digit AWS account ID")
	}
	if !regionRE.MatchString(c.Region) {
		return c, errors.New("region must be an explicit AWS region such as us-east-2")
	}
	if !labelRE.MatchString(c.Deployment) || !labelRE.MatchString(c.Owner) {
		return c, errors.New("deployment and owner must each be 1–63 letters, digits, underscores or hyphens, starting with a letter or digit; choose a stable owner")
	}
	if strings.ContainsAny(c.AWSProfile, "\r\n\x00") {
		return c, errors.New("aws_profile must be a valid shared AWS profile name")
	}
	if c.Manifest == "" {
		c.Manifest = "deployment.json"
	}
	if !filepath.IsAbs(c.Manifest) {
		c.Manifest = filepath.Join(filepath.Dir(path), c.Manifest)
	}
	if c.SSHIdentityFile != "" && !filepath.IsAbs(c.SSHIdentityFile) {
		c.SSHIdentityFile = filepath.Join(filepath.Dir(path), c.SSHIdentityFile)
	}
	if c.SSHIdentityFile != "" {
		c.SSHIdentityFile, err = filepath.Abs(c.SSHIdentityFile)
		if err != nil {
			return c, errors.New("cannot resolve ssh_identity_file to an absolute path")
		}
	}
	if c.ProfileFile != "" && !filepath.IsAbs(c.ProfileFile) {
		c.ProfileFile = filepath.Join(filepath.Dir(path), c.ProfileFile)
	}
	return c, nil
}

func LoadProfile(path string) (Profile, error) {
	var p Profile
	data := profiles.Agent
	if path != "" {
		var err error
		data, err = os.ReadFile(path)
		if err != nil {
			return p, errors.New("cannot read profile_file; correct the path or omit it to use the embedded agent profile")
		}
	}
	if err := decodeTOML(data, &p); err != nil {
		return p, err
	}
	if p.SchemaVersion != 1 {
		return p, errors.New("unsupported profile schema_version; use version 1")
	}
	if p.Name != "agent" || !labelRE.MatchString(p.Image) {
		return p, errors.New("profile name must be agent and image must be a manifest image key")
	}
	if p.Market != "spot" && p.Market != "on-demand" {
		return p, errors.New("profile market must be spot or on-demand")
	}
	if len(p.InstanceTypes) == 0 {
		return p, errors.New("profile instance_types must contain at least one instance type")
	}
	seen := map[string]bool{}
	for _, t := range p.InstanceTypes {
		if !typeRE.MatchString(t) || seen[t] {
			return p, errors.New("profile instance_types must be valid, distinct EC2 type names")
		}
		seen[t] = true
	}
	if p.DiskGB < 8 || p.DiskGB > 16384 {
		return p, errors.New("profile disk_gb must be between 8 and 16384")
	}
	return p, nil
}

func resourceID(value, prefix string) bool {
	return regexp.MustCompile(`^` + prefix + `-([0-9a-f]{8}|[0-9a-f]{17})$`).MatchString(value)
}

func readManifest(path string, c Config) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, errors.New("deployment manifest missing; follow docs/setup.md to provision the OpenTofu foundation and export deployment.json beside the config, or set manifest to its path")
	}
	if err != nil {
		return m, errors.New("cannot read deployment manifest; check its path and permissions")
	}
	d := json.NewDecoder(bytes.NewReader(data))
	d.DisallowUnknownFields()
	if err = d.Decode(&m); err != nil {
		return m, errors.New("invalid deployment manifest JSON or unknown fields; re-export it from the foundation")
	}
	if d.Decode(new(any)) != io.EOF {
		return m, errors.New("deployment manifest must contain exactly one JSON object")
	}
	if m.SchemaVersion != 4 {
		return m, errors.New("unsupported manifest schema_version; apply and re-export version 4 from the foundation; replace old workers for execution support")
	}
	if m.Account != c.ExpectedAccount || m.Region != c.Region || m.Deployment != c.Deployment || m.Owner != c.Owner {
		return m, errors.New("deployment manifest scope differs from expected account, region, deployment or owner; select the matching configuration and foundation export")
	}
	if m.Region != "us-east-2" {
		return m, errors.New("foundation currently supports us-east-2 in commercial AWS only")
	}
	if len(m.Deployment) > 23 || len(m.Owner) > 23 {
		return m, errors.New("foundation deployment and owner must each fit 23 characters")
	}
	return m, nil
}

// LoadResultManifest validates only trusted storage scope. Completed recovery
// remains available after launch resources, profiles and SSH identities are gone.
func LoadResultManifest(path string, c Config) (Results, error) {
	m, err := readManifest(path, c)
	if err != nil {
		return Results{}, err
	}
	if err = ValidateResults(m.Results, c); err != nil {
		return Results{}, err
	}
	return m.Results, nil
}

func LoadManifest(path string, c Config, p Profile) (Manifest, error) {
	m, err := readManifest(path, c)
	if err != nil {
		return m, err
	}
	if _, err := sshkey.Parse(m.SSHPublicKey); err != nil {
		return m, errors.New("manifest requires a dedicated Ed25519 ssh_public_key; apply and re-export the foundation")
	}
	if len(m.SubnetIDs) != 1 || len(m.Images) != 1 || p.Image != "agent" {
		return m, errors.New("foundation requires one subnet and the agent image")
	}
	if !resourceID(m.RouteTableID, "rtb") || !resourceID(m.InternetGatewayID, "igw") || m.DevelopmentUser != "devbox" || !digestRE.MatchString(m.BootstrapSHA256) {
		return m, errors.New("manifest requires route table, internet gateway, devbox user and bootstrap digest; re-export the foundation")
	}
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`).MatchString(m.Readiness.Name) || !numericVersion(m.Readiness.Version) || !digestRE.MatchString(m.Readiness.ContentSHA256) {
		return m, errors.New("manifest readiness requires a name, positive numeric version and content digest")
	}
	if err := ValidateExecution(m.Execution); err != nil {
		return m, err
	}
	if m.Execution.Name == m.Readiness.Name {
		return m, errors.New("execution and readiness must use separate documents")
	}
	if err := ValidateResults(m.Results, c); err != nil {
		return m, err
	}
	roleRE := regexp.MustCompile(`^arn:aws:iam::` + c.ExpectedAccount + `:role/[A-Za-z0-9+=,.@_/-]+$`)
	if len(m.Roles) != 2 {
		return m, errors.New("manifest requires exactly the instance and operator roles")
	}
	for _, key := range []string{"instance", "operator"} {
		role := m.Roles[key]
		if !roleRE.MatchString(role.ARN) || !labelRE.MatchString(role.PolicyName) || !digestRE.MatchString(role.TrustSHA256) || !digestRE.MatchString(role.PolicySHA256) {
			return m, errors.New("manifest roles require scoped ARNs, policy names and trust/policy digests")
		}
	}
	if m.Roles["instance"].ARN == m.Roles["operator"].ARN {
		return m, errors.New("instance and operator roles must differ")
	}
	if !resourceID(m.VPCID, "vpc") || !resourceID(m.SecurityGroupID, "sg") || len(m.SubnetIDs) == 0 {
		return m, errors.New("manifest requires valid vpc_id, security_group_id and subnet_ids; re-export the foundation")
	}
	for _, id := range m.SubnetIDs {
		if !resourceID(id, "subnet") {
			return m, errors.New("manifest contains an invalid subnet ID")
		}
	}
	arnRE := regexp.MustCompile(`^arn:aws:iam::` + c.ExpectedAccount + `:instance-profile/[A-Za-z0-9+=,.@_/-]+$`)
	if !arnRE.MatchString(m.InstanceProfileARN) {
		return m, errors.New("manifest instance_profile_arn must identify an IAM instance profile in the expected account")
	}
	if _, ok := m.Images[p.Image]; !ok {
		return m, errors.New("manifest does not resolve the profile image; export an image with the matching key")
	}
	for key, img := range m.Images {
		if img.UbuntuRelease != "24.04" || img.OwnerAccount != "099720109477" || !ubuntuNameRE.MatchString(img.Name) || img.RootDeviceName != "/dev/sda1" {
			return m, errors.New("manifest must pin Canonical Ubuntu 24.04 amd64 server provenance and /dev/sda1 root device")
		}
		if !labelRE.MatchString(key) || !resourceID(img.AMIID, "ami") || !resourceID(img.LaunchTemplateID, "lt") || !numericVersion(img.LaunchTemplateVersion) || img.Architecture != "x86_64" {
			return m, errors.New("manifest images require an exact AMI ID, architecture, launch template ID and positive numeric version (not $Latest or $Default)")
		}
	}
	return m, nil
}

var digestRE = regexp.MustCompile(`^[0-9a-f]{64}$`)
var ubuntuNameRE = regexp.MustCompile(`^ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-[0-9.]+$`)

func ValidateExecution(e Execution) error {
	if !regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,127}$`).MatchString(e.Name) || !numericVersion(e.Version) || !digestRE.MatchString(e.ContentSHA256) || e.Step != "execute" || !digestRE.MatchString(e.RunnerSHA256) || e.MinimumAgentVersion != "3.3.2746.0" {
		return errors.New("execution requires pinned document version/content, execute step, runner digest and supported agent minimum; apply and re-export the foundation")
	}
	return nil
}

func ResultsPrefix(c Config) string {
	return "results/v1/" + c.ExpectedAccount + "/" + c.Region + "/" + c.Deployment + "/" + c.Owner + "/"
}

func ValidateResults(r Results, c Config) error {
	if r.SchemaVersion != 1 || r.ExpectedBucketOwner != c.ExpectedAccount || r.Region != c.Region || r.Region != "us-east-2" || r.Prefix != ResultsPrefix(c) {
		return errors.New("result storage schema or scope differs from the selected account, region, deployment or owner")
	}
	// Foundation bucket names contain only DNS-safe letters, digits and hyphens.
	if !regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,61}[a-z0-9]$`).MatchString(r.Bucket) || r.RetentionDays < 2 || r.RetentionDays > 365 || !digestRE.MatchString(r.PolicySHA256) {
		return errors.New("result storage requires a valid private bucket, 2–365 day retention and a policy digest; re-export the foundation")
	}
	return nil
}

func numericVersion(s string) bool {
	n, err := strconv.ParseInt(s, 10, 64)
	return err == nil && n > 0 && versionRE.MatchString(s)
}
