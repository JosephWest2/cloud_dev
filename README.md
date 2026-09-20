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

## Install and set up

On Arch Linux x86-64, use the [guided installer and setup](docs/guided-setup.md).
Choose a numbered GitHub release containing `install.sh` and the matching setup
bundle, download and inspect its installer, then run:

```sh
bash install.sh --version VERSION
```

Replace `VERSION` with the selected release. The installer verifies downloads,
installs the CLI and local dependencies, and prepares matching foundation
artifacts. Releases predating guided setup do not contain these assets.
No source checkout, Go, Python or manual artifact build is needed for this path.
See [Arch packages](docs/arch-packaging.md) for direct package/source installation.

Authenticate an existing AWS profile with your usual AWS login or SSO workflow,
then run:

```sh
devbox setup --aws-profile YOUR_SOURCE_PROFILE
```

The wizard asks for your expected account, deployment and stable owner, prepares
a dedicated SSH key and configuration, shows the AWS changes for approval,
provisions the v6 foundation, and verifies it through the restricted operator
role. It offers separately confirmed scheduled-expiry enablement. It creates no
worker. Keep the printed setup ID to resume after an interruption.

To connect an existing deployment instead:

```sh
devbox setup --manifest /path/to/deployment.json --aws-profile YOUR_PROFILE
```

This configures the local client without provisioning infrastructure. Existing
foundation adoption/upgrades and full teardown remain in the
[manual foundation guide](docs/setup.md). See [configuration](docs/configuration.md)
for credential/profile precedence and [guided recovery](docs/guided-setup.md#resume-and-recover)
for partial setup.

Use the operator profile printed by setup for all worker commands below; replace
`devbox-operator` with that name.

### Direct package installation

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
updates for this downloaded package. This older release uses the [manual AWS setup](docs/setup.md) workflow. See [Arch packages](docs/arch-packaging.md) for optional SSH
dependencies, building with `makepkg`, and removal instructions.

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
