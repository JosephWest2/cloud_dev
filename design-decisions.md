# Design decisions

Latest decision, September 16, 2026: tmux is the selected next step for interactive
work on general development machines as well as agents. Ship manual sessions
first; managed observation, intervention and agent integration remain planned.
Herdr evaluation is preserved in [#59](https://github.com/JosephWest2/cloud_dev/issues/59)
and is not a selected dependency.

Current selections, updated September 16, 2026: interactive
devboxes first; Go/AWS SDK v2 + OpenTofu + TOML; Linux locally; Ohio (`us-east-2`);
Canonical Ubuntu 24.04 LTS x86-64; public IPv4 with no inbound rules and outbound
TCP 80/443; real SSH over SSM for shells, editors and file transfer. Use a dedicated
local Ed25519 identity, public-key-only remote authentication and strict host trust
seeded by the fixed authenticated SSM probe. One explicitly selected personal
account/profile/deployment/stable owner forms the scope. Exact account/key/backend
inputs remain local; the exact AMI/template/document versions are exported.

These choices were deployed and exercised in [#7 foundation](docs/acceptance/07-foundation.md),
[#8 lifecycle](docs/acceptance/08-lifecycle.md) and [#9 SSH/editor](docs/acceptance/09-readiness-shell.md).
The [parent acceptance gate](docs/acceptance/01-lifecycle.md) records the independent
final run. [Exec/log recovery](docs/acceptance/02-exec-logs.md),
[Spot groups](docs/acceptance/03-spot-groups.md), and
[scheduled expiry](docs/acceptance/04-expiry.md) are implemented with recorded live
acceptance. The current foundation exports v6; launches default to Spot, On-Demand
is explicit, TTL defaults to 2h (maximum 168h), and scheduling uses a 5-minute cadence.
Literal live Scheduler retry exhaustion remains unproved in
[#58](https://github.com/JosephWest2/cloud_dev/issues/58). The separate interactive
release handoff #5, repository-ready images, coding-agent automation, benchmark
automation and additional networking/platform choices remain open work.

The historical sections after the tmux decision retain the September 10–12
decision history. Earlier proposals, open questions and statements that
deployment/access had not yet happened describe those earlier stages. The current
selections and linked acceptance records above supersede historical proposals.

## General interactive sessions (September 16, 2026)

Include a recorded tmux version in development environments for manual experiments,
monitoring and intentionally interactive scripts, as well as agents. Deliver the
manual workflow on the existing devbox foundation before managed agent commands;
it need not wait for the complete repository image or agent adapter. Use existing
SSH-over-SSM authentication and host trust. See the
[delivery and acceptance plan](docs/plans/05-interactive-sessions.md).

Keep `exec` noninteractive: stdin EOF, separate stdout/stderr, exact workload exit,
and post-execution publication. `logs --stream` is retrieval, not live following.
Installing tmux neither makes an existing exec attachable nor publishes manual
session output to S3. Interactive work must start in its terminal session.

Automated benchmarks use explicit inputs and durable structured results; optional
terminal monitoring is separate from the measured process. Periodic durable output
and progress publication is a distinct future improvement for noninteractive jobs.
Worker loss destroys sessions, and attach/detach/waiting never extend expiry.
Retain cloud dev authority over job/attempt identity, questions, publishing and
recovery if a future terminal backend such as Herdr is adopted.

## Interactive agent sessions and observability (September 14, 2026)

Implementation: [#56 — tmux sessions, live observation and input-required notifications](https://github.com/JosephWest2/cloud_dev/issues/56).

### Decision and reasoning

Include tmux in the first agent image and make it available for the initial manual
agent workflow. Every managed interactive agent run starts in a named, detached
tmux session under the existing `devbox` user, with a stable job/attempt identity.
Observe and attach through the existing authenticated SSH-over-SSM access path.

An EC2 instance can be healthy while its agent is blocked on a question. Seeing the
actual terminal makes prompts, errors and progress inspectable, and interactive
attachment provides a direct way to answer. A detached session also lets the agent
continue when the operator disconnects or changes computers. This makes tmux part
of everyday observability from the first agent workflow onward.

### User-facing workflow

| Interface | Selected behavior |
| --- | --- |
| `devbox watch NAME_OR_ID` | Attach read-only to the agent terminal without changing the interactive client's terminal size. |
| `devbox attach NAME_OR_ID` | Attach interactively to the existing agent session to answer questions and investigate. |
| `devbox logs JOB_ID` | Retrieve persisted agent output/status by durable job ID, extending the existing command-result interface. |
| `devbox ls` | Report agent status and waiting duration alongside the separate EC2/SSM/bootstrap observations. |

Resolve friendly names through the existing managed-resource scope checks. Bind
sessions to job/attempt IDs so repeated issue runs cannot attach to an unrelated
session or start a second agent accidentally. Define explicit selection if more
than one session is eligible. Attachment and observation never start a missing job.

### Lifecycle and durable observability

- Keep the agent running across client detach or transport loss. Document detach,
  Ctrl-C, terminal resize and client-exit behavior. Preserve exited panes for
  inspection while the machine remains available; record the agent's actual exit
  status independently of the tmux client/server lifetime.
- Persist output and job metadata to the existing scoped S3 result storage.
  Capture output from startup, publish periodically and at completion, and report
  incomplete uploads accurately. Terminal output may contain control sequences
  and combined streams; define its format without presenting it as the existing
  noninteractive exec protocol's separate stdout/stderr streams. Prefer agent
  event/transcript output where available.
- Track `working`, `waiting_for_input`, `completed`, `failed`, `timed_out` and
  `interrupted` explicitly, with timestamps and unknown/stale observations where
  needed. Obtain question events from the selected agent adapter; silence or pane
  existence does not establish agent state. Persist the pending question's ID,
  context and resolution alongside the job record.
- Notify the operator when input is required, without requiring an attached
  terminal. Choose a minimal configurable notification channel during
  implementation and document its delivery/failure behavior. Notification failure
  must leave the question inspectable; elapsed time is never an answer or approval.
- Keep logs, checkpoints and pending-question metadata outside EC2. tmux survives
  a connection loss but does not restore a terminated machine's process or session.
  Replacement recovery requires the separate checkpoint/resume workflow, including
  any needed conversation state and uncommitted work beyond a Git branch.
- Existing worker expiry applies while attached, detached or waiting for input.
  Preserve diagnostics before expiry when possible; tmux activity never extends TTL.

### Delivery order

Ship tmux availability on the existing development foundation as described in the
September 16 general-session decision, then managed `watch` /
`attach`, durable logs, explicit state and input-required notifications with the
initial agent integration. A cross-machine question inbox, GitHub/chat reply
bridges, automatic checkpoint-and-release while waiting, and supervisor agents
remain follow-on options. The initial intervention path is interactive attachment.

References: [tmux concepts and disconnect behavior](https://github.com/tmux/tmux/wiki/Getting-Started),
[read-only attachment, pane output and exit handling](https://man.openbsd.org/tmux.1),
and the existing [SSH access contract](docs/contracts.md#readiness-and-ssh-access-9).

## Confirmed during issue #6

- One personal AWS account and region per configuration, an explicitly selected AWS profile, and a stable owner identifier. Actual account/profile/owner values are supplied locally, not inferred or recorded in the repository.
- Linux, starting with the user's Arch Linux environment, is the initial local platform. Other platforms are not claimed supported.
- **SSH over SSM** is the first-release access mode, including remote editor and file-transfer support. A native Session Manager shell alone does not satisfy the requirement. Local prerequisites are OpenSSH and the AWS Session Manager plugin. Foundation/shell work must configure remote sshd, SSH authentication, host-key verification and a usable SSM proxy; no inbound SSH port is needed. Exact key management remains implementation work for #7/#9.

## Selected stack

- Go CLI with the AWS SDK for Go v2 for launching, listing, connecting to, running commands on, and terminating disposable machines.
- OpenTofu with HCL definitions for durable AWS infrastructure, including networking, IAM, launch templates, storage, and scheduled cleanup.
- TOML for user configuration and versioned machine profiles.

Normal machine operations call the AWS SDK directly and do not invoke an OpenTofu apply. Keep resource ownership explicit so SDK operations do not conflict with infrastructure managed by OpenTofu.

## Selected base OS

The user selected Ubuntu LTS for remote machines and currently uses Arch locally. The exact Ubuntu release and AMI ID will be selected during image setup; x86-64 remains the proposed architecture. Pin the image and development tool versions for reproducibility. Local dotfiles integration remains optional.

## Seven implementation options

The CLI language and infrastructure engine are independent choices. The CLI calls AWS APIs to manage disposable machines; the infrastructure engine manages durable resources. TOML remains suitable for user profiles with any option. Packer and small bootstrap scripts can be added later regardless of CLI language.

The tradeoffs below are engineering judgments for this project, assuming similar language familiarity; they are not measured performance comparisons.

| Stack | Pros | Cons | Prefer it when |
| --- | --- | --- | --- |
| Go + OpenTofu | Straightforward compiled CLI distribution; convenient concurrency for polling and multiple launches; comparatively simple language | More explicit error-handling boilerplate; weaker type-level modeling than Rust; separate HCL infrastructure language | Shipping and maintaining a small cloud tool with little ceremony is the priority |
| Rust + OpenTofu | Strong types for lifecycle states and errors; compiled distribution; Cargo fits Rust development habits | Ownership/async complexity; large SDK dependencies can make builds expensive; extra implementation effort may not improve a network-bound CLI's user experience | You already enjoy Rust or deliberately want this tool in your Rust ecosystem |
| Python + OpenTofu | Fast iteration; concise AWS automation with Boto3; easy investigation of API responses | Interpreter/environment packaging; more reliance on runtime validation and type checking; synchronous SDK calls need care during concurrent work | You want the fastest path to validating workflows and are happy maintaining a Python tool permanently |
| TypeScript + Node.js + OpenTofu | Typed application code, natural async I/O, straightforward npm distribution | Node runtime/dependency maintenance; runtime config validation still needed; infrastructure remains a separate language | TypeScript is your strongest language but you want explicit declarative infrastructure files |
| TypeScript + Node.js + AWS CDK | Same language for CLI and infrastructure; AWS constructs; CloudFormation manages deployment state | AWS-specific infrastructure model; generated templates and stack rollback add a debugging layer; CDK bootstrap/toolchain | You expect substantial AWS event-driven infrastructure and want to express it as code |
| TypeScript + Node.js + Pulumi | Same language for CLI/infrastructure; reusable functions/types; infrastructure preview and state management | Pulumi engine, provider versions, output/dependency model, and state backend to learn; more abstraction than this initial resource set requires | You prefer programming-language infrastructure and may later manage multiple providers |
| C# + .NET + OpenTofu | Strong typing, async support, good refactoring tools; official AWS SDK | Additional .NET publishing/runtime choices; little project-specific advantage without existing .NET expertise | C# is already your most productive language or Windows/.NET integration matters |

Decision: the user selected Go + OpenTofu + TOML for simplicity. The comparison above is retained as context for the alternatives considered.

Official references: [Go AWS SDK](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/welcome.html), [Rust AWS SDK](https://docs.aws.amazon.com/sdk-for-rust/latest/dg/welcome.html), [Boto3](https://docs.aws.amazon.com/boto3/latest/), [JavaScript SDK](https://docs.aws.amazon.com/sdk-for-javascript/v3/developer-guide/welcome.html), [.NET SDK](https://docs.aws.amazon.com/sdk-for-net/v4/developer-guide/welcome.html), [AWS CDK](https://docs.aws.amazon.com/cdk/v2/guide/home.html), [Pulumi](https://www.pulumi.com/docs/iac/concepts/), [OpenTofu state backend](https://opentofu.org/docs/language/settings/backends/s3/).

## Region

AWS's standard commercial region list has no `us-central` region. Propose US East (Ohio), `us-east-2`, as a nearby full region for a Chicago-based developer. Geography motivates this choice; actual latency and Spot capacity are not yet measured. Compare with another region only if real usage justifies it. See [AWS region list](https://docs.aws.amazon.com/global-infrastructure/latest/regions/aws-regions.html).

## Networking choices

A VPC is the project's isolated AWS network. A public subnet has a direct route to an Internet gateway. Public addressing makes direct Internet connectivity possible; security-group rules determine which connections are allowed.

Inbound means a new connection initiated toward the machine, such as an Internet client reaching its SSH server. Outbound means the machine initiates a connection, such as fetching GitHub code, downloading dependencies, contacting an agent API, or connecting to SSM. Security groups are stateful: replies to an allowed outbound connection can return without an inbound allow rule. See [AWS security-group rules](https://docs.aws.amazon.com/vpc/latest/userguide/security-group-rules.html).

| Design | Advantages | Limitations |
| --- | --- | --- |
| Public IPv4, no inbound rules, Internet egress | Simple; few durable components; development tools work; pays for addresses while allocated | An accidental inbound-rule change can expose a listening service; allowed outbound traffic can leak data |
| Private IPv4 with NAT gateway | No direct public address on workers; additional protection against accidental direct exposure; shared outbound path | NAT hourly/data charges; still permits outbound data leakage; NAT is not a domain/content filter |
| Private workers with controlled egress proxy/firewall and AWS endpoints | Restricts reachable destinations; stronger containment and centralized policy | More infrastructure/cost; dependency downloads and redirects complicate allowlists; permitted destinations can still be abused |
| Private workers with no general Internet egress, AWS endpoints only | Small external connectivity surface; useful for prepackaged offline jobs | Ordinary GitHub cloning, public package installs, web browsing, and external agent APIs do not work without additional connectivity or mirrors |

See [AWS Internet gateway behavior](https://docs.aws.amazon.com/en_en/vpc/latest/userguide/VPC_Internet_Gateway.html), [NAT gateways](https://docs.aws.amazon.com/vpc/latest/userguide/vpc-nat-gateway.html), and [SSM connectivity requirements](https://docs.aws.amazon.com/en_en/systems-manager/latest/userguide/troubleshooting-ssm-agent.html).

Security implication for agents: code running on a machine can potentially read that machine's available files and credentials and use permitted network destinations. Malicious dependencies or agent-directed commands can therefore leak data even when every inbound port is closed. NAT alone does not change this. Restrict accessible secrets, repository permissions, and the instance IAM role; keep unrelated credentials off the machine. A dedicated VPC should have no connection to sensitive existing networks. Short lifetimes reduce persistence but do not undo a leak. These are design implications of the access model, not guarantees of containment.

Proposed MVP network: dedicated VPC in Ohio, public IPv4 on each disposable worker, no inbound rules, SSM access, and outbound HTTPS plus explicitly needed package-repository traffic; use the VPC DNS resolver. HTTPS to arbitrary destinations is still broad Internet access. If code/data sensitivity calls for destination restrictions, design a proxy/allowlist before unattended agents. Do not label port-443-only rules as data-loss prevention.

Price illustration, before credits and other charges: AWS lists public IPv4 at $0.005/address-hour, so a four-hour machine adds $0.02. AWS's Ohio NAT example lists $0.045/hour and $0.045/GB processed; one continuously provisioned gateway is about $32.85 per 730-hour month before its IPv4 charge, data processing, and applicable transfer charges. Network designs have different billing lifetimes: a durable NAT gateway can bill while all workers are gone. See [AWS VPC pricing](https://aws.amazon.com/vpc/pricing/).

## Authentication starting point

Propose temporary local AWS credentials through a named profile, preferably IAM Identity Center where appropriate for the account setup. Workers use separate restricted IAM roles; they do not receive the local user's administrative credentials. Select the concrete login setup after establishing whether an AWS account/organization already exists. See [AWS IAM best practices](https://docs.aws.amazon.com/IAM/latest/UserGuide/best-practices.html).

## Next questions

1. Is Ohio (`us-east-2`) with public IPv4, blocked inbound connections, and SSM access acceptable for the initial deployment?
2. Will workers handle only your trusted repositories, or also arbitrary third-party code/fork PRs or particularly sensitive data?
3. Select exact SSH key management and host-key verification for the confirmed SSH/editor/file-transfer access mode during foundation/shell implementation.

Account scope is confirmed above; actual AWS login and account values are configured during setup. Ohio and the proposed network are recommendations pending selection.


## Issue #7 implementation choices

Foundation setup now targets only `us-east-2`, Canonical Ubuntu 24.04 LTS amd64
standard server, one public subnet and HTTP/HTTPS egress with zero security-group
ingress. The exact AMI is resolved explicitly during setup, checked for Canonical
provenance and exported with a numeric launch-template version. These bounded
implementation choices are not evidence of an authorized/live deployment.

Access remains SSH over SSM. Bootstrap creates a sudo-capable `devbox` user and
sshd with public-key-only authentication; keys, host trust and the editor proxy
remain #9. A fixed, versioned SSM Command document reports pending/complete/failed
bootstrap markers without exposing a general command runner. The manifest is v2
because it now includes these contracts and IAM/bootstrap content digests.

State starts locally in the S3 bootstrap root and migrates to an encrypted,
versioned S3 backend using native lockfiles; foundation uses a separate key.
Pinned tooling is OpenTofu 1.12.6 with AWS provider 6.64.0. See
[setup and recovery](docs/setup.md), [IAM limits](docs/iam.md), and
[live acceptance status](docs/acceptance/07-foundation.md).


## Confirmed during issue #9 (September 12, 2026)

The user selected a dedicated local Ed25519 SSH key. Only its public key is
installed by foundation bootstrap; private material stays with local OpenSSH and
ssh-agent. Host trust is bootstrapped through the pinned, parameterless SSM probe,
then enforced with a private per-instance known_hosts file and strict checking.
The access contract is real SSH over SSM, including external OpenSSH editor and
file-transfer integration. See the [reviewed implementation plan](docs/plans/09-readiness-shell.md)
and [acceptance procedure](docs/acceptance/09-readiness-shell.md).
