# CLI contracts (config/profile/results v1, deployment v2)

## User configuration and workload profile

`examples/config.toml` is the complete user schema. `schema_version = 1`,
`expected_account`, `region`, `deployment`, and `owner` are required.
The expected account is a quoted 12-digit string. Deployment and owner are
1–63 ASCII letters, digits, underscores or hyphens, beginning with a letter or
digit. A region must have AWS region syntax; availability and partition-specific
resource checks belong to the foundation slice. `aws_profile`, `manifest`,
and `profile_file` are optional; precedence is documented in the README.
Unknown TOML fields, invalid types, malformed TOML and missing/unsupported
schema versions are rejected. This includes credential fields.

The embedded [agent profile](../profiles/agent.toml) is a separate versioned
document. An optional user profile_file uses the same contract:

| Field | Version 1 contract |
| --- | --- |
| `schema_version` | Integer `1` |
| `name` | `agent`; the only workload name in this slice |
| `market` | `spot` or `on-demand`; bundled agent defaults to **spot** |
| `instance_types` | Nonempty array of distinct EC2 type names; hardware compatibility checked later |
| `image` | Manifest image key, with the same character/length rules as deployment |
| `disk_gb` | Integer 8–16384; actual image size and volume constraints checked later |

The launch path preserves explicit market selection. Until Spot launches
are implemented, `devbox up agent` using the default profile must explain that
Spot is unsupported and show `devbox up agent --on-demand`. It must not launch
On-Demand implicitly. `--on-demand` is a launch override, not a doctor
option. No market fallback is implemented here.

## Deployment manifest

The foundation exports a non-secret schema-v2 JSON object using
`tofu output -json deployment_manifest`. Schema 1 is deliberately rejected:
re-export after applying the foundation. Unknown fields and trailing JSON are
rejected. User config, workload profiles and result envelopes stay at version 1.

| Manifest field | Contract |
| --- | --- |
| `schema_version` | Integer 2 |
| `account`, `region`, `deployment`, `owner` | Must equal effective config; foundation currently Ohio only, labels at most 23 characters |
| `vpc_id`, `subnet_ids`, `security_group_id` | Exact EC2 IDs, exactly one subnet |
| `route_table_id`, `internet_gateway_id` | Exact EC2 IDs for explicit public route/association |
| `instance_profile_arn` | Exact commercial-AWS IAM profile ARN in expected account |
| `development_user` | `devbox` |
| `bootstrap_sha256` | Lowercase SHA-256 of the exact decoded template user-data bytes |
| `readiness` | `name`, positive numeric string `version`, `content_sha256` of canonical JSON |
| `roles` | Exactly `instance` and `operator`, each with distinct scoped `arn`, `trust_sha256`, `policy_name`, `policy_sha256` |
| `images` | Exactly `agent`, with the image fields below |

Image fields are `ami_id`, `architecture="x86_64"`, `ubuntu_release="24.04"`,
`owner_account="099720109477"`, exact Canonical `name`
(`ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-SERIAL`),
`root_device_name="/dev/sda1"`, `launch_template_id` and a positive signed-64-bit
numeric `launch_template_version` string. No `$Latest`, `$Default` or AMI aliases.
The standard Ubuntu LTS AMI is selected explicitly in the setup guide and its
regional ID and provenance are exported. All hashes have 64 lowercase hex digits.

The export is trusted local configuration, not a signed attestation. Doctor
verifies actual resource scope, configured routing/DNS/egress and zero ingress,
image provenance/state/architecture/root size, instance-type architecture,
template security settings and user-data digest, IAM role/profile membership,
trust/policy digests with no extra policies, and exact readiness document version
and content. IAM/document hashes use decoded JSON with sorted keys and compact
encoding equivalent to OpenTofu `jsonencode`; JSON formatting alone is ignored.
Policy array shapes remain significant. Any unexplained service normalization
must be investigated rather than bypassed. Read [IAM limitations](iam.md) and
[setup](setup.md) before treating these checks as a live acceptance gate.

