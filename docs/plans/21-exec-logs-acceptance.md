# Issue #21 and parent #2 final acceptance

Follows merged #20 / PR #26 (`17e2f18`). All five preceding children are closed,
with fresh independent reviews, passing implementation checks and their required
live evidence. The user selected Ctrl-C detachment and default 30-day retention,
and authorized the scoped foundation and disposable-worker exercises. No new
behavior decision or infrastructure apply is needed for this acceptance slice.

## Tested source and scope

The clean checkout `/tmp/devbox-issue21-live-17e2f18` builds CLI SHA-256
`841452e211347306f1c03cdf68be48384f1af9dcdc1e3b792b4b0652a2fa3457`.
Fresh `make check`, `make build` and
`make infra-check TOFU=/tmp/devbox-tools/tofu` passed there; its offline backend
initialization is separate from the accepted live foundation checkout. Retain
#18's deployed template version 4 and runner
`e21c0536522661235b6ea5cb22bfe3b9bd66081af640570f7a151f51db8a74f6`.
The latest local runner build is not automatically applied.

Use `devbox-setup` only to export the existing foundation manifest and
`devbox-operator` for lifecycle, SSH, exec and logs in account `464557813916`,
region `us-east-2`, deployment `personal-dev`, owner `joseph`. Create one temporary
On-Demand worker named `issue21-live` after fresh helper review and initial
inventory with no nonterminated instances. Independently verify exact request, instance/profile/network/runtime,
root disk, IMDSv2 and zero-ingress bindings before running commands.

## Remaining completion gate

Freshly review the local acceptance/SSH helpers and the parent
[runbook](../acceptance/02-exec-logs.md). Through the actual `devbox ssh` shell,
install only missing Git/Python tools and clone pinned public
`pypa/sampleproject` revision `621e4974ca25ce531773def586ba3ed8e736b3fc`.
The fixture uses Python's standard-library unittest; no pip, project installation
or custom AMI is required.

Execute six independently tracked commands: passing fixture check in text mode,
deliberate fixture failure in JSON, literal arguments/cwd/nonroot environment,
large binary stdout/stderr with exit 255, real terminal Ctrl-C with fresh-client
recovery, and an actual execution timeout. Verify all streams through logs and
independent S3 reads. Preserve every attempt and recovery ID; never replay an
uncertain command. Distinguish controlled failure/history tests from actual live
evidence rather than forcing unsafe service failures.

After all results are persisted and verified, terminate the exact worker, verify
the captured original root absent and scoped inventory without nonterminated
instances or volumes, then recover all
six results again using new processes, empty local state and only the retained
storage descriptor. Record public recovery IDs, hashes, tool versions, exact
commands, timing evidence and any limitations in the runbook. Retain the durable
foundation and result storage intentionally; full teardown is a separate action.

Have fresh non-implementing subagents review the final requirement matrix,
actual live evidence, cleanup, documentation and any demonstrated fixes before
merging. Close #21 and parent #2 only when every required gate passes; no required
unrun test or incomplete cleanup can be represented as completion.
