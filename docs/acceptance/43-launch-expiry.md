# Issue #43: immutable launch expiry (offline verification)

Status: offline implementation only. **Live acceptance is not passed.** No AWS
resources were mutated, no infrastructure was changed, and no cloud messages,
comments or PRs were published during this implementation. The parent handles
push, PR, independent review and merge.

## Delivered contract

- Config-v1 optional `default_ttl`; explicit `up --ttl` overrides config, then 2h.
  Both configured and explicit values are validated at fresh launch, including
  explicit empty values. Maximum 168h, no unlimited or environment/profile override.
  Malformed launch duration does not disable inventory, down or saved logs.
- One UTC RFC3339Nano creation/deadline pair, sampled from an injectable clock,
  with no rounding. Every attempt retains the original pair; effective TTL is
  derived from it. Plan v2 appends omitted-zero `expires_at`; receipt v3 pairs
  exactly with plan v2. Legacy plan-v1/receipt-v2 and single receipt-v1 remain readable.
- All fresh Spot/On-Demand, count-one/batch requests use instant CreateFleet.
  Creation tags on instance, volume and Fleet have exact identical `CreatedAt`
  and `ExpiresAt`. RunInstances is retained only as a historic serializer;
  its allocating service branch is retired.
- Gates before preparation, claim and send (including final SDK middleware)
  reject legacy/expired requests. Fleet SDK retries are disabled. Expiry after
  an acquired permanent claim never releases it. Accepted responses and IDs
  survive persistence/observation failures. Resume is observation-only; explicit
  missing-capacity retry counts historical fulfillment and never refreshes expiry.
- `ls` and per-worker launch output expose nullable expiry and the five statuses;
  preview/batch output includes effective TTL and deadline. Raw invalid values
  are not emitted. Malformed/duplicate scope tags remain strict; missing/invalid/
  duplicate expiry does not hide workers or prevent explicit down.

## Exact handoff to #46

This slice's **minimum v6 reader/capability contract** is:

1. `schema_version: 6` means the trusted foundation export authorizes expiry-aware
   instant Fleet creation. All existing v5 fields, subnet/pool/image pins, results
   and launch-ledger descriptors retain their exact names and validation.
2. Top-level `roles` remains exactly `instance` and `operator`. Their existing
   `arn`, `trust_sha256`, `policy_name`, `policy_sha256` fields keep their meanings.
   Actual launch foundation/IAM hash verification now runs for both v5 and v6.
3. The only reserved new top-level reader field is `cleanup`, represented as
   `json.RawMessage` with `json:"cleanup,omitempty"`. This reader intentionally
   does not invent nested deployment fields or validate cleanup health. #46
   replaces/extends that opaque boundary with its exact descriptor and separate
   health verifier: nested Role-shaped `execution_role` and `scheduler_role`,
   function/runtime/architecture/code digest, schedule/group/state/cadence,
   time/retry budgets and retained logs, plus optional versioned `evidence` for #47.
4. #46 must export v6 only with instance/volume/Fleet `ExpiresAt` creation-tag
   authorization alongside the existing exact creation tags. Do not relabel a
   v5 export or infer IAM support from a local fixture. New allocation rejects v4/v5;
   old receipts/manifests still support observation, down and saved logs.
5. Cleanup health and failure evidence are independent of launch-independent
   inventory/manual cleanup/log retrieval. An absent/unhealthy `cleanup` descriptor
   does not block those recovery paths. A v6 version number alone is no evidence
   of healthy unattended cleanup. Schedule cadence remains 5m per #42.

No infrastructure or IAM policy was edited here. Scheduled runtime/deployment,
cleanup health, failure routing and live restricted-role acceptance remain the
work of #44–#48. The current foundation still exports v5 and cannot launch with
this client until the real v6 migration is installed.

## Schema and compatibility audit

- `BuildFleetInput` validates plan v1 and v2 without consulting the clock;
  actual allocation uses a separate gate. Raw v1 `expires_at`, including empty
  string or null, is rejected by every nested plan decoder. Unknown fields and
  unsupported record versions still fail validation.
