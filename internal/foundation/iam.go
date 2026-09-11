package foundation

import (
	"context"
	"errors"

	"github.com/JosephWest2/cloud_dev/internal/config"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
)

func checkIAM(ctx context.Context, api IAM, m config.Manifest) error {
	fail := errors.New("foundation IAM profile, trust or permissions differ from the trusted export or cannot be read; review policies and attachments with the setup profile, apply intended changes and re-export")
	out, err := api.GetInstanceProfile(ctx, &iam.GetInstanceProfileInput{InstanceProfileName: aws.String(roleName(m.InstanceProfileARN))})
	if err != nil || out == nil || out.InstanceProfile == nil {
		return fail
	}
	profile := out.InstanceProfile
	if aws.ToString(profile.Arn) != m.InstanceProfileARN || len(profile.Roles) != 1 || aws.ToString(profile.Roles[0].Arn) != m.Roles["instance"].ARN {
		return fail
	}
	for _, key := range []string{"instance", "operator"} {
		expected := m.Roles[key]
		name := aws.String(roleName(expected.ARN))
		role, err := api.GetRole(ctx, &iam.GetRoleInput{RoleName: name})
		if err != nil || role == nil || role.Role == nil || aws.ToString(role.Role.Arn) != expected.ARN || role.Role.PermissionsBoundary != nil {
			return fail
		}
		trust, err := jsonDigest(aws.ToString(role.Role.AssumeRolePolicyDocument))
		if err != nil || trust != expected.TrustSHA256 {
			return fail
		}
		tags := map[string]string{}
		for _, t := range role.Role.Tags {
			tags[aws.ToString(t.Key)] = aws.ToString(t.Value)
		}
		if tags["ManagedBy"] != "devbox" || tags["Deployment"] != m.Deployment || tags["Owner"] != m.Owner {
			return fail
		}
		var marker *string
		count := 0
		for {
			policies, err := api.ListRolePolicies(ctx, &iam.ListRolePoliciesInput{RoleName: name, Marker: marker})
			if err != nil || policies == nil {
				return fail
			}
			for _, policy := range policies.PolicyNames {
				if policy != expected.PolicyName {
					return fail
				}
				count++
			}
			if !policies.IsTruncated {
				break
			}
			if aws.ToString(policies.Marker) == "" || aws.ToString(policies.Marker) == aws.ToString(marker) {
				return fail
			}
			marker = policies.Marker
		}
		if count != 1 {
			return fail
		}
		marker = nil
		for {
			attached, err := api.ListAttachedRolePolicies(ctx, &iam.ListAttachedRolePoliciesInput{RoleName: name, Marker: marker})
			if err != nil || attached == nil || len(attached.AttachedPolicies) != 0 {
				return fail
			}
			if !attached.IsTruncated {
				break
			}
			if aws.ToString(attached.Marker) == "" || aws.ToString(attached.Marker) == aws.ToString(marker) {
				return fail
			}
			marker = attached.Marker
		}
		policy, err := api.GetRolePolicy(ctx, &iam.GetRolePolicyInput{RoleName: name, PolicyName: aws.String(expected.PolicyName)})
		if err != nil || policy == nil || aws.ToString(policy.RoleName) != aws.ToString(name) || aws.ToString(policy.PolicyName) != expected.PolicyName {
			return fail
		}
		hash, err := jsonDigest(aws.ToString(policy.PolicyDocument))
		if err != nil || hash != expected.PolicySHA256 {
			return fail
		}
	}
	return nil
}

func checkReadiness(ctx context.Context, api SSM, m config.Manifest) error {
	fail := errors.New("pinned readiness document is missing, inaccessible or changed; review and re-export the foundation's fixed bootstrap probe")
	out, err := api.GetDocument(ctx, &ssm.GetDocumentInput{Name: aws.String(m.Readiness.Name), DocumentVersion: aws.String(m.Readiness.Version), DocumentFormat: ssmtypes.DocumentFormatJson})
	if err != nil || out == nil || aws.ToString(out.Name) != m.Readiness.Name || aws.ToString(out.DocumentVersion) != m.Readiness.Version || out.DocumentType != ssmtypes.DocumentTypeCommand || out.Status != ssmtypes.DocumentStatusActive {
		return fail
	}
	hash, err := jsonDigest(aws.ToString(out.Content))
	if err != nil || hash != m.Readiness.ContentSHA256 {
		return fail
	}
	return nil
}
