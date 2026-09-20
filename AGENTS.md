# Working on devbox

## Project orientation

This is a Go CLI for disposable AWS development machines, with OpenTofu-managed
shared infrastructure. Current support is Linux locally, Ohio (`us-east-2`), and
Ubuntu 24.04 x86-64 workers. The only shipped workload profile is `agent`.
Manual/managed tmux sessions and agent automation are plans, not current commands.

Read [docs/development.md](docs/development.md) for the repository map and checks,
[docs/contracts.md](docs/contracts.md) for command/schema rules, and
[docs/iam.md](docs/iam.md) for permission boundaries. For user workflows use
[README.md](README.md), [setup](docs/setup.md), [configuration](docs/configuration.md)
and [usage](docs/usage.md). The [docs index](docs/README.md) links plans and evidence.

## Where to work

- `cmd/devbox` and `internal/cli`: process/terminal handling, parsing and output.
- `internal/lifecycle`, `internal/access`: launch/recovery/teardown and SSH over SSM.
- `internal/config`, `internal/identity`, `internal/doctor`, `internal/foundation`:
  configuration, scope and deployed-resource checks.
- `internal/execution`, `internal/execprotocol`, `internal/logs`, `internal/runner`
  and `cmd/devbox-runner`: remote execution and durable output.
- `internal/expiry`, `internal/expirycleanup`, `internal/cleanuplambda` and
  `cmd/devbox-cleanup`: shared expiry policy and manual/scheduled cleanup.
- `infra/`: durable foundation and S3 state bootstrap; `profiles/` and `examples/`:
  embedded profile and configuration examples. Tests are beside Go packages.

## Validation

- For Go changes run `make check` (format, module verification, build, vet, tests)
  and `make build`. Use focused package tests during development.
- For runner/cleanup changes build the relevant artifacts with `make runner` and
  `make cleanup-check`; cleanup packaging needs Python 3.
- For infrastructure/runtime integration run `make infra-check TOFU=/path/to/tofu`
  with OpenTofu 1.12.6 and committed provider locks in a separate clean checkout.
  Its `init -backend=false` must stay separate from live backend initialization.
- For acceptance-helper changes run `python3 scripts/test-expiry-acceptance.py`.
- For docs-only changes check examples against CLI help/source, relative links and
  anchors, and that `CLAUDE.md` remains a symlink to `AGENTS.md`.
- Report which checks ran and distinguish offline tests from live AWS evidence.

## Preserve these boundaries

- Scope resources by expected account, region, deployment and stable owner.
  Validate identity before mutation and revalidate exact targets before teardown.
  Tags support discovery; they do not replace IAM enforcement.
- The CLI reads a trusted exported manifest, never OpenTofu state. Fresh launches
  need a real v6 foundation; do not make an old deployment pass by editing its
  manifest version or inventing resource IDs.
- Keep launch recovery idempotent: `--resume` observes; `--retry-missing` explicitly
  requests proven missing capacity. Do not resend uncertain allocations, replace
  lost workers automatically or silently fall back from Spot to On-Demand.
- Preserve immutable expiry across retries and active work. Keep inventory,
  explicit teardown and retained logs available when launch prerequisites fail.
- Report EC2 termination and observed root-volume deletion separately. Missing
  mappings or an empty inventory never prove deletion. Preserve recovery IDs and
  partial outcomes through timeouts and failures.
- Preserve versioned JSON, stdout/stderr ownership and exit semantics. `exec`
  passes literal arguments after `--`; interruption detaches without canceling the
  remote command. `logs` reports retrieval success separately from workload exit.
- Keep credentials, private keys, state, local deployment inputs and arbitrary
  SDK/provider diagnostics out of commits and public output. Use normal AWS
  credential sources and retain the existing redaction and IAM boundaries.
- Routine development checks are offline. Infrastructure apply/destroy, live
  launches and fault injection must fit the user's authorized deployment scope;
  never treat an acceptance procedure as blanket authorization to run it.

Update user guides when behavior changes and contracts when schemas change.
Keep future work under plans, and record only observed results as acceptance.
`AGENTS.md` is the canonical instruction file; edit it instead of replacing the
`CLAUDE.md` symlink with a second copy.
