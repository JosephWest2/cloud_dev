# Foundation IAM boundary

The setup identity administers durable resources. The operator assumes a separate
role with one inline policy and cannot edit IAM, network rules, templates, SSM
documents or existing tags. The instance has its own one-policy SSM transport
role; neither role has state-bucket access. Doctor detects changed trust/policy
JSON, added attachments, permission boundaries and wrong profile membership
against the trusted manifest. This does not establish the caller's effective
permissions or constrain an administrator.

| Operation | Restriction |
| --- | --- |
| `RunInstances`: existing resources | Exact regional Canonical AMI, template, subnet and security group ARNs; required template ARN |
| New instance | Account/region; `ManagedBy=devbox`, deployment, owner, `Profile=agent`; nonempty Name/RequestId/CreatedAt; only seven allowed tag keys; IMDSv2; exact profile; On-Demand only |
| New root volume | Account/region; same required creation tags; encrypted; required template |
| New network interface | Account/region and template-resource condition; interface subnet/security groups come from the template |
| `CreateTags` | Instances/volumes only, during `RunInstances` only, fixed scope and allowed tag keys; no retagging existing instances |
| `PassRole` | Exact worker role, passed only to EC2 |
| `TerminateInstances` | Account/region and all three managed/deployment/owner resource tags |
| SSM SSH | Matching instance tags; document-access check; only AWS-StartSSHSession |
| SSM readiness | Matching instance tags; only this foundation's fixed parameterless Command document |
| SSM session data channel and cleanup | Session ARN prefix from exact deployment/owner role-session name, enforced in trust |
| IAM doctor reads | Exact two roles and instance profile; no IAM writes |

Creation tags must be supplied on **both instance and volume** tag specifications
by the future launch command; the template deliberately does not invent dynamic
request/name/time tags. Spot is the profile's preserved default, but this initial
operator role permits only explicit On-Demand; #8 must explain `--on-demand`.

AWS IAM cannot enforce every CLI invariant. The template ARN condition does not
pin a numeric version. Template-resource conditions restrict resource overrides,
not every non-resource setting such as user data or root delete-on-termination.
The CLI must use the numeric export and reviewed launch settings. A trusted
operator directly calling AWS may override settings IAM does not expose. Never
claim IAM alone guarantees all template content. Template/document administration
is withheld from the operator. IAM does not validate the format or uniqueness of
Name/RequestId/CreatedAt beyond the nonempty requirements; the CLI owns that.

These permissions require `Resource="*"` and cannot be isolated by inventory tags:

- EC2 `DescribeInstances`, `DescribeVolumes`, `DescribeVpcs`,
  `DescribeVpcAttribute`, `DescribeSubnets`, `DescribeSecurityGroups`,
  `DescribeRouteTables`, `DescribeInternetGateways`, `DescribeImages`,
  `DescribeLaunchTemplates`, `DescribeLaunchTemplateVersions`,
  `DescribeInstanceTypes`.
- SSM `DescribeInstanceInformation` and `GetCommandInvocation`. The latter may
  read command output beyond the deployment if the caller knows command IDs;
  no arbitrary command-sending permission is granted.
- Instance `ssm:UpdateInstanceInformation` and the four ssmmessages channel
  creation/open actions. They are restricted to the selected region. No S3,
  Secrets Manager, Parameter Store, other EC2 or account administration actions
  are granted to instance credentials. Development processes with host privileges
  can access the instance role; it is not a secret store.

Inventory filters and instance-ID scope validation in #8 remain mandatory,
because the read APIs expose account-wide regional inventory. STS identity checks
precede resource reads and must precede later mutations. IAM is additive: source
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
