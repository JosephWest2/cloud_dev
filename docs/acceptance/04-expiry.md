# Issue #4 expiry acceptance and recovery (#48)

**Preparation only: live acceptance is pending.** No AWS authentication, API
observation, migration, launch, scheduled cleanup, or laptop-offline result is
claimed by this record. #42–#46 are merged and independently reviewed; #47's
failure route and health implementation must be integrated before final checks
and the reviewed live campaign. Keep #48 and parent #4 open until every live gate
below has actual evidence. Interactive release #5 is a separate gate.

This document is an executable protocol plus an evidence ledger. Commands under
**Planned live protocol** are unrun, not historical results. Controlled fixtures
prove code behavior; they do not prove restricted-role AWS authorization,
scheduler delivery, or cleanup while a laptop is offline.

## Current preparation record

| Item | Actual preparation result |
| --- | --- |
| Preparation baseline | `73076ea`, merged #42–#46; final tested revision pending #47 integration |
| Date | September 15, 2026 UTC (September 14, US/Central) |
| Durable evidence directory | `~/.local/state/devbox/acceptance/04-expiry/20260915T023635Z-4d506132`, mode 0700 |
| Original configuration | User config and referenced manifest copied byte-for-byte to `original-config/`; original paths/hashes recorded privately in `preservation.json`; originals untouched |
| Final test config/export | Pending reviewed migration; never relabel the preserved schema-3 manifest as v6 |
| Local helper verification | Five controlled tests passed; `commands/helper-tests/` records exact command, output, time and exit 0 |
| Tool versions captured | Go `go1.27.1-X:nodwarf5 linux/amd64`; Python `3.14.7`; AWS CLI `2.34.32`; OpenTofu `1.12.6 linux_amd64`; jq `1.8.2` |
| Locked provider | AWS `6.64.0`; final backendless init/validation pending |
| Intermediate integration | Parent reports `make check` and `make build` passing on `73076ea`; this is not the final #48 revision or a live result |
| Final artifacts/tests | Pending final #47 integration and exact revision pinning |
| AWS scope and resources | Prior accepted scope below is a planning input; fresh identity/state/inventory checks remain pending |

Prior accepted scope was account `464557813916`, region `us-east-2`, deployment
`personal-dev`, owner `joseph`, setup profile `devbox-setup`, and restricted
operator `devbox-operator`. Fresh STS must establish the same expected account
before any live work. Do not derive owner from credentials. Previous `/tmp`
acceptance evidence is gone; the [Spot acceptance record](03-spot-groups.md)
provides historical context, not freshly available raw evidence.

Preserve the actual deployed three AZs (`us-east-2a/b/c`), four approved types
(`c7i.2xlarge`, `c7a.2xlarge`, `c6i.2xlarge`, `c6a.2xlarge`),
encrypted disposable 100-GiB gp3 roots, existing image/template pins, networking,
result retention and permanent launch history. Existing local tfvars omit newer
fields and cannot be copied as proof of current deployed settings. Inspect the
current reviewed state/output through setup credentials without printing full
state, credential processes, or private keys.

## Parent requirement and evidence matrix

The named tests below exist in the merged implementation. Their final-revision
rerun remains pending; child verification is linked separately.

