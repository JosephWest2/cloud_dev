# Issue #7 validation

Automated checks: September 10, 2026. Live setup: September 11, 2026.
Host: Arch Linux, linux/amd64.
**Live acceptance is in progress. The state bucket is deployed and its backend
migration and locking are verified; foundation deployment is still pending.**
Do not close #7 until the selected-account workflow below has been executed and
its results recorded. Worker allocation/cleanup and real SSH belong to #8/#9.

## Automated evidence

- OpenTofu **1.12.6**, AWS provider **6.64.0**, signed provider downloads with
  committed dependency lockfiles for both roots.
- `make check`: Go formatting, dependency checksums, build, vet and tests pass.
- `go test -race ./...` and `GOTOOLCHAIN=go1.24.0 go test ./...`: pass. `bash -n infra/foundation/bootstrap.sh` passes.
  Generated state/input/plan/backend/acceptance paths are confirmed ignored.
- `make infra-check`: recursive format check, both provider-backed validations,
  and five mock-provider tests (four foundation runs, one state-backend plan).
- Foundation mock plan checks zero ingress, HTTP/HTTPS egress, encrypted/deleted
  root, IMDSv2, public-IP settings, explicit AMI and bootstrap bytes. Negative
  plans reject an unsupported region/image. A mock apply resolves policy ARNs
  and checks launch tags, PassRole, scoped termination and inline-policy size.
- State mock plan checks encryption, versioning, public-access blocking and no
  forced bucket deletion. Native S3 locking is configured in backend blocks;
  live backend locking also passed during the selected-account setup below.
- Controlled Go API responses exercise successful resource validation, exact
  numeric API requests, network/image/template drift, bad root sizing, altered
  bootstrap/readiness content, foreign resources, added IAM policies including
  second-page attachments, all read-API failures/nil responses and cancellation.
- Doctor tests prevent resource calls after invalid/wrong-scope/schema-v1 manifests
  or identity failures, preserve timeout classification and redact provider errors.
  Canonical JSON digest vectors include percent encoding, plus signs and HTML.
- `make infra-check` also feeds the real mock-provider export into the Go
  manifest parser and verifies bootstrap/IAM/readiness digests against OpenTofu
  resource values; the exporter-to-CLI contract passes.

Mock plan/apply evidence checks configuration logic only. It is not an AWS plan,
policy authorization proof, real AMI confirmation or evidence that bootstrap ran.
Actual regional AMI selection remains a local setup input, never a made-up ID
committed as a deployable default.

## Human live acceptance (selected test account only)

Follow [setup](../setup.md) first. Keep generated outputs under
`infra/foundation/acceptance-output/` (ignored), inspect them locally, and record
only non-secret summaries here. Use the setup profile for provisioning, state
checks, Access Analyzer and simulation. Use **devbox-operator** for doctor.

1. Record test account/profile/deployment/owner locally; run STS and confirm the
   account before either apply. Record `tofu version`, formatting/validation,
   reviewed saved plan and apply results for local bootstrap, state migration and
   foundation. Confirm neither plan contains an instance, EIP, NAT or paid endpoint.
2. Export the named schema-v2 manifest. Run `devbox doctor --aws-profile
   devbox-operator --timeout 60s --json` and save the result. Every check must pass.
   If a trust/policy digest differs, compare decoded documents using the setup
   profile; report/fix normalization or real drift, never disable validation.
3. Verify the backend as below, including a genuine competing lock attempt.
4. Inspect the security group and exact template version as below. Confirm no
   ingress, root encryption/deletion, required IMDSv2, and exact AMI/version.
5. Run IAM Access Analyzer validation and targeted simulations as below. Record
   all ERROR/SECURITY_WARNING findings and expected allowed/denied decisions;
   investigate rather than suppress unexplained results. Full RunInstances/SSM
   effective authorization also needs #8/#9 integration; no worker launch is
   part of this issue's acceptance.
6. Copy the manifest to a temporary config directory; change account, region or
   schema version individually. Run doctor and confirm `manifest_unavailable`
   and skipped resource checks. Restore the real export. `git status --ignored
   --short infra` must show local state/inputs/plans as ignored; never commit them.
7. Check the export only contains the documented fields and no credential values.
   Follow foundation teardown/recovery instructions if the test foundation will
   not be used for #8. Record whether the S3 bucket is retained or deleted.

