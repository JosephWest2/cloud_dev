# Issue #19 observation and detachment

Follows merged #18 / PR #24 (`fd867d4`). The user selected Ctrl-C detachment and
30-day retention, requested sequential children and fresh independent review
before merge. No new behavior decision is needed for this slice.

## Implementation

Observe one acknowledged invocation and strictly bound started/outcome/final S3
records. Retain independently validated workload evidence through later failures,
check record agreement and expiry, and separate raw SSM wrapper response from the
workload exit. A complete final record wins an optional SSM API failure or a local
interruption once completion is established. SSM success alone never establishes
workload success or complete publication.

Each read is bounded by 10 seconds and the caller's local wait deadline. Polling
backs off from 1 to 5 seconds; six consecutive transient failures of either
backend end observation with a sanitized API failure. Eventual visibility,
pending/delayed and cancellation-pending states remain nonterminal. A terminal
wrapper or ending optional API failure gets a final durable reread. The observer
has no send or cancellation API. Delivery, wrapper, workload and local deadlines
remain distinct; cancellation of the local observer never establishes remote
termination.

Credential-provider classification preserves the shared SDK cache without parsing
raw errors. Source authentication failures cannot enter the S3 missing-key
fallback, and terminal SSO token/client errors do not become transient polling.
Storage retries one exact read when an authenticated listing discovers a just-
published object after a denied GET. Interrupted response bodies remain transient
or canceled, while successfully received malformed/short/oversized metadata is
corrupt. Immutable PUT and start-claim retry guarantees remain unchanged.

JSON retains one metadata envelope and all known IDs, with separate SSM/durable
states and validated timestamps. Text distinguishes `ssm_response_code` from
`remote_exit_code`. Recovery instructions and acknowledgement warnings survive
post-dispatch failures. Workload bytes still require explicit retrieval in #20.

The Linux supervisor previously returned 4 when its context was canceled after a
child had emitted completed exit-0 JSON and then exited 0. A real subprocess
regression reproduced that contradiction. The supervisor now preserves the
child's actual normal exit status. Both supervisor and worker keep signal handlers
installed through process exit, closing the repeated-signal window after a final
result. The existing bounded credential-descendant cleanup remains in place.

## Review and validation

Fresh non-implementing reviews approved storage, observation and CLI/supervisor
source. Storage review found and fixed credential denial masquerading as object
denial, and terminal SSO refresh failures masquerading as retryable transport.
Controlled SDK regressions cover both, including a denied provider that would
succeed on its next retrieval: no resource lookup is permitted in that case.

State-machine tests cover exact exits 0/1/2/4/255, response -1 and wrapper 255,
pending/delayed/eventual visibility, transient recovery/exhaustion, auth/denial,
all timeout categories, external cancellation states, identity/record disagreement,
expiry and completion races. Actual supervisor subprocess tests cover preserved
completion, SIGINT/SIGTERM/repeated signals, controlling-terminal Ctrl-C and one
JSON envelope with recovery IDs. Existing actual-CLI credential/descendant cleanup
tests remain passing. Full `make check`, `make build`, execution/CLI/supervisor
race tests and pinned `make infra-check TOFU=/tmp/devbox-tools/tofu` passed.

## Remaining live gate

Freshly review the prepared nine-case helper, freeze/build the CLI in a clean
checkout and launch one temporary On-Demand worker. Use #18's accepted template 4
and runner pin; no infrastructure apply is required for the observation client.
Exercise exits 0/1/2/4/255, an actual short workload timeout, local wait expiry,
terminal Ctrl-C and supervisor SIGTERM. Recover each detached command through
scoped STS/S3 reads and verify its original IDs, status, full bytes and completion
after local detachment. Preserve attempts and never replay an uncertain command.

Record exact worker/root IDs, terminate the worker, independently verify its root
absent and scoped inventory empty, then recover again without the worker. Review
actual evidence, final documentation and any corrections before merging. Parent
#2 stays open for #20 retrieval and #21 complete acceptance.
