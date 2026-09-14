# Issue #21 and parent #2 final acceptance

Follows merged #20 / PR #26 (`17e2f18`). The final fixture and all six command
cases passed, including complete output recovery after independently verified
worker/root cleanup. [PR #27](https://github.com/JosephWest2/cloud_dev/pull/27)
records the [parent acceptance evidence](../acceptance/02-exec-logs.md).
All five preceding children are closed after fresh independent review.
The user selected Ctrl-C detachment and default 30-day retention and authorized
the scoped foundation and disposable-worker exercises. This slice required no
new behavior decision or infrastructure apply.

## Tested source and scope

The clean checkout `/tmp/devbox-issue21-live-17e2f18` builds CLI SHA-256
`841452e211347306f1c03cdf68be48384f1af9dcdc1e3b792b4b0652a2fa3457`.
Fresh `make check`, `make build` and
`make infra-check TOFU=/tmp/devbox-tools/tofu` passed there; its offline backend
initialization is separate from the accepted live foundation checkout. Retain
#18's deployed template version 4 and runner
`e21c0536522661235b6ea5cb22bfe3b9bd66081af640570f7a151f51db8a74f6`.
The latest local runner build is not automatically applied.

Used `devbox-setup` only to export the existing foundation manifest and
`devbox-operator` for lifecycle, SSH, exec and logs in account `464557813916`,
region `us-east-2`, deployment `personal-dev`, owner `joseph`. One temporary
On-Demand worker named `issue21-live` was launched after fresh helper review,
14 passing doctor checks and inventory with no nonterminated instances.
Independent inspection verified request, instance/profile/network/runtime,
root disk, IMDSv2 and zero-ingress bindings before commands ran.

## Completed acceptance

Fresh non-implementing reviewers checked the acceptance/SSH/lifecycle helpers
and parent runbook. The actual `devbox ssh` shell cloned public
`pypa/sampleproject` revision `621e4974ca25ce531773def586ba3ed8e736b3fc`,
with Git 2.43.0 and Python 3.12.3 on Ubuntu 24.04.
The fixture uses Python's standard-library unittest; no pip, project installation
or custom AMI is required.

Six independently tracked commands covered a passing fixture check in text mode,
deliberate fixture failure in JSON, literal arguments/cwd/nonroot environment,
large binary stdout/stderr with exit 255, real terminal Ctrl-C with fresh-client
recovery, and an actual execution timeout. Every stream was verified through
logs and independent S3 reads. A helper assertion incorrectly compared fractional
payload time to whole-second runner metadata after the fifth submission.
The preserved evidence proved successful remote completion. A reviewed precision
fix and boundary regressions allowed recovery of those five original IDs; only
the independently proved unattempted timeout ran in a separate guarded output
directory. No command was replayed and no product code changed.

After all six results had complete byte baselines, exact worker
`i-0187d2a6a708e198e` was terminated, original root
`vol-0e08ebaac53219717` was absent and scoped inventory had zero nonterminated
instances or volumes. Post-down recovery passed 569 assertions and 30 fresh
logs processes across the preserved five-plus-one runs. All twelve streams,
1,836,085 bytes total, matched their original complete records and bytes.
Each CLI used empty local state and only the six-field storage descriptor.
The runbook records IDs, checksums, UTC times, versions and exact commands, and
distinguishes actual live evidence from controlled failure/history/expiry tests.
The durable foundation and result storage remain intentionally deployed;
full teardown is a separate action.

The final fresh non-implementing review covers the requirement matrix, actual
evidence, helper correction, cleanup and final documentation before merge.
Merge #27 to close #21, then close parent #2 only after confirming all six child
issues are closed. No required live test or cleanup remains unrun.
