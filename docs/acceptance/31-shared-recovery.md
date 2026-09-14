# Issue #31 shared launch recovery

This slice integrates the immutable attempt allocator from
[PR #37](https://github.com/JosephWest2/cloud_dev/pull/37) with permanent shared
S3 coordination and observation-only recovery. Public batch commands remain
gated until #32. No live resources were changed for this slice.

## Shared authority and retries

The private result bucket holds a canonical plan and immutable prepared,
dispatch-claim and original-response records for each deterministic attempt.
Records are excluded from result expiration and retained until the deployment
is deliberately removed. Each object has strict schema, scope, plan/input hash,
token and lineage validation. Reads are bounded to one request and at most 256
attempts; each record is limited to 1 MiB. Credentials and remote error payloads
are replaced by fixed diagnostics.

Every write uses `If-None-Match: *`, expected bucket owner, encryption and a
SHA-256 checksum. Claim writes have exactly one SDK wire attempt. Only a positive
acknowledgment grants dispatch; a failed acknowledgment cannot be recovered into
permission by reading the object. Two independent clients therefore share one
permanent claim. A crash after claiming but before sending can remain unresolved.
No claim is released, expired or deleted by runtime commands.

Resume reads shared records, scoped paginated EC2 inventory, exact known worker
IDs and exact known Fleet IDs. It never dispatches, including prepared attempts.
Current worker settings and root-volume mappings are verified independently.
Instant Fleet disappearance and worker termination do not erase fulfillment
recorded by the original complete response. An empty inventory, visible target
count, current Fleet activity or a local terminal receipt cannot establish a
missing count for an unknown allocation.

Explicit retry requires complete shared responses for all preceding attempts
and successful reconciliation. It requests only the original unfulfilled
remainder in a deterministic successor slot with a new token. Repeating the
same `--after`, including from another computer, observes the existing successor.
Changed profile, template, market, scope or other plan pins cannot authorize a
new attempt; a lower current count cap blocks allocation but permits observation.

A policy-digest update at the same bucket and scope still permits observation
and preservation of the original response for an already claimed attempt. It
does not permit a new preparation or claim against the old plan. This preserves
evidence when policy changes race with an in-flight allocation.

Missing, corrupt and unsupported local caches can be restored from validated
shared history. Corrupt bytes are preserved in a private adjacent backup.
Conflicting valid caches are retained and block allocation. If shared authority
is unavailable, local-only evidence exposes validated IDs with fixed diagnostics,
zero confirmed fulfillment and an unknown missing count. Lost shared response
records are not reconstructed from local claims of completeness. Discovery,
access and teardown remain independent of the local launch cache; #32 connects
the batch service to their public commands.

## Controlled verification

Real S3 SDK tests inspect serialized conditional-write, owner, encryption and
checksum headers and simulate committed writes followed by lost acknowledgments,
409/412 conflicts, 503 responses and transport disconnection. Separate-client
tests use independent local directories and shared conditional storage, forcing
both clients to read the same one-of-two result before retrying: exactly one
additional worker is requested.

State-machine tests cover plan-only/prepared/claimed crashes, uncertain allocation,
explicit zero and partial fulfillment, repeated retry, corrupt/missing records,
invalid lineage and reused Fleet IDs, changed scope/pins/cap, delayed visibility,
pagination failures, contradictory inventory, exact-ID partial responses,
historical workers after teardown, cache loss/conflict and private diagnostics.
The existing allocator tests cover cancellation after claiming and independent
local/shared persistence failures after dispatch. Legacy receipt recovery keeps
its existing implementation and regression tests.

September 14, 2026 (US/Central): `make check`, `make build`,
`go test -race ./internal/lifecycle -count=1` and `git diff --check` passed.
Fresh independent review by `review31_recovery` covered the final ledger,
lineage, reconciliation, retry, cache, error-output and acceptance changes and
approved them. The reviewer ran separate recovery/reconciliation and ledger/
receipt race suites. Earlier ledger review also prompted the committed
regressions that strip untrusted cached diagnostics and fulfillment claims.

Final review found a historical-count regression: observing a contradictory
extra worker changed the attempt to unknown and could hide an earlier confirmed
worker after its removal. Validated original-response fulfillment is now retained
separately from mutable observations. Both disappearance and contradictory
current settings preserve the earlier count while blocking further allocation.

Live restricted-operator launches, independent remote commands, restart discovery
and verified cleanup remain #34; controlled failures do not stand in for those
gates.
