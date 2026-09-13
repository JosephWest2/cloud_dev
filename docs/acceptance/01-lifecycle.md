# MVP 1 clean-checkout lifecycle acceptance

Issue #10 is the completion gate for parent #1. The procedure below is for a
selected test account; the dated results at the end distinguish this final run,
controlled tests and earlier live evidence. **Gate: passed September 13, 2026 UTC
(September 12 US/Central).** Live acceptance, controlled failures, final offline
checks and independent PR review are complete. No user test or decision remains.

## Selected decisions and prerequisites

- Local Linux (Arch initially); Go 1.24+, Git, Make, OpenSSH, AWS CLI v2, `jq`,
  OpenTofu 1.12.6 and the committed AWS provider 6.64.0 lockfiles.
- Session Manager plugin >=1.2.764.0 with logging disabled. Bootstrap requires
  SSM Agent >=3.3.40.0 on Canonical Ubuntu 24.04 LTS amd64.
- Ohio (`us-east-2`), a dedicated VPC/public subnet, public IPv4, outbound TCP
  80/443 and **zero inbound rules on every attached security group**.
- One explicit account, deployment and stable owner; separate setup and restricted
  operator profiles. This deployment uses `devbox-setup` and `devbox-operator`.
  Preserve the selected scope across reinstalls. Actual account/backend/key inputs
  stay local and are never replaced with example values on an existing deployment.
- Real SSH over SSM as the sudo-capable `devbox` user, using a dedicated local
  Ed25519 identity and public-key-only sshd. The fixed, authenticated SSM probe
  supplies the host key; OpenSSH checks it strictly. This supersedes the parent's
  original native-SSM-shell proposal and supports editors/scp/sftp.
- Explicit `--on-demand` for acceptance. The embedded `agent` profile retains its
  intended Spot default; Spot, general remote exec/logs, agent automation and TTL
  cleanup are outside MVP 1.