| Parent deliverable | Child / meaningful controlled coverage | Required live evidence | Status |
| --- | --- | --- | --- |
| D1 TTL across Spot, On-Demand and batches; visible expiry | #42/#43/#46; `TestTTLGrammarAndPrecedence`, `TestExpiryFleetSingleBatchMarketsActualWireTags`, `TestExpiryRecoveryRetainsOriginalWindowAndHistoricFulfillment`, CLI expiry output tests | Original short/long up JSON, plan/attempt/Fleet IDs, exact EC2 creation tags, `ls` text+JSON | Pending |
| D2 inspectable dry-run/manual cleanup | #44/#45; `TestDryRunNoWritesOrRechecks`, `TestCleanupAdapterSharedFixtures`, `TestCleanupAdapterDecisionsEqualDirectService` | Complete expired dry-run with exactly the short set and future long worker; later harmless manual rerun | Pending |
| D3 Go Lambda, schedule and scoped IAM | #46/#47; actual package/export bridge, `TestCleanupHealthRejectsDrift`, strict Lambda decoder/factory tests | Reviewed saved plan/apply, v6 export, role/code pins, recent scheduled successful summary/end | Pending |
| D4 exact scope, diagnostics and final recheck | #42/#44/#46; `TestEligibilityScopeTagsStatesAndUTC`, `TestFinalRecheckRejectsForgedAndDriftedEvidence`, `TestSDKScopeSerializationAndSingleMutationAttempt` | Restricted operator launch/manual actions; scheduled cleanup role; exact tags/IDs and untouched long worker | Pending |
| D5 retained decisions, termination/invocation failures, health | #44/#46/#47; journal acknowledgment/rejection tests; #47 transport and health tests to be added after merge | Handler events plus independent Scheduler exhaustion/Lambda pre-handler failure records, health failure/repair and later success | Pending |
| D6 repeated/concurrent cleanup, later retry, disposable roots | #44/#48; `TestHappyPathAndRepeat`, `TestTrulyConcurrentRunsHaveIndependentAuthority`, `TestDenialProtectionThrottlingAndLaterScanRecovery`, `TestVolumeEvidenceIsExactAndIndependent`, terminal-history regressions | Harmless rerun, exact terminal states, exact root deletion, final empty campaign set | Pending |

| Human acceptance step | Evidence predicate and planned capture | Status |
| --- | --- | --- |
| H1 provision and recent successful invocation | Saved reviewed migration applied by setup; genuine scheduled `summary` and `invocation_end` both successful, with correlation/request IDs; `doctor` health follows #47 | Pending |
| H2 short/long and batch/market matrix | Two Spot workers in one 20m request and one explicit On-Demand worker with 2h; original request/attempt/Fleet/instance/root/deadline pins | Pending |
| H3 inspect and expired dry-run | `ls` table+JSON; complete no-write dry-run after short deadline and before any termination request, exact short candidates only | Pending |
| H4 actual local client closure/offline interval | User confirms actual offline start/end; AWS schedule request and termination timestamps fall within it; separate API/log observation | Pending |
| H5 retained decision, root deletion, long worker survives | Exact `termination_prepared` mappings and outcomes; separate EC2 state and per-root DescribeVolumes evidence; long worker running before its deadline | Pending |
| H6 harmless rerun and explicit remaining cleanup | Manual cleanup makes no new short-worker termination; exact-ID down of long worker, independent roots and final scope inventory | Pending |

| Failure gate | Coverage and evidence required | Status |
| --- | --- | --- |
| UTC equality, invalid/overflow/long durations | Pure policy tests plus delayed dispatch/final SDK gate tests; label controlled | Final rerun pending |
| Missing/malformed/duplicate expiry, other owner/deployment/account/region, unmanaged records | Pure/service/CLI fixtures; do not create unrelated live resources solely for this | Final rerun pending |
| Incomplete scan, wrong-ID response, scope/expiry drift | Service pagination/recheck and real SDK loopback tests; zero unauthorized sends | Final rerun pending |
| Concurrent invocation, terminal disappearance, exact volume evidence | Service race and terminal-history tests; live benign rerun separately | Pending |
| Temporary termination API failure and later recovery | Controlled `TestDenialProtectionThrottlingAndLaterScanRecovery`, with actionable event and next-scan success; no live throttling claim | Final rerun pending |
| Disabled/failed schedule detectable and repaired | Captured schedule/health failure, exact preserved restore, later scheduled success | Pending live |
| Scheduler retry exhaustion before handler starts | Independent retained failure envelope with actual retry/exhaustion fields, original delivery correlation, no handler start; exact mechanism from reviewed #47/#48 failure protocol | Pending live |
| Lambda async pre-handler failure | Retained OnFailure envelope for a new async event while concurrency is zero, no handler start, exact restore | Pending live |
| Evidence Pipe failure detected and recovered | Captured unhealthy route/backlog, restored transport, the same retained failure records arrive in Logs | Pending live |
| Historical records/results and emergency cleanup survive upgrade | Historical serialization/recovery fixtures; actual old manifest/result read if a still-retained command exists; otherwise document the unavailable live case | Pending |
| Every interrupted/partial launch cleaned | Preserve every known request/Fleet/instance/root; exact observation and explicit recovery even when up fails | Pending live |

