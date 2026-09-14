# Issue #3 Spot group acceptance and cleanup

**Acceptance and fresh review complete; PR #41 carries this record.** All six
live workflow steps and required controlled gates pass. Two Spot workers and one explicit On-Demand worker were
verified ready, exercised and terminated; all three exact roots are deleted.
Scoped live-worker and volume inventories are empty. Initial launch failures,
their reviewed fixes and the final build's validation limits are retained below.

The initial merged implementation revision is
`81cd07747226a988169e578d8b3f76da7564b6fa` (September 14, 2026, US/Central).
Freshly reviewed integration correction
`aea245ce68860fb5096c0ba370d09efa7e36b8de` adds the live Fleet IAM fix and
doctor wording correction described in H1. Runtime revision
`6ae85027b7c614c397c84b26cdae4c0510d0b94f` adds the reviewed pending-worker
readiness correction; H5 exercised that build directly. Final runtime revision
`be4107f87cba3659b93d42eb69672354ba46805a` corrects the subsequently observed
missing pending-root mapping, with controlled regression coverage and final
inventory verification. The live commands retain the exact earlier build
outcomes below; no direct live launch is claimed for the final build. The normative behavior is in the
[Spot batch contract](../contracts.md#spot-batch-contract-28). Each implementation
slice received fresh independent subagent review before merging:

| Child | Implementation | Prior evidence |
| --- | --- | --- |
| #28 profiles and contracts | [PR #35](https://github.com/JosephWest2/cloud_dev/pull/35) | [Contract checks and review](../plans/28-spot-contract.md#28-verification-and-review) |
| #29 foundation and permissions | [PR #36](https://github.com/JosephWest2/cloud_dev/pull/36) | [Foundation checks and migration](29-spot-foundation.md) |
| #30 instant-Fleet attempt | [PR #37](https://github.com/JosephWest2/cloud_dev/pull/37) | [SDK, persistence and worker checks](30-fleet-attempt.md) |
| #31 shared recovery | [PR #38](https://github.com/JosephWest2/cloud_dev/pull/38) | [S3 claims, reconciliation and retry races](31-shared-recovery.md) |
| #32 launch, discovery and access | [PR #39](https://github.com/JosephWest2/cloud_dev/pull/39) | [Integrated batch/readiness checks](32-group-launch.md) |
| #33 plural teardown | [PR #40](https://github.com/JosephWest2/cloud_dev/pull/40) | [Scope, confirmation and root cleanup checks](33-plural-teardown.md) |
| #34 fresh acceptance | [PR #41](https://github.com/JosephWest2/cloud_dev/pull/41) | Acceptance and fresh review complete; PR #41 carries this record. |

## Scope, prerequisites and evidence

Use Linux, Bash, Go, Python 3, `jq`, AWS CLI, OpenSSH, the Session Manager plugin
and OpenTofu 1.12.6 (`/tmp/devbox-tools/tofu`); the locked AWS provider is 6.64.0.
Record actual versions and command dates in the evidence directory. Never record
credential-process output, keys or a full OpenTofu state dump.

| Descriptor | Selected value |
| --- | --- |
| AWS account / region | `464557813916` / `us-east-2` (Ohio) |
| Deployment / owner | `personal-dev` / `joseph` |
| Setup / operator profiles | `devbox-setup` / `devbox-operator` |
| Live checkout | `/tmp/devbox-issue34-live-81cd077` |
| Evidence directory | `/tmp/devbox-issue34-evidence` |
| Intended v5 config | `/tmp/devbox-issue34-evidence/config/config.toml` |
| Retained legacy config and manifest | `/tmp/devbox-issue34-evidence/legacy-config/` |
| Live backend data | `/tmp/devbox-issue34-live-81cd077/.tofu-live/foundation` |
| Selected profile | Embedded schema-2 `agent`, x86_64, encrypted disposable 100-GiB gp3 root |
| Spot candidate types | `c7i.2xlarge`, `c7a.2xlarge`, `c6i.2xlarge`, `c6a.2xlarge`, intersected with exported offerings |
| Original desired counts | Spot `2`, group `smoke-batch`; explicit On-Demand `1`, group `smoke-ondemand` |
| Applied AMI / template | `ami-00adec9774170bad2` / `lt-0ef8072b1bcc495b1`, numeric version `5` |
| Allowed AZs / actual placements | `us-east-2a`, `us-east-2b`, `us-east-2c`; both Spot workers placed in `us-east-2c` |
| Request / attempt / Fleet / worker / root / command IDs | Spot request/attempts/Fleet and workers/roots in H1; three command IDs in H2/H3; On-Demand request/attempt/Fleet/worker/root in H5; exact cleanup in H4/H6 |

At the pre-apply checkpoint, both setup and restricted-operator STS checks
authenticated in the selected account, scoped EC2 inventory was empty including
historical instances, and the Spot service-linked role was absent
(`NoSuchEntity`). These baseline observations do not prove effective allocation
permissions. Fresh plan review passed, and the user subsequently approved its
apply, role creation and the live worker campaign. The v5 export and operator
`doctor` subsequently passed before allocation. Standard Spot quota is 32 vCPUs; the planned two 8-vCPU workers
require 16. Quota availability is not a capacity guarantee.

Before apply, preserve the legacy manifest and any unexpired completed command
ID that will be used to check retained result access. If no retained command is
available, record that fact; do not claim a historical-result read was exercised.
Keep existing workers discoverable and cleanable through their original scope.
An unexpected baseline worker requires a decision about its ownership and
disposition before a campaign-wide `down --all`.

Build and test the implementation in the ordinary checkout, whose infrastructure
initialization is backend-disabled. **Do not run `make infra-check` in the live
checkout or with its `TF_DATA_DIR`.** Capture these fresh results separately from
the prior slice passes:

```sh
make check
make build
make infra-check TOFU=/tmp/devbox-tools/tofu
go test -race ./internal/lifecycle ./internal/cli ./internal/access ./internal/execution -count=1
git diff --check
```

Prior `make check`/`make build` passed in each slice. #29 passed one bootstrap and
23 foundation OpenTofu runs, the Go export/hash bridge and 25 modeled IAM chains.
#30–#33 passed their recorded race suites. The current combined revision's fresh
check results are recorded below.

After review and the setup decision, follow the
[Spot role prerequisite and upgrade procedure](../setup.md#prepare-the-accounts-spot-role).
Apply only the reviewed saved plan with its unchanged runner artifact. Export
only `deployment_manifest` beside the intended config; a copied v4 manifest is
not an upgrade. Then run restricted-operator `doctor`. Allocation remains blocked
until it returns exit 0 with every required check passing.

## Fresh plan and preflight — approved

On September 14, 2026, prepared the live checkout from exact merged revision
`81cd07747226a988169e578d8b3f76da7564b6fa`, copied the trusted deployment inputs
and initialized its existing S3 backend in the separate data directory above.
No apply, service-linked-role creation, allocation or termination had run when
this preflight evidence was captured. After reviewing the concrete checkpoint,
the user explicitly replied, “Yes, you can proceed,” authorizing this saved
plan's apply, the Spot service-linked role, two Spot workers followed by one
On-Demand worker, and termination/root-volume cleanup.

The first post-approval checks stopped before any mutation: setup STS and
restricted-operator EC2 inventory both returned exit 255 because the AWS login
session had expired. The saved-plan and runner hashes still matched the reviewed
pins. `$work/pre-apply-auth-check.json` records the timestamp, approved scope,
hashes, actual authentication errors and that no mutation was attempted. The
user refreshed the login; repeated setup/operator identity checks returned the
selected account and scoped inventory was empty. Work resumed under the existing
approval, with the actual mutation results recorded below.

The saved plan is `$live/infra/foundation/issue34.tfplan`, SHA-256
`b95db9e2bd1f006116e1c6d57d1ce7892b615301722102a5f82f5f7ac795af77`.
Its readable and structured renderings are `$work/live-plan.txt` and
`$work/live-plan.json`. The detailed plan command returned 2, meaning changes
are proposed. The plan contains **5 additions, 4 in-place updates and 1 deletion**:

| Planned action | Exact resource / effect |
| --- | --- |
| Create two subnets | `aws_subnet.additional["us-east-2b"]`, `10.77.2.0/24`; `aws_subnet.additional["us-east-2c"]`, `10.77.3.0/24` |
| Create their two route associations | `aws_route_table_association.additional` for b/c, using existing `rtb-0e618affde4165a24` |
| Update two scoped inline policies | `aws_iam_role_policy.instance`, `aws_iam_role_policy.operator`; Fleet and conditional permanent-ledger permissions |
| Update template in place | `aws_launch_template.agent`; new bootstrap/runner and placement selected by Fleet, new numeric version resolved at apply |
| Update private bucket policy | `aws_s3_bucket_policy.results`; immutable launch writes and scoped deletion protection |
| Replace content-addressed runner object | `aws_s3_object.runner`, create new hash key before deleting old artifact |

The existing VPC `vpc-07086c3d0aa6cd4df`, subnet-a
`subnet-0d4c255b7bbbca257`, route table, internet gateway, security group,
bucket storage/privacy/encryption and result-only 30-day lifecycle are unchanged.
The ledger prefix is
`launches/v2/464557813916/us-east-2/personal-dev/joseph/` in the existing bucket
`devbox-results-464557813916-us-east-2-0164521a41ec5a6e`; it has no expiration.
Old command results and state remain retained. The only object removed by this
plan is the previous runner artifact, after its replacement is created.

| Pinned build / bootstrap | SHA-256 |
| --- | --- |
| CLI copied to live checkout | `4968fa79f5ba83f2000fa6a5d22e552f90e7828a3711e39774775cdcbaf3643e` |
| Runner copied to live checkout and referenced by plan | `2dc3a329080c385f2b70b762ee2da8f3e6a5798a915b61a66edebaba56b324d8` |
| Planned bootstrap | `a1192682b811bdaa909930e3c806d25b8e715181d6dbe2d5db913e50d566b886` |

Fresh independent reviewer `review34_live_plan` approved this exact saved plan
for the user's apply checkpoint with no blocking findings, and independently
regenerated its JSON rendering. Read-only IAM checks found one inline policy per
role and no attached managed policies. The instance policy is 2,708 bytes; the
passing three-AZ live-label fixture covers the operator policy's expected
10,144 characters against its 10,240-character limit. At plan time the quota
guard was deferred because subnet IDs were unknown. The actual applied policy
size/hash and exported resource pins were subsequently verified below.

Fresh preflight evidence in `$work`:

| Check | Actual result / artifact |
| --- | --- |
| Separate live backend initialization | PASS, `live-init.log` |
| `make check` on merged implementation | PASS, `make-check.log` (module verification, build, vet, all Go tests) |
| `make build` | PASS, `make-build.log`; copied CLI hash matches |
| Backend-disabled `make infra-check` | PASS, `infra-check.log`; 23 foundation tests, one bootstrap test and Go export bridge |
| `make check` including the #34 sequential cleanup regression | PASS, `make-check-draft.log`; production implementation remains unchanged from `81cd077` |
| New sequential cleanup test under `-race -count=1` | PASS, `sequential-cleanup-race.log` |
| Standard Spot quota | 32 vCPUs, `spot-quota.json` (`L-34B43A08`) |
| Existing public `logs` through preserved v4 config | PASS, `legacy-logs-before.json`, command `dc1-0c3d4bc0d96a54a3876db78254737ff7`, exit 0 |

Repeat the retained command read after upgrade, using the unchanged legacy
config, and record both actual results. Fresh tool versions: Go
`go1.27.0-X:nodwarf5 linux/amd64`, AWS CLI `2.34.32`, OpenSSH `10.5p1`,
Session Manager plugin `1.2.835.0`, jq `1.8.2`, Python `3.14.7`, OpenTofu
`1.12.6 linux_amd64`, locked AWS provider `6.64.0`.

Fresh independent reviewer `review34_runbook` approved the runbook and new
sequential cleanup test for this checkpoint and a draft PR, with no remaining
findings. The reviewer independently passed the targeted race test, checked all
12 shell blocks and matched the recorded plan/binary hashes. Eventual live
evidence and any integration changes still require fresh review before merging.

The approved migration and export commands are:

```sh
live=/tmp/devbox-issue34-live-81cd077
work=/tmp/devbox-issue34-evidence
aws iam create-service-linked-role --aws-service-name spot.amazonaws.com \
  --profile devbox-setup --output json --no-cli-pager > "$work/spot-role-created.json"
AWS_PROFILE=devbox-setup TF_DATA_DIR="$live/.tofu-live/foundation" \
  /tmp/devbox-tools/tofu -chdir="$live/infra/foundation" \
  apply -input=false issue34.tfplan > "$work/live-apply.log" 2>&1
AWS_PROFILE=devbox-setup TF_DATA_DIR="$live/.tofu-live/foundation" \
  /tmp/devbox-tools/tofu -chdir="$live/infra/foundation" \
  output -json deployment_manifest > "$work/config/deployment.json.tmp" && \
  mv "$work/config/deployment.json.tmp" "$work/config/deployment.json"
sha256sum "$work/config/deployment.json" > "$work/manifest.sha256"
```

Recheck the role first if another setup session may have created it; an already
present correct role needs no creation. Stop on any failed setup command and
inspect its evidence before continuing. Actual apply/export outcomes and pins
are recorded below. The execution helper captured the apply's text stdout as
`live-apply.json` despite that filename's suffix, with separate stderr, start
time and exit files.

## Applied foundation and live evidence

All artifact names in this section are relative to
`/tmp/devbox-issue34-evidence`. Dates and captured start times are UTC.

| Setup / baseline step | Actual outcome / evidence |
| --- | --- |
| Account Spot service-linked role | PASS; `spot-role-created` started `2026-09-14T13:18:42Z`, exit 0. Created `arn:aws:iam::464557813916:role/aws-service-role/spot.amazonaws.com/AWSServiceRoleForEC2Spot`, trusting `spot.amazonaws.com`. |
| Reviewed saved-plan apply | PASS; `live-apply` started `2026-09-14T13:18:53Z`, exit 0: 5 added, 4 changed, 1 destroyed. The approved plan and runner hashes were unchanged. |
| Initial schema-5 manifest export and pins | PASS; preserved as `manifest-before-fleet-iam.json`, SHA-256 `66d2d4b8f9b5b4adc3706418f8aa58a46a30e3c3d5bae2033eaa78b38a6b5424` (`manifest.sha256`), template version `5`. The policy-only correction's export is recorded in H1. |
| Applied operator inline-policy limit and digest | PASS; `operator-policy.json`, 10,144 canonical characters; sorted compact JSON SHA-256 `f3cc7d1acddb44802ce435eb78838c1c27149134e2af0becb636bd28c44e4915`, matching the manifest. |
| Restricted-operator `doctor` | PASS; `doctor` started `2026-09-14T13:19:22Z`, exit 0, all 15 checks pass, including live network, template, IAM, results and launch ledger. Its stale Spot-not-shipped display text is a separate wording defect, not a failed check. |
| Retained v4 command result after upgrade | PASS; `legacy-logs-after-upgrade` started `2026-09-14T13:19:33Z`, exit 0, complete durable publication for `dc1-0c3d4bc0d96a54a3876db78254737ff7`. This metadata read reports `verification: not_downloaded`; it does not claim fresh byte verification. |
| Scoped pre-allocation CLI inventory | PASS; `baseline` started `2026-09-14T13:19:40Z`, exit 0, schema-2 `instances: []`. |

The applied subnets are the preserved `subnet-0d4c255b7bbbca257` in
`us-east-2a`, new `subnet-0ebbaddf117adef0e` in `us-east-2b`, and new
`subnet-014cf55846af4a48d` in `us-east-2c`. No worker launch is implied by the
successful infrastructure apply.

The applied AMI is `ami-00adec9774170bad2`; launch template
`lt-0ef8072b1bcc495b1` is pinned to numeric version `5`. Bootstrap and runner
SHA-256 remain respectively
`a1192682b811bdaa909930e3c806d25b8e715181d6dbe2d5db913e50d566b886` and
`2dc3a329080c385f2b70b762ee2da8f3e6a5798a915b61a66edebaba56b324d8`.
The permanent ledger keeps the scoped bucket/prefix recorded above, with applied
bucket-policy SHA-256
`d3aa8a96850b2d2be51a6b83fa11f9b40eb93acccefa174aac253bae5fde522f`.

## Capture helpers and baseline

Run the following in Bash, evaluating each result before proceeding. The helper
retains stdout, stderr and the real process exit; it preserves terminal stdin
and displays stderr for preview and confirmation. Do not automatically rerun an allocation command after a
failure. `--json` stdout must contain one result object; preview, progress and
prompts appear on stderr.

```sh
umask 077
live=/tmp/devbox-issue34-live-81cd077
work=/tmp/devbox-issue34-evidence
cli="$live/bin/devbox"
config="$work/config/config.toml"
manifest="$work/config/deployment.json"
mkdir -p "$work/state"
export XDG_STATE_HOME="$work/state"
db=("$cli" --config "$config" --aws-profile devbox-operator --region us-east-2)
awsop=(aws --profile devbox-operator --region us-east-2 --output json --no-cli-pager)
capture() {
  local capture_label="$1" capture_rc=0
  shift
  date -u +%FT%TZ > "$work/$capture_label.started"
  "$@" > "$work/$capture_label.json" 2> >(tee "$work/$capture_label.stderr" >&2) || capture_rc=$?
  printf '%s\n' "$capture_rc" > "$work/$capture_label.exit"
  return "$capture_rc"
}
capture doctor "${db[@]}" doctor --timeout 90s --json
capture legacy-logs-after-upgrade "$cli" --config "$work/legacy-config/config.toml" \
  --aws-profile devbox-operator logs dc1-0c3d4bc0d96a54a3876db78254737ff7 --json
capture baseline "${db[@]}" ls --json
jq -e '.ok and all(.instances[]; .ec2_state == "terminated")' "$work/baseline.json"
```

Retain a non-secret manifest copy and SHA-256 plus `git rev-parse HEAD`, `go
version`, `aws --version`, `ssh -V`, `session-manager-plugin --version`, `jq
--version`, `python3 --version` and `tofu version`. The fresh plan section must
record the actual applied plan digest, apply date/result, numeric exported
template version, AMI, bootstrap/runner pins, approved type/subnet/AZ pairs and
permanent ledger descriptor. Result storage keeps its existing retention;
launch records must remain outside expiration.

## H1: two Spot workers and their exact identities — PASS after explicit recovery

`spot-up` started `2026-09-14T13:20:05Z` and returned exit 1. The captured
preview correctly resolved profile `agent`, Ohio, Spot, count 2 and base/group
`smoke-batch`; the immutable plan lists all 12 approved type/subnet/AZ choices.
EC2 denied allocation (`launch_permission_denied`). The aggregate code is
`no_capacity`, but this evidence establishes an authorization failure, not Spot
pool scarcity. That first attempt did not pass H1.

| Original request | Attempt / status | Captured allocation / recovery evidence |
| --- | --- | --- |
| `2b7125d97638df1e3dc7573a47b5eee8` | `8a576a05189eca66b16db1424469a4d6`, `rejected` | Requested 2, fulfilled 0, ready 0, bounded missing 2; no Fleet or worker IDs. `spot-up.json`, `.stderr`, `.started`, `.exit` retain the original result. |

The result includes observation-only
`up --resume 2b7125d97638df1e3dc7573a47b5eee8` and explicit
`up --retry-missing 2b7125d97638df1e3dc7573a47b5eee8 --after 8a576a05189eca66b16db1424469a4d6`
commands with the exact config, region and operator profile. Investigate and
review the permission failure before requesting the proven remainder; do not
rerun the fresh-allocation command below as an implicit retry.

Observation-only `spot-reconcile-denied` at `2026-09-14T13:23:25Z` returned the
same rejected attempt, zero workers and bounded missing 2 (exit 1).
`denied-inventory` at `13:24:13Z` independently returned no scoped instances
(exit 0). `initial-launch-ledger` at that same second returned the original
request's four permanent S3 objects: `plan.json` and the attempt's
`prepared.json`, `dispatch.json` and `response.json`, all with SHA-256 checksums.
The rejected allocation therefore retains shared recovery evidence.

A diagnostic rebuilt the original Fleet request with `DryRun=true` and made no
allocation. `fleet-dryrun.stderr` records `UnauthorizedOperation`;
`fleet-authorization.json` decodes the denied `CreateFleet` authorization for
`arn:aws:ec2:us-east-2:464557813916:volume/*`. AWS supplied no request-tag,
encryption or volume-type condition context for that precheck. This exposed a
live IAM integration gap despite passing modeled policy checks; the corrective
policy and its tests received the fresh review recorded next before apply and
retry.

The resulting correction separates the initial Fleet resource authorization
from the conditions enforced during `RunInstances`. Fresh independent reviewer
`review34_integration` approved the correction, doctor wording and IAM
documentation; all 23 foundation tests and 29 modeled IAM cases passed. The
new regression cases fail against the prior policy. The reviewed `iam.tf`
SHA-256 is
`50c5d44f1c26adf1243ac68b709c7b290f09e75235965c5a1406fe77bce71a1b`.
The reviewer also approved saved plan `issue34-fleet-iam.tfplan`, SHA-256
`585b760ebb26de7d1b672040f158a6c1f913f55c07d2c51a407ad71795bb3fb0`:
it changes only the operator inline policy and its exported digest, with no
resource addition or deletion. The rendered policy decreases from 10,144 to
10,042 characters and its planned canonical SHA-256 is
`f092e4ddd8a6f59990dba6eda543db3c9e39daa74c8d93673aca105cd637491f`.
`fleet-iam-plan.*`, `fleet-iam-simulations.log` and
`fleet-iam-before-simulations.log` retain the plan and positive/negative checks.
`fleet-iam-apply` started `2026-09-14T13:28:42Z` and succeeded with exit 0:
0 added, 1 changed, 0 destroyed. The corrected export in `config/deployment.json`
has SHA-256
`33e29b8d6e1e23a3c0597d535df0da430caf27dcfa3ff51b18d50ade1c79a8d8`
(`manifest-after-fleet-iam.sha256`) and the reviewed new policy digest; all AMI,
template, runner, bootstrap and ledger pins are unchanged.

`fleet-dryrun-after` at `2026-09-14T13:29:15Z` received EC2's
`DryRunOperation` response: the request passed authorization and was not
allocated because `DryRun` was set. The diagnostic process returned exit 0;
its `.json` file contains raw SDK text. `doctor-after-fleet-iam` at `13:29:28Z`
returned exit 0 with all 15 checks passing. The live CLI now includes the
reviewed doctor wording correction, SHA-256
`95df74b7d6b044a7e01bc83edf729a2889c8b79ace37273e3f77a00661c4641e`
(`cli-after-doctor-fix.sha256`); allocation code and the runner remain unchanged.

Read-only controls in `runinstances-dryrun-controls.jsonl` at
`2026-09-14T13:32:23Z`–`13:32:26Z` returned `DryRunOperation` for the approved
exact template, network, tags and disk. EC2 returned `UnauthorizedOperation`
for unencrypted or gp2 roots (`RunInstances` on `volume/*`), IMDSv1
(`RunInstances` on `instance/*`), and a foreign owner (`CreateTags` on
`instance/*`). Every request set `DryRun=true` and returned no instance IDs.
Synthetic foreign profile/subnet probes returned invalid/not-found errors,
which do not prove IAM denial. These results prove direct `RunInstances`
constraints; Fleet dry-run variants only passed the initial authorization stage
and do not establish negative dependent authorization during a real Fleet
launch.

The explicit `spot-retry` started `2026-09-14T13:31:06Z` for the original
request's proven remainder of two, using `--after` the rejected attempt. It
created deterministic successor `974da05dd3d5114cb038e64da6721a56` and Fleet
`fleet-abbdd735-c42e-6685-8eb0-0b20b6ecc29d`; the Fleet response is complete
with both exact worker IDs. Immediate verification returned
`worker_observation_unavailable`, so the CLI correctly preserved fulfilled 2,
missing `null`, ready 0 and both root mappings, with exit 1. This is an
observation failure after allocation, not permission to allocate again. Only
resume/inspection is appropriate while readiness remains unresolved.

Fresh reviewer `review34_startup` identified a related startup integration
defect: this early pending-worker observation ended `up` before its bounded
readiness wait. The identities and uncertain outcome were preserved, but the
command should continue observing pending workers within its deadline. A
correction was implemented in
`6ae85027b7c614c397c84b26cdae4c0510d0b94f` and freshly approved by
`review34_integration`. It waits for exact worker pins within the shared
deadline, preserves independent ready peers and root identities, and never
dispatches extra capacity. Regressions cover pending-to-ready transitions,
timeout with a ready peer, mismatched workers, queue fairness, an explicit
successor making only one allocation, and preservation of the original
response-persistence failure. `startup-final-race.log` records the passing
independent targeted race run. H5 exercised a direct launch with that reviewed
build; its actual nonzero result and the subsequent missing-root correction are
retained below.

The startup-correction CLI used in H5/H6 has SHA-256
`e1993034357614c52d70919897962cf36f9d4b553cb62e15471d011b4ed97d2c`;
the runner remains
`2dc3a329080c385f2b70b762ee2da8f3e6a5798a915b61a66edebaba56b324d8`
(`final-binaries.sha256`). Checks on that revision all returned exit 0:

| Startup-correction validation | Captured start UTC / evidence |
| --- | --- |
| `make check` | `2026-09-14T13:47:22Z`; `final-check.json`, `.stderr`, `.exit` |
| `make build` | `2026-09-14T13:47:31Z`; `final-build.json`, `.stderr`, `.exit` |
| Backend-disabled `make infra-check` | `2026-09-14T13:46:21Z`; `final-infra-check.json`, `.stderr`, `.exit`; 23 foundation tests, one bootstrap test and Go export/hash bridge |

These three `.json` files contain command text logs, not JSON envelopes.

| Allocated Spot worker / generated name | Type / placement | Captured root / mapping |
| --- | --- | --- |
| `i-077169681934e7447` / `smoke-batch-i-077169681934e7447` | `c6i.2xlarge`; `subnet-014cf55846af4a48d`, `us-east-2c` | `vol-0cd9b3cc8820562f5`; `/dev/sda1`, delete-on-termination true; independently verified deleted in H4 |
| `i-0ac8519de89e56a6b` / `smoke-batch-i-0ac8519de89e56a6b` | `c7a.2xlarge`; `subnet-014cf55846af4a48d`, `us-east-2c` | `vol-062ba16d07d70ea83`; `/dev/sda1`, delete-on-termination true; independently verified deleted in H4 |

Both belong to original request `2b7125d97638df1e3dc7573a47b5eee8`, group
`smoke-batch`, successor attempt `974da05dd3d5114cb038e64da6721a56`, and
the approved AMI/template version `5`. The table records the retained
`spot-retry.json` observations. Observation-only `spot-resume-ready` started
`2026-09-14T13:31:42Z` and subsequently returned exit 0, `ready`, requested /
fulfilled / ready `2/2/2` and missing 0 for the same request, successor and worker
IDs. Historical denial diagnostics remain attached to the original rejected
attempt; they do not erase the successful successor.

`spot-aws.json` and `spot-roots-before.json` independently confirm both permitted
placements, Spot lifecycle, exact AMI and AWS launch-template tags/version,
instance profile, security group, IMDSv2, all creation/scope/group/attempt tags,
and each correct 100-GiB encrypted gp3 root attachment with
delete-on-termination. `spot-raw-verification.json` records the passing checks,
12 eligible pools and the two observed types in the same permitted AZ.
`spot-text.json` captures generated names, groups, IDs, types and market in the
human-readable inventory. H1 passes through explicit retry and subsequent
observation, preserving the initial nonzero outputs.

The block below records the initial launch form and the later inspection
commands; it is not a command to replay this completed allocation. The initial
`spot-up` was run exactly once and contains no workers. This campaign's actual
worker source is `spot-resume-ready.json`, assigned to `spot_result` below and
used throughout H1–H3. The intervening allocation was the explicit
`up --retry-missing 2b7125d97638df1e3dc7573a47b5eee8 --after 8a576a05189eca66b16db1424469a4d6`;
readiness then used `up --resume 2b7125d97638df1e3dc7573a47b5eee8`.
For a future campaign whose initial launch succeeds, its own `spot-up.json`
can supply `spot_result` directly.

```sh
# Recorded original invocation; do not rerun this allocation to inspect evidence.
capture spot-up "${db[@]}" up agent --count 2 --group smoke-batch --timeout 5m --json
cat "$work/spot-up.stderr"
spot_result="$work/spot-resume-ready.json"
jq '{status,requested_count,fulfilled_count,ready_count,missing_count,plan,attempts,instances,errors}' "$spot_result"
jq -e '.schema_version == 2 and .ok and .status == "ready" and
  .requested_count == 2 and .fulfilled_count == 2 and .ready_count == 2 and
  .missing_count == 0 and (.instances | length == 2) and
  all(.instances[]; .market == "spot" and .group == "smoke-batch" and .readiness == "ready")' "$spot_result"
mapfile -t spot_names < <(jq -r '.instances | sort_by(.instance_id) | .[].name' "$spot_result")
mapfile -t spot_ids < <(jq -r '.instances | sort_by(.instance_id) | .[].instance_id' "$spot_result")
mapfile -t spot_roots < <(jq -r '.instances[].volumes[] | select(.root) | .volume_id' "$spot_result")
spot_request=$(jq -r '.request_id' "$spot_result")
capture spot-aws "${awsop[@]}" ec2 describe-instances --instance-ids "${spot_ids[@]}"
capture spot-roots-before "${awsop[@]}" ec2 describe-volumes --volume-ids "${spot_roots[@]}"
capture spot-text "${db[@]}" ls --group smoke-batch
```

Require real exit 0 and a pre-allocation preview with profile `agent`, Ohio,
count 2, market `spot`, base/group `smoke-batch` and every eligible type/subnet/AZ
choice. `spot-text.json` is deliberately text despite the helper's suffix; inspect
both generated names, actual types, market, group, IDs, placements and readiness.
Names end with the full instance ID. They are not ordinal suffixes.

Compare the raw AWS records with the result: exact AMI/template version, each
actual type/subnet/AZ in `plan.choices`, Spot lifecycle, managed scope and all
request/batch/attempt/group/base/naming tags. Verify exact captured roots are
encrypted gp3 of the planned size, attached to the correct workers and marked
delete-on-termination in the instance mapping; verify their creation tags too.
Save the request, every attempt/Fleet ID and the complete worker/root mappings.
The two workers may share one AZ; both placements must be permitted. A nonzero
result leads to recovery below, never a second fresh batch hidden as a retry.

## H2: independent commands and verified durable results — PASS

Both commands ran through generated names with `--timeout 90s`; each returned
local exit 0, workload exit 0 and complete publication. The independent
`spot-logs-one` and `spot-logs-two` exports started `2026-09-14T13:35:08Z`,
returned exit 0 and `verification: verified`. `spot-exec-verification.json`
records all four exact stdout/stderr byte comparisons passing.

| Command / start time UTC | Exact worker / public command ID | Verified output |
| --- | --- | --- |
| `spot-exec-one`, `2026-09-14T13:33:09Z` | `i-077169681934e7447`; `dc1-14c56d95c00d1c4101cd590da98c1790` | stdout `spot-worker-one\n`; empty stderr |
| `spot-exec-two`, `2026-09-14T13:33:55Z` | `i-0ac8519de89e56a6b`; `dc1-1b70612840e71daa21b051053460acfa` | stdout `spot-worker-two\n`; stderr `second-stderr\n` |

```sh
capture spot-exec-one "${db[@]}" exec "${spot_names[0]}" --timeout 90s --json -- /usr/bin/printf '%s\n' spot-worker-one
capture spot-exec-two "${db[@]}" exec "${spot_names[1]}" --timeout 90s --json -- sh -c 'printf "%s\n" spot-worker-two; printf "%s\n" second-stderr >&2'
command_one=$(jq -r '.command_id' "$work/spot-exec-one.json")
command_two=$(jq -r '.command_id' "$work/spot-exec-two.json")
test "$command_one" != "$command_two"
capture spot-logs-one "${db[@]}" logs "$command_one" --stdout-file "$work/one.stdout" --stderr-file "$work/one.stderr" --json
capture spot-logs-two "${db[@]}" logs "$command_two" --stdout-file "$work/two.stdout" --stderr-file "$work/two.stderr" --json
python3 - "$work" <<'PY'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
for label in ('spot-exec-one', 'spot-exec-two'):
    v = json.loads((p/(label+'.json')).read_text())
    assert v['outcome'] == 'remote_exit' and v['workload']['exit_code'] == 0
    assert v['publication'] == 'complete'
for label in ('spot-logs-one', 'spot-logs-two'):
    v = json.loads((p/(label+'.json')).read_text())
    assert v['verification'] == 'verified' and v['exit_code'] == 0
assert (p/'one.stdout').read_bytes() == b'spot-worker-one\n'
assert (p/'one.stderr').read_bytes() == b''
assert (p/'two.stdout').read_bytes() == b'spot-worker-two\n'
assert (p/'two.stderr').read_bytes() == b'second-stderr\n'
PY
```

Each exec must resolve its generated name to the intended different instance,
retain its command ID and return exit 0 with complete publication. Logs must
verify checksums and return the exact distinct bytes. Keep both command IDs for
post-teardown retrieval. Existing [SSH and execution acceptance](02-exec-logs.md)
is historical regression evidence, not a fresh result of this campaign.

## H3: restart with empty local state; name and ID access — PASS

The new directory `/tmp/devbox-issue34-evidence/fresh-state.lIfArjDP`
(`fresh-state-path.txt`) initially contained no state. `spot-rediscover` at
`2026-09-14T13:35:23Z` returned exit 0 and exactly the two original generated
names, IDs, group, request and attempt values. `spot-ssh-config` at `13:36:37Z`
resolved the first generated name with exit 0. `spot-exec-by-id` at `13:36:52Z`
targeted second worker `i-0ac8519de89e56a6b` and returned exit 0, workload
exit 0, complete publication and public command
`dc1-367b0a94dcac09f9cf3589f609aad379`. `spot-logs-by-id` at `13:37:43Z`
returned exit 0 with verified stdout `rediscovered-by-id\n` and empty stderr.

Actual `spot-ssh-interactive` from a PTY at `2026-09-14T13:37:02Z` produced
`spot-ssh-ok` in the remote shell and exited 0. Before `spot-resume-fresh` at
`13:37:23Z`, the fresh directory still contained no launch receipt. Resume
reconstructed it from shared S3 and returned exit 0, ready/fulfilled 2 and
missing 0, retaining exactly the original two attempts, successor Fleet and
worker IDs. No new allocation was requested during these observations.

Start new CLI processes with a new, initially empty state directory. This leaves
the original evidence intact and supplies no copied launch or command receipt:

```sh
fresh_state=$(mktemp -d "$work/fresh-state.XXXXXXXX")
test -z "$(ls -A "$fresh_state")"
capture spot-rediscover env XDG_STATE_HOME="$fresh_state" "${db[@]}" ls --group smoke-batch --json
jq -S '[.instances[] | {instance_id,name,group,request_id,attempt_id}] | sort_by(.instance_id)' "$spot_result" > "$work/original-identities.json"
jq -S '[.instances[] | {instance_id,name,group,request_id,attempt_id}] | sort_by(.instance_id)' "$work/spot-rediscover.json" > "$work/rediscovered-identities.json"
cmp "$work/original-identities.json" "$work/rediscovered-identities.json"
capture spot-ssh-config env XDG_STATE_HOME="$fresh_state" "${db[@]}" ssh-config "${spot_names[0]}"
capture spot-exec-by-id env XDG_STATE_HOME="$fresh_state" "${db[@]}" exec "${spot_ids[1]}" --timeout 90s --json -- /usr/bin/printf '%s\n' rediscovered-by-id
capture spot-resume-fresh env XDG_STATE_HOME="$fresh_state" "${db[@]}" up --resume "$spot_request" --timeout 5m --json
```

Require unchanged IDs/names and only the requested group, working SSH-config by
generated name and exec by exact ID, and the same request/attempt/worker IDs on
resume. Resume reconstructs its cache from shared S3 and only observes existing
allocation; it must not create another worker. Record the additional command ID
and result. For a fresh actual interactive SSH smoke check, run
`"${db[@]}" ssh "${spot_names[0]}"`, then `printf 'spot-ssh-ok\n'; exit` in
the remote shell, recording its successful output and local exit.

## H4: remove one by name, the remainder by group; verify roots — PASS

`spot-down-one` started `2026-09-14T13:37:51Z` and returned exit 0,
`teardown_complete`, selected / terminated / cleaned `1/1/1` for generated name
`smoke-batch-i-077169681934e7447`, preserving root
`vol-0cd9b3cc8820562f5` with deletion `deleted`. `spot-after-one` at
`13:38:42Z` showed that exact worker terminated and the second worker still
running under its unchanged name.

`spot-down-group` started `2026-09-14T13:39:34Z` and returned exit 3,
`teardown_partial`, counts `2/2/1`. It terminated the remaining worker
`i-0ac8519de89e56a6b` and verified `vol-062ba16d07d70ea83` deleted.
The already terminated first worker remained visible without a root mapping,
so the group command accurately reported `root_volume_unverified` for that
historical observation. Its earlier exact deletion proof remains in
`spot-down-one.json`.

Independent `spot-terminated.json` shows both original IDs in EC2 state
`terminated`; `spot-after-group.json` also contains no nonterminated group
worker. Exact root queries `spot-root-vol-0cd9b3cc8820562f5` and
`spot-root-vol-062ba16d07d70ea83` each returned
`InvalidVolume.NotFound`, exit 254, proving deletion of both captured roots.
`spot-logs-after-down` returned exit 0 with `verification: verified`; exported
stdout/stderr match the first command's original bytes exactly. H4 passes with
the expected historical-root diagnostic retained rather than hidden.

Observation-only `spot-resume-after-down` at `2026-09-14T13:41:43Z` returned
exit 1, `readiness_failed`, ready 0, historical fulfilled 2 and missing 0. Both
workers were intentionally removed; recovery preserved their fulfillment and
did not offer replacement capacity.

```sh
capture spot-down-one "${db[@]}" down "${spot_names[0]}" --timeout 5m --json
capture spot-after-one "${db[@]}" ls --group smoke-batch --json
capture spot-down-group "${db[@]}" down --group smoke-batch --timeout 5m --json
capture spot-after-group "${db[@]}" ls --group smoke-batch --json
capture spot-terminated "${awsop[@]}" ec2 describe-instances --instance-ids "${spot_ids[@]}"
jq -e '([.Reservations[].Instances[]] | length == 2) and
  all(.Reservations[].Instances[]; .State.Name == "terminated")' "$work/spot-terminated.json"
for root_id in "${spot_roots[@]}"; do
  capture "spot-root-$root_id" "${awsop[@]}" ec2 describe-volumes --volume-ids "$root_id"
done
capture spot-logs-after-down "${db[@]}" logs "$command_one" --stdout-file "$work/one.after-down.stdout" --stderr-file "$work/one.after-down.stderr" --json
cmp "$work/one.stdout" "$work/one.after-down.stdout"
cmp "$work/one.stderr" "$work/one.after-down.stderr"
```

The first down should return `teardown_complete`, exit 0, with selected/terminated/
cleaned counts `1/1/1`, the intended exact ID and its root deletion `deleted`.
The group down must clean the remaining worker and retain its exact root proof.
AWS can still list the first terminated worker without its old root mapping:
group/all selection includes these visible historical IDs. In that case expect
`teardown_partial`, exit 3, with the historical worker's `root_volume_unverified`
and the remaining worker's successful cleanup. If the historical ID has
disappeared, expect `teardown_complete` and `1/1/1`. Preserve both command results;
do not invent deletion proof from the later missing mapping or require a
successful aggregate exit in place of checking each original root.
After the first down, only the other worker remains nonterminated; its name is
unchanged. After group cleanup, no worker in the group remains nonterminated.
For **each** independently queried root require AWS's exact
`InvalidVolume.NotFound` result and record its real nonzero exit. An empty success,
denial, timeout or unrelated error does not prove deletion. Retry read-only
verification if deletion is still pending; preserve unresolved IDs.

The post-down logs export must still verify and match the original bytes with
exit 0. A resume after teardown may report zero ready workers, but historical
fulfilled count must remain 2 and missing count 0; removal never authorizes
replacement capacity.

## H5: explicit On-Demand — PASS after observation-only resume

Direct `ondemand-up` started `2026-09-14T13:47:48Z` with the reviewed startup
build and explicit `--count 1 --group smoke-ondemand --on-demand --timeout 5m`.
The preview lists market `on-demand`, first profile type `c7i.2xlarge`, and all
three approved subnet/AZ choices. Fleet returned a complete one-worker
allocation, but the initial pending observation had no root mapping and
returned `launch_identity_mismatch`. The CLI retained its exact identity with
fulfilled 1, missing `null`, ready 0 and exit 1. That result did not pass H5 and
could not authorize another allocation.

| Request / attempt / Fleet | Allocated worker / name / observed placement | Root / readiness |
| --- | --- | --- |
| `32d4d6ab693b6b27d26094cd3978b644` / `1cee0d3739623fe924836beebb6d99b9` / `fleet-293f5d15-eeae-cc27-a4ba-abaadd579b3d` | `i-0c2ef838a36392a7e` / `smoke-ondemand-i-0c2ef838a36392a7e`; `c7i.2xlarge`, `subnet-0d4c255b7bbbca257`, `us-east-2a` | Root `vol-05f976b59a811c391`; ready through observation-only resume, cleanup recorded in H6 |

`ondemand-aws` at `2026-09-14T13:49:22Z` returned exit 0 and observed that
exact worker running, with no Spot lifecycle field, the expected type, AMI,
AWS template ID/version tags, scope/creation tags and IMDSv2. Its `/dev/sda1`
mapping now contains `vol-05f976b59a811c391`, attached with
delete-on-termination true. `ondemand-roots-before` at
`2026-09-14T13:54:17Z` independently confirms the exact root is encrypted
100-GiB gp3, attached to that worker, with matching scope/creation/attempt tags.
`ondemand-resume-ready` at `13:54:18Z` returned exit 0, requested / fulfilled /
ready `1/1/1`, missing 0, retaining the same request, attempt, Fleet and worker.
The deliberate On-Demand workflow therefore passes after observation-only
resume; the first direct call's failure remains visible.

Freshly reviewed correction `be4107f87cba3659b93d42eb69672354ba46805a`
treats an absent root mapping during EC2 `pending` as incomplete observation,
while preserving failures for conflicting or invalid mappings. Its regressions
and passing `pending-root-check` / `pending-root-build` captures verify the
corrected direct startup behavior without another live allocation. The final
CLI was copied only after H6 cleanup, SHA-256
`8f5447d2c56946e14a57468e18fa2c378bb82db6d1c57ea0ae864e54e847b825`
(`pending-root-binaries.sha256`), with the runner unchanged. H1–H6 live commands
used the earlier builds recorded above. Final inventory uses this final CLI;
there is no claimed direct live `up` success on the final revision.

As with Spot, the recorded initial invocation below was run once. Actual
inspection and root extraction use the ready observation artifact through
`ondemand_result`, preserving the failed `ondemand-up.json` unchanged.

```sh
# Recorded original invocation; do not rerun this allocation to inspect evidence.
capture ondemand-up "${db[@]}" up agent --count 1 --group smoke-ondemand --on-demand --timeout 5m --json
ondemand_request=$(jq -r '.request_id' "$work/ondemand-up.json")
capture ondemand-resume-ready "${db[@]}" up --resume "$ondemand_request" --timeout 5m --json
ondemand_result="$work/ondemand-resume-ready.json"
jq -e '.ok and .status == "ready" and .requested_count == 1 and
  .fulfilled_count == 1 and .ready_count == 1 and .plan.market == "on-demand" and
  (.instances | length == 1) and .instances[0].market == "on-demand"' "$ondemand_result"
ondemand_id=$(jq -r '.instances[0].instance_id' "$ondemand_result")
mapfile -t ondemand_roots < <(jq -r '.instances[].volumes[] | select(.root) | .volume_id' "$ondemand_result")
capture ondemand-aws "${awsop[@]}" ec2 describe-instances --instance-ids "$ondemand_id"
capture ondemand-roots-before "${awsop[@]}" ec2 describe-volumes --volume-ids "${ondemand_roots[@]}"
```

Require preview and result market `on-demand`, one instance of the first profile
type (`c7i.2xlarge`) in an approved placement, no Spot `InstanceLifecycle`, exact
pins/tags and captured disposable root. Record its new request, attempt and Fleet
IDs. This deliberate launch is independent of the completed Spot request.

## H6: decline interactive all, then complete scoped cleanup — PASS

The actual terminal `all-decline` at `2026-09-14T13:54:37Z` received `n` plus
newline and returned exit 0, `confirmation_declined`, selected 3 and zero
terminated/cleaned. The preview contained only the exact account/region/
deployment/owner and three campaign IDs (two already terminated Spot workers
and the live On-Demand worker). `ondemand-after-decline` at `13:54:50Z`
independently showed the On-Demand worker still running. Noninteractive
`all-noninteractive` at `13:54:51Z` returned exit 2, `confirmation_required`,
again with zero terminated/cleaned; `ondemand-after-noninteractive` at
`13:54:52Z` again showed it running.

Authorized `all-cleanup --yes` started `2026-09-14T13:55:14Z` with the same
three frozen IDs and returned exit 3, `teardown_partial`, selected / terminated /
cleaned `3/3/1`. Both Spot workers were `already_terminated` with the expected
historical `root_volume_unverified` diagnostics; their original exact deletion
proof remains in H4. The On-Demand worker was `termination_observed`, with
root `vol-05f976b59a811c391` marked `deleted`.

Independent `ondemand-terminated` at `2026-09-14T13:55:55Z` confirmed exact
worker `i-0c2ef838a36392a7e` terminated. The exact root query
`ondemand-root-vol-05f976b59a811c391` at `13:55:56Z` returned
`InvalidVolume.NotFound`, exit 254. `final-inventory` at `13:55:57Z` contains
only the three historical terminated campaign workers. Raw scoped
`final-aws-inventory` at `13:55:59Z` returned `Reservations: []` for every
nonterminated state; `final-aws-volumes` at `13:56:00Z` returned `Volumes: []`.
Those reads all exited 0. All three captured roots have independent exact
deletion proof; no allocation or worker/root cleanup is unresolved.

Run from a real terminal. The exact account/region/deployment/owner and all
frozen candidate IDs must appear on stderr: the On-Demand worker plus any still
visible terminated Spot workers. Type **`n` and Enter** at the first prompt:

```sh
capture all-decline "${db[@]}" down --all --timeout 5m --json
capture ondemand-after-decline "${awsop[@]}" ec2 describe-instances --instance-ids "$ondemand_id"
```

Require `confirmation_declined`, exit 0, zero terminated/cleaned, and independent
AWS observation that the exact worker remains running. Then cover the
noninteractive gate without supplying consent:

```sh
capture all-noninteractive "${db[@]}" down --all --timeout 5m --json < /dev/null
capture ondemand-after-noninteractive "${awsop[@]}" ec2 describe-instances --instance-ids "$ondemand_id"
```

Require `confirmation_required`, exit 2, and the same still-running worker. Inspect
the final scope preview, then perform the authorized scoped cleanup:

```sh
capture all-cleanup "${db[@]}" down --all --yes --timeout 5m --json
capture ondemand-terminated "${awsop[@]}" ec2 describe-instances --instance-ids "$ondemand_id"
for root_id in "${ondemand_roots[@]}"; do
  capture "ondemand-root-$root_id" "${awsop[@]}" ec2 describe-volumes --volume-ids "$root_id"
done
capture final-inventory "${db[@]}" ls --json
capture final-aws-inventory "${awsop[@]}" ec2 describe-instances --filters \
  Name=tag:ManagedBy,Values=devbox Name=tag:Deployment,Values=personal-dev Name=tag:Owner,Values=joseph \
  Name=instance-state-name,Values=pending,running,stopping,stopped,shutting-down
capture final-aws-volumes "${awsop[@]}" ec2 describe-volumes --filters \
  Name=tag:ManagedBy,Values=devbox Name=tag:Deployment,Values=personal-dev Name=tag:Owner,Values=joseph
```

Require the On-Demand worker's successful cleanup and exact EC2 state
`terminated`, every captured root's `InvalidVolume.NotFound`, no remaining
scoped nonterminated instance and no unexpected scoped volume. If it is the
only candidate, expect `teardown_complete`, exit 0 and counts `1/1/1`. Still-visible
terminated Spot workers can instead produce exit 3 and historical missing-root
diagnostics as described in H4; retain those diagnostics and prove all original
roots independently before accepting cleanup. Other errors remain failures.
Multiple-name
selection and interactive affirmative consent have controlled coverage below;
do not launch extra workers solely to duplicate it. If additional campaign
workers require cleanup, use `down NAME1 NAME2 --timeout 5m --json` with their
actual recorded names and record that live evidence too.

## Recovery and incomplete cleanup

Retain every JSON/stderr result even if its exit is nonzero. Inspect all attempts,
known instances, root mappings and pool errors; use the request ID already
announced before dispatch. The following forms require no launch overrides:

```sh
"${db[@]}" up --resume REQUEST_ID --timeout 5m --json
"${db[@]}" up --retry-missing REQUEST_ID --after ATTEMPT_ID --timeout 5m --json
"${db[@]}" ls --group smoke-batch --json
"${db[@]}" down INSTANCE_ID --timeout 5m --json
```

Substitute recorded IDs. Resume is observation-only. Use retry-missing only for
a fully reconciled, definitively bounded positive remainder, after deciding to
request that remainder. A one-of-two allocation requests **one** successor;
repeating the same `--after` observes that successor. Unknown missing count
(`null`), a crash after the permanent dispatch claim or lost shared response
cannot authorize retry. Empty current inventory is not proof of zero historical
allocation. Changing profile/market/pins requires a separately authorized new
request, never a recovery override. Do not force live scarcity or deliberately
drop live acknowledgments to exercise these controlled invariants.

Expected aggregate exits: ready 0; definitive partial capacity 3; no capacity or
unknown allocation 1; full allocation with only some workers ready 3; invalid
usage/config 2; timeout/interruption 4. No-capacity output must explain explicit
retry or a deliberate new request. No command silently changes Spot to On-Demand.
Teardown partial cleanup returns 3, no verified cleanup 1 and timeout 4; exact
known IDs remain recoverable. A missing instance alone never proves root cleanup.

After any interrupted or partially successful exercise, repeat scoped inventory,
reconcile all known requests and check every captured root independently. Use
exact-ID teardown for surviving managed workers. If termination permissions or
scope verification fail, retain the evidence and pause for a concrete cleanup
decision; do not bypass it with an unreviewed broad AWS termination command.
Unresolved allocation or cleanup keeps #34 and #3 open.

Networking, routing, IAM roles/policies, instance profile, exact templates, SSM
documents, state bucket/history, result/artifact bucket and permanent launch
records are intentionally retained. Worker cleanup never deletes result data or
launch records. Manual cleanup remains necessary until #4. For deliberate full
deployment removal, first verify worker/root cleanup, preserve required results
and state, decide about irreversible record deletion, then use the reviewed
foundation destroy and state-bucket migration/deletion procedure in
[setup teardown](../setup.md#recovery-and-teardown). Do not remove the shared
account Spot service-linked role as part of worker cleanup.

## Final checks and retained records

The final source is `be4107f87cba3659b93d42eb69672354ba46805a`, freshly
approved by `review34_integration` before commit. The final pending-root
regressions are
`TestBatchStartupOnDemandWaitsForRootMappingBeforeProbe` and
`TestVerifyFleetWorkersDistinguishesPendingRootAbsenceFromContradiction`.
They verify convergence from the incomplete pending response while preserving
rejection of contradictory pins. This final correction was checked with
controlled tests; live readiness was established on the existing workers by
observation-only resumes, and no additional worker was allocated for a direct
final-build replay.

| Final source / deployment check | Result and captured start UTC |
| --- | --- |
| `make check` | PASS, `pending-root-check`, `2026-09-14T13:54:31Z`, exit 0 |
| `make build` | PASS, `pending-root-build`, `2026-09-14T13:54:40Z`, exit 0; exact final CLI/unchanged runner hashes in `pending-root-binaries.sha256` |
| Four-package `go test -race ... -count=1` | PASS, `pending-root-race`, `2026-09-14T13:55:32Z`, exit 0: lifecycle, CLI, access and execution |
| Offline infrastructure / modeled IAM | PASS, unchanged infrastructure verified by `final-infra-check`; 23 foundation tests, one bootstrap test and Go export bridge. The reviewed Fleet correction also passed 29 modeled IAM cases. |
| Restricted-operator doctor on final CLI | PASS, `final-doctor`, `2026-09-14T13:56:35Z`, exit 0, all 15 checks pass |
| Worker and volume cleanup | PASS, H4/H6 exact instance/root proofs; final raw scoped nonterminated-instance and volume inventories both empty |
| Permanent shared launch records | PASS, `final-launch-ledger`, `2026-09-14T13:56:34Z`, exit 0; all 11 original objects retained: two request plans and prepared/dispatch/response records for all three attempts |
| Cross-artifact acceptance verification | PASS, `final-evidence-verification.json`; independently checks bounded attempt counts 2/1, ready workers 2/1, both confirmation refusals without mutation, all three exact terminations/root deletions, empty final inventories, passing checks and retained S3 records |

The record prefix remains
`launches/v2/464557813916/us-east-2/personal-dev/joseph/` in
`devbox-results-464557813916-us-east-2-0164521a41ec5a6e`, outside the
result-only 30-day expiration rule. The applied foundation, account Spot role,
state, results and immutable launch history are deliberately retained. Worker
cleanup removed only the three campaign instances and their disposable roots.

## Parent requirement matrix

Existing slice tests below are from implementation revision `81cd077`.
#34 adds `TestMultiDownNamedThenGroupCleanupKeepsHistoricalEvidenceSeparate`,
the live Fleet policy regressions and startup tests in `6ae8502`. Prior passing
evidence is linked above; fresh combined checks and race results are recorded
in the preflight and final-runtime tables.

| Parent deliverable | Responsible implementation and meaningful controlled coverage | Fresh evidence needed |
| --- | --- | --- |
| D1: validated market/type/architecture/disk/placement profiles and multi-AZ network | #28/#29, PRs #35/#36: `TestProfileV2RejectsInvalidOrMissingOptions`, `TestManifestV5RejectsInvalidFoundationAndContradictions`, `TestResolvedLaunchPlanUsesOnlyApprovedCombinations`, `TestBatchDeployedResourcesAndExactPools`; OpenTofu `three_zone_placement`, `root_smaller_than_snapshot` | PASS: reviewed migration/correction, exports, doctor and H1 exact live worker/root pins across permitted pools |
| D2: SDK instant Spot Fleet, price-capacity-optimized instance counts, exact pins | #30, PR #37: `TestFleetSDKSerializedMarketAndOverrides`, `TestFleetSDKRetriesKeepExactDispatch`, `TestVerifyFleetWorkersUsesExactIDsAndActualPins` | PASS after reviewed IAM correction: restricted operator allocated both Spot workers, H1 raw records match the complete response and exact pins |
| D3: count/group, stable generated names, filtering and plural teardown | #28/#32/#33, PRs #35/#39/#40: `TestWorkerNamesAndCreationTagsSurviveOrderAndPartialResults`, `TestGeneratedNamesResolveAfterRestartAndPeerLoss`, `TestTeardownCLIGroupAndMixedTargetsUseFrozenScopedIDs` | PASS: H1/H3/H4/H6 stable generated names, filtering, empty-state access and named/group/all cleanup |
| D4: pre-allocation preview, configurable cap, explicit On-Demand only | #28/#30/#32, PRs #35/#37/#39: `TestMaximumCountConfiguration`, `TestPublicBatchCountCapPrecedesAWSAndProfileReads`, `TestBatchRunPreviewCapacityAndReadinessAreIndependent`, `TestBatchRunFailedPreviewCannotDispatch` | PASS: H1 Spot and H5 explicit On-Demand previews/observed markets; controlled count-cap checks |
| D5: retained partial successes/errors, nonzero outcomes and explicit safe retry | #30/#31/#32, PRs #37/#38/#39: `TestFleetCompletePartialAndZeroPreserveEveryDistinctIdentityAndError`, `TestRecoveryResumeNeverAllocatesAndRetryRequestsOnlyMissing`, `TestRecoveryCopiedClientsCannotRetryTheSameRemainderTwice`, `TestEmitBatchPreservesOneEnvelopeOutcomesAndRecoveryIDs` | PASS controlled failure gates plus live denied-zero result, explicit bounded successor, preserved transient observation failure, H3 shared resume and H4 historical fulfilled 2/missing 0 after removal |
| D6: creation tags and AWS discovery after restart; no atomic name reservation claim | #30/#31/#32, PRs #37/#38/#39: `TestFleetSDKSerializedMarketAndOverrides`, `TestGroupInventoryCloudOnlyPaginationAndIndependentRequests`, `TestGeneratedNameCollisionWithLegacyRetainsCandidates` | PASS: H1 raw instance/root creation tags and H3 unchanged identities discovered without local launch state |
| D7: final termination scope, all confirmation, structured per-resource errors | #33, PR #40: `TestMultiDownFinalExactReadRejectsDrift`, `TestMultiDownFrozenPlanCannotExpandOrBeChangedThroughPreview`, `TestTeardownCLIConfirmationGatesEveryTermination`, `TestMultiDownRootDeletionRequiresExactPositiveEvidence` | PASS: H4/H6 restricted-operator named/group/all cleanup, exact root deletion, scoped terminal/noninteractive confirmation and preserved per-resource diagnostics |

| Human acceptance step | Command/evidence above | Current result |
| --- | --- | --- |
| 1. Two ready Spot workers, resolved types/IDs/group/market | H1 initial `spot-up`, explicit `spot-retry`, `spot-resume-ready`, `spot-text`, raw AWS/root records and verification | **PASS**; same request, two workers, ready 2, missing 0, exact pins/roots verified |
| 2. Different commands and retrieved results | H2 both exec IDs, verified logs and byte comparisons | **PASS**; two different workers/commands and all four exact stream comparisons |
| 3. Restart and rediscover from AWS | H3 empty-state inventory, identity comparison, name/ID access, actual PTY SSH and shared resume | **PASS**; original identities/Fleet/attempts preserved, verified exact-ID execution and remote SSH marker |
| 4. One by name, remainder by group, exact root deletion | H4 both down results, independent instance/root checks and retained logs | **PASS**; both terminated, each original root independently NotFound, retained logs verified; expected historical-root diagnostic preserved |
| 5. Explicit On-Demand worker and displayed market | H5 preview/result, observation-only ready resume and raw AWS lifecycle/type/root pins | **PASS after resume**; initial direct failure retained; final pending-root correction has controlled coverage, not a new direct live allocation |
| 6. Decline all without termination, then scoped cleanup | H6 real terminal decline, raw before/after, noninteractive gate, `--yes` cleanup | **PASS**; terminal decline and noninteractive refusal preserved the live worker; scoped cleanup and exact final root/empty inventories verified |

| Failure/completion gate | Named controlled coverage and expected invariant | Evidence status |
| --- | --- | --- |
| Partial, zero and uncertain response after allocation | `TestFleetCompletePartialAndZeroPreserveEveryDistinctIdentityAndError`, `TestFleetMalformedAndIncompleteOutcomesRemainUnknownWithoutLosingIDs`, `TestAttemptSDKRejectionAfterUncertainRetryRemainsUnknown`, `TestBatchRunPreviewCapacityAndReadinessAreIndependent`: retain IDs/errors; 3/1/1 exits; unknown missing remains null | Prior controlled PASS (#30/#32); fresh combined check PASS; no forced live scarcity |
| No silent full-batch retry; separate clients and lost acknowledgment | `TestRecoveryResumeNeverAllocatesAndRetryRequestsOnlyMissing`, `TestRecoveryCopiedClientsCannotRetryTheSameRemainderTwice`, `TestRecoveryAmbiguousSuccessorRemainsUnknownOnRepeatedRetry`, `TestLaunchLedgerSDKUnsuccessfulClaimNeverAuthorizes`: one-of-two permits exactly one successor; one permanent claim wins | Prior controlled PASS (#31); fresh combined check PASS |
| Invalid/excessive counts, architecture and profile options precede mutation | `TestMaximumCountConfiguration`, `TestProfileV2RejectsInvalidOrMissingOptions`, `TestPublicBatchCountCapPrecedesAWSAndProfileReads`, `TestInvalidBatchConfigurationMakesNoAWSRequests`, `TestLaunchPlanRejectsBeforeAllocation` | Prior controlled PASS (#28/#29/#32); fresh combined check PASS |
| Concurrent duplicate bases and ambiguous friendly names | `TestWorkerNamesAndCreationTagsSurviveOrderAndPartialResults`, `TestGroupInventoryCloudOnlyPaginationAndIndependentRequests`, `TestGeneratedNameCollisionWithLegacyRetainsCandidates`, `TestMultiDownIndependentTargetsDeduplicationAndAmbiguity`: distinct generated IDs; genuine ambiguity requires explicit ID | Prior controlled PASS (#28/#32/#33); fresh combined check PASS |
| Unmanaged, wrong owner/deployment and malicious explicit IDs never enter termination | `TestMultiDownIndependentTargetsDeduplicationAndAmbiguity`, `TestMultiDownFinalExactReadRejectsDrift`, `TestMultiDownIncompleteScopeSelectionAuthorizesNothing`: verify final scope and retain independent target errors | Prior controlled PASS (#33); fresh combined check PASS |
| Noninteractive all without yes fails, affirmative consent is exact and bounded | `TestDownConfirmationNoninteractiveNeverReadsConsent`, `TestDownTerminalDetectionAndPTYConfirmation`, `TestTeardownCLIConfirmationGatesEveryTermination`, `TestDownConfirmationOutputFailureNeverApproves`: no mutation on pipe/EOF/failed prompt; exit 2 | PASS: prior controlled checks and fresh H6 terminal decline / noninteractive exit 2 / scoped consent cleanup |
| Partial roots, timeout or output failure cannot fabricate cleanup | `TestMultiDownRootDeletionRequiresExactPositiveEvidence`, `TestMultiDownMixedTerminationOutcomesPreservePeers`, `TestMultiDownConcurrencyDeadlineAndQueuedWorkers`, `TestTeardownCLIShortOutputRetainsAllCleanupIdentities`; #34 adds `TestMultiDownNamedThenGroupCleanupKeepsHistoricalEvidenceSeparate` for the sequential named/group/all workflow | PASS: prior controlled checks, fresh sequential regression and live H4/H6 conservative historical-root diagnostics with independent exact deletion proof |
| Pending workers wait within the deadline without another allocation | #34 `TestBatchStartupWaitsForExactPinsBeforeReadiness`, `TestBatchStartupTimeoutKeepsReadyPeerAndRoots`, `TestBatchStartupStopsMismatchedWorkerWhilePeerProgresses`, `TestBatchStartupPendingWorkersDoNotStarveVerifiedQueue`, `TestBatchStartupExplicitSuccessorWaitsWithoutAnotherAllocation`, `TestBatchStartupDoesNotEraseOriginalPersistenceFailure`; final missing-root regressions named above | PASS controlled regression/review and final four-package race; live H5 initial failure is retained, existing worker became ready through resume; no direct final-build allocation claimed |
| Retained single-worker access and durable results through upgrade/removal | `TestLocalSSHMasterPreservesRemoteExitStatus`, `TestRunSDKRecoversHistoricalBinaryExportsWithoutRuntimeOrSSM`, `TestRecoverStandaloneHistoricalFinalSeparatesRetrievalFromWorkload`; historical [#2 acceptance](02-exec-logs.md) | PASS: prior controlled/historical checks, fresh pre/post-upgrade metadata reads, H2/H3 access and verified byte exports, H4 verified logs after teardown |
| Relevant Go/OpenTofu checks, actual effective operator permissions and cleanup | Commands above; OpenTofu `spot_policy_boundary`, `permanent_launch_ledger`, `three_az_live_scope_policy_quota`, Go `TestOpenTofuExport` | PASS: final Go/build/four-package race and unchanged offline infrastructure checks; live H1–H6 operator operations, final doctor and independent deletion of all three roots |

## Acceptance and review

All seven parent deliverables, six live workflow steps and required controlled
failure gates have the evidence recorded above. No campaign allocation or root
deletion remains unresolved. Acceptance and fresh review complete; PR #41
carries this record. Review preserves the distinction between successful live
resumes and controlled coverage of the final direct startup fix. Automatic
replacement, market fallback, expiry scheduling, benchmark execution and agent
orchestration remain outside #3.
