# Issue #3 Spot group acceptance and cleanup

**Status: implementation and controlled coverage are merged; fresh live acceptance
is UNRUN. #34 and parent #3 remain open.** This runbook records the remaining
gate without treating mock capacity, policy simulation or historical single-worker
acceptance as a live Spot launch. No infrastructure apply or worker mutation has
been performed for this campaign at the time of this draft.

The implementation revision is `81cd07747226a988169e578d8b3f76da7564b6fa`
(September 14, 2026, US/Central). The normative behavior is in the
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
| #34 fresh acceptance | Pending | This document; all live workflow rows below remain UNRUN |

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
| Planned AMI / template | `ami-00adec9774170bad2` / `lt-0ef8072b1bcc495b1`; new numeric template version pending apply/export |
| Allowed AZs / actual placements | `us-east-2a`, `us-east-2b`, `us-east-2c`; actual worker placements UNRUN |
| Request / attempt / Fleet / worker / root / command IDs | None allocated in this campaign yet; pending the captured results below |

Both setup and restricted-operator STS checks currently authenticate in the
selected account. Read-only scoped EC2 inventory is empty, including historical instances. The
Spot service-linked role is absent (`NoSuchEntity`). These are preflight facts,
not proof of effective allocation permissions. Fresh plan review has passed; the setup
decision to create that role and apply, the v5 export and operator `doctor`
remain required. Standard Spot quota is 32 vCPUs; the planned two 8-vCPU workers
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

## Fresh plan and preflight — awaiting setup decision

On September 14, 2026, prepared the live checkout from exact merged revision
`81cd07747226a988169e578d8b3f76da7564b6fa`, copied the trusted deployment inputs
and initialized its existing S3 backend in the separate data directory above.
No apply, service-linked-role creation, allocation or termination has run.

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
10,144 characters against its 10,240-character limit. Its current quota guard
is deferred because subnet IDs are unknown. Before worker allocation, verify
the actual rendered size/hash, exported numeric template version and new subnet
IDs through the fresh export, `doctor` and the live operations below.

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

Only after the setup decision, execute the reviewed migration and export:

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
inspect its evidence before continuing. Record actual apply/export outcomes and
pins; the block above is currently **UNRUN**.

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

## H1: two Spot workers and their exact identities — UNRUN

```sh
capture spot-up "${db[@]}" up agent --count 2 --group smoke-batch --timeout 5m --json
cat "$work/spot-up.stderr"
jq '{status,requested_count,fulfilled_count,ready_count,missing_count,plan,attempts,instances,errors}' "$work/spot-up.json"
jq -e '.schema_version == 2 and .ok and .status == "ready" and
  .requested_count == 2 and .fulfilled_count == 2 and .ready_count == 2 and
  .missing_count == 0 and (.instances | length == 2) and
  all(.instances[]; .market == "spot" and .group == "smoke-batch" and .readiness == "ready")' "$work/spot-up.json"
mapfile -t spot_names < <(jq -r '.instances | sort_by(.instance_id) | .[].name' "$work/spot-up.json")
mapfile -t spot_ids < <(jq -r '.instances | sort_by(.instance_id) | .[].instance_id' "$work/spot-up.json")
mapfile -t spot_roots < <(jq -r '.instances[].volumes[] | select(.root) | .volume_id' "$work/spot-up.json")
spot_request=$(jq -r '.request_id' "$work/spot-up.json")
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

## H2: independent commands and verified durable results — UNRUN

```sh
capture spot-exec-one "${db[@]}" exec "${spot_names[0]}" --json -- /usr/bin/printf '%s\n' spot-worker-one
capture spot-exec-two "${db[@]}" exec "${spot_names[1]}" --json -- sh -c 'printf "%s\n" spot-worker-two; printf "%s\n" second-stderr >&2'
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

## H3: restart with empty local state; name and ID access — UNRUN

Start new CLI processes with a new, initially empty state directory. This leaves
the original evidence intact and supplies no copied launch or command receipt:

