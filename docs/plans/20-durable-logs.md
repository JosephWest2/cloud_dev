# Issue #20 durable logs retrieval

Follows merged #19 / PR #25 (`66809f6`). The selected contract already defines
status-only retrieval, explicit stream selection, distinct new file exports,
retrieval exit codes and 30-day submission-based retention. No new user decision
or infrastructure change is needed for this client slice.

## Implementation and review

`execution.Recover` discovers historical identities from strict cloud records
using only the trusted result scope and public command ID. A standalone final
requires one metadata read and no SSM/EC2/current runtime dependency. Other
snapshots reconcile all observed records, use request server creation time for
request-only retention, retain known outcomes through later failures, and never
infer expiry from absence. SSM remains a separate optional wrapper snapshot.
Fresh independent review approved the source and focused race/vet coverage,
including independent failure and per-call deadline probes.

`logs.Run` loads the storage-only descriptor and checks AWS identity before
resource clients. It separates retrieval success from workload exit, downloads
only explicitly selected streams from complete publication, and preserves
workload metadata through transfer failure. Transfers use a 32 KiB buffer and
require exact advertised/body lengths, SHA-256 and EOF. File exports use private
adjacent temporary files and atomic no-overwrite publication; each successfully
published file remains identified if a later export fails.

Fresh review reproduced two transfer deadline gaps and one parser-order problem.
A blocked stdout pipe could outlast the retrieval deadline, and cancellation
inside a successful final write could be reported as verified. The Linux adapter
uses an independently opened nonblocking pipe handle, preserves the parent
descriptor's flags and regular-file offsets/append behavior, and joins its cancel
callback before releasing the handle. Transfer checks cancellation after writes
and before success. Invalid-option diagnostics now honor stream mode even when
the stream option occurs later; the remote exec separator still ends local
interpretation. Permanent regressions and independent probes cover these fixes.

The first full check also exposed an existing terminal-test fixture race: its
foreground shell printed READY but deferred its interrupt trap until another
input line arrived. Six of 30 unchanged repetitions failed. A test-only Go child
now registers signals before readiness and still exercises actual terminal
Ctrl-C, SIGWINCH geometry, foreground handoff and termios restoration. Production
terminal handling is unchanged. The fixture passed 100 focused and 30 race
repetitions; an injected failure verified helper cleanup with no remaining
fixture processes. Full `make check` then passed. `make build`, relevant package
race/vet checks and isolated pinned `make infra-check` also passed.

## Remaining gates

Finish fresh review of the terminal-test correction and final documentation,
then freeze a clean CLI build. Use the independently reviewed three-case live
helper with one scoped disposable On-Demand worker with
the accepted #18 runner/template. Exercise large binary stdout/stderr with remote
exit 255, known-empty streams, and observer restart during a delayed command.
Use new empty local state and a storage-only descriptor for logs, verify both
CLI exports and selected raw stream bytes against independent S3 reads, then
terminate the exact worker, verify root deletion/empty inventory, and export the
same results again after teardown. No uncertain command may be replayed.

Record actual evidence and fresh final review before merging. Parent #2 remains
open for #21's public fixture, complete requirement matrix and final acceptance.
