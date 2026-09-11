// Package foundation verifies deployed resources using read-only AWS APIs.
// The manifest is a trusted, non-secret export, not an authorization credential.
package foundation

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"net/url"
	"strings"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/JosephWest2/cloud_dev/internal/identity"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"
)

type Check struct {
	Name string
	Err  error
}
type Clients struct {
	EC2 EC2
	IAM IAM
	SSM SSM
}

func CheckDeployment(ctx context.Context, c config.Config, m config.Manifest, p config.Profile) []Check {
	a, err := identity.Load(ctx, c)
	if err == nil {
		err = identity.Verify(ctx, sts.NewFromConfig(a), c.ExpectedAccount)
	}
	if err != nil {
		if ctx.Err() != nil {
			err = ctx.Err()
		} else {
			err = errors.New("cannot verify AWS identity for resource checks; refresh the selected profile and retry")
		}
		return []Check{{"foundation_identity", err}}
	}
	return Verify(ctx, Clients{ec2.NewFromConfig(a), iam.NewFromConfig(a), ssm.NewFromConfig(a)}, m, p)
}

func Verify(ctx context.Context, clients Clients, m config.Manifest, p config.Profile) []Check {
	var checks []Check
	for _, probe := range []struct {
		name string
		run  func() error
	}{
		{"foundation_network", func() error { return checkNetwork(ctx, clients.EC2, m) }},
		{"foundation_image", func() error { return checkImage(ctx, clients.EC2, m, p) }},
		{"foundation_template", func() error { return checkTemplate(ctx, clients.EC2, m, p) }},
		{"foundation_iam", func() error { return checkIAM(ctx, clients.IAM, m) }},
		{"foundation_readiness", func() error { return checkReadiness(ctx, clients.SSM, m) }},
	} {
		err := ctx.Err()
		if err == nil {
			err = probe.run()
		}
		if ctx.Err() != nil {
			err = ctx.Err()
		}
		checks = append(checks, Check{probe.name, err})
	}
	return checks
}

func digest(data []byte) string { sum := sha256.Sum256(data); return hex.EncodeToString(sum[:]) }

// OpenTofu jsonencode and encoding/json both sort object keys and escape HTML.
// Decode IAM percent encoding only when it isn't already JSON: literal '+' stays '+'.
func jsonDigest(document string) (string, error) {
	if !json.Valid([]byte(document)) {
		var err error
		document, err = url.PathUnescape(document)
		if err != nil {
			return "", err
		}
	}
	var value any
	if err := json.Unmarshal([]byte(document), &value); err != nil {
		return "", err
	}
	b, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return digest(b), nil
}

func roleName(arn string) string { return arn[strings.LastIndex(arn, "/")+1:] }

// Message is an allowlist boundary for structured diagnostics, including injected dependencies.
func Message(name string) string {
	switch name {
	case "foundation_network":
		return "network check failed; verify scoped VPC/subnet, DNS, route association, gateway, no ingress and HTTP/HTTPS egress; review a foundation plan"
	case "foundation_image":
		return "image check failed; verify Canonical Ubuntu 24.04 provenance, x86_64 types and sufficient profile disk size"
	case "foundation_template":
		return "template check failed; verify pinned version, image, profile, network, bootstrap digest, encrypted/deleted root and IMDSv2; review and re-export the foundation"
	case "foundation_iam":
		return "IAM check failed; verify role/profile membership, trust and policy digests and absence of extra policies; review with the setup profile and re-export intended changes"
	case "foundation_readiness":
		return "readiness document check failed; verify the exact version and fixed content, then re-export the foundation"
	default:
		return "cannot verify deployed resources; check selected credentials, foundation read permissions and connectivity, then follow docs/setup.md"
	}
}