```sh
fresh_state=$(mktemp -d "$work/fresh-state.XXXXXXXX")
test -z "$(ls -A "$fresh_state")"
capture spot-rediscover env XDG_STATE_HOME="$fresh_state" "${db[@]}" ls --group smoke-batch --json
jq -S '[.instances[] | {instance_id,name,group,request_id,attempt_id}] | sort_by(.instance_id)' "$work/spot-up.json" > "$work/original-identities.json"
jq -S '[.instances[] | {instance_id,name,group,request_id,attempt_id}] | sort_by(.instance_id)' "$work/spot-rediscover.json" > "$work/rediscovered-identities.json"
cmp "$work/original-identities.json" "$work/rediscovered-identities.json"
capture spot-ssh-config env XDG_STATE_HOME="$fresh_state" "${db[@]}" ssh-config "${spot_names[0]}"
capture spot-exec-by-id env XDG_STATE_HOME="$fresh_state" "${db[@]}" exec "${spot_ids[1]}" --json -- /usr/bin/printf '%s\n' rediscovered-by-id
capture spot-resume env XDG_STATE_HOME="$fresh_state" "${db[@]}" up --resume "$spot_request" --timeout 5m --json
```

Require unchanged IDs/names and only the requested group, working SSH-config by
generated name and exec by exact ID, and the same request/attempt/worker IDs on
resume. Resume reconstructs its cache from shared S3 and only observes existing
allocation; it must not create another worker. Record the additional command ID
and result. For a fresh actual interactive SSH smoke check, run
`"${db[@]}" ssh "${spot_names[0]}"`, then `printf 'spot-ssh-ok\n'; exit` in
the remote shell, recording its successful output and local exit.

## H4: remove one by name, the remainder by group; verify roots — UNRUN

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

## H5: explicit On-Demand — UNRUN

```sh
capture ondemand-up "${db[@]}" up agent --count 1 --group smoke-ondemand --on-demand --timeout 5m --json
jq -e '.ok and .status == "ready" and .requested_count == 1 and
  .fulfilled_count == 1 and .ready_count == 1 and .plan.market == "on-demand" and
  (.instances | length == 1) and .instances[0].market == "on-demand"' "$work/ondemand-up.json"
ondemand_id=$(jq -r '.instances[0].instance_id' "$work/ondemand-up.json")
mapfile -t ondemand_roots < <(jq -r '.instances[].volumes[] | select(.root) | .volume_id' "$work/ondemand-up.json")
capture ondemand-aws "${awsop[@]}" ec2 describe-instances --instance-ids "$ondemand_id"
capture ondemand-roots-before "${awsop[@]}" ec2 describe-volumes --volume-ids "${ondemand_roots[@]}"
```

Require preview and result market `on-demand`, one instance of the first profile
type (`c7i.2xlarge`) in an approved placement, no Spot `InstanceLifecycle`, exact
pins/tags and captured disposable root. Record its new request, attempt and Fleet
IDs. This deliberate launch is independent of the completed Spot request.

## H6: decline interactive all, then complete scoped cleanup — UNRUN

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

## Parent requirement matrix

Existing test names below are from implementation revision `81cd077`.
`TestMultiDownNamedThenGroupCleanupKeepsHistoricalEvidenceSeparate` is the new
#34 test addition. Prior passing evidence is linked above; the fresh combined
checks and this addition's race test are recorded in the preflight table.

