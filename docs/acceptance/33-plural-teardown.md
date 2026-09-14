# Issue #33 scoped plural teardown

This slice completes `down NAME_OR_ID [...]`, `down --group GROUP` and
`down --all [--yes]` after the reviewed group launch integration in
[PR #39](https://github.com/JosephWest2/cloud_dev/pull/39). No live resources were
changed. Fresh live teardown and exact disposable-root cleanup remain #34.

## Selection, consent and execution

Selection resolves and freezes exact managed IDs before execution. Plan internals
are private; preview candidates and result metadata are independent copies.
Group/all selection requires complete scoped inventory. An incomplete scan
retains known IDs but authorizes none. Explicit selectors may resolve independently:
invalid or ambiguous targets report errors while other verified IDs can proceed.
Generated and legacy friendly names use the existing cloud resolver. Repeated
IDs are deduplicated, and ambiguity never authorizes every matching worker.

`--all` always previews account, region, deployment, owner, count and exact IDs
on stderr, including with `--yes`. Without `--yes`, a real terminal must supply
a newline-terminated `y` or `yes` answer. Negative/other answers decline with
exit 0. EOF, incomplete/oversized input and piped input without `--yes` fail with
exit 2; cancel/deadline exits 4. A failed preview or prompt cannot grant consent.
Input is bounded to 1024 bytes; waiting for input is cancellable and never closes
the caller's shared stdin. The callback receives only the frozen candidates.

Immediately before each termination, the backend reads the exact ID and
revalidates account/managed scope and selected name/group membership. It never
repeats discovery after confirmation. New workers cannot join the confirmed set.
At most four workers execute concurrently under one command deadline (20s by
default, at most 5m). Each AWS request is capped at 15s; each inventory lookup is
bounded to 128 pages. Denied, protected, slow or uncertain workers retain their
results without hiding peers.

Pre-mutation root mappings survive later missing mappings and errors. Termination
requested, unknown, denied and observed are reported separately from root-volume
deletion. Only exact observed EC2 termination plus verified deletion of captured
roots contributes to cleaned_count. Missing roots, empty successful volume
responses, inaccessible or retained roots do not prove cleanup. No explicit
volume deletion, result-bucket mutation or launch-ledger mutation occurs.

One schema-v2 JSON envelope reports selected, terminated and cleaned counts plus
per-worker identity, termination state, roots and errors. Partial verified cleanup
returns 3; no verified cleanup returns 1; timeout/interruption takes precedence
with exit 4. Decline and successful empty selections do not claim any deletion.
Final output failures print retained cleanup identities and exact-ID recovery
commands to stderr. Inventory and teardown have no launch profile, manifest or
receipt prerequisite.

## Controlled verification

CLI integration exercises affirmative/declined confirmation, EOF, piped yes,
explicit `--yes`, cancellation and a worker created while the prompt is open.
It checks one JSON result, exact confirmed IDs, group scoping, mixed targets,
deduplication and repeated cleanup. Confirmation tests also use a real PTY,
actual terminal detection, short/failed output and bounded input.

Backend tests cover ambiguous and malicious targets, scope/name/group drift on
the actual final read, frozen plan copies, incomplete/cyclic/excessive pagination,
independent denial/protection/uncertainty, disappearance, already-terminated
workers and separate root outcomes. Concurrency and deadline tests preserve all
queued IDs.

September 14, 2026 (US/Central): `make check`, `make build`, full lifecycle/CLI
race tests and `git diff --check` passed. Two fresh independent reviewers approved
the final implementation: `review33_confirmation` covered confirmation, routing,
output and documentation; `review33_backend` covered selection, final target
verification, termination, roots and aggregate outcomes. Both ran focused race
checks; neither implemented this slice.

Review caught a truncated-output case where a writer returned a short byte count
without an error. A shared output adapter now converts that into an error in
teardown and batch output. JSON and text regressions verify exit 1 and retention
of every known instance/root ID on stderr even after successful cleanup.

No live termination or infrastructure change occurred during this slice. #34
must record real restricted-operator acceptance and cleanup before #3 can close.
