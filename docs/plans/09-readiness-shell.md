# Issue #9 implementation plan

Status: reviewed by a GPT-6 Astra high subagent on September 11, 2026;
review adjustments incorporated below. The user confirmed dedicated local SSH keys on September 12, 2026. Based on merged #7/#8 at `017d268`.

## Outcome and existing decisions

`up` waits for independently observed EC2, SSM and bootstrap readiness; `ls`
reports those signals without treating EC2 running as ready. `ssh NAME_OR_ID`
opens an actual OpenSSH shell as `devbox` through `AWS-StartSSHSession` with no
inbound rules. Supply an OpenSSH configuration/proxy interface usable by scp,
sftp and remote editors. Repository decisions from #6 confirm this access model;
the older native-shell proposal in issues #1/#9 must be reconciled explicitly.
No general remote command runner, logs, output storage, Spot or TTL is added.

## Confirmed SSH key decision

Recommend a dedicated user-managed SSH key for this single-owner MVP. The user
keeps the private key locally (an encrypted key may use ssh-agent); OpenTofu takes
only its public key and bootstrap installs it for `devbox`. CLI configuration
selects the local identity file. Rotation updates the foundation/template and
applies to newly launched workers; existing workers retain the original key
until manually removed. Never generate private keys in OpenTofu or put them in
state, manifests, user data or request receipts.

Alternative: EC2 Instance Connect publishes a public key for a short authentication
window at each connection. That avoids durable authorized keys but needs another
AWS API, scoped `SendSSHPublicKey` permissions, custom-user integration in sshd,
and reconnect/editor acceptance. Both options still tunnel SSH through SSM.
The user selected the dedicated-key option on September 12, 2026.

Host trust recommendation: obtain the instance's public Ed25519 host key through
the pinned, parameterless SSM readiness document, then use strict OpenSSH host
verification in a dedicated local known-hosts file keyed by account, region and
instance ID. This trusts authenticated AWS/SSM scope and the trusted document;
it is not an independent defense against compromise of that control plane or
root on the target. Never use `StrictHostKeyChecking=no` or silently replace a
previously trusted key. Public host keys are not credentials.

## Implementation sequence

1. Add injectable SSM observation APIs alongside the existing verified EC2
   service. Preserve scoped AWS inventory and revalidate explicit IDs immediately
   before SendCommand and StartSession. Reject ambiguous names and all account,
   deployment or owner mismatches before remote action. Keep cleanup independent
   of manifests, profiles, local SSH files and receipts.
2. Define observed states and bounded polling. EC2 retains its API state; SSM
   distinguishes online, offline/not registered, and unknown with a sanitized
   reason; bootstrap distinguishes pending, complete, failed and unknown.
   Aggregate ready requires running + online + complete. Probe permission errors,
   missing documents, malformed/truncated output, stale/in-flight invocations and
   service failures must never become complete or a fabricated bootstrap failure.
   A failed bootstrap marker ends a wait immediately with the instance retained.
3. Run only the pinned name/version/content-hash readiness document. Extend its
   fixed output to a small versioned status plus public host key, prioritizing a
   failure marker if markers conflict. Retain command IDs while polling
   GetCommandInvocation; retry InvocationDoesNotExist within the deadline, avoid
   overlapping probes for one target, cap parsed output and allowlist all fields.
   No document parameters, arbitrary scripts, S3 or CloudWatch output are exposed.
4. Integrate observation into up/ls without weakening restart recovery. Default
   up readiness/connection setup deadline: 5 minutes; default ls/doctor deadline:
   20 seconds. Preserve --timeout (positive, maximum 5 minutes) for short failure
   tests. A resumed dispatched request first reconciles its original identity;
   unavailable/currently incompatible readiness metadata must retain allocation
   IDs and must never authorize a fresh allocation. ls returns EC2 inventory even
   if readiness metadata/probes fail, with explicit unknown states and a partial
   observation failure. down continues to work with no working manifest/profile.
   Report progress transitions on stderr; JSON stdout remains one final envelope.
5. Update the foundation for the selected key strategy and host-key probe. For
   dedicated keys, validate a single supported OpenSSH public key, render it safely
   into bootstrap, preserve the exact rendered bootstrap digest, and update the
   manifest/config version where required. Pin readiness output version and host
   key contract. Update doctor, immutable receipt compatibility, IAM tests and
   fixture exports together. Old receipts still allow reconciliation/cleanup;
   old manifests give an actionable re-export error for new access operations.