### Backend inspection

```sh
aws s3api get-bucket-encryption --bucket YOUR_STATE_BUCKET
aws s3api get-bucket-versioning --bucket YOUR_STATE_BUCKET
aws s3api get-public-access-block --bucket YOUR_STATE_BUCKET
aws s3api get-bucket-policy --bucket YOUR_STATE_BUCKET
aws s3api head-object --bucket YOUR_STATE_BUCKET --key bootstrap/terraform.tfstate
aws s3api head-object --bucket YOUR_STATE_BUCKET --key foundation/YOUR_DEPLOYMENT/terraform.tfstate
```

Expect AES256, Enabled versioning, all four public-access flags true, a non-TLS
Deny, and encrypted state objects with version IDs. Check both local backend
blocks/cache entries specify `encrypt=true` and `use_lockfile=true`, the correct
bucket/account, and distinct keys (do not publish `.terraform` contents).

For an actual concurrency check, in terminal A run `tofu console
-var-file=foundation.tfvars` in the initialized foundation root and leave its
interactive prompt open. OpenTofu console holds the state lock without changing
resources. In terminal B, using the same root and setup profile, run
`tofu plan -lock-timeout=1s -var-file=foundation.tfvars`. Expect an acquiring-state-
lock error identifying the live lock. Inspect the matching key plus `.tflock`
with `head-object`, then exit terminal A's console. Confirm a new plan succeeds
and the current lock object is gone. Never force-unlock the live process. Keep
evidence of contention rather than merely asserting the option is enabled.

### Deployed template/API inspection

Set `manifest_path` to your exported file (absolute path):

```sh
aws ec2 describe-security-groups \
  --group-ids "$(jq -r .security_group_id "$manifest_path")" \
  --query 'SecurityGroups[].{Ingress:IpPermissions,Egress:IpPermissionsEgress,VPC:VpcId}'
aws ec2 describe-launch-template-versions \
  --launch-template-id "$(jq -r .images.agent.launch_template_id "$manifest_path")" \
  --versions "$(jq -r .images.agent.launch_template_version "$manifest_path")" \
  --query 'LaunchTemplateVersions[].{Version:VersionNumber,Image:LaunchTemplateData.ImageId,Root:LaunchTemplateData.BlockDeviceMappings,Metadata:LaunchTemplateData.MetadataOptions,Network:LaunchTemplateData.NetworkInterfaces,Profile:LaunchTemplateData.IamInstanceProfile}'
```

Compare with manifest pins and the separately recorded Canonical provenance.
SSM availability and bootstrap runtime are intentionally untested until a worker
exists. Doctor checks their durable configuration and exact probe content only.

### IAM checks

Use the setup profile; these APIs are deliberately absent from the operator role.
AWS Access Analyzer may not be available under every administrator policy; record
that explicitly if denied. Run for each role from the manifest:

```sh
mkdir -p infra/foundation/acceptance-output
role_arn=$(jq -r .roles.operator.arn "$manifest_path")
role_name=${role_arn##*/}
policy_name=$(jq -r .roles.operator.policy_name "$manifest_path")
aws iam get-role-policy --role-name "$role_name" --policy-name "$policy_name" \
  --query PolicyDocument --output json > infra/foundation/acceptance-output/operator-policy.json
aws accessanalyzer validate-policy --policy-type IDENTITY_POLICY \
  --policy-document file://infra/foundation/acceptance-output/operator-policy.json
# Repeat for roles.instance. Also validate the two trust policies as
# RESOURCE_POLICY with --validate-policy-resource-type AWS::IAM::AssumeRolePolicyDocument.
```

For the trust policies, use `aws iam get-role --role-name ROLE --query
Role.AssumeRolePolicyDocument --output json` as input. Validate the fixed document
rather than adding general SendCommand privileges to address readiness failures.

Example targeted termination simulation (replace deployment/owner values):

```sh
aws iam simulate-principal-policy --policy-source-arn "$role_arn" \
  --action-names ec2:TerminateInstances \
  --resource-arns arn:aws:ec2:us-east-2:YOUR_ACCOUNT:instance/i-0123456789abcdef0 \
  --context-entries \
    ContextKeyName=aws:RequestedRegion,ContextKeyType=string,ContextKeyValues=us-east-2 \
    ContextKeyName=ec2:ResourceTag/ManagedBy,ContextKeyType=string,ContextKeyValues=devbox \
    ContextKeyName=ec2:ResourceTag/Deployment,ContextKeyType=string,ContextKeyValues=YOUR_DEPLOYMENT \
    ContextKeyName=ec2:ResourceTag/Owner,ContextKeyType=string,ContextKeyValues=YOUR_OWNER
```

