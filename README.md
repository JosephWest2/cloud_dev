# devbox

devbox is a command-line tool for disposable AWS development machines. Launch an
Ubuntu machine, connect with SSH or a remote editor, run commands and retrieve
their output, then remove the machine and verify its root disk was deleted.
SSH travels through AWS Systems Manager (SSM), so no inbound ports are opened.

The current target is **Linux locally** (starting with Arch Linux) and **Ubuntu
24.04 LTS x86-64 in AWS Ohio (`us-east-2`)**. Workers use public subnets and public
IPv4. The bundled `agent` machine profile uses Spot instances by default;
`--on-demand` opts into On-Demand. There is no automatic fallback or replacement
of interrupted workers.

## Install

### Arch Linux package (x86-64)

Download the package and checksums from the [v0.1.0 release](https://github.com/JosephWest2/cloud_dev/releases/tag/v0.1.0),
then verify and install:

```sh
curl -fLO https://github.com/JosephWest2/cloud_dev/releases/download/v0.1.0/cloud-dev-0.1.0-1-x86_64.pkg.tar.zst
curl -fLO https://github.com/JosephWest2/cloud_dev/releases/download/v0.1.0/SHA256SUMS
sha256sum --check --ignore-missing SHA256SUMS &&
sudo pacman -U ./cloud-dev-0.1.0-1-x86_64.pkg.tar.zst
devbox version
devbox --help
```

This installs the `cloud-dev` package and the `devbox` command; no Go toolchain
or AUR account is needed to install the package. It conflicts with Jetify's
unrelated `devbox` packages because they use the same command name.

Install later releases with `pacman -U` as well; `pacman -Syu` does not fetch
updates for this downloaded package. Continue with [AWS setup](#set-up-aws-once)
after installation. See [Arch packages](docs/arch-packaging.md) for optional SSH
dependencies, building with `makepkg`, and removal instructions.

### Build from source

You need Go 1.24 or newer, Git and Make. On Arch Linux:

```sh
sudo pacman -S --needed go git make openssh jq
```

Build and install from a checkout:

```sh
git clone https://github.com/JosephWest2/cloud_dev.git
cd cloud_dev
go mod download
make build
mkdir -p "$HOME/.local/bin"
GOBIN="$HOME/.local/bin" make install
export PATH="$HOME/.local/bin:$PATH"
devbox --help
```

Persist that PATH setting in your shell configuration. The machine profile is
embedded in the binary; ordinary CLI use does not require the checkout.

## Set up AWS once

You need an AWS account and an authenticated AWS profile. Follow the
[foundation setup guide](docs/setup.md) before launching your first worker. It
walks through installing AWS CLI v2 and OpenTofu 1.12.6, provisioning shared AWS
resources, creating a dedicated SSH key, and exporting the deployment manifest.
All new launches require the current **v6 foundation**.

For SSH access, install OpenSSH and the
[AWS Session Manager plugin](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-working-with-install-plugin.html)
(version 1.2.764.0 or newer, with plugin logging disabled).
See [local access tools](docs/configuration.md#local-access-tools) for details.

The setup guide also walks through copying [the example config](examples/config.toml)
to `~/.config/devbox/config.toml` (or `$XDG_CONFIG_HOME/devbox/config.toml`) and
setting your account, region, deployment, stable owner ID, operator profile and
SSH key path. Credentials stay in your normal AWS configuration.

After setup, verify the deployment with the restricted operator profile:

```sh
devbox doctor --aws-profile devbox-operator --timeout 60s
```

Use your operator profile's name if different. The explicit flag takes precedence
over any `AWS_PROFILE` left over from setup. For existing deployments, follow the
[upgrade instructions](docs/setup.md#upgrade-to-the-multi-az-spot-foundation-29).

## Use a devbox

Launch one worker and list it:

```sh
devbox up agent --aws-profile devbox-operator
devbox ls --aws-profile devbox-operator
```

Copy the instance ID from the result, then connect:

```sh
worker=i-REPLACE_WITH_RETURNED_INSTANCE_ID
devbox ssh "$worker" --aws-profile devbox-operator
```

Run your interactive work in that shell, then type `exit` to return locally.
**Exiting SSH leaves the worker running.** For remote editors and file transfers,
see [SSH configuration and group usage](docs/usage.md#ssh-and-remote-editors).

You can also run a noninteractive command from your local terminal:

```sh
devbox exec "$worker" --aws-profile devbox-operator -- uname -a
```

`exec` reports command status and prints a `dc1-...` command ID; it does not print
the command's output. Copy that ID to retrieve the output:

```sh
command_id=dc1-REPLACE_WITH_RETURNED_COMMAND_ID
devbox logs "$command_id" --aws-profile devbox-operator --stream stdout
```

When finished, remove the worker and its root disk:

```sh
devbox down "$worker" --aws-profile devbox-operator --timeout 5m
```

Check that teardown reports both termination and root-volume deletion. Saved
command results survive worker removal (30-day retention by default). Shared
foundation resources remain and can still incur charges; see
[full foundation teardown](docs/setup.md#recovery-and-teardown).

Workers default to a **2-hour lifetime**; use `up agent --ttl 4h` to choose another
(up to 168h). Active work never extends that deadline. Scheduled cleanup starts
**disabled** and must be enabled and verified through setup; expiry is not an
exact termination guarantee. Use `down` when finished rather than waiting for expiry.
If a launch times out or has an uncertain result, retain its request ID and follow
[launch recovery](docs/usage.md#launch-recovery) before starting another launch.

## More information

- [Documentation index](docs/README.md): guides, references and development plans.
- [Configuration and diagnostics](docs/configuration.md): profiles, paths and `doctor`.
- [Usage guide](docs/usage.md): groups, recovery, expiry cleanup and command results.
- [Development guide](docs/development.md): repository layout, builds and checks.
- [Agent instructions](AGENTS.md): project guidance for coding agents.
