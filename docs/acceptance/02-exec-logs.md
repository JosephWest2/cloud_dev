# MVP 2: execute commands and recover complete logs

This is the reproducible acceptance runbook for [#2](https://github.com/JosephWest2/cloud_dev/issues/2)
and its final slice [#21](https://github.com/JosephWest2/cloud_dev/issues/21).
On September 14, 2026 UTC, all six live command paths passed, and all twelve
streams remained byte-identical after exact worker/root cleanup. The evidence
below distinguishes this final fixture campaign from earlier live slices and
controlled failure tests. Final independent merge review is recorded separately.

## Prerequisites and fixed scope

Use Linux, Bash, Git, Python 3.9+, Go as required by `go.mod`, AWS CLI v2,
OpenSSH, Session Manager Plugin and pinned OpenTofu 1.12.6. The provider lock pins
AWS 6.64.0. Follow [setup](../setup.md) and the
[dedicated SSH key procedure](09-readiness-shell.md#configure-the-dedicated-key).
The fixture needs only Git and Python's standard library on the Ubuntu worker;
install either only if missing. No pip packages or custom AMI are needed.

This campaign uses account `464557813916`, region `us-east-2`, deployment
`personal-dev`, owner `joseph`, explicit setup profile `devbox-setup` and restricted
operator profile `devbox-operator`. Use setup only to export the accepted
foundation; operator performs lifecycle, SSH, exec and retrieval. Preserve the
existing AWS authentication configuration, including its credential-process
bridge. A fresh devbox state directory must not remove or copy AWS credentials.

The frozen source is `17e2f1804db4b627efd3094a94763b006fa17c57`, at
`/tmp/devbox-issue21-live-17e2f18`. Its CLI SHA-256 is
`841452e211347306f1c03cdf68be48384f1af9dcdc1e3b792b4b0652a2fa3457`.
The deployment remains launch template version **4**, AMI
`ami-00adec9774170bad2`, runner SHA-256
`e21c0536522661235b6ea5cb22bfe3b9bd66081af640570f7a151f51db8a74f6`.
#21 requires no infrastructure apply. Building a newer local runner during
offline checks does not authorize replacing that accepted artifact.

[#17's reviewed actual plan/apply](17-result-foundation.md) created nine
resources and updated three, with a subsequent no-change plan. The
[#18 correction](18-exec-dispatch.md) fixed a demonstrated S3 submission-clock
boundary before the worker's start claim. Its reviewed actual apply replaced
one runner object, updated three resources and removed the prior object;
the follow-up plan again had no changes. That correction produced template 4
and the runner pin above. #19, #20 and this acceptance slice use that deployment.

Run these offline checks in the clean source checkout, without copying live
backend initialization, credentials, tfvars, state or plans into it:

```sh
make check
make build
make infra-check TOFU=/tmp/devbox-tools/tofu
sha256sum bin/devbox
go version
/tmp/devbox-tools/tofu version
aws --version
ssh -V
session-manager-plugin --version
python3 --version
git --version
./bin/devbox version
```

`make infra-check` uses backend-disabled initialization, mocked infrastructure
tests and the Go export/hash bridge. Never run it in the initialized live
foundation checkout. Actual local and remote versions and final check results
belong in the evidence section, not assumptions based on installed prerequisites.

## Launch once and clone through its shell

The examples below use Bash. Set `config` to the trusted TOML whose account,
region, deployment, owner and dedicated key match the exported manifest. Its
`manifest` path must point to the export beside it. Retain this original scope
for future recovery. For the actual campaign these are:

```sh
umask 077
cli=/tmp/devbox-issue21-live-17e2f18/bin/devbox
config=/tmp/devbox-issue21-live-checks/config/config.toml
manifest=/tmp/devbox-issue21-live-checks/config/deployment.json
work=$(mktemp -d /tmp/devbox21-manual.XXXXXXXX)
export XDG_STATE_HOME="$work/state"
mkdir "$XDG_STATE_HOME"
db=("$cli" --config "$config" --aws-profile devbox-operator --region us-east-2)
AWS_PROFILE=devbox-setup /tmp/devbox-tools/tofu \
  -chdir=/tmp/devbox-issue18-live-21ad872/infra/foundation \
  output -json deployment_manifest > "$manifest"
"${db[@]}" doctor --timeout 90s --json > "$work/doctor.json"
"${db[@]}" ls --json > "$work/initial-ls.json"
```

Require doctor exit 0 with all 14 checks passing and no active scoped worker.
An unexpected worker or different scope is a stop condition. Launch one
disposable On-Demand `agent` only after those checks:

```sh
"${db[@]}" up agent --on-demand --name issue21-live --timeout 5m --json \
  > "$work/up.json" 2> "$work/up.stderr"
instance_id=$(python3 - "$work/up.json" <<'PY'
import json, sys
v = json.load(open(sys.argv[1]))
assert v['ok'] and len(v['instances']) == 1
i = v['instances'][0]
assert i['ec2_state'] == 'running'
assert (i['ssm'], i['bootstrap'], i['readiness']) == ('online', 'complete', 'ready')
print(i['instance_id'])
PY
)
aws --profile devbox-operator --region us-east-2 --no-cli-pager \
  ec2 describe-instances --instance-ids "$instance_id" \
  > "$work/instance.json"
root_volume_id=$(python3 - "$work/instance.json" <<'PY'
import json, sys
r = json.load(open(sys.argv[1]))['Reservations']
assert len(r) == 1 and r[0]['OwnerId'] == '464557813916'
assert len(r[0]['Instances']) == 1
i = r[0]['Instances'][0]
roots = [b['Ebs'] for b in i['BlockDeviceMappings'] if b['DeviceName'] == i['RootDeviceName']]
assert len(roots) == 1 and roots[0]['DeleteOnTermination']
print(roots[0]['VolumeId'])
PY
)
aws --profile devbox-operator --region us-east-2 --no-cli-pager \
  ec2 describe-volumes --volume-ids "$root_volume_id" > "$work/root.json"
```

Before SSH, independently match `ManagedBy=devbox`, `Deployment=personal-dev`,
`Owner=joseph` and `RequestId` to the launch result; match the AMI, numeric
template version, instance profile, VPC, subnet and security group to the
manifest. Require IMDSv2, an encrypted 100 GiB gp3 root with deletion on
termination and zero ingress on every attached security group. Retain the
launch request, exact instance and original root IDs before proceeding. The
reviewed campaign helper automates these comparisons and saves private responses.

Clone [pypa/sampleproject](https://github.com/pypa/sampleproject) at commit
`621e4974ca25ce531773def586ba3ed8e736b3fc` through actual `devbox ssh`.
Save the following as `$work/fixture-shell.input`:

```sh
stty -echo
timeout --signal=TERM --kill-after=15s 12m /bin/bash -se <<'DEVBOX21_FIXTURE_SETUP'
set -euo pipefail
test "$(id -un)" = devbox
test "$(id -u)" -ne 0
fixture_dir='/home/devbox/issue21 sampleproject'
fixture_revision=621e4974ca25ce531773def586ba3ed8e736b3fc
test ! -e "$fixture_dir"
missing_tools=()
command -v git >/dev/null || missing_tools+=(git)
command -v python3 >/dev/null || missing_tools+=(python3)
if ((${#missing_tools[@]})); then
  sudo -n env DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=1 -o Acquire::http::Timeout=30 -o Acquire::https::Timeout=30 update </dev/null
  sudo -n env DEBIAN_FRONTEND=noninteractive apt-get -o Acquire::Retries=1 -o DPkg::Lock::Timeout=60 install -y --no-install-recommends "${missing_tools[@]}" </dev/null
fi
cat /etc/os-release
git --version
python3 --version
python3 -S -c 'import sys; assert sys.version_info >= (3, 9)'
timeout --signal=TERM --kill-after=10s 2m git clone --no-checkout https://github.com/pypa/sampleproject.git "$fixture_dir" </dev/null
git -C "$fixture_dir" checkout --detach "$fixture_revision"
test "$(git -C "$fixture_dir" rev-parse HEAD)" = "$fixture_revision"
test -z "$(git -C "$fixture_dir" status --porcelain)"
test -f "$fixture_dir/tests/test_simple.py"
printf 'DEVBOX21_FIXTURE_READY revision=%s\n' "$fixture_revision"
DEVBOX21_FIXTURE_SETUP
fixture_rc=$?
printf '\nDEVBOX21_FIXTURE_EXIT=%s\n' "$fixture_rc"
exit "$fixture_rc"
```

```sh
"${db[@]}" ssh "$instance_id" --timeout 3m \
  < "$work/fixture-shell.input" > "$work/fixture-shell.stdout" \
  2> "$work/fixture-shell.stderr"
```

Require real exit 0, the exact selected instance, pinned READY marker and
`DEVBOX21_FIXTURE_EXIT=0`. Keep the terminal transcript private. The SSH timeout
bounds connection setup; the script separately bounds the established shell.
The automated campaign also imposes a 16-minute local bound and cleans up its
own session descendants on failure. Reconcile an incomplete checkout/package
operation through retained evidence and a bounded read-only shell check before
any continuation. Do not rerun launch or clone blindly after an uncertain result.

## Six command submissions

Every argument after `--` is literal. Default cwd is `/home/devbox`; the selected
relative cwd below becomes `/home/devbox/issue21 sampleproject`. Execution runs
as nonroot `devbox`, with stdin EOF, umask 022 and exactly the fixed environment
in [the contract](../contracts.md#invocation-and-literal-arguments). Local shell
variables and credentials are not forwarded. Shell evaluation, when desired,
must be explicitly requested with `-- sh -c '...'`.

Do not enable automatic retries around these commands. Each is one submission;
save prepared/acknowledged IDs and recover by the public `dc1-...` ID if the local
result is uncertain. Intentional exits 1, 4 and 255 below are expected. Use the
structured outcome to distinguish workload failure from local failure.

```sh
exec_options=(--timeout 3m --delivery-timeout 60s --wait-timeout 4m)
record_exec() {
  local label=$1 expected=$2 rc=0
  shift 2
  "${db[@]}" exec "$instance_id" "${exec_options[@]}" "$@" \
    > "$work/$label.result" 2> "$work/$label.diagnostics" || rc=$?
  printf '%s\n' "$rc" > "$work/$label.exit"
  test "$rc" -eq "$expected"
}
```

Stop and inspect the preserved result if any `record_exec` returns nonzero.
`exec` emits metadata, never the workload's stdout/stderr. Retrieve those bytes
using the logs procedure below. Acknowledgement appears promptly on stderr as
`devbox: submitted command_id=dc1-... ssm_command_id=...`.

**L1: successful fixture check, text output.**

```sh
record_exec L1 0 --cwd 'issue21 sampleproject' --exec-timeout 60s -- \
  /usr/bin/env PYTHONPATH=src /usr/bin/python3 -S \
  -m unittest discover -s tests -v
```

Expect text `remote_exit`, `workload_status=exited`, `remote_exit_code=0` and
`publication=complete`, with real local exit 0. Retrieved stdout is empty;
stderr contains `test_add_one`, `Ran 1 test` and final `OK`. The duration varies,
so record the actual complete stream hash rather than assuming a fixed digest.

**L2: deliberate failure, JSON and absolute cwd.**

```sh
record_exec L2 1 --cwd '/home/devbox/issue21 sampleproject' \
  --exec-timeout 60s --json -- /usr/bin/env PYTHONPATH=src \
  /usr/bin/python3 -S -c \
  'import unittest; from sample.simple import add_one; unittest.TestCase().assertEqual(add_one(5), 7)'
```

Expect exactly one newline-terminated JSON envelope with `command="exec"`,
`ok=false`, `exit_code=1`, `outcome="remote_exit"`,
`workload={"status":"exited","exit_code":1,"signal":null}` and
`publication="complete"`. Retrieved stderr contains `AssertionError: 6 != 7`;
stdout is empty. The fixture checkout is unchanged. Successful logs retrieval
returns **0**, while its separate `workload.exit_code` remains **1**.

**L3: literal arguments, cwd and process context.**

```sh
literal_code=$(cat <<'PY'
import json,os,pwd,sys
mask=os.umask(0o022);os.umask(mask)
print(json.dumps(dict(argv=sys.argv[1:],cwd=os.getcwd(),user=pwd.getpwuid(os.getuid()).pw_name,uid=os.getuid(),euid=os.geteuid(),environment=dict(os.environ),stdin_eof=sys.stdin.buffer.read()==b"",umask=mask),ensure_ascii=False,sort_keys=True))
PY
)
literal_code+=$'\n'
record_exec L3 0 --cwd 'issue21 sampleproject' --exec-timeout 60s --json -- \
  /usr/bin/python3 -S -c "$literal_code" \
  '' 'two words' "single'quote" '"double"' '$(id)' '; echo WRONG' '*' \
  $'a\nb' 'snowman ☃' --json --help --timeout --
```

Decode the retrieved stdout JSON and compare all 13 arguments exactly, including
the empty argument and embedded newline. Require cwd
`/home/devbox/issue21 sampleproject`, user `devbox`, equal positive real/effective
UIDs, `stdin_eof=true`, `umask=18` (decimal 022), and exactly:

```json
{"HOME":"/home/devbox","USER":"devbox","LOGNAME":"devbox","LANG":"C.UTF-8","PATH":"/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"}
```

No metacharacter is evaluated and the remote option-looking arguments cannot
change local output mode. Require workload/local exit 0 and empty stderr.

**L4: full binary output and exit 255.**

```sh
large_code=$(cat <<'PY'
import sys,time
time.sleep(float(sys.argv[1]))
marker=b"DEVBOX21_PAYLOAD_ONLY_9dd2eb53\x00\xff\xfe\n"
sys.stdout.buffer.write((marker+bytes(range(256))*4096)[:1048576])
sys.stderr.buffer.write((marker+bytes(reversed(range(256)))*3072)[:786432])
sys.exit(int(sys.argv[2]))
PY
)
large_code+=$'\n'
record_exec L4 255 --cwd 'issue21 sampleproject' --exec-timeout 60s --json -- \
  /usr/bin/python3 -c "$large_code" 0 255
python3 - "$work" <<'PY'
import pathlib, sys
p = pathlib.Path(sys.argv[1])
marker = b'DEVBOX21_PAYLOAD_ONLY_9dd2eb53\x00\xff\xfe\n'
(p/'expected.stdout').write_bytes((marker + bytes(range(256))*4096)[:1048576])
(p/'expected.stderr').write_bytes((marker + bytes(reversed(range(256)))*3072)[:786432])
PY
sha256sum "$work/expected.stdout" "$work/expected.stderr"
```

Expect `remote_exit` and exact workload/local exit 255, complete publication,
**1,048,576 stdout bytes** and **786,432 stderr bytes**, including NUL and
non-UTF-8. Both streams exceed inline SSM limits. Compare entire retrieved files
and selected raw streams to these independently constructed expected bytes;
length or ETag alone is insufficient. Every complete logs retrieval exits 0
while its workload metadata still says 255.

| Independently generated stream | Bytes | Expected SHA-256 |
| --- | ---: | --- |
| stdout | 1,048,576 | `e4b90b6607d9958b6f4c811acdd39ef3eb79cc8edd7fb24eacfcf36d455e16a2` |
| stderr | 786,432 | `aa5ab7a6138cc2697d302082ac548b566589108f3f59daf81c01f1b26a5b036f` |

**L5: real terminal Ctrl-C, restart and recover.** Run this from a terminal with
stdin still attached. Before starting this command, define `fresh_logs` from the
next section so it is ready to run immediately after detachment.
After the submitted-ID diagnostic, wait at least five
seconds, then press **Ctrl-C once** during the 30-second sleep:

```sh
detach_code=$(cat <<'PY'
import json,sys,time
print(json.dumps(dict(event="begin",at=time.time())),flush=True)
time.sleep(30)
print(json.dumps(dict(event="finish",at=time.time())),flush=True)
sys.stderr.buffer.write(b"DEVBOX21_DETACHED_FINISHED\n")
PY
)
detach_code+=$'\n'
detach_rc=0
"${db[@]}" exec "$instance_id" "${exec_options[@]}" \
  --cwd 'issue21 sampleproject' --exec-timeout 60s --json -- \
  /usr/bin/python3 -S -c "$detach_code" \
  > "$work/L5.result" 2> >(tee "$work/L5.diagnostics" >&2) || detach_rc=$?
printf '%s\n' "$detach_rc" > "$work/L5.exit"
```

Require local `interrupted`/4, one JSON envelope with retained IDs and no invented
ordinary workload exit. Immediately start new `logs` processes using the fresh
state function below: require at least one `pending` snapshot (retrieval exit 1)
while the workload is running, then a complete result (retrieval exit 0).
Logs is a snapshot, so repeat by ID with a finite deadline; it does not tail.

The retrieved begin/finish timestamps must enclose Ctrl-C, and remote finish
must follow local CLI exit. Runner metadata timestamps have whole-second
precision; compare fractional workload times with the represented one-second
interval. The remote job must exit 0 with two JSON stdout
lines and exact stderr `DEVBOX21_DETACHED_FINISHED\n`. The reviewed campaign
records signal/exit timestamps, establishes a started record, verifies the
supervised worker owns the controlling PTY foreground group, then writes byte
`0x03`; this is an actual terminal interrupt. It requires all dedicated local
session groups to stop without emergency cleanup. Ctrl-C sends **no remote
cancellation request**; `--exec-timeout` continues to bound the remote job.

**L6: actual two-second remote execution timeout.**

```sh
timeout_code=$(cat <<'PY'
import sys,time
sys.stdout.buffer.write(b"DEVBOX21_EXECUTION_TIMEOUT_STARTED\n");sys.stdout.flush()
time.sleep(30)
print("DEVBOX21_TIMEOUT_MUST_NOT_FINISH")
PY
)
timeout_code+=$'\n'
record_exec L6 4 --cwd 'issue21 sampleproject' --exec-timeout 2s --json -- \
  /usr/bin/python3 -S -c "$timeout_code"
```

Require `outcome="execution_timeout"`, local exit 4 and durable
`workload.status="execution_timeout"`, with no ordinary workload exit. Retain
its stopping signal if observed. Complete logs retrieval still exits 0. Its
stdout is exactly `DEVBOX21_EXECUTION_TIMEOUT_STARTED\n`, stderr is a real
zero-byte stream, and the completion marker is absent. Wrapper response codes
and local wait deadlines do not establish a workload timeout.

## Fresh-process logs, byte verification and recovery

Use each original **public command ID**, never an SSM ID. For example, extract
L4's ID from its saved metadata; L1's text output also includes `command_id=`:

```sh
command_id=$(python3 -c 'import json,sys; print(json.load(open(sys.argv[1]))["command_id"])' "$work/L4.result")
```

Each call below creates a new empty devbox state directory and a trusted
storage-only manifest with exactly six top-level fields. It copies no command
receipt and references absent SSH/workload-profile files. It keeps the original
bucket, expected owner, region, scope prefix and retention policy. A complete
final record requires no current launch runtime, EC2, SSH or SSM history.

```sh
fresh_logs() (
  invocation=$(mktemp -d "$work/logs.XXXXXXXX")
  mkdir "$invocation/state" "$invocation/path"
  ln -s "$(command -v aws)" "$invocation/path/aws"
  ln -s "$(command -v sh)" "$invocation/path/sh"
  python3 - "$manifest" "$invocation" <<'PY'
import json, pathlib, sys
m = json.load(open(sys.argv[1])); p = pathlib.Path(sys.argv[2])
fields = ('schema_version', 'account', 'region', 'deployment', 'owner', 'results')
(p/'deployment.json').write_text(json.dumps({k:m[k] for k in fields})+'\n')
(p/'config.toml').write_text('''schema_version = 1
expected_account = "464557813916"
region = "us-east-2"
deployment = "personal-dev"
owner = "joseph"
aws_profile = "devbox-operator"
manifest = "deployment.json"
profile_file = "missing-profile"
ssh_identity_file = "missing-key"
''')
PY
  rc=0
  XDG_STATE_HOME="$invocation/state" PATH="$invocation/path" \
    "$cli" --config "$invocation/config.toml" --aws-profile devbox-operator \
    --region us-east-2 logs "$@" --timeout 20s || rc=$?
  test -z "$(find "$invocation/state" -mindepth 1 -print -quit)" || exit 1
  exit "$rc"
)
fresh_logs "$command_id" > "$work/L4.status.txt"
fresh_logs "$command_id" --json > "$work/L4.status.json"
fresh_logs "$command_id" --stdout-file "$work/L4.stdout" \
  --stderr-file "$work/L4.stderr" --json > "$work/L4.export.json"
fresh_logs "$command_id" --stream stdout > "$work/L4.stdout.raw" \
  2> "$work/L4.stdout.metadata"
fresh_logs "$command_id" --stream stderr > "$work/L4.stderr.raw" \
  2> "$work/L4.stderr.metadata"
cmp "$work/expected.stdout" "$work/L4.stdout"
cmp "$work/expected.stderr" "$work/L4.stderr"
cmp "$work/L4.stdout" "$work/L4.stdout.raw"
cmp "$work/L4.stderr" "$work/L4.stderr.raw"
wc -c "$work/L4.stdout" "$work/L4.stderr"
sha256sum "$work/L4.stdout" "$work/L4.stderr"
stat -c '%a %n' "$work/L4.stdout" "$work/L4.stderr"
```

Both `sh` and `aws` are needed by the configured credential-process bridge;
SSH, Session Manager Plugin, OpenTofu and local receipts are absent from this
retrieval PATH. Every complete call must return 0. Status-only output says
`outcome="complete"`, `publication="complete"`, `encoding="bytes"` and
`verification="not_downloaded"`. Exports say `verification="verified"`, include
the verified selected file paths and byte counts, create mode 0600 files and
leave no temporary files. Metadata contains original IDs and separate workload
exit 255. `--stream` places only the selected bytes on stdout and all metadata
on stderr. It cannot combine with `--json` or file export.

Repeat status, both-file export and each raw selection for **all six IDs**, using
new destinations each time. For L5, first take fresh JSON snapshots immediately
after detachment until complete, at most 270 seconds; preserve every pending
snapshot and its actual timestamps. Establish all six complete results before
down. For nondeterministic unittest/timestamp output, compare exports and raw
copies to the original independently read S3 objects, not a rerun of the command.

The reviewed independent reader uses only STS identity and scoped S3 GET/LIST
with expected bucket owner. It checks all five metadata records, scope/command/
instance/document/runner/payload bindings, AES256 encryption, exact request
server submission time, shared 30-day deadline, timeline, workload/capture/
publication agreement and every byte/length/SHA-256 of both streams. If optional
GET reports missing/denied but the exact authenticated listing sees a newly
published key, it makes one required reread of that key; a second failure stops
that attempt. Object presence alone never proves a complete result.

On pending, transient failure or refreshed authentication, retry **logs with the
same ID**, using new export filenames. An uncertain SendCommand response is
not permission to submit again. Preserve the first attempt marker, request,
early IDs and diagnostics; a marker without a usable recovery ID is uncertain,
not unattempted. The campaign atomically claims each attempt and refuses reused
output directories. A later submission is allowed only for a separately proved
unattempted case; it never uses recovery as an execution retry.

## Exact worker cleanup and retained retrieval

After all six final records and complete bytes are established, terminate only
the captured instance. Never select a replacement by friendly name:

```sh
"${db[@]}" down "$instance_id" --timeout 5m --json > "$work/down.json"
aws --profile devbox-operator --region us-east-2 --no-cli-pager \
  ec2 describe-instances --instance-ids "$instance_id" \
  --query 'Reservations[].Instances[].{ID:InstanceId,State:State.Name}' \
  > "$work/terminated.json"
aws --profile devbox-operator --region us-east-2 --no-cli-pager \
  ec2 describe-volumes --volume-ids "$root_volume_id" \
  > "$work/root-after.json" 2> "$work/root-after.stderr"
aws --profile devbox-operator --region us-east-2 --no-cli-pager \
  ec2 describe-instances --filters Name=tag:ManagedBy,Values=devbox \
  Name=tag:Deployment,Values=personal-dev Name=tag:Owner,Values=joseph \
  Name=instance-state-name,Values=pending,running,shutting-down,stopping,stopped \
  --query 'Reservations[].Instances[].InstanceId' > "$work/remaining-instances.json"
aws --profile devbox-operator --region us-east-2 --no-cli-pager \
  ec2 describe-volumes --filters Name=tag:ManagedBy,Values=devbox \
  Name=tag:Deployment,Values=personal-dev Name=tag:Owner,Values=joseph \
  --query 'Volumes[].VolumeId' > "$work/remaining-volumes.json"
"${db[@]}" ls --json > "$work/final-ls.json"
fresh_logs "$command_id" --stdout-file "$work/L4.after.stdout" \
  --stderr-file "$work/L4.after.stderr" --json > "$work/L4.after.json"
cmp "$work/L4.stdout" "$work/L4.after.stdout"
cmp "$work/L4.stderr" "$work/L4.after.stderr"
sha256sum "$work/L4.after.stdout" "$work/L4.after.stderr"
```

Require successful down, the exact ID in `terminated`, the original root's
specific `InvalidVolume.NotFound` response, empty scoped nonterminated-instance
and volume arrays, and successful `devbox ls` with no nonterminated instances.
EC2 may continue to list terminated instances briefly. An arbitrary volume API failure is not deletion
evidence. If deletion is still propagating, bounded read-only checks of that
same ID may continue; incomplete cleanup leaves acceptance open.

Run text and JSON status, both-file export and each raw selection again for every
original ID with fresh state and new files. Require the same workload, complete
records, original submission/expiry,
full bytes and SHA-256 after teardown, with retrieval exit 0. The independent
post-down reader performs only STS/S3 reads. Controlled tests establish the
unavailable-SSM-history property; do not describe actual live history as expired
or disabled when it was not.

Results are retained **30 days from submission**, configurable from 2–365 days.
Every record shares the request's authoritative server deadline. Later object
creation and asynchronous S3 lifecycle rounding can delay physical deletion;
this is not an exact-time deletion promise or a month-long live retention test.
Keep the trusted storage descriptor and authorized reader identity for old IDs.

The nonempty results bucket, runner artifact, state storage, networking, IAM and
SSM documents remain intentionally deployed. Worker `down` does not remove them.
See [retaining result access](../setup.md#retaining-result-access) and
[full teardown](../setup.md#recovery-and-teardown): the result bucket has
`force_destroy=false`; destroying retained results ends recovery, and removing
the operator role requires arranging continued reader access first. Full
foundation destruction is a separate decision. Manual worker cleanup remains
necessary until MVP 4.

## Requirement and evidence matrix

The implementation slices are [#16 / PR #22](https://github.com/JosephWest2/cloud_dev/pull/22),
[#17 / PR #23](https://github.com/JosephWest2/cloud_dev/pull/23),
[#18 / PR #24](https://github.com/JosephWest2/cloud_dev/pull/24),
[#19 / PR #25](https://github.com/JosephWest2/cloud_dev/pull/25) and
[#20 / PR #26](https://github.com/JosephWest2/cloud_dev/pull/26).
Their historical live reports are [#17](17-result-foundation.md),
[#18](18-exec-dispatch.md), [#19](19-exec-observation.md) and
[#20](20-durable-logs.md). Each was reviewed by fresh non-implementing subagents
before merge. Named tests below exist in the tested source and run under
`make check`; service failures injected by tests are controlled evidence.

| Parent deliverable | Slices and meaningful automated tests | Live evidence |
| --- | --- | --- |
| Go SSM exec and managed resolver | #16/#18: `TestDispatchUsesLifecycleResolverScopeAndAmbiguityGuards`, `TestSubmitLiteralPayloadAndDurableAnnouncementOrder`, `TestActualSDKLostSendResponseMakesOneWireSubmission` | Historical #18 actual dispatch; fresh L1–L6 gate below |
| User/cwd/timeout/literal argv and explicit shell | #16–18: `TestExecSeparatorPreservesAllRemoteArguments`, `TestPrepareUsesRemotePOSIXPathsAndRejectsInvalidBytes`, `TestProcessLiteralEnvironmentCwdAndStdin`, `TestLiteralPayloadRoundTrip` | Historical #17/#18 context probes; fresh L1–L3/L6 |
| Exact workload exit, early ID and distinct local outcomes | #18/#19: `TestObserveDurableExitAndPublicationPrecedence`, `TestExecutionOutputSeparatesWorkloadWrapperAndLocalInterruption`, `TestRunPostDispatchObservationFailuresRetainRecoveryAndStatus` | Historical #18/#19 exits 0/1/2/4/255; fresh L1/L2/L4/L5/L6 |
| Restricted S3 persistence, full logs and post-down recovery | #17/#20: `TestResultStorageControlsAndOwnerBoundRequests`, `TestResultStorageAndRunnerDrift`, `TestRunSDKRecoversHistoricalBinaryExportsWithoutRuntimeOrSSM`, `TestCopyStreamExactBytesAndBoundedWrites`, `TestExportStreamPublishesOnlyVerifiedPrivateFile` | Historical #17 actual IAM boundaries, #20 full before/after exports; fresh six-ID recovery |
| Asynchronous output and Ctrl-C detachment | #19/#20: `TestPrototypeObserverDisappearance`, `TestSupervisorDetachPreservesOneEnvelopeAndRecoveryIDs`, `TestObservePendingAndCancellationRemainNonterminal`, `TestObserveFinalWinsCancellationAndOptionalSSMRaces` | Historical #18 SIGKILL, #19 actual PTY/SIGTERM, #20 pending snapshots; fresh L5 |
| JSON, actionable sanitized diagnostics and managed permissions | #17–20: `TestRunIdentityFailuresAreSanitized`, `TestSDKStorageAPIErrorsDoNotLeakOrLookMissing`, `TestSDKCredentialProviderFailuresAreSanitizedAndClassified`, `TestRunRejectsUsageAndWrongStorageScopeBeforeAWS`, `TestLogsCLISelectionAndMetadataNeverMixWithBytes` | Historical #17 existing-object role denial and unmanaged-document denial; fresh metadata/byte separation |

| Seven human steps | Reproducible path and passing gate | Automated coverage |
| --- | --- | --- |
| 1. Launch and clone public fixture through shell | One inspected worker; exact detached checkout through actual SSH, minimal tool versions | `TestLocalSSHMasterPreservesRemoteExitStatus` plus lifecycle/readiness checks |
| 2. Successful fixture check | L1: text `remote_exit`/0, one passing fetched unittest | `TestObserveDurableExitAndPublicationPrecedence` |
| 3. Deliberate failed check | L2: JSON `remote_exit`/1, original assertion stderr | `TestExecutionOutputSeparatesWorkloadWrapperAndLocalInterruption` |
| 4. Literal args and cwd/user | L3: exact 13 arguments, nonroot user, cwd with space, fixed environment/EOF/umask | `TestExecSeparatorPreservesAllRemoteArguments`, `TestProcessLiteralEnvironmentCwdAndStdin` |
| 5. Output beyond inline limit | L4: 1 MiB/768 KiB full binary exports/raw streams match bytes and SHA-256, workload 255/retrieval 0 | `TestCopyStreamExactBytesAndBoundedWrites`, `TestRunSDKRecoversHistoricalBinaryExportsWithoutRuntimeOrSSM` |
| 6. Disconnect, restart and recover | L5: actual terminal Ctrl-C, local 4, fresh empty-state pending then complete, remote finish after local exit | `TestSupervisorDetachPreservesOneEnvelopeAndRecoveryIDs`, `TestRecoverStartedAndOutcomeSnapshots` |
| 7. Finish, down and retrieve | All six: complete before down, exact instance/root cleanup, same IDs/bytes/hashes after down | `TestRecoverStandaloneHistoricalFinalSeparatesRetrievalFromWorkload`, `TestResultRecoveryDescriptorWithoutLaunchOrSSH` |

| Failure gate | Meaningful automated coverage and expected result | Evidence classification |
| --- | --- | --- |
| Execution timeout versus ordinary failure | `TestProcessTimeoutDescendantsAndCaptureBounds`, `TestRunCompleteTimeoutResultIsSuccessfulRetrieval`: workload timeout/exec 4; complete retrieval 0 | Fresh L6 versus L2/L4; historical #19 actual timeout |
| Delivery and hard wrapper timeout | `TestObserveWrapperStatesNeverBecomeWorkloadExit`: `delivery_timeout`/4 and `runner_timeout`/4; raw -1/137 cannot become workload exit | Controlled SSM responses; no live network disruption |
| Local observation loss | `TestObserveExpiredAndInterruptedEvidence`, `TestRunUnknownSubmissionAndInterruptionRetainIdentity`: keep IDs and last known state | Fresh L5; historical #19 local wait expiry/SIGTERM and #20 delayed logs restart |
| Temporarily unreachable SSM | `TestObserveEventualVisibilityAndTransientErrorsWithoutRedispatch`, `TestObserveRetryBudgetsAndActionableErrors`: recover bounded transient errors; exhausted budget is `api_failed`/`ssm_unavailable`/1 | Controlled throttle/transport responses; never redispatch |
| Denied S3 output | `TestRunOutputFailurePreservesKnownWorkload/denied`, `TestRunSecondExportFailureReportsFirstVerifiedFile`: `access_denied`/1, failed overall verification, retain workload/any verified first file | Controlled logs fixtures; historical #17 actual role denial is complementary |
| Missing/unavailable output | `TestRunOutputFailurePreservesKnownWorkload/missing`, `/credentials`, `/opaque`: `incomplete` or `unavailable`/1; `TestRecoverAbsentMetadataCannotProveExpiryOrSSMIdentity`: `missing_or_expired` | Controlled fixtures; absence is not proof of expiry |
| Corrupt/short/extra bytes and unsafe exports | `TestCopyStreamRejectsUnverifiedContent`, `TestCopyStreamRequiresEOFAndClosesOnEveryReadFailure`, `TestExportStreamDoesNotOverwriteRacingFileOrSymlink`, `TestRunOutputFailurePreservesKnownWorkload/corrupt` | Controlled fixtures; raw stream failure may already have emitted an unverified prefix |
| Wrong scope | `TestRunRejectsUsageAndWrongStorageScopeBeforeAWS`, `TestRecoverRejectsInvalidInputBeforeCloudReads`, `TestRecoverReconcilesEveryObservedBindingAndTimeline` | Controlled result bindings plus historical #17 actual other-owner/deployment S3 denial; no foreign worker provisioned |
| Cancellation and completion races | `TestObservePendingAndCancellationRemainNonterminal`, `TestObserveFinalWinsCancellationAndOptionalSSMRaces`, `TestSupervisorPreservesEstablishedCompletionDuringInterrupt` | Controlled precise races; fresh L5 and historical #19 real signals |
| SSM history unavailable | `TestRecoverOptionalSSMFailuresAndPublicationRace`, `TestRecoverCompleteFinalSupersedesOptionalReadFailureButNotCorruption`, `TestRunSDKRecoversHistoricalBinaryExportsWithoutRuntimeOrSSM` | Controlled unavailable-history/no-SSM-call tests plus actual storage-only post-down retrieval; live history not artificially expired |
| Retention deadline | `TestRecoverRequestRetentionUsesServerTimestamp`, `TestRecoverFinalIncompleteAndExpiredPreserveRemoteOutcome` | Controlled expiry plus historical #17 live policy and fresh immutable deadlines; no claim of 30-day observed deletion |

For a focused reproduction of unsafe-to-force failure gates, run these locally;
they use controlled fixtures and do not mutate the deployed AWS scope:

```sh
go test ./internal/execution -run 'TestObserve(WrapperStatesNeverBecomeWorkloadExit|EventualVisibilityAndTransientErrorsWithoutRedispatch|RetryBudgetsAndActionableErrors|PendingAndCancellationRemainNonterminal|FinalWinsCancellationAndOptionalSSMRaces)$' -v
go test ./internal/logs -run 'TestRun(OutputFailurePreservesKnownWorkload|SecondExportFailureReportsFirstVerifiedFile|RejectsUsageAndWrongStorageScopeBeforeAWS|SDKRecoversHistoricalBinaryExportsWithoutRuntimeOrSSM|CompleteTimeoutResultIsSuccessfulRetrieval)$' -v
go test ./internal/execution -run 'TestRecover(OptionalSSMFailuresAndPublicationRace|CompleteFinalSupersedesOptionalReadFailureButNotCorruption|RequestRetentionUsesServerTimestamp|FinalIncompleteAndExpiredPreserveRemoteOutcome)$' -v
go test ./cmd/devbox -run 'TestSupervisor(PreservesEstablishedCompletionDuringInterrupt|DetachPreservesOneEnvelopeAndRecoveryIDs)$' -v
```

## Fresh #21 actual evidence

All six commands completed their required paths on **September 14, 2026 UTC**.
The actual campaign retained one worker and one fixture checkout across five
original submissions and one guarded continuation. It never replayed an attempted
command. Private evidence is under `/tmp/devbox-issue21-live-checks`; the final
allowlisted summary is `evidence/final-aggregate.json`. Credentials, raw API/state
responses, terminal transcripts and workload bytes remain outside the repository.

Fresh `make check`, `make build` and isolated `make infra-check` all returned 0
from source `17e2f1804db4b627efd3094a94763b006fa17c57`; logs are under
`/tmp/devbox-issue21-checks`. The source and CLI hash at the top of this document
did not change during acceptance. Local versions were Go `1.27.0-X:nodwarf5`
on Linux amd64, OpenTofu `1.12.6`, locked AWS provider `6.64.0`, AWS CLI
`2.34.32`, OpenSSH `10.5p1`, Session Manager Plugin `1.2.835.0`, Python
`3.14.7`, Git `2.55.0` and devbox `0.1.0-dev`. Actual worker versions were
Ubuntu `24.04`, Git `2.43.0` and Python `3.12.3`.

Fresh export and doctor passed all 14 checks with no initial nonterminated scoped
worker. The launched worker reached running, SSM online, bootstrap complete and
readiness ready. Independent inspection passed exact request/scope, template 4,
AMI, instance profile, VPC/subnet/security group, required IMDSv2, encrypted
100 GiB gp3 root with deletion on termination, and zero ingress on every group.
Actual `devbox ssh` returned 0 with both exact fixture markers; the clean detached
checkout matched `621e4974ca25ce531773def586ba3ed8e736b3fc`.

| Lifecycle recovery identifier | Actual value |
| --- | --- |
| Launch request | `5f85d90d2143d7b54edb6f0b3015c98e` |
| Exact worker | `i-0187d2a6a708e198e` |
| Captured original root | `vol-0e08ebaac53219717` |

Fresh non-implementing reviewers approved the helpers before use and the later
helper-only correction/continuation guard. The frozen helper identities are:

| Helper | SHA-256 |
| --- | --- |
| Original campaign `check.py` | `acac684c795b2532468ad4f4a98516657b24acc3a309c9ce7362329d3982e398` |
| Corrected campaign `check.py` | `f673e93eb3ffb4403b9908b745fa023d93714107bda064303efe68b191af8e7b` |
| SSH `fixture.py` | `cd123574cc77036f9cace937962f0f17234801ecb6753dbc2fcb8bde0ef6bc97` |
| `fixture-shell.input` | `37eb761f326f8f67789e30771b3505f949c84973719354113d72235a8d107ae2` |
| `lifecycle.py` | `729e8af633291d5044f7260bad1b9d8c352be07199c23d7905abe9c3094d551a` |
| `inspect_worker.py` | `ce3e2d7c64b6c577c07b34df993a9adafee1645158c3f8d9230e4b195483536d` |
| `verify_cleanup.py` | `864663f2983df412ae9fbde3ce599cca872b399ee847d5b7385155e5ab3c6913` |
| Guarded `continue_timeout.py` | `ca9ba248a1bbc18880c2048efff3e3203ce1f9553ca8aa4e3902d119959fef43` |

### Actual results and recovery IDs

| Case | Exec outcome / actual local exit | Durable workload | Full publication / logs retrieval |
| --- | --- | --- | --- |
| L1 fixture success | Text `remote_exit` / 0 | Exited 0; one passing unittest | Complete / 0 |
| L2 deliberate assertion | JSON `remote_exit` / 1 | Exited 1; expected `6 != 7` failure | Complete / 0 |
| L3 literal context | JSON `remote_exit` / 0 | Exited 0; exact 13 args, cwd, nonroot user, environment/EOF/umask | Complete / 0 |
| L4 large binary | JSON `remote_exit` / 255 | Exited 255; exact 1 MiB stdout and 768 KiB stderr | Complete / 0 |
| L5 terminal Ctrl-C | JSON `interrupted` / 4 | Continued and exited 0 after local detach | Complete / 0 after seven pending snapshots |
| L6 two-second execution limit | JSON `execution_timeout` / 4 | Execution timeout, signal 15, no ordinary exit | Complete / 0; exact partial stdout and empty stderr |

Every JSON invocation produced one newline-terminated metadata envelope.
Connected exec results retained exact workload status; L5 retained its original
IDs and started observation without inventing an exit. Text exec/logs metadata
stayed separate from workload bytes. Every completed command passed text/JSON
status, two-file export, raw stdout and raw stderr checks. Status reported
`verification=not_downloaded`; downloads reported verified selected bytes,
private 0600 exports and no temporary leftovers. Every logs process started with
new empty devbox state, a six-field storage descriptor and the restricted
`sh`/`aws` PATH, without SSH keys, runtime pins or command receipts.

| Case | Public command ID | SSM command ID |
| --- | --- | --- |
| L1 | `dc1-1944c64be27d0049ac2c189a4cab5725` | `86427f72-f174-44f9-bb64-848a9131def6` |
| L2 | `dc1-810b553e16043c75f95665522f7b73dc` | `cfe843ac-5500-4560-9b61-0a98f56e35b3` |
| L3 | `dc1-92045e134616e5443ef99ef0bde464ef` | `6ee212bf-53d7-488f-8f16-da5d8a1718a1` |
| L4 | `dc1-0c3d4bc0d96a54a3876db78254737ff7` | `dbed4a36-1025-4186-bd8e-98e79ec8939b` |
| L5 | `dc1-8f3cb686d423ac2a13ef8d958c22a9f7` | `f7d9f19d-0229-4b1b-a21c-f491932b3dd9` |
| L6 | `dc1-bc57e969949c8eb55529ad180cc74803` | `c3d20b7e-8609-4ccb-a5a3-04b388281247` |

These non-secret IDs still require the retained trusted scope and an authorized
reader. For example, within its retention window:

```sh
fresh_logs dc1-0c3d4bc0d96a54a3876db78254737ff7 --json
```

Independent S3 reads established all five records, exact bindings, AES256,
capture/publication agreement and shared submission-based deadlines. All dates
below are UTC; started/finished metadata uses whole-second precision.

| Case | Server submission | Started → finished on 2026-09-14 | Shared expiry |
| --- | --- | --- | --- |
| L1 | `2026-09-14T00:39:16Z` | `00:39:16Z` → `00:39:16Z` | `2026-10-14T00:39:16Z` |
| L2 | `2026-09-14T00:39:46Z` | `00:39:46Z` → `00:39:46Z` | `2026-10-14T00:39:46Z` |
| L3 | `2026-09-14T00:40:15Z` | `00:40:15Z` → `00:40:15Z` | `2026-10-14T00:40:15Z` |
| L4 | `2026-09-14T00:40:33Z` | `00:40:33Z` → `00:40:33Z` | `2026-10-14T00:40:33Z` |
| L5 | `2026-09-14T00:40:55Z` | `00:40:55Z` → `00:41:25Z` | `2026-10-14T00:40:55Z` |
| L6 | `2026-09-14T00:52:12Z` | `00:52:12Z` → `00:52:14Z` | `2026-10-14T00:52:12Z` |

The complete original streams and post-down downloads shared these lengths and
SHA-256 values. L4 additionally matched the independent expected byte patterns.
Empty streams were actual zero-byte objects/files, not absent output.

| Case / stream | Bytes | SHA-256 before and after down |
| --- | ---: | --- |
| L1 stdout | 0 | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| L1 stderr | 155 | `5d0e404c90db5713216f29517decad307fd800c4d1797b39c841b4ed5ff3f21c` |
| L2 stdout | 0 | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| L2 stderr | 326 | `0a94e16f8fb8fc8fa5056b1a84b1fa8cf48f3a316c05f4e5302feb38b9c615f5` |
| L3 stdout | 443 | `2c8fdbd4de69ceeb53749552569ed0939612c5b21ce1049ce918e399b1201660` |
| L3 stderr | 0 | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |
| L4 stdout | 1,048,576 | `e4b90b6607d9958b6f4c811acdd39ef3eb79cc8edd7fb24eacfcf36d455e16a2` |
| L4 stderr | 786,432 | `aa5ab7a6138cc2697d302082ac548b566589108f3f59daf81c01f1b26a5b036f` |
| L5 stdout | 91 | `9c2f8dc707b5249d113c0f5014802f102219be21edc72636a7f041b4b86c220c` |
| L5 stderr | 27 | `a48b6bac95ff086a234d0378f9ac40eaf30995b956abc4afb7eb5ad060d5201d` |
| L6 stdout | 35 | `3f8997660de3a85adf91b59f69832cbafa581a640ffe35f8152ec7760ac55670` |
| L6 stderr | 0 | `e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855` |

### Detachment, preserved failure and guarded continuation

L5's acknowledgement was observed at `00:40:54.871010Z`; its workload began at
`00:40:55.139608Z`. Actual controlling-PTY Ctrl-C arrived at
`00:41:00.832497Z`, **5.692889 seconds after remote start**. The CLI exited at
`00:41:00.833710Z`, **1.213 ms later**, with local exit 4. The remote workload
finished at `00:41:25.139737Z`, after **30.000128 seconds** of sleep and
**24.306027 seconds after local exit**. No remote cancellation occurred and
all dedicated local session groups stopped without emergency cleanup.

Seven fresh `pending`/1 snapshots finished between `00:41:02.143984Z` and
`00:41:22.624100Z`, entirely within the remote execution interval. The next
snapshot completed at `00:41:25.532699Z`, reporting final workload exit 0 and
complete publication. These are original live pending observations, not
snapshots reconstructed during later recovery.

The original helper stopped after **539 checks: 538 passed and one failed**, with
28 logs processes. The failing assertion compared fractional workload finish
`00:41:25.139737Z` against the runner's whole-second `finished_at=00:41:25Z` as
if that value had subsecond precision. The command had completed correctly;
this was a helper-only timestamp assertion error. The original helper,
checks, attempts, snapshots and byte evidence remain unchanged.

A fresh reviewer approved comparing against the represented one-second interval
and reusing the saved original pending observations. Corrected read-only recovery
of L1–L5 passed **471 checks with 25 logs processes**, established the fifth
complete byte baseline and explicitly skipped the never-attempted L6. A separately
reviewed guard proved no L6 attempt or recovery ID existed, bound the unchanged
CLI/manifest/worker/fixture, and submitted only L6 into `run-02`. That command
passed **105 checks with five logs processes**. There are exactly six distinct
public IDs and six distinct SSM IDs across the two runs; none was redispatched.
Product code, runtime artifact and infrastructure did not change.

### Exact cleanup and post-down verification

Down returned 0 for `i-0187d2a6a708e198e`. Independent operator reads confirmed
that exact instance terminated and original root `vol-0e08ebaac53219717`
returned `InvalidVolume.NotFound`. Cleanup completed at
`2026-09-14T00:54:35.994257Z`, with **zero scoped nonterminated instances and
zero scoped volumes**. Final `devbox ls` returned 0 and showed two terminated
entries, including the exact campaign worker; it contained no active worker.

Both post-down recoveries required their pre-down complete baselines. Original
`run-01` recovered its five submitted IDs with **474 passing checks and 25 new
logs processes**, explicitly skipping its original unattempted L6 slot.
`run-02` recovered L6 with **95 passing checks and five new logs processes**,
with no skip. Together, **all six submitted commands passed 569 post-down
assertions through 30 fresh logs processes**. No submitted case was omitted.
Across original execution, recovery, continuation and post-down phases, 88 fresh
logs processes ran; seven original pending snapshots correctly returned 1.

All **twelve streams, totaling 1,836,085 bytes**, were independently compared
byte-for-byte before and after teardown and had unchanged SHA-256, workload,
publication, bindings and deadlines. Production logs calls used retained storage
descriptors and fresh empty state; the independent reader used only STS/S3.
No worker, EC2/SSM observation, local finalizer, cancellation or redispatch was
needed. This proves immediate retained recovery; controlled tests above prove
unavailable-history behavior, and thirty days of physical deletion were not
observed. The durable foundation and valid results remain intentionally retained.

Fresh non-implementing review independently approved the actual six-command
evidence, original-versus-after byte comparisons, deadlines, no-replay guards
and raw cleanup responses. It also verified every complete baseline preceded
AWS shutdown at `00:53:03Z`, and every post-down logs process followed completed
cleanup at `00:54:35.994257Z`. Fresh final review also approved this report's
identifiers, timestamps, hashes, versions, counts and cleanup conclusions.
[The #21 plan](../plans/21-exec-logs-acceptance.md) and
[PR #27](https://github.com/JosephWest2/cloud_dev/pull/27) record the final
commit verification and merge outcome. Parent #2 closes after confirming all
six child issues are closed and its acceptance gates have passed.
Interactive terminal streaming, coding-agent orchestration, repository-specific
images, Spot/groups and scheduled expiry remain in later issues.
