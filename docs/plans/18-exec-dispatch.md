# Issue #18 scoped command dispatch

Follows merged #17 / PR #23 (`a4eaf4f`). The user requested sequential child
issues, fresh review before merge and pauses for key decisions or required user
tests. Ctrl-C detachment and 30-day retention remain the selected contract.

## Implementation

`exec TARGET [options] -- COMMAND [ARGS...]` stops all local option parsing at
the first separator, including the initial JSON/help scan. Validate literal
argv, UTF-8/NUL, encoded bounds and the independent clocks before cloud work.
Relative cwd is lexical POSIX resolution from `/home/devbox`; shell evaluation
is explicit. Execution-specific manifest loading validates the full scoped v4
runtime export without a local SSH identity, launch profile or executable probe.

Use one verified SDK identity and region for lifecycle resolution/readiness, S3
and SSM. Require the current AMI/template pins, ready worker, supported Linux EC2
agent, exact execution document/version/hash and matching private result storage.
Revalidate the original exact instance immediately before sending. Reserve an
immutable request under a random public ID; print it before dispatch. Configure
the non-idempotent SendCommand operation with a no-retry policy. Emit the returned
SSM ID before optional acknowledgement publication. A lost response is unknown
and never triggers another send; a lost acknowledgement PUT preserves submission.

The worker/publisher shipped in #17 supplies actual execution and full output
capture independently of this client. A baseline S3-final waiter makes this
slice usable: strict final metadata, exact binding/SSM ID, retention, remote exits
and publication completeness are checked under a separate local wait deadline.
Only metadata is emitted; explicit retrieval will verify stream bytes in #20.
Detailed SSM intermediate states, outcome-only recovery, transient API handling
and process/signal completion races remain #19. An interrupted SendCommand keeps
`submission_state=submission_unknown`, even when local outcome is interrupted.

## Review and validation

Meaningful tests cover all remote argument classes and local option boundaries,
scope/ambiguity/stale pins, independent clocks, denied/malformed/uncertain writes,
announcement order and failures, partial acknowledgement, exact remote exits and
strict final-record validation. An actual configured SSM SDK with a controlled
transport accepts the request then loses its response; the test proves exactly
one wire submission despite a retry-capable client default.

Three fresh non-implementing reviewers approved the core dispatch, CLI/integration
and config/final-observation scopes. Targeted tests, race checks and vet passed.
Full `make check`, `make build`, execution/CLI/config race tests and pinned
`make infra-check TOFU=/tmp/devbox-tools/tofu` passed. Infrastructure checks ran
offline, separate from the existing live backend. Initial live checks used #17's
existing pinned runner and exported foundation.

## Live finding and runner correction

The first isolated CLI attempt failed before dispatch because the test PATH
omitted `sh`, which the SDK's credential-process provider requires. The reviewed
helper correction retains only `sh` and `aws`, with SSH/plugin and local
key/profile dependencies still absent. The failed attempt is preserved.

The next attempt submitted once, but the runner began at `22:16:09.707Z` while
S3 reported the immutable request's submission time as `22:16:10Z`. Its strict
future-timestamp guard mislabeled this subsecond difference as expiry, so the
workload did not start. The CLI retained both recovery IDs and eventually
reported its independent observation deadline. This failed evidence is also
preserved; no uncertain request is replayed.

Correct the runner to wait for the authoritative submission instant within its
existing preparation deadline, retaining genuine expiry checks and the single
start claim. Freshly review the fix, rebuild and inspect the actual artifact/
launch-template plan, then apply the reviewed update before a new live run.
The original worker must be removed and its captured root volume verified
absent before deploying the replacement runner.

## Remaining live gate

Freeze and build the CLI in a clean checkout, launch one disposable On-Demand
worker from the current export, and exercise the actual CLI. Include literal
argv, exit 0/nonzero/missing command/bad cwd, binary stdout/stderr beyond inline
limits and real empty objects. Kill the observing local process immediately
after acknowledged submission and during execution, then recover the original
cloud records and verify full bytes without a local finalizer or redispatch.

Keep exact instance/root-volume IDs and every public/SSM command ID. Terminate
the worker, independently verify its original volume absent and scoped inventory
empty, then freshly review the live evidence and any fixes before merging #18.
#2 and #18 remain open until their respective gates pass.
