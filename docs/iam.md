# Foundation IAM boundary

The setup identity administers durable resources. The operator assumes a separate
role with one inline policy and cannot edit IAM, network rules, templates, SSM
documents or existing tags. The instance has its own one-policy SSM transport
and result-publisher role; neither role has state-bucket access. Doctor detects changed trust/policy
JSON, added attachments, permission boundaries and wrong profile membership
against the trusted manifest. This does not establish the caller's effective
permissions or constrain an administrator.

| Operation | Restriction |
| --- | --- |
| `CreateFleet`: existing resources | Exact regional Canonical AMI, template and selected subnet ARNs; template dependencies also require `RunInstances` permission |
| `RunInstances`: existing resources | Exact AMI, template, selected subnets and security group; required template ARN |
| New fleet | Account/region; required managed/deployment/owner/profile and batch identity creation tags |
| `CreateFleet`: preliminary instance/volume checks | Account/region only; EC2 authorizes placeholder resources before supplying launch tags or properties; template resources also require the constrained `RunInstances` grants |
| New instance through `RunInstances` | Required scope and creation tags; exact instance profile; IMDSv2, approved template and Spot or On-Demand market |
| New root volume through `RunInstances` | Required scope and creation tags; encrypted gp3; complete launch authorization requires the approved template |
| New network interface | Account/region, approved template, exact selected subnet ARNs and public IPv4; exact security group authorized separately |
| `CreateTags` | Instances/volumes only during `RunInstances` or `CreateFleet`, fleets only during `CreateFleet`; fixed scope and twelve allowed keys; no existing-resource retagging |
| `PassRole` | Exact worker role, passed only to EC2 |
| `TerminateInstances` | Account/region and all three managed/deployment/owner resource tags |
| SSM SSH | Matching instance tags; document-access check; only AWS-StartSSHSession |
| SSM readiness | Matching instance tags; only this foundation's fixed parameterless Command document |
| SSM execution | Matching instance tags; only this foundation's fixed runner Command document; no CancelCommand |
| SSM session data channel and cleanup | Session ARN prefix from exact deployment/owner role-session name, enforced in trust |
| IAM doctor reads | Exact two foundation roles and instance profile, plus `GetRole` on the account’s exact Spot service-linked role; no IAM writes |
| Operator S3 result writes | Only `request.json` and `acknowledgement.json` below the exact account/region/deployment/owner result prefix |
| Operator S3 launch records | GetObject/PutObject under the exact `launches/v2/ACCOUNT/REGION/DEPLOYMENT/OWNER/` prefix; separately prefix-scoped ListBucket; no deletion |
| Worker S3 launch records | No object or list access |
| Worker S3 writes | Only `started.json`, `outcome.json`, `stdout`, `stderr`, and `result.json` below the exact result prefix |
| Result reads | Both roles can read the seven protocol object names; ListBucket requires the owned result prefix |
| Runner artifact reads | Both roles can GetObject only the current SHA-256-addressed artifact; bootstrap verifies all bytes, doctor checks the object's hash metadata |
| Result bucket doctor reads | Operator can read location, policy, public-access blocks, ownership, encryption, versioning, lifecycle and tags on this exact bucket |

