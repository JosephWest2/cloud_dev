# Stack, region, and networking decisions

Status: decisions and discussion notes, September 10, 2026. The user confirmed interactive devboxes first, coding agents second, and selected Go + OpenTofu + TOML with Ubuntu LTS for remote machines. Region and networking remain open. No implementation or deployment has been performed.

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
3. For interactive development, do you need terminal access only, or an editor such as VS Code Remote SSH and file transfer in the first release?

Account ownership/scope and exact AWS login can be settled during setup planning. Ohio and the proposed network are recommendations pending selection.
