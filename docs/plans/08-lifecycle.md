# Issue #8 implementation plan

## Outcome and boundaries

Implement `up agent --on-demand --name smoke`, `ls`, and `down NAME_OR_ID`
with schema-versioned JSON, sanitized stderr, bounded operations, and inventory
scoped to verified account, explicit region, deployment and stable owner.
Spot remains unsupported; no market fallback, readiness/shell, groups or TTL.

## Implementation order

1. Add a lifecycle service with injectable EC2 operations and a production factory
   that loads one SDK configuration, verifies STS, and uses those credentials for
   all resource operations. `up` validates the profile/manifest and deployed
   foundation before allocation. `ls`/`down` need only valid user scope and identity,
   so broken/lost manifest, profile or receipt files cannot prevent cleanup.
2. Implement paginated AWS inventory and safe teardown first. Inspect reservation
   account and instance scope tags even when AWS filters are supplied. Explicit
   IDs receive the same validation. Reject ambiguous live friendly names; allow
   explicit IDs to resolve collisions. Re-describe selected ID immediately before
   termination. Poll termination and each captured EBS volume within the deadline;
   report root device, volume IDs, delete-on-termination flags and observed deletion.
   Distinguish `no_managed_match`, `already_terminated`, and `terminated`; absence
   of an instance alone never proves termination. Keep identifiers on timeout.
3. Implement durable schema-v1 request receipts under
   `$XDG_STATE_HOME/devbox/requests` (default `~/.local/state/devbox/requests`).
   Generate a random request ID/client token; store immutable scope, exact AMI and
   numeric template version, selected first profile type, disk size, name and
   creation time. Atomic writes, file/directory fsync and a per-request process
   lock prevent torn receipts and concurrent replay. Files contain no credentials.
   `up --resume REQUEST_ID` loads that receipt; launch-changing options are rejected
   and current scope/profile/manifest parameters must still match before dispatch.
   Print receipt/request identity to stderr before the first mutation; include it
   in JSON results. Receipts are replay records, never instance inventory.
4. Persist a dispatch marker before RunInstances. A prepared receipt can dispatch
   on resume, using its original token and parameters. Once dispatch may have
   happened, resume reconciles by scoped RequestId and client token with bounded
   backoff, including terminated matches, without sending another RunInstances.
   SDK retries of the initial request reuse the exact token. This conservative
   policy intentionally leaves the crash-before-send gap uncertain; it avoids
   relying on an undocumented token retention period or relaunching a deleted
   instance. A missing result remains recoverable, never grounds for a new token.
   Successful responses and reconciliation retain instance IDs in the receipt.
5. Launch exactly one instance using the pinned template and explicit AMI/type,
   encrypted gp3 root disk with delete-on-termination and IMDSv2. Supply the seven
   required dynamic tags on instances and volumes (ENI tags are not permitted by
   the current operator policy). Do a name preflight and post-launch collision
   check; tags do not guarantee uniqueness. Concurrent collisions return IDs and
   require explicit-ID cleanup, never automatic deletion of another request.
   Expose actual EC2 market/state and `not_observed` SSM/bootstrap/readiness fields.
6. Wire CLI parsing/output, document receipt guarantees/recovery and live acceptance.
   Add controlled tests for unsafe scopes/ambiguity, pagination, teardown waits,
   volume observation, lost launch response, restart replay, invisible inventory,
   immutable replay parameters, receipt persistence/locking and parseable errors.
   Run `make check` and targeted race tests, then create a PR and have a separate
   agent review the diff. Correct actionable findings and revalidate.

## Review and live gate

Before implementation, request GPT-6 Astra high review of this plan and repository.
Record findings and adjustments here. No live allocation until controlled teardown
passes. Pause with concise user instructions for live AWS acceptance; record
actual evidence separately and do not claim live success from mocks or close the
issue before its live acceptance is recorded.

AWS references: [EC2 idempotency](https://docs.aws.amazon.com/ec2/latest/devguide/ec2-api-idempotency.html)
and [eventual consistency](https://docs.aws.amazon.com/ec2/latest/devguide/eventual-consistency.html).

## Astra high plan review adjustments

- Dispatched receipts reconcile using their recorded request and verified current
  scope without requiring a working current manifest/profile/foundation. Prepared
  receipts validate the entire current immutable launch specification and perform
  request lookup before any dispatch.
- Persist the full effective launch specification: scope, profile, On-Demand
  market, image, template ID/version, subnet, instance profile, root device/disk,
  selected type, and all dynamic tags including the original timestamp.
- Lock a separate stable file inode, with context-aware bounded acquisition;
  atomic receipt replacement must not replace the lock inode. Abort before launch
  on any durable-write or pre-mutation diagnostic failure. Keep returned instance
  IDs in output even if updating the receipt fails.
- Root mappings unavailable, retained volumes, and deletion observed are distinct.
  A lost termination response still enters bounded observation with captured IDs.
- Use EC2's immutable `aws:ec2launchtemplate:id` and `:version` system tags for
  AWS-authoritative template identity. Do not infer it from today's manifest.
- Document that dispatch-marked requests can remain permanently unresolved; no
  automatic allocation is permitted from a negative inventory result. New `up`
  invocations are independent requests and name preflight cannot enforce global
  uniqueness. Collisions may only become visible in later `ls`/lookup calls.
