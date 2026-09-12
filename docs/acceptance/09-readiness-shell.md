# Issue #9 readiness and SSH acceptance

Implementation and controlled verification: September 12, 2026.
**Live foundation, operator doctor, worker readiness and SSH command checks are complete.**
Live terminal input/interrupt/resize/exit and SCP/SFTP checks pass, and the user
confirmed keyboard input, Ctrl-C and exit. Editor and cleanup acceptance remain pending.

## Controlled evidence

- `make check`: Go formatting, module verification, build, vet and tests.
- `go test -race ./internal/access ./internal/lifecycle ./internal/cli ./cmd/devbox`:
  concurrency, readiness, host-trust locking and supervised-process checks.
- `make infra-check TOFU=/tmp/devbox-tools/tofu`: pinned OpenTofu 1.12.6,
  AWS provider 6.64.0, formatting, validate, state-bootstrap mock test and four
  foundation mock tests, including the actual exported manifest/bootstrap digests.
- Readiness tests: EC2 running without SSM, offline SSM, pending/failed bootstrap,
  denied probes, malformed/incorrect invocation results, eventual visibility,
  one in-flight command, timeout with retained IDs, and scope revalidation.
- Access tests: real OpenSSH `-G` parsing, pinned host algorithms and strict trust,
  host-key mismatch, concurrent trust writes, path/proxy escaping, bounded startup
  output filtering and binary passthrough, plugin version/logging guards,
  child-only token handoff, rejected out-of-scope StartSession, startup timeout,
  and SSM cleanup on success/failure. A local inetd-mode sshd fixture verifies
  a real OpenSSH master/multiplexed shell and preserves remote exit status 4,
  without a listening port or changes to existing keys. The installed 1.2.835.0 plugin was exercised
  locally with isolated invalid test credentials and a loopback endpoint.
- A Linux PTY test verifies foreground terminal ownership, interrupt delivery,
  resize and terminal mode restoration. Controlled SSH subprocess tests verify
  that an established shell can outlive the setup timeout and retain its exit code.
  These are local checks, not evidence that a real AWS tunnel/editor works.

## Configure the dedicated key

Use the existing selected test account/config and restricted `devbox-operator`
profile from #7/#8. Keep credentials and private keys out of Git and pasted output.
If creating a new dedicated key, choose an unused path and a passphrase:

```sh
ssh-keygen -t ed25519 -f "$HOME/.ssh/devbox_ed25519" -C devbox
ssh-add "$HOME/.ssh/devbox_ed25519"
awk '{print $1 " " $2}' "$HOME/.ssh/devbox_ed25519.pub"
```

If ssh-agent is not available, start one in your terminal with
`eval "$(ssh-agent -s)"`, then run ssh-add. The public-key line is safe to put in
`ssh_public_key = "ssh-ed25519 BASE64"` in your existing foundation.tfvars.
Use only its first two fields, with no comment. Do not overwrite another key or
copy private material into OpenTofu. The private-key file must be owned by you
and mode 0600; keep its `.pub` file beside it.

Set the absolute local private-key path in your devbox TOML:

```toml
ssh_identity_file = "/home/YOUR_USER/.ssh/devbox_ed25519"
```

