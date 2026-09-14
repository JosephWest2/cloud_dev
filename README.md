# devbox

A Go CLI for disposable AWS development machines: provision a durable foundation,
launch an Ubuntu devbox, run noninteractive commands, connect with real SSH over
SSM, rediscover it after a restart, and remove it with root-volume verification. Remote editors and file
transfer use the generated OpenSSH configuration. No inbound ports are opened.

Start with installation and identity below, then follow the
[foundation setup guide](docs/setup.md) for state bootstrap/migration, a dedicated
SSH key, provisioning and manifest-v5 export. The
[MVP 1 acceptance runbook and results](docs/acceptance/01-lifecycle.md) connect the
complete lifecycle workflow, failure checks and cleanup evidence. The
[MVP 2 acceptance runbook](docs/acceptance/02-exec-logs.md) covers remote checks,
literal arguments, complete output, detachment and recovery after teardown.
Existing deployments need the [multi-AZ foundation upgrade](docs/setup.md#upgrade-to-the-multi-az-spot-foundation-29)
and a real manifest-v5 export for batch launches. Scoped inventory and teardown
remain available for older workers; legacy single-worker receipt recovery remains
supported. Fresh live Spot acceptance and cleanup are tracked in issue #34.

The selected first-release scope is Linux locally, Ohio (`us-east-2`), Canonical
Ubuntu 24.04 LTS x86-64, approved public subnets across selected AZs with public IPv4, outbound TCP 80/443,
and zero security-group ingress. AMI and launch-template versions are explicitly
pinned during setup. The bundled profile defaults to Spot through an instant
Fleet. Explicit `--on-demand` selects On-Demand; there is no automatic fallback
or replacement of interrupted workers.

## Install from a checkout

The initial local target is **Linux, starting with Arch Linux**. Other local
platforms are not claimed supported. Install Go 1.24 or newer, Git and Make. On Arch:

```sh
sudo pacman -S --needed go git make openssh jq
git clone https://github.com/JosephWest2/cloud_dev.git
cd cloud_dev
go mod download
make check
make build
./bin/devbox version
```

Provisioning also needs AWS CLI v2, OpenTofu **1.12.6** and the committed AWS
provider **6.64.0** lockfiles. SSH access also needs the AWS Session Manager plugin
**>=1.2.764.0** and keep its logging disabled; verify
`session-manager-plugin --version` and `ssh -V`. The pinned AMI must provide SSM
Agent **>=3.3.2746.0**, which bootstrap checks. Installation links and identity setup
are below and in [setup](docs/setup.md#tools-and-identities).

Run `make infra-check TOFU=/path/to/tofu` in a separate clean checkout for
OpenTofu formatting, validation, mock-provider tests and manifest-export checks.
It uses `init -backend=false`; keep that checkout separate from live S3-backend
initialization and local deployment inputs. These offline checks do not apply
infrastructure or establish live acceptance.

`make runner` builds the pinned Linux/amd64 execution-runner artifact used by
foundation provisioning; `infra-check` builds it automatically. The foundation
now includes private command-result storage with 30-day retention from submission
and a separate execution document. `exec` submits commands and observes durable
results and the exact SSM invocation, with distinct timeout and detach outcomes.
`logs` retrieves recorded status and verifies complete output exports by ID. See the
[execution contract](docs/contracts.md#selected-exec-and-durable-result-protocol-16).

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
`owner`. The implemented foundation supports Ohio (`us-east-2`); use the same
scope as your foundation export. Never put access keys, secret keys, session
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
Empty option values are errors. Repeated lifecycle options are rejected;
other repeated value options use the last value.

A selected profile is passed explicitly to the SDK. In the pinned SDK version,
that profile takes precedence over ambient access-key environment variables;
a nonexistent selected profile fails instead of falling back to another identity.
A role profile may explicitly use `credential_source=Environment`. If no profile
is selected, the normal SDK default chain applies, including environment
credentials. `doctor` always verifies the resulting account with STS
`GetCallerIdentity`. The selection behavior is tested against the pinned SDK.
Browser-based `aws login` profiles need the documented [process bridge](docs/setup.md#4-configure-the-restricted-operator-and-run-doctor) when used as the operator role's source with the pinned Go SDK; `doctor` provides an actionable error.
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
deployed-resource calls. `doctor` is read-only; `up` and `down` perform lifecycle
mutations, and `exec` submits a remote command and its durable request.

Install the **AWS Session Manager plugin**, then verify
`session-manager-plugin --version`. Follow AWS's
[plugin installation instructions](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html).
Arch may require a separately packaged or source-built plugin; a package being
available does not establish AWS vendor support for Arch. `doctor` checks that
the executable starts; it does not claim a live remote session works.

Access uses **SSH over SSM**, supporting real SSH, remote editors and file
transfer. Follow the [dedicated key instructions](docs/acceptance/09-readiness-shell.md#configure-the-dedicated-key):
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
manifest** until the foundation exists. Follow [setup](docs/setup.md) to provision
and export it; do not invent IDs to make real setup pass. Doctor checks the
actual resources against that export. It does not launch a machine, exercise
runtime bootstrap/SSH, or prove effective IAM authorization. Historical foundation
acceptance is [documented separately](docs/acceptance/07-foundation.md).

Checks default to a 20-second deadline (`--timeout` accepts up to 5 minutes).
Each local executable probe is capped at 5 seconds within that deadline. Refresh expired
credentials outside devbox, then retry. Credential helpers must be ready to run
without an interactive prompt: their stderr is discarded to keep arbitrary
provider output and secrets out of devbox diagnostics. Raw TOML/JSON parser
errors, SDK errors, account ARNs, and credential values are never printed.
On Linux, a supervised worker process keeps credential helpers and their shell
descendants in one process group; that group is stopped when the command exits,
including after a deadline. Credential helpers must not daemonize or detach
from that group. SSH sessions transfer terminal ownership to the worker and
restore it on exit; their lifetime is independent of the setup deadline.

See [schema and output contracts](docs/contracts.md) for fields, exit statuses,
Spot behavior and structured-output rules, and
[foundation validation evidence](docs/acceptance/07-foundation.md) and
[IAM boundaries](docs/iam.md).

## Launch, rediscover and use a group

```sh
devbox up agent --count 2 --group smoke-batch --aws-profile devbox-operator --timeout 5m --json > batch.json
# Inspect batch.json even if up returns a partial result or timeout.
devbox ls --group smoke-batch --aws-profile devbox-operator --json
first_worker=$(jq -r '.instances[0].instance_id' batch.json)
second_worker=$(jq -r '.instances[1].instance_id' batch.json)
# Use IDs returned by the actual result; a partial batch may have only one.
devbox ssh "$first_worker" --aws-profile devbox-operator
# In the remote shell: uname -a; whoami; exit 0
devbox exec "$first_worker" --aws-profile devbox-operator -- uname -a
devbox exec "$second_worker" --aws-profile devbox-operator -- /usr/bin/printf 'second worker\n'
devbox down "$first_worker" --aws-profile devbox-operator --timeout 5m --json
devbox down "$second_worker" --aws-profile devbox-operator --timeout 5m --json
```

These examples explicitly select the restricted operator profile, preventing a
setup `AWS_PROFILE` environment variable from overriding TOML. Substitute your
operator profile's name if it differs from `devbox-operator`.
`up` verifies the foundation and prints profile, region, market, instance count,
base/group and eligible type/subnet/AZ choices before allocating. Actual placement
appears per worker. Set `max_count` in the local TOML (default 10, range 1–100);
there is no CLI or environment override. Use `--on-demand` explicitly when
needed; it selects the first profile type across its approved subnets.

Names derive from the base and full instance ID, such as
`smoke-batch-i-0123456789abcdef0`. Use the exact generated name or ID for `ssh`,
`ssh-config`, `exec` and individual `down`. Names remain stable after partial
capacity, retries and peer removal. One group can contain several independent
launch requests. Group discovery uses AWS tags and does not need local receipts.

Full readiness requires EC2 running, SSM online and bootstrap complete. Up to
four workers are observed concurrently under one overall deadline, default 5m;
failed or slow workers retain their IDs and do not cancel healthy peers.

Successful schema-v2 JSON `up` has `ok=true`, `exit_code=0`, and workers with
`ec2_state=running`, `ssm=online`, `bootstrap=complete`, `readiness=ready` and
the actual `market`. Progress and recovery instructions go to stderr; stdout is
one JSON envelope. Record the request/instance/root-volume IDs before teardown.
See [the acceptance procedure](docs/acceptance/01-lifecycle.md#launch-shell-and-rediscovery)
for exact evidence commands and expected fields.

`ssh` opens an interactive OpenSSH shell as `devbox`, not a native SSM shell.
`--timeout` bounds connection setup (default 5m), not the established session.
Ctrl-C interrupts the remote foreground command; terminal resizing is forwarded;
`exit` or Ctrl-D returns locally. SSH exit statuses are preserved: an explicit
`exit 0` succeeds, 1–254 report `remote_exit`, and 255 indicates an SSH failure.
Exiting immediately after an interrupted command can return 130. Interactive
`ssh` and `ssm-proxy` reject `--json` before AWS calls.

`devbox ssh-config "$first_worker" --aws-profile devbox-operator --json` returns `ssh_config_path` and `ssh_host` for
`ssh -F CONFIG_PATH SSH_HOST`, scp/sftp and remote editors. It does not edit
`~/.ssh/config`. Follow [transfer/editor examples](docs/acceptance/09-readiness-shell.md#launch-observe-connect-and-transfer).
Regenerate configuration after moving the CLI/config. A changed host key requires
inspection; it is never silently trusted. SSH tunnel contents are not logged by
[Session Manager](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-getting-started-enable-ssh-connections.html).

`ls` rediscovers instances from AWS after a restart, without local instance IDs.
`down` revalidates scope before termination and reports root-volume deletion
separately. Duplicate friendly names require explicit instance IDs. A repeated
teardown reports an observed already-terminated instance or `no_managed_match`;
the latter does not verify any particular termination.

For a successful first teardown, expect `status=terminated`,
`ec2_state=terminated`, and `root_volume_deletion=deleted`. If deletion is
`unavailable`, verify the recorded volume ID through EC2 before marking cleanup
complete. A later repeated teardown can lack volume mappings even when deletion
was previously verified. Full [manual cleanup](docs/acceptance/01-lifecycle.md#teardown-and-independent-cleanup-verification)
includes independent EC2/EBS inventory. Workers remain billable after shell exit,
timeout or connection failure; remove them even when acceptance fails. There is
no automatic TTL cleanup until MVP 4. Worker teardown retains networking, IAM,
the launch template, readiness document and the S3 backend; [full durable teardown](docs/setup.md#recovery-and-teardown)
is a separate explicit operation.

Before launching, devbox durably saves a non-secret request receipt and prints its
request ID to stderr. If the process exits or the launch outcome is uncertain:

```sh
devbox up --resume REQUEST_ID --aws-profile devbox-operator --timeout 5m --json
```

Use the original config/scope. Do not retry an uncertain launch with a fresh `up`.
Receipts live under `${XDG_STATE_HOME:-$HOME/.local/state}/devbox/requests`.
Batch resume observes shared S3 records and AWS; it never sends another launch,
including for a prepared request. An outcome can remain unresolved, including a
crash immediately before sending. Losing the local receipt cannot prevent
shared recovery, `ls`, access or cleanup by `down INSTANCE_ID`.

A definitive one-of-two allocation reports `partial_capacity`, exit 3 and a
retry command for exactly one missing worker. A complete allocation with only
one ready worker instead reports `readiness_failed`, exit 3. Unknown allocation
reports `missing_count: null` and exit 1; timeout/interruption returns exit 4.
Explicit missing-capacity consent is a separate command:

```sh
devbox up --retry-missing REQUEST_ID --after ATTEMPT_ID --aws-profile devbox-operator --json
```

Repeating the same retry, even on a second computer, observes its existing
successor. It never replaces interrupted or removed workers. Missing capacity
uses historical fulfillment from permanent shared records, and cannot be
inferred from an empty inventory. The pool and market stay pinned. To change
them, inspect the old request and deliberately start an independent new request.
No-capacity results explain these choices without launching automatically.
After an allocated timeout, inspect with `ls --json`, retry the same receipt or
SSH by instance ID if readiness permits, and remove the worker with
`down INSTANCE_ID --timeout 5m --json`, using the original config/profile/region.
Failed bootstrap blocks SSH; fix the foundation and replace the disposable
worker after cleanup. If credentials expire during cleanup, refresh the selected
source profile and retry the exact ID; keep cleanup marked incomplete until
EC2 termination and root deletion are observed.
Read the [batch recovery/output contract](docs/contracts.md#spot-batch-contract-28)
and [parent acceptance record](docs/acceptance/01-lifecycle.md) for failure
coverage, volume verification, and the final gate status. Historical
[#8 lifecycle](docs/acceptance/08-lifecycle.md) and
[#9 SSH/editor](docs/acceptance/09-readiness-shell.md) acceptance passed.

## Run a noninteractive command

Use a newly bootstrapped worker and its matching manifest v4 or v5:

```sh
devbox exec "$first_worker" -- /usr/bin/printf '%s\n' '' 'two words' '$(id)' '--json'
devbox exec "$first_worker" --cwd project --exec-timeout 10m -- sh -c 'make check'
devbox --json exec "$first_worker" --wait-timeout 2m -- /usr/bin/false
```

Everything after the first `--` is passed literally, including remote `--help`,
`--json` and `--timeout`. Use `sh -c` explicitly for shell evaluation. Commands run
as `devbox`, with stdin EOF, a fixed environment and default cwd `/home/devbox`.
Relative `--cwd` values resolve from that home directory. Exec uses the AWS SDK;
it requires no local SSH key, OpenSSH, Session Manager plugin, launch-profile file
or OpenTofu executable.

The CLI prints a `dc1-...` recovery ID on stderr before submission and announces
the acknowledged SSM ID immediately. Its stdout contains status metadata, or one
JSON envelope with `--json`, and no workload bytes. A complete final record
preserves the workload's exact exit code, including 1, 2, 4 and 255; `outcome`
distinguishes those codes from local failures. Publication failure retains the
workload status but prevents success. Exec validates the publisher's metadata;
full output-byte verification belongs to explicit retrieval.

Setup defaults to 5m, SSM delivery to 5m, remote runtime to 1h, and the independent
local result wait to 1h. Ctrl-C detaches and leaves the remote command running
within `--exec-timeout`. A lost SendCommand response is `submission_unknown`, with
its public ID preserved; never rerun it automatically. Results use the configured
30-day retention and survive worker teardown.

Exec observes durable started/outcome/final metadata and the exact SSM invocation.
It tolerates delayed visibility and bounded temporary API failures, and separates
SSM delivery/runner timeouts from a recorded workload timeout and the local wait
deadline. JSON `ssm` fields describe the last verified wrapper observation;
`ssm.response_code` never replaces `workload.exit_code`. Text uses
`ssm_response_code` and `remote_exit_code` for the same distinction. Completed
durable results can succeed while optional SSM observation is unavailable.

Ctrl-C/SIGTERM return `interrupted` with exit 4 and the known IDs; they request no
remote cancellation. An already established completion retains its actual exit,
even if interruption races with local process exit. Use the printed `logs` recovery
command after detachment. Retain the public ID and trusted storage export. The
[execution contract](docs/contracts.md#selected-exec-and-durable-result-protocol-16)
describes timing bounds, observation fields and incomplete-result behavior.

## Retrieve command results

Use the command ID and the original deployment configuration:

```sh
devbox --config ~/.config/devbox/config.toml logs dc1-0123456789abcdef0123456789abcdef --json
devbox logs dc1-0123456789abcdef0123456789abcdef --stdout-file ./stdout.bin --stderr-file ./stderr.bin --json
sha256sum stdout.bin stderr.bin
devbox logs dc1-0123456789abcdef0123456789abcdef --stream stderr > stderr-copy.bin
```

Default `logs` prints status and stream lengths/checksums. It reports the
publisher's completeness with `verification=not_downloaded`. File exports and
`--stream stdout|stderr` download exact bytes and verify length, SHA-256 and EOF;
they preserve binary data and genuine empty streams. Stream mode writes bytes
to stdout and metadata to stderr, and cannot combine with JSON or exports.
File exports may use JSON, require distinct new paths, and never replace an
existing file. A failed export removes its temporary file; if the first of two
exports succeeded before the second failed, JSON identifies the first verified
file separately.

Retrieval exits **0 even if the original workload failed or timed out**. Check
`workload.exit_code` or `workload.status` for the remote result. Invalid input
exits 2; local timeout/interruption exits 4; missing, pending, denied, incomplete,
corrupt or failed output retrieval exits 1. Known workload status survives an
output error. A failed raw stream may already have emitted unverified bytes;
discard them or retry into a new file. Explicit workload output is user data and
may contain sensitive information; it is not redacted.

The default `--timeout 20s` covers one retrieval, including downloads. Use up to
`--timeout 5m` for larger output or a slower connection. Logs takes a status
snapshot; repeat the same command ID to check a running command later. It never
resubmits or cancels the workload. Completed recovery needs only AWS identity and
the retained storage descriptor, with no SSH tools, local receipts, live worker,
current runtime resources or SSM history. It works after `down` within the
configured retention period. Keep the old deployment's trusted storage config
across upgrades; see [retention and full teardown](docs/setup.md#retaining-result-access).