References: [expiry contract](../plans/04-expiry-contract.md),
[launch verification](43-launch-expiry.md),
[shared service contract](../plans/04-expiry-cleanup-service.md),
[manual runbook](45-manual-cleanup.md),
[scheduled deployment](46-scheduled-expiry.md). Add the final #47 runbook and
specific test names after its merge; no unavailable health interface is assumed.

## Capture helper and final local verification

`scripts/expiry-acceptance.py` creates private durable runs, captures explicit
argv without a shell, records stdout/stderr/start/end/exit/SHA-256, preserves
partial outputs, refuses reused labels, and prepares complete schedule update
JSON without contacting AWS. Its `identities` command indexes even attempt-only
IDs from supplied JSON; that index is a recovery aid, never termination authority.
The helper stores no environment dump. Never give it credential-source commands,
keys, raw state, or secret-valued command arguments.

From the final integrated acceptance worktree, bind the existing run directory:

```bash
acceptance_repo=$(git rev-parse --show-toplevel)
acceptance_run="$HOME/.local/state/devbox/acceptance/04-expiry/20260915T023635Z-4d506132"
capture() {
  local label=$1
  shift
  python3 "$acceptance_repo/scripts/expiry-acceptance.py" capture \
    --run "$acceptance_run" --label "$label" -- "$@"
}
```

A genuinely new campaign uses `init --revision "$(git rev-parse HEAD)"` and its
printed directory. Do not overwrite prior capture labels on retries; use a new
suffix. A nonzero capture exit remains the underlying command exit. Inspect its
preserved output before deciding the next operation; an up error may still have
allocated workers. Optional `capture --timeout` terminates its process group and
preserves partial output; it never automatically retries a command.

After #47 and any demonstrated integration corrections are committed, capture
all final checks once. Parent owns this final run; do not reuse intermediate
success as final evidence:

```bash
capture final-revision git rev-parse HEAD
capture final-worktree git status --porcelain=v1
capture final-helper-tests python3 scripts/test-expiry-acceptance.py
capture final-make-check make check
capture final-make-build make build
capture final-cleanup-package make cleanup-check
capture final-race go test -race ./internal/expiry ./internal/expirycleanup \
  ./internal/cli ./internal/lifecycle ./internal/cleanuplambda ./internal/foundation -count=1
capture final-diff git diff --check
```

Use a separate clean checkout of that pinned revision for offline infrastructure
checks. Unset live `TF_DATA_DIR`; the two module directories get independent,
backend-disabled data. Disable ambient AWS credential discovery for this check:

```bash
acceptance_revision=$(git rev-parse HEAD)
git worktree add --detach "$acceptance_run/offline-checkout" "$acceptance_revision"
capture final-infra env -u TF_DATA_DIR -u AWS_PROFILE -u AWS_ACCESS_KEY_ID \
  -u AWS_SECRET_ACCESS_KEY -u AWS_SESSION_TOKEN \
  AWS_EC2_METADATA_DISABLED=true AWS_SHARED_CREDENTIALS_FILE=/dev/null \
  AWS_CONFIG_FILE=/dev/null make -C "$acceptance_run/offline-checkout" \
  infra-check TOFU=/tmp/devbox-tools/tofu
```

Pin/copy the tested CLI, runner, cleanup ZIP, helper, exact revision and hashes
under `artifacts/`. `cleanup-check` checks repeated ZIP bytes and content changes;
record its real output, then confirm copied ZIP hex digest equals the applied
manifest and the decoded base64 Lambda CodeSha256. If later code changes, rerun
affected checks and update the tested-revision boundary explicitly.

## Planned live protocol — no steps run yet

### A. Concrete migration and role baseline

1. Parent authenticates setup/operator only when live work is authorized. If
   required authentication is unavailable, the user runs
   `aws login --profile devbox-setup`; never invoke or expose credential_process.
2. Preserve the originals already copied above. Prepare new test config beside
   the actual exported v6 manifest. It keeps exact expected scope, uses
   `devbox-operator`, and may set `default_ttl = "2h"`. Do not hand-edit schema.
3. Inspect current scoped inventory and existing deployment settings before the
   plan. Do not treat old tfvars defaults as intended network/AZ changes. An
   unexpected pre-existing worker needs an ownership/disposition decision;
   campaign-wide `down --all` is not authorized by this protocol.
