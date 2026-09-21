# Configuration and prerequisite checks

Follow the [installation instructions](../README.md#install-and-set-up) and
[guided setup](guided-setup.md) for a new deployment. This reference explains
identity selection, local tools and diagnostics.

- [Local access tools](#local-access-tools)
- [Identity and scope](#configure-your-identity-and-scope)
- [Prerequisite checks](#run-prerequisite-checks)

## Local access tools

Local use targets Linux, starting with Arch Linux; other local platforms are not
claimed supported. Install OpenSSH and the AWS Session Manager plugin
**>=1.2.764.0**, and keep the plugin's logging disabled. Verify
`session-manager-plugin --version` and `ssh -V`. The pinned worker AMI must provide
SSM Agent **>=3.3.2746.0**, which bootstrap checks. Foundation provisioning also
needs AWS CLI v2, OpenTofu **1.12.6** and the committed AWS provider **6.64.0**
lockfiles; see [setup tools and identities](setup.md#tools-and-identities).

## Configure your identity and scope

Use one personal AWS account and region per configuration, a selected AWS
profile, and a stable owner ID. Separate config files can select different
deployments. The owner is a fixed identifier you choose, not an inferred local
username or a changing SSO/role session name. Keep it across reinstalls and
credential refreshes so future inventory can find the same machines.

Configure your AWS profile using your normal AWS setup. For IAM Identity Center profiles,
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

For a new configuration, run from the checkout root. If the config file already
exists, edit it instead of copying the example over it; preserve your SSH key path
and deployment settings.

```sh
mkdir -p "${XDG_CONFIG_HOME:-$HOME/.config}/devbox"
cp examples/config.toml "${XDG_CONFIG_HOME:-$HOME/.config}/devbox/config.toml"
```

Edit that file: replace `expected_account`, `aws_profile`, `deployment`, and
`owner`. The implemented foundation supports Ohio (`us-east-2`); use the same
scope as your foundation export. Never put access keys, secret keys, session
tokens or other credentials in devbox TOML or deployment manifests.

### Configuration precedence

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
Empty option values are errors. Repeated lifecycle options are rejected;
other repeated value options use the last value. `cleanup` rejects every repeated
option, including global options.

A selected profile is passed explicitly to the SDK. In the pinned SDK version,
that profile takes precedence over ambient access-key environment variables;
a nonexistent selected profile fails instead of falling back to another identity.
A role profile may explicitly use `credential_source=Environment`. If no profile
is selected, the normal SDK default chain applies, including environment
credentials. `doctor` always verifies the resulting account with STS
`GetCallerIdentity`. The selection behavior is tested against the pinned SDK.
When an interactive command finds an expired or missing browser session, devbox
runs `aws login --profile SOURCE` (or `aws sso login --profile SOURCE` for IAM
Identity Center). AWS CLI opens the browser; finish signing in and devbox continues
with its normal account and resource checks. Valid sessions do not prompt again.
Devbox follows `source_profile` and the documented `aws configure
export-credentials` bridge to authenticate the source, rather than the operator
role. It does not infer a login method for static credentials, environment-only
credentials, or arbitrary credential helpers. Network and permission errors do
not start login.

Automatic login requires terminal stdin and is disabled for `--json`, `proxy`,
`ssh-config`, and `logs`. Scripts must authenticate beforehand. The credential
probe has a 15-second limit; browser login has a separate five-minute limit before
the ordinary command deadline starts. Ctrl-C stops login. Login output goes to
stderr, while credential-export output is discarded. A failed login prevents AWS
client loading; commands are never replayed after a mutation. AWS CLI v2 must be
on PATH for this convenience; missing tools retain ordinary credential diagnostics.

Browser-based `aws login` profiles need the documented [process bridge](setup.md#4-configure-the-restricted-operator-and-run-doctor) when used as the operator role's source with the pinned Go SDK; `doctor` provides an actionable error.
See [AWS SDK configuration](https://docs.aws.amazon.com/sdk-for-go/v2/developer-guide/configure-gosdk.html).

## Run prerequisite checks

```sh
devbox doctor
devbox doctor --aws-profile devbox --json
devbox --config ./my-config.toml doctor --timeout 60s
```

`doctor` checks user config, the agent profile, deployment manifest structure and
scope, AWS credentials and expected account, deployed network/image/template/IAM
and readiness-document settings, Linux, and the Session Manager
plugin and OpenSSH client. Independent local checks still run if the config or
identity fails, unless the deadline expires or the command is canceled; remaining
executable probes are then explicitly skipped.
Invalid configuration/profile schemas prevent even the STS identity call.
A missing/wrong-scope/unsupported manifest or failed identity check prevents
deployed-resource calls. `doctor` makes no AWS resource mutations; interactive authentication can refresh
local AWS session credentials. `up` and `down` perform lifecycle
mutations, and `exec` submits a remote command and its durable request.

Install the **AWS Session Manager plugin**, then verify
`session-manager-plugin --version`. Follow AWS's
[plugin installation instructions](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html).
Arch may require a separately packaged or source-built plugin; a package being
available does not establish AWS vendor support for Arch. `doctor` checks that
the executable starts; it does not claim a live remote session works.

Access uses **SSH over SSM**, supporting real SSH, remote editors and file
transfer. Follow the [dedicated key instructions](acceptance/09-readiness-shell.md#configure-the-dedicated-key):
keep an Ed25519 private key locally, load an encrypted key with `ssh-add`, put only
its public key in foundation inputs, and set the absolute `ssh_identity_file`
path in devbox TOML. Bootstrap configures sshd and the sudo-capable `devbox` user.
The pinned SSM probe supplies the public host key; OpenSSH enforces strict trust
in private per-instance files. Local probe success alone does not validate remote
authentication or editor/file-transfer integration.
OpenTofu is a foundation setup tool, not an installed prerequisite for ordinary
CLI commands; installation and provisioning are covered by the setup guide.

From a clean configuration, expect actionable failures for missing configuration
and the plugin. After configuring identity, expect a **missing deployment
manifest** until the foundation exists. Follow [setup](setup.md) to provision
and export it; do not invent IDs to make real setup pass. Doctor checks the
actual resources against that export. It does not launch a machine, exercise
runtime bootstrap/SSH, or prove effective IAM authorization. Historical foundation
acceptance is [documented separately](acceptance/07-foundation.md).

Checks default to a 20-second deadline (`--timeout` accepts up to 5 minutes).
Each local executable probe is capped at 5 seconds within that deadline. For noninteractive commands, refresh expired credentials outside devbox, then
retry. Credential helpers must be ready to run
without an interactive prompt: their stderr is discarded to keep arbitrary
provider output and secrets out of devbox diagnostics. Raw TOML/JSON parser
errors, SDK errors, account ARNs, and credential values are never printed.
On Linux, a supervised worker process keeps credential helpers and their shell
descendants in one process group; that group is stopped when the command exits,
including after a deadline. Credential helpers must not daemonize or detach
from that group. SSH sessions transfer terminal ownership to the worker and
restore it on exit; their lifetime is independent of the setup deadline.

See [schema and output contracts](contracts.md) for fields, exit statuses,
Spot behavior and structured-output rules, and
[foundation validation evidence](acceptance/07-foundation.md) and
[IAM boundaries](iam.md).
