# MVP 4: expiry policy and implementation contract (#42)

Status: contract and pure validation only. Approved policy: default two hours,
configurable up to seven days; cleanup every five minutes; elapsed time continues
through active work; legacy requests remain inspectable/removable but cannot
allocate more once #43 installs the allocation gates. **This slice does not
activate TTL, alter existing launch behavior, or deploy cloud resources. Manual
`down` remains necessary until the implementation and live acceptance gates pass.**

## Policy and clock

- New-request precedence: explicit `up --ttl DURATION`, then optional config-v1
  `default_ttl`, then `2h`. No environment or profile TTL override. An explicitly
  empty value is invalid. Validate a configured value even when overridden.
  Parse/validate launch defaults at the fresh-launch boundary; a malformed
  launch-only duration must not disable `ls`, `down`, cleanup, or saved logs.
- Accepted grammar is unsigned Go `time.ParseDuration`: decimal numbers with
  units `ns`, `us`, `µs`, `μs`, `ms`, `s`, `m`, `h`, including concatenation and
  fractions (`1h30m`, `.5h`, `36h`, `168h`). Units are case sensitive. No signs,
  whitespace, day/week suffixes, exponents, zero, negative, infinity, or unlimited
  spelling. Go converts fractional nanoseconds toward zero; the resulting
  duration must be at least 1ns and at most `168h`. Parse overflow is rejected,
  never clamped. `168h1ns` is invalid. Longer work requires a new request before
  or after expiry; no in-place extension is supported.
- One original `created_at` and one absolute `expires_at` are computed before
  any dispatch and durably persisted with the immutable request. Timestamp
  grammar is canonical UTC RFC3339Nano: `YYYY-MM-DDTHH:MM:SS[.fraction]Z`, at most
  nine fractional digits, no redundant trailing fractional zeros, no numeric
  UTC offset, no leap second. Creation uses the injected clock converted to UTC
  with its monotonic component removed; there is no rounding to seconds. Zero
  clocks and timestamps outside years 1–9999, including addition overflow, fail.
- Creation tags are exactly `CreatedAt=<created_at>` and `ExpiresAt=<expires_at>`
  on instances, root volumes and Fleet where supported. Every worker/attempt in
  the request uses identical strings. Effective `ttl` in preview/output is the
  canonical Go duration derived from these persisted timestamps; it is not
  another independent immutable input. Plan `expires_at` must equal its tag.
- Expired means `now >= expires_at`, including equality at nanosecond precision.
  TTL is elapsed wall time, not idle time. Boot/readiness failure, active SSH or
  exec, client exit, detached work, stopping/starting, and reconciliation never
  extend it. Results still have their independent storage retention policy.
- `internal/expiry.Clock` is injected into creation and orchestration; production
  uses `SystemClock`. Discovery samples once, each final recheck samples again.
  A clock moving backward can make a prior candidate future and must prevent
  termination. A fast creation clock grants an effectively longer real lifetime;
  a fast cleanup clock can terminate early. There is no hidden grace period or
  client-provided clock in scheduled input. Hosts must use synchronized clocks.
  Schedule delay, throttling, and AWS completion mean the deadline is an
  eligibility boundary, not a guarantee of termination at an exact second.

## Immutable requests, schemas and migration

| Object | Existing records | New expiry-aware records |
| --- | --- | --- |
| User config | v1 | v1, optional `default_ttl` added by #43 |
| Launch plan | v1, no expiry | v2; append `ExpiresAt string` with `json:"expires_at,omitempty"` after existing fields |
| Local batch receipt | v2 with plan v1 | v3 with plan v2; no other envelope changes |
| Single-worker receipt | v1 | No new version; new single-worker launches use batch/Fleet path |
| Prepared attempt / dispatch claim / response | v1 | v1; existing plan/input hashes commit to new contents |
| Permanent S3 namespace | `launches/v2/...` | Unchanged; request IDs remain globally random within scope |
| Deployment manifest | v4/v5 accepted for existing recovery | v6 required for new allocations after #43 integration |
| Lifecycle JSON envelopes | Existing schema versions | Keep versions; additive expiry fields described below |
| Cleanup result/event | None | v1, types in `internal/expiry/cleanup.go` |

