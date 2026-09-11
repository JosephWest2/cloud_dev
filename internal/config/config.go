// Package config defines the version 1 user, workload, and deployment contracts.
package config

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"

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
	Images             map[string]Image `json:"images"`
}

type Image struct {
	AMIID                 string `json:"ami_id"`
	Architecture          string `json:"architecture"`
	LaunchTemplateID      string `json:"launch_template_id"`
	LaunchTemplateVersion string `json:"launch_template_version"`
}

var (
	accountRE = regexp.MustCompile(`^[0-9]{12}$`)
	regionRE  = regexp.MustCompile(`^[a-z]{2}(-[a-z]+)+-[0-9]+$`)
	labelRE   = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9_-]{0,62}$`)
	typeRE    = regexp.MustCompile(`^[a-z0-9-]+\.[a-z0-9]+$`)
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

func LoadManifest(path string, c Config, p Profile) (Manifest, error) {
	var m Manifest
	data, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return m, errors.New("deployment manifest missing; provision the OpenTofu foundation (issue #7) and export deployment.json beside the config, or set manifest to its path")
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
	if m.SchemaVersion != 1 {
		return m, errors.New("unsupported manifest schema_version; use version 1")
	}
	if m.Account != c.ExpectedAccount || m.Region != c.Region || m.Deployment != c.Deployment || m.Owner != c.Owner {
		return m, errors.New("deployment manifest scope differs from expected account, region, deployment or owner; select the matching configuration and foundation export")
	}
	if !resourceID(m.VPCID, "vpc") || !resourceID(m.SecurityGroupID, "sg") || len(m.SubnetIDs) == 0 {
		return m, errors.New("manifest requires valid vpc_id, security_group_id and subnet_ids; re-export the foundation")
	}
	for _, id := range m.SubnetIDs {
		if !resourceID(id, "subnet") {
			return m, errors.New("manifest contains an invalid subnet ID")
		}
	}
	arnRE := regexp.MustCompile(`^arn:aws(-us-gov|-cn)?:iam::` + c.ExpectedAccount + `:instance-profile/[A-Za-z0-9+=,.@_/-]+$`)
	if !arnRE.MatchString(m.InstanceProfileARN) {
		return m, errors.New("manifest instance_profile_arn must identify an IAM instance profile in the expected account")
	}
	if _, ok := m.Images[p.Image]; !ok {
		return m, errors.New("manifest does not resolve the profile image; export an image with the matching key")
	}
	for key, img := range m.Images {
		if !labelRE.MatchString(key) || !resourceID(img.AMIID, "ami") || !resourceID(img.LaunchTemplateID, "lt") || !versionRE.MatchString(img.LaunchTemplateVersion) || (img.Architecture != "x86_64" && img.Architecture != "arm64") {
			return m, errors.New("manifest images require an exact AMI ID, architecture, launch template ID and positive numeric version (not $Latest or $Default)")
		}
	}
	return m, nil
}