- Batch receipt version checks and shared-ledger reconstruction use exact
  v2/v1 and v3/v2 pairs. Prepared/claim/response records stay v1. Plan/input
  digest and token algorithms, permanent `launches/v2/` keys and claims are unchanged.
- `Instance` is also embedded in permanent response workers: new expiry fields
  omit zero values, and volatile status is not placed in immutable responses.
  Public output uses a separate nullable projection. Historical response worker
  equality and byte-for-byte serialization continue to validate.
- Former plan-v1-only readiness/startup/outcome/text branches now handle both
  supported plans. Manifest-v5 branches in config, image, template, network,
  offerings, IAM, and ledger verification also include v6. Legacy v4 branches,
  config/profile/result/document schema checks retain their independent meanings.
- Historical `testdata/expiry-legacy` fixtures were not rewritten. New
  `testdata/expiry-v2` fixtures pin the complete expiry plan, batch receipt,
  successful-worker response, prepared record, claim, digest and token/input hashes.
  Obsolete RunInstances allocation tests now exercise historical observation or
  rejection; Fleet dispatch/persistence/restart/concurrency tests remain active.

## Verification

Controlled tests cover:

- TTL precedence, grammar, explicit emptiness, overridden bad config, nanoseconds,
  UTC conversion, zero/addition-overflow clocks and maximum bounds.
- Actual EC2 Query serialization for Spot and On-Demand with counts one and two,
  including exact instance/root-volume/Fleet creation tags and preview-before-send.
- Expiry at equality and 1ns before expiry; delayed preview, post-claim delay and
  delay after serialization; a lost response cannot trigger an SDK mutation retry.
- Real shared-ledger round trips, permanent claim retention, restart and missing
  capacity with changed/invalid current TTL; expired historical workers remain
  fulfilled and cannot authorize replacements. Historical pure serializers work.
- AWS-authoritative inventory without receipts: missing, invalid, nil, identical
  duplicate, future and expired tags; explicit down still verifies root deletion.
  Duplicate/malformed non-expiry scope tags continue to prohibit mutation.
- CLI text/JSON nullable fields, stable error reasons, replay-override rejection
  before config/AWS, and v6 launch-pin verification with controlled drift.

Commands (run locally; no live endpoint):

```sh
go test ./internal/lifecycle -run Expiry -count=1
go test ./internal/lifecycle ./internal/cli ./internal/config
make check
make build
go test -race ./internal/lifecycle ./internal/cli
git diff --check
```

All commands above passed. The race run covered lifecycle dispatch, shared-ledger
recovery, startup/readiness and CLI integration. It exposed a request-counter
race in the controlled lost-response test; the counter now uses atomic access,
and the complete lifecycle/CLI race run passes. Historical fixture bytes,
`infra/`, `go.mod` and `go.sum` are unchanged. Live acceptance remains pending.

## PR #51 review corrections

Reproduced both P2 findings against `0b06e84` using the supplied review overlay.
The overlay tests now pass after these corrections:

- Historical `shutting-down` and `terminated` rows retain AWS-observed expiry
  diagnostics before being filtered from live-setting verification. Public
  `up --resume` output preserves missing/invalid/duplicate expiry, including
  partial reads, while request deadline, identities and fulfillment stay intact.
  Existing scope/naming/pin validation remains strict; diagnostics do not grant
  observation or allocation authority. Immutable worker ledger records are unchanged.
- Legacy plan-v1 decoding rejects every Unicode-case-folded spelling of
  `expires_at`, including empty/null values and escaped long-s variants accepted
  by Go's struct decoder. Nested receipt and permanent-ledger tests cover the
  rejection, true historical records, valid plan-v2 aliases and unknown fields.

Public recovery tests use the real orchestration/shared-ledger paths after local
cache loss and compare permanent record bytes before/after observation. They
cover both terminal states, valid original/future expiry controls, missing,
malformed, empty, nil, noncanonical and duplicate tags, plus scope/name mismatches
and partial exact-ID responses with an independently preserved peer.

Verification passed:

```sh
go test -count=1 -overlay=/tmp/issue43-review-7d25txin/overlay.json ./internal/lifecycle -run '^TestReview' -v
go test -count=1 ./internal/lifecycle ./internal/cli ./internal/expiry
go test -race -count=1 ./internal/lifecycle ./internal/cli
go vet ./internal/lifecycle ./internal/cli
git diff --check
```

These corrections are confined to recovery diagnostics and plan decoding; the
complete affected package suites cover their callers without repeating the full
repository check. The v6 handoff and all TTL/send gates remain unchanged. No AWS
or GitHub writes were made; live acceptance remains pending.

## Follow-up review: scoped inventory expiry fallback

Reproduced the P2 finding against `8f3d9be` with the independent final-review
overlay. Scoped scans now retain worker expiry diagnostics when later exact-ID
inspection fails, returns NotFound, or returns no rows. Newer exact-ID rows still
replace those diagnostics, including rows returned with a partial-read error.
This changes only observed worker expiry fields; schema, immutable records,
request deadline, historical fulfillment, scope validation and allocation bounds
remain unchanged. The manifest-v6 handoff and TTL/send gates are unchanged.

Permanent regressions cover running, shutting-down and terminated workers;
missing, invalid, empty, nil, noncanonical and duplicate expiry tags; original
and future valid controls; exact-read precedence and partial responses. Public
`up --resume` tests recover from shared records after local cache loss and check
nullable JSON, stable worker IDs, immutable ledger bytes and zero new allocation.
Unavailable-read readiness retries use controlled cancellation.

Verification passed:

```sh
go test -count=1 ./internal/lifecycle -run '^TestExpiryScan'
go test -count=1 -overlay /tmp/issue43-final-review-045v18w6/overlay.json ./internal/lifecycle -run '^TestFinalReview'
go test -count=1 -overlay /tmp/issue43-review-7d25txin/overlay.json ./internal/lifecycle -run '^TestReview'
go test -count=1 ./internal/lifecycle ./internal/cli ./internal/config ./internal/foundation ./internal/expiry
go test -race -count=1 ./internal/lifecycle ./internal/cli
go vet ./internal/lifecycle ./internal/cli
git diff --check
```

The independent final-review overlay also checks the SDK send gate at expiry
equality and 1ns before expiry. Full affected package suites and race checks cover
this observation-only correction; the repository-wide check was not repeated.
Fresh review of the final commit is pending with the parent. No live acceptance
was performed.

## Follow-up review: expiry provenance through readiness refresh

Reproduced the P2 finding against `ea56190` with the Astra review overlay.
Instance observations now carry a private, in-memory `expiryObserved` marker.
Only inspecting an AWS row sets it, including a row with no expiry tag. Immutable
deadline defaults and JSON-decoded records do not assert observation provenance.
Outcome clones preserve the marker; it adds no serialized field or schema change.

The full public resume path was traced through reconciliation, startup polling,
worker readiness, the final pre-probe lookup, and automatic recovery refresh:

- A newer scan or exact-ID row supplies the current diagnostic, even on a partial
  read. An error, empty result or NotFound without a row retains prior evidence.
- Readiness retains diagnostics before live-target filtering or error handling
  discards a returned row. This includes a terminal row followed by an empty
  terminal fallback read. Existing scope, identity and probe checks still apply.
- Refresh merges only observed expiry for the same request plan and worker ID;
  immutable defaults cannot replace previously observed missing/invalid/duplicate
  or changed-valid expiry. Repeated empty refreshes preserve that provenance.
- The replacement after a retry preparation conflict uses the same merge rule.
  Repeated inventory rows retain the newer diagnostic while existing conflicting
  inventory errors still prohibit authority. Other worker map retention, value
  copies and final projections preserve the marker without changing launch pins.

Permanent public regressions exercise startup, terminal and live resolution, and
terminal and live pre-probe observations followed by readiness-triggered refresh.
They cover all existing expiry diagnostic cases, newer scan/exact evidence,
partial responses, scan/exact errors, empty results and NotFound. Assertions check
public nullable expiry, original request digest/deadline, stable worker IDs,
historical fulfillment, allocation bounds, no new launches and unchanged ledger
bytes. Additional tests check unobserved defaults, serialization excluding the
marker, repeated refresh, cross-plan isolation, retry conflicts and repeated rows.