`omitempty` is required on the appended plan field so re-encoding a v1 plan
retains its exact original bytes/digest. Version pairs are exact: receipt v2
must contain plan v1, receipt v3 must contain plan v2. A v1 plan carrying any
expiry field/tag is invalid. A v2 plan must have canonical positive timestamps,
a duration within the maximum and exact creation-tag agreement. Unknown versions
fail closed. Decoders must check raw field presence before decoding strings:
explicit `expires_at:""` or `expires_at:null` in a v1 plan is rejected too;
`omitempty` alone cannot enforce this. `ValidatePlanFields` validates values/tags,
so the version-aware decoder supplies that additional presence check.
Unknown fields remain rejected; a new client must not infer schema
from an optional field, normalize old creation strings, or reorder old structs.

Historical fixtures in `internal/lifecycle/testdata/expiry-legacy` pin complete
serialized plans, both receipt families, and all three permanent attempt
envelopes, including a plan digest. They must remain readable and byte-stable.
New v2 plan/v3 receipt integration fixtures belong to #43. The pure schema/tag
validator is already tested here. Digest/token generation remains the existing
canonical struct JSON/ordered choices algorithm. Fresh values change because
the new schema, deadline and tags change; historical values never do.

`Instance` is embedded in `WorkerOutcome` inside permanent response-v1 records.
Do not append always-present expiry/status fields to that historical wire shape.
Use omitted zero-value fields for historical DTOs and version-aware public output
projections where `expires_at:null` is required. Response-v1 bytes and expected
worker equality must remain stable alongside the plan digest fixtures.

After #43, all fresh allocations (Spot/On-Demand; count one or many) use the
existing Fleet path with an expiry-aware v6 foundation. Fresh legacy v4
RunInstances allocation is retired rather than launching an unexpired worker.
Manifest v4/v5 and receipt v1/batch v2 still permit observation and explicit
cleanup. Legacy prepared requests cannot dispatch, and old completed requests
cannot obtain a missing-capacity successor: return `legacy_request_no_expiry`
and require a new request. No old worker is retroactively tagged or extended.
Older client binaries do not acquire these guards; rollout must replace clients
and migrate IAM before claiming universal TTL enforcement.

`up --resume` always observes known identities. `up --retry-missing` can only
prepare a successor for proven missing capacity while the **original** deadline
is future. Terminated historic fulfillment still counts toward fulfillment.
Expired/legacy/unknown requests remain available for observation and cleanup.
Expiry never releases a dispatch claim or changes the immutable request lineage.
All explicit replay launch overrides (including an identical `--ttl`, name,
count, group, profile, or market) are rejected as `replay_override` before AWS
mutation; current config TTL is ignored on replay. Validate the allocation gate
before preparing/claiming and again immediately before every allocating send,
including any SDK retry that could allocate. Observation reads need no such gate.
A claim acquired before expiry but not sent before expiry stays permanently
claimed and unsent. A later response may describe a send accepted just before
expiry; retain and clean up its workers without refreshing their deadline.

#46 defines the exact v6 `cleanup` descriptor: nested Role-shaped
`execution_role` and `scheduler_role` (top-level `roles` stays instance/operator),
function ARN/runtime/architecture/code digest, schedule ARN/group/state/cadence,
time/retry budgets, retained log group, and optional versioned `cleanup.evidence`
capability for #47. Validate cleanup health separately from launch-independent
inventory/manual cleanup/log recovery. Missing failure evidence means the offline
cleanup acceptance gate has not passed; merely exporting v6 is not that proof.

## Shared cleanup API and scope

The pure package exports `ParseTTL`, `ResolveTTL`, `NewWindow`, `Window.Validate`,
`ValidatePlanFields`, `CheckAllocation`, `InspectTags`, `Scope.Validate`,
`Evaluate` and `Recheck`. `CheckAllocation` adds a temporal gate; it never grants
dispatch permission. `Evaluate` accepts a raw duplicate-preserving `[]Tag` and
verified account/region evidence. `Decision` is observation only. The #44 service
owns private frozen scope/IDs and implements bounded discovery/execution around
these functions. CLI/Lambda adapters use its single implementation and the shared
`Result`, `Outcome`, `Volume`, `Event`, `Problem`, and injectable `Sink` types.
No CLI, SSM, SSH, profile, receipt, OpenTofu, or scheduler dependency belongs in
that service. Manual cleanup needs only valid selected config scope, credentials,
STS expected-account verification, and an explicit regional EC2 client.

