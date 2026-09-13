# Issue #17 result storage and runner foundation

Parent #2; follows the reviewed contract in #16, merged as
[PR #22](https://github.com/JosephWest2/cloud_dev/pull/22) at `c71fb0a`.
The user selected Ctrl-C detachment and 30-day result retention from submission.

## Implementation and boundaries

Add one private S3 bucket, separate from state, with SSE-S3 encryption, all public
access blocks, bucket-owner-enforced ownership, TLS enforcement and required
conditional result creation. A single result-prefix lifecycle rule expires
objects after the chosen 2–365 days and aborts incomplete multipart uploads after
one day. The default is 30 days. No versioning or force-destroy; the original
request timestamp establishes the shared logical retention deadline.

The instance role reads scoped request/result objects and the exact runner
artifact, and writes only runner records/streams. The operator reads owned
results, the exact artifact and bucket configuration; it writes only request and
acknowledgement objects. Both list only the owned result prefix. Existing scoped
instance targeting remains; add only the execution document ARN for SendCommand.
Detach does not need CancelCommand. SSM GetCommandInvocation remains region-bound
with Resource=* because AWS does not support per-command resource authorization.

Build a static Linux/amd64 Go runner and provision its content-addressed artifact
outside the expiring prefix. Ubuntu bootstrap installs the explicit AWS CLI
download dependency, verifies the artifact's actual SHA-256 and installs root-owned
configuration. Require SSM Agent >=3.3.2746.0. The separate fixed execution document
passes request ID/payload through ENV_VAR and its numeric timeout through ordinary
document-property substitution. The worker publishes its own outcome, output and
result independently of the observer. Public exec dispatch and logs retrieval
are subsequent children.

Manifest v4 exports storage/document/runner pins and changed IAM/bootstrap hashes.
Doctor checks actual resources against the export. Its artifact metadata probe is
not a claim to have verified the artifact bytes; bootstrap does that before
execution. Storage-only manifest loading remains independent of launch profiles,
SSH keys and removed worker resources. Existing inventory/down and launch-receipt
reconciliation remain usable. New workers must be launched with the new export.

Runner updates replace the currently managed S3 artifact. Finish or remove
bootstrapping workers before applying, preserve result storage, and do not launch
old template versions whose artifact has been removed. Decreasing retention must
not shorten an outstanding result promise; use a new storage scope or wait until
all existing promises expire. These limitations are documented in setup/IAM.

## Review and acceptance gates

Run Go format/module/build/vet/tests, build the CLI and runner, and run all pinned
OpenTofu formatting/validation/mock/export checks in an offline checkout. Use
meaningful controlled tests for scoped descriptor validation, configuration drift,
missing/denied AWS evidence, runner process/capture/publication behavior and
immutable record decoding. Review the integrated final change independently before
merge; preliminary review of a subset does not approve unfinished integration.

Prepare a real saved plan under the explicitly selected setup profile, review
the concrete changes, then pause for any required apply decision. Validate live
intended and denied storage writes/reads using the scoped operator and instance
roles. Keep static/mock checks distinct from actual AWS authorization evidence.
Record setup/operator account matching without storing credentials or raw state.
Any disposable worker used for enforcement checks needs observed termination,
exact root-volume deletion and scoped inventory afterward.

## Offline evidence and independent review

Final `make check`, `make build`, `make infra-check TOFU=/tmp/devbox-tools/tofu`,
runner/protocol race tests, vet and whitespace checks passed after the source
freeze. Tooling was Go `go1.27.0-X:nodwarf5 linux/amd64`, OpenTofu 1.12.6, locked
AWS provider 6.64.0 and S3 SDK v1.113.1. Offline infrastructure checks passed one
state-backend mock, nine foundation mocks and the Go/OpenTofu export/hash bridge
in a worktree separate from the initialized live backend.

Fresh infrastructure and runner subagents reviewed the complete implementation
in their respective scopes; neither implemented #17. Findings were addressed:
use an authenticated exact-prefix listing to distinguish a missing S3 object
from a denied read when possible, and document the region-wide
UpdateInstanceInformation grant as a compatibility choice. It is not an AWS
resource-scoping limitation. The final runner review approved preparation and
cancellation boundaries, uncertain claims/publication and distinct unsupported
schema handling. Race and targeted checks passed after the fixes.

The infrastructure reviewer checked operator policy size with maximum permitted
23-character scope labels: approximately 8,998 characters, below the 10,240 limit.
The frozen runner SHA-256 is
`eede5e37b4bc9e552027a2ff90fc70eb73455688f6ea37b59dde5f9aef646940`.
Live evidence and its limits are recorded separately in
[the #17 acceptance report](../acceptance/17-result-foundation.md).

## Reviewed and authorized live apply

From a separate clean checkout at `7e672fd`, initialized the existing S3 backend
under `devbox-setup` and saved a real foundation plan. Its exit was 2 (changes),
with **9 additions, 3 updates, 0 deletions**: result bucket plus six controls,
runner artifact and execution document; the existing two inline policies and
launch template update. Networking, state backend and EC2 worker allocation are
unchanged. Scoped operator inventory found zero nonterminated managed workers.
The clean checkout's rebuilt artifact matches the frozen hash above.

A third fresh subagent reviewed this actual saved plan and frozen inputs and
approved presenting it for the user's apply decision, with no blockers. New
document ARN/version values are necessarily resolved during apply; the policy
and bootstrap expressions that consume them were reviewed. Refresh records an
existing SSH Bool-to-BoolIfExists policy representation change; the planned
policy preserves the current BoolIfExists behavior.

Local recovery location: `/tmp/devbox-issue17-live-7e672fd`. The binary plan is
`infra/foundation/issue17.tfplan`; private JSON, logs and a human summary are under
the ignored `infra/foundation/acceptance-output/issue17/` directory. No state or
credentials were committed. Preserve the frozen runner binary and bootstrap
inputs; recheck hashes before applying, and regenerate/review the plan if its
inputs or remote state change.

After reviewing the incremental durable costs, the user authorized the saved
plan and scoped temporary-worker checks. Rechecked both AWS profiles against the
configured account and the frozen runner hash, then applied the exact saved plan:
exit 0. A subsequent real foundation plan returned exit 0 (no changes).

Exported a fresh manifest v4 to the protected acceptance configuration under
`/tmp/devbox-issue17-live-checks`. All 14 doctor checks passed under the explicitly
selected restricted operator profile. One On-Demand worker launched from the new
numeric template version and reached EC2 running, SSM online, bootstrap complete
and readiness ready. Direct operator EC2 inspection captured its exact instance
and root-volume IDs, required IMDSv2, encrypted 100 GiB gp3 root with deletion on
termination, and zero ingress rules on every attached security group.

The local harness initially requested an unsupported seven-minute launch timeout;
the CLI rejected it before creating a receipt or calling EC2. Preserved that
usage-error evidence, confirmed no allocated worker, and used the supported
five-minute timeout for the single successful launch.

[PR #23](https://github.com/JosephWest2/cloud_dev/pull/23) completes this slice.
Live role enforcement and worker execution passed 105 assertions. Cleanup
independently verified exact instance termination, exact root-volume deletion
and empty scoped inventory. Post-down Python recovery passed 28 assertions, and
a separate production Go storage-only probe passed 38 assertions without EC2,
SSM, SSH or writes. Full evidence and distinctions between controlled/live cases
are in the acceptance report. A fresh non-implementing subagent reviewed the
actual evidence, both acceptance helpers, final docs and PR description and
approved the merge gate with no blockers. #17 is complete; proceed to #18 only
after merging this reviewed slice.