6. Implement local prerequisite checks and shell setup with injectable process
   runners. Check Linux, OpenSSH, Session Manager plugin, identity/public-key
   availability and secure local path permissions before StartSession. Generate
   local connection artifacts atomically under the user's private devbox state
   directory; do not edit ~/.ssh/config implicitly. `ssh-config NAME_OR_ID`
   produces a scoped, instance-ID-pinned OpenSSH stanza; an explicit proxy command
   revalidates scope and opens only AWS-StartSSHSession on port 22. Quote paths and
   config/profile arguments safely for both OpenSSH parsing and ProxyCommand's
   shell, including whitespace, percent tokens and shell metacharacters.
7. Feed the Session Manager response to the plugin through its supported
   AWS_SSM_START_SESSION_RESPONSE environment handoff, never command arguments or
   diagnostics. Disable SDK logging and keep raw plugin diagnostics/session tokens
   out of logs. Permit the supported plugin's OpenSSH-compatible startup preamble
   on proxy stdout, then pass the established binary stream without filtering;
   no devbox results/progress may enter that stream. Preserve actionable
   allowlisted failures on stderr. Terminate the known SSM session on plugin/SSH
   completion or failed startup using a separate bounded cleanup context; diagnose
   cleanup failure without hiding the SSH result. Verify the existing operator
   session-ARN restriction against actual assumed-role session IDs and adjust
   policy narrowly if needed.
8. Separate setup deadlines from session lifetime. Reject --json for interactive
   ssh/proxy before AWS calls; ssh-config is noninteractive and can emit a separate
   documented JSON configuration result. The interactive shell inherits the
   terminal streams; Ctrl-C reaches the remote foreground command, exit/Ctrl-D
   closes the shell, and terminal size changes work. Return the SSH exit status
   after session start (255 for transport/authentication failure); preflight uses
   existing CLI error codes, including 4 for setup timeout. Fix the Linux process
   supervisor's background process-group behavior so a real TTY works while
   preserving credential-helper cancellation and descendant cleanup. Test PTYs,
   interrupts, SIGTERM, terminal restoration and protocol-compatible proxy operation.
9. Keep all observed instance/request/command IDs on timeout, cancel and access
   failure. Print concrete ls, ssh-by-ID, up --resume and down-by-ID recovery steps
   as appropriate, preserving the effective config/profile/region. Document manual
   cleanup until TTL ships; no wait or connection failure automatically terminates
   workers. Explain that SSM does not log SSH tunnel contents.
10. Add meaningful controlled tests for state combinations, bounded waits and
    retained identities, probe-denial/malformed/failure/eventual-consistency cases,
    unsafe scope/name resolution, receipt replay compatibility, strict host trust,
    command/config injection, token redaction, plugin launch/cleanup failures,
    JSON rejection and interactive subprocess behavior. Run make check, targeted
    race tests and OpenTofu fmt/validate/mock tests. Document real SSH, scp/sftp and
    one remote-editor acceptance plus forced timeout and cleanup in
    docs/acceptance/09-readiness-shell.md.
11. Create a PR with offline validation and any unperformed live checks stated
    accurately. Have a fresh agent review the PR, fix actionable findings and rerun
    affected checks. Pause with concise instructions when the user needs to apply
    the reviewed foundation, export the manifest, select a public key, authenticate,
    or run live shell/editor checks. Do not claim live acceptance or close #9 until
    the user provides the required evidence. No live AWS mutation during planning.

## References