| Parent deliverable | Responsible implementation and meaningful controlled coverage | Fresh evidence needed |
| --- | --- | --- |
| D1: validated market/type/architecture/disk/placement profiles and multi-AZ network | #28/#29, PRs #35/#36: `TestProfileV2RejectsInvalidOrMissingOptions`, `TestManifestV5RejectsInvalidFoundationAndContradictions`, `TestResolvedLaunchPlanUsesOnlyApprovedCombinations`, `TestBatchDeployedResourcesAndExactPools`; OpenTofu `three_zone_placement`, `root_smaller_than_snapshot` | Fresh plan review PASS; apply/export/doctor plus H1 pins and permitted placements — **UNRUN** |
| D2: SDK instant Spot Fleet, price-capacity-optimized instance counts, exact pins | #30, PR #37: `TestFleetSDKSerializedMarketAndOverrides`, `TestFleetSDKRetriesKeepExactDispatch`, `TestVerifyFleetWorkersUsesExactIDsAndActualPins` | Restricted-operator H1 allocation and raw EC2/root records — **UNRUN** |
| D3: count/group, stable generated names, filtering and plural teardown | #28/#32/#33, PRs #35/#39/#40: `TestWorkerNamesAndCreationTagsSurviveOrderAndPartialResults`, `TestGeneratedNamesResolveAfterRestartAndPeerLoss`, `TestTeardownCLIGroupAndMixedTargetsUseFrozenScopedIDs` | H1, H3, H4 and H6 — **UNRUN** |
| D4: pre-allocation preview, configurable cap, explicit On-Demand only | #28/#30/#32, PRs #35/#37/#39: `TestMaximumCountConfiguration`, `TestPublicBatchCountCapPrecedesAWSAndProfileReads`, `TestBatchRunPreviewCapacityAndReadinessAreIndependent`, `TestBatchRunFailedPreviewCannotDispatch` | H1/H5 previews and observed markets — **UNRUN** |
| D5: retained partial successes/errors, nonzero outcomes and explicit safe retry | #30/#31/#32, PRs #37/#38/#39: `TestFleetCompletePartialAndZeroPreserveEveryDistinctIdentityAndError`, `TestRecoveryResumeNeverAllocatesAndRetryRequestsOnlyMissing`, `TestRecoveryCopiedClientsCannotRetryTheSameRemainderTwice`, `TestEmitBatchPreservesOneEnvelopeOutcomesAndRecoveryIDs` | Controlled failure gates below; H3 observation-only live resume and any naturally encountered partial outcome — **UNRUN** |
| D6: creation tags and AWS discovery after restart; no atomic name reservation claim | #30/#31/#32, PRs #37/#38/#39: `TestFleetSDKSerializedMarketAndOverrides`, `TestGroupInventoryCloudOnlyPaginationAndIndependentRequests`, `TestGeneratedNameCollisionWithLegacyRetainsCandidates` | H1 creation tags, H3 empty-state identical identities — **UNRUN** |
| D7: final termination scope, all confirmation, structured per-resource errors | #33, PR #40: `TestMultiDownFinalExactReadRejectsDrift`, `TestMultiDownFrozenPlanCannotExpandOrBeChangedThroughPreview`, `TestTeardownCLIConfirmationGatesEveryTermination`, `TestMultiDownRootDeletionRequiresExactPositiveEvidence` | H4/H6 restricted-operator termination and exact root deletion — **UNRUN** |

| Human acceptance step | Command/evidence above | Current result |
| --- | --- | --- |
| 1. Two ready Spot workers, resolved types/IDs/group/market | H1 `spot-up`, `spot-text`, `spot-aws`, `spot-roots-before` | **UNRUN** |
| 2. Different commands and retrieved results | H2 both exec IDs, verified logs and byte comparisons | **UNRUN** |
| 3. Restart and rediscover from AWS | H3 empty-state inventory, identity comparison, name/ID access and resume | **UNRUN** |
| 4. One by name, remainder by group, exact root deletion | H4 both down results, independent instance/root checks and retained logs | **UNRUN** |
| 5. Explicit On-Demand worker and displayed market | H5 preview/result plus raw AWS lifecycle/type/pins | **UNRUN** |
| 6. Decline all without termination, then scoped cleanup | H6 real terminal decline, raw before/after, noninteractive gate, `--yes` cleanup | **UNRUN** |