Expect allowed. Repeat with a different owner, deployment, account ARN and region;
each must be denied. Also simulate these targeted cases using the
[service's documented context keys](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html):

| Case | Expected |
| --- | --- |
| PassRole to exact instance role with `iam:PassedToService=ec2.amazonaws.com` | Allowed |
| PassRole to another role or service | Denied |
| CreateTags outside RunInstances / DeleteTags / template modifications / IAM writes | Denied |
| Launch instance/volume with omitted dynamic tag, wrong scope, encryption false or IMDSv1 | Denied |
| SSM StartSession with owned instance and AWS-StartSSHSession document-access check | Allowed |
| SSM StartSession/SendCommand on another owner's instance | Denied |
| ssmmessages:OpenDataChannel on the configured role-session ARN prefix / another prefix | Allowed / denied |
| SendCommand using AWS-RunShellScript instead of the fixed readiness document | Denied |

Use complete API-specific context; an implicit deny from a missing context key is
not proof of a tested tag boundary. Simulations do not execute AWS requests or
model all SCPs/session/resource policies. Record `MissingContextValues` and
resolve them for expected-allowed cases. See [IAM limits](../iam.md).

## Live results (September 11, 2026)

The user explicitly selected and authenticated the setup profile with a non-root
IAM administrator, then authorized applying the reviewed state-bootstrap plan.
Account/resource identifiers and raw output are retained only in the ignored
local inputs and `infra/foundation/acceptance-output/`.

- Selected-account bootstrap apply: **passed**. Six additions: one S3 bucket and
  its five protection configurations; no worker, network or IAM resources applied.
- Bootstrap state migration: **passed**. Local state migrated to the bucket's
  bootstrap key; all six resources remain in inventory. A protected local recovery
  copy was retained. The following plan reported no changes.
- S3 protections: **passed via AWS APIs**. AES256 default encryption, versioning,
  all four public-access blocks, bucket-owner-enforced ownership and non-TLS Deny.
  The remote state object has AES256 encryption and a nonempty version ID.
- Native S3 locking: **passed live**. An open OpenTofu console held the `.tflock`
  object; a competing plan failed to acquire it. Exiting the console removed the
  current lock object and a subsequent plan succeeded without drift. Backend
  cache inspection confirmed encryption, lockfiles and selected account/key.
- Ohio AMI provenance: **passed**. Resolved an exact Canonical Ubuntu 24.04 amd64
  server AMI; EC2 confirmed owner, release/name, architecture, availability and an
  8 GiB root snapshot. Exact IDs/creation evidence are saved locally.
- Foundation live plan: **passed**, saved but not applied. Thirteen expected
  network/IAM/template/document additions, zero changes/deletions. Reviewed the
  exact AMI, zero-ingress group, root settings, IMDSv2 and selected trust principal.
- Foundation apply, exported manifest and operator-profile doctor: **pending**.
- Access Analyzer and targeted IAM decisions: **pending**.
- State bucket is retained for the ongoing setup; full teardown: **pending**.

## Independent PR review

A different agent reviewed PR #12 after creation and found three issues, all
corrected before the final handoff:

- Ubuntu 24.04 socket activation may leave `/run/sshd` absent before the first
  service start. Bootstrap now creates its root-owned 0755 runtime directory
  before validating sshd configuration. The reviewer checked Canonical's package
  behavior and confirmed the fix; actual cloud-init execution remains a live gate.
- Current Session Manager plugins sign the operator's OpenDataChannel request.
  The operator policy now grants `ssmmessages:OpenDataChannel` only on the same
  deployment/owner session prefix used for cleanup, following AWS's SSH policy.
  A native infrastructure assertion checks the grant and its scope.
- A no-drift refresh-only apply cannot reliably hold a lock at a prompt. The live
  concurrency procedure now uses an open OpenTofu console; the reviewer reproduced
  console-versus-plan lock contention locally with 1.12.6.

The reviewer rechecked the corrections and reported no remaining actionable
findings. This is code-review evidence, not live AWS acceptance.
