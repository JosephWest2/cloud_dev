# Issue #30 immutable instant-Fleet attempt

This slice follows the reviewed multi-AZ foundation in
[PR #36](https://github.com/JosephWest2/cloud_dev/pull/36). It implements one
allocation attempt behind injected durable storage and verification interfaces.
The S3 backend and cross-invocation reconciliation belong to #31; public batch
launch remains unavailable until #32. No worker has been launched for this slice.

## Allocation and evidence

The adapter sends an explicit instant Fleet with only the chosen market:
price-capacity-optimized Spot across approved pairs, or lowest-price On-Demand
using the first profile type across its approved subnets. Exact numeric template,
AMI, effective encrypted disposable gp3 root and IMDSv2 pins occur in the actual
SDK request. Every fleet, instance and root volume receives the same complete
sorted creation tags. There are no weighted units, maintenance or market fallback.

The dispatcher reconstructs the immutable plan against current trusted config,
manifest and profile, verifies the deployed foundation, saves the local prepared
receipt and announces recovery before shared writes. It verifies shared
preparation, saves dispatch intent, then requires a positively acknowledged
permanent claim before the SDK call. Plan and input digests plus the client token
bind the prepared record, claim and response. Cancellation after a winning claim
can intentionally leave an unresolved attempt without sending.

Every distinct valid returned instance ID and every normalized pool error is
retained. Full fulfillment can be recognized without an empty error set; partial
or zero fulfillment requires an explicit instance collection and valid pool
errors. Duplicates, contradictory counts or pins, omitted evidence and transport
uncertainty remain unknown. An over-target response retains all IDs as unknown
and cannot authorize a successor. Missing capacity is never inferred from error
cardinality or absence in EC2 inventory.

A single observed SDK attempt can establish a definitive modeled rejection.
A final rejection after an earlier uncertain transport attempt remains unknown.
The original normalized response is saved before eventual-consistency worker
observation; both local and shared persistence are attempted independently.
Later observation checks cannot rewrite that immutable response. Known IDs and
observed root-volume mappings survive post-dispatch persistence/observation
failures in the returned outcome.

The local schema-v2 cache uses a private file, fsync, atomic rename and directory
sync under the request lock. It refuses legacy overwrites, changed plan pins,
dispatch rewinds, forgotten identities and changes to terminal attempt evidence.
It is never dispatch authority; only #31's permanent shared claim can grant that.

## Controlled verification

Actual EC2 SDK tests use local HTTP servers with XML responses. They inspect the
complete serialized Query parameters for both markets, including a 137 GiB
effective root overriding a 100 GiB template and exact template version 7.
Throttling and a dropped connection after simulated acceptance both produce
byte-identical same-token retries. Omitted and explicit-empty response collections
are tested through the SDK deserializer.

Controlled response tests cover full, partial and explicit zero allocation,
multiple pools and multiple errors per pool, duplicates, malformed/incomplete
responses, over-target identities, permission/quota/capacity failures and uncertain
transport errors. Dispatcher tests cover preparation and announcement failures,
existing/ambiguous claims, cancellation after claiming, and post-dispatch writes.
Worker verification checks exact identity and resource bindings independently.

September 14, 2026 (US/Central): `make check`, `make build`,
`go test -race ./internal/lifecycle -count=1` and `git diff --check` passed.
Fresh independent reviewers `review30_dispatch` and `review30_workers` reviewed
dispatch/cache/response behavior and worker verification respectively. Review
found and prompted regression coverage for two identity checks:

- Verified counts require the current attempt's exact instance IDs and attempt
  identity; an equally sized set of foreign workers cannot prove fulfillment.
- Per-worker type/subnet/AZ reported by the immutable Fleet response stays pinned
  even if another placement is also approved by the original plan.

Current worker settings are distinct from historical allocation evidence.
Teardown can remove address, profile, interface and root observations; the verifier
retains original identities and prior root mappings and reports unavailable
current verification. #31 must count authoritative historical fulfillment without
turning a removed worker into replacement capacity.

Live restricted-operator allocation, worker use and verified cleanup remain #34.