| Failure/completion gate | Named controlled coverage and expected invariant | Evidence status |
| --- | --- | --- |
| Partial, zero and uncertain response after allocation | `TestFleetCompletePartialAndZeroPreserveEveryDistinctIdentityAndError`, `TestFleetMalformedAndIncompleteOutcomesRemainUnknownWithoutLosingIDs`, `TestAttemptSDKRejectionAfterUncertainRetryRemainsUnknown`, `TestBatchRunPreviewCapacityAndReadinessAreIndependent`: retain IDs/errors; 3/1/1 exits; unknown missing remains null | Prior controlled PASS (#30/#32); fresh combined check PASS; no forced live scarcity |
| No silent full-batch retry; separate clients and lost acknowledgment | `TestRecoveryResumeNeverAllocatesAndRetryRequestsOnlyMissing`, `TestRecoveryCopiedClientsCannotRetryTheSameRemainderTwice`, `TestRecoveryAmbiguousSuccessorRemainsUnknownOnRepeatedRetry`, `TestLaunchLedgerSDKUnsuccessfulClaimNeverAuthorizes`: one-of-two permits exactly one successor; one permanent claim wins | Prior controlled PASS (#31); fresh combined check PASS |
| Invalid/excessive counts, architecture and profile options precede mutation | `TestMaximumCountConfiguration`, `TestProfileV2RejectsInvalidOrMissingOptions`, `TestPublicBatchCountCapPrecedesAWSAndProfileReads`, `TestInvalidBatchConfigurationMakesNoAWSRequests`, `TestLaunchPlanRejectsBeforeAllocation` | Prior controlled PASS (#28/#29/#32); fresh combined check PASS |
| Concurrent duplicate bases and ambiguous friendly names | `TestWorkerNamesAndCreationTagsSurviveOrderAndPartialResults`, `TestGroupInventoryCloudOnlyPaginationAndIndependentRequests`, `TestGeneratedNameCollisionWithLegacyRetainsCandidates`, `TestMultiDownIndependentTargetsDeduplicationAndAmbiguity`: distinct generated IDs; genuine ambiguity requires explicit ID | Prior controlled PASS (#28/#32/#33); fresh combined check PASS |
| Unmanaged, wrong owner/deployment and malicious explicit IDs never enter termination | `TestMultiDownIndependentTargetsDeduplicationAndAmbiguity`, `TestMultiDownFinalExactReadRejectsDrift`, `TestMultiDownIncompleteScopeSelectionAuthorizesNothing`: verify final scope and retain independent target errors | Prior controlled PASS (#33); fresh combined check PASS |
| Noninteractive all without yes fails, affirmative consent is exact and bounded | `TestDownConfirmationNoninteractiveNeverReadsConsent`, `TestDownTerminalDetectionAndPTYConfirmation`, `TestTeardownCLIConfirmationGatesEveryTermination`, `TestDownConfirmationOutputFailureNeverApproves`: no mutation on pipe/EOF/failed prompt; exit 2 | Prior controlled PASS (#33); fresh H6 pending |
| Partial roots, timeout or output failure cannot fabricate cleanup | `TestMultiDownRootDeletionRequiresExactPositiveEvidence`, `TestMultiDownMixedTerminationOutcomesPreservePeers`, `TestMultiDownConcurrencyDeadlineAndQueuedWorkers`, `TestTeardownCLIShortOutputRetainsAllCleanupIdentities`; #34 adds `TestMultiDownNamedThenGroupCleanupKeepsHistoricalEvidenceSeparate` for the sequential named/group/all workflow | Prior controlled PASS (#33); fresh sequential test PASS; fresh H4/H6 pending |
| Retained single-worker access and durable results through upgrade/removal | `TestLocalSSHMasterPreservesRemoteExitStatus`, `TestRunSDKRecoversHistoricalBinaryExportsWithoutRuntimeOrSSM`, `TestRecoverStandaloneHistoricalFinalSeparatesRetrievalFromWorkload`; historical [#2 acceptance](02-exec-logs.md) | Prior controlled/historical PASS; fresh pre-upgrade retained-result PASS; post-upgrade and H2/H3/H4 checks pending |
| Relevant Go/OpenTofu checks, actual effective operator permissions and cleanup | Commands above; OpenTofu `spot_policy_boundary`, `permanent_launch_ledger`, `three_az_live_scope_policy_quota`, Go `TestOpenTofuExport` | Fresh combined Go and offline infrastructure checks PASS; live apply/doctor/operations and all root checks pending |

## Remaining completion gate

Before closing #34 or #3, replace every live UNRUN row with dated results and
artifact references, record exact pins and all recovery identities, resolve every
unknown allocation and verify every disposable root deletion. Attach the final
check results and fresh independent review of this acceptance work. Link #34's
merged PR and update the parent's seven child/deliverable checklists only after
the associated evidence passes. Automatic replacement, market fallback, expiry
scheduling, benchmark execution and agent orchestration remain outside #3.
