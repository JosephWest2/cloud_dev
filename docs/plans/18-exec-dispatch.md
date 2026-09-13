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

The reviewed runner correction waits for the authoritative submission instant
within its existing preparation deadline, retaining genuine expiry checks and
the single start claim. Full checks and deterministic clock/cancellation/expiry
tests passed. After independently verifying removal of the original worker/root,
the actual reviewed artifact/template plan applied successfully. Both role
policies rotated only their exact runner-artifact ARN. A real follow-up plan
returned exit 0 with no changes.

## Live completion gate

Clean source `21ad872` and the updated export passed all 14 doctor checks. Eleven
live command cases passed: literal argv, exits 0/1/2/4/255/127, invalid cwd, full
binary stdout/stderr and true empty streams. Actual SIGKILL immediately after
acknowledgement and during execution stopped every local process group without
a local finalizer; both remote jobs finished and published complete results.

A helper GET/list publication race was corrected after independent review. Ten
already attempted cases were recovered read-only, and an exact-source/scope/ID
guard submitted only the one unattempted final case in a separate directory.
No attempted or uncertain command was replayed. Both failed-run evidence and all
eleven distinct public/SSM IDs are retained privately.

The replacement worker and its captured root volume were independently verified
removed, with scoped inventory empty. Post-teardown STS/S3-only recovery passed
318 assertions across all eleven commands, including complete bytes and hashes.
See the [actual acceptance report](../acceptance/18-exec-dispatch.md). Fresh
non-implementing review approved the correction, actual plan, helpers and live
evidence; final documentation review completes the merge gate. Parent #2 remains
open for #19 observation, #20 retrieval and #21 complete acceptance.