Read [README installation/configuration](../../README.md),
[current decisions](../../design-decisions.md) and [setup](../setup.md).
Create/load the [dedicated SSH key](09-readiness-shell.md#configure-the-dedicated-key)
before provisioning. Private keys and temporary AWS credentials never enter
TOML, OpenTofu, manifests, request receipts, Git or published evidence.

## Clean checkout, state and configuration

Use separate fresh checkouts for live setup and offline infrastructure checks.
In the offline checkout, before adding any local deployment/backend files:

```sh
git rev-parse HEAD
git status --porcelain                 # Expect no output.
go version
make check
make build
/path/to/tofu version                  # Must be 1.12.6.
make infra-check TOFU=/path/to/tofu
```

`make check` runs Go formatting, module integrity, build, vet and tests.
`make infra-check` runs formatting, provider validation, mock-provider tests and
actual manifest-export contract tests. It uses `init -backend=false`; do not run
it in the checkout initialized against live S3 state. Record command exit codes,
versions, the tested commit and failures/skips honestly. These checks are offline.

In the live checkout, follow [setup steps 1–4](../setup.md#1-bootstrap-local-state-then-migrate-it):
bootstrap the encrypted/versioned S3 bucket from local state, migrate bootstrap
state, select the exact Canonical AMI, initialize the distinct foundation state
key, review saved plans, apply and export **only** `deployment_manifest`.
Native S3 `.tflock` locking is enabled; no DynamoDB table is used. Verify migration
and locking as documented in [foundation acceptance](07-foundation.md#backend-inspection).

For an existing accepted deployment, copy only its locally protected inputs and
backend configuration into the fresh live checkout. Initialize both roots with
`tofu init -input=false -lockfile=readonly -backend-config=backend.hcl`; bootstrap
also needs its generated `backend.tf` from the committed example. Do not copy
`.terraform`, local state, receipts or a preexisting manifest. Run `tofu validate`
and `tofu plan -detailed-exitcode -var-file=... -out=acceptance.tfplan` in each root
under the explicit setup profile. Exit 0 means no changes; 2 means review the
saved changes before apply; 1 is an error. An empty plan needs no apply. Record
previous creation/migration/apply evidence as historical, not rerun. Recover
state and stale locks only through the [documented recovery procedure](../setup.md#recovery-and-teardown).

After setup, run these from the live checkout root in Bash:

```sh
umask 077
export DEVBOX_ACCEPTANCE_DIR="$(mktemp -d /tmp/devbox-acceptance.XXXXXX)"
mkdir -p "$DEVBOX_ACCEPTANCE_DIR/config" "$DEVBOX_ACCEPTANCE_DIR/state" \
  "$DEVBOX_ACCEPTANCE_DIR/evidence" "$DEVBOX_ACCEPTANCE_DIR/install"
cp examples/config.toml "$DEVBOX_ACCEPTANCE_DIR/config/config.toml"
# Edit the copied config: selected account/deployment/owner, us-east-2,
# aws_profile="devbox-operator", manifest="deployment.json", and the absolute
# ssh_identity_file path. Use the same identity public key as foundation inputs.
AWS_PROFILE=devbox-setup /path/to/tofu -chdir=infra/foundation \
  output -json deployment_manifest > "$DEVBOX_ACCEPTANCE_DIR/config/deployment.json"
make build
GOBIN="$DEVBOX_ACCEPTANCE_DIR/install" make install
export DEVBOX_ACCEPTANCE_BIN="$DEVBOX_ACCEPTANCE_DIR/install/devbox"
db() {
  XDG_STATE_HOME="$DEVBOX_ACCEPTANCE_DIR/state" "$DEVBOX_ACCEPTANCE_BIN" \
    --config "$DEVBOX_ACCEPTANCE_DIR/config/config.toml" \
    --aws-profile devbox-operator "$@"
}
db doctor --timeout 60s --json > "$DEVBOX_ACCEPTANCE_DIR/evidence/doctor.json"
db ls --json > "$DEVBOX_ACCEPTANCE_DIR/evidence/initial-ls.json"
```

All 12 doctor checks must pass. Verify setup/operator STS identities match the
configured account, and that initial inventory contains no active acceptance
worker. Stop on unexpected inventory or drift. Explicit `--aws-profile` keeps an
ambient setup `AWS_PROFILE` from selecting the setup identity for devbox.
An encrypted SSH key must already be available through `ssh-agent`/`ssh-add`;
access setup is noninteractive. Do not overwrite the user's normal devbox config.

## Launch, shell and rediscovery

These commands allocate a billable worker. Keep this terminal and the acceptance
directory until cleanup is verified. No automatic cleanup exists; a timeout,
shell exit or terminal closure leaves the worker allocated.

```sh
db up agent --on-demand --name smoke --timeout 5m --json \
  > "$DEVBOX_ACCEPTANCE_DIR/evidence/up.json"
cat "$DEVBOX_ACCEPTANCE_DIR/evidence/up.json"
db ls
db ls --json > "$DEVBOX_ACCEPTANCE_DIR/evidence/ls.json"
```

For successful up expect exit 0, `ok=true`, exact image/template/version, actual
`market=on-demand`, and an instance with `ec2_state=running`, `ssm=online`,
`bootstrap=complete`, `readiness=ready`. Progress/receipt announcements go to stderr;
JSON stdout is one envelope. Capture the instance/request IDs even if up fails.
**Never issue a new up to retry an uncertain launch.** Use the recovery section.

```sh
export DEVBOX_INSTANCE_ID="$(jq -er '.instances[0].instance_id' "$DEVBOX_ACCEPTANCE_DIR/evidence/up.json")"
export DEVBOX_REQUEST_ID="$(jq -er '.request_id' "$DEVBOX_ACCEPTANCE_DIR/evidence/up.json")"
export DEVBOX_ROOT_VOLUME_ID="$(jq -er --arg id "$DEVBOX_INSTANCE_ID" '.instances[] | select(.instance_id == $id) | .volumes[] | select(.root) | .volume_id' "$DEVBOX_ACCEPTANCE_DIR/evidence/ls.json")"
db ssh smoke
```

In the remote shell run `uname -a` and `whoami` (expect Linux/x86_64 and `devbox`).
Run `sleep 60`, interrupt it with Ctrl-C, and verify the shell remains usable.
Resize and run `stty size`, then `exit 0`. Check the local terminal works normally.
SSH setup has a bounded deadline; the authenticated session can outlive it.
Ctrl-D/exit closes the shell, with the SSH exit status preserved. A bare exit
following Ctrl-C can return 130 (`remote_exit`); 255 reports an SSH failure.
`ssh`/`ssm-proxy` reject `--json`; `ssh-config --json` provides metadata separately.
See [transfer/editor acceptance](09-readiness-shell.md#launch-observe-connect-and-transfer)
for generated configuration, scp/sftp and editor checks. Do not claim an editor
check from a shell test. This final gate may cite the earlier actual user-confirmed
editor test while identifying it as historical.

Start a new CLI process from another directory with **empty local state**:

```sh
mkdir "$DEVBOX_ACCEPTANCE_DIR/rediscovered-state"
(
  cd /tmp
  XDG_STATE_HOME="$DEVBOX_ACCEPTANCE_DIR/rediscovered-state" \
    "$DEVBOX_ACCEPTANCE_BIN" --config "$DEVBOX_ACCEPTANCE_DIR/config/config.toml" \
    --aws-profile devbox-operator ls --json
) > "$DEVBOX_ACCEPTANCE_DIR/evidence/rediscovered.json"
```

Expect the same instance/request/image/type/market in AWS inventory. No receipt,
SSH config or cached instance ID is copied to the new state directory.
Recently terminated workers can still appear; compare by the exact acceptance
instance ID rather than assuming inventory contains only one record.

Use the operator profile and exact ID to inspect EC2 metadata and all attached
security groups; record root encryption/deletion flags and all seven creation tags:
`ManagedBy`, `Deployment`, `Owner`, `Profile`, `Name`, `RequestId`, `CreatedAt`.

```sh
aws ec2 describe-instances --profile devbox-operator --region us-east-2 \
  --instance-ids "$DEVBOX_INSTANCE_ID" \
  --query 'Reservations[].Instances[].{ID:InstanceId,State:State.Name,Image:ImageId,Type:InstanceType,Market:InstanceLifecycle,Metadata:MetadataOptions,Groups:SecurityGroups,Root:RootDeviceName,Blocks:BlockDeviceMappings,Tags:Tags}' \
  > "$DEVBOX_ACCEPTANCE_DIR/evidence/ec2-worker.json"
aws ec2 describe-volumes --profile devbox-operator --region us-east-2 \
  --volume-ids "$DEVBOX_ROOT_VOLUME_ID" \
  --query 'Volumes[].{ID:VolumeId,Encrypted:Encrypted,State:State,Size:Size,Type:VolumeType,Attachments:Attachments}' \
  > "$DEVBOX_ACCEPTANCE_DIR/evidence/root-before.json"
# Repeat for EVERY GroupId returned by describe-instances.
aws ec2 describe-security-groups --profile devbox-operator --region us-east-2 \
  --group-ids GROUP_ID --query 'SecurityGroups[].{ID:GroupId,Ingress:IpPermissions}'
```

Every group's `Ingress` must be `[]`. EC2 omits `InstanceLifecycle` for On-Demand;
the devbox result must explicitly report `on-demand`. The template requires
IMDSv2 and encrypted gp3 root with `DeleteOnTermination=true`. Keep the saved
root-volume ID even after EC2 drops its block-device mappings.

## Failures and recovery

Controlled SDK responses cover risky/rare failures without creating unsafe
resources or changing the working IAM/bootstrap. Run the existing named tests
in the evidence matrix below; their result is controlled evidence, not live
AWS fault injection. Invalid config must stop before even STS; failed identity
must stop before resource calls/allocation. Expired credentials should instruct
refresh, and a wrong account should report `account_mismatch` without secrets.
Use temporary config copies for optional real invalid-input checks; never alter
the working config or deliberately revoke/expire active test credentials.

For safe replay and a forced **readiness-phase** timeout, use the original
successful launch receipt in the original state/config/scope:

```sh
db up --resume "$DEVBOX_REQUEST_ID" --timeout 5m --json \
  > "$DEVBOX_ACCEPTANCE_DIR/evidence/resume.json"
db up --resume "$DEVBOX_REQUEST_ID" --timeout 1s --json \
  > "$DEVBOX_ACCEPTANCE_DIR/evidence/timeout.json"
printf 'timeout exit: %s\n' "$?"
cat "$DEVBOX_ACCEPTANCE_DIR/evidence/timeout.json"
db ls --json > "$DEVBOX_ACCEPTANCE_DIR/evidence/after-timeout.json"
```

The successful replay must find the same instance and allocate no replacement.
For timeout, require exit 4, `operation_timeout`, retained request/instance/volume
IDs, and per-instance `observation_code=observation_timeout` or equivalent proof
that `WaitReady` was entered after allocation reconciliation. This diagnostic
can also arise during readiness-document verification or scope revalidation;
it does not prove an SSM status probe was dispatched. A timeout in credential lookup/reconciliation
without that evidence does **not** pass. If needed, retry only this same receipt
with a bounded 2s/3s budget; a success is recorded as success, not timeout. If none
produces a qualifying readiness-phase timeout, keep that gate blocked and use the
controlled timeout test as separate evidence. Do not allocate another request.

A lost launch response is reconciled with `up --resume REQUEST_ID` before any
new allocation. Dispatched receipts never issue another RunInstances request;
a crash immediately before dispatch can therefore remain unresolved. Keep the
receipt and inspect AWS by scope/request ID until the outcome is known.
Losing a receipt does not prevent `ls` or ID-based `down`.

Failed bootstrap blocks SSH. A denied/unavailable/malformed probe means unknown,
not a fabricated bootstrap failure. Inspect the saved fixed command's status with
`aws ssm get-command-invocation --command-id COMMAND_ID --instance-id INSTANCE_ID
--profile devbox-operator --region us-east-2 --query
'{Status:Status,ResponseCode:ResponseCode,Document:DocumentName,Version:DocumentVersion}'`.
Fix the foundation and replace the disposable worker after cleanup. No arbitrary
remote exec/logging interface or unready-shell bypass is part of this gate.

## Teardown and independent cleanup verification

Use the exact instance ID retained by timeout/inspection:

```sh
db down "$DEVBOX_INSTANCE_ID" --timeout 5m --json \
  > "$DEVBOX_ACCEPTANCE_DIR/evidence/down.json"
db down smoke --timeout 5m --json \
  > "$DEVBOX_ACCEPTANCE_DIR/evidence/down-again.json"
aws ec2 describe-instances --profile devbox-operator --region us-east-2 \
  --instance-ids "$DEVBOX_INSTANCE_ID" \
  --query 'Reservations[].Instances[].{ID:InstanceId,State:State.Name}'
aws ec2 describe-volumes --profile devbox-operator --region us-east-2 \
  --volume-ids "$DEVBOX_ROOT_VOLUME_ID"
db ls --json > "$DEVBOX_ACCEPTANCE_DIR/evidence/final-ls.json"
```

First down must observe EC2 `terminated` and root deletion `deleted`. The
independent exact-volume query must return `InvalidVolume.NotFound`; an empty
instance lookup alone does not prove deletion. Repeated down returns
`already_terminated` or `no_managed_match` after AWS expires inventory. A missing
mapping on repeated down may report deletion `unavailable`; preserve the first
verification. `no_managed_match` does not verify a particular termination.
See AWS's [termination and EBS deletion behavior](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/terminating-instances.html).

Also use EC2 and EBS inventory filtered by `ManagedBy=devbox`, `Deployment` and
`Owner` from the selected config. Require no nonterminated instances and no
remaining tagged worker volumes; check every captured root ID independently.
Record any intentionally retained worker/volume and keep cleanup incomplete.

```sh
export DEVBOX_DEPLOYMENT="$(jq -er '.deployment' "$DEVBOX_ACCEPTANCE_DIR/config/deployment.json")"
export DEVBOX_OWNER="$(jq -er '.owner' "$DEVBOX_ACCEPTANCE_DIR/config/deployment.json")"
aws ec2 describe-instances --profile devbox-operator --region us-east-2 \
  --filters Name=tag:ManagedBy,Values=devbox \
    "Name=tag:Deployment,Values=$DEVBOX_DEPLOYMENT" "Name=tag:Owner,Values=$DEVBOX_OWNER" \
  --query 'Reservations[].Instances[].{ID:InstanceId,State:State.Name}' \
  > "$DEVBOX_ACCEPTANCE_DIR/evidence/final-ec2.json"
aws ec2 describe-volumes --profile devbox-operator --region us-east-2 \
  --filters Name=tag:ManagedBy,Values=devbox \
    "Name=tag:Deployment,Values=$DEVBOX_DEPLOYMENT" "Name=tag:Owner,Values=$DEVBOX_OWNER" \
  --query 'Volumes[].{ID:VolumeId,State:State,Attachments:Attachments}' \
  > "$DEVBOX_ACCEPTANCE_DIR/evidence/final-ebs.json"
```

On test failure, remove the worker anyway. If down times out, retry the same ID.
If credentials expire, retain all IDs, refresh the selected source profile
(`aws login --profile devbox-setup` for this deployment, or `aws sso login` for an
SSO source), then retry ID-based down and verification. If the CLI cannot clean
up, verify STS/account/tags and use the restricted operator's
`aws ec2 terminate-instances --profile devbox-operator --region us-east-2
--instance-ids INSTANCE_ID`, followed by the same observations. Do not delete an
unexpected remaining volume until its exact ownership/attachments are inspected.
If authentication/termination still fails, pause with the IDs and commands;
cleanup is incomplete and the acceptance gate stays open.

Worker cleanup intentionally retains VPC/subnet/Internet gateway/routes, security
group, instance/operator IAM roles and inline policies, instance profile, launch
template/versions, fixed SSM readiness document, and the encrypted/versioned S3
state bucket and history. S3 storage/requests remain billable. There is no retained
worker, EBS volume, EIP, NAT gateway or paid endpoint in the intended end state.
Use [manual durable teardown](../setup.md#recovery-and-teardown) when those resources
are no longer wanted. Do not destroy networking/IAM under running workers or
remove the state bucket before migrating both state roots and handling history.

## Requirement and evidence matrix

Results are populated only after execution. Controlled test names below refer to
existing tests, run by `make check`; no duplicate tests are needed for doc changes.

| Requirement | Evidence source | Final result |
| --- | --- | --- |
| CLI/config/profile/doctor | Fresh install/config/manifest and operator doctor; config/doctor tests | Passed; see dated results |
| Durable infra/state migration/locking | Historical #7 creation/migration/real lock contention; fresh backend init and both live plans | Passed; see dated results |
| Exact AMI/template, market, tags, IMDSv2 and encrypted/deleted root | Fresh up/ls, EC2/EBS/template API inspection | Passed; see dated results |
| Readiness and SSH `uname -a`/`whoami` | Fresh up plus real SSH terminal | Passed; see dated results |
| Editor/file transfer selected access contract | Historical #9 user-confirmed VS Code and real scp/sftp | Passed September 12–13 UTC |
| Restart without local inventory | Fresh process, different cwd and empty XDG state | Passed; see dated results |
| Termination, root deletion and repeated teardown | Fresh down and exact EC2/EBS verification; final scoped inventory | Passed; see dated results |
| No inbound rules | Every attached security group's `IpPermissions` empty | Passed; see dated results |
| Expired credentials/wrong account | `TestIdentityFailuresAreActionableAndRedacted` classifies ExpiredToken/account mismatch; `TestRunInvalidConfigManifestIdentityPreventLaunch` proves failed service identity prevents launch | Passed (controlled) |
| Invalid config before AWS | `TestRunInvalidConfigManifestIdentityPreventLaunch`, `TestLifecycleUsageFailsBeforeAWS` | Passed (controlled) |
| Readiness-phase timeout with recoverable IDs | Fresh original-request resume timeout and ID cleanup; `TestBoundedReadinessAndCommandIdentity`, `TestLifecycleJSONAndTimeoutIdentifiers` | Passed; see dated results |
| Safe SDK retry/replay, lost response and crash gap | `TestSDKLaunchRetryKeepsTokenAndParameters`, `TestLostLaunchResponseAndRestartReplay`, `TestUncertainLaunchNeverReallocates`, `TestDispatchCrashGapRemainsUnresolved`; fresh same-request resume | Passed; see dated results |
| Ambiguous names and unmanaged/out-of-scope termination refusal | `TestDownRejectsUnsafeTargets` covers both names/IDs, scope change before mutation and zero terminate calls; `TestRequestAndConcurrentNameConflictsPreserveIDs` | Passed (controlled) |
| Go format/build/vet/test; OpenTofu format/validate | Separate clean offline checkout, `make check`, `make build`, `make infra-check` | Passed; final PR checks below |

## Dated final results

Implementation planning: September 13, 2026 UTC (September 12 US/Central).
Baseline `61895cb8bc4c9ec25aba095e0c58dfbe61d3f864` includes merged #6–#9.
The [plan](../plans/10-lifecycle-acceptance.md) was reviewed by a GPT-6 Astra
subagent at high reasoning effort before implementation.

Two new local clones began with empty `git status --porcelain` at the baseline
revision. The installed binary ran from `/tmp`, with newly constructed TOML,
freshly exported manifest and initially empty state; no local instance inventory
or AWS Console was used. This issue changes documentation only, so the runtime,
tests, dependency pins and infrastructure are identical to that tested revision.
Protected command outputs and local inputs remain outside Git; a local index is
in `infra/foundation/acceptance-output/10/context.json` in the issue worktree.
Public non-secret identities/pins and results are recorded below.

| Selected version | Observed value |
| --- | --- |
| Local platform | Arch Linux, linux/amd64 |
| Go | `go1.27.0-X:nodwarf5 linux/amd64`; module minimum 1.24.0 |
| OpenTofu / AWS provider | 1.12.6 / 6.64.0, committed lockfiles unchanged |
| AWS CLI | 2.34.32 |
| OpenSSH | 10.5p1, OpenSSL 3.6.4 |
| Session Manager plugin | 1.2.835.0 |
| Remote | Ubuntu 24.04.4 LTS, `7.0.0-1012-aws`, x86_64 |

### Live setup and worker results (02:22–02:40 UTC)

- Reused the previously selected `devbox-setup` and restricted `devbox-operator`
  profiles in Ohio. Both STS identities matched the configured test account;
  manifest/config deployment and owner matched. The fresh state directory had
  zero receipts. Initial inventory had zero active workers and zero scoped EBS
  volumes; the prior #9 terminated smoke record was still visible.
- Fresh S3 backend initialization and validation succeeded in both roots. Saved
  plans returned exit 0/no changes: six bootstrap resources and thirteen foundation
  resources, with unchanged outputs. **No new apply or state migration was run**;
  there were no changes to apply. Historical #7 creation, state migration, S3
  protections and real lock contention, plus #9 foundation updates, remain the
  corresponding actual live evidence. No infrastructure or IAM was changed here.
- The first foundation plan attempt stopped because an older #7 local input copy
  lacked `ssh_public_key`. Recovered the current #9 inputs, checked that every
  prior input was unchanged and the public key matched the local identity, then
  reran the successful no-change plan. This was an input-recovery finding, not a
  successful first plan or a new key decision.
- Exported the actual schema-v3 manifest from S3-backed foundation output. All 12
  doctor checks passed using the operator profile. The fresh installed binary
  has SHA-256 `30daa46fdcb0059f9480d0dcf2c803cf4136e795ee4153a055a683f25a8613a7`.

| Worker observation | Result |
| --- | --- |
| Creation time | `2026-09-13T02:33:54.307562305Z` |
| Request | `eb40a87b50528480c12eb2de760a05f6` |
| Instance | `i-057f138790bb4d1bc`, name `smoke`, profile `agent` |
| Image | `ami-00adec9774170bad2`, Canonical owner `099720109477`, `ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-20260904` |
| Launch template | `lt-0ef8072b1bcc495b1`, numeric version `2` |
| Readiness document | `devbox-personal-dev-joseph-readiness`, numeric version `2` |
| Type / actual market | `c7i.2xlarge` / `on-demand` |
| Root | `/dev/sda1`, `vol-0f836b0904d2d5ade`, encrypted 100 GiB gp3, delete-on-termination true |
| Security | EC2 `HttpTokens=required`; all seven required tags matched; every attached group's ingress was `[]` |

- Up returned exit 0 / `ready`, with independent progress from pending/unregistered,
  through running/online/bootstrap pending, to bootstrap complete. Initial up
  still lacked EBS mappings and reported deletion `unavailable`; subsequent ls
  and EC2/EBS queries captured the exact root above. No deletion was inferred from
  the allocation response.
- JSON inventory and a new process in a different working directory with an
  empty second state directory found the same active instance/request/pins/market.
  The second state directory stayed empty. A local evidence assertion initially
  assumed one total record; the historical terminated #9 record demonstrated
  that selection must use the exact ID. The runbook now does so.
- The actual installed `devbox ssh smoke` was exercised through a controlling PTY.
  Typed `uname -a` reported Linux `7.0.0-1012-aws` x86_64; `whoami` returned `devbox`.
  Ctrl-C interrupted `sleep 60`; the next typed command printed `INTERRUPT_OK`.
  A local resize changed remote `stty size` from `24 80` to `37 101`. `exit 0`
  returned status 0. This is a new real SSH check. SCP/SFTP and the user's VS Code
  save/reconnect confirmation are explicitly the historical #9 evidence, not
  newly repeated editor tests.
- A normal resume found the same ready instance, exit 0, with no replacement.
  A one-second resume then reconciled that allocation and expired within
  `WaitReady`, returning exit 4 / `operation_timeout`, `observation_timeout`, and
  the original request/instance/root-volume IDs. Output does **not** establish
  that the SSM status-observation loop or probe dispatch began: timeout may have
  occurred during readiness-document verification or readiness-phase scope
  revalidation. No exact failing API or pending-bootstrap timeout is claimed.
  The plan reviewer confirmed this satisfies the required readiness-phase timeout;
  the controlled tests separately exercise an in-flight probe/loop timeout.
- Post-timeout ls found the worker still running. `down` by its retained exact ID
  returned exit 0 / `terminated`, with EC2 terminated and root deletion `deleted`.
  Independent describe-instances confirmed termination. Describe-volumes for the
  exact saved root returned exit 254 / `InvalidVolume.NotFound` (the expected AWS
  CLI error result establishing absence). Repeated `down smoke` returned exit 0 /
  `already_terminated`; absent mappings then reported deletion unavailable without
  contradicting the first observed deletion.
- Final independent tag-scoped EC2 inventory had **zero nonterminated instances**;
  EBS inventory had **zero volumes**, and final devbox inventory agreed. Cleanup
  was complete at `2026-09-13T02:39:41Z`. Only historical terminated records could
  remain visible. The durable foundation and state/history listed above are
  intentionally retained. No acceptance worker or root volume remains.

### Controlled failures and offline checks

- `make check`, `make build`, and `GOBIN=... make install` passed from the clean
  baseline checkout. `make infra-check TOFU=/tmp/devbox-tools/tofu` passed from the
  separate offline checkout: both validations, one bootstrap mock test, all four
  foundation mock tests and the actual exported-manifest contract test passed.
  The live checkout's initialized S3 backend was never used for mock tests.
- `go test -count=1 -json ./...` additionally recorded the named failure cases:
  200 test/subtest pass events, zero failures. Two direct suite skips are explicit:
  `TestReceiptLockHelper` is a subprocess helper exercised by its parent test;
  `TestOpenTofuExport` is run and passed by `make infra-check` with its required
  export input. Real local OpenSSH/PTY fixtures and the installed-plugin test
  passed, not skipped. No tests were added merely to mirror documentation.
- ExpiredToken and account-mismatch classifications/redaction, invalid config and
  failed identity before launch, safe SDK retry and unchanged token/parameters,
  lost-response replay, unresolved crash gap, ambiguous names, and unmanaged/
  owner/deployment/account/scope-change refusal all passed the existing named
  controlled cases in the matrix. These are **controlled** results; credentials
  were not deliberately expired and unsafe AWS workers were not created.
- Documentation checks verified local link targets, named test references, Bash
  syntax of command blocks and `git diff --check`. Final PR checks and review are
  recorded below.

### PR completion

At 03:01–03:03 UTC, final `make check`, `make build` and
`make infra-check TOFU=/tmp/devbox-tools/tofu` all returned exit 0 in another clean
clone at `5772a31fe41920c35500d7d8c42fdb05344da80e`. Its tracked worktree remained
clean after the checks. The repository has no GitHub status checks configured;
these are recorded local checks, not an invented CI run.

A fresh agent, separate from the Astra high plan reviewer, reviewed the actual
[PR #15](https://github.com/JosephWest2/cloud_dev/pull/15) diff against issues #10/#1,
implementation, named tests and private live/offline evidence. It found no
actionable findings or missing user test/decision. Final documentation edits
record these results and make the README lifecycle examples explicitly select
the operator profile so a setup `AWS_PROFILE` cannot override TOML. Runtime,
infrastructure, tests and dependency pins remain identical to the checked source.

The parent MVP 1 acceptance requirements are substantiated by the matrix and
dated results above. The acceptance worker and root volume are removed, and the
retained durable resources and manual teardown are documented. There are no
remaining acceptance blockers.