Ordinary CLI commands read only this JSON export, never OpenTofu state or an
infrastructure apply. After an intentional foundation change, review, apply and
re-export. Invalid manifest scope/schema prevents doctor foundation calls and new allocation;
scoped inventory, cleanup and dispatched-request reconciliation remain available. Independent
STS and local prerequisite checks can still run. STS identity must succeed before
resource validation. Timeout/cancellation retains exit 4; resource failures use
exit 1 and allowlisted diagnostics, never raw SDK errors. Foundation check names
are `foundation_network`, `foundation_image`, `foundation_template`,
`foundation_iam`, `foundation_readiness` (and `foundation_identity` when the
resource client's identity cannot be verified). Pass code is `foundation_verified`,
failure code `foundation_drift`, timeout code `foundation_timeout`.

Inventory/mutations scope by expected account, region, deployment and
stable owner. STS identity verification must precede mutations. Required creation
tags include `ManagedBy=devbox`, `Deployment`, `Owner`, `Profile`, `Name`,
`RequestId`, and `CreatedAt`; tags do not replace corresponding IAM restrictions.

## Command results

`--json` produces exactly one JSON object on stdout, followed by a newline,
including on usage/configuration failures. All result envelopes have
`schema_version`, `command`, `ok`, and `exit_code`. Doctor and usage failures
include `checks`, an array with `name`, `status` (`pass`, `fail`, `skip`),
`code`, and `message`. Usage errors use an empty command rather than echoing an
untrusted command argument. `version` adds `version`; `--help` adds `help`.
Human messages may evolve; automation should use schema version, status and code.

```json
{"schema_version":1,"command":"doctor","ok":false,"exit_code":1,"checks":[{"name":"manifest","status":"fail","code":"manifest_unavailable","message":"deployment manifest missing; follow docs/setup.md to provision the OpenTofu foundation and export deployment.json beside the config, or set manifest to its path"}]}
```

This excerpt shows one check; real doctor results also contain the other checks.
Text mode renders the same command results on stdout. Diagnostic summaries go
to stderr. Credentials, SDK/provider error text, parser source excerpts, arbitrary
flag values and session tokens must never be included in either stream.

| Exit | Meaning |
| --- | --- |
| 0 | Command completed; all implemented doctor checks passed |
| 1 | Prerequisite, authentication/account, deployment manifest, service or output failure |
| 2 | Usage, user configuration or workload profile error |
| 3 | Reserved: completed remote command reported failure |
| 4 | Check timeout/cancellation; reserved for future readiness timeouts too |
| 5 | Reserved: partial batch completion |

When doctor has multiple failures, config/profile errors take priority over
timeouts, which take priority over other prerequisite failures. A local probe
that times out is reported as `probe_timeout` (4); probes not started before
the overall deadline/cancellation are `skip` with `check_canceled` (4), not
installation failures. Other local execution failures are prerequisites (1).

The agreed first-release access mode is real SSH over SSM, including editor and
file-transfer support. The foundation provides remote sshd and a `devbox` user; #9 must add SSH
authentication, host-key verification, and a proxy interface that OpenSSH-based tools can use
without opening inbound ports. Doctor probes the local `ssh` client and Session Manager plugin and verifies
foundation settings; runtime/key/editor checks belong to #9.

No interactive session command exists in this slice. Session bytes must have a
separate stream lifecycle from structured results; #9 must settle and document
whether interactive commands reject `--json` or provide a distinct control
channel before implementing them. Never mix terminal bytes with a JSON object.

## Instance lifecycle and request recovery (#8)

`up agent --on-demand --name NAME` requests exactly one On-Demand instance. The
explicit flag is required even for a custom On-Demand profile. Spot is rejected;
there is no type or market fallback. The first profile instance type is selected.
The manifest's exact AMI and numeric launch-template version are used; profile
disk size overrides the template's root size with encrypted gp3 and deletion on
termination. Existing deployed-resource validation runs before allocation.
Instance and volume creation receive the seven required dynamic tags. ENIs use
the template network settings; the operator policy does not permit dynamic ENI tags.
EC2's immutable `aws:ec2launchtemplate:id` and `aws:ec2launchtemplate:version` tags
provide template identity in inventory ([AWS documentation](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/launch-instances-from-launch-template.html)).

`ls` includes all currently visible managed instances, including recently terminated
ones. `down NAME_OR_ID` selects one nonterminated instance; multiple live name
matches fail with their IDs. If only terminated names match, it reports those
observations without mutation. Names cannot look like EC2 instance IDs. Creation
performs name checks before and after launch, but these are best effort under EC2
eventual consistency. Concurrent new requests may allocate distinct instances with
the same name, and may not detect the collision until a later inventory call.
Names are not globally unique locks. Use explicit IDs for collision cleanup.

Every lifecycle resource client shares the configuration/credentials used for STS
account verification. Inventory uses explicit region and managed/deployment/owner
filters, then validates reservation account and instance tags. ID lookup validates
the same scope. Teardown revalidates the ID immediately before mutation; IAM scope
conditions enforce the resource tags at termination. Inventory, teardown, and
reconciliation of a dispatched receipt require only valid user scope and identity:
missing or broken manifest/profile/receipt files never gate `ls` or `down`.

Request receipts live at `$XDG_STATE_HOME/devbox/requests/REQUEST_ID.json`, defaulting
to `~/.local/state/devbox/requests`. XDG_STATE_HOME must be absolute. A schema-v1
receipt contains a random 32-hex request ID, identical stable EC2 client token,
original UTC creation timestamp, complete effective launch parameters and template
pins, state, and any observed instance IDs. No credentials or inventory cache is
stored. Files are 0600 and newly created directories 0700. Atomic replacement and
file/directory fsync precede mutation. A separate persistent `.lock` file uses
Linux advisory locking across processes; keep the state on a local filesystem
supporting fsync and flock. Do not edit receipts, remove lock files while commands
run, or restore stale copies over newer receipts. Receipts are trusted local replay
records, not an authorization boundary or authoritative inventory.

Before the first mutation, stderr prints the saved request ID, receipt location,
and resume command. `--json` stdout remains one result object. After process exit:

```sh
devbox up --resume REQUEST_ID --aws-profile devbox-operator --timeout 5m --json
```

Use the original scope/configuration; AWS credentials may be refreshed. Resume
rejects profile/name/market flags. A `prepared` receipt compares the current
profile/manifest with the entire saved launch specification, verifies deployed
resources and reconciles the request ID before it can dispatch. Changed parameters
are rejected. The state becomes `dispatched` durably **before** RunInstances; SDK
retries within this initial call use the same client token and unchanged input.
An `observed` receipt records instance IDs. A dispatched/observed resume reconciles
scoped RequestId and client token without loading the current manifest/profile,
changing parameters, or issuing RunInstances again. Terminated request matches
are returned as already terminated; they are never relaunched.

Reconciliation makes at most five inventory attempts with 1/2/4/8 second delays,
within the command deadline. If AWS remains invisible or unavailable, the outcome
is unresolved, and the same resume command is safe to repeat. It can remain
unresolved permanently, including a crash between the durable dispatch marker and
actual send, or a terminated instance disappearing from inventory. There is no
promise of forward progress and no reset/force-relaunch option. A fresh `up` always
creates an independent request, even with the same name: it is **not** a safe retry
of an uncertain launch. Losing receipts does not prevent `ls` or `down`. This policy
avoids relying on an indefinite token-retention guarantee; AWS describes
[idempotent requests](https://docs.aws.amazon.com/ec2/latest/devguide/ec2-api-idempotency.html)
and [eventual consistency](https://docs.aws.amazon.com/ec2/latest/devguide/eventual-consistency.html).

Lifecycle JSON adds `code`, `message`, `status`, `instances`, and where applicable
`request_id`/`receipt_path` to the common envelope. Errors retain all known recovery
IDs. Instance records include actual image/type/market, template pins, original
creation tags, EC2 state, `ssm`, `bootstrap`, `readiness`, and EBS mappings with root
and delete-on-termination flags. SSM/bootstrap/readiness are `not_observed`: an
allocation success does not claim a ready shell. Fields missing in AWS remain
empty/unknown rather than inferred from the current profile/manifest.

Teardown bounds termination and per-volume observation to 30 polls each, with
backoff capped at 8 seconds and the shared deadline (default 20s; maximum 5m).
Stopped/stopping instances can be terminated; shutting-down instances are observed
without another mutation. Lost termination responses still enter observation.
Only EC2's explicit terminated state establishes `terminated`/`already_terminated`.
`no_managed_match` (exit 0) means no matching inventory, with no verified termination.
Root deletion is separate: `deleted` requires a captured exact volume ID and EC2
`deleted` or `InvalidVolume.NotFound`; `retained` means deletion-on-termination was
false; `unavailable` means root mapping evidence is missing; `not_observed` means
no deletion was observed yet. Retained/unavailable evidence can accompany a
successful EC2 termination. A timeout is exit 4 and keeps IDs. Service failures,
conflicts and unresolved outcomes use exit 1; usage/config/profile errors use 2.
Receipt paths/IDs are also preserved when a later receipt save fails.

Recognized launch rejections are preserved as the optional schema-v1 receipt field
`launch_error_code` (an allowlisted EC2 code, never the service's raw message).
A dispatched request still reconciles first and never resubmits. When no matching
instance appears within the bound, `PendingVerification` produces
`launch_pending_verification` with guidance to await the AWS verification email
or contact AWS Support. The message describes the **original** response, not a
fresh account-status check. Other previously recognized rejection codes retain
`launch_rejected`; unknown/transport failures remain `outcome_unresolved`.
Observation of an instance clears the historical error. Older receipts without
this field remain readable; no rejected receipt is reset to prepared.
