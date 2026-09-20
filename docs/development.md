# Developing devbox

The repository contains a Go CLI and two remote runtimes, plus OpenTofu definitions
for the durable AWS foundation. Start with [user setup](../README.md) to operate
it, and [CLI contracts](contracts.md) before changing public behavior.

## Repository map

| Path | Responsibility |
| --- | --- |
| `cmd/devbox` | CLI entry point, Linux process supervision and terminal handling |
| `internal/cli` | Argument parsing, dispatch, text/JSON output and exit codes |
| `internal/config`, `internal/identity`, `internal/doctor` | Configuration, AWS identity and prerequisite checks |
| `internal/foundation` | Manifest validation and verification of deployed resources |
| `internal/lifecycle` | Fleet launch, inventory, shared recovery, readiness and teardown |
| `internal/access`, `internal/sshkey` | SSH over SSM, generated SSH configuration and key validation |
| `internal/execution`, `internal/execprotocol`, `internal/logs` | Command submission, durable result protocol and output retrieval |
| `cmd/devbox-runner`, `internal/runner` | Linux worker runtime and result publication |
| `internal/expiry`, `internal/expirycleanup` | Shared expiry policy and cleanup service |
| `cmd/devbox-cleanup`, `internal/cleanuplambda` | Scheduled Lambda cleanup adapter |
| `infra/state-bootstrap`, `infra/foundation` | S3 state backend and durable infrastructure |
| `profiles`, `examples` | Embedded workload profile and example local configuration |
| `scripts` | Artifact packaging and acceptance evidence helpers |
| `docs/acceptance`, `docs/plans` | Recorded validation and scoped design/implementation plans |

The CLI consumes a trusted deployment manifest exported by OpenTofu; it does not
parse infrastructure state. Workers are allocated by the CLI, while networking,
IAM, launch templates, result storage and scheduled cleanup belong to the
foundation. AWS inventory enables discovery after local state loss; permanent
shared launch records support allocation recovery.

## Build and check

Use Go 1.24 or newer and Make from the repository root:

```sh
go mod download
make check
make build
./bin/devbox version
./bin/devbox --help
```

`make check` checks Go formatting, verifies modules, builds all packages, runs
`go vet`, and runs `go test ./...`. Tests live beside their packages. Use targeted
package tests while iterating, then run the full check for Go changes.

Without Make, build with
`go build -trimpath -buildvcs=false -o bin/devbox ./cmd/devbox` and install with
`GOBIN="$HOME/.local/bin" go install -trimpath -buildvcs=false ./cmd/devbox`.
`go.mod` and `go.sum` pin dependencies; do not rewrite them as an unrelated cleanup.
To reproduce a binary, use the same commit, Go toolchain version (`go version`),
GOOS and GOARCH. Builds exclude checkout paths and VCS metadata.

`make build VERSION=1.2.3` and `make install VERSION=1.2.3` set the CLI release
version through `internal/cli.Version`. The default remains `0.1.0-dev` for
unversioned development builds. Text and JSON output use the same value.
See [Arch packaging](arch-packaging.md) for package checks and release automation.

## Infrastructure and runtime artifacts

Infrastructure uses OpenTofu **1.12.6** and the committed AWS provider **6.64.0**
lockfiles. Python 3 is needed for cleanup packaging and infrastructure checks.

```sh
make runner
make cleanup-check
make infra-check TOFU=/path/to/tofu
```

`make runner` builds the pinned Linux/amd64 execution runner.
`make cleanup-check` builds, packages and verifies the Lambda artifact.
`make infra-check` builds both artifacts, checks OpenTofu formatting, initializes
with `-backend=false`, validates both roots, runs mock-provider tests and verifies
manifest exports with Go tests. Run it in a **separate clean checkout**, apart
from live S3-backend initialization and local deployment inputs. These offline
checks do not apply infrastructure or establish live acceptance.

Runtime or infrastructure changes should run the relevant artifact checks and
`infra-check`. Keep artifact bytes fixed from deployment plan through apply;
see [foundation provisioning](setup.md#3-apply-the-foundation-and-export).
The acceptance-helper tests are also local-only:

```sh
python3 scripts/test-expiry-acceptance.py
```

## Continuous integration

The [CI workflow](../.github/workflows/ci.yml) runs on every pull request, pushes
to `main`, and manual dispatch. It runs two parallel jobs on fresh Ubuntu 24.04
checkouts, using the Go version from `go.mod` and the runner's Python 3:

| Check | Commands and coverage |
| --- | --- |
| `Go and helpers` | `go mod download`, `make check`, `make build`, CLI `version`/`--help` smoke checks, and `python3 scripts/test-expiry-acceptance.py` |
| `Infrastructure and runtimes` | `make infra-check TOFU=tofu` with OpenTofu 1.12.6: runner build, cleanup packaging, formatting, validation, mock-provider tests and manifest export contracts |

Use the commands above to reproduce failures locally; infrastructure checks still
require a separate clean checkout. Ordinary `make check` skips the OpenTofu export
contract, so both CI jobs are needed. Each job logs its tool versions, has a
20-minute timeout, and caches Go modules/builds using `go.sum`. Infrastructure
working directories and state are not cached. New runs cancel superseded runs
for the same event and PR or branch.

CI downloads dependencies but uses no AWS credentials or deployment inputs and
does not perform live AWS operations. Its results are offline validation, not
live acceptance evidence. Actions are pinned to full commit SHAs with release
comments; update both together. The workflow grants only repository read access
and does not persist checkout credentials.

Keep the two check names above stable when configuring required checks for
`main`. Run all jobs even for documentation changes so required checks are always
reported. Validate workflow edits with `actionlint` and confirm both jobs pass on
the PR before merging.

## Documentation and validation

Keep the [README](../README.md) focused on setup and first use. Put detailed user
behavior in [configuration](configuration.md) or [usage](usage.md), protocol rules
in [contracts](contracts.md), and operational procedures in [runbooks](runbooks/cleanup.md).
Keep coding-agent guidance in [AGENTS.md](../AGENTS.md); `CLAUDE.md` is its relative
symlink. Check relative links and heading anchors when moving material.

The [documentation index](README.md) separates current guides, acceptance evidence
and future plans. Plans are not proof of shipped behavior. Record actual checks
and limitations; local mocks do not prove AWS authorization, bootstrap, SSH or
scheduled cleanup. Live validation uses the corresponding acceptance runbook and
an explicitly selected deployment, with instance and root-volume evidence retained.
