# Interactive sessions for development machines

Status: selected plan, September 16, 2026. No tmux installation, managed session
commands or live acceptance is claimed by this document. Current bootstrap does
not explicitly install/pin tmux; `agent` is the only implemented workload profile.
Related: [#56](https://github.com/JosephWest2/cloud_dev/issues/56),
[implementation chunks 5–8](../../implementation-plan.md), and deferred
[Herdr research #59](https://github.com/JosephWest2/cloud_dev/issues/59).

## Intended outcome

An operator can start intentional interactive work, observe it, disconnect and
return to the same process through existing SSH over SSM. This applies to ordinary
scripts, manual experiments and monitoring as well as coding agents. Tmux is the
selected next step; it does not replace durable results or worker lifecycle rules.

## Preserve the existing execution contract

| Workflow | Interface and semantics |
| --- | --- |
| Noninteractive commands and automated benchmarks | Existing `exec`; stdin EOF, separate stdout/stderr, exact exit status, independent remote runtime and local observation |
| Manual interactive scripts or experiments | Existing `ssh`, then a named tmux session; explicit input, detach and reconnect |
| Live manual observation | Read-only tmux attachment through SSH; observer must not affect input or the controlling terminal's size |
| Managed interactive jobs | Future `watch`/`attach` using recorded job/attempt/session identity and existing managed-resource resolution |
| Persisted results | Existing `logs COMMAND_ID`; future agent-job retrieval must preserve the command-ID contract |

Current exec captures output on the worker and publishes streams after execution.
`logs --stream` copies recorded bytes; it does not follow live output. A script
receiving stdin EOF may fail, choose a default, or mishandle EOF; it is not an
attachable waiting session. Starting tmux later cannot add a terminal to that exec.
Manual tmux output is not automatically published or assigned a command/job ID.

## Delivery 1: manual tmux baseline

This is a bounded next deliverable on the existing Ubuntu development foundation;
it need not wait for a complete repository-ready Packer image or agent selection.

- Install tmux explicitly, record its actual version with bootstrap/image
  provenance, and define how updates affect the pinned environment. A base AMI
  incidentally containing tmux is insufficient acceptance evidence.
- Document named sessions owned by the existing `devbox` user and reached through
  the existing dedicated SSH key, SSM transport and strict host-trust files.
- Demonstrate start, list, read-only observation, interactive attachment and
  explicit detach. Retain exited panes for diagnostics and record the command's
  exit status separately from the SSH/tmux client exit.
- Configure pane retention and any output recorder before starting the workload
  so immediate exits/startup output are not missed. A retained pane is bounded
  local history, not proof of complete output or successful execution.
- Define behavior for a missing session, non-terminal invocation, Ctrl-C,
  resizing, client exit and transport failure. Reattachment targets the existing
  session and never silently reruns the command.
- Carry this baseline into later repository-ready development and benchmark
  images. Installing tmux does not require running every workload through it.

Do not add `agent`, `watch` or `attach` to the implemented CLI help until those
commands exist. Manual instructions must distinguish commands run locally from
those entered on the worker. After installation, the documented workflow starts
with `devbox ssh RETURNED_NAME_OR_ID`; the session remains local to that worker.

## Delivery 2: managed interactive jobs

Implement alongside the selected agent adapter in chunk 6 and #56:

- Bind session names to immutable job/attempt IDs; reject ambiguous selection.
  Attachment and launch retries must not create duplicate processes.
- `watch NAME_OR_ID` observes read-only; `attach NAME_OR_ID` permits intervention.
  Reuse existing scope checks, readiness, authentication and host trust. Define
  an explicit selector if multiple sessions are eligible. Missing sessions do
  not start work, and client exit must not be treated as the job's exit.
- Persist actual workload exit and required publication independently of pane
  existence. Keep machine readiness separate from agent/job state and freshness.
- Capture terminal output from startup, publish periodically and at completion,
  and retrieve it after teardown. Define encoding/control sequences, combined
  streams, checksums, completeness, last verified persisted progress and retention.
  Do not relabel terminal capture as exec's separate stdout/stderr. Prefer native
  agent transcripts/events when available.
- Use structured adapter events for questions, with durable IDs, context and
  resolution. Deliver one documented external notification channel while no
  terminal is attached; bound retries/deduplication and expose delivery failure.
  Answer through interactive attachment initially. Silence is not a question,
  and timeout is never permission or an answer.

General scripts need not implement agent question events. Their initial input path
is manual attachment; automatic generic prompt detection is not promised.
Cloud dev retains job/attempt, question, publication and recovery authority if a
future backend such as Herdr is evaluated. That research does not delay tmux.

## Expiry, loss and benchmarks

Attach, detach, active work and waiting never refresh TTL. Tmux survives client
disconnect, not worker termination/root deletion. Retain diagnostics periodically;
best-effort pre-expiry publication cannot guarantee preservation during abrupt
Spot loss. Recovery requires the separate checkpoint/resume workflow, including
necessary conversation data and uncommitted workspace state.

Automated benchmarks should receive explicit inputs and produce durable structured
results without human interaction. Use tmux for manual trials or a separate
monitoring terminal. Record monitoring settings and measure overhead for sensitive
timings; terminal-backed execution can change buffering and output behavior.
Periodic durable progress/output for noninteractive exec is useful independently
of a multiplexer, but remains a separate protocol extension.

## Acceptance and failure checks

For the manual delivery, publish a matching runbook with actual version/image
provenance and distinguish offline tests from live results:

1. Launch a worker with the baseline; start an ordinary input-requesting script
   in a named session. Disconnect all clients, reconnect from a fresh client,
   answer and confirm the same PID continued with no duplicate command.
2. Attach a read-only observer beside an interactive client. Verify observer
   typing and resizing cannot affect input or the interactive terminal dimensions.
3. Exercise detach, Ctrl-C, connection loss, missing session and immediate failing
   command. Preserve the exited pane and actual workload exit without rerunning it.
4. Confirm the original expiry is unchanged throughout. Remove the worker and
   independently verify its root deletion; do not claim the old session survives.

For managed delivery, additionally cover real-agent questions with no clients,
failed/duplicate notifications, two isolated jobs, recorder interruption, large
and redrawing output, denied uploads, worker loss while waiting, and verified
post-teardown retrieval. A terminal fixture does not prove real-agent semantics.
Existing exec/logs behavior must remain compatible. Run relevant implementation
checks when code lands; documentation alone establishes no runtime acceptance.

References: [tmux session behavior](https://github.com/tmux/tmux/wiki/Getting-Started),
[read-only/size and pane controls](https://man.openbsd.org/tmux.1),
[SSH contract](../contracts.md#readiness-and-ssh-access-9), and
[durable execution contract](../contracts.md#selected-exec-and-durable-result-protocol-16).
