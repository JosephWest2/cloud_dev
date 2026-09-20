# devbox documentation

Start with the [project README](../README.md) for installation, AWS setup and a
first worker. These guides cover the details as you need them.

## Setup and everyday use

| Guide | Use it for |
| --- | --- |
| [Foundation setup](setup.md) | Tools, AWS identities, provisioning, manifest export, upgrades and full foundation teardown |
| [Configuration and checks](configuration.md) | Profile selection, path/option precedence, SSH prerequisites and `doctor` |
| [Usage](usage.md) | Groups, SSH/editor access, recovery, worker lifetime, cleanup, execution and logs |
| [Arch packaging](arch-packaging.md) | Packages without an AUR account, validation, GitHub releases and eventual AUR publication |
| [Cleanup operations](runbooks/cleanup.md) | Scheduled cleanup health, retained failure evidence and recovery |

## Technical reference and contributing

| Document | Use it for |
| --- | --- |
| [CLI contracts](contracts.md) | Versioned schemas, output, exit codes and lifecycle/execution protocols |
| [IAM boundaries](iam.md) | Operator, worker, setup and cleanup permissions |
| [Development](development.md) | Source layout, builds, offline checks and documentation conventions |
| [Agent instructions](../AGENTS.md) | Working rules for coding agents (`CLAUDE.md` links to the same file) |

## Acceptance evidence and plans

Acceptance records describe particular tested deployments and commits, not the
health of your deployment. The main records are [lifecycle](acceptance/01-lifecycle.md),
[execution and logs](acceptance/02-exec-logs.md), [Spot groups](acceptance/03-spot-groups.md)
and [scheduled expiry](acceptance/04-expiry.md). See [all acceptance records](acceptance/)
for focused checks and limitations. Literal live Scheduler retry exhaustion
remains unproved in [#58](https://github.com/JosephWest2/cloud_dev/issues/58).

The [implementation roadmap](../implementation-plan.md),
[product proposal](../cloud-dev-tool-plan.md), [design decisions](../design-decisions.md)
and [scoped plans](plans/) include historical choices and future work; consult the
current guides and code for shipped behavior. The next interactive-workflow plan
is [manual tmux followed by managed sessions](plans/05-interactive-sessions.md).
Tmux is not explicitly installed by current bootstrap, and `agent`, `watch` and
`attach` commands are not implemented. Repository-ready and benchmark images
remain planned; possible [Herdr integration](https://github.com/JosephWest2/cloud_dev/issues/59)
is deferred research.
