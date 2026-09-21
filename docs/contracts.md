# CLI contracts

## Build version

`devbox version` prints `devbox VERSION`; `devbox --json version` retains the
v1 result envelope with the same `version` string. Release builds inject this
value at link time. Direct development builds default to `0.1.0-dev`; Arch Git
packages report their revision-based package version. The version command
requires no user configuration, AWS identity or network access.

## Expiry integration (#43)

[The approved expiry contract](plans/04-expiry-contract.md) governs new launches.
Config stays v1 with optional `default_ttl`; `up --ttl` wins over config and the
2h default, up to 168h. One immutable UTC nanosecond window is recorded before
allocation. Launch plan v2 appends `expires_at,omitempty`; receipt v3 contains
plan v2. Exact historic receipt-v2/plan-v1 pairs remain readable with unchanged
bytes, hashes, tokens, permanent v1 attempt envelopes and `launches/v2/` keys.
Legacy single-worker receipt v1 also remains readable. None can newly allocate
without a valid future expiry and the v6 foundation capability.

All fresh Spot/On-Demand and single/batch allocations use instant Fleet with
identical instance/volume/Fleet creation tags. Dispatch checks expiry before
preparation, before the permanent claim and immediately before send; automatic
SDK mutation retries are disabled. A claimed-but-unsent request remains claimed.
Resume observes only. Missing-capacity retry retains the original window and
counts historic fulfillment, even after worker termination.

Lifecycle envelope versions remain unchanged. Public `ls`/worker projections
add required nullable `expires_at` and `expiry_status`; batch output adds nullable
`ttl` and `expires_at`. Permanent response workers only add omitted-zero fields,
so old response-v1 bytes stay stable. Raw malformed expiry is never printed.
Expiry diagnostics are separate from strict scope-tag checks and explicit down.

The v6 manifest uses all existing v5 fields/pins, plus top-level
`cleanup` (decoded separately from the launch reader). Top-level `roles` remains exactly
instance/operator. #46/#47 define and validate the cleanup/evidence descriptors;
the foundation exports v6 with the required creation-tag IAM authorization. Existing
launch pin/IAM hash verification applies to v6. Inventory/manual down/saved logs
do not require cleanup health. A manifest alone proves no live scheduled health;
see [the actual expiry acceptance](acceptance/04-expiry.md) and `doctor` for
deployment observations. Literal live Scheduler retry exhaustion remains #58.


