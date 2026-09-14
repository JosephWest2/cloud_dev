# Issue #32 group launch, discovery and readiness

This slice connects the reviewed allocator and shared recovery from
[PR #38](https://github.com/JosephWest2/cloud_dev/pull/38) to the public CLI.
Version-5 deployments support Spot groups, explicit On-Demand, observation-only
resume and explicit missing-capacity retry. Legacy v1 receipts and v4 single
On-Demand launches keep their existing path. No live AWS resources changed.

## Public behavior

New launches validate the configured count cap, profile and exact manifest
before constructing AWS services. The verified dispatch path prints the resolved
profile, region, market, original and attempt counts, base/group and eligible
type/subnet/AZ choices before shared dispatch permission or allocation. Progress
and recovery commands go to stderr; stdout contains one schema-v2 result. Final
output failures still print known request, worker and root-volume identities to
stderr. Inventory also uses a schema-v2 result with per-worker identity fields.

Partial capacity and partial readiness are distinct. A one-of-two response with
one ready worker reports requested/fulfilled/ready counts 2/1/1 and exit 3. Full
capacity with one ready worker reports 2/2/1, `readiness_failed`, exit 3. Explicit
zero capacity and unknown allocation return exit 1; unknown missing count stays
null. Timeouts and interruption return exit 4 with all known identities.

Resume can restore a lost local cache from shared records and has no launch
profile dependency. Readiness uses the validated original plan's pinned document.
Explicit retry revalidates current launch pins and count cap and requests only
the original missing remainder. A repeated retry does not allocate again.

Cloud inventory derives names from creation-time `NamingVersion=1`, `BaseName`
and each full instance ID. Group, request and attempt identities remain separate;
one group may contain multiple requests. Names survive reordered responses,
partial capacity, retry and peer removal. Shared name/ID resolution is used by
individual SSH, SSH-config, exec and down. A generated name colliding with an
older worker's literal Name requires an explicit candidate ID.

Inventory validates scope, filters, batch naming metadata and actual identity,
fully paginates, deduplicates identical IDs and sorts output. Contradictory
duplicates or malformed/foreign responses fail closed while preserving earlier
valid IDs. Group discovery does not read launch receipts or a local profile.
Legacy workers remain discoverable. Readiness metadata failures retain inventory
and per-worker observation errors.

Readiness observes at most four workers concurrently under one overall deadline
of at most five minutes, including queue and document validation time. A failed
worker does not cancel its peers. Only verified allocated workers are probed;
historical/terminated workers remain fulfilled but not ready. Exact-ID checks
bind the original batch settings before each probe. Progress cannot authorize
allocation, and identity/root-volume evidence survives failures.

## Controlled verification

The integrated lifecycle tests use actual plan construction, attempt dispatch,
shared S3 state transitions, AWS reconciliation and readiness observation with
controlled EC2/S3/SSM APIs. They cover full, partial, zero, uncertain and explicit
On-Demand allocation, independent readiness failures, pre-dispatch preview,
cache-loss resume and a one-worker retry followed by a harmless repeat.

CLI tests cover valid public selections, count caps before AWS, schema-v2 JSON,
sanitized errors, exact group filters, aggregate exit codes and failed final
output retaining all identities. Inventory tests cover cloud-only paginated
rediscovery, multiple requests in one group, generated/legacy name ambiguity,
malformed metadata, scope/filter mismatches and incomplete pages. Readiness tests
cover shared deadline/concurrency, peer isolation, unverified identities, scope
and pin drift, cancellation, queued workers, terminal workers and output failure.

September 14, 2026 (US/Central): `make check`, `make build`, full lifecycle/CLI
race tests and `git diff --check` passed. Fresh independent reviewer
`review32_readiness` reviewed the full readiness, inventory/resolver, public
orchestration, CLI output, tests and documentation. The reviewer also ran race
suites for lifecycle, CLI, access and execution, and approved the final code
after these fixes:

- Progress enqueue no longer blocks when its bounded output queue fills. Twelve
  healthy workers all get observed even when a progress writer is blocked.
- The last exact-ID read immediately before a readiness probe now revalidates
  batch identity and settings, including an absent group becoming present.
  Fourteen changed-pin cases cannot send a probe under the old identity.
- Text inventory includes the same base/group/attempt/subnet/AZ information as
  JSON. Current help and contracts describe enabled batch operations.

Advisory progress can be dropped under backpressure; final worker evidence stays
complete. An already-blocked writer cannot be forcibly interrupted by the Go
writer interface, but it cannot hold worker observation beyond the deadline.

Live two-worker independent commands/logs, restart discovery, actual Spot
authorization and verified cleanup remain #34. Plural/group teardown remains
gated until #33; individual teardown remains available now.
