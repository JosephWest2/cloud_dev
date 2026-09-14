# Issue #20 durable logs acceptance

This slice implements public-ID recovery, verified stdout/stderr selection and
file exports for parent #2. The full public-fixture exercise and parent evidence
matrix remain in #21.

## Source and independent review

The clean live source is `fffc393`, built separately with CLI SHA-256
`841452e211347306f1c03cdf68be48384f1af9dcdc1e3b792b4b0652a2fa3457`.
It uses the accepted #18 template version 4 and runner SHA-256
`e21c0536522661235b6ea5cb22bfe3b9bd66081af640570f7a151f51db8a74f6`.
No runner replacement or infrastructure apply is required for this retrieval
client. Private evidence is under `/tmp/devbox-issue20-live-checks`.

Fresh non-implementing reviews approved metadata recovery, transfers, CLI and
service integration, final documentation and the live helpers. Independent
production-binary probes used local fake STS/S3 endpoints and placeholder
credentials. They verified explicit profile selection, identity before resource
access, one S3 metadata GET for a completed status, exactly three S3 GETs for two
exports, no EC2/SSM requests, absent runtime/SSH/local receipts, historical pins,
and workload exit 255 with retrieval exit 0. Wrong-account identity stopped
before resource reads. These are controlled checks, not live AWS evidence.

Review reproduced blocked stdout ignoring its deadline, cancellation during the
final successful write being marked verified, and parse errors preceding
`--stream` writing metadata to stdout. The corrected Linux pipe adapter preserves
the parent's shared descriptor flags, offsets and append semantics, handles
deadline/cancellation without a detached writer goroutine, and releases its owned
handle. Post-write checks and early stream-intent parsing close the other gaps.
Independent actual supervised CLI probes confirmed blocked output exits 4 for
deadline and SIGTERM, retains the known workload exit, and reports only the exact
emitted prefix as unverified. Permanent regressions and fresh review cover these
cases.

The first full suite also exposed an unrelated existing shell-fixture race in
the terminal test. Six of 30 unchanged repetitions stalled after READY, and a
probe showed the shell emitted its pending trap only after another line arrived.
The test-only replacement registers Go signal handlers before readiness while
retaining actual terminal Ctrl-C, SIGWINCH geometry, foreground handoff and full
termios restoration. It passed 100 focused and 30 race repetitions, deliberate
failure cleanup, and fresh independent review. Production terminal handling did
not change.

## Automated checks

`make check`, `make build`, relevant logs/CLI/execution race tests and vet, and
`make infra-check TOFU=/tmp/devbox-tools/tofu` passed. The initial terminal-fixture
failure remains recorded in `/tmp/devbox-issue20-checks`; full checks passed after
its correction. Infrastructure checks ran separately from the initialized live
backend checkout.

Meaningful coverage includes historical standalone-final recovery without SSM,
strict scope/record/retention bindings, request-server timestamp expiry, missing
versus denied data, unsupported/corrupt records, bounded transient reads, known
outcome preservation and publication races. Transfer tests verify zero/binary
and generated 64 MiB streams with bounded writes, exact length/hash/EOF, truncated
and extra bytes, interrupted reads/writes, private exports, existing destinations,
symlink aliases and publication races, partial dual-export evidence, and removal
of temporary files. Status-only retrieval does not claim byte verification.

## Live gate

The independently reviewed helper is pinned at SHA-256
`c3928d489205448f734b63043a65e988e948b6c26a239f435981372c3b2f4e37`.
Its local smoke uses fake AWS/CLI processes and cannot establish live acceptance.

On September 14, 2026 (UTC), fresh export/preflight passed all 14 doctor checks in
the `personal-dev` / `joseph` scope in `us-east-2`, with no existing active worker.
One disposable On-Demand worker reached running, SSM online, bootstrap complete
and readiness ready. Independent checks matched account, request/scope tags,
template 4, AMI, instance profile, VPC/subnet/security group, required IMDSv2,
encrypted 100 GiB gp3 root with deletion on termination, and zero ingress.

All **three distinct commands passed 370 live checks**, with **20 fresh logs
processes** and no replay or emergency cleanup:

| Command | Exec result | Logs result |
| --- | --- | --- |
| Large binary stdout/stderr, workload exit 255 | `remote_exit`, exit 255 | Retrieval exit 0; exact 1,048,576-byte stdout and 786,432-byte stderr |
| Both streams empty, workload exit 0 | `remote_exit`, exit 0 | Retrieval exit 0; actual zero-byte S3 objects and verified zero-byte files |
| Same large streams after a 24-second sleep | Local wait expires after 2s: `observation_timeout`, exit 4 | Seven fresh `pending` snapshots during execution, then complete result and exact bytes with workload/retrieval exit 0 |

The delayed command ran for 24.000 seconds and finished 22.204 seconds after
local exec exit. Every pending snapshot retained its known IDs and started
metadata, without inventing a workload exit. Each logs process used a new empty
state directory, a fresh six-field storage-only descriptor, missing local SSH/
profile files, and a PATH containing only `sh` and `aws` for authentication.
No local receipt or current runtime identity was available to the logs CLI.

For every completed command, separate new CLI processes checked status-only
JSON, both file exports together, raw stdout selection and raw stderr selection.
Status reported complete publication with `verification=not_downloaded`; exports
were private 0600 files with verified exact bytes, and no temporary files remained.
Selected raw bytes appeared only on stdout and matched the corresponding complete
objects. JSON remained one metadata envelope with separate workload/retrieval
status, verified file locations and original IDs. Completed results emitted no
SSM snapshot; controlled source/probe evidence establishes absence of SSM API
dependency, without claiming that actual live history was disabled or expired.

Independent authenticated S3 reads checked all five metadata records, bindings,
encryption, request server timestamp, the shared 30-day deadline, outcome/final
agreement and both streams' entire contents, lengths and SHA-256 values. Fresh
review independently recomputed the same byte evidence and checked real pending
snapshot timings.

## Cleanup and post-teardown exports

The exact worker was terminated. Independent operator EC2 responses confirmed
that same instance terminated, its captured original root returned
`InvalidVolume.NotFound`, and scoped inventory contained zero nonterminated
instances and zero volumes. Final `devbox ls` also succeeded with no active
worker. Launch, instance, root and command IDs remain in private evidence.

After teardown, the helper accepted only a retained six-field storage descriptor
and recovered the three existing IDs without dispatch. **Twelve new logs
processes passed 243 checks**, with no skipped commands. Status, two-file export,
raw stdout and raw stderr retrieval each ran again with fresh empty local state.
All six streams matched their pre-teardown bytes and SHA-256 values; the large
streams remained complete, empty streams remained real empty objects/files, and
workload exits 255/0/0 still accompanied retrieval exit 0.

The recovery helper used only STS/S3 reads and `devbox logs`; it performed no
EC2/SSM observation, cancellation, resubmission or remote finalization. This
establishes immediate post-teardown recovery within the promised retention
period, not a month-long live retention test. Results and durable foundation
resources remain intentionally retained. Fresh independent review verified
actual execution, exports, exact cleanup and post-down bytes before merge; final
report review is recorded in the #20 plan. Parent #2 remains open for #21.
