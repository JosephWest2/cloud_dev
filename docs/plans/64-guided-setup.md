# Guided installation and foundation setup

Status: implementation added; offline validation and code review recorded in the
PR. Original direction confirmed and reviewed September 20, 2026.
Addresses [issue #64](https://github.com/JosephWest2/cloud_dev/issues/64).
This document records the approved design. Use [guided setup](../guided-setup.md)
for the implemented workflow and limitations. No live AWS acceptance is claimed.

## Problem and intended outcome

The current [README](../../README.md), [installation guide](../arch-packaging.md)
and [foundation guide](../setup.md) require users to assemble a working system:
install several tools, build two runtime artifacts, copy account and identity
values into multiple files, bootstrap and migrate state, choose an AMI and SSH
key, apply infrastructure, export a manifest, and configure the operator profile.
The published v0.1.0 package contains the CLI but not a ready-to-use foundation
bundle. Better documentation alone does not address this workflow.

A first-time user should run an installer and `devbox setup`, answer a short set
of questions, review concrete changes, and finish with usable local configuration
and a verified foundation. They should not need a source checkout, Go, Make,
Python, jq, or hand-edited HCL for the supported release workflow. AWS account
creation and authentication remain with AWS's existing tools.

## Confirmed product decisions

| Decision | Recommended choice | Alternative and tradeoff |
| --- | --- | --- |
| Product interface | Small installer, then a Go `devbox setup` wizard | Standalone setup script avoids CLI integration but separates progress, validation and recovery from the product |
| Initial deployment scope | Create new foundations; also configure a client from an existing trusted manifest | Automate adoption/upgrades too, with substantially more state migration, retention and existing-worker cases |
| Scheduled expiry | Offer and recommend enabling after infrastructure checks, with separate confirmation | Leave disabled with an explicit incomplete-scheduling status and a resumable next step |

The user confirmed all three recommended choices on September 20, 2026.
The sections below develop those choices into an implementation plan.
Initial automated installation targets Arch Linux x86-64;
the existing Linux support boundary and Ohio/Ubuntu worker choices remain intact.
Additional distribution installers, AWS account vending, arbitrary infrastructure
adoption/upgrades, foundation destruction and launching a worker are outside this
first delivery. Existing manual installation and foundation workflows remain usable.

## User journey

1. **Install.** A downloadable, inspectable installer checks platform, executable
   conflicts, existing installations and prerequisites. It selects an explicit
   release and verifies its downloads, installs the Arch package through pacman,
   and offers the required access/setup tools. Show the exact package operations
   before requesting privilege. Re-running preserves user files and avoids
   duplicate installations. Show `devbox setup` as the next command.
2. **Choose setup mode.** `devbox setup` offers a new foundation or connection to
   an existing manifest. Detect an unfinished managed setup and offer to resume
   it. Existing user configuration supplies proposed values; it is not overwritten.
3. **Resolve local prerequisites and AWS identity.** Check OpenSSH, Session Manager
   plugin version/logging, AWS CLI v2, and exact OpenTofu 1.12.6. Select an existing
   AWS setup profile and explain authentication failures with an exact login next
   step. Prepare and test any required credential-process bridge before SDK or
   OpenTofu calls. Confirm expected account, Ohio region, short deployment name, stable
   owner, and operator source IAM principal. Resolve IAM role paths with trusted
   AWS/profile information; never turn an STS session ARN into a guessed IAM ARN.
   Show detected values for confirmation rather than silently choosing identity.
4. **Prepare inputs.** Generate a dedicated Ed25519 key through `ssh-keygen` or
   validate a selected existing key and matching public key. Interactive key
   passphrases stay in the terminal; only the public key enters infrastructure
   inputs. Derive the public key from the private key with `ssh-keygen` and compare
   it with the selected public key; checking an adjacent `.pub` alone is insufficient.
   Record the intended key path before generation, never replace an existing
   private key on resume, and recover a missing `.pub` only from that private key.
   Discover and validate the exact Canonical AMI and proposed AZ/type
   choices; freeze them for the plan. Offer a collision-resistant state bucket
   name, distinct bootstrap/foundation keys, and the existing resource defaults.
5. **Bootstrap state.** Show a scoped S3-only plan, obtain approval, apply that
   saved plan, then migrate the bootstrap state to the new bucket with native S3
   locking. Verify migration before foundation provisioning. Keep the local
   recovery copy until verified. Existing bucket ownership or same-named resources
   do not authorize adoption; ambiguous collisions stop with recovery guidance.
6. **Provision the foundation.** Reuse or explicitly create the account-wide EC2
   Spot service-linked role with the setup identity. Present that action separately
   from the OpenTofu plan. Prepare a foundation plan containing the current v6
   infrastructure, runtime artifacts and independent failure evidence, with its
   cleanup schedule disabled. Preview account, scope, resource actions and durable
   billable services. After approval, apply those exact saved bytes and export
   only `deployment_manifest` to a temporary file for validation.
7. **Configure and verify the operator.** Preview edits to devbox TOML and the
   named AWS profile, preserving unrelated settings and any existing credentials.
   Set the exact operator role and required role-session name. Support the
   preflight-tested `aws login` credential-process bridge when needed, without
   ever displaying credential output. Validate and atomically install the manifest and
   configuration, then run resource checks with the explicit operator profile.
8. **Offer scheduled cleanup.** Verify function, schedule, IAM and failure-evidence
   configuration before offering enablement. Approval applies a second saved
   plan whose only intended managed-resource change is enabling that schedule;
   other drift requires a new review. Re-export and check real enabled state,
   alarms and a scope-matched scheduled completion occurring after the recorded
   enablement attempt. Waiting is bounded and resumable; a pending first invocation
   never becomes fabricated success. Timeout leaves the schedule enabled and
   health pending; resume observes without another apply unless fresh review is needed.
9. **Hand off.** Report separate local installation, foundation verification and
   scheduled-cleanup health results, plus any pending next step. Give the exact
   operator-scoped first-launch command and `doctor` command. Setup creates no
   worker and does not claim SSH, effective launch authorization or laptop-offline
   root-volume deletion has been tested.

The existing-manifest mode needs no OpenTofu, setup administrator or runtime
bundle. It validates the manifest's scope and matching local SSH identity,
previews local config/profile changes, and runs applicable operator checks.
Legacy v4/v5 manifests may support recovery but must be reported as requiring a
real foundation upgrade for new launches; no schema rewriting or fabricated IDs.
It never applies infrastructure or offers scheduler mutation without the managed
setup workspace needed to plan that operation.

## Implementation design

### Installation and release assets

- Extend release production to publish a versioned setup bundle containing the
  exact two infrastructure roots, committed provider locks, bootstrap templates,
  Linux/amd64 runner, cleanup ZIP, source commit and per-file SHA-256 metadata.
  Preserve the roots' relative `../../bin` artifact layout. Test the assembled
  archive itself, not just its source tree. Match CLI, bundle and artifact release;
  reject missing, mixed-version or changed bytes before any apply.
- Keep the Arch package as the owner of `/usr/bin/devbox`; detect the unrelated
  Jetify executable/package conflict before installation. The bundle is optional
  for ordinary CLI use and available to already-installed release users. Store
  immutable versions in the user's data directory and per-deployment working
  copies in their private state directory, outside the checkout.
- The installer handles devbox, AWS CLI and OpenSSH through reviewed Arch package
  operations. Supply exact OpenTofu in a private versioned tools directory when
  the system version differs, following upstream artifact verification. Session
  Manager uses [AWS's prebuilt Linux x86-64 Debian archive](https://docs.aws.amazon.com/systems-manager/latest/userguide/install-plugin-debian-and-ubuntu.html), downloaded directly
  from the upstream distribution and checked against a project-pinned version
  and digest. Extract only reviewed binary/support paths into a private versioned
  directory and expose its executable in the user's local bin directory. Validate
  logging configuration and help the user make that bin directory effective on
  PATH through a previewed shell-config edit. Test this layout on Arch; do not
  claim AWS vendor support for Arch. Slice 1 must pin and prove the precise archive
  layout and verification method before shipping; do not fall back to source
  compilation or an arbitrary AUR helper if it fails. Dependency delivery is part
  of the installer milestone, not a link back to the current long manual guide.
- Pin the selected release for the whole run, including prerelease opt-in; do not
  resolve a moving latest version midway through setup. Existing checksums detect
  corruption and are not publisher signatures. Reject unsafe archive paths and
  symlink traversal. Installer tests cover corrupt/truncated downloads, wrong
  platform, package conflicts, failed privilege and interrupted installation.
- First-release automatic provisioning supports matching numbered CLI/bundle
  releases. Git packages may use existing-manifest mode; fresh provisioning
  requires a deliberately prepared bundle from the exact build commit or directs
  the user to a numbered release. Resume after a CLI update must use the journal's
  original release/bundle with a compatible journal reader. Reject incompatible
  versions with a concrete recovery command; never upgrade the workspace silently.

### Setup engine and local data

- Add `internal/setup` for typed inputs, stage transitions, safe local writes and
  injected AWS, process, clock and prompt dependencies. Add a dedicated setup CLI
  adapter alongside the existing cleanup dispatch pattern; keep ordinary command
  parsing and recovery paths unchanged.
- Store a versioned, private setup journal under the XDG state directory, keyed
  by the confirmed account/region/deployment/owner. Record selected release and
  hashes, input digest, workspace/backend identity, stage outcomes, local file
  locations and outstanding actions. Do not store credentials or private-key
  bytes. Use atomic writes, private permissions and an exclusive workspace lock.
- Define `devbox setup`, `devbox setup --resume SETUP_ID`, and
  `devbox setup --manifest PATH` as the initial command surface. Re-running offers
  the matching journal. A read-only `devbox setup status SETUP_ID --json` reports
  recorded progress as recorded progress, not fresh AWS verification. Interactive
  mutation has no blanket `--yes` in this first delivery; non-terminal invocation
  returns an actionable confirmation/input-required result before mutation.
- Preserve config comments/unrelated keys and named AWS profile sections. Preview
  replacements, back up modified files privately, reject unexpected symlinks and
  detect concurrent edits before atomic replacement. Stage manifest/config as a
  recoverable multi-file transaction; failed validation preserves previous files.
- Keep live setup workspaces separate from `infra-check` checkouts. Let OpenTofu
  own state and migration; Go never parses or edits state. Setup consumes saved
  plan metadata and the named manifest output. Ordinary CLI commands still read
  only the trusted exported manifest and never invoke OpenTofu.

### Approval, identity and interruption

- Before the first SDK or OpenTofu provisioning operation, prepare and validate
  any `aws login` credential-process bridge required by the selected source.
  Preserve the source profile and AWS config path, avoid recursive bridges, and
  test credential compatibility for the CLI SDK and OpenTofu backend/provider
  independently. Repeat the compatibility check for the final operator profile.
- Use explicit setup credentials for all provisioning and explicitly select the
  restricted operator profile for verification, regardless of ambient
  `AWS_PROFILE`. Revalidate the expected account before every cloud mutation and
  bind provider/backend allowed accounts and region to confirmed inputs. Resolve
  principal details with bounded, typed reads; a denied read never proves absence.
- Check the effective principal as well as the account. In particular, final
  verification must prove the manifest's operator role and exact session name;
  an administrator in the same account cannot satisfy that claim. Freeze and
  revalidate the setup principal too, permitting refreshed sessions of the same
  confirmed role. Add a same-account wrong-role test.
- Define a tested credential/environment policy separately for SDK, AWS CLI,
  backend and provider processes. Remove conflicting ambient selectors and
  OpenTofu argument/variable overrides; explicitly preserve only the selected
  profile's required credential mechanism, including a declared
  `credential_source=Environment` when applicable. Do not accidentally strip
  intentional source credentials or allow them to bypass an assumed role. Honor
  selected AWS config/credentials file paths without copying credential contents.
- Bind each approval to account/scope, input/artifact digests and saved plan bytes.
  Apply the saved plan, never substitute a fresh implicit plan. Changed inputs,
  binaries, infrastructure source or identity invalidate approval. Fresh setup
  does not accept destructive/replacement plans or silently adopt existing state.
- Treat interruption/timeout during apply or migration as an uncertain partial
  operation. Retain the workspace and recover through locked OpenTofu operations,
  refreshed plans and renewed review. Never recreate a bucket, force-unlock,
  delete state, roll back cloud resources, or blindly replay apply on journal
  evidence alone. A journal is a recovery aid, not authority over remote state.
- Persist and flush stage intent before each mutation, including bootstrap,
  Spot-role creation, state migration, each apply and local-file publication.
  Failure to durably record intent blocks dispatch; loss of the completion write
  leaves an uncertain stage, never a reason to repeat creation automatically.
- Give setup its own bounded stage deadlines; the existing CLI's five-minute
  maximum is unsuitable for complete provisioning. Proposed defaults are 20
  minutes per apply/migration and 15 minutes for initial scheduler observation.
  Prompt time is distinct from cloud-operation deadlines. Continue to bound AWS
  calls and credential helpers.
- Audit `cmd/devbox` supervision explicitly: its current seven-second forced
  termination can interrupt OpenTofu state persistence. Add a setup-only graceful
  shutdown policy with a bounded drain and durable uncertain-stage reporting;
  retain existing exec detachment and other commands' signal behavior. EOF or
  failed preview output never grants confirmation.
- Render allowlisted plan/action summaries, not arbitrary SDK/provider errors.
  Raw saved plans and process output remain private local diagnostic artifacts;
  extract only required fields from plan JSON and discard unnecessary values.
  Route progress/prompts to the terminal/stderr and structured output to stdout.
  Credential helper output and private-key material must not enter diagnostics.

### Bootstrap migration protocol

1. Record the new bucket, expected owner, source workspace, destination backend
   and distinct state keys before bootstrap apply. Require a durable local state
   recovery copy before migration and hold the setup workspace lock throughout.
2. Verify bucket ownership/protections and the exact remote state destination.
   A denied or indeterminate lookup does not mean the key is empty. An existing
   destination enters reconciliation; it is not permission to overwrite it.
3. Obtain migration approval bound to those paths and the recorded bootstrap.
   Run native `init -migrate-state` with locking and committed provider locks.
   Implement and fixture-test the pinned tool's confirmation interaction; accept
   only the expected empty-destination migration prompt after approval, and stop
   on overwrite or unrecognized prompts. Never use blanket `-force-copy`.
4. Verify the remote backend independently of a leftover local backend cache:
   initialize a separate verification directory with the exact remote backend,
   read the named bootstrap bucket output and require a no-change plan against
   the frozen bootstrap inputs. OpenTofu handles state; Go does not inspect its
   bytes. Keep the local recovery copy until this succeeds.
5. After an uncertain migration, inspect the destination first. If remote state
   exists, use the independent verification path; never replay local migration
   over it. If it is proven absent and the original source is intact, prepare a
   newly reviewed migration. Conflicting or unverifiable state requires manual
   recovery. Resume foundation provisioning only after one authoritative remote
   backend is established. Never automatically remove its lock or recovery data.

OpenTofu documents that migration can prompt and that `-force-copy` answers all
migration questions affirmatively; this is why setup needs a bounded prompt
adapter instead of unattended forced copying. See
[backend initialization](https://opentofu.org/docs/cli/commands/init/).
Saved-plan application supplies its own approval to OpenTofu, so the wizard must
enforce its approval before invoking it; see
[saved-plan apply](https://opentofu.org/docs/cli/commands/apply/).
Plan JSON can include sensitive values; parse only needed action fields and keep
the full artifact private, following
[show JSON output](https://opentofu.org/docs/cli/commands/show/).

### Verification and result contracts

Reuse config/SSH-key validation, identity loading, `internal/foundation` checks
and doctor output where possible. Do not make `doctor` pass when the schedule is
disabled or no successful recent run exists. Its present alarm check combines
configuration with alarm state; factor reusable checks so pre-enable verification
can validate configuration without requiring a run that cannot exist yet. Runtime
health still requires the current full checks, including actual alarm state.

Define setup input, journal, bundle and result schema version 1 before coding.
Results contain setup ID, confirmed scope, phase/status, completed/pending stages,
safe error code, recovery command and the three separate verification outcomes.
Use exit 0 only for the selected mode's requested completion, 1 for failed
prerequisites/operations, 2 for invalid input or missing confirmation, and 4 for
timeout/interruption; partial cloud state is explicit in the result regardless
of exit. If enablement was requested, pending health is incomplete, not success.
If the user declines enablement, report foundation completion and scheduling
disabled explicitly. Document exact field/exit precedence in the contract slice.

## Delivery slices and validation

| Slice | Concrete deliverable | Required evidence |
| --- | --- | --- |
| 1. Contract and recovery design | Finalize setup schemas, phase/approval and migration recovery tables, exact dependency acquisition and interruption policy | Review fault cases before provisioning code; verify pinned-tool behavior and prebuilt plugin layout with offline fixtures |
| 2. Release bundle and installer | Matching prebuilt foundation assets and guided Arch dependency installation | Fresh Arch container installation, rerun/conflict/corruption cases, existing packaging checks; no Go/Make/Python/jq/source checkout in user setup |
| 3. Guided local setup | Typed prompts, identity/key checks, config/profile transactions, existing-manifest mode | Temp-directory and fake-credential tests; early login bridge, wrong-role rejection, private/public mismatch, no clobbering/leaks, legacy launch status, non-TTY behavior |
| 4. New foundation orchestration | Private workspace, bootstrap/migration, Spot role, approved foundation apply, validated export and resume | Fake process/AWS sequence tests; wrong identity, denied-vs-missing, migration interruption, lost journal write, stale plan/artifact, concurrent setup and uncertain apply |
| 5. Verification and scheduling | Operator verification, staged evidence checks, optional reviewed enablement and bounded health wait | Disabled, pending, stale pre-enable evidence, drift, alarm failure, expired credentials and observe-only resume after timeout; preserve doctor semantics |
| 6. End-to-end delivery | Short README onboarding, detailed recovery guide and recorded acceptance | Offline install-to-setup harness plus separately authorized clean-account/deployment acceptance; existing deployment files preserved |

Run focused tests during development, then `make check` and `make build` for Go
changes; `make runner`, `make cleanup-check`, and
`make infra-check TOFU=/path/to/tofu` in a separate clean checkout for bundled
runtime/infrastructure changes. Run the existing Arch checks plus new installer
fixtures. Verify CLI help, documentation links and the `CLAUDE.md` symlink.

Live acceptance needs an explicitly selected account, region, deployment, owner
and permitted operations. Demonstrate the complete fresh-install flow, an
interrupted/resumed stage and a verified exported v6 foundation under the operator
profile. Verify enabled scheduling through real retained completion evidence;
an empty successful scheduled run proves plumbing, not root-volume deletion.
A separately authorized first-worker smoke test should cover launch, SSH and
explicit teardown with independent root-volume evidence. Full fault injection,
Scheduler retry exhaustion (#58), and replaying old acceptance campaigns are not
part of routine setup. Record only observed results.

Issue #64 is complete when both guided workflows ship and a fresh supported
machine reaches verified setup without manually copying configuration values,
editing HCL or building artifacts, with actionable recovery after failure.

## Review record

The user confirmed the three product decisions above. An independent subagent
reviewed this plan against the repository's guides, Go code, infrastructure and
release workflow. It recommended the direction after six amendments, incorporated
above: move credential compatibility before provisioning; specify durable intent
and uncertain migration recovery; verify exact principals across credential paths;
prove SSH key correspondence; make dependency delivery/version compatibility
concrete; and require cleanup evidence after this enablement attempt.

The review requested no further product decisions. Exact process fixtures,
dependency pins and journal/result field definitions are deliverables of slice 1,
not claims of tested implementation. This task covers investigation and planning,
not implementation. No AWS mutation or live acceptance occurred.

The reviewer rechecked the amended plan and confirmed that all six findings were
addressed, with no remaining blocking design issue. Its approval remains subject
to the explicitly planned slice-1 feasibility tests. Plan/index relative links,
diff whitespace and the `CLAUDE.md` symlink were checked locally; no Go or live
AWS tests were run for this documentation-only task.