Use your real path; TOML does not expand `$HOME` or `~`. Existing deployments
require a reviewed foundation plan/apply and manifest v3 export, using the setup
profile and backend from [the setup guide](../setup.md#3-apply-the-foundation-and-export).
Expected changes: new launch-template/bootstrap content with your public key,
updated fixed readiness document, and manifest v3. No instance is launched by
this apply. Do not replace your existing backend/config/scope with example values.
Use a fresh worker for acceptance; key rotation does not update old workers.

After applying and exporting, from the repository root:

```sh
make build
./bin/devbox doctor --aws-profile devbox-operator --timeout 60s --json
./bin/devbox ls --aws-profile devbox-operator --json
```

Stop if doctor fails. Refresh credentials using the established login workflow.
Plugin >=1.2.764.0 and SSM Agent >=3.3.40.0 are required. Plugin logging must remain
disabled: access checks `/usr/local/sessionmanagerplugin/seelog.xml` and accepts
its absence (official default logging off) or `minlevel="off"` without exceptions.
If enabled, disable logging deliberately before retrying; do not paste its logs.

## Launch, observe, connect and transfer

These commands allocate a billable worker. Manual cleanup is required until TTL
ships, including after errors, terminal closure and successful SSH exit.

```sh
./bin/devbox up agent --on-demand --name smoke --aws-profile devbox-operator --timeout 5m --json > /tmp/devbox-up-09.json
cat /tmp/devbox-up-09.json
./bin/devbox ls --aws-profile devbox-operator
./bin/devbox ls --aws-profile devbox-operator --json
./bin/devbox ssh smoke --aws-profile devbox-operator
```

Expect progress on stderr and one final JSON object for up: `ec2_state=running`,
`ssm=online`, `bootstrap=complete`, `readiness=ready`. Keep the instance/request
and probe IDs. In the remote shell run:

```sh
uname -a
whoami
sleep 60
```

Expect `whoami` to print `devbox`. Interrupt sleep with Ctrl-C and verify the
shell remains usable. Resize your terminal, run `stty size`, then `exit`. Check
that your local terminal behaves normally. A bare `exit` preserves the last shell
status, including 130 after Ctrl-C; use `exit 0` for an explicitly successful exit.
The CLI preserves that status and reports `remote_exit` for statuses 1–254.
The worker remains allocated.

Export configuration for external OpenSSH clients:

```sh
./bin/devbox ssh-config smoke --aws-profile devbox-operator --json > /tmp/devbox-ssh-09.json
cat /tmp/devbox-ssh-09.json
```

Use the returned `ssh_config_path` and `ssh_host` as CONFIG_PATH and SSH_HOST
below (replace the placeholders, do not copy them literally):

```sh
ssh -F CONFIG_PATH SSH_HOST
printf 'devbox transfer check\n' > /tmp/devbox-transfer-09.txt
scp -F CONFIG_PATH /tmp/devbox-transfer-09.txt SSH_HOST:/tmp/devbox-transfer-09.txt
scp -F CONFIG_PATH SSH_HOST:/tmp/devbox-transfer-09.txt /tmp/devbox-transfer-09-returned.txt
cmp /tmp/devbox-transfer-09.txt /tmp/devbox-transfer-09-returned.txt
sftp -F CONFIG_PATH SSH_HOST
```

For a remote editor, select this generated file as its SSH config (for VS Code
Remote-SSH: `remote.SSH.configFile`), connect to SSH_HOST and open `/home/devbox`.
Create/save/reopen a small file and use the editor's terminal to verify `whoami`.
Close the editor connection. Record the editor/version used. Do not claim editor
acceptance from a shell or scp test alone. An editor may install its own remote
server, independently of devbox bootstrap.

Restart the local terminal environment and rediscover with ls, without request
receipts. Reload the key with ssh-add if required; regenerate ssh-config after
moving the binary or local config.

## Failures, inspection and cleanup

Force a bounded readiness/setup timeout on the existing worker, without allocating
another request:

```sh
./bin/devbox ssh smoke --aws-profile devbox-operator --timeout 1ms
./bin/devbox ls --aws-profile devbox-operator --json
```

For an allocated up failure, **do not issue a fresh up as a retry**. Use its exact
saved request ID with `up --resume REQUEST_ID`; reconciliation cannot allocate a
replacement. To exercise up's readiness timeout, use that already observed
request with a short timeout; depending on timing, the earlier AWS setup or
reconciliation step may consume the deadline before readiness begins. Controlled
SDK tests specifically cover a readiness-loop timeout after allocation.

All recovery commands must use the original config/profile/region. For a fixed
probe command, inspect its execution status (not arbitrary command output) using
its saved IDs:

```sh
aws ssm get-command-invocation --profile devbox-operator --region us-east-2 \
  --command-id PROBE_COMMAND_ID --instance-id INSTANCE_ID \
  --query '{Status:Status,ResponseCode:ResponseCode,Document:DocumentName,Version:DocumentVersion}'
```

Probe permission/transport/invocation failure means bootstrap unknown; the failed
marker specifically means bootstrap failed. A confirmed failed marker blocks ssh.
Investigate through the fixed probe status/setup administrator workflow, fix the
foundation and replace the disposable worker. The CLI does not provide a general
exec or logs command or an unready-shell bypass.

Exercise missing-plugin diagnostics by temporarily removing only the plugin's
PATH directory in a subshell (keep the devbox binary/OpenSSH accessible). Exercise
out-of-scope refusal only with a known existing instance ID from a different owner
or deployment in the selected test account; do not create extra resources solely
for this check. Failed-bootstrap and probe-permission-denial live tests require a
separately reviewed, disposable test setup change; controlled tests cover both
without modifying the working production/test IAM policy or bootstrap implicitly.

If the host key changes, stop. Review the target account/tags, command identity and
host key from the authenticated pinned probe. Only after establishing an intended
host-key replacement, remove that instance's dedicated `known_hosts` file under
`$XDG_STATE_HOME/devbox/ssh` (default `~/.local/state/devbox/ssh`) and regenerate
ssh-config. Never set StrictHostKeyChecking=no.

Clean up using the exact recorded instance ID:

```sh
./bin/devbox down INSTANCE_ID --aws-profile devbox-operator --timeout 5m --json
./bin/devbox down INSTANCE_ID --aws-profile devbox-operator --timeout 5m --json
./bin/devbox ls --aws-profile devbox-operator --json
```

Expect observed termination and root-volume deletion; retain the volume ID for
independent verification if deletion is unavailable. Follow [#8 recovery](08-lifecycle.md)
for describe-volumes/manual teardown. Removing a worker preserves durable
foundation resources and local public host trust/config files. A reported session
cleanup failure includes an exact SSM session ID for `aws ssm terminate-session`;
that action does not terminate the EC2 instance.

## Live results and independent PR review

A fresh agent reviewed draft PR #14 after creation. The review found a relative
IdentityFile path that could fail from an editor's different working directory;
it is now absolute, with a different-directory OpenSSH regression test. Review
also confirmed the remote-exit-4/setup-timeout collision and hidden cleanup
diagnostics found during local review. Fixes separate setup errors from SSH exit
status, preserve sanitized proxy diagnostics, allow bounded SSM cleanup, and
keep concurrent configuration variants in separate immutable files. The reviewer
reported no blocking actionable findings in the reviewed working tree. A later
race check found shared diagnostic-buffer writes, now serialized for non-file
writers and covered by the race suite.

Remaining live evidence is listed below. Record actual test date, selected scope locally (no secrets), readiness
signals in text/JSON, uname/user, Ctrl-C/resize/exit, transfer comparison, editor
save/reconnect, failure diagnostics, termination/root deletion and repeated down.
Do not close #9 or mark parent MVP acceptance complete before the required live
evidence is recorded.


## Live foundation update (September 12, 2026)

- Confirmed the refreshed setup identity matches the previously selected account.
- Verified the dedicated Ed25519 public key matches the local identity and the
  key loaded in ssh-agent. Configured the local identity path and reused the
  existing deployment variables and S3 backend; all local inputs remain ignored.
- Reviewed the saved live plan: exactly two in-place changes (launch template
  bootstrap/public key and the fixed SSM readiness document), zero additions or
  deletions. No IAM, networking, AMI, or other resource changes were planned.
- Applied that saved plan successfully. Exported manifest v3 with numeric
  launch-template version 2 and readiness document version 2.
- Rebuilt the CLI. All 12 doctor checks passed using the restricted operator
  profile, including actual resource scope, unchanged IAM/network settings and
  updated template/document hashes.
- Scoped inventory was empty before worker acceptance. No worker was allocated
  during this foundation update. Local plan summary and doctor/inventory evidence
  are retained in the ignored foundation acceptance-output directory.

Live shell, transfer/editor, failure/recovery and teardown checks are still the
remaining acceptance gate; the issue and PR remain open/draft.


## Live readiness and SSH IAM correction (September 12, 2026)

- The user's On-Demand `smoke` launch progressed from EC2 pending/SSM unregistered
  to EC2 running/SSM online/bootstrap complete and returned `ready`.
- Initial SSH failed before authentication. A direct SDK diagnostic exposed only
  the allowlisted `AccessDeniedException` code and denied instance ARN. The
  operator's `Bool` document-access condition rejected this explicit-document
  request. Changed it to AWS's documented `BoolIfExists` pattern while retaining
  account, region, ownership tags and the exact SSH document grant.
- An independent agent reviewed the correction with no blocking findings. Mock
  policy tests pin those restrictions. Reviewed and applied a saved plan whose
  only resource change was that condition in the operator inline policy; exported
  the updated manifest and passed operator doctor again.
- Under the restricted operator, `AWS-StartSSHSession` port 22 succeeded and its
  session was immediately terminated. Requests with omitted DocumentName,
  explicit `SSM-SessionManagerRunShell`, and `AWS-StartPortForwardingSession` were
  all denied on their document ARNs. No native shell/forwarding sessions opened.
- Real OpenSSH using the generated strict config and installed plugin returned
  exit 0, `Linux 7.0.0-1012-aws x86_64`, and user `devbox` on the existing worker.
  This also verifies the scoped signed data-channel grant. Normal proxy shutdown
  initially emitted a misleading transport error; expected cancellation after SSH
  identification now closes quietly, with a regression test and unchanged bounded
  SSM cleanup. A second live SSH run confirmed exit 0 without the false error. OpenSSH remains responsible for authentication/remote exit status.
- API errors now distinguish an allowlisted `session_denied` diagnostic from
  connectivity errors without printing raw provider messages. Controlled tests
  cover denial/redaction/no plugin launch, and the access race suite passes.

Local sanitized results are in the ignored foundation acceptance-output directory.
The existing worker remains allocated for the human terminal/editor checks; do
not launch another worker. Interactive Ctrl-C/resize/exit, transfer/editor and
termination/root-deletion evidence remain required before closing #9.


## Interactive keyboard correction (September 12, 2026)

The user reached the remote prompt but could not type. Local process inspection
showed the SSH master stopped in a background process group, while its multiplexed
client owned the foreground group. OpenSSH passes the client's terminal file
descriptors to its master; Linux stopped that master when it tried to read input.
The earlier real-SSH test used a pipe, and the terminal test did not include the
SSH master/client pair, so neither exercised this failure.

After authentication, the multiplexed client now joins the master's group and
makes that group the terminal foreground owner. The worker restores the original
foreground group and termios after master/proxy cleanup, preserving SIGTTOU's
previous ignored disposition. Piped input keeps the existing path.

- A new real OpenSSH/controlling-PTY regression reproduced the missing keyboard
  input before the fix. It now checks that both SSH processes share the foreground
  group, keyboard commands run remotely, Ctrl-C interrupts a command, resize
  reaches the remote terminal, and exit 4 is preserved. Both normal exit and
  SIGTERM cancellation restore terminal ownership and modes.
- The actual rebuilt `devbox ssh smoke --aws-profile devbox-operator` was exercised
  through a controlling PTY against the existing worker. `whoami` returned
  `devbox`, a typed command printed `KEYBOARD_OK`, Ctrl-C interrupted `sleep 30`,
  `stty size` returned `37 101` after a local resize, and `exit` returned code 0.
- `make check` and `go test -race ./internal/access ./cmd/devbox` passed.

User terminal confirmation, transfer/editor and teardown remain pending. The
worker is retained; no additional machine was launched for this correction.


## User terminal confirmation and transfers (September 12, 2026)

- User terminal evidence confirmed `whoami` returned `devbox`, keyboard input
  worked, Ctrl-C interrupted `sleep 30` and cleared an unfinished command, and
  `exit` returned to the local shell.
- Exiting immediately after Ctrl-C preserved status 130. The generic `ssh_failed`
  diagnostic was misleading; statuses 1–254 now report `remote_exit` with the
  numeric status and explain `exit 0`. Status 255 retains the ambiguous SSH
  transport/authentication failure diagnostic. A live repeat of sleep/Ctrl-C/exit
  verified exit 130 and the new message. `make check` passed, and independent
  review found no actionable issue with the diagnostic change.
- SCP uploaded and downloaded a 1 MiB binary fixture using the generated strict
  SSH config; byte comparison passed. An explicit SFTP batch uploaded/downloaded
  the same fixture and passed byte comparison. Temporary local and remote files
  were removed. Sanitized results are in the ignored acceptance-output directory.

Remote editor save/reconnect and final termination/root-volume deletion remain
pending. The existing worker remains allocated for editor acceptance.
