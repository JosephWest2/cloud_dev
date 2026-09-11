# devbox

A Go CLI for disposable AWS development machines. This checkout implements
[issue #6](https://github.com/JosephWest2/cloud_dev/issues/6): installation,
versioned configuration, and read-only prerequisite checks. It cannot yet
provision, launch, connect to, or terminate a machine. The OpenTofu foundation
comes in #7, lifecycle operations in #8, and shell access in #9.

## Install from a checkout

The initial local target is **Linux, starting with Arch Linux**. Other local
platforms are not claimed supported. Install Go 1.24 or newer and Git. On Arch:

```sh
sudo pacman -S --needed go git
git clone https://github.com/JosephWest2/cloud_dev.git
cd cloud_dev
go mod download
make check
make build
./bin/devbox version
```

Install into a directory on your PATH:

```sh
mkdir -p "$HOME/.local/bin"
GOBIN="$HOME/.local/bin" make install
export PATH="$HOME/.local/bin:$PATH"
devbox --help
```

Persist that PATH setting in your shell configuration. Without `make`, use
`go build -trimpath -buildvcs=false -o bin/devbox ./cmd/devbox` and
`GOBIN="$HOME/.local/bin" go install -trimpath -buildvcs=false ./cmd/devbox`.
The embedded agent profile installs with the binary; runtime use does not require
this checkout. `go.mod` and `go.sum` pin dependencies. To reproduce a binary,
use the same commit, Go toolchain version (`go version`), GOOS and GOARCH;
the build excludes checkout paths and VCS metadata. Keep `go.sum` unchanged.

## Configure your identity and scope

Use one personal AWS account and region per configuration, a selected AWS
profile, and a stable owner ID. Separate config files can select different
deployments. The owner is a fixed identifier you choose, not an inferred local
username or a changing SSO/role session name. Keep it across reinstalls and
credential refreshes so future inventory can find the same machines.

Authenticate using your normal AWS setup. For IAM Identity Center profiles,
the AWS CLI can configure and refresh a login:

```sh
aws configure sso --profile devbox
aws sso login --profile devbox
```

AWS CLI installation is needed if your login workflow uses it; `devbox` itself
uses the AWS SDK for Go v2. Follow the official
[AWS CLI installation guide](https://docs.aws.amazon.com/cli/latest/userguide/getting-started-install.html)
and [SSO setup guide](https://docs.aws.amazon.com/cli/latest/userguide/cli-configure-sso.html).
Existing shared profiles, assume-role profiles, SSO, credential processes and
SDK environment/workload credential sources are handled by the SDK.

```sh
mkdir -p "${XDG_CONFIG_HOME:-$HOME/.config}/devbox"
cp examples/config.toml "${XDG_CONFIG_HOME:-$HOME/.config}/devbox/config.toml"
```

Edit that file: replace `expected_account`, `aws_profile`, `deployment`, and
`owner`, and choose your region. Ohio in the example is a placeholder choice,
not an agreed deployment location. Never put access keys, secret keys, session
tokens or other credentials in devbox TOML or deployment manifests.

Configuration precedence:

| Setting | Highest to lowest priority |
| --- | --- |
| Config path | `--config PATH`; `$XDG_CONFIG_HOME/devbox/config.toml`; `~/.config/devbox/config.toml` |
| AWS profile | `--aws-profile NAME`; nonempty `AWS_PROFILE`; TOML `aws_profile`; SDK default credential chain |
| Region | `--region REGION`; TOML `region` (required; ambient AWS region variables do not override it) |
| Expected account, deployment, owner | Required TOML fields; never inferred from credentials or local username |
| Manifest | TOML `manifest`; `deployment.json` beside the config |
| Agent profile | TOML `profile_file`; embedded `profiles/agent.toml` |

Relative manifest/profile paths resolve against the config file's directory,
not the current working directory. Paths in TOML do not expand `~` or variables.
Options can appear before or after the command and accept `--option=value`.
Empty option values are errors. Repeated value options use the last value.

A selected profile is passed explicitly to the SDK. In the pinned SDK version,
that profile takes precedence over ambient access-key environment variables;
a nonexistent selected profile fails instead of falling back to another identity.
A role profile may explicitly use `credential_source=Environment`. If no profile
is selected, the normal SDK default chain applies, including environment
credentials. `doctor` always verifies the resulting account with STS
`GetCallerIdentity`. The selection behavior is tested against the pinned SDK;
see [AWS SDK configuration](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-gosdk.html).

## Run prerequisite checks

```sh
devbox doctor
devbox doctor --aws-profile devbox --json
devbox --config ./my-config.toml doctor --timeout 60s
```

`doctor` checks user config, the agent profile, deployment manifest structure and
scope, AWS credentials and expected account, Linux, and the Session Manager
plugin and OpenSSH client. Independent local checks still run if the config or
identity fails, unless the deadline expires or the command is canceled; remaining
executable probes are then explicitly skipped.
Invalid configuration/profile schemas prevent even the STS identity call.
No EC2 launch client or mutation exists in this slice.

Install the **AWS Session Manager plugin**, then verify
`session-manager-plugin --version`. Follow AWS's
[plugin installation instructions](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html).
Arch may require a separately packaged or source-built plugin; a package being
available does not establish AWS vendor support for Arch. `doctor` checks that
the executable starts; it does not claim a live remote session works.

The first release will use **SSH over SSM**, supporting real SSH, remote editors
and file transfer while keeping inbound ports closed. Install the OpenSSH client
(`sudo pacman -S --needed openssh` on Arch) and verify `ssh -V`.
The foundation/shell slices must configure remote sshd, SSH authentication,
host-key verification and the SSM proxy command before access works. No SSH
keys are generated or uploaded in this slice, and local probe success does not
validate remote authentication or editor/file-transfer integration.
OpenTofu is a foundation setup tool, not an installed prerequisite for ordinary
CLI commands; its installation and resource validation belong to #7.

From a clean configuration, expect actionable failures for missing configuration
and the plugin. After configuring identity, expect a **missing deployment
manifest** until the foundation exists. Provision and export it in #7; do not
invent resource IDs to make a real setup pass. A present manifest is currently
validated locally only; AWS resource existence, network/IAM properties, and image
compatibility are not checked yet. Even a passing doctor is not a launch-readiness
guarantee at this stage.

Checks default to a 20-second deadline (`--timeout` accepts up to 5 minutes).
Each local executable probe is capped at 5 seconds within that deadline. Refresh expired
credentials outside devbox, then retry. Credential helpers must be ready to run
without an interactive prompt: their stderr is discarded to keep arbitrary
provider output and secrets out of devbox diagnostics. Raw TOML/JSON parser
errors, SDK errors, account ARNs, and credential values are never printed.
On Linux, a supervised worker process keeps credential helpers and their shell
descendants in one process group; that group is stopped when the command exits,
including after a deadline. Credential helpers must not daemonize or detach
from that group. Interactive session process handling will be defined in #9.

See [schema and output contracts](docs/contracts.md) for fields, exit statuses,
Spot behavior and structured-output rules, and
[validation evidence](docs/acceptance/06-cli-foundation.md) for this slice's checks.
