# Issue #4 expiry acceptance and recovery (#48)

**Revised #48/#4 acceptance is satisfied; PR #55 merged and #48/#4 closed on
September 16, 2026.** This status update adds no new live test claim. The retained
campaign evidence below remains unchanged. On September 16, 2026, the user approved deferring the
one literal live Scheduler retry-exhaustion test to
[#58](https://github.com/JosephWest2/cloud_dev/issues/58). Installation,
market/batch checks, the authorized retry's actual offline cleanup, and all five
workers' exact root cleanup passed. Cases A/B/C retained their distinct failure
and recovery evidence; all fault campaigns were restored and final normal health
passed in the retained observations through 04:59 UTC. The original offline
attempt remains **NOT_PERFORMED**.

**Literal live Scheduler retry exhaustion remains UNPROVED and deferred to #58.**
Both A2 mechanisms were unsupported, and Case A recorded permanent denial with
zero retries. The deferral changes acceptance scope; it does not turn those results
or controlled fixtures into a live pass. The separate interactive release #5
remains open; expiry acceptance alone does not complete its release handoff.

All evidence paths below are relative to the private durable run in the table.
Controlled tests, scheduled online cleanup and actual laptop-offline proof remain
distinct. The execution recipe below is retained for context; completed launches
must not be replayed as new requests.

## Live health discovery and correction

After installation and enablement, genuine scheduled completion and alarms passed,
but doctor rejected `EnrichmentParameters: {}` returned by AWS for an otherwise
correct Pipe with no enrichment ARN. The pinned SDK allocates a nonnil struct for
that empty object. Revision `d3967f3` accepts empty parameters/removal templates
while retaining rejection of enrichment ARNs, nonempty templates and HTTP blocks.
Ten actual SDK wire cases, `make check`, foundation race tests and CLI build passed;
the empty-object regression fails against the preceding implementation.

Independent review approved the exact fix and rebuilt identical CLI bytes.
`commands/h1-doctor-fixed-01/` then passed **24/24 checks**; `migration/h1-proof.json`
binds genuine scheduled completion and the successful rerun. The original failure
and raw route captures remain under `commands/h1-doctor-01/` and
`commands/h1-route-installed-{pipe,queue,failure-streams}/`. The separately pinned
CLI is `artifacts/devbox-d3967f3`, SHA-256
`88d0125e6257cc865b856cd8c583c02c308fcef7a375ef53a07f20409ea025db`.
Lambda, runner and infrastructure were unchanged by this client fix.

## Revision, deployment and preservation record

| Item | Actual result |
| --- | --- |
| Original tested build | `30ab07614cf29b2389f2c997c7035b909a166cc2`; production Go/infra bytes match reviewed main `14bb326c1dcabef92e4a645554da17aee1d7f819` |
| Evidence interval | September 15–16, 2026 UTC |
| Durable evidence directory | `~/.local/state/devbox/acceptance/04-expiry/20260915T023635Z-4d506132`, mode 0700 |
| Original configuration | User config and referenced manifest copied byte-for-byte to `original-config/`; original paths/hashes recorded privately in `preservation.json`; originals untouched |
| Actual exports | Installed schema 6/template 6: `commands/deployment-after/`; enabled export: `commands/deployment-enabled/`; original schema-3 files untouched |
| Initial helper verification | Five controlled tests passed; `commands/helper-tests/` preserves the original preparation result |
| Protocol revision `7a830dc` | 14 controlled helper tests plus shell/document syntax passed; `commands/lifecycle-review-fix-tests/` pins exact tested scripts |
| Tool versions captured | Go `go1.27.1-X:nodwarf5 linux/amd64`; Python `3.14.7`; AWS CLI `2.34.32`; OpenTofu `1.12.6 linux_amd64`; jq `1.8.2` |
| Locked provider | AWS `6.64.0`; backendless init/validation and all infrastructure tests passed at `30ab076` |
| Durable OpenTofu | Parent preserved `tools/tofu-1.12.6` under the run with hash/provenance; use it for migration/recovery after `/tmp` loss |
| Intermediate integration | Parent reports `make check` and `make build` passing on `73076ea`; this is not the final #48 revision or a live result |
| CLI correction `d3967f3` | Full `make check`, foundation race, ten SDK wire cases and CLI build passed; `commands/pipe-empty-enrichment-*`; independent review under `reviews/48-h1-client-fix/` |
| Deployment and IAM | Approved install applied 34 additions/3 changes/1 deletion; 904 actual configuration predicates passed; health policy 3352 bytes and operator policy 9635 bytes |
| Quota | Explicitly approved Ohio Lambda concurrency increase became effective at 1000 before installation |
| Separate enablement | Independently reviewed plan applied 0 additions/1 change/0 deletions, only schedule DISABLED→ENABLED; complete canary restoration/disarm preceded it |

Verified scope is account `464557813916`, region `us-east-2`, deployment
`personal-dev`, owner `joseph`, setup profile `devbox-setup`, and restricted
operator `devbox-operator`. Fresh identity checks continue to bind every live phase to that account. Do not derive owner from credentials. Previous `/tmp`
acceptance evidence is gone; the [Spot acceptance record](03-spot-groups.md)
provides historical context, not freshly available raw evidence.

Installation preserved the actual deployed three AZs (`us-east-2a/b/c`), four approved types
(`c7i.2xlarge`, `c7a.2xlarge`, `c6i.2xlarge`, `c6a.2xlarge`),
encrypted disposable 100-GiB gp3 roots, existing image/template pins, networking,
result retention and permanent launch history. The sole deleted object was the
superseded content-addressed runner; its original bytes were preserved. Genuine
installed export SHA-256 is `23bddb683b14c366eaeb2fd18ea116d077989104fa7906f2d103c50251f1064b`;
enabled export is `29f2e6f99e61b8f96cf07f26965d50e674543f15a548d445811120ad15bffe03`.
Actual preservation/configuration proofs are in `migration/installed-verification-*`.

## Actual live outcomes and current retry

| Evidence | Result and source |
| --- | --- |
| Historical results | Same retained command `dc1-00f0dcad46d8e9d1abfee9aaabb25d6f` retrieved before/after installation with verified, identical stdout/stderr; original workload remains execution_timeout/signal 15. `commands/historical-logs-v5-export-{before,after}/` |
| Worker-free Case B | `async-prehandler-3da38173ee2e`: actual 202 acceptance, concurrency-zero readbacks, retained ZeroReservedConcurrency/count 0 envelope through Pipe, drained queue. AsyncEventsDropped Sum 1; other metric responses had no datapoints, not measured zeros. `failure-20260915T230122Z-58580a09/` |
| Case B recovery | Exact parked settings/concurrency 1/Pipe RUNNING restored; 604-second recovery finished 23:16:12Z September 15, traps disarmed. `finished.json`, `finish.completed.utc`, independent enablement review |
| Original matrix | Two Spot workers TTL20m and one On-Demand TTL2h ready; initial/future/expired reports passed 73/85/97 predicates. `observations/worker-matrix-*-01.json` |
| Original scheduled cleanup | `i-02e3ceb89391f52ab` was Lambda-cleaned within `[00:05:06.474536042Z, 00:06:02.878139384Z]` September 16; exact root NotFound independently captured |
| Original Spot interruption | `i-0d01e901e6238121d` was service-reclaimed at 00:03:21Z, code Server.SpotInstanceTermination. Its disappearance/root deletion is **not** attributed to Lambda |
| Original offline result | **NOT_PERFORMED**: user stayed online. Scheduled online proof and harmless manual rerun are retained in `observations/missed-offline-actual-proof.json`; they do not satisfy the physical-offline gate |
| Original final resources | The original long worker expired and was Lambda-cleaned at the 01:40Z tick. All three original workers and their individually pinned roots are gone. `observations/original-campaign-final-cleanup.json` |
| Original live manual down | **NOT_RUN**: the original long worker expired naturally before that step; a later down of an absent worker would not satisfy it |

### Authorized retry: two fresh On-Demand workers

The user explicitly approved two new workers after the original future control
was gone. Retry evidence is isolated under `retry2-20260916T023800Z-02eaa155/`;
original captures/deadlines and the NOT_PERFORMED record remain unchanged.
Both count-one requests use `agent`, On-Demand and encrypted disposable 100-GiB
gp3 roots. No automatic replacement or TTL extension is authorized.

| Role | Request | Instance | Exact root | Original expiry (UTC, September 16) |
| --- | --- | --- | --- | --- |
| Short, 20m | `cde1a8edea68153c33eb3c3e1a4c7b0a` | `i-004179009e625a5aa` | `vol-0b9405add4ad5cbe9` | `03:07:53.634971114Z` |
| Future control, 2h | `3d282c919b75688a6cdbebd48ced1b84` | `i-089036f46ef2d5705` | `vol-04debe75ceb6c318d` | `04:49:13.583693561Z` |

Normal enabled schedule reconciliation and doctor 24/24 passed before retry
launch. The full schedule was parked with exact readback at 02:47:37Z. Initial,
future and expired verification passed **63/73/82 predicates**, with immutable
request/attempt/Fleet/root pins. Prior-delivery drain passed **202 predicates**;
guarded activation acknowledged and read back the first eligible tick at 03:14Z.

The user confirmed physical offline participation from **10:12–10:35 PM CDT on
September 15**, with minute precision. The conservative definitely-offline interval
is **03:13–03:35 UTC on September 16**. Retained invocation
`aa6aaa09-78ca-45b2-8746-2bd65e23ab46` prepared the exact short worker at
`03:14:06.403084418Z` and observed termination and its exact root deletion at
`03:14:33.991416149Z`. This **prepared-to-observed operation bracket** lies wholly
inside that interval; it is not an exact API dispatch timestamp. Summary and end
Results agree. Four later ticks had no new candidates or termination preparations.
The deliberate schedule pause is not evidence of ordinary expiry-to-tick latency.

Retry `observations/offline-cleanup-proof-01.json` passed **487 predicates** and
pins all source hashes, separate Scheduler/Lambda/service correlations, complete
logs and individual return reads. Its SHA-256 is
`254b118beab257a155e8ab7fcb074c223be649fb148cb578a214bbf32602be94`.
The returned short worker was terminated and its single-ID root read returned
`InvalidVolume.NotFound`; the control was still running with its unchanged future
deadline and original in-use root.

The existing 45-minute control task completed successfully at `03:35:42.890Z`
(`commands/long-work-ssm-returned-01/`). The retained logs response records workload
exit 0 and complete publication, with verification **not_downloaded**; this is not
stream-byte verification (`commands/long-work-logs-returned-01/`).

The restricted manual cleanup rerun completed with zero candidates and no new
termination at `03:38:51Z`. Its terminal count of one describes the already-cleaned
short worker. Explicit exact-ID down of the still-live, future control then
completed at `03:39:45Z`, before its original deadline. Independent final reads
confirmed the control terminated, its exact root `InvalidVolume.NotFound`, and the
active managed deployment/owner inventory empty. Sources are retry
`commands/manual-cleanup-rerun-01/`, `control-down-01/`, `control-instance-final/`,
`control-root-final/` and `final-active-scope/`.

All original and retry workers/roots were verified cleaned. Normal schedule settings
without the temporary StartDate were restored. All executed fault campaigns were
restored and final health passed. Literal live Scheduler retry exhaustion remains
**UNPROVED**, deferred by the user to #58; final independent acceptance/merge review
remains pending. No new worker batch is required.

### A2 unsupported target and completed recovery

On September 16 at `03:54:21Z`, AWS rejected the proposed universal Lambda
`Invoke` target with `InvocationType=RequestResponse` during `UpdateSchedule`.
The actual `ValidationException` requires asynchronous `Event` invocation.
This disproves the assumed integration support; **no Scheduler retry exhaustion
was demonstrated**. The retained rejection is
`commands/exhaustion-f5dceb2c89ff-arm-once-476b61b7/`; case status and recovery are
under `failure-20260916T035130Z-de6b048a/`.

The shell's EXIT recovery completed its full 600-second guard and restored the
exact parked schedule, original concurrency and Pipe by `04:04:25Z`. The shell
then closed, preserving the rejection exit 254. Subsequent independent review
verified the restored settings before the exact normal schedule was reapplied at
`04:06:14Z`. Complete readback shows ENABLED, `rate(5 minutes)`, the original
Lambda target/retry/DLQ settings and no temporary StartDate. Doctor passed **24/24**
at `04:11:40Z` (`commands/a2-rejected-normal-{update,readback,doctor-01}/`).
This is recovered intermediate health, not final acceptance after all fault tests.
The [failure protocol](04-expiry-failures.md#case-a2-unsupported-synchronous-target-exhaustion-unproved)
records the unsupported mechanism and remaining evidence requirement.

A subsequent capability probe at `04:13:53Z` was also rejected: AWS reports that
`invokeWithResponseStream` is not a valid Scheduler `aws-sdk:lambda` API.
`failure-20260916T041206Z-09027586/cases/stream-capability-61b43c070827/`
retains the exact rejection and readbacks. The complete original schedule remained
DISABLED, reserved concurrency remained 1, and no occurrence/invocation was
requested. This is unsupported capability evidence, not a second exhaustion test
or a reason to change production code/infrastructure. Normal settings were
reapplied at `04:15:09Z`; doctor passed 24/24 at `04:15:27Z`
(`commands/stream-rejected-normal-{update,doctor-01}/`). The subsequent genuine
scheduled invocation `636aaa17-cd77-48a3-acd1-b833fb7f3557` completed successfully at
`04:15:50.189898647Z`, with matching successful summary/end Results in
`failure-20260916T041545Z-ae5c055f/cases/baseline-107f5cbd7dbe/normal-handler.raw.json`.
This establishes the healthy baseline for the separate Case C campaign below.


### Case C: stopped evidence consumer, retained original failure

Actual `pipe-stopped-f5f816f3ad5b` passed **177 verification predicates**. The
healthy, empty baseline was preserved, the exact Pipe reached STOPPED, and one
accepted asynchronous invocation generated a new failure. Queue visible backlog
became 1, its visible-message alarm reached ALARM, and restricted doctor reported
`cleanup_evidence_route` failure. The case was absent from failure Logs before
restart; the fixed stream subsequently retained the original case payload,
SQS message `fe7d2b99-3f68-4d62-9afa-519c5f476159`, and Lambda destination request
`e6ce9715-1c18-4703-9792-079941cb92ea`.

The actual failure timestamp `04:18:04.154Z` and SQS SentTimestamp
`04:18:04.182Z` precede the second-precision restart marker `04:21:56Z`.
CloudWatch retained that message at `04:22:11.843Z`; its actual condition is
`ZeroReservedConcurrency`, approximate invoke count 0. The full bounded handler
query contains no case-correlated invocation, and the recovered queue has zero
visible/not-visible/delayed messages. Source hashes, raw event metadata and honest
timestamp precision are in
`failure-20260916T041545Z-ae5c055f/cases/pipe-stopped-f5f816f3ad5b/transport-proof-01.json`.
The local verifier also passed five controlled positive/negative timestamp,
correlation and query-window checks; these are parser checks, not additional live
failure tests. This evidence does not satisfy Scheduler retry exhaustion.

**Case C full normal recovery also passed.** The complete 600-second guard finished
and recovery was disarmed. Exact original configuration pins were read back, and
the original enabled schedule without StartDate was restored at `04:34:16Z`.
Scheduled request `9c6aaa1c-4860-408b-b89c-b433ff8958d3` completed successfully at
`04:34:57.448022126Z`; all 16 alarms were OK and `commands/c-normal-doctor-01/`
passed 24/24 at `04:35:59Z`. Source hashes and full recovery references are in
`failure-20260916T041545Z-ae5c055f/normal-recovery-proof.json`. Case A's separate
scoped permission-denial result and final restored health follow.


### Case A: permanent denied delivery retained; exhaustion unproved

The one-time `04:43:00Z` occurrence targeted the original cleanup function with
its original input, retry2/age300 and DLQ settings. The sole policy change denied
InvokeFunction on that exact function, preserving Scheduler trust and queue-send
permission. Actual retained message `9894803a-1426-4a9a-9128-a61c4d127eee` at
`04:43:05.438Z` binds Scheduler execution
`b66aaa1e-54eb-4e63-803f-3051df4623c0` across its message attributes and nested
original payload. AWS's body is the Lambda API request wrapper: exact FunctionName,
InvocationType `Event`, and JSON-string Payload. The local case ID is not the
Scheduler execution ID.

The actual error is `AccessDeniedException`, naming the exact Scheduler identity,
function and explicit identity-policy Deny. Payload truncation/invalid flags are
false, **RETRY_ATTEMPTS is 0 and EXHAUSTED_RETRY_CONDITION is absent**. The complete
bounded handler query contains no matching invocation. Thus **permanent failed-
delivery retention passed; literal retry exhaustion remains UNPROVED**.
`failure-20260916T043635Z-65620005/cases/denied-delivery-88667b852400/delivery-proof-03.json`
passed 111 predicates and retains raw records/source hashes. Ten controlled
wrapper/correlation/classification checks also passed; they are not live retries.
Doctor detected the deliberate drift (`commands/a-denied-doctor-01/`).

Full guarded Case A recovery and final normal health passed as recorded below.
The user-approved deferral to #58 requires no further AWS actions for #48/#4
acceptance. Successful denied-delivery retention does not prove retry exhaustion.

A narrowly scoped **controlled** export-bridge fixture now exercises positive
Scheduler retry/exhaustion attributes through the actual rendered Pipe template,
alongside permanent denial and Lambda async variants. It checks complete body,
attributes and correlation preservation, including RETRY_ATTEMPTS2 and
EXHAUSTED_RETRY_CONDITION `MaximumRetryAttempts`. Five isolated OpenTofu mock tests
and the non-skipped Go `TestOpenTofuExport` bridge passed; all three envelope
variants passed for both rendered exports. Captures are
`commands/case-a-controlled-{tofu-export,export-bridge}/`; the exact working test
file hash and offline isolation are pinned in
`migration/controlled-exhausted-envelope-proof.json`. A focused bridge rerun after
making the fixture attempt counters internally consistent is captured under
`commands/case-a-controlled-export-bridge-attempt-consistency/` and pinned in
`migration/controlled-exhausted-envelope-attempt-fidelity.json`. The independent
body maps use attempt1 for denial and a constructed attempt3/two-retry example for
exhaustion; no undocumented DLQ attempt-selection rule is asserted. Only test
fixtures/assertions changed. This proves field preservation under controlled substitution, **not**
AWS delivery retry classification or actual exhausted retries; the live test is
still **UNPROVED**, deferred to #58.


### Final restoration, health and retained inventory

These are the retained September 16 observations through 04:59 UTC, not a new
health measurement at the time of the acceptance amendment.

All executed fault campaigns finished or recovered. The last campaign completed
the full 600-second guard, disarmed recovery and closed its shell. Exact original
policy/trust/function/async/queue/Pipe/scope pins were verified; concurrency is 1,
the Pipe is RUNNING and all queue counts are zero. The complete original enabled
five-minute schedule, without temporary StartDate, was restored at `04:56:07Z`.
Genuine scheduled request `4e6aaa21-67c7-4298-8178-3bc9c3eba5f7` completed at
`04:56:48.627369576Z`, with equal successful summary/end Results, zero candidates
and zero newly cleaned workers. All 16 alarms were OK and restricted doctor
passed 24/24 (`commands/a-normal-doctor-01/`).

The final plan exited 0 with no resource or output changes. Its SHA-256 is
`11ad7d8a4d352e7ef6d64d42769c31f30b30fee47915916a32d550a8f0ecffb0`.
All five campaign workers and exact roots remain cleaned; active managed scope
is empty. **59 managed infrastructure resources are intentionally retained**:
foundation networking/IAM, runner/results/permanent launch-history storage,
scheduled cleanup, and its logs/evidence queue/Pipe/alarms. Exact resource addresses,
source hashes and cleanup references are in `migration/final-retained-inventory.json`
and `migration/final-after-faults-proof.json`; the final campaign also retains
`failure-20260916T043635Z-65620005/normal-recovery-proof.json`. The teardown section
below explains their separate lifecycle.

Earlier independent evidence reviews approved the offline run, Case C recovery
and actual Case A denied-delivery retention. They do not replace author-independent
review of this final amended candidate. The user approved the sole scope change on
September 16: defer literal live Scheduler retry exhaustion to #58. Revised #48/#4
acceptance is satisfied subject only to that final independent acceptance/merge
review. The deferred test remains **UNPROVED**; controlled fixtures and permanent
zero-retry denial do not satisfy it. No merge, issue closure or further AWS probe
is claimed by this record.

## Parent requirement and evidence matrix

The named tests below passed with the integrated production implementation at
`30ab076`; actual live statuses below include the separately tested CLI correction.
Child verification is linked separately.

| Parent deliverable | Child / meaningful controlled coverage | Required live evidence | Status |
| --- | --- | --- | --- |
| D1 TTL across Spot, On-Demand and batches; visible expiry | #42/#43/#46; `TestTTLGrammarAndPrecedence`, `TestExpiryFleetSingleBatchMarketsActualWireTags`, `TestExpiryRecoveryRetainsOriginalWindowAndHistoricFulfillment`, CLI expiry output tests | Original short/long up JSON, plan/attempt/Fleet IDs, exact EC2 creation tags, `ls` text+JSON | Passed original and retry matrices |
| D2 inspectable dry-run/manual cleanup | #44/#45; `TestDryRunNoWritesOrRechecks`, `TestCleanupAdapterSharedFixtures`, `TestCleanupAdapterDecisionsEqualDirectService` | Complete expired dry-run with exactly the short set and future long worker; later harmless manual rerun | Passed original and retry expired dry-runs and harmless reruns |
| D3 Go Lambda, schedule and scoped IAM | #46/#47; actual package/export bridge, `TestCleanupHealthRejectsDrift`, strict Lambda decoder/factory tests | Reviewed saved plan/apply, v6 export, role/code pins, recent scheduled successful summary/end | Passed installation and H1 |
| D4 exact scope, diagnostics and final recheck | #42/#44/#46; `TestEligibilityScopeTagsStatesAndUTC`, `TestFinalRecheckRejectsForgedAndDriftedEvidence`, `TestSDKScopeSerializationAndSingleMutationAttempt` | Restricted operator launch/manual actions; scheduled cleanup role; exact tags/IDs and untouched long worker | Passed retry offline exact-short cleanup and future-control exclusion |
| D5 retained decisions, termination/invocation failures, health | #44/#46/#47; journal acknowledgment/rejection tests; `TestBothProducerDestinationsAreVerified`, `TestEvidenceRouteAndAlarmDrift`, `TestCompletionCannotBeInferredFromSilenceOrPartial` | Handler events, Lambda pre-handler failure records, health failure/repair and later success; literal live Scheduler exhaustion deferred to #58 | Revised scope satisfied: Case A permanent denial retained; B/C passed; A2 unsupported/recovered; literal live exhaustion UNPROVED/deferred to #58; all executed cases restored and final health passed |
| D6 repeated/concurrent cleanup, later retry, disposable roots | #44/#48; `TestHappyPathAndRepeat`, `TestTrulyConcurrentRunsHaveIndependentAuthority`, `TestDenialProtectionThrottlingAndLaterScanRecovery`, `TestVolumeEvidenceIsExactAndIndependent`, terminal-history regressions | Harmless rerun, exact terminal states, exact root deletion, final empty campaign set | Passed original/retry roots and reruns; explicit live-control down completed |

| Human acceptance step | Evidence predicate and planned capture | Status |
| --- | --- | --- |
| H1 provision and recent successful invocation | Saved reviewed migration applied by setup; genuine scheduled `summary` and `invocation_end` both successful, with correlation/request IDs; `doctor` health follows #47 | Passed 24/24; normal retry health rechecked before parking |
| H2 short/long and batch/market matrix | Two Spot workers in one 20m request and one explicit On-Demand worker with 2h; original request/attempt/Fleet/instance/root/deadline pins | Passed original batch/market matrix and retry readiness |
| H3 inspect and expired dry-run | `ls` table+JSON; complete no-write dry-run after short deadline and before any termination request, exact short candidates only | Passed original 97-predicate and retry 82-predicate expired reports |
| H4 actual local client closure/offline interval | User confirms actual offline start/end; retained scheduled prepared-to-observed termination bracket falls wholly within it; separate return observations | Passed retry; conservative 03:13–03:35Z offline interval. Original NOT_PERFORMED unchanged |
| H5 retained decision, root deletion, long worker survives | Exact `termination_prepared` mappings and outcomes; separate EC2 state and per-root DescribeVolumes evidence; long worker running before its deadline | Passed retry exact short/root cleanup and future-control survival; original Spot interruption remains distinct |
| H6 harmless rerun and explicit remaining cleanup | Manual cleanup makes no new short-worker termination; exact-ID down of long worker, independent roots and final scope inventory | Passed retry zero-candidate rerun, live-control down and exact roots/final empty active inventory; original live manual down NOT_RUN unchanged |

| Failure gate | Coverage and evidence required | Status |
| --- | --- | --- |
| UTC equality, invalid/overflow/long durations | Pure policy tests plus delayed dispatch/final SDK gate tests; label controlled | Controlled tests passed at `30ab076` |
| Missing/malformed/duplicate expiry, other owner/deployment/account/region, unmanaged records | Pure/service/CLI fixtures; do not create unrelated live resources solely for this | Controlled tests passed at `30ab076` |
| Incomplete scan, wrong-ID response, scope/expiry drift | Service pagination/recheck and real SDK loopback tests; zero unauthorized sends | Controlled tests passed at `30ab076` |
| Concurrent invocation, terminal disappearance, exact volume evidence | Service race and terminal-history tests; live benign rerun separately | Controlled tests passed; original and retry benign reruns passed |
| Temporary termination API failure and later recovery | Controlled `TestDenialProtectionThrottlingAndLaterScanRecovery`, with actionable event and next-scan success; no live throttling claim | Controlled tests passed at `30ab076` |
| Disabled/failed schedule detectable and repaired | Captured schedule/health failure, exact preserved restore, later scheduled success | Passed disabled/fault detection, exact restoration and later normal scheduled completion |
| Scheduler retry exhaustion before handler starts | Independent retained failure envelope with actual retry/exhaustion fields, original delivery correlation, no handler start; exact mechanism from reviewed #47/#48 failure protocol | Controlled exhausted-envelope export fixture passed; both A2 targets rejected and Case A has zero retries/no exhaustion field; literal live test UNPROVED, user-approved deferral to #58 on September 16, 2026 |
| Lambda async pre-handler failure | Retained OnFailure envelope for a new async event while concurrency is zero, no handler start, exact restore | Passed actual Case B and restoration |
| Evidence Pipe failure detected and recovered | Captured unhealthy route/backlog, restored transport, the same retained failure records arrive in Logs | Case C stopped/backlog/same-message recovery and full normal restoration passed |
| Historical records/results and emergency cleanup survive upgrade | Historical serialization/recovery fixtures; actual old manifest/result read if a still-retained command exists; otherwise document the unavailable live case | Actual historical output recovery passed; emergency/terminal behavior covered separately |
| Every interrupted/partial launch cleaned | Preserve every known request/Fleet/instance/root; exact observation and explicit recovery even when up fails | All original and retry exact workers/roots cleaned; active scope empty |

References: [expiry contract](../plans/04-expiry-contract.md),
[launch verification](43-launch-expiry.md),
[shared service contract](../plans/04-expiry-cleanup-service.md),
[manual runbook](45-manual-cleanup.md),
[scheduled deployment](46-scheduled-expiry.md). The final [cleanup health/recovery runbook](../runbooks/cleanup.md) and
[#47 controlled verification](47-cleanup-evidence.md) define the deployed interface.
Doctor uses the restricted operator, validates the health-role pins, then assumes
the read-only health role for these checks.

## Capture helper and local verification provenance

`scripts/expiry-acceptance.py` creates private durable runs, captures explicit
argv without a shell, records stdout/stderr/start/end/exit/SHA-256, preserves
partial outputs, refuses reused labels, and prepares complete schedule update
JSON without contacting AWS. Its `identities` command indexes even attempt-only
IDs from supplied JSON; that index is a recovery aid, never termination authority.
The helper stores no environment dump. INT/TERM/HUP stop the entire command
process group with a bounded grace period and final kill, then preserve final
stdout/stderr hashes and status; repeated catchable signals cannot interrupt
finalization. The direct-child exit/signal race also drains remaining process-group
writers before hashing. Capture commands must not detach background work. An
output-artifact error preserves the underlying command exit, available stream
hashes and a separate evidence-capture failure. SIGKILL, host suspension or power loss can leave an unresolved
started record and cannot prove that AWS rejected a request. Reconcile any
possibly accepted mutation from independent state before proceeding. Never give it credential-source commands,
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

The parent executed the following checks at `30ab076` in the durable tested
checkout. This is the executed recipe, **not a request to rerun or reuse these
immutable capture labels**:

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

The parent also used a separate clean checkout of `30ab076` for offline
infrastructure checks, with independent backend-disabled data and ambient AWS
credential discovery disabled. Executed recipe:

```bash
acceptance_revision=30ab07614cf29b2389f2c997c7035b909a166cc2
git worktree add --detach "$acceptance_run/offline-checkout" "$acceptance_revision"
capture final-infra env -u TF_DATA_DIR -u AWS_PROFILE -u AWS_ACCESS_KEY_ID \
  -u AWS_SECRET_ACCESS_KEY -u AWS_SESSION_TOKEN \
  AWS_EC2_METADATA_DISABLED=true AWS_SHARED_CREDENTIALS_FILE=/dev/null \
  AWS_CONFIG_FILE=/dev/null make -C "$acceptance_run/offline-checkout" \
  infra-check TOFU=/tmp/devbox-tools/tofu
```

### Actual integrated check evidence

All paths below are relative to the private durable run above. No AWS credentials,
authentication, APIs or cloud mutations were used for these checks.

| Capture | Actual result at `30ab076` (September 15, 2026 UTC) |
| --- | --- |
| `commands/final-revision`, `final-worktree`, `final-diff` | Exact revision pinned; clean checkout; diff check passed |
| `commands/final-helper-tests` | 13 controlled helper tests passed, 03:16:47–03:16:57 |
| `commands/final-make-check` | Passed, 03:17:20–03:17:36 |
| `commands/final-make-build` | Passed, 03:17:36–03:17:37 |
| `commands/final-cleanup-package` | Passed, 03:17:37–03:17:39; architecture, executable, deterministic ZIP and digest-change checks |
| `commands/final-race` | Six listed packages passed, 03:17:20–03:18:24 |
| `commands/final-infra` | Bootstrap 1 and foundation 28 tests plus real OpenTofu export/Go digest bridge passed, 03:17:20–03:18:21 |

`artifacts/integrated-build-30ab076.json` records these pinned artifact SHA-256
values and byte-identical cleanup ZIPs from both isolated checkouts:

| Artifact | Bytes | SHA-256 |
| --- | ---: | --- |
| `devbox` | 25782861 | `7ccada5a0ef6c05c160086136165293a8c443b1427f9984c7411186a1157125b` |
| `devbox-runner-linux-amd64` | 13869412 | `c4237239f21270f85583885582e220515f20157ad683ca79383b4dd9ab655a27` |
| `devbox-cleanup-linux-amd64.zip` | 8464450 | `6287cf664eab47b2c4ffecd587e020b361cfdb315063f895a9e6713a2d82cdf8` |

The campaign-finish correction at `7a830dc` changed only protocol scripts/tests
and this ledger. Its affected verification is recorded at `7a830dc`: 14 controlled helper tests passed in
`commands/lifecycle-review-fix-tests/`, including real sourced-wrapper,
generated-recovery and stateful-stand-in lifecycle scenarios; all 23 Bash document
blocks, wrapper/Python syntax and diff checks passed. The capture also pins the
exact tested script bytes in `source-revision.json`. These are local controlled results, not live evidence.
The full Go/build/infra/race results above apply to `30ab076`, not to an unrun
full suite at the later script revision. Independent preparation and subsequent client-fix reviews passed; final
acceptance review remains pending. Preserve the initial helper provenance and each later helper
revision separately; never overwrite the earlier artifact.

The actual installed manifest and decoded Lambda CodeSha256 matched the copied
cleanup ZIP digest; the 904-predicate installed verification records that live check.

## Reviewed execution protocol — retained recipe

The original launch section below has already run. Do not execute it again for
the retry: the separately authorized two-request retry and its actual identities
are recorded above. The user deferred the one literal live Scheduler exhaustion
test to #58 on September 16, 2026. This retained recipe does not authorize another
fault probe.

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
6. Keep the first installation disabled and run the **worker-free Case B async
   pre-handler canary first**. The Pipe creates its fixed failure stream only
   after a real event; doctor intentionally cannot verify the missing stream
   before this canary. Verify configuration/role/route pins while recording that
   expected initial health failure, then preserve the real OnFailure record and
   finish/disarm the campaign while the schedule remains parked, verifying
   original concurrency/settings before crossing to the enabled-plan step. Reuse this Case B evidence in the
   final matrix; it is live route evidence, not scheduled/offline worker proof.
7. Parent prepares/reviews/applies the separate saved enabled-schedule plan.
   Capture a genuine successful scheduled invocation, summary/end/correlation,
   and `doctor --timeout 120s --json` plus independent health/API results after
   alarms settle. This is H1; the earlier manual canary is not H1. Then disable
   the same schedule using a complete preserved update and capture that disabled
   health state before launching the timed matrix.

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
acceptance_cli="$acceptance_run/artifacts/devbox-d3967f3"
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

The original identity table is retained here; request/attempt/Fleet and root pins
are preserved in the immutable original captures and verified matrix reports:

| Request/market | Original request / attempts / Fleet | Exact worker / root / DeleteOnTermination | Created / expires UTC | Outcome |
| --- | --- | --- | --- | --- |
| Spot batch | `09530d865fb961a66546facf3a9b1073`; attempts/Fleet in original up capture | Both original short IDs/roots in original proof | Original expiry `2026-09-15T23:58:02.758500653Z` | One Lambda cleanup, one Spot interruption; both roots deleted |
| On-Demand | Original up-long capture and receipt | `i-01c13c8414ab8eb3d` / `vol-01b11ec2ae10c2613` | Original expiry `2026-09-16T01:39:13.536814201Z` | Lambda cleanup at expiry; root deleted; live manual down NOT_RUN |

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
read-only metadata and changes state/start date. Preparation refuses unknown fields or a start less than two minutes ahead.
**Preparation sends nothing and does not authorize later activation.**

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

Parent reviews the exact JSON and records its SHA-256 together with the start
boundary. Activation must use the executable path below, not a separate raw
update-schedule command. Supply the previously reviewed hash; do not recompute a
new hash to silently accept changed bytes:

```bash
python3 scripts/expiry-acceptance.py activate-schedule \
  --run "$acceptance_run" --label schedule-offline-activation \
  --input "$acceptance_run/migration/schedule-offline-start.json" \
  --sha256 "$acceptance_reviewed_schedule_sha256" \
  --profile devbox-setup --region us-east-2
```

It captures a private byte-for-byte input snapshot, checks its reviewed hash and
current UTC **immediately before** starting the one-attempt CLI, and sends nothing
unless at least 180 seconds remain. The CLI has a 45-second outer limit and
bounded connection/read calls, preserving at least 120 seconds for acknowledgment
and disconnect under normal host operation. Code 0 means only
acknowledged_pending_readback. Every failed, timed-out, signaled, or late result
after command start is outcome_unknown_reconcile_before_disconnect: AWS may have
accepted the update. Do not retry automatically or tell the user to disconnect;
read the exact schedule and park/replan the controlled run if needed.

Immediately read GetSchedule with a bounded capture and compare the complete
accepted settings/boundary with the reviewed input. Before the user checkpoint,
verify at least 60 seconds still remain. If that margin has gone, keep the user
online and reconcile/replan; readback cannot retroactively prevent an invocation.
Record acknowledgment, readback and first eligible occurrence separately. Host
suspension, clock jumps or unknown outcomes make the offline attempt inconclusive
until independent reconciliation. Later restore/remove temporary StartDate via
the reviewed intended configuration, preserving the full target.

Aim to finish dry-run soon after expiry and choose the first occurrence around
expiry +5m, with sufficient review/activation/disconnect margin. If that timing
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
pass predicates. Actual exported-manifest verification and concrete saved recovery
artifacts still precede its live use; this gate is not waived.

The original required cases were Scheduler retry exhaustion without handler
startup, a new Lambda async event sent directly to OnFailure with reserved
concurrency zero, and stopped/broken evidence transport with detectable backlog
followed by the same records arriving after repair. The latter two passed;
the user deferred only the literal live Scheduler retry-exhaustion test to #58.
Restore concurrency, schedule target/input, DLQ/retries, Pipe state and all other
modified settings before declaring success.
Drain intentional failures and capture a later genuine successful scheduled run.
No extra worker launch is required for these infrastructure failure cases.
For a simpler recovery boundary, complete exact teardown of all campaign
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
| Pinned build/check provenance | Full suite/infra at `30ab076`; helper lifecycle at `7a830dc`; CLI fix full make check/foundation race/wire/build at `d3967f3`; final test-only fixture: isolated mock/export bridge and attempt-consistency rerun passed |
| Concrete migration, independent review, authorization, exact apply/export | Passed; 904 configuration predicates and preserved resources |
| Restricted operator Spot/batch and explicit On-Demand creation | Original matrix and approved two-On-Demand retry passed |
| Genuine scheduled completion and doctor | H1 passed 24/24; retry normal health passed before deliberate parking |
| Expired dry-run | Original 97-predicate and retry 82-predicate reports passed |
| Actual offline interval plus independent AWS events | Passed retry; conservative 03:13–03:35Z offline interval. Original NOT_PERFORMED unchanged |
| Exact roots and future-control survival | Passed retry offline short/root cleanup and future-control survival; all original roots independently deleted |
| Failure route and recovery | Revised scope satisfied: Case A permanent denial retained; B/C fully recovered; A2 unsupported; literal live exhaustion UNPROVED/deferred to #58; all executed cases restored and final health passed |
| Sole acceptance-scope amendment | User approved September 16, 2026: defer the literal live Scheduler retry-exhaustion test to [#58](https://github.com/JosephWest2/cloud_dev/issues/58); no live pass claimed |
| Harmless rerun and live manual remaining-worker removal | Passed retry harmless rerun, live-control explicit down and independent root checks; original live down NOT_RUN unchanged |
| Final scoped inventory, enabled settings, retained resources and health | Passed: exact workers/roots cleaned, active scope empty, 59 managed resources retained, zero-change plan and final health |
| Final merge status | PR #55 merged and #48/#4 closed September 16, 2026; this updates the former pre-merge gate without adding live evidence |

No idle detector, checkpointing, cost estimate, automatic replacement or TTL
extension is introduced here. PR #55 and #48/#4 are complete within the revised
acceptance scope. Interactive MVP release #5 remains
a separate handoff; its acceptance has not been claimed here.