The session-document condition uses `BoolIfExists`, following the
[AWS SSH-only policy example](https://aws.amazon.com/blogs/machine-learning/integrate-hyperpod-clusters-with-active-directory-for-seamless-multi-user-login/).
Explicit-document requests may omit `ssm:SessionDocumentAccessCheck`; plain `Bool`
incorrectly denied the scoped SSH instance during live acceptance. The condition
still enforces authorization for the default document. Live restricted-role
checks confirmed SSH-document access and denied omitted-document, explicit native
shell and port-forwarding-document requests. Only `AWS-StartSSHSession` is granted.

Creation tags must be supplied on **fleet, instance and volume** tag
specifications. Required keys are `ManagedBy`, `Deployment`, `Owner`, `Profile`,
`Name`, `BaseName`, `NamingVersion=1`, `RequestId`, `BatchId`, `AttemptId`, and
`CreatedAt`; `Group` is allowed and optional. Every required dynamic value must
be nonempty. The template does not invent these values. Legacy `RunInstances` recovery keeps the original seven required
tags (`ManagedBy`, `Deployment`, `Owner`, `Profile`, `Name`, `RequestId`,
`CreatedAt`) and accepts the expanded key allowlist.

The fleet creation grant and the dependent `RunInstances` instance/volume
grants require `ManagedBy=devbox`; this prevents an untagged request from
skipping EC2's dependent `CreateTags` check.
The separate tag grants then enforce the full scope and required metadata for
each API, with `ec2:CreateAction` preventing retagging existing resources. This
avoids repeating the whole tag contract in every creation statement while
preserving its enforcement. See
[AWS creation-time tag authorization](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/supported-iam-actions-tagging.html).

The operator may use Spot and On-Demand. The CLI owns explicit `--on-demand`
consent and instant-only allocation. AWS exposes no `ec2:FleetType` condition;
adding it would deny valid launches. Fleet instance authorization also lacks
`ec2:MetadataHttpTokens`, `ec2:InstanceMarketType` and `ec2:LaunchTemplate`.
The instance checks remain in the separate `RunInstances` path. Live instant
Fleet authorization also checked a placeholder `volume/*` without tags,
encryption, volume type or template context. The preliminary Fleet instance and
volume grant therefore checks account and region only. This grant does not
authorize `RunInstances`; the dependent checks retain required tags, the worker
profile, IMDSv2, and encrypted gp3 volumes.
Shared dependency grants use `ArnEqualsIfExists` for the template
key absent from Fleet authorization. Mandatory RunInstances instance and NIC
grants still require the approved template without `IfExists`, so omitting a
template cannot bypass the complete request's authorization. Fleet override
image/subnet/template resources have exact ARN grants, and template resources
require the constrained `RunInstances` grants. See the
[EC2 authorization reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html#ec2-CreateFleet).

Setup must establish or reuse `AWSServiceRoleForEC2Spot`. The operator receives
only `GetRole` on that exact account-wide role; it cannot create or delete it.
The foundation does not own shared role deletion. AWS explicitly exempts instant
Fleet from `AWSServiceRoleForEC2Fleet`; do not add this second role as a runtime
prerequisite. See [Fleet prerequisites](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-fleet-prerequisites.html).

AWS IAM cannot enforce every CLI invariant. The template ARN condition does not
pin a numeric version. Exact resource ARN grants constrain dependencies, not
every non-resource setting such as user data or root delete-on-termination.
The CLI must use the numeric export and reviewed launch settings. A trusted
operator directly calling AWS may override settings IAM does not expose. Never
claim IAM alone guarantees all template content. Template/document administration
is withheld from the operator. IAM does not validate identity equality, label
syntax, token lineage or uniqueness beyond the required values/nonempty checks;
the CLI owns those invariants.

These permissions require `Resource="*"` and cannot be isolated by inventory tags:

- EC2 `DescribeInstances`, `DescribeVolumes`, `DescribeVpcs`,
  `DescribeVpcAttribute`, `DescribeSubnets`, `DescribeSecurityGroups`,
  `DescribeRouteTables`, `DescribeInternetGateways`, `DescribeImages`,
  `DescribeLaunchTemplates`, `DescribeLaunchTemplateVersions`,
  `DescribeInstanceTypes`, `DescribeInstanceTypeOfferings`,
  `DescribeAvailabilityZones` and `DescribeFleets`.
- SSM `DescribeInstanceInformation` and `GetCommandInvocation`. The latter may
  read command output beyond the deployment if the caller knows command IDs;
  only the two pinned readiness/execution documents are authorized for sending.
- The four instance ssmmessages channel creation/open actions, restricted to the
  selected region.

The existing instance `ssm:UpdateInstanceInformation` grant also retains
`Resource="*"` with the selected-region condition for the already exercised agent
startup/reconnect path. This is a compatibility choice, not an AWS limitation:
the [authorization table](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ssm.html#ssm-UpdateInstanceInformation)
supports instance/managed-instance resources, tags and source-instance conditions
for that action. The retained grant permits broader regional instance reporting;
it does not grant command execution or result reads beyond the listed policies.
No Secrets Manager, Parameter Store, other EC2 or account administration actions
are granted to instance credentials. Development processes with host privileges
can access the instance role; it is not a secret store.

The result bucket is separate from infrastructure state. It has SSE-S3 encryption,
all four public-access blocks, bucket-owner-enforced ownership, and no versioning.
Its policy denies HTTP requests and requires `If-None-Match: *` for every PUT
under both the result and launch-ledger prefixes, including administrator writes.
Both protocols use bounded single PUTs, so the policy grants no multipart
header exemption. Neither runtime role receives DeleteObject, bucket mutation,
artifact upload, or object-tagging permissions. The setup identity uploads the
runner artifact outside the expiring result prefix. Launch records also remain
outside that prefix and have no expiration. They survive command-result retention
and worker teardown until the setup identity deliberately removes the deployment.
The bucket has `force_destroy=false`; ordinary foundation destruction must not
silently erase records. See
[AWS conditional-write enforcement](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes-enforce.html).

Object-name IAM wildcards do not validate the public command ID grammar; the
CLI and runner validate it before access. Scoped ListBucket supports explicit
prefix listings; it is not unrestricted bucket discovery. The development user
has passwordless sudo and can obtain instance credentials, so a malicious or
compromised workload can create misleading records within its allowed owner
prefix. These records are operational recovery data, not tamper-proof attestations.
An administrator can change the bucket policy or delete data. Retention guarantees
depend on preserving the reviewed storage policy and existing promises.

Instant Fleet recovery uses `DescribeFleets` with the known Fleet ID and scoped
instance inventory. AWS omits instant fleets from unqualified Fleet listings and
does not support them in `DescribeFleetInstances`, so the latter is not granted.
See [DescribeFleets](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeFleets.html)
and [DescribeFleetInstances](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeFleetInstances.html).

IAM caps each role's aggregate inline policy at 10,240 characters. The foundation
checks both rendered policies before sending them to IAM; long deployment/owner
labels can exceed this limit because their scoped ARNs repeat. A failed check
requires a reviewed compaction or shorter labels before apply. Do not shorten
an existing deployment's identity without reviewing the resource migration.

The mocked OpenTofu policy tests check separate fleet/instance/volume grants,
wrong dependency/scope exclusions, supported condition keys, role passing, tag
allowlists, ledger immutability/access and policy size. These are static contract
checks. They do not prove live effective AWS permissions; #34 must exercise
restricted-operator Spot and explicit On-Demand launches and rejection cases.

Inventory filters and instance-ID scope validation are implemented in #8,
because the read APIs expose account-wide regional inventory. STS identity checks
precede resource reads and precede mutations. IAM is additive: source
administrator credentials or another attached policy can grant more authority;
run live tests under the restricted role. Organization/session policies, resource
policies and service behavior require integration evidence; policy simulation
alone is insufficient. This personal foundation intentionally scopes to one owner.

The operator's `ssmmessages:OpenDataChannel` permission is scoped to its SSM
session ARN prefix, following AWS's [SSH user-policy example](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-getting-started-enable-ssh-connections.html).
Current Session Manager plugins sign that request; a StartSession grant alone is
insufficient. The instance transport role's channel grants remain `Resource="*"`.
Check the actual signed tunnel during #9 acceptance; the generic ssmmessages
authorization table does not describe the user-session ARN behavior. During
live foundation validation, IAM simulation denied session-ARN requests even with
an exact ARN or hypothetical wildcard policy, while wildcard policy/resource
simulation allowed the action. This conflicts with AWS's scoped SSH example and
appears to reflect the simulator's resource model. It is not evidence to broaden
the deployed grant or claim the tunnel works; see the
[recorded comparisons](acceptance/07-foundation.md#live-results-september-11-2026).

References: [EC2 launch policies](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ExamplePolicies_EC2.html),
[EC2 authorization reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ec2.html),
[SSM authorization reference](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ssm.html),
[SSM message endpoint permissions](https://docs.aws.amazon.com/systems-manager/latest/userguide/systems-manager-setting-up-messageAPIs.html).