Trusted scope is exactly selected expected account, region, `ManagedBy=devbox`,
deployment and configured owner. Do not infer owner from an STS principal or
accept account/region/deployment/owner/instance-ID/time overrides from a schedule
payload. The deployed one-owner environment is authority. Scheduled input requires `{"schema_version":1}` and optionally accepts only
`scheduled_time`, `schedule_arn`, `execution_id`, and `attempt_number` string
fields for Scheduler delivery correlation. They never override scope, time, or
candidates; unknown keys/versions and wrong types fail before mutation. See
[AWS Scheduler correlation attributes](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-schedule-context-attributes.html).
Manual global AWS-profile/region options may select credentials/region using the
existing config rules; they do not override expected account/deployment/owner.

### Discovery, recheck and failure boundary

1. Verify scope syntax, actual STS account and explicit regional client before
   mutation. Discover with exact managed/deployment/owner tag filters in that
   scope, **without expiry or state-only filters**. Paginate to completion.
   Retain malformed/missing expiry diagnostics. Unrelated returned resources
   are diagnosed and skipped; unrelated regional inventory need not be listed.
2. Deduplicate exact IDs; identical repeated evidence coalesces. Conflicting
   duplicate records are `resource_invalid` and authorize no termination for
   that ID. A failed, truncated, repeated-token or canceled scan is incomplete:
   retain known observations, set `scan_complete=false`, authorize **zero** IDs.
   Per-resource validation errors in an otherwise complete scan skip that
   resource while verified peers can proceed.
3. Freeze eligible IDs, exact expiry strings, scope, evaluation time and known
   volume mappings privately. Sort output by ID. A dry run performs zero writes,
   including tag changes or EC2 DryRun permission probes. Its candidate set is
   advisory; a subsequent cleanup performs an entirely new discovery/recheck.
4. Immediately before each termination, read that exact ID with bounded paging,
   verify a complete singular response, scope/account/region and expiry again,
   then use `Recheck`. Wrong IDs, missing records, changed expiry (even another
   past value), future/removed/invalid expiry, and scope drift do not authorize
   mutation. Preserve original IDs and diagnostics if the read returns a forged
   unrelated resource. Revalidation is the final read before dispatch; EC2 has
   no compare-and-terminate transaction, so IAM tag constraints and this narrow
   interval reduce but cannot eliminate concurrent tag-change races.
5. `pending`, `running`, `stopping`, `stopped` plus expired/valid scope are eligible
   for a one-ID termination call. `shutting-down` is observation only;
   `terminated` is harmless terminal observation. Both retain separate reasons
   and need no repeated termination. Unknown state is unverified. Already terminal
   resources discovered with valid expired tags may undergo volume observation;
   they do not count as candidates. Other expiry problems remain diagnostic.
6. Bound each AWS request (15s), pagination (128 pages per scan/recheck), service
   deadline (default 165s), observation retries (at most 30), and concurrency (4).
   Read retries fit the context deadline. Each termination dispatch makes exactly
   one SDK attempt per application-level exact-ID recheck (disable automatic
   mutation retries); later scans safely retry unresolved workers. Cancellation preserves known partial
   evidence. One denied/protected/throttled/slow resource cannot suppress peers.
   Subsequent invocations retry still-eligible failures; no launch or replacement
   logic, local receipts or expiring locks are involved.

Decision reasons are enumerated in `internal/expiry/policy.go`. Evaluation
precedence: invalid clock/scope/identity; mismatched account/region; malformed or
duplicate non-expiry tags; managed/deployment/owner mismatch; duplicate/missing/
invalid expiry; future expiry; then state/expired. Identical duplicate expiry
tags are invalid. Noncanonical strings are invalid. `InspectTags` independently
supports `ls` with `expiry_missing`, `expiry_duplicate`, `expiry_invalid`, or a
canonical value. Missing/invalid expiry never hides a worker or prevents explicit
`down`; do not reuse a strict inventory validator that does so.

### Termination and root-volume evidence

Before mutation, preserve each exact volume ID, device, root mapping and explicit
`DeleteOnTermination` value (null means unverified) in a `termination_prepared`
event. Missing/contradictory mappings or an unverified root deletion flag prevent
that instance's dispatch (`root_volume_unverified`) so disposable-root behavior
is established before mutation. A verified retained root (`false`) is reported
as `root_volume_retained` and skipped for automatic cleanup; explicit `down`
remains available for deliberate teardown. Never set deletion flags or broaden
permissions as part of cleanup. Each independently valid peer can still proceed.

