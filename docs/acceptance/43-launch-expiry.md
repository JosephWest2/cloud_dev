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
