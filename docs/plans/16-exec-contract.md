# Issue #2 ordered implementation and #16 contract gate

Based on merged MVP 1 at `6cb875b`. The user requested sequential subissues and
fresh subagent review before every merge. Work stops for user decisions or tests
requiring the user's setup; no child or parent closes with required acceptance
still unrun. The user confirmed Ctrl-C detachment and 30-day retention from
submission on September 13, 2026.

## Sequence and merge gates

| Child | Change and required evidence | Status |
| --- | --- | --- |
| #16 | Contract, literal argv codec and observer-independent publisher prototype; fresh review | Complete; checks/reviews below |
| #17 | Private result storage, scoped permissions, pinned working runner/document, manifest v4; offline checks, reviewed live plan and storage enforcement evidence | In progress; see [#17 plan](17-result-foundation.md) |
| #18 | Scoped one-attempt dispatch and integrated runner publication; actual SDK lost-response test and live publication | Not started |
| #19 | Observation, exact remote status, timeout/interruption semantics; process tests and live timeout/Ctrl-C | Not started |
| #20 | Complete S3 retrieval/exports with cloud-only recovery; binary/checksum, missing/denied/corrupt data and post-down recovery | Not started |
| #21 | `docs/acceptance/02-exec-logs.md`, requirement/evidence matrix, reproducible full live exercise and independently verified cleanup | Not started |

Each child gets its own reviewable change/PR. A newly assigned reviewer must
inspect that child's final diff, relevant tests and issue gates; an implementation
agent is not its own fresh reviewer. Resolve findings and rerun affected checks
before merging. Then continue to the next child in order. #2 closes only after
all six children and the parent's own live acceptance gate pass.

## Selected architecture

The normative protocol, exact CLI behavior, record schema, time bounds, state and
failure matrices, examples and retention are in
[contracts](../contracts.md#selected-exec-and-durable-result-protocol-16).
Use one private S3 result store and a root-owned Go runner on the existing Ubuntu
worker. The runner executes literal argv as `devbox`, freezes capture and writes
its own durable outcome and complete result. A pre-dispatch public-ID request
and the runner's `SSM_COMMAND_ID` mapping eliminate a waiting-client finalizer.
No scheduled worker, Lambda, DynamoDB index or custom AMI is necessary for this
bounded design. Worker loss or missing records remain honest incomplete/unknown
states. Native SSM inline text and its response code cannot prove the workload
outcome or byte completeness.

Implement v4 manifest and result descriptor validation separately from general
launch/SSH prerequisites. An old trusted result descriptor must still retrieve
completed records after instance/document/template removal. Preserve current
scope-only inventory/down and launch-receipt reconciliation while new exec needs
the upgraded foundation and newly bootstrapped workers.

The production runner belongs with #17's document/artifact integration, with #18
completing the user dispatch path. Build Linux/amd64 runner artifacts explicitly,
pin their content hash and install them with declared bootstrap download tools;
do not provision a placeholder document and report execution ready. Any change
to the selected wire schema is reviewed and reconciled here before dependent
merges.

## #16 executable evidence and limits

`internal/execprotocol` supplies the strict payload codec and a Linux test-only
publisher harness. The latter starts actual observer/worker/workload processes
and uses a file-backed object-store stand-in. Kill the observer immediately after
simulated acknowledgement and during execution; the worker must still commit
both streams and a final hash/length/status record. Read results from a fresh
reader. Test literal argument boundaries, binary and large output, empty streams,
nonzero exits and denied/partial publication. This is controlled local evidence
of process/data-flow invariants, **not** live SSM/S3 acceptance, production user
switching, full process-group timeout enforcement, cloud IAM, or implementation
of the entire record schema.

Run `go test ./internal/execprotocol`, `make check` and `make build`. #16 changes
no infrastructure; the next child runs OpenTofu checks from a separate offline
checkout. Record the actual fresh review and checks before merging this child.

## Human checkpoints

- Decisions: Ctrl-C detaches and default retention is 30 days from submission;
  both confirmed. Routine protocol details are specified in the contract.
- #17: prepare and review the concrete live infrastructure plan before requesting
  any needed apply decision. Authenticate or ask the user to authenticate the
  explicitly selected setup/operator profiles if necessary. Do not substitute
  static policy review for live intended/denied storage enforcement evidence.
- #18–#21: use one disposable On-Demand Ubuntu devbox. Pause for any required
  local interaction and provide exact commands/expected outcomes. Keep command,
  instance and root-volume IDs for recovery and cleanup.
- End each live worker exercise with observed termination, exact root-volume
  deletion and scoped inventory. Retain result storage for post-down recovery;
  full durable teardown is a separate explicit decision.

## #16 review and verification

September 13, 2026: `make check` and `make build` passed. Package tests,
`go test -race ./internal/execprotocol`, `go vet ./internal/execprotocol` and
`git diff --check` passed. Meaningful coverage is named
`TestLiteralPayloadRoundTrip`, `TestPayloadRejection`, `TestPayloadSizeBoundary`,
`TestPrototypeObserverDisappearance`, and
`TestPrototypePublicationFailureAndImmutability`.

Two fresh subagents independently reviewed the code/prototype and the complete
contract before merge; neither implemented this slice. Both approved after:

- Correcting the hard SSM step timeout to a separate decimal parameter with
  ordinary property substitution. ENV_VAR substitution there would leave an
  unparseable timeout and trigger the agent's 3600-second fallback.
- Granting scoped worker reads for reconciliation of uncertain object writes
  and requiring conditional creation in the bucket policy.
- Aligning the examples with the distinction between a runner-recorded workload
  timeout and SSM killing the wrapper without a known workload outcome.

This records controlled local evidence only. No live resources have been changed
by this contract slice. Required production execution/IAM/storage/timeout and
live evidence remains in the subsequent children.
