# Shared AWS expiry cleanup service (#44)

`internal/expirycleanup` implements the AWS orchestration in the
[#42 contract](04-expiry-contract.md). CLI (#45) and Lambda (#46) should call this
same service. This commit does not install either adapter, activate a scheduler,
or provide live AWS acceptance evidence.

## Adapter API

```go
svc, err := expirycleanup.NewAWS(scope, awsConfig, clock, sink, limits)
result, err := svc.Run(ctx, dryRun)
```

- `scope` is `expiry.Scope`, obtained from trusted selected configuration or the
  deployed environment. It contains expected account, explicit region, deployment
  and configured owner. Schedule input must never select these values.
- Load `aws.Config` under an adapter deadline. Its region must equal the scope,
  and it must have a credential provider. `NewAWS` constructs STS and EC2 from
  that same configuration. `Run` verifies the STS account on every invocation.
  Region-bound calls additionally reject contradictory reservation ownership,
  placement and volume identity evidence. Standard, Local and Wavelength zones
  are supported; volume ARN validation understands AWS partitions.
- `expiry.Clock` is mandatory. Production uses `expiry.SystemClock{}`. The service
  consumes only canonical absolute `ExpiresAt` tags. It never computes or extends
  an allocation deadline. The approved 2h default, 168h maximum, no-unlimited
  policy and five-minute schedule remain owned by their existing contracts.
- `expiry.Sink` is mandatory. Supply a concurrency-safe, context-aware sink which
  synchronously acknowledges preservation of the event snapshot before returning
  nil. Lambda must preserve these events off the worker. The sink has at most the
  configured request timeout; it must stop when its context expires. Service code
  does not hold a lock across a sink call. Event snapshots share no mutable data
  with private candidates, other events or returned results. Like SDK clients,
  injected sinks and clocks must meet their interface contract; an implementation
  that ignores cancellation cannot be made bounded by the service.
- `Limits{}` selects request 15s, invocation 165s, scan/recheck 128 pages,
  observation 30 rounds, concurrency 4, read attempts 3 and polling interval 2s.
  Positive overrides can only lower these values; invalid limits fail construction.
  The caller's earlier deadline always wins. Adapters that accept a larger outer
  timeout still retain the service's 165s ceiling.
- `New(scope, Dependencies, Limits)` permits controlled tests or alternate trusted
  wiring. Its EC2 interface has `Options`, `DescribeInstances`,
  `TerminateInstances` and `DescribeVolumes`; STS has `GetCallerIdentity`.
  It also accepts an optional context-aware `WaitFunc`. Dependencies must be safe
  for concurrent use. Production should use `NewAWS`, which binds both clients to
  the same credentials. There are no volume-delete, tagging, allocation, launch
  profile, SSM, SSH, receipt, OpenTofu or scheduler-health dependencies.

Constructor errors are `*expirycleanup.Failure` with `Code=cleanup_invalid`;
translate these into the adapter's usage/configuration response (exit 2). Runtime
errors have the aggregate `Result.Code`; always retain/render the returned result,
including on error. Runtime results use the shared schema-1 `expiry.Result` and
its exit/count/completeness semantics. Messages and problems contain stable codes,
not raw provider errors, tags or credentials. Adapters can explain these codes in
text without changing the machine-readable envelope.

## Execution and evidence

Discovery uses only the three exact managed/deployment/owner filters. It does not
filter by state or expiry. Duplicate tag fields survive conversion into the pure
policy evaluator. Identical repeated records coalesce; conflicting records retain
known volume mappings and disqualify their ID. Page errors, nil pages, partial
output plus an error, malformed empty reservations, token cycles, exhausted page
bounds and cancellation make the scan incomplete. All observed IDs remain in the
result, but candidate count is zero and no instance can be terminated.

A complete scan freezes candidate IDs, original expiry, evaluation time and exact
root/device/volume/deletion-flag evidence. Root flags remain pointers: nil is
unknown, false is retained. A verified disposable root is required to send a
termination request. Missing or contradictory mappings and retained roots are
reported per instance; independently valid peers proceed.

For an eligible worker, the service acknowledges `termination_prepared` with the
captured mappings **before** the final exact-ID read. That read must be complete,
singular and unchanged in scope, expiry and mappings. The clock is sampled after
it returns, and `expiry.Recheck` gates the immediately following one-ID send. This
ordering keeps sink latency out of the final read-to-send interval. A prepared
event records evidence and intent, not proof that a termination was sent. Events
also include `decision`, `outcome` and `summary`, with a fresh run ID and scope.
There is no EC2 compare-and-terminate transaction; scoped IAM remains essential.

Each termination operation explicitly installs `aws.NopRetryer` and maximum
attempts one, regardless of the caller's AWS retry configuration. Read operations
install bounded standard retry. Failed or uncertain workers can be selected again
on a later scan; the service never sends a second termination in the same run.
Already-shutting-down or terminated workers receive observation only. A malformed
or lost termination acknowledgement stays unknown until an exact terminal
observation resolves it. An absent instance is never terminal proof.

Dispatch work has four slots per invocation. Observation proceeds in rounds across
all active workers, with at most one exact-volume request per worker per round,
rotating among its captured volumes. This lets other workers and other volumes
progress when one call is slow. Every AWS request and sink acknowledgement has a
child deadline. Exhausted observation or invocation time preserves pending IDs,
known mappings and partial outcomes; incomplete work never becomes aggregate
success. Concurrent runs have independent authority and no mutable receipt/lock;
they may both request termination of the same expired exact ID harmlessly.

Deletion evidence is independent of EC2 termination. Only trusted, captured exact
volume IDs with explicit disposable flags are queried after terminal EC2 evidence.
An exact `deleted` response must have no contradictory owner, region, ARN,
pagination, identity or attachment evidence. `InvalidVolume.NotFound` is accepted
only with nil output. Empty successful responses and contradictory NotFound
responses are unresolved. Retained or unknown volumes are never treated as deleted;
non-root failures remain visible even when the root was verified deleted.

If terminal EC2 no longer returns mappings, the service uses the mappings already
captured in this invocation. If a later invocation has no mapping, it reports the
root as unavailable: it does not reconstruct authority from guesses or enumerate
regional volumes. Investigate using the earlier run's retained
`termination_prepared`/`outcome` events, exact instance and volume IDs, scope,
expiry, flags and timestamps. #47 supplies durable routing and operational recovery
instructions. The service never deletes volumes, results, ledger or foundation.

Dry-run performs discovery and policy/root diagnostics only. It returns advisory
`would_terminate` decisions; it does not exact-recheck, observe volume completion,
call the sink, send termination, tag resources or make EC2 DryRun probes. Adapters
can render its returned result without introducing persistent writes.

## Controlled verification

The package tests cover eligibility boundaries and malformed tags; complete and
incomplete pagination; duplicate conflicts; frozen authority and drift; root
mapping gates; denial/protection/throttling and later retry; uncertain responses;
exact volume evidence; terminal resources; cancellation and queued IDs; concurrent
workers, concurrent invocations and sink snapshot ownership. Loopback HTTP tests
exercise real AWS SDK query serialization, signing region, the three scope
filters, exact IDs, read retries and one mutation attempt. They use static test
credentials and never contact AWS. These are controlled checks, not live evidence.