The [Spot batch contract](#spot-batch-contract-28) adds profile v2, deployment
v5 and receipt v2 while retaining config/result-storage v1 and legacy receipt
recovery. Group launch, discovery and shared recovery are integrated in #32.
The legacy single-worker contracts below describe historic v4 launches and
v1 receipts; fresh RunInstances allocation is retired by #43. Scoped plural/group teardown is integrated in #33 and works with
both old names and generated batch names or explicit instance IDs.

## User configuration and workload profile

`examples/config.toml` is the complete user schema. `schema_version = 1`,
`expected_account`, `region`, `deployment`, and `owner` are required.
The expected account is a quoted 12-digit string. Deployment and owner are
1–63 ASCII letters, digits, underscores or hyphens, beginning with a letter or
digit. A region must have AWS region syntax; availability and partition-specific
resource checks belong to the foundation slice. `aws_profile`, `manifest`,
`profile_file`, and `ssh_identity_file` are optional; precedence is documented in
[configuration](configuration.md#configuration-precedence). Access requires the
local identity and matching public-key file.
Unknown TOML fields, invalid types, malformed TOML and missing/unsupported
schema versions are rejected. This includes credential fields.

The embedded [agent profile](../profiles/agent.toml) is a separate versioned
document. An optional user profile_file uses the same contract:

| Field | Legacy version 1 contract |
| --- | --- |
| `schema_version` | Integer `1` |
| `name` | `agent`; the only workload name in this slice |
| `market` | `spot` or `on-demand`; bundled agent defaults to **spot** |
| `instance_types` | Nonempty array of distinct EC2 type names; hardware compatibility checked later |
| `image` | Manifest image key, with the same character/length rules as deployment |
| `disk_gb` | Integer 8–16384; actual image size and volume constraints checked later |

The v6 launch path defaults to the profile's Spot market. On-Demand always
requires explicit `--on-demand`, including an On-Demand profile. Manifest v4/v5 permits observation and cleanup; new allocation requires v6. `--on-demand` is a launch
override, not a doctor option. There is no automatic market fallback.

## Deployment manifest

The current foundation exports a non-secret schema-v6 JSON object using
`tofu output -json deployment_manifest`. Schemas 1–3 are deliberately rejected:
apply the runner/storage foundation, re-export and replace old workers for new
execution support. Unknown fields and trailing JSON are
rejected. User config and command-result storage stay at version 1. Legacy
manifests v4/v5 remain accepted for applicable non-launch operations. The table
below describes the legacy v4 base; the [v5 batch additions](#spot-batch-contract-28)
and [v6 expiry requirements](#expiry-integration-43) extend it. All new launches
require v6, including explicit On-Demand and count one.

| Manifest field | Contract |
| --- | --- |
| `schema_version` | Integer 4 |
| `account`, `region`, `deployment`, `owner` | Must equal effective config; foundation currently Ohio only, labels at most 23 characters |
| `vpc_id`, `subnet_ids`, `security_group_id` | Exact EC2 IDs, exactly one subnet |
| `route_table_id`, `internet_gateway_id` | Exact EC2 IDs for explicit public route/association |
| `instance_profile_arn` | Exact commercial-AWS IAM profile ARN in expected account |
| `development_user` | `devbox` |
| `ssh_public_key` | Canonical dedicated Ed25519 public key (type and base64 only) |
| `bootstrap_sha256` | Lowercase SHA-256 of the exact decoded template user-data bytes |
| `readiness` | `name`, positive numeric string `version`, `content_sha256` of canonical JSON |
| `execution` | Separate `name`, positive numeric `version`, `content_sha256`, `step="execute"`, `runner_sha256`, `minimum_agent_version="3.3.2746.0"` |
| `results` | `schema_version=1`, exact `bucket`, `expected_bucket_owner`, `region`, scope-derived `prefix`, `retention_days` (2–365), and canonical bucket `policy_sha256` |
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
`foundation_iam`, `foundation_readiness`, `foundation_results`,
`foundation_execution` (and `foundation_identity` when the
resource client's identity cannot be verified). Pass code is `foundation_verified`,
failure code `foundation_drift`, timeout code `foundation_timeout`.

Result checks bind every bucket request to its expected owner and verify region,
scope tags, policy digest, public-access blocks, bucket-owner-enforced ownership,
SSE-S3 encryption, never-enabled versioning and retention/abort rules. Execution
checks verify the exact document and nonempty content-addressed artifact with
matching checksum metadata. Bootstrap verifies the actual downloaded bytes;
object metadata alone is not a byte attestation. These read-only checks do not
prove effective object-write/read authorization or live execution.

`LoadResultManifest` validates only a v4 descriptor's scope and `results`. A saved
manifest (or subset containing `schema_version`, `account`, `region`, `deployment`,
`owner`, `results`) remains usable for completed recovery after launch/SSH resources
are removed. It never opens profile/key files. The storage-only subset cannot
authorize a new launch. Retain its original bucket/prefix/retention policy for
the promised result lifetime when upgrading or tearing down a foundation.

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
| 3 | No special meaning; remote commands may return 3 like any other remote exit |
| 4 | Check timeout/cancellation; reserved for future readiness timeouts too |
| 5 | Reserved: partial batch completion |

These codes describe local failures. SSH preserves its remote/transport exit
status as described below. The selected exec contract also preserves completed
workload exits, including 1, 2, 3, 4 and 255; use its structured `outcome` to
distinguish these numeric collisions from local failures.

When doctor has multiple failures, config/profile errors take priority over
timeouts, which take priority over other prerequisite failures. A local probe
that times out is reported as `probe_timeout` (4); probes not started before
the overall deadline/cancellation are `skip` with `check_canceled` (4), not
installation failures. Other local execution failures are prerequisites (1).

The first-release access mode is real SSH over SSM as the `devbox` user.
`ssh` and `proxy` reject `--json` before local probes or AWS calls. `ssh-config`
provides a separate noninteractive configuration result; see the access contract
below. Native Session Manager shells do not satisfy the confirmed editor and
file-transfer scope, superseding the original proposal in issues #1/#9.

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
missing or broken manifest/profile/receipt files never block inventory discovery
or `down`. `ls` retains inventory but returns partial observation failure when
readiness metadata is unavailable. A resumed allocation is durably reconciled
before readiness metadata is loaded; metadata errors never authorize a new launch.

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
and delete-on-termination flags. Up waits for separate readiness observations;
ls attempts observations within its shared deadline. Down reports these fields
as `not_observed` because cleanup does not probe readiness. Fields missing in AWS remain
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


## Readiness and SSH access (#9)

User config v1 adds optional `ssh_identity_file`, resolved beside the TOML file
when relative. Manifest v4 requires a canonical Ed25519 `ssh_public_key` without
comment/newline; it contains no private-key path or secret. The rendered bootstrap digest now includes the public
key. Receipt schema v1 is unchanged: the numeric template and bootstrap digest
already bind this launch setting. Dispatched old receipts remain reconcilable;
new launches/access require a current manifest. No current launch profile is
loaded for ls, dispatched reconciliation, or access.

| Field | Observation contract |
| --- | --- |
| `ec2_state` | Actual EC2 state, independent of SSM and bootstrap |
| `ssm` | `online`, `offline`, `not_registered`, `unknown` |
| `bootstrap` | `pending`, `complete`, `failed`, `unknown` |
| `readiness` | `ready` only when running + online + complete; otherwise `pending`, `failed`, `not_ready`, or `unknown` |
| `probe_command_id` | Most recently dispatched, known fixed probe command ID |
| `observation_code` | Optional sanitized reason for unavailable observation |

The parameterless, version/hash-pinned SSM document returns output schema 1 with
bootstrap status and, only on completion, the Ed25519 public host key. A failure
marker wins if both markers exist. A denied, unsuccessful, malformed or mismatched
invocation means **unknown bootstrap**, not failed bootstrap. In-flight commands
are polled by their exact ID; InvocationDoesNotExist is retried under the deadline.
This command has no arbitrary script parameters or output-storage destinations.
Readiness probes contain no user command. General execution uses the separate
document and protocol below.

Default up and access setup timeout is 5m; ls/doctor/down default to 20s.
`--timeout` accepts any positive duration up to 5m, including milliseconds for
controlled failure tests. Up includes launch/reconciliation in this same overall
budget. Ls uses at most four concurrent observations within one deadline and
retains every discovered record. A valid observation of pending/offline/failed
states is a successful inventory (exit 0); probe/metadata failures return exit 1,
and deadline/cancellation returns 4. Up exits 1 immediately for failed bootstrap.
Progress goes to stderr and --json remains one final result on stdout. Failed
waits and connections retain workers; **manual cleanup is required until TTL ships**.

`ssh NAME_OR_ID` validates Linux, OpenSSH, Session Manager plugin >=1.2.764.0,
plugin logging disabled, and a local identity/.pub matching the exported public
key. Private-key files must be owned regular files with mode 0600; encrypted keys
must be loaded in ssh-agent before invocation. Public-key authentication uses
BatchMode/IdentitiesOnly and does not forward the agent. No private key is generated
by the CLI or OpenTofu. Rotation affects newly launched workers only.

Name/ID resolution verifies account, region and all managed scope tags; ambiguous
live names fail with IDs. EC2 scope is revalidated before fixed SendCommand and
AWS-StartSSHSession on port 22. Initial SSH requires complete readiness; failed
bootstrap does not have an unready-shell bypass. Use probe IDs to investigate,
then down by ID rather than repeatedly retrying a confirmed failed bootstrap.

Host trust derives from the authenticated, exact SSM invocation. The dedicated
known_hosts/config directory is private under `$XDG_STATE_HOME/devbox/ssh`, default
`~/.local/state/devbox/ssh`. A scoped immutable host alias, strict Ed25519 host
checking, no global known_hosts and disabled automatic host-key updates prevent
unrelated trust entries from matching. Updates are locked across processes and
atomic. A changed key fails without overwrite: inspect the instance and freshly
verified SSM probe, then deliberately remove only that instance's known_hosts
file and regenerate its config. This trusts AWS/SSM and target root, not a separate
host identity authority. Reinstall/loss of local trust bootstraps trust from SSM
again. No insecure host-checking fallback is offered.

`ssh-config NAME_OR_ID` emits an OpenSSH stanza and saves its private config file.
`--json` returns the common envelope plus `ssh_config`, `ssh_config_path` and
`ssh_host`. Use the file with ssh/scp/sftp -F or an editor's SSH-config setting.
It references this devbox executable and exact config/profile/region and instance
ID; regenerate after moving the executable/config. The proxy refreshes readiness
and validates existing host trust for every new tunnel. Generated paths support
spaces, quotes and literal percent signs; control characters and dollar signs in
SSH file paths are rejected. Using `-F` isolates these settings from user/system
SSH configuration. Do not override the supplied trust/authentication settings.

The CLI first establishes a noninteractive OpenSSH control master within the
setup deadline, then opens an interactive client over that authenticated socket.
The shell's lifetime is independent of --timeout. It inherits terminal streams;
Ctrl-C interrupts the remote foreground command, resize is forwarded by OpenSSH,
and exit/Ctrl-D closes the shell. The supervisor restores terminal ownership/mode
and cleans ordinary subprocess descendants on failure/cancellation. Setup failures
use existing CLI exits (1/2/4); after handoff OpenSSH's exit status is preserved
(remote shell status or 255 for transport/authentication failures). No remote
command arguments are accepted by devbox ssh; external OpenSSH tools use the proxy.

Plugin session-response tokens go through a child-only environment variable,
never argv or diagnostics. Upstream plugin logging is independent of SDK logging;
access refuses a seelog configuration that permits logs. Keep it disabled/stable
for the session. Startup output is bounded and suppressed until SSH identification,
then the transport is copied as opaque bytes. Plugin stderr is discarded; remote
interactive stderr remains terminal data. On closure or startup failure, the proxy
attempts TerminateSession with a separate 5s cleanup budget; failures report a
session ID and retry command. Hard crashes/disconnection can leave a server session
until AWS detects closure/timeout. SSM does not record the contents of SSH tunnels.

## Selected exec and durable result protocol (#16)

This section specifies MVP 2 for implementation in #17–#20. The #16 payload codec
and local publisher prototype validate selected invariants. #17 adds the pinned
runner and result-storage foundation. #18 adds the `exec` dispatch path; #19 adds
bounded SSM/durable observation and process-level detachment semantics. The
user-facing `logs` recovery and verified export interface is implemented in #20.
The user selected detach on Ctrl-C and 30-day retention from submission on
September 13, 2026. Implementation and live gates are tracked in
[the ordered plan](plans/16-exec-contract.md).

### Invocation and literal arguments

```sh
devbox exec smoke -- /usr/bin/printf '%s\n' '' 'two words' '$(id)' '--json'
devbox exec smoke --cwd project --exec-timeout 10m -- sh -c 'make check'
devbox --json exec smoke --wait-timeout 2m -- /usr/bin/false
devbox logs dc1-0123456789abcdef0123456789abcdef --json
devbox logs dc1-0123456789abcdef0123456789abcdef --stdout-file ./stdout.bin --stderr-file ./stderr.bin
```

`exec` requires one lifecycle-managed name or instance ID and a nonempty command
after the first `--`. That separator ends **all** local interpretation, including
the initial `--json` error-format scan, help, timeout and other flags. Remote
`--json`, `--help`, `--timeout` and a second `--` are ordinary arguments. An empty
executable is invalid; empty later arguments are valid. Only `exec` accepts the
remote-argument separator. Local option errors never echo their values.

Execution is noninteractive as the existing `devbox` user, with stdin `/dev/null`
and separate byte streams for stdout/stderr. Default cwd is `/home/devbox`.
`--cwd PATH` accepts an absolute path or a path relative to `/home/devbox`, never
to the local checkout or an earlier command. Resolve `.`/`..` lexically and send
the resulting absolute path; symlinks follow ordinary remote filesystem behavior.
There is no automatic checkout, directory creation, tilde/environment expansion,
login shell, shell startup file, or forwarded local environment. The explicit
environment is `HOME=/home/devbox`, `USER=devbox`, `LOGNAME=devbox`, `LANG=C.UTF-8`,
`PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin`. Workload umask
is 022. Resolve executables against this PATH, or relative to cwd if argv[0]
contains `/`. Missing executable returns workload exit 127, permission/format
failure 126, and inaccessible/missing cwd `runner_setup_failed` with no remote exit.
Use `-- sh -c '...'` when shell evaluation is wanted.

The payload is a schema-1 JSON object with exactly `schema_version`, `argv`,
`cwd`, `exec_timeout_seconds`, encoded using padded standard base64. Validate
UTF-8 **before** JSON encoding; invalid UTF-8 and NUL in every argument/cwd are
rejected locally rather than replaced. Valid Unicode, empty arguments, whitespace,
newlines, quotes, metacharacters and leading dashes round-trip without evaluation.
At most 256 argv entries and 32,768 encoded payload bytes are accepted. JSON
must contain one object, supported schema, known fields and valid required values.
The remote decoder applies the same limits before allocating/executing.

The dedicated schema-2.2 SSM document takes three String parameters, `requestId`,
`payload` and `stepTimeoutSeconds`, with strict allowed patterns and length
limits. Only `requestId` and `payload` use `interpolationType: ENV_VAR`.
The decimal `stepTimeoutSeconds` uses ordinary document substitution solely for
the step's `timeoutSeconds` property, never shell source; it must equal the
payload execution seconds plus 180. Do not mark this numeric property ENV_VAR:
the agent substitutes an environment reference that its timeout parser cannot
evaluate. See [agent document substitution](https://github.com/aws/amazon-ssm-agent/blob/mainline/agent/framework/docparser/docparser.go)
and [timeout parsing](https://github.com/aws/amazon-ssm-agent/blob/mainline/agent/plugins/pluginutil/pluginutil.go).
Its one `execute` step runs a fixed root-owned Go runner;
the runner reads `SSM_requestId` and `SSM_payload` as data and invokes argv
directly with `execve` semantics. No payload is substituted into shell source.
Require SSM Agent >=3.3.2746.0; bootstrap must reject older agents instead of
falling back to interpolation. The agent supplies `SSM_COMMAND_ID` to the runner.
See [AWS ENV_VAR support](https://docs.aws.amazon.com/systems-manager/latest/userguide/documents.html)
and [the agent runscript implementation](https://github.com/aws/amazon-ssm-agent/blob/mainline/agent/plugins/runscript/runscript.go).

### Four independent clocks and interruption

| Option/budget | Default | Accepted bounds and ownership |
| --- | --- | --- |
| `--timeout` | 5m for exec; 20s for logs | Positive duration through 5m; local configuration, identity, readiness, validation and submission, or one logs retrieval |
| `--wait-timeout` | 1h | Positive duration through 25h; local observation after acknowledged submission, independent of setup |
| `--delivery-timeout` | 5m | Whole seconds, 30s–1h; sent as SSM `TimeoutSeconds` |
| `--exec-timeout` | 1h | Whole seconds, 1s–24h; workload process lifetime enforced by the remote runner |
| Remote finalization | 120s | Fixed independent deadline for durable outcome/output/result publication |
| Remote stop grace | 5s | TERM then KILL for ordinary workload process-group descendants |
| SSM step hard deadline | exec timeout + 180s | Covers preparation, workload, stop grace and publication; last-resort wrapper deadline |

Preparation on the runner has a 30s budget and must not start a workload after
that budget expires. Publication has bounded SDK retry attempts within its own
deadline. No local clock establishes a remote timeout. SSM reports delivery and
step execution timeout separately; an absent invocation can also reflect eventual
visibility. The runner's own persisted timeout identifies a workload execution
timeout, while SSM timing out the wrapper does not establish the workload exit.
See [SSM status semantics](https://docs.aws.amazon.com/systems-manager/latest/userguide/monitor-commands.html)
and [SendCommand parameters](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_SendCommand.html).

**Ctrl-C detaches.** SIGINT/SIGTERM cancel local setup or observation, return
`interrupted` (4), and never call CancelCommand. A known completion observed
before the interrupt wins; otherwise preserve its recovery ID and last remote
observation. The remote job continues within its execution timeout. Killing the
local client or losing its network cannot confirm remote termination. No cancel
CLI is included in MVP 2. An externally requested SSM cancellation remains
`cancelling` until SSM reports `Cancelled`; that reports the wrapper's state, not
proof that every descendant stopped. If a durable workload result exists it is
retained alongside that observation. AWS explicitly describes cancellation as
[best effort](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_CancelCommand.html).

The runner manages a separate workload process group and captures through pipes
that only the runner writes to private files. On timeout, capture failure, or
direct-child exit with surviving descendants, it stops that group, bounds waiting
for pipe EOF and freezes capture before hashing. Deliberately detached processes
or privileged workloads can escape the group; bounded drain expiry means
`capture_incomplete`, never complete output. This is a development runner, not
process isolation. Teardown is the way to remove the entire disposable worker.
Each captured stream is capped at 1 GiB; overflow/storage exhaustion stops the
workload and reports incomplete capture, including the captured byte count.

### Public ID, cloud records and publisher

Public `COMMAND_ID` is `dc1-` plus 32 lowercase random hex digits (128 random
bits), generated before any mutation. It is distinct from SSM's UUID and is
never an execution replay token. Validate this grammar before cloud retrieval.
Trusted scope consists of account, region, deployment and owner from the user's
config and verified STS identity. A trusted manifest supplies one exact result
bucket, expected bucket owner, region, retention setting and scope prefix:

```text
results/v1/ACCOUNT/REGION/DEPLOYMENT/OWNER/COMMAND_ID/
  request.json     operator, create before SendCommand
  acknowledgement.json  operator, optional best-effort copy of acknowledged SSM ID
  started.json     remote runner, conditional claim before workload launch
  outcome.json     remote runner, workload/capture outcome before output uploads
  stdout          remote runner, exact captured bytes, including zero-length object
  stderr          remote runner, exact captured bytes, including zero-length object
  result.json     remote runner, final publication record written last
```

All records have `schema_version=1`, `kind`, `command_id`, `scope` (the four
fields above), `instance_id`, `document` (`name`, numeric `version`,
`content_sha256`, `step="execute"`), `runner_sha256`, and `payload_sha256`
(SHA-256 of the decoded JSON bytes). `request.json` also has `retention_days`.
Neither request nor diagnostic records repeat argv, environment or credentials.
Acknowledgement and runner records add `ssm_command_id`. Runner records copy
`submitted_at`, the original request object's server `LastModified` timestamp,
and `expires_at = submitted_at + retention_days * 24h`. Timestamps are UTC
RFC3339. `started.json` additionally has `started_at`. Outcome/result additionally
have `finished_at`, `workload`, `capture`, and `streams`. Result additionally
has `publication` and `finalized_at`. Records are capped at 16 KiB and must be
strictly decoded; unsupported schema, missing required fields, invalid values,
trailing data or identity disagreement fail closed.

`workload` records `status` (`exited`, `signaled`, `execution_timeout`,
`runner_setup_failed`, `capture_failed`, `unknown`), nullable `exit_code`, and
nullable numeric `signal`. Only `exited` has an ordinary 0–255 exit; a signal
has a separately recorded signal and derived CLI status `128 + signal`.
`capture` is `complete` or `incomplete`. Each `streams.stdout/stderr` records
the exact derived object `key`, captured `bytes`, lowercase `sha256` and
`upload` (`pending`, `complete`, `failed`). `publication` is `complete` only
if workload status is known, capture complete and both uploads acknowledged;
otherwise `incomplete`. An uploaded partial capture stays incomplete. Empty
streams require a real zero-byte object and the SHA-256 of empty bytes.
Outcome upload flags start as pending. A result describes the observed final
upload flags even on failure if the record itself can be published.

Every record/object is immutable and uses conditional creation. On uncertain
data/metadata PUT, a bounded GET/HEAD plus exact identity, length and checksum
reconciliation may confirm success; it never authorizes running a workload.
`started.json` is special: only the caller of a definitely successful initial
conditional PUT may launch. A conflict or lost claim response aborts execution,
even if an identical claim can later be read. This prevents the conforming runner
from starting twice under one public ID without claiming exactly-once SSM service
delivery. An existing contradictory record is corruption, never overwritten.
See [S3 conditional writes](https://docs.aws.amazon.com/AmazonS3/latest/userguide/conditional-writes.html).

The operator prepares the request in S3 before calling SendCommand. A failed or
unreconciled request write prevents dispatch. Print `prepared command_id=...`
to stderr before sending; after a valid acknowledgement, immediately print
`submitted command_id=... ssm_command_id=...` **before any other network write**.
Any later optional acknowledgement write failure retains the ID. SendCommand
targets exactly one revalidated instance and pins document version and SHA-256.
Set its SDK retry max attempts to **1**, independently of normal read retries.
An ambiguous response is `submission_unknown`; never automatically send again,
and never offer exec resume as a retry. The public ID can still find a runner's
eventual records. If the client dies before sending, a prepared request can stay
unknown forever. AWS SendCommand offers no EC2-style ClientToken guarantee.

On the worker, the runner validates the payload hash against the cloud request,
scope/instance against pinned local scope and IMDSv2 identity, and all document,
step and runner pins against its installed configuration. It requires a valid
`SSM_COMMAND_ID`, reads the request's server retention timestamp, rejects expired
requests, and writes the start claim before executing as `devbox`. The root
runner retains its own restricted instance credentials and owns private capture
files; the workload receives only the documented environment. After process
termination/capture freeze, it writes outcome, uploads both streams using S3
SDK single-object PUTs, then writes the result. The fixed 1 GiB stream limit is
below S3's single-PUT limit; streaming file reads bound memory. This writer is on
the worker and has no dependency on the observing CLI. Wrapper exit/SSM response
code describes runner publication success/failure and never substitutes for the
workload exit. Worker loss before finalization can leave honest unknown/partial
data; no separate scheduled publisher is proposed.

The complete recovery path with an empty local state directory is:

```mermaid
sequenceDiagram
    participant C as CLI
    participant S as Scoped S3 records
    participant M as SSM
    participant W as Worker runner
    C->>S: Conditional request.json under public ID
    C->>M: SendCommand once, public ID and encoded argv
    M-->>C: Acknowledged SSM ID; print public ID immediately
    Note over C: Client can disappear here
    M->>W: Pinned execute step, SSM_COMMAND_ID
    W->>S: started.json including complete mapping
    W->>W: Execute argv, capture and stop/drain
    W->>S: outcome.json, stdout, stderr, result.json
    Note over W: Worker can now be removed
    C->>S: logs public ID: result.json then exact stream keys
    S-->>C: Scope-bound status, bytes and checksums
```

`result.json` is self-contained, so completed retrieval needs neither request nor
SSM history nor an instance nor a prior fetch. Request/start/outcome provide
recovery when completion is absent. The acknowledged ID remains useful before
the client saves any mapping because its S3 prefix was reserved before dispatch
and the runner writes the SSM mapping independently.

### Outcomes and output ownership

Exec text mode emits status metadata on stdout, early IDs/progress and failures
on stderr. It emits **no workload bytes**; use logs explicitly. JSON mode emits
one newline-terminated version-1 envelope on stdout, including failures, with
early IDs/progress on stderr. Every post-dispatch outcome retains `command_id`,
any known `ssm_command_id`, and a recovery command preserving config/profile/region.
`ok` for exec means a known workload exit 0 plus established complete publication.
Output publication failures do not erase a known workload status, but may make
the CLI fail even after workload success. Numeric statuses alone are ambiguous.

Exec reads strict started/outcome/final metadata and checks all submitted identity
pins, retention and agreement between independently observed records. It trusts
the independent publisher's completed upload record; it does not download workload
streams. Explicit logs retrieval verifies the requested bytes. Intermediate
workload evidence remains available if later observation fails. A valid standalone
final record needs no successful SSM observation; contradictory metadata or an
invocation with different identity is rejected.

Metadata and SSM reads each have a 10-second call bound within the independent
local wait deadline. Polling starts at 1 second and doubles to a 5-second maximum.
Six consecutive transient failures of either backend stop observation with a
sanitized API failure. Successful observation resets that backend's counter.
Missing/eventually visible invocations and pending/delayed/cancelling states keep
waiting; no read failure triggers another dispatch. Credential-source failures
are distinct from S3/SSM access denial and retryable service/transport failures.
When a terminal wrapper state or optional SSM failure would end observation, the
CLI checks durable metadata again to catch publication during the API call.

`durable_state` is `pending`, `started`, `outcome` or `final`, with independently
validated submission/expiry/start/finish timestamps when known. Optional `ssm`
contains the last identity-verified wrapper `state`, allowlisted `status` and
`status_details`, and its raw numeric `response_code`. This observation can be
older than the final record; no extra SSM call is required after completed durable
metadata is established. Wrapper output, URLs and raw SDK/provider errors are
excluded. The raw response, including -1, never becomes a workload exit.
`submission_state` separately preserves whether submission was prepared,
acknowledged, rejected or uncertain, including when an interrupt ends local setup.

| Observation | `outcome` | CLI exit | Workload status retained |
| --- | --- | --- | --- |
| Persisted exit 0, complete output | `remote_exit` | 0 | 0 |
| Persisted exit 1 / 2 / 4 / 255, complete output | `remote_exit` | 1 / 2 / 4 / 255 | Exact code |
| Persisted signal, complete output | `remote_signal` | 128 + signal | Signal, no ordinary exit |
| Invalid argv/usage/config | `config_invalid` | 2 | Unknown; not dispatched |
| Setup/identity/API denial | `setup_failed` or `api_failed` | 1 | Preserve if already known |
| Setup deadline | `setup_timeout` | 4 | Usually not dispatched; uncertain send is submission_unknown |
| Lost SendCommand response | `submission_unknown` | 1 | Unknown until cloud records establish more |
| SSM reports delivery timeout, no known workload | `delivery_timeout` | 4 | Not a remote exit |
| Runner records execution timeout | `execution_timeout` | 4 | Timeout, signal if observed |
| SSM reports hard step timeout without outcome | `runner_timeout` | 4 | Unknown |
| Local wait deadline expires | `observation_timeout` | 4 | Last known state; command may continue |
| Ctrl-C / local SIGTERM | `interrupted` | 4 | Last known state; cancellation not requested |
| External cancellation pending | Continue waiting with `ssm.state=cancellation_pending` | Not terminal; local interrupt/deadline retains its own outcome | Preserve independently recorded evidence |
| SSM reports external cancellation completed | `cancelled` | 4 | Unknown unless independently recorded |
| Terminal wrapper without trustworthy workload record | `execution_unknown` | 1 | Unknown; response -1 is never a workload exit |
| Known workload, capture/upload/result failure | `result_incomplete` | 1 | Original exit/signal/timeout, if known |
| Mismatched record/invocation or invalid bytes/hash | `result_corrupt` | 1 | Preserve independently trusted outcome only |

Priority: validated durable workload outcome wins over an SSM wrapper result;
corruption and unavailable required result data prevent success. A recorded
execution timeout remains `execution_timeout` even if its logs are incomplete.
Otherwise incomplete persistence takes priority over the remote numeric CLI exit,
which remains available under `workload`. Local interruption/deadline when no
final result is established describes observation, not the remote state. SSM
`Success` alone never proves workload success or log completeness. SSM observation
is optional for completed logs; its unavailability does not defeat valid S3 data.

Example text (IDs abbreviated here only):

```text
# stderr, as soon as acknowledgement arrives:
submitted command_id=dc1-... ssm_command_id=...
# stdout at completion; local exit is 4, a remote failure in this example:
remote_exit command_id=dc1-... remote_exit_code=4 publication=complete
# stderr after detaching instead:
interrupted: observation stopped; the remote command may continue; recover with devbox logs dc1-...
```

JSON field example for a deliberately failing command (complete real IDs use
their validated grammars; this example omits optional SSM observation):

```json
{"schema_version":1,"command":"exec","ok":false,"exit_code":2,"outcome":"remote_exit","command_id":"dc1-0123456789abcdef0123456789abcdef","workload":{"status":"exited","exit_code":2,"signal":null},"publication":"complete","recovery_command":"devbox logs dc1-0123456789abcdef0123456789abcdef"}
```

For the matrix's other cases `outcome`, `exit_code`, `workload` and `publication`
change together: remote exit 0 uses `ok=true`; exits 1/4/255 use `ok=false` and
their exact codes; API failure uses `api_failed`/1 with last known workload;
delivery timeout uses `delivery_timeout`/4 and unknown workload; a runner-recorded
workload timeout uses `execution_timeout`/4 and timeout workload; SSM hard wrapper
timeout without an outcome uses `runner_timeout`/4 and unknown workload; detach uses `interrupted`/4
and last observation; external cancellation uses `cancelled`/4 and independently
known workload or unknown. Diagnostics use allowlisted messages and never include
raw SDK errors, payloads or credential helper output. Deliberately retrieved
workload bytes may themselves contain secrets and are not automatically redacted.

### Logs, completeness and retention

`logs COMMAND_ID` prints status and per-stream persistence metadata. `--stream
stdout|stderr` instead copies that stream's exact bytes to stdout and sends all
metadata/diagnostics to stderr. `--stdout-file PATH` and `--stderr-file PATH`
export either or both streams, using distinct, newly created files. Stream mode
cannot combine with export or JSON; file exports may combine with JSON.
JSON contains structured workload status, capture/publication/verification states,
byte counts, hashes and object/file locations, never embedded raw output. It
describes output encoding as `bytes`, with no implicit UTF-8 decoding. Successful
logs retrieval exits 0 even when the original workload failed or timed out;
invalid input exits 2, local deadline/interruption 4, and unavailable/incomplete/
corrupt results or output write failure 1. Status of the original command always
remains separate.

S3 reads use the authenticated SDK with expected bucket owner and exact keys
derived from trusted scope and ID. Reject supplied URLs, alternate buckets,
prefix traversal and record identity mismatch; never follow SSM output URLs.
Bound metadata to 16 KiB and stream output with bounded memory. Full verification
requires exact lengths, SHA-256 and EOF against a complete result; inline SSM
text, ETags and object existence alone are insufficient. Status-only logs may
report `publication=complete` from a record but `verification=not_downloaded`;
byte verification is established only by reading the selected streams. A partial
stream write can already have emitted bytes when verification fails: return
failure and diagnose it. Exports use private temporary files, verify then publish
atomically without replacing an existing destination, and remove unfinished temporary files.
Do not mark a partially downloaded file as a successful export.

The top-level `verification` describes the selected downloads: `not_downloaded`
for status-only retrieval, `verified` after all selected streams pass, and
`failed` if any selected retrieval fails. `downloads.stdout/stderr` describes
only selected streams, with `destination=stdout|file`, `bytes_written`, individual
`verification`, and `file` only for successfully published exports. The existing
`streams` fields describe both immutable objects. No selection implicitly verifies
the unselected stream. Exports are independent: if the first succeeds and the
second fails, retain the first file and report its verification separately while
the overall retrieval fails. Incomplete publication returns status without
claiming a complete stream export.

Logs uses one local deadline (default 20s, maximum 5m). It first reads the exact
`result.json`; a valid final requires no auxiliary metadata or SSM call. Otherwise
it reads outcome/start/acknowledgement/request records, reconciles all evidence
actually read, takes at most one optional exact SSM snapshot, and rereads the
final once to cover concurrent publication. Each metadata attempt is bounded by
10s inside the overall deadline, with at most two record-read attempts and 100ms
backoff for transient failure. SDK retries and the bounded exact-key access check
may make additional HTTP requests within that deadline. This is a status snapshot,
not a follow loop. Repeat `logs`
with the same ID after pending, temporary failure or authentication refresh;
use new export filenames. A complete result cannot be defeated by unavailable
SSM history. Observed contradictory metadata remains corrupt, and authoritative
workload evidence remains present when a later read or output transfer fails.

```sh
# Status-only metadata: exit 0 can accompany workload.exit_code=255.
devbox logs dc1-0123456789abcdef0123456789abcdef --json
# Both full byte streams; paths must not exist. JSON contains no workload bytes.
devbox logs dc1-0123456789abcdef0123456789abcdef --timeout 2m --stdout-file stdout.bin --stderr-file stderr.bin --json
# Only stderr bytes on stdout; all status/diagnostics go to stderr.
devbox logs dc1-0123456789abcdef0123456789abcdef --stream stderr > stderr-copy.bin
```

| Evidence | Result availability/completeness |
| --- | --- |
| Request only, or no runner yet | `pending` if live SSM confirms pending; otherwise `execution_unknown` |
| Started, still running | `pending`; output normally uploads after execution |
| Outcome known, uploads not finalized | `incomplete`; retain workload outcome |
| Complete result and verified zero-byte objects | `complete`, known-empty streams |
| Missing stream, short body, wrong checksum or extra bytes | `incomplete` or `corrupt`; never complete |
| S3 access denied | `access_denied`; no claim of absence or expiry |
| Unsupported record version | `unsupported_schema`; do not guess fields |
| Retention deadline proven by a valid record/server timestamp and passed | `expired` |
| No authoritative record, or missing expected data with no retention evidence | `missing_or_expired` |

Retain command results for **30 days from submission** by default, configurable
in foundation to an integer 2–365 days. Submission means server creation time of
the immutable request, before SendCommand; long jobs use part of their retention
window. The minimum exceeds maximum delivery + execution + finalization. The
fixed public-ID lookup requires no expiring external index. Every later record
copies that original deadline; retain the local trusted storage descriptor across
upgrades for any old deployment. During the promised interval the request and
all later-created records/objects are protected by an S3 lifecycle age at least
the selected retention. Objects may physically persist longer because their
creation is later and S3 rounds expiration up to midnight and deletes
asynchronously; this is not a promise of exact-time deletion. See
[S3 lifecycle age calculation](https://docs.aws.amazon.com/AmazonS3/latest/userguide/intro-lifecycle-rules.html).

Use a nonversioned result bucket and immutable conditional object creation;
configure lifecycle expiration for the result prefix and abort incomplete
multipart uploads after one day, even though this runner uses single PUTs.
No noncurrent versions exist in this design; enabling versioning requires an
explicit migration and noncurrent-version cleanup policy. Retention may be
increased in place, but must not be reduced while older unexpired promises exist:
retain the old bucket/policy and choose a new storage deployment for shorter
retention, or wait for old results to expire before shortening. Disabling or
shortening lifecycle is detected as drift for new dispatch. Preserving the old
storage config allows completed retrieval without launch-profile, SSH, readiness,
EC2 or SSM dependencies. Worker `down` leaves results intact. Full foundation
teardown must explicitly retain or deliberately empty this bucket; `force_destroy`
is false. Result/artifact storage and durable foundation can keep incurring charges
after worker removal.

### Failure windows and implementation boundaries

| Window/failure | Durable evidence and behavior |
| --- | --- |
| Request PUT fails/uncertain | Confirm exact request or stop without dispatch |
| Client dies before send | Prepared request may never execute; unknown, never replay |
| Send accepted, response lost | Public ID printed before send; worker can finish; one wire submission |
| Client dies immediately after acknowledgement | Request already exists; runner publishes mapping/results independently |
| Claim PUT succeeds but response lost | Abort workload; conservative unknown, no second claim/execution |
| Runner/worker lost before start | Request/ack only; SSM may explain delivery, otherwise unknown |
| Worker lost during job | Start only; no fabricated exit or complete logs |
| SSM kills runner on timeout/cancel | Preserve durable outcome if any; no final record means incomplete/unknown |
| Workload exits nonzero | Publisher still runs; upload helper exit cannot replace workload status |
| Capture limit/disk/pipe failure | Kill/drain within bounds, record incomplete capture if possible |
| One upload fails or final-record PUT fails | Preserve outcome; pending/partial output, retry reads only |
| Result claims complete but objects disagree | Corruption/incomplete, even if SSM says Success |
| Completed worker is removed, SSM history unavailable | S3 result supplies all status and object identities |
| Result later absent | Only authoritative retention evidence permits expired; otherwise missing_or_expired |

#17 adds a result bucket separate from state, SSE-S3 encryption, all public-access
blocks, bucket-owner-enforced ownership and TLS enforcement. The worker role gets
GetObject for scoped requests, runner records/streams (including uncertain-write
reconciliation) and pinned runner artifacts, and PutObject for scoped
runner records/streams; the operator gets scoped result reads and request/ack
writes, not result overwrite/delete. Restrict ListBucket to the owned prefix
where needed to distinguish missing keys. Neither role needs broad S3 resources.
Require conditional creation in the result-prefix bucket policy, so a writer
cannot accidentally overwrite a completed record by omitting If-None-Match.
SendCommand permits the exact execution and existing readiness documents and
owned instances only. GetCommandInvocation needs `Resource="*"` with the supported
region restriction; CLI validation is additional isolation, not an IAM tag
guarantee for unsupported actions. No CancelCommand permission is added for
detach. Verify details against the [SSM authorization table](https://docs.aws.amazon.com/service-authorization/latest/reference/list_ssm.html).
Development workloads have passwordless sudo and can access instance credentials;
these records are trusted development results, not tamper-proof attestations
against an operator or a compromised worker within its allowed owner prefix.

The selected runner is a separately built Linux/amd64 Go binary, stored under a
content-addressed artifact key outside the expiring result prefix. Foundation
pins the exact artifact SHA-256 and installs/verifies it during bootstrap using
the instance role and explicit provisioning download dependencies. The fixed
execution document pins its location/config; readiness remains a separate fixed
probe. #17 must ship a working runner/document interface, not placeholder success.
Manifest v4 adds `execution` (document pins, step, runner hash/minimum agent) and
`results` (schema, bucket/owner/region/prefix/retention and policy digest). Doctor
validates actual storage controls, document and IAM digests; keep general SSH
prerequisite checks separate. Apply/re-export and replace old workers to install
the new runner/agent/bootstrap; reject v3 for new exec with actionable guidance.
Inventory, down and old launch reconciliation remain available under their
existing scope-only recovery contract. Logs reads only the trusted result
descriptor and current identity for completed recovery, regardless of whether
the original template, document, instance or SSH identity still exists.

## Spot batch contract (#28)

This section is normative for #28–#34. #28 provides pure validation, parser
coverage and shared Go types; #29 verifies/provisions the foundation, #30 builds
one immutable allocation attempt, #31 implements shared recovery, #32 enables
launch/inventory/access, and #33 enables plural teardown. All these operational
forms are integrated. No successful
parser or plan test is evidence of a Spot launch or live IAM authorization.

### Configuration, profiles and migration

Config remains schema 1. Optional `max_count` defaults to **10**, accepts integers
1–100, and counts instances. An explicit zero, fractional value or overflow fails
parsing/validation. TOML overrides the built-in default; neither environment nor
CLI has a cap override. `--count` defaults to 1 and accepts decimal digits only
(leading zeros are permitted), with no sign, fraction, exponent or whitespace.
The parser enforces the hard maximum; resolved planning enforces the configured
maximum before mutation. Missing-capacity actions also check the original desired
count against the current cap; lowering a cap never blocks observation/cleanup.

The bundled profile remains Spot by default and becomes schema 2:

| Field | Version 2 contract |
| --- | --- |
| `schema_version`, `name`, `image` | `2`, `agent`, `agent` |
| `market` | `spot` or `on-demand`; every On-Demand launch requires explicit `--on-demand`, even for custom profiles |
| `instance_types` | Nonempty distinct explicit EC2 types; every selected type needs at least one permitted, compatible offering |
| `architecture` | Explicit `x86_64`; supported OS remains Canonical Ubuntu 24.04 amd64 in Ohio |
| `disk_gb`, `disk_type` | 8–16384 GiB, `gp3`; effective size must meet the exact AMI snapshot minimum |
| `encrypted`, `delete_on_termination` | Explicit `true`; roots are encrypted and disposable |
| `subnet_ids`, `availability_zones` | Optional distinct lists restricting the foundation's exact subnet/AZ mappings; omission or empty lists mean all permitted choices |

If both restriction lists are supplied, they intersect. Unknown subnets/AZs,
empty resulting choices, architecture mismatch and types with no remaining
offering fail. Never invent a cartesian product from independent type/subnet
lists. Offerings describe capabilities, not available Spot capacity.
Profile v1 still loads, normalizing architecture/disk defaults to the supported
values. V2-only TOML fields in v1 fail, including explicit empty/false values.
Upgrade such a profile's version before adding those fields.
Placement restrictions also require manifest v5; the legacy single-subnet path
rejects them rather than ignoring them. Historical v5 launches used the shared batch
allocator and recovery service; the legacy allocator cannot bypass that path.

Manifest v5 retains all v4 execution, results, IAM, image and network fields and
adds the following. V4 remains accepted for the existing single-worker path,
inventory, access, teardown, legacy recovery and completed logs. New batch plans
require v5. Merely changing `schema_version` is never a migration: #29 must apply
and re-export the actual resources and preserve the existing subnet address.

| Field | Version 5 addition |
| --- | --- |
| `subnets` | Array of `{id, availability_zone}`; every distinct `subnet_ids` entry occurs exactly once, with only one selected subnet per Ohio AZ (a Fleet API constraint) |
| `compatible_pools` | Array of `{instance_type, architecture, subnet_ids}`; distinct x86_64 types with explicitly approved subnet memberships |
| `images.agent.root_disk` | Exact template default `{size_gb, type, encrypted, delete_on_termination}` |
| `images.agent.minimum_root_disk_gb` | Positive exact AMI root snapshot size; template and effective profile size must meet it |
| `launch_ledger` | `{schema_version:1, bucket, expected_bucket_owner, region, prefix, policy_sha256}`; same bucket/owner/region/policy as results, separate permanent `launches/v2/ACCOUNT/REGION/DEPLOYMENT/OWNER/` prefix |

Completed logs validates its retained storage descriptor independently of these
new fields. No local profile, launch receipt, launch ledger or live worker is a
prerequisite for inventory/teardown or completed-log retrieval. Existing workers
without group/naming tags remain visible by their legacy `Name` and ID.

### Resolved launch plan and pinned SDK mapping

`lifecycle.LaunchPlan` schema 1 freezes scope; random 32-lowercase-hex request ID;
creation time; profile/base/group/market/original count; sorted approved
`{instance_type, subnet_id, availability_zone}` choices; exact AMI provenance and
numeric launch-template version; effective root disk; approved security group
and instance profile; bootstrap digest; readiness and execution document/runner
pins; shared ledger descriptor; and creation tags. Its SHA-256 covers canonical
Go JSON (sorted map keys and deterministic choice order). Once prepared, changes
to these inputs require a separately authorized new request, never a resume.
The constructor is pure; #29's deployed-resource verification must succeed
before any allocator can use a plan. The template must preserve IMDSv2, exactly
one new primary interface, public IPv4, approved security group, interface/root
deletion, no ingress, and the pinned SSH/runner bootstrap.

The pinned SDK is `github.com/aws/aws-sdk-go-v2/service/ec2 v1.332.0`:

- Use `CreateFleetInput.Type=instant` explicitly; the API default is `maintain`.
  Spot uses `SpotOptions.AllocationStrategy=price-capacity-optimized` with the
  full approved pool. No maintenance, replacement or market fallback exists.
- On-Demand batches use the same instant-Fleet adapter with explicit
  `--on-demand`, `OnDemandOptions.AllocationStrategy=lowest-price`, and only the
  first profile type across its approved subnets. Target capacity belongs solely
  to the selected market, with total equal to the requested instance count.
  Leave `WeightedCapacity` unset and omit `TargetCapacityUnitType` (the SDK
  restricts that field to attribute-based selection); no weights/vCPU semantics.
- `FleetLaunchTemplateSpecificationRequest` supplies an exact template ID and
  positive numeric version. Each override supplies an explicit `InstanceType`,
  `SubnetId`, matching `AvailabilityZone`, exact `ImageId`, and root
  `BlockDeviceMappings` using `FleetEbsBlockDeviceRequest` size/gp3/encrypted/delete
  fields. `MetadataOptions.HttpTokens=required` preserves IMDSv2. There are no
  floating AMI aliases, default template versions or instance requirements.
- The pinned override type has **no network-interface field**. #29 must remove
  the template interface's fixed subnet while retaining its security group,
  public address and deletion settings, then verify Fleet subnet overrides
  against that effective template. Runtime commands never edit templates.
- Instant Fleet supports top-level creation `TagSpecifications` for `fleet`,
  `instance` and `volume`. Apply identical required metadata to all three;
  tag inherited root volumes at creation. No post-launch rename is required.

See the [CreateFleet API](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_CreateFleet.html),
[override model](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_FleetLaunchTemplateOverridesRequest.html),
and [target-capacity model](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_TargetCapacitySpecificationRequest.html).
#30 must test actual serialized requests against the pinned SDK, not only these
Go field names. #34 must verify restricted-operator behavior live. Instant Fleet
does not require `AWSServiceRoleForEC2Fleet`; setup establishes the separate
Spot service-linked-role prerequisite without granting operator role creation.

### Names, groups and identities

New command forms are:

```sh
devbox up agent --count 2 --group smoke-batch
devbox up agent --count 2 --group smoke-batch --name agent
devbox up agent --on-demand --name smoke
devbox ls --group smoke-batch
devbox down NAME_OR_ID [NAME_OR_ID ...]
devbox down --group smoke-batch
devbox down --all [--yes]
```

Base defaults to group when supplied, otherwise `agent`; count defaults to 1.
Base/group are 1–63 ASCII letters/digits/underscores/hyphens, begin with a letter
or digit, and cannot resemble an EC2 instance ID. Group is independent of a
request: several requests may share a group but never share fulfillment counts.

Creation tags are `ManagedBy=devbox`, `Deployment`, `Owner`, `Profile`,
`Name=BASE`, `BaseName=BASE`, `NamingVersion=1`, `RequestId`, `BatchId` (equal to
request ID), `AttemptId`, `CreatedAt`, and `Group` when supplied. Fleet-wide tags
cannot assign ordinal worker names. The public name is instead
`BASE[:63-len("-"+INSTANCE_ID)] + "-" + INSTANCE_ID`, for example
`smoke-batch-i-0123456789abcdef0`. This is a deterministic suffix derived from
cloud identity, not `-01`/`-02` or response ordering. A 63-character base is
truncated to 43 characters for modern 19-character instance IDs. Names do not
change when a peer is absent or a later attempt adds a worker. AWS console
`Name` remains the shared base. Legacy `Name` values remain unchanged.

Discovery obtains these tags from complete paginated scoped AWS inventory,
deduplicates IDs, validates exact filter matches and sorts by instance ID. Access
resolves the generated public name; a duplicate legacy friendly name yields
candidate IDs and requires an explicit ID. Scope tags are not an atomic name
reservation; concurrent independent requests are allowed to share a base.

### Durable attempts, recovery and explicit retry

The user approved shared immutable S3 launch records on September 13, 2026.
Schema-v2 `BatchReceipt` caches the request ID, complete plan, plan digest and
ordered `AttemptReceipt` records. Each attempt stores its ID, parent ID, token,
requested count, creation timestamp, state, optional Fleet ID, every known
instance ID and all normalized pool/resource errors. Receipt v1 is read by the
existing recovery path, never rewritten as a fabricated batch. Unknown versions,
invalid JSON, changed pins, duplicate identities and invalid lineage fail closed.

The initial attempt ID is the first 16 bytes of SHA-256 of
`"devbox-attempt-v2\0" + REQUEST_ID + "\0"`. A successor adds its parent's
32-hex ID after the last separator. A parent has exactly one possible successor,
regardless of copied state, current time or requested count. The 64-hex client
token hashes canonical `{plan_sha256, attempt_id, count}` JSON. New parameters
never reuse a token. Actual SDK transport retries in one dispatch reuse the same
immutable input and token.

Under `launch_ledger.prefix/REQUEST_ID/`, use immutable `plan.json` plus
`attempts/ATTEMPT_ID/{prepared,dispatch,response}.json`. Every write uses
`If-None-Match: *`; the bucket policy enforces that header. Runtime roles cannot
delete or overwrite ledger objects; workers have no ledger access. Ledger
records are excluded from command-result expiration and remain until deliberate
deployment removal. The operator may read/write only its exact scope.

The dispatcher durably saves local preparation and announces request recovery
before cloud allocation, verifies the shared plan and prepared attempt, persists
local dispatch intent, then conditionally creates the permanent shared
`DispatchClaim` containing request/attempt IDs, plan/input digests and token.
**Only a positively acknowledged conditional-claim winner may send CreateFleet.**
A failed/ambiguous claim write, an existing claim, a recovered GET or a copied
local receipt never grants send permission. The claim is never released. A crash
after claiming but before sending can therefore remain unresolved; avoiding
duplicate workers takes precedence over automatic retry in that window.

Local process locks serialize a state directory, but shared claims coordinate
separate cooperating clients. This guarantee assumes the deployment/ledger is
retained and runtime IAM cannot erase it; it does not cover a setup administrator
deleting records, bypassing the protocol or direct external allocations.

States are `prepared -> dispatched -> complete|rejected|unknown`; later verified
complete evidence may resolve `unknown`. A complete instant-Fleet response must
account for all distinct returned identities, validate its envelope and retain
every error. HTTP success alone, number of errors, missing fields or duplicated
IDs cannot establish a missing count. Definitive API rejection with no possible
allocation is `rejected`; uncertain transport failure remains `unknown`. Store
the complete normalized shared response before authorizing any successor. A
failed post-dispatch write preserves all known request/Fleet/instance/root IDs
in output and does not turn the attempt back into `prepared`.

```sh
# Observe/reconcile only, including readiness; no batch allocation:
devbox up --resume REQUEST_ID --json
# Explicitly authorize only the proven original missing capacity:
devbox up --retry-missing REQUEST_ID --after ATTEMPT_ID --json
```

These options reject profile/count/group/name/market overrides. Batch resume
never dispatches, even for prepared v2 records; report a prepared record with
no allocation as such. Existing v1 prepared recovery retains its historical
same-token behavior. For v2, a fresh invocation without recovery syntax creates
an independent request and is never presented as a safe retry of uncertainty.

Before a retry, verify the shared original plan and complete lineage, reconcile
known identities and all request/attempt tags, and retain fulfilled identities
even after interruption, termination or user teardown. Missing means original
desired count minus cumulative distinct original successes, never desired minus
currently running workers. One confirmed success of two permits a single-worker
successor; an uncertain one-or-two success permits none. Repeating the same
`--after` resumes the existing successor without granting another claim. Another
retry requires explicit `--after` naming that successor after its own definitive
partial/zero response. Fully fulfilled requests never allocate replacements.

Revalidate account/scope, tags, template/image/type/subnet/market bindings during
reconciliation. Do not assume Fleet's client token propagates to each instance's
`ClientToken`. When a Fleet ID is known, its exact `DescribeFleets` record may
verify the token. [DescribeFleets](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeFleets.html)
does not list instant fleets without their IDs, and
[DescribeFleetInstances](https://docs.aws.amazon.com/AWSEC2/latest/APIReference/API_DescribeFleetInstances.html)
does not support instant fleets. Fleet activity `fulfilled`, current Fleet
capacity or a negative tag scan cannot prove historical allocation. Unbounded
attempts remain unknown; never infer missing capacity from eventual-consistency
absence. Losing local receipts still permits cloud inventory/access/cleanup;
retry requires intact authoritative shared records and scope. Losing those
records prevents safe retry, not inspection or teardown.

### Output, partial results and teardown

Batch lifecycle JSON uses one schema-v2 envelope on stdout with `command`, `ok`,
`exit_code`, `code`, `message`, request/status/counts, `plan`, `attempts`,
`instances`, `errors`, and applicable `resume_command`/`retry_command`.
`requested_count` is the original desired count; `fulfilled_count` is cumulative
distinct historical allocation; `ready_count` is currently ready workers;
`missing_count` is **null** when allocation cannot be bounded. Attempt fields
include parent/Fleet IDs, requested count, known instance IDs, missing count and
all pool errors. Worker fields retain legacy instance/root-volume/readiness
fields plus base/group/attempt/subnet/AZ/status. Errors contain allowlisted codes
and actionable messages, with resource/type/subnet context; never raw SDK text.

Preview goes to stderr before allocation: profile, region, eligible types and
subnets/AZs, count, market, group and base. Text results include the same data and
each known success/error. Progress, prompts and recovery commands stay on stderr
in JSON mode. Output/persistence failure after allocation cannot erase known IDs.

| Aggregate outcome | Meaning | Exit |
| --- | --- | --- |
| `ready` | All original capacity fulfilled and all current workers ready | 0 |
| `allocated` | Allocation complete; readiness observation still in progress | Progress only |
| `partial_capacity` | Definitive response fulfilled some original capacity; successes retained | 3 |
| `no_capacity` | Definitive zero fulfillment; show explicit retry with same pool, or deliberate profile/new-request alternatives | 1 |
| `allocation_unknown` | Fulfillment cannot be bounded; resume/inspect, never automatic new capacity | 1 |
| `readiness_failed` | Complete allocation, one or more workers not ready | 3 if some ready, otherwise 1 |
| `teardown_partial` | Some selected resources cleaned up and others failed/unverified | 3 |
| `teardown_failed` | No complete verified cleanup; include per-resource outcomes | 1 |
| `confirmation_declined` | Interactive refusal, zero terminations | 0 |
| `confirmation_required` | Noninteractive/EOF without affirmative consent, zero terminations | 2 |
| Invalid config/usage | Before mutation | 2 |
| Timeout/interruption | Preserve capacity/worker errors and known identities | 4 |

For example, full two-worker output has requested/fulfilled/ready `2/2/2`,
missing `0`, two generated names and exit 0. A definitive one-of-two result has
`2/1/1`, missing `1`, `partial_capacity`, exit 3 and a retry command for one
missing worker. A lost response with one observed ID has requested `2`, known
fulfilled `1`, missing `null`, `allocation_unknown` and a reconcile-only command.
An interrupted worker remains historically fulfilled, so its absence does not
increase missing count or authorize replacement. #32 observes at most four
verified workers concurrently under one overall readiness deadline of at most
five minutes, including document verification and queue time. A shorter caller
deadline wins. Failed workers do not cancel peers; unverified identities are not
probed. Known workers whose startup settings are still pending wait for exact
instance and root-volume verification within that same budget. Already verified
workers enter the queue first. Startup observation never dispatches again;
readiness cannot supply a missing-capacity bound without validated original
allocation evidence. Batch pins are rechecked on the final exact-ID read before each probe.
Advisory progress may be dropped under output backpressure so it cannot delay
queued workers; the final result retains every known worker and root-volume ID.
Inventory JSON uses schema 2 with group/base/request/attempt/subnet/AZ fields,
including legacy workers with empty batch metadata. Text inventory includes the
same identity fields. Missing local launch profiles or receipts do not prevent
cloud group discovery, access or teardown.

`down` accepts one selector: explicit names/IDs, one group, or all scoped workers.
`--yes` applies only to `--all`. Resolve and freeze candidate IDs before its
preview/prompt; workers arriving during confirmation cannot enter that set.
Interactive `n` declines successfully with no mutations; EOF fails with exit 2,
and Ctrl-C exits 4. A piped `devbox down --all` without `--yes` exits 2. Then
`devbox down --all --yes` performs scoped cleanup without the prompt.

Immediately before termination, fetch exact IDs and revalidate account, region,
ManagedBy/deployment/owner and selected name/group membership. Ambiguous names
produce errors with candidate IDs, never permission for every match. Independent
authorized targets may proceed while invalid/ambiguous/drifted targets report
per-target errors. Preserve exact root-volume mappings before mutation and report
termination requested/unknown/denied/observed separately from root deletion
deleted/retained/unavailable. Absence alone is not proof of cleanup. Repeated
teardown is harmless; it never deletes result storage, ledger records or unrelated
volumes. #34 records fresh live acceptance and exact-volume cleanup before the
parent can close.

Teardown JSON is one schema-v2 envelope with command/result/exit fields, `status`,
`selected_count`, `terminated_count`, `cleaned_count`, `instances` and `errors`.
Each worker retains its instance/root metadata plus termination `status` and
per-worker errors. `cleaned_count` requires exact observed EC2 termination and
verified deletion of every captured root; missing mappings never count. Complete
cleanup returns `teardown_complete`, exit 0. Some cleanups with other failures
return `teardown_partial`, exit 3; zero complete cleanups return
`teardown_failed`, exit 1. A successful empty selection is `no_managed_match` and
does not assert cleanup of a particular resource.

At most four teardown workers run concurrently. The command shares one deadline
across selection, confirmation and cleanup (20 seconds by default, configurable
up to five minutes). Individual AWS requests have a 15-second cap, and inventory
reads have a 128-page bound. Incomplete group/all discovery authorizes no target;
an incomplete explicit lookup cannot authorize its candidates, while independently
verified selectors may proceed. Preview/output failures never grant consent.

## Expiry policy and shared cleanup contract (#42)

The approved release policy is a configurable two-hour default (`default_ttl`),
explicit fresh-launch `up --ttl` precedence, a positive maximum of `168h` and a
five-minute cleanup schedule. TTL is elapsed wall time, including active SSH/exec,
boot failure and detached work. Each immutable request has one canonical UTC
`ExpiresAt` creation tag / `expires_at` field; equality (`now >= expires_at`)
means expired. Retries never refresh the deadline. Legacy requests will remain
inspectable/removable while fresh allocation requires a new expiry-aware request.

The normative [expiry contract](plans/04-expiry-contract.md) specifies exact
Go-duration/timestamp grammar, clock behavior, v2 plans/v3 batch receipts/v6
manifest migration, historical digest compatibility, frozen cleanup selection,
exact-ID revalidation, eligible states, scope, errors, volume evidence, and shared
text/JSON/exit contracts. Pure APIs and fixtures live in `internal/expiry`; pinned
historical bytes are under `internal/lifecycle/testdata/expiry-legacy`.

The rollout is implemented: #43 supplies TTL flags/configuration, immutable
expiry and allocation gates; #44–#48 supply cleanup service, cloud deployment and
recorded live evidence. #45 supplies manual `cleanup` and its no-write dry-run;
explicit `down` remains available for deliberate teardown. The current foundation
exports v6. Old v5 deployments cannot authorize this client's new launches and
must apply/re-export the actual upgrade. Revised expiry acceptance is recorded in
[the #48 ledger](acceptance/04-expiry.md); literal Scheduler retry exhaustion is
unproved and deferred to [#58](https://github.com/JosephWest2/cloud_dev/issues/58).

## Manual expiry cleanup (#45)

`devbox cleanup [--dry-run] [--json] [--timeout DURATION]` calls the shared
`internal/expirycleanup` service described in the
[service contract](plans/04-expiry-cleanup-service.md). Explicit invocation
provides consent for the scoped expiry policy. The separate `down` selectors and
confirmation rules are unchanged. Cleanup accepts global config/AWS-profile/
region options before or after the command; every duplicate option and any
selector, positional target, `--yes`, launch/TTL flag or exec/log flag fails with
`cleanup_invalid`, exit 2, before loading credentials or constructing a service.

`config.LoadCleanup` reads only schema 1, expected account, explicit region,
deployment, exact configured owner and credential profile. It rejects unknown
configuration fields but deliberately ignores known launch-only settings,
including invalid `default_ttl` or `max_count`. It never reads the manifest,
workload profile, SSH identity or receipts, and requires no access tools, SSM,
OpenTofu or scheduler health. Profile and region overrides keep their documented
precedence. Credential loading and STS/EC2 share a bounded context and selected
region. The service verifies expected account before discovery or mutation.

The outer timeout defaults to 165s and accepts positive durations through 5m.
The adapter bounds credential setup and service work to at most 165s; an earlier
caller deadline wins. NewAWS uses the shared 15s request and four-worker limits.
Dry-run bypasses the evidence sink and every mutation API, and returns only an
advisory snapshot. No stale dry-run result grants later termination authority.

JSON stdout is one unchanged schema-1 `expiry.Result` envelope, including partial
or interrupted outcomes. Empty arrays are emitted even for early failures. Text
shows the same scope, times, flags, counts, IDs, expiry/skip codes, termination
status and exact root-volume outcomes, with allowlisted explanations. Exit codes
follow the [shared table](plans/04-expiry-contract.md).
An output write failure returns 1 and attempts to preserve the complete result
JSON on stderr; a failed/truncated stdout stream itself cannot be repaired.
For cleanup only, both the supervisor and worker ignore SIGPIPE so closed
inherited stdout/stderr descriptors return write errors instead of terminating
the process before evidence/fallback handling. Other commands retain their
existing process and signal behavior.

Actual cleanup emits newline-delimited schema-1 `expiry.Event` evidence on stderr.
The concurrent-safe sink acknowledges the full write before dispatch; short,
failed or canceled writes cannot authorize termination. Evidence includes trusted
scope, run/time/instance identity, expiry and exact root/device mappings with
DeleteOnTermination flags. The sink serializes writes under a context-aware gate,
permits at most one underlying write in flight and never closes caller-owned
stderr. An arbitrary blocking Go `io.Writer` cannot be canceled; acknowledgment
and queue waiting remain bounded by context, and a blocked write cannot start
additional writer goroutines or authorize mutation. Final diagnostic writes use
a separate one-second cap per write through that same gate. Retain stderr when
investigating incomplete root deletion; it is not a new launch-ledger record.

Missing expiry is a benign legacy skip; future expiry is a benign skip; malformed
or duplicate expiry is a per-resource diagnostic. `ls` remains available and
explicit `down INSTANCE_ID` provides deliberate teardown. Incomplete discovery
has zero authorized candidates. Partial failures retain known IDs and can be
safely rerun with fresh checks. Already-terminated instances without historical
root mappings may be benign, but never count as verified root deletion.

Controlled API/CLI fixtures cover the shared decision contract, no-write dry-run,
account/profile scope, absent launch prerequisites, invalid syntax, malformed and
mixed outcomes, uncertain roots, interruption/timeout, evidence failure and
stdout failure. See the [manual recovery runbook](acceptance/45-manual-cleanup.md).
These checks do not deploy a scheduler or replace #48's live/manual/offline gate.

## Guided setup (#64)

`devbox setup` is a separate interactive adapter for new foundations and local
connection to an existing trusted manifest (`--manifest PATH`). `--resume ID`
uses a private journal; `setup status ID --json` reads recorded progress without
credentials or AWS. Duplicate/unknown options, incompatible selectors and invalid
scope fail before setup. Setup requires a terminal for mutation and has no
`--yes`. Ordinary commands retain their existing parsing, deadlines and semantics.

Setup result schema 1 contains `command="setup"`, `cli_version`, `scope`, `setup_id` when assigned, `ok`,
`exit_code`, safe `code`/`message`, `phase`, `partial`, `recorded`, `completed`,
`installation`, `foundation`, `scheduling`, `recovery` when assigned, and `checks`
(`name`, `ok`). Arrays are always emitted. Status reports recorded progress rather
than fresh health. Installation/foundation/scheduling outcomes are separate;
legacy manifests can produce `foundation="legacy_recovery_only"`. Disabled
scheduling is explicit. Requested scheduling with pending health cannot return
complete success. Exit 0 is requested completion, 1 a prerequisite/operation
failure, 2 invalid input or missing confirmation, and 4 timeout/interruption.
Errors keep known phase, progress and setup ID; output errors return 1.

Journal schema 1 records ID, CLI version, bundle and OpenTofu digests, confirmed
inputs and their digest, mode, phase/state, pending mutation, completed stages,
update time, scheduling choice/observation boundary and any approved local-file
transaction. It contains no credential values or private-key bytes. Input fields
are account/region/deployment/owner, source/setup/operator profiles, effective
setup IAM principal, SSH path/public key, state bucket, exact AMI, selected config/
manifest/AWS file paths, and whether an existing operator profile is used directly.
Journals are recovery aids, not evidence that a cloud mutation did or did not
succeed. Intent is durably stored before mutation; failed intent storage prevents
dispatch. Unknown schemas/fields, changed input digests and incompatible CLI
versions prevent automated resume. Native state remains opaque to Go.

Bundle schema 1 contains version, source commit and exact relative-file SHA-256
mapping. It includes both infrastructure roots/provider locks and matching runner
and cleanup artifacts. Additional files, links, traversal, missing files and digest
mismatches are rejected. Bundle/workspace/generated inputs, the OpenTofu executable
and saved plan bytes are checked before an approved apply. Destructive or replacement
plans are rejected. Schedule enablement permits only the expected state transition;
unrelated changes require manual review. Setup exports and validates only the named
manifest output and never invents resource IDs or upgrades schemas in place.

Local publication records private staged bytes and backups, previews changed
scope/profile/configuration values, and checks all target digests before replacing
any. Resume completes only the exact approved transaction; concurrent edits fail.
Unrelated configuration is preserved. Existing credential-file sections cannot
shadow generated profiles, and source chains cannot recurse through generated
profiles. SDK, AWS CLI, provider and backend use explicit profile selection.
Source CLI and bridge SDK identities must match; provisioning revalidates the
confirmed account/principal. Final resource checks require the exact manifest
operator role and session name. Credentials remain in normal AWS sources.

Setup configuration verification reuses foundation checks, excluding schedule
activity and observed alarm/completion health before enablement. It still verifies
alarm/filter, function, IAM and evidence configuration. Full doctor behavior is
unchanged. After enablement, successful evidence must be scope-matched, satisfy
normal health checks and come from a scheduled time after the recorded enablement
attempt. A health timeout leaves the schedule enabled; resume observes it.

Setup tool deadlines default to 20 minutes (maximum 1 hour); initial health wait
is 15 minutes (maximum 30 minutes). Prompt time is separate. Noninteractive tools
run in separate process groups so physical Ctrl-C reaches the worker first; the
worker delivers one graceful interrupt, drains up to 90 seconds, and then cleans
up the child group. Setup's supervisor allows that drain. Existing exec detach,
SSH terminal handling and cleanup output behavior retain their own contracts.

## Interactive AWS authentication

After argument/configuration validation and before command deadlines, terminal
invocations may probe a selected browser credential source with AWS CLI
`configure export-credentials` (stdout discarded). Recognized expired or missing
session diagnostics trigger at most one `aws login` or `aws sso login` attempt.
Only explicit profile chains and the documented export-credentials bridge are
followed; arbitrary credential processes are never interpreted as login commands.
Static/environment credentials retain their existing behavior. Network failures,
permission failures and account mismatches do not trigger browser login.

Authentication has separate bounds: 15 seconds for the credential probe and five
minutes for login. Login inherits supervised process-group cancellation and uses
terminal stdin and stderr, never command stdout. This explicit AWS login UI is
separate from SDK/provider diagnostics, which remain suppressed. Login failure is
carried into AWS loading and existing command result/exit contracts; no operation
is replayed. Normal expected-account and exact-target checks still run afterward.

`--json`, nonterminal stdin, `proxy`, `ssh-config` and `logs` never start automatic
login. Guided setup invokes the same preflight on its recorded source profile
before executing stages, including resume; recorded `setup status` never invokes
it. Setup's cloud/local-publication confirmations remain independent of login.
