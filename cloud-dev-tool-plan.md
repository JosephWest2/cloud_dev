# Cloud Dev Infrastructure Tool Plan

## 1. Purpose

Build a small developer-focused tool for launching, connecting to, managing, and destroying repeatable AWS EC2 machines.

The tool should support three primary use cases:

1. **Coding agents** running on isolated disposable machines.
2. **CI runners** using ephemeral EC2 instances, with Spot preferred for cost-sensitive workloads.
3. **Repeatable benchmark machines** using pinned hardware and images.

The intended developer experience should feel closer to `podman run` than traditional cloud administration.

Typical usage should require no AWS Console interaction after initial infrastructure setup.

---

## 2. Repository Boundaries

This tool and its AWS infrastructure should live in a **separate Git repository** from the chezmoi dotfiles repository.

Suggested repository names:

- `cloud-dev`
- `dev-infra`
- `cloud-dev-infra`

The chezmoi repository may provide lightweight integration such as:

- installing the CLI
- shell completions
- aliases
- default local configuration
- AWS profile selection

The AWS infrastructure itself should not be a Git submodule of the dotfiles repository.

---

## 3. High-Level Goals

### Required

- Launch one or many EC2 instances with a single command.
- Use named machine profiles such as `agent`, `benchmark`, and `runner`.
- Connect to instances without manually looking up IP addresses.
- Avoid exposing SSH to the public Internet.
- Support ephemeral Spot instances for coding agents and CI.
- Support stable On-Demand instances for benchmarking.
- Make machines reproducible through versioned configuration and images.
- Retrieve secrets securely without embedding credentials into AMIs.
- Make instances disposable by keeping persistent state outside the VM.
- Allow multiple concurrent instances without cumbersome manual management.
- Provide clear lifecycle commands for listing and terminating machines.

### Nice to Have

- Automatic Spot-to-On-Demand fallback.
- Automatic retry/replacement when a Spot instance is interrupted.
- Automatic agent startup based on GitHub issue or branch.
- GitHub Actions integration for ephemeral self-hosted runners.
- S3-backed benchmark artifacts and CI caches.
- Machine TTL / automatic cleanup.
- Cost estimates and running-cost display.
- Optional AMI image building with Packer.
- Structured JSON output for scripting.

---

## 4. Non-Goals

Initial versions should avoid unnecessary infrastructure complexity.

Do not require:

- Kubernetes
- ECS
- EKS
- long-running orchestration services
- persistent EC2 runner fleets
- manually managed SSH host lists
- public port 22
- storing long-lived AWS access keys on instances
- managing application infrastructure unrelated to development compute

The first version should remain understandable and operable by one developer.

---

## 5. User Experience

A single CLI should expose the common workflows.

Working name:

```text
devbox
```

Possible future specialized aliases such as `agentbox` may call the same underlying tool.

### Launch a machine

```bash
devbox up agent
```

### Launch several machines

```bash
devbox up agent --count 4
```

### Launch a named machine

```bash
devbox up agent --name issue-142
```

### Launch an agent for a GitHub issue

```bash
devbox agent 142
```

Possible behavior:

- launch machine
- clone configured repository
- create or checkout `issue-142`
- retrieve the issue
- start the configured coding agent

### Launch benchmark hardware

```bash
devbox up benchmark
```

Benchmark profiles should default to On-Demand instances.

### List machines

```bash
devbox ls
```

Example:

```text
NAME        PROFILE      INSTANCE TYPE   MARKET       STATE      AGE
issue-142   agent        c7i.2xlarge     spot         running    14m
issue-143   agent        c7a.2xlarge     spot         running    12m
bench-01    benchmark    c7i.4xlarge     on-demand    running    4m
```

### Connect to a machine

```bash
devbox ssh issue-142
```

The implementation should resolve the friendly name to the EC2 instance ID automatically.

### Run a command remotely

```bash
devbox exec issue-142 -- cargo test
```

### Destroy a machine

```bash
devbox down issue-142
```

### Destroy a group

```bash
devbox down --group issue-batch-4
```

### Destroy all managed machines

```bash
devbox down --all
```

This command should require an explicit confirmation unless `--yes` is supplied.