4. Prepare a saved plan in an isolated live backend directory through setup
   identity. Review exact additions/updates/deletions, function/runner/policy
   hashes and saved-plan hash. Reject unintended networking, existing-worker,
   result-bucket or permanent-ledger destruction. Parent presents this concrete
   reviewed plan plus the three-worker/failure/offline campaign at the user
   checkpoint; this document is not apply authorization.
5. Apply only the unchanged reviewed plan after authorization, export actual v6
   beside the new config, and compare deployed code/policy pins. Follow #47 for
   the complete failure route and health-reader roles. Never substitute a mock
   export, policy simulation or zero-error counter for working authorization.
6. With no campaign workers allocated, enable the reviewed schedule and capture
   a genuine successful scheduled invocation, its summary/end/correlation and
   independent health/API results. This is H1; a manual Lambda invoke is not H1.
   Then disable the same schedule using a complete preserved update and capture
   that disabled health state before launching the timed matrix.

All cloud command results must capture explicit profile/region, request start/end
and exit. Read previous result/ledger records without deleting them. If the old
result ID has expired under its original retention, record that fact and retain
controlled compatibility coverage; do not manufacture a successful historical read.

### B. Minimal worker matrix and original identities

| Request | Market | Count | Explicit TTL | Purpose |
| --- | --- | --- | --- | --- |
| `expiry-short` | Spot | 2 | `20m` | Batch expiry and actual scheduled cleanup |
| `expiry-long` | Explicit On-Demand | 1 | `2h` | Future exclusion, active-work survival before deadline, explicit final down |

Use at most three actual workers. A capacity shortfall is recorded honestly;
resume original requests for observation, and request proven missing capacity
only within their unchanged deadline and the approved count. Never silently
replace an expired request or launch another full batch after partial success.

After the reviewed migration and campaign authorization, the exact CLI syntax is:

```bash
acceptance_cli="$acceptance_run/artifacts/devbox"
acceptance_config="$acceptance_run/config/config.toml"
capture up-short "$acceptance_cli" --config "$acceptance_config" \
  up agent --count 2 --group expiry-short --ttl 20m --json
capture up-long "$acceptance_cli" --config "$acceptance_config" \
  up agent --count 1 --group expiry-long --on-demand --ttl 2h --json
capture ls-text "$acceptance_cli" --config "$acceptance_config" ls
capture ls-json "$acceptance_cli" --config "$acceptance_config" ls --json
python3 scripts/expiry-acceptance.py identities \
  --output "$acceptance_run/observations/initial-identities.json" \
  "$acceptance_run/commands/up-short/stdout" \
  "$acceptance_run/commands/up-long/stdout" \
  "$acceptance_run/commands/ls-json/stdout"
```

Before any later mutation, fill the actual identity table (currently empty):

| Request/market | Original request / attempts / Fleet | Exact worker / root / DeleteOnTermination | Created / expires UTC | Outcome |
| --- | --- | --- | --- | --- |
| Spot batch | Pending | Pending | Pending | Unrun |
| On-Demand | Pending | Pending | Pending | Unrun |

Independently DescribeInstances for **every known exact campaign ID**, retaining
reservation account, all tags, EC2 state, placement, root device and block-device
mappings. Root IDs must be captured while visible, with explicit disposable flags.
Include IDs known only in partial attempts or stderr fallback. If stdout is
truncated, retain it unchanged and reconstruct observations from request recovery,
scoped AWS inventory and saved evidence; do not run JSON indexing as though an
invalid capture were complete. `up --resume REQUEST_ID` only observes; it does not
reset TTL or recreate removed fulfillment.

### C. Expired dry-run and actual offline timing

Keep the schedule disabled through the first expired dry-run. Disabling a
schedule does not retract work already accepted by Scheduler or Lambda. Drain
prior deliveries before short expiry: budgets can span Scheduler 300s + Lambda
300s + active runtime 180s. The 20m short TTL leaves room beyond that conservative
13m chain after disabling; retain observed invocation history and inspect any
late activity. A queue or active invocation is not harmless merely because the
schedule configuration says disabled.

After the original short deadline, capture another `ls` text/JSON and:

```bash
capture expired-dry-run "$acceptance_cli" --config "$acceptance_config" \
  cleanup --dry-run --json
```

