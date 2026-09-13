# Issue #19 observation and detachment acceptance

This slice verifies exact workload status, bounded observation and local
detachment for parent #2. The user selected Ctrl-C detachment and 30-day retention
from submission. The `logs` command and complete parent fixture exercise remain
in #20 and #21.

## Reviewed source and deployment

The live CLI was built from clean source `0045fd0`; its SHA-256 is
`8a5bb0cd51141b7aecf88178adc908144c72cddc0c45774fbddfe001e7c767de`.
It uses the accepted #18 launch template version 4 and runner SHA-256
`e21c0536522661235b6ea5cb22bfe3b9bd66081af640570f7a151f51db8a74f6`.
No infrastructure apply or runner replacement is required for this client slice.
The private live evidence directory is `/tmp/devbox-issue19-live-checks`.

Fresh non-implementing subagents separately reviewed storage authentication,
observation, and CLI/supervisor integration. Review found credential-source
denial could enter a missing-object fallback, and terminal SSO refresh failures
could become transient retries. The corrected provider boundary preserves the
shared credential cache and returns typed, sanitized errors before SDK wrapping.
Controlled SDK tests verify no object lookup follows source credential denial.

A deterministic real-subprocess test reproduced a supervisor completion race:
the child emitted completed exit-0 JSON and exited normally, but concurrent
context cancellation made the parent return 4. The supervisor now preserves
the child's actual normal exit status, and both processes retain signal handlers
until process exit. Repeated signals cannot replace an established completion.
Bounded cleanup of local credential-helper descendants remains in place.

The live helper was independently reviewed at SHA-256
`bea66ddbc639cff42adf1e9b17951d0cc28b8b6b41ff32dccce18592ff31d767`.
It atomically records attempts, refuses directory reuse and never automatically
resubmits an uncertain command. Its recovery mode performs only STS and scoped
S3 reads. Each exec runs with empty local state, missing SSH/workload-profile
files and a PATH containing only `aws` and `sh` for the credential bridge.

## Automated validation

`make check`, `make build`,
`go test -race ./internal/execution ./internal/cli ./cmd/devbox`, separate protocol
race/vet checks, and `make infra-check TOFU=/tmp/devbox-tools/tofu` passed.
Offline infrastructure checks ran outside the initialized live backend checkout.

Meaningful state tests cover exact workload exits 0/1/2/4/255, raw SSM response -1,
delayed visibility, pending/delayed/cancellation-pending states, bounded transient
retries, terminal credentials, access denial, delivery/wrapper/workload/local
timeouts, record disagreement, expiry and completion races. Supervisor tests run
actual subprocesses for SIGINT, SIGTERM, repeated signals and a controlling PTY;
fresh review also repeated those tests 25 times. These controlled tests cover
unsafe-to-force service failures and precise races; they are separate from live
AWS evidence.

## Live gate

On September 13, 2026, a fresh manifest export passed all 14 doctor checks under
`devbox-operator` in the `personal-dev` / `joseph` scope in `us-east-2`. Initial
scoped inventory contained no active worker. One disposable On-Demand worker
reached running, SSM online, bootstrap complete and readiness ready. Independent
inspection verified the launch request and scope, AMI/template pins, instance
profile, VPC/security group, required IMDSv2, encrypted 100 GiB gp3 root with
deletion on termination, and zero ingress on every attached security group.

All **nine distinct commands passed 361 live assertions**, without replay:

| Case | Local result | Independently recovered result |
| --- | --- | --- |
| Ordinary exits 0, 1, 2, 4, 255 | `remote_exit`, exact original code | Same workload exit, complete publication and exact binary stdout/stderr |
| Remote timeout, 2-second limit on 30-second sleep | `execution_timeout`, exit 4 | Recorded execution timeout, signal 15, no ordinary exit; exact 55-byte captured stdout and empty stderr |
| Local wait expires after 2 seconds | `observation_timeout`, exit 4 | Workload exits 0 and finishes 10.241 seconds after the local CLI exits |
| Actual terminal Ctrl-C | `interrupted`, exit 4 | Workload exits 0 and finishes 24.881 seconds after the local CLI exits |
| SIGTERM to supervisor | `interrupted`, exit 4 | Workload exits 0 and finishes 24.299 seconds after the local CLI exits |

The Ctrl-C helper supplied a controlling PTY, verified the supervised worker was
the foreground process group, and wrote terminal byte `0x03`. The signal arrived
5.110 seconds after the remote workload began; the local CLI exited 8 ms later.
SIGTERM targeted the actual supervisor 5.700 seconds after remote start; it
returned in 1 ms. Both executions finished after detachment with their original
IDs, complete output and no remote cancellation. All dedicated local process
groups stopped normally; no emergency cleanup was required.

Every local result contained one newline-terminated JSON envelope, exact retained
public/SSM IDs, appropriate outcome and scoped recovery instructions. Connected
results had `durable_state=final`; detached results retained a validated started
snapshot. Each available SSM snapshot used allowlisted wrapper state and a
separate raw response code. All five metadata records agreed on scope, command,
instance, document, runner and payload. Started/outcome/result shared one 30-day
submission-based deadline, matching the request's server timestamp and retention.
Authenticated independent reads checked encryption, full lengths, SHA-256 and
expected bytes for both streams, including the timeout's known-empty stderr.

## Cleanup and retained recovery

The exact worker was terminated successfully. Independent operator EC2 reads
confirmed that instance terminated, its captured original root returned
`InvalidVolume.NotFound`, and scoped inventory contained zero nonterminated
instances and zero volumes. Final `devbox ls` also succeeded with no active
worker. Exact launch, instance and root IDs remain in private evidence.

After teardown, all nine commands passed **318 recovery assertions**, with no
skipped or replayed cases. The helper used only STS/S3 reads and independently
verified unchanged status, identity, deadlines and complete stream bytes. It
required no live worker, EC2/SSM observation or waiting-client finalizer. This
establishes immediate post-teardown persistence within the selected retention
period, not a month-long live retention test.

Fresh independent review approved the actual nine-command evidence, cleanup and
retained recovery. Final report approval is recorded in the #19 plan. The result
bucket and durable foundation remain intentionally deployed; full teardown is a
separate decision.
