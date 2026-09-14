# MVP 2: execute commands and recover complete logs

This is the reproducible acceptance runbook for [#2](https://github.com/JosephWest2/cloud_dev/issues/2)
and its final slice [#21](https://github.com/JosephWest2/cloud_dev/issues/21).
The fresh #21 outcome is explicitly pending in the evidence section below;
earlier passing slices do not establish this final gate.

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
must follow local CLI exit. The remote job must exit 0 with two JSON stdout
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

Run all four logs modes again for every original ID with fresh state and new
files. Require the same workload, complete records, original submission/expiry,
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

## Fresh #21 actual evidence — pending

**This section is deliberately incomplete until the live campaign and fresh
independent review pass. #21 and parent #2 must remain open while it is pending.**
Private evidence is reserved at `/tmp/devbox-issue21-live-checks`; publish only
the selected non-secret identifiers, versions, timestamps, counts and hashes.
Never commit credentials, signed URLs, raw API/state responses or workload bytes.

Fresh `make check`, `make build` and isolated `make infra-check` have passed
from the frozen source above. Captured local versions are Go
`1.27.0-X:nodwarf5` on Linux amd64, OpenTofu `1.12.6`, locked AWS provider
`6.64.0`, AWS CLI `2.34.32`, OpenSSH `10.5p1`, Session Manager Plugin
`1.2.835.0` and Git `2.55.0`. The non-secret version record is
`evidence/tool-versions.json`. These completed offline checks do not establish
the pending worker or fixture gate.

The final reviewer must fill and verify this one evidence record:

- Actual UTC campaign date; tested full source and final documentation commit;
  CLI, campaign, SSH helper/script and cleanup-helper SHA-256; local Go/OpenTofu/
  provider/AWS CLI/OpenSSH/Plugin/Python/Git/devbox versions and remote Ubuntu/
  Git/Python versions; final offline check outcomes and private log locations.
- Fresh export/doctor 14-pass and no initial nonterminated instances; exact launch request,
  instance and captured original root IDs; launch/template/AMI/role/network/
  storage inspection; exact fixture revision, real SSH exit and READY/EXIT markers.
- Six distinct public recovery IDs and SSM IDs mapped to L1–L6, actual local
  outcomes/exits, durable workloads/publication, each server submission/shared
  expiry/start/finish UTC timestamp; no replay, uncertain or skipped attempt.
- For each original stdout/stderr: actual byte length and full SHA-256, before/
  after download comparisons and private 0600 exports. L4 must additionally
  match the independent deterministic expected bytes above.
- L5 acknowledgement/start/actual Ctrl-C/local exit/remote begin/finish times,
  real pending snapshot times/count, final result, local descendant cleanup;
  L6 actual timeout/signal/marker evidence; one-envelope JSON and text/logs output
  separation; total command, logs invocation and independent assertion counts.
- Exact down exit, exact terminated ID, original root `InvalidVolume.NotFound`,
  empty scoped nonterminated-instance/volume inventories and final `ls`; six original IDs
  recovered afterward with unchanged complete status and all stream bytes/hashes,
  no missing baselines, skipped cases, remote finalizer or redispatch.
- Any failed attempts or corrected helpers, bounded recovery and affected reruns;
  fresh non-implementing review of helpers before use and actual evidence,
  cleanup, report and PR before merge; final #21 PR and parent closure outcome.

The seven paths and all failure gates above must have passing evidence before
closing the parent, with all six child issues closed. Interactive terminal
streaming, coding-agent orchestration, repository-specific images, Spot/groups
and scheduled expiry remain in later issues.