Reuse bounded teardown observation and exact-volume helpers where appropriate,
not name resolution or expiry-blind termination. Preserve old mappings after
EC2 stops returning them. A termination request, a confirmed terminated instance,
and confirmed deleted root are three separate facts. A lost termination response
is `termination_unknown` until exact observation resolves it. An absent instance
is not termination or root-deletion proof. Verify deletion only for previously
captured exact volume IDs after observed terminal state; explicit
`InvalidVolume.NotFound` or a verified exact `deleted` state is evidence, an empty
successful response is not. Report retained roots as retained and unknown mappings
as unavailable; do not invent cleaned counts. Never call DeleteVolume, enumerate
and delete regional volumes, or delete results, launch history, or foundation.

`Sink.Emit(context.Context, Event) error` must honor the bounded context, support
concurrent workers, and acknowledge an owned immutable event snapshot before
returning nil. Callers do not hold service locks across sink calls. The off-worker
sink must preserve pre-dispatch mappings. A failed pre-dispatch
sink write prevents that instance's termination; a later sink failure marks the
run incomplete without attempting to undo termination. `decision`,
`termination_prepared`, `outcome`, and `summary` events have a run ID and trusted
scope. #47 defines the durable routing/log contract and the investigation path
using these exact IDs when later EC2 scans cannot recover old mappings.

## Output, exits and adapters

`cleanup [--dry-run] [--json] [--timeout DURATION]` accepts no targets, selectors,
`--yes`, TTL, or launch flags. Explicit cleanup invocation authorizes the policy
operation. `--timeout` is positive, default 165s, maximum 5m. Global config,
AWS-profile and region options retain their meanings. `down` keeps its independent
selector and confirmation semantics. Unknown arguments exit 2 before mutation.

One schema-1 `cleanup` JSON envelope goes to stdout followed by newline; progress
and text preview go to stderr. Text output presents the same scope, mode,
evaluation time, scan/completion flags, counts, and each ID's expiry/reason,
termination status and root deletion. Never emit arbitrary raw tags, AWS errors,
credentials, or user-controlled input in messages. JSON shape is the exported
`expiry.Result`: required scope/mode/times/flags/counts, `instances` and `errors`
arrays (empty arrays, never null). `expires_at` is a canonical string or null.
Each outcome includes `ec2_state`, `evaluated_at`, `eligible`, `reason`, `status`,
`root_volume_deletion`, `volumes`, and `errors`.

Outcome `status` values: `skipped`, `would_terminate`, `termination_not_requested`,
`termination_requested`, `termination_unknown`, `termination_denied`,
`termination_observed`, `already_terminating`, `already_terminated`.
Root/volume deletion values: `not_observed`, `deleted`, `retained`, `unavailable`.
Deletion and termination remain separate even for an otherwise successful run.

Counts use unique instance IDs: `scanned_count` includes valid IDs returned by
scoped discovery, including diagnosed unrelated records; `candidate_count` is
frozen eligible count from a complete scan (zero for incomplete discovery);
`terminated_count` counts exact observed terminal instances;
`cleaned_count` counts observed terminal instances with verified deleted roots.
Already-terminal observations can therefore exceed the new candidate count.
Dry-run counts for terminated/cleaned remain zero because it only evaluates,
without terminal/volume completion observation. `scan_complete` means discovery
finished; `complete` also requires finishing requested per-resource work with no
unresolved failures. `ok` means exit 0, not merely that a termination was sent.

| Exit | Aggregate code / interpretation |
| --- | --- |
| 0 | `cleanup_complete`; complete scan/work, no unresolved failures; or `cleanup_no_candidates` for a clean zero-candidate scan |
| 1 | `cleanup_failed`; prerequisite/service/incomplete-scan failure or unresolved resource failures with no successful candidate/terminal work |
| 2 | `cleanup_invalid`; syntax/configuration error before mutation |
| 3 | `cleanup_partial`; some successful candidate/terminal work and some unresolved failures |
| 4 | `cleanup_interrupted`; deadline/cancellation, with partial outcomes preserved |

Exit precedence: usage/config before starting; interruption overrides runtime
partial/failure; otherwise aggregate by actual outcomes. A successful dry-run
candidate (`would_terminate`) counts as useful candidate work for partial status;
a mutating candidate succeeds only when termination and root deletion are
verified. Benign future/missing-expiry/terminal skips and a valid expiry change
on recheck need not produce an error. Malformed/duplicate tags, unrelated scope,
invalid state/identity, failed rechecks, retained/unverified roots, denied or
unresolved termination, and sink failures add `Problem` diagnostics and prevent
`complete=true`. Recheck scope drift is `scope_mismatch`; absent/contradictory
exact-ID responses are `resource_unverified`. Use stable service error codes
`scan_incomplete`, `identity_unverified`, `termination_denied`,
`termination_protected`, `termination_unresolved`, `root_volume_unverified`,
`root_volume_retained`, `volume_unresolved`, `evidence_unavailable`, `interrupted`.
No candidates with malformed expiry is a failed scan outcome, not silent success.