Verification passed:

```sh
go test -count=1 -overlay=/tmp/issue43-review-7d25txin/overlay.json ./internal/lifecycle -run '^TestReview'
go test -count=1 -overlay=/tmp/issue43-final-review-045v18w6/overlay.json ./internal/lifecycle -run '^TestFinalReview'
go test -count=1 -overlay=/tmp/issue43-astra-review-k7w3b3ua/overlay.json ./internal/lifecycle -run '^TestAstraPublicRefreshExpiryEvidence$'
go test -count=1 ./internal/lifecycle ./internal/cli ./internal/config ./internal/foundation ./internal/expiry
make check
make build
go test -race -count=1 ./internal/lifecycle ./internal/cli
go test -race -count=1 ./internal/lifecycle -run '^TestExpiryInventoryRetainsLatestConflictingRow$'
git diff --check
```

Changes remain in recovery/observation and tests. CLI/manifest interfaces, permanent
serialization, fulfillment and allocation authority are unchanged. The exact v6
handoff and all TTL/send gates remain intact. Parent publication and fresh review
of the final head are pending; live acceptance was not performed.

## Review D: legacy reads and mixed exact-ID observation order

Reproduced both P2 findings against `090b382` with the original review-D probes.
Read and compared the separate diagnostic-only correction overlay before making
the local changes; its probes passed as a control.

- Legacy single-receipt resume copies matching-ID expiry diagnostics from the
  name inventory before error handling. Its readiness lookup now retains expiry
  before partial-read errors or terminal filtering, using the existing helper.
  Name/scope checks, state handling, and the observation-only legacy rule remain
  unchanged. An unrelated row or a read with no matching row cannot replace the
  retained diagnostic.
- Exact-ID batch inspection records the last matching row's expiry before
  filtering terminal rows. It applies that diagnostic after verification, keeping
  terminal/live row order across responses and pagination. Historical state
  handling still uses its separate terminal observation. Duplicate/conflict
  errors, worker verification, fulfillment and allocation denial are unchanged.

Permanent public regressions cover missing, malformed, empty, nil, noncanonical,
duplicate and valid expiry controls. Legacy tests cover partial/successful name
checks, unrelated and out-of-scope rows, empty/error/NotFound reads, stopped,
stopping, shutting-down, terminated and partial/running readiness reads. They
assert existing error codes, no launches or probes, exact legacy receipt bytes,
and unchanged historical launch serialization/token.

Batch tests resume after local receipt loss and alternate live rows with both
terminal states in two- and three-row sequences, on one page or multiple pages,
including partial responses. They assert the latest public nullable diagnostic,
duplicate errors, stable peers and fulfillment, unchanged request deadline/digest
and permanent record bytes, and no new allocation. Changes are limited to
diagnostic propagation; CLI/manifest interfaces, schemas and TTL gates are intact.

Final verification passed:

```sh
go test -count=1 ./internal/lifecycle -run '^TestExpiry(LegacyResumeLaterEvidence|PublicMixedExactObservationOrder)$'
go test -count=1 -overlay=/tmp/issue43-review43d/overlay.json ./internal/lifecycle -run '^TestReviewD'
go test -count=1 -overlay=/tmp/issue43-review-7d25txin/overlay.json ./internal/lifecycle -run '^TestReview'
go test -count=1 -overlay=/tmp/issue43-final-review-045v18w6/overlay.json ./internal/lifecycle -run '^TestFinalReview'
go test -count=1 -overlay=/tmp/issue43-astra-review-k7w3b3ua/overlay.json ./internal/lifecycle -run '^TestAstraPublicRefreshExpiryEvidence$'
go test -count=1 ./internal/lifecycle ./internal/cli ./internal/config ./internal/foundation ./internal/expiry
make check
make build
go test -race -count=1 ./internal/lifecycle ./internal/cli
git diff --check
```

The parent coordinates publication and final-head recheck by reviewer 43d. No
GitHub/AWS writes were made; live acceptance remains pending.
