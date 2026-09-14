# Issue #3 ordered implementation and #28 contract gate

Based on merged MVP 2 at `69206ea`. The user requested one subissue at a time,
fresh subagent review before merging, and pauses for key decisions or human
testing. Each child has its own reviewable PR; its implementers cannot provide
its fresh review. Resolve findings and rerun affected checks before merging and
starting the next child. Parent #3 stays open until all seven children and live
acceptance/cleanup gates pass.

| Child | Scope and completion gate | Status |
| --- | --- | --- |
| #28 | Validated profiles, CLI selections, plan/naming/retry/output contracts; Go checks and fresh review | Complete; reviewed checks below |
| #29 | Multi-AZ foundation/manifest/IAM; offline checks and reviewed concrete live migration plan | Complete; [verification and fresh reviews](../acceptance/29-spot-foundation.md) |
| #30 | One immutable pinned instant-Fleet attempt; controlled serialized SDK and failure tests | Complete; [verification and fresh reviews](../acceptance/30-fleet-attempt.md) |
| #31 | Shared dispatch claims, unknown/partial recovery and explicit proven-missing retry; crash/concurrency tests | Complete; [verification and fresh review](../acceptance/31-shared-recovery.md) |
| #32 | Integrated group launch/inventory/readiness and independent access; CLI checks | Complete; [verification and fresh review](../acceptance/32-group-launch.md) |
| #33 | Exclusive scoped plural teardown, frozen confirmation set and root-volume evidence | Pending |
| #34 | Fresh two-worker Spot/exec/log/restart/cleanup, explicit On-Demand/decline, full checks and acceptance matrix | Pending |

Normative decisions and examples are in the
[Spot batch contract](../contracts.md#spot-batch-contract-28). The user approved
small permanent shared S3 launch records in the existing private bucket on
September 13, 2026. Only a positively acknowledged conditional dispatch-claim
winner may send one attempt; uncertainty after claiming remains unresolved.
Claims never expire or get released; result retention is unchanged. Runtime
roles cannot delete ledger records and worker roles cannot access them.

#28 deliberately leaves public batch operations unavailable until recovery is
integrated. Parser acceptance, plan construction and receipt validation perform
no mutation. Profile v1, manifest v4 and receipt v1 remain usable; completed
log recovery does not gain launch prerequisites. #29 owns cloud verification
of every actual template/AMI/type/subnet combination; #30 owns SDK serialization.

Human checkpoints: prepare and review the concrete infrastructure migration
before requesting any apply decision; pause for authentication or interactive
checks requiring the user's environment. Do not replace live evidence with
policy-text matching. Preserve every request/Fleet/instance/root-volume/command
ID during live work and verify cleanup even if acceptance fails.

## #28 verification and review

September 13, 2026 (US/Central): `make check`, `make build`, targeted config/CLI/
lifecycle tests and vet, and `git diff --check` passed. The parent launch example
parses and produces one JSON `feature_unavailable` result before AWS, as intended
until #32. No live resources changed; infrastructure checks belong to #29.

Two fresh independent subagents, `review28_code` and `review28_contract`,
reviewed the complete final implementation and contract; neither implemented
this slice. Both approved after these fixes:

- Require an explicit non-null fulfilled-ID array in complete receipt records;
  omitted/null fields cannot turn one-of-two fulfillment into two missing workers.
- Reject different selected subnets in the same AZ, matching Fleet limitations.
- Reject placement restrictions with a legacy v4 manifest and gate v5 launches
  from the old allocator before AWS so it cannot bypass new contracts.

Meaningful coverage includes `TestMaximumCountConfiguration`,
`TestProfileV2RejectsInvalidOrMissingOptions`,
`TestManifestV5RejectsInvalidFoundationAndContradictions`,
`TestInvalidLifecycleSyntaxDoesNotReachAWS`,
`TestReservedLifecycleSyntaxDoesNotReachAWS`,
`TestResolvedLaunchPlanUsesOnlyApprovedCombinations`,
`TestWorkerNamesAndCreationTagsSurviveOrderAndPartialResults`,
`TestMissingCapacityPreservesHistoricFulfillmentAndLineage`,
`TestUnknownAndCorruptReceiptsCannotProveMissingCapacity`,
`TestCompleteReceiptRequiresExplicitFulfillmentArray`, and
`TestLegacyLaunchCannotBypassV5Integration`.
