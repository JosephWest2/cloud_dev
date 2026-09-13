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

## Current evidence

- Go config/foundation/doctor/lifecycle targeted tests pass. An independent
  reviewer (not an implementer of #17) approved that subset; integrated
  runner/infrastructure review remains pending.
- Live authentication was unavailable for both configured profiles. The user
  was asked to refresh `aws login --profile devbox-setup`; no live apply has run.
- The user refreshed the setup login; both setup/operator STS checks subsequently
  authenticated and matched the configured account. No live apply has run.
- Final `make check`, `make build`, `make infra-check TOFU=/tmp/devbox-tools/tofu`,
  targeted runner/protocol race tests, vet and whitespace checks passed after
  source freeze. Tooling: Go `go1.27.0-X:nodwarf5 linux/amd64`, OpenTofu 1.12.6,
  locked AWS provider 6.64.0 and S3 SDK v1.113.1.
- Fresh infrastructure and runner subagents reviewed the complete implementation
  in their respective scopes; neither implemented #17. The final runner review
  approved cancellation/preparation, claim/publication failures, authenticated
  exact-prefix absence checks and distinct unsupported-schema handling.
- The runner artifact from the final offline build has SHA-256
  `eede5e37b4bc9e552027a2ff90fc70eb73455688f6ea37b59dde5f9aef646940`.
- Live plan/apply/enforcement and actual root-to-devbox execution remain pending.
  #17 and parent #2 stay open until those gates pass.

The first complete `make infra-check TOFU=/tmp/devbox-tools/tofu` passed one
state-backend mock, nine foundation mocks and the Go/OpenTofu export/hash bridge.
It ran in this offline worktree, separate from the prior initialized live
checkout. A fresh infrastructure reviewer approved the dependency graph, IAM,
storage, document and bootstrap after two findings were addressed: disambiguate
an S3 denied read using an exact-prefix listing when possible, and document that
the retained region-wide UpdateInstanceInformation grant is a compatibility
choice rather than an unsupported AWS resource-scoping limitation. The listing
fallback subsequently passed integrated tests and the final runner review; its
live evidence remains pending.

The reviewer also checked policy size with maximum permitted 23-character scope
labels: approximately 8,998 operator-policy characters, below the 10,240-character
limit. These are controlled/static checks, not live policy enforcement evidence.