- [SSH over Session Manager](https://docs.aws.amazon.com/systems-manager/latest/userguide/session-manager-getting-started-enable-ssh-connections.html)
- [Instance Connect IAM and OS-user restrictions](https://docs.aws.amazon.com/AWSEC2/latest/UserGuide/ec2-instance-connect-configure-IAM-role.html)
- [Plugin response handoff implementation](https://github.com/aws/session-manager-plugin/blob/mainline/src/sessionmanagerplugin/session/session.go)
- [Session Manager SSH logging limits](https://docs.aws.amazon.com/en_en/systems-manager/latest/userguide/session-manager.html)

## Astra high review adjustments

The reviewer supports the dedicated-key recommendation and considers the plan
implementable after the following refinements and the confirmed authentication
decision. These requirements govern implementation; live acceptance remains open.

- **Plugin credentials and logging:** parse the version and require at least
  1.2.764.0, then verify against the actually supported installed release. The
  plugin has independent AWS credential loading and a seelog configuration;
  devbox's SDK logger does not control either. Check the effective plugin logging
  configuration and fail with installation/configuration instructions if disabled
  sensitive logging cannot be established. Explicitly propagate effective
  profile, region and endpoint; exercise the documented assume-role and
  credential-process bridge, expiry and helper failure. Give only the plugin
  child the session-response environment variable. Never dump child environments.
- **Plugin stdout:** accept and document its ordinary pre-identification session
  banner, as standard OpenSSH does. Test the supported plugin's startup failure
  and banner/SSH-identification sequencing. Do not claim a strictly byte-clean
  startup and do not line-filter encrypted SSH traffic.
- **TTY and setup deadline:** separate a bounded, noninteractive authenticated
  OpenSSH master from the interactive terminal client. Use a private control
  socket and a successful control check as the authentication handoff, not merely
  a successful StartSession or plugin spawn. Use BatchMode for this setup;
  passphrase-protected identities require ssh-add beforehand and remain under
  OpenSSH/agent control. Disable multiplex persistence; the CLI owns and cleans
  up the master/proxy on every exit. Transfer foreground terminal ownership to
  the interactive client process group and restore it in the supervisor. After
  handoff, ordinary Ctrl-C goes through SSH to the remote foreground command,
  not the setup cancellation context; SIGTERM shuts down the supervised session.
  The editor proxy is a separate ordinary stdio process, without terminal
  handoff. Prototype/PTY-test this design before integrating it into the public
  commands; preserve credential-helper descendant cleanup throughout.
- **Agent/IAM prerequisites:** validate SSM Agent >=3.3.40.0 for the selected
  Message Gateway command delivery, and verify execution with the restricted
  instance role during acceptance. Do not speculate that GetDocument or legacy
  ec2messages permissions are required, or widen the operator session-ARN grant
  based only on the previously recorded IAM simulator mismatch.
- **Host trust:** strictly parse an Ed25519 public key's encoded structure and
  bind successful probe output to the exact command ID, instance ID, document
  name/version and step. Use an immutable scoped HostKeyAlias, dedicated
  UserKnownHostsFile, disabled GlobalKnownHostsFile/UpdateHostKeys, strict checking
  and the pinned Ed25519 host algorithm. Lock trust-file updates across processes
  to prevent concurrent editor connections from losing entries. A changed key
  is an error requiring deliberate inspection and removal of that exact entry;
  never automatically overwrite it. Use one supported user key type (Ed25519),
  publish its fingerprint in non-secret access metadata, set IdentitiesOnly yes
  and disable agent forwarding by default.
- **Observation/recovery boundary:** obtain inventory and reconcile/persist an
  already-dispatched allocation before loading readiness metadata. Add a
  readiness-specific manifest loader/validator that does not depend on a valid
  launch profile or local identity file. One overall ls deadline and bounded
  probe concurrency preserve all discovered records even if some observations
  fail. Pending/offline/failed *observations* can be successfully listed (exit 0);
  unavailable/denied/malformed observations use exit 1, or exit 4 on the overall
  deadline, with ok=false, all records retained and per-instance sanitized reason
  codes. Unknown is never a fabricated failed bootstrap. Cover old dispatched
  receipts with missing/old manifests and ls with an invalid profile explicitly.
- **Failed bootstrap access:** initial ssh requires complete readiness and
  refuses a failed bootstrap with inspect/recovery/teardown IDs and instructions.
  No unready-shell bypass is included in this slice. Documentation must not
  instruct users to retry ssh as though a confirmed failed marker were pending.

Baseline before implementation: `make check` passed on September 11, 2026.

Additional references:
[plugin release requirements](https://docs.aws.amazon.com/systems-manager/latest/userguide/plugin-version-history.html),
[plugin logging implementation](https://github.com/aws/session-manager-plugin/blob/mainline/src/log/log_unix.go),
[SSM Agent message transport](https://docs.aws.amazon.com/systems-manager/latest/userguide/systems-manager-setting-up-messageAPIs.html).


## Implementation refinements and validation

The proxy uses the reviewed bounded startup-adapter alternative: it suppresses
preamble/errors until the SSH identification, then copies opaque binary bytes.
The OpenSSH master runs outside the foreground group; the interactive client
shares the supervised foreground worker group. PTY tests cover this foreground
handoff/restoration, Ctrl-C and resize. Plugin setup now kills the entire ordinary
child process group before waiting for EOF, preventing a helper from extending
the timeout by holding the pipe open. Shared launch receipt schema stays v1;
manifest v3 binds the public key through rendered bootstrap/template pins.

Review fixes additionally distinguish remote exit codes from setup errors, make
identity paths absolute, preserve cleanup diagnostics, and store configuration
variants separately so simultaneous callers cannot replace each other's selected
profile. Non-file diagnostic writers shared by master/client are serialized.
A local inetd-mode OpenSSH integration test now covers a real master/multiplexed
shell and remote exit 4 without opening a listener or changing existing keys.