A healthy five-minute schedule plus bounded invocation has a planning target of
roughly nine minutes from expiry to a termination request (including Scheduler's
[60-second precision](https://docs.aws.amazon.com/scheduler/latest/UserGuide/schedule-types.html)); AWS completion and
retry/failure scenarios are separate and this is not a hard SLA. #46 uses a
180s Lambda limit with a 165s internal deadline, reserved concurrency one,
Scheduler retry limit two/max age 300s, Lambda function-error retries zero/max
age 300s, and 30-day logs. #47 adds an independent shared standard SQS failure
queue for Scheduler DLQ and Lambda async failures, then an EventBridge Pipe to
retained CloudWatch Logs. Preserve message attributes as well as body. A schedule
cannot claim unattended readiness until that failure route is installed and
verified. These deployment details do not become manual cleanup prerequisites.

### Target examples (available only after the owning child ships)

```toml
schema_version = 1
# ...existing required scope/config fields...
default_ttl = "2h" # fresh launches; --ttl wins; maximum 168h
```

```text
devbox up agent --count 2 --ttl 36h
# preview includes ttl=36h0m0s expires_at=2026-09-15T12:00:00Z
# all workers and missing-capacity attempts retain that same expires_at

devbox cleanup --dry-run --json
devbox cleanup
# deliberately remove an older worker without a usable expiry tag:
devbox down i-0123456789abcdef0
```

Complete no-candidate dry run (required arrays retained):

```json
{"schema_version":1,"command":"cleanup","ok":true,"exit_code":0,"code":"cleanup_no_candidates","message":"No expired candidates.","scope":{"account":"123456789012","region":"us-east-2","deployment":"personal-dev","owner":"owner"},"dry_run":true,"evaluated_at":"2026-09-14T02:00:00Z","completed_at":"2026-09-14T02:00:01Z","scan_complete":true,"complete":true,"scanned_count":0,"candidate_count":0,"terminated_count":0,"cleaned_count":0,"instances":[],"errors":[]}
```

Representative per-instance dry-run outcome:

```json
{"instance_id":"i-0123456789abcdef0","ec2_state":"running","expires_at":"2026-09-14T02:00:00Z","evaluated_at":"2026-09-14T02:00:00Z","eligible":true,"reason":"expired","status":"would_terminate","root_volume_deletion":"not_observed","volumes":[],"errors":[]}
```

`ls` and per-worker launch results add `expires_at` (string/null) and
`expiry_status`: `future`, `expired`, `missing`, `invalid`, or `duplicate`, using
the same clock/parser. The `ls` text table adds expiry and status columns. Launch
preview/top-level batch outcome adds effective `ttl` and `expires_at`; unavailable
legacy values are null. Retained legacy records need not be rewritten to render
these derived output fields.

## Requirement-to-child map and gates

| Child | Requirements and completion gate |
| --- | --- |
| #42 | Approved policy; pure clocks/durations/schema/tag/eligibility/recheck tests; pinned historic bytes/digests; docs/examples/help; no AWS mutation |
| #43 | Config/CLI TTL; persist immutable v2/v3 records; allocation gates for delayed dispatch/retries/legacy; v6-only new launches; exact tag serialization; tolerant inventory/output; compatibility tests |
| #44 | Bounded shared discovery/recheck/termination/volume service and sink; complete-scan gate; per-instance failures; concurrency/race/API tests |
| #45 | Manual cleanup adapter, dry-run and output/exits; independent prerequisites; no-write and mixed-outcome CLI tests |
| #46 | v6 foundation/tag IAM, narrow cleanup role, scheduled runtime/deployment and verifier; reviewed migration; no live apply implied by this contract |
| #47 | Retained structured events, Scheduler/Lambda fail-before-start evidence and independent route, health/recovery guidance; failure injection |
| #48 | User-run live restricted-role launch/manual/scheduled/offline evidence, failure paths, retention checks, exact-ID root cleanup and final docs |

Keep result retention, permanent launch records, explicit teardown semantics,
known IDs and their recovery instructions intact in every child. Each child gets
its own PR and fresh independent review. Stop for user-run authentication/live
checks or material policy changes; complete offline implementation and review
first. The parent remains open until live acceptance and cleanup evidence pass.