---

## 6. AWS Architecture

The initial architecture should use:

- EC2
- EC2 Launch Templates
- IAM
- AWS Systems Manager Session Manager
- AWS Secrets Manager
- SSM Parameter Store where appropriate
- S3 for persistent artifacts and caches
- optional Packer-built AMIs

Conceptual architecture:

```text
Local machine
    |
    | devbox CLI
    v
AWS API
    |
    +---- EC2 Launch Template: agent
    |         |
    |         +---- Spot EC2 instances
    |
    +---- EC2 Launch Template: runner
    |         |
    |         +---- Spot EC2 instances
    |
    +---- EC2 Launch Template: benchmark
              |
              +---- On-Demand EC2 instances

EC2 instances
    |
    +---- IAM role
    +---- SSM Session Manager
    +---- Secrets Manager
    +---- S3
```

---

## 7. Infrastructure as Code

Use **OpenTofu or Terraform** for persistent AWS infrastructure.

Suggested structure:

```text
cloud-dev/
├── infra/
│   ├── terraform/
│   │   ├── iam/
│   │   ├── networking/
│   │   ├── launch-templates/
│   │   ├── runners/
│   │   └── storage/
│   └── packer/
├── cli/
├── bootstrap/
├── config/
├── docs/
└── README.md
```

Infrastructure state should not be stored directly in Git.

Initially, remote state may use:

- S3 backend
- state locking where supported/configured

---

## 8. Machine Profiles

Profiles should describe reproducible machine configurations.

Example:

```toml
[profiles.agent]
market = "spot"
instance_types = [
    "c7i.2xlarge",
    "c7a.2xlarge",
    "c6i.2xlarge",
    "c6a.2xlarge"
]
ami = "agent-v3"
disk_gb = 100

[profiles.benchmark]
market = "on-demand"
instance_type = "c7i.4xlarge"
ami = "benchmark-v2"
disk_gb = 100

[profiles.runner]
market = "spot"
instance_types = [
    "c7i.2xlarge",
    "c7a.2xlarge",
    "c6i.2xlarge"
]
ami = "runner-v2"
disk_gb = 100
```

The exact configuration format may change, but profiles should be version-controlled.

---

## 9. AMI Strategy

Use custom AMIs once bootstrap time becomes significant.

Images may contain:

- Git
- GitHub CLI
- Rust toolchains
- Node/Bun
- Python
- CMake
- Ninja
- build essentials
- AWS CLI
- SSM Agent
- coding-agent binaries or harnesses
- common developer utilities

Do not bake secrets into AMIs.

### Agent AMI

Optimized for coding work.

### Runner AMI

Optimized for CI.

May contain preinstalled compilers and build dependencies.

### Benchmark AMI

Must prioritize reproducibility.

Pin:

- OS version
- kernel version where practical
- compiler/toolchain versions
- benchmark dependencies
- scripts and configuration

---

## 10. Bootstrap

User data or cloud-init should perform only instance-specific initialization.

Examples:

- retrieve configuration
- retrieve secrets
- clone a repository
- checkout a branch
- configure Git identity
- start a coding agent
- register an ephemeral GitHub Actions runner
- restore caches

Keep expensive package installation out of bootstrap when possible by putting stable dependencies into the AMI.

---

## 11. Authentication and Secrets

### AWS Authentication

The local CLI should use normal AWS credentials, preferably:

- AWS SSO
- AWS CLI profiles
- temporary credentials

Avoid embedding AWS credentials into the tool configuration.

### EC2 Authentication

Each EC2 machine should receive an IAM instance role.

Do not store long-lived AWS access keys on instances.

### Secrets

Use **AWS Secrets Manager** for credentials such as:

- OpenAI API keys
- GitHub tokens when unavoidable
- third-party API credentials
- database credentials

Use **SSM Parameter Store** for non-secret configuration such as:

- repository URLs
- S3 bucket names
- profile configuration
- feature flags
- bootstrap settings

Prefer workload identity or short-lived credentials over long-lived secrets wherever supported.

---

## 12. Remote Access

Use **AWS Systems Manager Session Manager** as the primary access mechanism.

Requirements:

- no public SSH port
- no manually maintained EC2 IP list
- friendly instance-name resolution in the CLI

Example:

```bash
devbox ssh issue-142
```

Internally:

```text
issue-142
    |
    v
EC2 tag lookup
    |
    v
instance ID
    |
    v
SSM session
```

Optional future support may allow normal OpenSSH syntax tunneled through SSM.

---

## 13. EC2 Tagging

Every managed instance should receive predictable tags.

Minimum suggested tags:

```text
ManagedBy=devbox
Name=issue-142
Profile=agent
Owner=<user>
CreatedAt=<timestamp>
Group=<optional group>
Purpose=<agent|runner|benchmark>
Repository=<optional repo>
Issue=<optional issue number>
TTL=<optional expiration>
```

Tags will be used by the CLI for discovery and cleanup.

Avoid relying on locally persisted instance IDs as the primary source of truth.

AWS should remain the authoritative inventory.

---

## 14. Spot Instances

Use Spot by default for disposable workloads.

### Good Spot Workloads

- coding agents
- CI runners
- batch jobs
- disposable build machines

### Do Not Default to Spot For

- reproducible benchmarks
- stateful services
- workloads where interruption would invalidate expensive work

### Spot Strategy

Allow multiple interchangeable instance types.

Example pool:

```text
c7i.2xlarge
c7a.2xlarge
c6i.2xlarge
c6a.2xlarge
```

Allow multiple Availability Zones when practical.

Prefer AWS capacity-aware allocation rather than requesting one exact Spot pool.

### Interruption Handling

Treat interruption as normal.

For coding agents:

1. periodically checkpoint work
2. commit work locally
3. push the branch to GitHub
4. on interruption notice, push immediately
5. launch replacement if requested
6. continue from the existing branch

For CI:

1. job runs on ephemeral runner
2. Spot machine may disappear
3. failed/lost job can be retried
4. replacement runner launches

---

## 15. Coding Agent Workflow

Target workflow:

```bash
devbox agent 142
```

Possible process:

```text
1. Determine configured repository
2. Launch Spot EC2 agent machine
3. Wait for SSM readiness
4. Retrieve required secrets
5. Clone repository
6. Fetch GitHub issue #142
7. Create branch issue-142
8. Start coding agent
9. Agent works
10. Agent commits and pushes regularly
11. Agent opens PR
12. Instance is terminated after completion
```

Multiple issues:

```bash
devbox agent 142 143 144 145
```

should launch independent machines.

Isolation should be machine-level, not only process-level.

---

## 16. GitHub Authentication

Avoid using a highly privileged personal GitHub token if a more limited mechanism is available.

Preferred future options include:

- GitHub App installation tokens
- fine-grained tokens
- short-lived credentials

Credentials should have the smallest practical scope.

Agent credentials may require:

- clone repository
- push branches
- read issues
- create pull requests

CI runner credentials should generally be separate from agent credentials.

---

## 17. GitHub Actions Runner Workflow

The long-term CI runner design should be **ephemeral**.

Do not keep EC2 runner instances alive waiting for jobs.

Target lifecycle:

```text
GitHub job queued
        |
        v
runner controller / launcher
        |
        v
EC2 Spot instance
        |
        v
register ephemeral runner
        |
        v
run one job
        |
        v
upload results/cache
        |
        v
terminate instance
```

Initial CI integration may be implemented after the interactive `devbox` functionality is stable.

### Hybrid CI Strategy

Use GitHub-hosted runners for:

- included monthly minutes
- small jobs
- formatting
- lightweight checks

Use AWS Spot runners for:

- expensive Rust compilation
- test suites
- integration tests
- large builds

---

## 18. Benchmark Workflow

Benchmark machines need stricter reproducibility than agent machines.

Example:

```bash
devbox benchmark run ./benchmarks/compiler.sh
```

Benchmark profile should pin:

- region
- Availability Zone where useful
- exact instance type
- CPU architecture
- AMI
- kernel
- compiler
- dependency versions
- EBS volume configuration
- benchmark source revision

Use **On-Demand** by default.

Benchmark commands should support repeated runs.

Example:

```bash
devbox benchmark run     --repetitions 10     --output results.json     ./benchmarks/build.sh
```

