# Guided installation and setup

`devbox setup` guides local configuration and creation of a new AWS foundation.
It prepares the files, previews changes, applies only approved saved plans, and
verifies the deployment through the restricted operator role. It creates no worker.
Existing foundation adoption, upgrades and destruction use the
[manual foundation guide](setup.md).

## Install from a release

Choose a numbered [GitHub release](https://github.com/JosephWest2/cloud_dev/releases)
that includes `install.sh` and a matching `cloud-dev-VERSION-setup-linux-amd64.tar.gz`.
These assets are produced by the release workflow; releases predating guided
setup do not contain them. Automated installation targets Arch Linux x86-64.

Download `install.sh` from that release, inspect it, then run it as your normal
user, replacing `VERSION` with the selected version:

```sh
bash install.sh --version VERSION
```

Add `--prerelease` only when intentionally choosing a prerelease. The installer
verifies downloaded checksums, installs the Arch CLI package and AWS CLI/OpenSSH
through reviewed pacman operations, and installs pinned OpenTofu and Session
Manager binaries under your user data directory. It does not require Go, Make,
Python, jq, a source checkout or an AUR helper. Checksums detect corruption; they
are not detached publisher signatures. Package/executable conflicts and modified
existing bundles stop installation rather than replacing unrelated software.

OpenTofu 1.12.6 and Session Manager 1.2.835.0 are downloaded from their upstream
release distributions. The Session Manager binary is extracted from AWS's Debian
archive into a private directory; this project tests that layout on Arch and does
not claim AWS vendor support for Arch. Keep plugin logging disabled at
`/usr/local/sessionmanagerplugin/seelog.xml`. The installer offers a PATH edit for
Bash/Zsh if `~/.local/bin` is missing; apply its displayed command to the current
shell too. AWS credentials, SSH keys and cloud resources are not created by the
installer.

Already-installed numbered releases can use the same installer to obtain the
matching setup bundle. A source developer can build matching artifacts with
`make build setup-bundle VERSION=0.0.0`, extract the generated archive into a fresh
private directory, and pass that directory with `--bundle`. Keep release version
and source/artifact bytes together. Production use should select a tested release.

## Create a foundation

First authenticate an existing AWS profile with your normal AWS login or SSO
workflow. Account creation and interactive authentication remain with AWS tools.
Then run:

```sh
devbox setup --aws-profile YOUR_SOURCE_PROFILE
```

Setup asks once for the expected account, deployment and stable owner, uses Ohio
(`us-east-2`), and offers a dedicated Ed25519 key. It derives the public key from
the private key to prove the pair matches. Passphrases stay in `ssh-keygen`; private
keys never enter OpenTofu or the journal. Load encrypted keys with `ssh-add` for
later SSH use.

For a new foundation, the source profile needs the setup administration permissions
in the [manual guide](setup.md#tools-and-identities), including resolving the
source IAM role with `GetRole`. Setup adds a separate credential-process bridge
using AWS CLI export-credentials. This supports browser-login sources without
copying credentials into configuration. It preserves the original profile and
rejects conflicting profile names or recursive credential chains. It verifies
both the source and bridge identities before provisioning.

The wizard then:

1. Pins the current Canonical Ubuntu 24.04 x86-64 AMI, the release artifacts, and
   the existing default AZ/type pool and 100 GiB disk settings.
2. Previews and applies a state-bucket plan after approval, migrates local state
   to S3 with locking, and independently verifies the remote backend.
3. Checks or offers to create the shared EC2 Spot service-linked role.
4. Previews and applies the foundation with scheduling disabled, then validates
   the real v6 manifest and previews local config/operator-profile changes.
5. Verifies deployment and failure-evidence configuration using the exact operator
   role and required session name. It offers to enable automatic expiry cleanup
   with a separately reviewed plan and waits for actual post-enable health evidence.

Local previews include the selected identity and new configuration values, file
paths and digests. Existing unrelated TOML/AWS settings are preserved, with private
backups and detection of concurrent edits. The source and restricted operator
profiles remain distinct. Explicit profile settings are passed to OpenTofu's
provider and backend; ambient `AWS_PROFILE` cannot select the provisioning identity.

The final output distinguishes local tools, foundation verification, and scheduled
cleanup health. Declining enablement leaves scheduling disabled and says so;
resume can offer it again. No passing setup result proves worker bootstrap, SSH,
effective launch authorization, or deletion of a worker's disk. Run the displayed
first-worker command when ready. Shared storage and cleanup/evidence services can
incur charges even without workers.

## Connect an existing foundation

```sh
devbox setup --manifest /absolute/path/deployment.json --aws-profile YOUR_PROFILE
```

This validates the existing export and matching private key, prepares local
configuration, and runs operator checks. An existing profile already resolving to
the exact operator role/session is used directly. A source profile needing the
operator role gets a separate bridge/role profile instead. This mode needs no
OpenTofu, provisioning bundle or setup administration rights when using an already
configured operator. It does not mutate infrastructure or enable scheduling.

Legacy v4/v5 foundations can be connected for applicable observation and recovery,
but new launches still require a real v6 upgrade. Setup never changes a manifest's
schema version or invents resource IDs. Preserve old manifests needed for retained
result access.

## Resume and recover

Each run prints a setup ID. Journals and workspaces live under
`$XDG_STATE_HOME/devbox/setup` (default `~/.local/state/devbox/setup`). Bundles and
tools live under `$XDG_DATA_HOME/devbox` (default `~/.local/share/devbox`). Keep these
private, including native OpenTofu state, saved plans and configuration backups.

```sh
devbox setup status SETUP_ID --json
devbox setup --resume SETUP_ID
```

Status reads recorded progress only; it does not contact AWS or prove current
health. Resume uses the recorded release, identities and paths. If the CLI was
upgraded, use the original release and bundle to finish that run; setup will not
upgrade an in-progress workspace silently. Keep custom AWS config/credentials
file environment settings the same when resuming.

| Interrupted stage | Resume behavior |
| --- | --- |
| Local publication | Checks the saved old/new digests and completes the approved transaction; concurrent edits require review |
| Bootstrap or foundation apply | Reads existing state through OpenTofu and produces a newly reviewed plan; uncertain creation without authoritative state requires manual recovery |
| State migration | Checks the exact remote destination first; an existing remote state is independently verified and never overwritten from the old local copy |
| Schedule apply | Reconciles a fresh schedule-only plan; unrelated drift blocks automatic changes |
| First health observation | Leaves the schedule enabled and observes it without another apply |

Ctrl-C requests graceful shutdown of a provisioning tool and allows up to 90
seconds for it to drain before forced cleanup. Interrupted cloud calls may have
partially succeeded. Setup records intent before dispatch and retains recovery
information. Never remove `.terraform`, state, journals or locks to make an
uncertain operation look fresh. Never force-unlock without establishing that the
lock holder is gone. The native pre-migration recovery copy is
`infra/state-bootstrap/pre-migration.recovery` inside the setup workspace.

`manual_recovery_required` means setup cannot prove one authoritative backend or
safe continuation. Inspect that exact private workspace with the original tool
version and setup profile using the [state recovery guidance](setup.md#recovery-and-teardown).
Resolve the state conflict before resuming; do not start a replacement deployment
with the same resource names or copy stale state over the remote key.

Per-tool stages default to 20 minutes (`--timeout`, maximum 1 hour). Initial
scheduler observation defaults to 15 minutes (`--health-timeout`, maximum 30
minutes). Prompt time is separate. `--json` writes one versioned result to stdout;
prompts and safe progress go to stderr. Mutation requires terminal confirmation;
there is no blanket `--yes`. Exit codes are 0 complete, 1 failed, 2 invalid input
or missing confirmation, and 4 interrupted/timed out. A failed result preserves
partial progress and its recovery command.