Required assertions: `dry_run=true`, `scan_complete=true`, `complete=true`,
exactly the two short worker IDs as `eligible=true` / `would_terminate`, each
reason `expired` with its original deadline/root mapping, long worker
`expiry_future` and running, zero terminated/cleaned counts, no errors. Compare
sets explicitly; a count of two alone is not proof. Any unrelated eligible
pre-existing worker prevents this campaign from authorizing a broader cleanup.
Capture the frozen comparison and its source hashes under `observations/`.

Prepare a future first scheduled occurrence, allowing time to review/acknowledge
the user disconnect. AWS documents StartDate for the first rate occurrence and
60-second scheduling precision. Updating a schedule resets omitted optional
fields, so always capture GetSchedule and preserve every mutable setting.
[StartDate/precision](https://docs.aws.amazon.com/scheduler/latest/UserGuide/schedule-types.html),
[complete update semantics](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_UpdateSchedule.html).

The helper copies exact target ARN, role, literal input, DLQ, retry policy,
flexible window, timezone, KMS and other returned mutable fields. It removes only
read-only metadata and changes state/start date. It refuses unknown fields or an
activation less than two minutes in the future. **Preparation sends nothing.**

```bash
# Read-only capture; bind names from the actual v6 export.
acceptance_schedule=$(jq -r '.cleanup.schedule.name' "$acceptance_run/config/deployment.json")
acceptance_group=$(jq -r '.cleanup.schedule.group_name' "$acceptance_run/config/deployment.json")
capture schedule-before-offline aws scheduler get-schedule \
  --name "$acceptance_schedule" --group-name "$acceptance_group" \
  --profile devbox-setup --region us-east-2 --output json --no-cli-pager

# Set this to the concrete reviewed UTC boundary; do not copy a sample date.
# acceptance_first_tick=YYYY-MM-DDTHH:MM:SSZ
python3 scripts/expiry-acceptance.py prepare-schedule \
  "$acceptance_run/commands/schedule-before-offline/stdout" \
  "$acceptance_run/migration/schedule-offline-start.json" \
  --state ENABLED --start-date "$acceptance_first_tick"
```

Parent reviews the exact update JSON and start time before the authorized
`aws scheduler update-schedule --cli-input-json file://...` call. Immediately
capture GetSchedule again to verify every preserved setting and the accepted
boundary. Record update acknowledgment and first eligible occurrence separately.
This temporary StartDate is controlled drift and must later be removed/restored
through the reviewed intended configuration, with the full target preserved.

Aim to finish dry-run soon after expiry and set the first occurrence around
expiry +4m, with at least two minutes left for user disconnect. If that timing
cannot be met, choose another reviewed future boundary; keep the original TTL
and report deliberate disabled delay separately. Healthy operation's roughly
nine-minute expiry-to-request planning target does not apply to an intentionally
disabled interval. Report actual expiry, re-enable, first eligible occurrence,
request, terminal observation and root-deletion times, without hiding delays.

**Human checkpoint before the first occurrence:** parent provides the exact
short/root/long IDs, original deadlines, accepted first occurrence, expected
window, durable recovery path, and a return time at least 15 minutes after that
occurrence. The user closes local devbox/observer processes and actually takes
the laptop offline, then reports actual disconnect/reconnect times. No local
manual cleanup or manual Lambda invoke may substitute for the scheduled event.
If the user misses the boundary, record the attempt as inconclusive and prepare
a truthful follow-up; do not infer offline status from process exit.

### D. Independent observations and healthy completion

Use a separate AWS CLI/API observer or retrieve independent retained AWS
observations after the user returns. Preserve raw outputs and query bounds.
The user-reported offline interval must contain actual AWS scheduled cleanup
activity for the exact short workers. A Lambda-start log alone is insufficient.

Capture at least:

- Full handler-log events covering pre-dispatch mappings, decisions, request IDs,
  original scope/expiry, termination outcomes and successful summary/end.
  Decode log `message` JSON; retain the original CloudWatch event timestamp and
  the event's own UTC timestamp. Match the exact `correlation.schedule_arn`,
  `scheduled_time`, `execution_id` and Lambda request ID. Do not equate delivery
  acceptance with handler completion.
- Exact DescribeInstances for both short IDs showing terminated, plus independent
  DescribeVolumes for **each known exact root ID**. Explicit NotFound (with
  captured exit/error) or exact verified deleted state is proof; empty success,
  missing instance or missing mapping alone is not. Do not combine all roots
  into a single NotFound call and infer which one was absent.
- Exact long-worker observation showing running and future expiry during the
  interval. Verify no long-worker termination_prepared/request event occurred.
- Recent scheduled successful summary/end and the final #47 health output.
  Record service/code/health mismatch as failure rather than assuming success
  from counters or schedule configuration.

For example, once an exact observed volume ID is bound:

```bash
capture short-root-one-after aws ec2 describe-volumes \
  --volume-ids "$acceptance_short_root_one" --profile devbox-operator \
  --region us-east-2 --output json --no-cli-pager
```

An expected NotFound makes that capture nonzero; preserve it as evidence and
review its exact code/ID. Never auto-retry termination or create a replacement
because an observation failed.

### E. Failure-route campaign and restoration

Run the independently reviewed #47/#48 failure protocol after H4/H5, with no
short workers left to lose. Bind its exact schedule/function/queue/Pipe/role
identities from the applied v6 manifest; capture complete before/after settings.
A temporary InvokeFunction permission denial can prove a failed delivery but
must not be called retry exhaustion without actual retryable-error/exhaustion
fields. The [failure-route protocol](04-expiry-failures.md) supplies concrete commands,
finite observation windows, full-setting restoration requirements and separate
pass predicates. Final #47 manifest/API verification and concrete saved recovery
artifacts still precede its live use; this gate is not waived.

Required cases are Scheduler retry exhaustion without handler startup, a new
Lambda async event sent directly to OnFailure with reserved concurrency zero,
and stopped/broken evidence transport with detectable backlog followed by the
same records arriving after repair. Restore concurrency, schedule target/input,
DLQ/retries, Pipe state and all other modified settings before declaring success.
Drain intentional failures and capture a later genuine successful scheduled run.
No extra worker launch is required for these infrastructure failure cases.
For a simpler recovery boundary, complete exact teardown of all three campaign
workers first, then run failure injection with an empty eligible scope. If keeping
the long worker during the failure campaign, its original deadline must remain
future throughout; otherwise explicitly remove it before introducing drift.

### F. Harmless rerun, exact cleanup and retained infrastructure

After independent short/root observations, invoke manual `cleanup --json` using
the restricted operator and preserve stdout plus stderr events. It must not send
another termination for already-terminal short workers; missing historical root
mappings may be a benign skip but must not increase cleaned counts. The future
long worker remains skipped. The actual invocation still performs a new scoped
scan/recheck; dry-run never grants stale authorization.

Manually remove the exact long worker with `down INSTANCE_ID --timeout 5m --json`.
For any partial/failed earlier launch, recover and remove every remaining known
campaign ID explicitly. Independently DescribeInstances and DescribeVolumes per
exact root, then capture final scoped `ls` and raw tagged instance/volume inventory.
An empty scoped inventory supplements, and does not replace, per-ID deletion proof.

Retain the intended enabled foundation/schedule, handler/failure Logs under their
configured retention, failure queue/Pipe/alarms, durable results under original
retention and permanent launch ledger with no expiry. Record exact retained
resource names and final healthy configuration. Worker `down` does not remove
these durable resources. Any later full foundation teardown requires its own
reviewed plan, result-lifetime decision and explicit action; retained Logs may
remain intentionally after destroy. Never broadly delete EBS, S3 results or
permanent launch records as acceptance cleanup.

## Final completion ledger

| Gate | Evidence / result |
| --- | --- |
| Final revision and all required local/infra/race checks | Pending |
| Concrete migration, independent review, authorization, exact apply/export | Pending |
| Restricted operator Spot/batch and explicit On-Demand creation | Pending |
| Recent genuine scheduled success and exact expired dry-run | Pending |
| User-confirmed actual offline interval plus independent AWS events | Pending |
| Exact short root deletion and running long worker | Pending |
| Disabled/failure/exhaustion/pre-handler/Pipe detection, repair and later health | Pending |
| Harmless cleanup rerun and exact remaining worker/root removal | Pending |
| Final scope inventory and retained durable resources | Pending |
| Fresh independent review of final #48 evidence and implementation | Pending |

No idle detector, checkpointing, cost estimate, automatic replacement or TTL
extension is introduced here. Complete these gates before closing #4; hand off
to interactive MVP release #5 without claiming its separate acceptance passed.