Results should include metadata:

```json
{
  "instance_type": "c7i.4xlarge",
  "ami": "ami-...",
  "region": "us-east-1",
  "availability_zone": "us-east-1a",
  "git_commit": "...",
  "repetitions": 10
}
```

Benchmark results should be copied to persistent storage before the machine is destroyed.

---

## 19. Persistent Storage

Instances should be treated as disposable.

Persistent state belongs elsewhere.

### GitHub

Use for:

- source code
- branches
- agent checkpoints
- pull requests

### S3

Use for:

- benchmark results
- logs
- flamegraphs
- build artifacts
- CI caches
- optional agent artifacts

### Secrets Manager

Use for secrets.

No critical state should exist only on an EC2 root disk.

---

## 20. Caching

Rust CI performance makes caching important.

Potential cache targets:

```text
~/.cargo/registry
~/.cargo/git
target/
```

The implementation should distinguish between:

- dependency cache
- compiler/build cache
- generated build artifacts

Potential backends:

- S3
- `sccache` backed by S3
- GitHub Actions cache where appropriate

`sccache` should be evaluated early for Rust builds.

---

## 21. CLI Implementation

The CLI should eventually be a small compiled tool.

Reasonable implementation languages:

- Go
- Rust

Rust is a natural choice if this remains a personal developer tool and ecosystem consistency is desirable.

The first prototype may use Bash or Python if that significantly speeds up validation.

### Core Modules

Suggested responsibilities:

```text
config
aws
instances
profiles
ssm
secrets
agents
runners
benchmarks
output
```

### Important Commands

Initial:

```text
devbox up
devbox ls
devbox ssh
devbox exec
devbox down
```

Next:

```text
devbox agent
devbox benchmark
```

Later:

```text
devbox runner
devbox cost
devbox cleanup
devbox image
```

---

## 22. Configuration

Support user-level configuration.

Possible location:

```text
~/.config/devbox/config.toml
```

Example:

```toml
aws_profile = "personal"
region = "us-east-1"

default_repo = "JosephWest2/example-project"

[defaults]
agent_profile = "agent"
benchmark_profile = "benchmark"
runner_profile = "runner"
```

Chezmoi may manage this configuration template.

Secrets must never be stored in this file.

---

## 23. Local Dotfiles Integration

The chezmoi repository may:

- install `devbox`
- configure shell completions
- set aliases
- manage `~/.config/devbox/config.toml`
- bootstrap AWS CLI configuration conventions

Optionally, EC2 images may use a server-safe subset of the dotfiles configuration.

Do not make EC2 machine creation depend on the entire interactive desktop dotfiles setup.

---

## 24. Safety Features

The CLI should protect against accidental cloud spend.

Required safeguards:

- all resources tagged `ManagedBy=devbox`
- `devbox down --all` confirmation
- optional TTL on instances
- automatic cleanup support
- clear display of Spot vs On-Demand
- display instance type before launch
- optionally show approximate hourly price
- never destroy untagged instances

Example:

```bash
devbox up agent --ttl 2h
```

Expired machines may be found by:

```bash
devbox cleanup
```

Future automation may terminate them automatically.

---

## 25. Observability

The tool should make machine status obvious.

Example:

```bash
devbox ls
```

should expose:

- friendly name
- instance ID
- profile
- instance type
- Spot/On-Demand
- lifecycle state
- SSM readiness
- age
- group
- optional issue/PR

Future versions may expose:

- approximate accumulated cost
- current public/private IP
- Spot interruption/rebalance state
- agent status
- CI job status

---

## 26. Error Handling

The CLI should produce actionable errors.

Examples:

```text
No Spot capacity was available for the configured agent pool.
Tried:
  c7i.2xlarge
  c7a.2xlarge
  c6i.2xlarge

Retry with:
  devbox up agent --on-demand
```

```text
Instance issue-142 is running but has not registered with SSM.
Check:
  - IAM instance profile
  - SSM agent
  - network access to SSM endpoints
```

Avoid surfacing raw AWS errors unless verbose/debug output is requested.

---

## 27. Phased Implementation Plan

### Phase 1 — Basic Disposable EC2

