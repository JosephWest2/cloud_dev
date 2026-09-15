package config

import (
	"errors"
	"os"
	"reflect"
	"strings"

	"github.com/JosephWest2/cloud_dev/internal/expiry"
)

// LoadCleanup reads trusted scope and credential selection only. Known launch
// settings are intentionally not decoded or validated: cleanup remains usable
// when a launch profile, default TTL, manifest or access configuration is broken.
func LoadCleanup(path string, overrides Overrides) (Config, error) {
	var c Config
	data, err := os.ReadFile(path)
	if err != nil {
		return c, errors.New("cannot read configuration; select a readable --config with the intended AWS scope")
	}
	var fields map[string]any
	if err = decodeTOML(data, &fields); err != nil {
		return c, err
	}
	known := map[string]bool{}
	typ := reflect.TypeOf(c)
	for i := 0; i < typ.NumField(); i++ {
		known[typ.Field(i).Tag.Get("toml")] = true
	}
	for key := range fields {
		if !known[key] {
			return c, errors.New("unknown configuration field; compare with examples/config.toml")
		}
	}
	if version, ok := fields["schema_version"].(int64); !ok || version != 1 {
		return c, errors.New("unsupported configuration schema_version; use version 1")
	}
	for key, target := range map[string]*string{"expected_account": &c.ExpectedAccount, "region": &c.Region, "deployment": &c.Deployment, "owner": &c.Owner, "aws_profile": &c.AWSProfile} {
		value, exists := fields[key]
		if !exists {
			continue
		}
		s, ok := value.(string)
		if !ok {
			return c, errors.New("cleanup scope and aws_profile must be strings")
		}
		*target = s
	}
	c.SchemaVersion = 1
	if overrides.Region != "" {
		c.Region = overrides.Region
	}
	if overrides.AWSProfile != "" {
		c.AWSProfile = overrides.AWSProfile
	} else if p := os.Getenv("AWS_PROFILE"); p != "" {
		c.AWSProfile = p
	}
	if (expiry.Scope{Account: c.ExpectedAccount, Region: c.Region, Deployment: c.Deployment, Owner: c.Owner}).Validate() != nil {
		return c, errors.New("cleanup requires expected_account, explicit region, deployment and configured owner; compare with examples/config.toml")
	}
	if strings.ContainsAny(c.AWSProfile, "\r\n\x00") {
		return c, errors.New("aws_profile must be a valid shared AWS profile name")
	}
	return c, nil
}
