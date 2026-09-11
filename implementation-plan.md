# Cloud dev implementation plan

Status: implementation plan based on [the product proposal](cloud-dev-tool-plan.md). The CLI/configuration foundation is implemented in issue #6; no AWS deployment has been performed.

Each numbered chunk delivers a usable workflow through infrastructure, CLI, configuration, and documentation. Its human acceptance test is the completion gate. Internal PRs may be smaller, but an infrastructure-only or CLI-only PR does not complete a chunk.

## GitHub MVP issues

Track the interactive release in [#5](https://github.com/JosephWest2/cloud_dev/issues/5). Implementation issues include dependencies, open decisions, and human acceptance tests:

1. [#1 — Launch, connect to, and destroy one Ubuntu devbox](https://github.com/JosephWest2/cloud_dev/issues/1)
2. [#2 — Run remote development commands and recover complete logs](https://github.com/JosephWest2/cloud_dev/issues/2)
3. [#3 — Launch and manage groups of Spot devboxes](https://github.com/JosephWest2/cloud_dev/issues/3)
4. [#4 — Expire devboxes automatically while the local client is offline](https://github.com/JosephWest2/cloud_dev/issues/4)

## Decisions to settle

| Decision | Proposed starting point | Needed before |
| --- | --- | --- |
| First release | **Confirmed:** interactive devboxes, followed by coding agents | Decided |
| Stack | **Confirmed:** Go CLI using the AWS SDK for Go v2, OpenTofu for durable infrastructure, TOML for user configuration and machine profiles | Decided |
| AWS scope | **Confirmed:** one personal account and region per configuration; explicit profile and stable owner identifier | Decided in #6 |
| Region | User prefers central US near Chicago; propose Ohio (`us-east-2`), pending selection | Chunk 1 |
| Networking | Dedicated VPC; public IPv4 for outbound access; no inbound security-group rules | Chunk 1 |
| Remote access | **Confirmed:** real SSH over SSM, including editor and file-transfer support; SSM Run Command remains proposed for noninteractive `exec` | Decided in #6; implement sshd, authentication, host-key verification and proxy in #7/#9 |
| Local platform | **Confirmed:** Linux, starting with Arch Linux | Decided in #6 |
| Base OS | **Confirmed:** Ubuntu LTS | Decided |
| Image and architecture | Propose x86-64; select the Ubuntu LTS release and pin the actual AMI ID with provenance | Chunk 1 |
| Spend controls | Explicit On-Demand opt-in, configurable instance count limit, proposed 2-hour default TTL once cleanup ships | Chunks 1–4 |
| Agent contract | Select one agent, repository, authentication method, and representative issue | Chunk 5 |
| Agent authority | Proposed: push a dedicated branch and open a draft PR; no merge; clarify checkpoint contents and failure retention | Chunk 6 |
| GitHub identity | Select GitHub App or limited token; separate agent and runner credentials | Chunks 5 and 9 |
| CI trust and trigger | Trusted repository jobs first; decide whether fork PRs are supported; hosted launcher workflow before webhook autoscaling | Chunk 9 |

The first-release priority, Go + OpenTofu + TOML stack, Ubuntu LTS base OS, personal account scope, Linux/Arch local target, and SSH-over-SSM editor/file-transfer access are confirmed. Other entries remain proposals. The user has no existing VPC or authentication constraints and requested an explanation of networking tradeoffs before choosing. See [decision notes](design-decisions.md) for confirmed decisions and network comparisons.

## Shared implementation contracts

- OpenTofu owns durable resources: networking, IAM, launch templates, storage, and later scheduled cleanup. The CLI owns disposable EC2 instances. Instance lifecycles do not run through an infrastructure apply.
- Implement the CLI in Go using the AWS SDK for Go v2. Go was selected for implementation and maintenance simplicity. Use TOML for user configuration and versioned machine profiles; OpenTofu infrastructure definitions use HCL. Keep AWS SDK mutations separate from infrastructure properties managed by OpenTofu.
- Versioned profile files describe workload choices. OpenTofu publishes a versioned, non-secret deployment manifest with resource identifiers; the CLI reads it without parsing infrastructure state. Resolve profile/image aliases to an exact AMI ID and launch-template version before launch and record both.
- Use the normal AWS credential chain. Scope inventory and mutations by account, region, deployment, and owner. Validate the expected account before a mutation. Tags identify resources but do not replace IAM enforcement.
- Tag at creation: `ManagedBy`, `Deployment`, `Owner`, `Profile`, `Name`, `RequestId`, creation time, and optional group/expiry/workload metadata. Tag attached resources where supported. EC2 inventory remains authoritative; local cache loss must not lose machines.
- Use immutable instance/request identifiers internally. Reject ambiguous friendly names and accept explicit instance IDs only after validating managed-resource scope. EC2 tags cannot guarantee atomic name uniqueness; concurrent name collisions must remain safe to inspect and resolve.
- Report EC2 state, SSM connectivity, and workload/bootstrap readiness separately. Bound retries and waits. A failed wait must print recoverable instance/request IDs and teardown instructions.
- Reuse an idempotency token for retries of the same launch request. Reconcile uncertain outcomes by request tags before launching again. Never describe a partial launch as full success or silently switch purchasing markets.
- Require confirmation for `down --all` unless `--yes` is supplied; noninteractive use without `--yes` fails. Revalidate resource scope at termination. Root volumes are encrypted and delete on termination; require IMDSv2. Keep secrets out of state, images, user data, command arguments, and logs.
- Introduce structured output with each command; diagnostics go to stderr. Define distinct outcomes for success, usage/config errors, remote command failure, timeout, and partial completion.

## 1. Launch, connect to, and remove one machine

**User outcome:** From a fresh checkout and an authenticated AWS profile, launch a disposable Linux machine, use a shell, and remove it without the AWS Console.

Deliver together:

- Minimal CLI packaging, user config, one `agent` profile, and `doctor` checks for identity, configuration, and local Session Manager prerequisites.
- OpenTofu state bootstrap and documented migration to an encrypted, versioned S3 backend with locking; ignore local state. One VPC/subnet, IAM roles, security group, and launch template. Export the deployment manifest.
- `up agent --on-demand --name smoke`, `ls`, `ssh smoke`, and `down smoke`. On-Demand is an explicit smoke-test choice until Spot support lands in chunk 3; no implicit change to the intended agent default.
- A pinned distribution AMI with SSM, minimal bootstrap, and a consistent development user. Basic CLI readiness and teardown diagnostics.

**Human acceptance test:** Follow the README from a clean configuration; launch `smoke`; open its shell and run `uname -a`; list its ID/type/market/readiness; terminate it and confirm EC2 termination and root-volume deletion through CLI/API output. Verify no inbound security-group rules exist. Repeat `down` and receive an intelligible already-gone result.

**Failure test:** Use an expired AWS session and then a deliberately invalid profile. Both should explain the problem before launch. Exercise an SSM readiness timeout and recover using the printed instance ID.

## 2. Run real development commands and recover diagnostics

**User outcome:** Run a build or test command remotely and trust its result.

Deliver `exec NAME -- COMMAND [ARGS...]` using SSM Run Command, with an explicit remote user and working directory, preserved argument boundaries, remote exit status, execution timeout, and command ID. Add S3 storage and permissions for complete output plus a `logs` command. Document that this first transport is noninteractive and output is retrieved asynchronously; establish what Ctrl-C cancels and report when cancellation is only requested.

**Human acceptance test:** Clone a small public fixture repository through the shell, then run its successful and failing checks with `exec`. Confirm the local exit code matches the remote result. Pass an argument containing spaces and shell metacharacters and confirm literal handling. Generate a large log and retrieve its complete stored output.

**Failure test:** Disconnect the local client during execution; reconnect and retrieve the result using its command ID. Force a timeout and distinguish it from a failed test. No command should report success while execution status is unknown.

## 3. Manage a group of Spot development machines

**User outcome:** Launch several disposable workers and manage them independently or as a group.

Deliver profile validation, multiple subnets/AZs and compatible instance types, `up --count --group`, group listing/teardown, and Spot launch through EC2 Fleet `instant` with `price-capacity-optimized` allocation. On-Demand remains an explicit override; the benchmark profile requires an exact type. Add launch previews, a configurable count limit, request IDs, and deterministic suffixes within a batch.

For partial fulfillment, preserve successfully launched machines, return a nonzero partial-completion result, and list both successes and errors. Make retry intent explicit and reconcile the original request before allocating more machines. An instant fleet does not maintain or replace workers automatically.

**Human acceptance test:** Launch two Spot machines in group `smoke-batch`; run different commands on each; close/reopen the CLI and rediscover both; terminate one by name, then the remainder by group. Launch an explicitly On-Demand machine and verify its displayed market.

**Failure test:** Use a controlled provider-response fixture for partial capacity and an uncertain network response; verify recovery does not silently create another full batch. Exercise duplicate names and `down --all` cancellation. A mocked unmanaged resource must never enter the termination request.

## 4. Leave machines unattended with automatic expiry

**User outcome:** A machine expires even if the local CLI is no longer running.

Deliver `--ttl`, visible expiry timestamps, `cleanup --dry-run`, and manual cleanup. Add an AWS-scheduled cleanup function, IAM scoped to the deployment, and logs describing decisions. Proposed defaults: a 2-hour TTL and checks every 5 minutes; document the cleanup delay and that this is not a hard spending cap. Explicitly configure longer lifetimes when required.

**Human acceptance test:** Launch a machine with a short TTL and another with a longer TTL; inspect dry-run output; close the CLI; confirm only the expired machine terminates and find its cleanup log. Repeat a cleanup invocation and confirm it is harmless.

**Failure test:** Test malformed/missing expiry metadata, a machine from another deployment, and a transient termination failure. Skip invalid/out-of-scope records with diagnostics and retry transient failures on later runs.

**Release gate:** Chunks 1–4 form the interactive devbox release. Install the CLI on a clean local environment and repeat the group/exec/expiry workflow using only the published instructions.

## 5. Open a development environment ready for a real repository

**User outcome:** Launch an agent-profile machine with the selected tools and repository ready for manual work.

Deliver one Packer image with pinned tool versions, an image manifest, and the selected repository bootstrap. Add runtime Secrets Manager access limited to the workload, non-secret Parameter Store configuration where useful, and a defined credential renewal strategy. Store only secret references in configuration and infrastructure state. Create/update secret values out of band.

Build an image once, reference its exact AMI ID, and launch through the existing workflow. Keep a previous manifest for rollback. Record image and bootstrap revisions on the machine; failures remain inspectable within TTL. Set a useful boot-readiness target after measuring this representative workload.

**Human acceptance test:** Launch, connect, and run the actual repository's smallest useful build/test. Terminate and recreate it; verify the same image/tool versions and successful bootstrap. Rotate a test credential without rebuilding the AMI and confirm a new machine uses it. Roll back to the previous image manifest.

**Failure test:** Revoke access to the required secret and verify bootstrap fails clearly without printing it. Verify an unrelated secret is inaccessible and bootstrap logs contain no credential values.

## 6. Take one issue through agent execution to a draft PR

**User outcome:** `devbox agent 142` starts a real issue on an isolated machine and leaves reviewable work in GitHub.

Deliver one selected agent adapter, repository/issue lookup, a unique branch, runtime authentication, a supervised job, status/log retrieval, durable result metadata, and draft-PR creation. `agent` returns a durable job ID; status remains retrievable after the machine terminates. Implement explicit completion/failure/timeout states.

Apply the agreed checkpoint and publishing policy. Terminate after verified persistence on success; proposed failure behavior is to retain the machine for inspection until its TTL. Do not assume that a local commit or an agent process exit means a successful GitHub push. Put independent concurrency limits around agent launches.

**Human acceptance test:** Create a small issue in a test repository; launch it; monitor status; inspect the pushed branch and draft PR; verify logs/results survive machine teardown. Then start two issues and confirm separate machines, branches, and result records.

**Failure test:** Reject a missing issue before allocating a machine. Exercise invalid agent credentials, a failing agent command, and a failed push; none may be reported as successful completion. Repeating an issue launch must not overwrite another active branch/job.

## 7. Recover agent work after interruption

**User outcome:** Resume an interrupted issue from its last durable checkpoint.

Deliver periodic checkpointing, best-effort interruption handling, `agent resume JOB_ID`, attempt lineage, and idempotent branch/PR updates. Record the latest successfully persisted checkpoint. Resume creates a replacement through the existing launch path, with an explicit retry budget. Automatic replacement is a later opt-in policy.

**Human acceptance test:** Start an issue, wait for a confirmed checkpoint, abruptly terminate its machine, then resume the job. Confirm the replacement continues from the pushed revision, retains attempt history, and does not create duplicate PRs. Separately simulate the interruption-notice path.

**Failure test:** Make GitHub temporarily unavailable during checkpointing and report the last durable revision accurately. Verify an interruption before the first checkpoint does not imply recoverable progress. No guarantee is made for unpersisted disk changes.

## 8. Run and retain a repeatable benchmark

**User outcome:** Recreate a benchmark environment and retrieve results after its machine is gone.

Deliver a pinned benchmark image/profile and `benchmark run --repetitions N --output results.json SCRIPT`. Package a defined source revision and script; reject dirty source by default or require an explicit recorded snapshot. Record exact AMI/type/architecture, region/AZ, actual CPU/kernel/tool versions, storage settings, source and script hashes, repetition results, and timing method. Pin dependencies and specify any warm-up procedure.

Upload results and logs to S3, verify persistence, download requested output, and terminate. Run supervision and TTL must survive local disconnection. A failed upload remains a failed job, eligible for retry/inspection until expiry. Reproducible configuration does not promise identical shared-cloud timings.

**Human acceptance test:** Run a small benchmark three times; verify the three records and metadata; retrieve them after teardown; recreate from the same manifest and compare environment identities. Repeat with a failing script and inspect retained failure logs.

**Failure test:** Deny the result upload and ensure the command reports failure and identifies the retained machine/job. Reject incompatible image architecture or missing pinned image before launch.

**Dependencies:** Chunks 1–5. This can proceed before agent automation if benchmarking has higher priority.

## 9. Run one GitHub Actions job on an ephemeral EC2 runner

**User outcome:** An explicitly dispatched workflow provisions an EC2 runner, runs one real job, and removes the runner and machine.

Deliver a separate runner image/role, narrow GitHub credentials, unique routing labels, ephemeral registration, external runner logs, and teardown on success/failure/cancellation. Start with a GitHub-hosted launcher job using AWS OIDC and restricted trust; add registration and idle timeouts plus TTL fallback. Validate which repositories/events may launch workers. Benchmark an uncached build before evaluating an S3-backed compiler cache.

**Human acceptance test:** Dispatch a test workflow, watch its build run on EC2, inspect artifacts/logs, and confirm both EC2 termination and GitHub runner deregistration. Dispatch again and verify a fresh machine. If caching is added, compare a warm run with the uncached baseline and verify cache scoping.

**Failure test:** Cancel during registration and during execution; interrupt the worker; make the build fail. Every path must eventually remove the machine and registration. Distinguish failure detection from actual job retry; replacement alone cannot resume an assigned Actions job.

**Dependencies:** Chunks 1–5. Settle CI trust/authentication before implementation.

## 10. Automatically provision runners for queued jobs

**User outcome:** An eligible queued job causes a worker to appear without a separate manual dispatch.

Deliver authenticated GitHub webhook ingestion, a durable queue, deduplication/reconciliation state, a serverless launcher, concurrency limits, and cleanup of abandoned registrations/jobs. Reuse chunk 9's runner lifecycle. Match repository, workflow, and runner labels; handle cancellation and duplicate/out-of-order deliveries. Decide explicit retry behavior for interrupted jobs. Prefer bounded serverless components to a permanent runner fleet.

**Human acceptance test:** Queue two eligible jobs and one ineligible job; only eligible jobs launch machines. Repeat a webhook and verify no duplicate capacity. Cancel a queued job, exhaust the configured concurrency limit, and replay delayed events; inspect status and eventual cleanup in each case.

**Failure test:** Restart the launcher between allocation and status recording. Reconcile to the existing request/machine without double launching. Verify malformed or unauthenticated events do not allocate capacity.

## Follow-on work, ordered by observed need

- Explicit Spot-to-On-Demand fallback with a user-configured policy and limit; test capacity failure and verify the market change is visible.
- Automatic agent replacement using chunk 7, bounded by attempts and lifetime; test repeated failures without an endless launch loop.
- Approximate cost display with price timestamps and coverage stated for compute, disks, IPv4, and durable services; no claim that an estimate is a billing cap.
- Measured cache optimization and faster image bootstrapping; compare representative cold/warm workflows before adoption.
- OpenSSH/editor integration over SSM if requested, with its own key/user/host-verification design and a real editor/file-transfer acceptance test.
- Chezmoi installation and shell completions after the CLI installation/configuration contract stabilizes.

## Verification and delivery

For each chunk, document setup, copy/paste commands, expected visible results, failure recovery, and teardown in a matching `docs/acceptance/NN-*.md`. Use low-cost test profiles and short lifetimes for live acceptance; query EC2/EBS inventory after cleanup. Do not force real Spot scarcity to test partial launch handling: use controlled SDK responses for rare failure conditions, plus normal live launches for integration.

Automate meaningful invariants: scope-safe termination, launch idempotency, partial fulfillment, argument handling and exit status, TTL boundaries, secret redaction, and durable workflow transitions. Run language formatting/lint/build checks and OpenTofu formatting/validation/plan when applicable. Live cloud acceptance is explicit and separate from ordinary offline CI. A chunk is done only when its human workflow and relevant failures are demonstrated and documented.

## Verified platform details

- Native Session Manager and SSH-over-SSM are distinct; the latter requires SSH setup, including server and key authentication. See [AWS SSH-over-SSM setup](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-getting-started-enable-ssh-connections.html).
- SSM Agent initiates outbound connections, supporting closed inbound rules when the required service endpoints remain reachable. See [AWS SSM connectivity troubleshooting](https://docs.aws.amazon.com/en_en/systems-manager/latest/userguide/troubleshooting-ssm-agent.html).
- AWS recommends EC2 Fleet `instant` for flexible launch-only Spot allocation; it supports multiple pools and reports partial fulfillment. See [AWS instant fleets](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/instant-fleet.html) and [fleet request types](https://docs.aws.amazon.com/us_en/AWSEC2/latest/UserGuide/ec2-fleet-request-type.html).
- OpenTofu supports native S3 state locking through `use_lockfile`; pin and validate the chosen release. See [OpenTofu S3 backend](https://opentofu.org/docs/language/settings/backends/s3/).
- Run Command script exit handling must preserve failures, and external output storage should be configured. See [AWS exit codes](https://docs.aws.amazon.com/systems-manager/latest/userguide/run-command-handle-exit-status.html) and [SendCommand API](https://docs.aws.amazon.com/systems-manager/latest/APIReference/API_SendCommand.html).
- Ephemeral GitHub runners take one job and deregister; EC2 termination and external log retention remain this tool's responsibility. See [GitHub self-hosted runner reference](https://docs.github.com/en/actions/reference/runners/self-hosted-runners).