Build the minimum useful system.

Infrastructure:

- AWS networking
- IAM instance role
- SSM access
- one Launch Template
- one base AMI or standard distro image

CLI:

```text
devbox up
devbox ls
devbox ssh
devbox down
```

Success criterion:

> A developer can launch an isolated EC2 machine, connect to it, and destroy it without opening the AWS Console.

---

### Phase 2 — Machine Profiles

Add:

- configuration file
- `agent`
- `benchmark`
- instance tags
- multiple instance types
- `--count`
- groups
- TTL metadata

Success criterion:

> Multiple reproducible machine classes can be managed entirely through the CLI.

---

### Phase 3 — Custom AMIs

Introduce Packer.

Create:

- agent image
- benchmark image

Preinstall expensive tooling.

Success criterion:

> A newly launched machine becomes useful within a short bootstrap period without large package installs.

---

### Phase 4 — Secrets

Add:

- Secrets Manager integration
- Parameter Store integration
- least-privilege IAM policies

Success criterion:

> Agents can access required credentials without secrets being stored in Git, AMIs, user-data, or local configuration.

---

### Phase 5 — Coding Agent Workflow

Implement:

```bash
devbox agent <issue>
```

Add:

- repository clone
- branch creation
- GitHub issue retrieval
- coding-agent startup
- periodic Git checkpointing
- interruption handling

Success criterion:

> One command launches an isolated agent machine for a GitHub issue.

---

### Phase 6 — Benchmarking

Add:

```text
devbox benchmark run
```

Implement:

- exact instance type
- exact image
- repeated runs
- metadata recording
- S3 result upload
- automatic termination

Success criterion:

> The same benchmark configuration can be recreated later and produces machine/environment metadata alongside results.

---

### Phase 7 — Ephemeral GitHub Actions Runners

Build automatic runner lifecycle.

Requirements:

- Spot-first
- one job per runner
- automatic registration
- automatic termination
- retry/replacement strategy
- cache restore/save

Success criterion:

> Expensive CI jobs automatically run on short-lived AWS Spot machines with no manually maintained runner fleet.

---

### Phase 8 — Cost and Reliability Improvements

Add:

- Spot capacity-aware selection
- On-Demand fallback
- automatic TTL cleanup
- cost estimates
- S3/sccache optimization
- interruption/rebalance handling
- machine replacement

---

## 28. Initial MVP Recommendation

Do not start by building the full CI autoscaler.

The first useful version should be:

```text
Terraform/OpenTofu
    |
    +-- IAM
    +-- Launch Template
    +-- SSM

devbox
    |
    +-- up
    +-- ls
    +-- ssh
    +-- down
```

Then manually prove that coding-agent workloads are pleasant to use.

Example target:

```bash
devbox up agent --name test-agent
devbox ssh test-agent
devbox down test-agent
```

Once that workflow is reliable, add:

```bash
devbox agent 142
```

Only after the underlying disposable-machine abstraction is solid should GitHub Actions autoscaling be introduced.

---

## 29. Key Design Principles

1. **Machines are disposable.**
2. **Git/S3 hold persistent state.**
3. **IAM provides machine identity.**
4. **Secrets are retrieved at runtime.**
5. **Spot is the default for fault-tolerant workloads.**
6. **On-Demand is the default for reproducible benchmarks.**
7. **The AWS Console should not be part of normal operation.**
8. **AWS tags are the source of truth for machine discovery.**
9. **The CLI should hide routine AWS complexity without hiding important cost/lifecycle information.**
10. **Start simple and introduce orchestration only when the workload requires it.**

---

## 30. Definition of Success

The tool is successful when workflows such as the following feel routine:

```bash
# Start four isolated coding machines
devbox agent 142 143 144 145

# See everything currently running
devbox ls

# Connect to one
devbox ssh issue-143

# Create a repeatable benchmark machine
devbox up benchmark --name compiler-bench

# Remove finished workers
devbox down issue-142 issue-143

# Clean up anything expired
devbox cleanup
```

The user should think primarily in terms of **jobs and disposable machines**, not EC2 instance IDs, security groups, public IP addresses, or AWS Console workflows.
