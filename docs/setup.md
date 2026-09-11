# Provision and verify the durable foundation

This sets up the resources that future devboxes will use. It **does not launch a
machine**. You need an AWS account and an authenticated local AWS profile first.
If you do not have those yet, stop before the apply steps and set up AWS access;
do not paste passwords, access keys or tokens into this repository or a chat.

The implemented starting choices are Ohio (`us-east-2`), Canonical Ubuntu 24.04
LTS on x86-64, a dedicated public subnet, outbound TCP 80/443, no inbound rules,
and Linux locally. These are implementation defaults; no AWS deployment was
performed during development. The exact regional AMI is selected and recorded
locally below. Other regions, partitions, architectures and private networking
need a separate change. Owner and deployment each allow 1–23 letters, digits,
underscores or hyphens, starting with a letter/digit.

## Tools and identities

Use **OpenTofu 1.12.6**, **hashicorp/aws 6.64.0** (both roots enforce these exact
versions), Go 1.24+, AWS CLI v2, and `jq`. Keep the committed provider lockfiles.
Install OpenTofu from its [official releases](https://github.com/opentofu/opentofu/releases/tag/v1.12.6)
and verify release signatures/checksums using the
[installation instructions](https://opentofu.org/docs/intro/install/).
`tofu version` must report 1.12.6. No OpenTofu executable is required by devbox.

Use a **setup profile** authorized to create/delete VPC networking, IAM roles,
instance profiles/policies, launch templates, SSM documents and the S3 state
bucket. Existing account administration is outside the operator policy. No
setup permissions are delegated to workers or the operator. Use your normal
AWS credential chain (SSO, assume-role, credential process, etc.); do not pass
credential values in provider/backend arguments. Refresh authentication before
running noninteractive commands. Do not enable SDK/provider debug tracing.

```sh
export AWS_PROFILE=YOUR_EXISTING_SETUP_PROFILE
export AWS_REGION=us-east-2
aws sts get-caller-identity
```

Confirm this is your intended test account. Record the account ID locally and
identify the existing **IAM role or IAM user ARN** that will assume the operator
role. An STS `assumed-role/.../session` ARN is not that IAM role ARN: use the IAM
role shown in your AWS profile/account configuration. Choose a short deployment
name (e.g. `personal-dev`) and stable owner (e.g. `joseph`). No public-IP address
or worker is allocated by this setup.

## 1. Bootstrap local state, then migrate it

Run from the checkout root. Keep local state/plans on an encrypted disk and
restrict permissions. Generated files below are ignored by Git.

```sh
umask 077
cd infra/state-bootstrap
# Replace the example account and bucket values before continuing.
cat > bootstrap.tfvars <<'VARS'
account_id  = "REPLACE_12_DIGIT_TEST_ACCOUNT"
bucket_name = "REPLACE-GLOBALLY-UNIQUE-LOWERCASE-BUCKET"
VARS
tofu init
tofu fmt -check
tofu validate
tofu plan -var-file=bootstrap.tfvars -out=bootstrap.tfplan
tofu show -no-color bootstrap.tfplan
# Review the account and S3-only changes, then:
tofu apply bootstrap.tfplan
```

This initially writes local `terraform.tfstate`. The bucket has SSE-S3 (AES256),
versioning, bucket-owner-enforced ownership, all public-access blocks, and a
policy denying non-TLS requests. It has `prevent_destroy` and no forced deletion.

```sh
cp backend.hcl.example backend.hcl
# Edit bucket and allowed_account_ids to match bootstrap.tfvars.
cp backend.tf.example backend.tf
tofu init -migrate-state -backend-config=backend.hcl
# Accept copying the existing local state to S3 when OpenTofu prompts.
tofu state list
```

The bootstrap root uses `bootstrap/terraform.tfstate`. The backend block enables
`encrypt=true` and `use_lockfile=true`. This is OpenTofu's
[native S3 locking](https://opentofu.org/docs/language/settings/backends/s3/),
using a `.tflock` object, not a DynamoDB table. After migration, all collaborators
must use this backend and the pinned tool version. Keep one encrypted recovery
copy of pre-migration state until migration is verified; remove other local
state backups and saved plans when no longer needed.

The setup identity needs `s3:ListBucket` on the exact bucket,
`s3:GetObject`/`s3:PutObject` on each state key, and
`s3:GetObject`/`s3:PutObject`/`s3:DeleteObject` on the corresponding `.tflock`
keys. Recovery additionally needs object-version read/list access. Bucket
management requires the corresponding S3 administration actions. Backend access
is deliberately absent from the operator and instance roles. Supply credentials
through the environment/profile chain, never `backend.hcl`. Backend settings are
cached in `.terraform`, which is also ignored.

## 2. Resolve and record the exact AMI

From the checkout root, using the setup profile in Ohio:

```sh
mkdir -p infra/foundation/acceptance-output
aws ssm get-parameter \
  --name /aws/service/canonical/ubuntu/server/24.04/stable/current/amd64/hvm/ebs-gp3/ami-id \
  --query Parameter.Value --output text
```

Copy the returned `ami-...` into `foundation.tfvars` in the next section. The
`current` parameter is used **only for this explicit setup-time selection**;
OpenTofu and devbox never resolve it during a launch. Inspect the chosen ID:

```sh
aws ec2 describe-images --image-ids YOUR_EXACT_AMI_ID --owners 099720109477 \
  --query 'Images[].{ID:ImageId,Owner:OwnerId,Name:Name,State:State,Architecture:Architecture,Root:RootDeviceName,Blocks:BlockDeviceMappings,Created:CreationDate}' \
  > infra/foundation/acceptance-output/ami-provenance.json
cat infra/foundation/acceptance-output/ami-provenance.json
```

Expect owner `099720109477`, `available`, `x86_64`, `/dev/sda1`, and name
`ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-SERIAL`. The template
uses a 100 GiB gp3 root and the snapshot must fit. See Canonical's
[image discovery documentation](https://ubuntu.com/aws/docs/aws-how-to/instances/find-ubuntu-images/).
The manifest records this exact AMI, owner/name, release, architecture and numeric
launch-template version. Image updates require an explicit input change, reviewed
plan/apply, and fresh export. Old exports keep their pinned version; do not use
`$Latest`/`$Default`. IAM itself cannot enforce a numeric template version.

## 3. Apply the foundation and export

```sh
cd infra/foundation
cp foundation.tfvars.example foundation.tfvars
cp backend.hcl.example backend.hcl
# Edit ALL placeholder account/owner/principal/AMI/bucket values.
# The foundation key must differ from the bootstrap key.
tofu init -backend-config=backend.hcl
tofu fmt -check
tofu validate
tofu plan -var-file=foundation.tfvars -out=foundation.tfplan
tofu show -no-color foundation.tfplan
# Verify there is no aws_instance, EIP or NAT gateway in this plan, then:
tofu apply foundation.tfplan
mkdir -p "${XDG_CONFIG_HOME:-$HOME/.config}/devbox"
tofu output -json deployment_manifest > "${XDG_CONFIG_HOME:-$HOME/.config}/devbox/deployment.json"
```

Only export the named output. Do not export full state or all provider diagnostics.
This schema-v2 JSON is non-secret, but contains account/resource identifiers; keep
it local. The manifest is trusted configuration: protect it from unauthorized
edits. It is not signed and is not an IAM authorization token. Existing schema-v1
fixtures must be replaced with a real schema-v2 export.

Durable resources are VPC/subnet/IGW/routing, security group, two IAM roles and
policies, instance profile, launch template and fixed SSM readiness document.
Removing a future worker leaves these and the S3 backend in place. The foundation
allocates **no EC2 instance, EBS volume, public IPv4 allocation, NAT gateway or paid
VPC endpoint**. S3 state storage, versions and requests are billable durable
usage. Worker compute, disk, public IPv4 and applicable transfer charges begin
only when a worker is launched; consult your AWS account's pricing before that
later lifecycle test.

## 4. Configure the restricted operator and run doctor

Add a profile to your normal AWS config (`~/.aws/config`), replacing all values:

```ini
[profile devbox-operator]
role_arn = arn:aws:iam::ACCOUNT:role/devbox-DEPLOYMENT-OWNER-operator
source_profile = YOUR_EXISTING_SETUP_PROFILE
role_session_name = devbox-DEPLOYMENT-OWNER
region = us-east-2
```

If the setup profile uses browser-based `aws login`, the pinned Go SDK cannot
use that `login_session` profile directly as a role's `source_profile`. The AWS
CLI can, so a successful CLI identity check alone does not expose this SDK issue.
Create a process bridge (replace `YOUR_EXISTING_SETUP_PROFILE` in the command):

```sh
aws configure set credential_process 'aws configure export-credentials --profile YOUR_EXISTING_SETUP_PROFILE --format process' --profile devbox-setup-credentials
aws configure set region us-east-2 --profile devbox-setup-credentials
aws configure set source_profile devbox-setup-credentials --profile devbox-operator
```

This delegates temporary-credential retrieval to the already authenticated AWS
CLI through the normal credential chain. It stores a command, not access keys.
Do not run the export command by itself or log its output. Keep the bridge and
source profile distinct to avoid recursion. SSO and other supported source-profile
mechanisms can use the original profile directly. See AWS's
[credential-process compatibility instructions](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sign-in.html).

The exact session name is required by the operator trust policy and scopes its
SSM session cleanup permissions. Your source identity must be allowed to assume
that exact role; a permissions boundary or organization policy may additionally
restrict it. For an SSO source profile, log in to that source profile first.
Do not attach administrator policies to make the operator pass.

Edit devbox `config.toml` (copy `examples/config.toml` if needed) to match account,
region, deployment and owner from the export; set `aws_profile="devbox-operator"`.
Then, from the checkout root:

```sh
make check
make build
./bin/devbox doctor --aws-profile devbox-operator --timeout 60s --json
```

The explicit flag avoids the setup `AWS_PROFILE` environment overriding TOML.
Install the local OpenSSH client and Session Manager plugin per the README.
Use a currently supported plugin (at least 1.2.764.0 as of this implementation;
see AWS's [release history](https://docs.aws.amazon.com/systems-manager/latest/userguide/plugin-version-history.html)).
Doctor checks executability; the human setup check must verify the version.
All checks should pass **using the operator profile**. Doctor reads the actual
network, pinned image/template, instance-type architectures, instance-profile
membership, both role trusts and sole inline policies, and the pinned readiness
document. Extra managed/inline policies fail. It checks the template user-data
hash, IMDSv2, public-IP/interface settings and encrypted/deleted root settings.
IAM policy responses are URL-decoded and JSON-canonicalized before comparing
hashes to the trusted export. A service normalization mismatch must be investigated
and fixed/re-exported; never bypass the check.

A passing doctor establishes these resource checks, not effective authorization
or a working machine. It does not evaluate SCPs, session policies, caller policy
attachments, NACLs, service quotas, capacity, actual instance egress, package
repository availability or remote bootstrap execution. It never starts a machine,
opens a session, sends a readiness command, invokes OpenTofu or reads state.
Run the additional [live acceptance checks](acceptance/07-foundation.md) before
closing issue #7. Actual launch/cleanup acceptance belongs to #8; key provisioning,
host-key verification, SSH proxy/editor/file transfer belong to #9.

## Bootstrap and readiness contract for #9

The template's minimal script creates the `devbox` development user, enables
sshd, disables root/password/keyboard-interactive SSH, and permits only `devbox`
SSH logins. The development user has passwordless sudo within its disposable
machine. No SSH private/public key or AWS credential is embedded. SSH authentication
will be implemented in #9; **this foundation alone cannot log you in**.
The selected standard Ubuntu image must include the SSM agent snap; bootstrap
fails if it is absent. Transport uses the regional SSM/ssmmessages endpoints.

The fixed, parameterless SSM Command document reads root-owned files:

- `/var/lib/devbox/bootstrap-complete` → stdout `complete`.
- `/var/lib/devbox/bootstrap-failed` → stdout `failed`.
- Neither file → stdout `pending`.

The command exits zero for these three observable states. API/command failure is
separate. #9 must send the exact manifest document version (and content hash),
then parse the stable status; no arbitrary shell command is accepted. Completion
requires successful sshd setup and an active SSM agent service. A failure before
the agent is reachable is only observable as a bounded readiness timeout. The
marker is a readiness hint, not a security attestation against a root-capable
worker. Inspect `/var/log/cloud-init-output.log` locally on the instance during
later authorized troubleshooting; do not publish raw logs.

## Recovery and teardown

Do not edit remote state by hand or run two independent state copies. On a stale
lock, first establish that the owning process is no longer running, then use
`tofu force-unlock LOCK_ID` in the correct root. Do not routinely use `-lock=false`
or delete `.tflock` objects to work around active writers.

If a state write or migration failed, stop all writers. Inspect the configured
bucket/key/account and `tofu state list` before deciding which copy is authoritative.
Use `aws s3api list-object-versions --bucket BUCKET --prefix EXACT_STATE_KEY` and
`aws s3api get-object --bucket BUCKET --key EXACT_STATE_KEY --version-id VERSION
recovered.tfstate` to retrieve a known good encrypted backup into a protected
local file. Inspect lineage/serial locally, back up the current remote version,
and use `tofu state push recovered.tfstate` only after understanding any serial or
lineage disagreement. Do not use `-force` to silence it. Then plan and reconcile
resources against AWS; a state restore does not undo infrastructure changes.
See [OpenTofu state recovery commands](https://opentofu.org/docs/cli/commands/state/).

For full teardown, first inventory and terminate any workers using the later
lifecycle CLI (#8), or explicitly verify their ownership and clean them up using
AWS during an authorized test. Do not delete networking/IAM beneath running
workers. Then run in `infra/foundation` with the setup profile:

```sh
tofu plan -destroy -var-file=foundation.tfvars -out=destroy.tfplan
tofu show -no-color destroy.tfplan
tofu apply destroy.tfplan
```

Remove/retire the local manifest. Keep the state bucket for recovery if desired;
it continues to store billable state versions. To remove the bucket as well:

1. Stop all writers and securely save both roots' latest state and required history.
2. In each root, temporarily replace/remove the `backend "s3"` block and run
   `tofu init -migrate-state` to move its state back locally. For bootstrap remove
   the generated `backend.tf`; for foundation temporarily remove only its backend
   block. Confirm local state is authoritative, then remove cached `.terraform`
   backend metadata only if reinitialization is needed. Never delete state itself
   to reset a backend.
3. After both roots no longer depend on S3, explicitly remove **all object versions
   and delete markers** from that one bucket using your S3 administration tools.
   An ordinary `aws s3 rm --recursive` does not empty version history. Double-check
   account and bucket; this irreversibly removes recovery history.
4. Temporarily change bootstrap's `prevent_destroy` to false, review
   `tofu plan -destroy -var-file=bootstrap.tfvars -out=destroy.tfplan`, then apply it.
   `force_destroy` remains false so an unexpectedly nonempty bucket blocks deletion.
5. Restore the tracked backend/protection declarations without overwriting other
   changes; remove generated backend files, local input/plan/state copies when no
   longer needed, and the local operator profile. Keep protected backups according
   to your own retention needs.
