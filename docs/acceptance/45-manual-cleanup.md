# Manual expiry cleanup and recovery (#45)

The manual CLI calls the same shared service intended for the scheduled adapter.
The controlled checks below do not establish live IAM, scheduler deployment or
laptop-offline acceptance. #48 owns those user-run gates.

## Preview and execute

Use the same explicit config, profile and region for preview, execution and
recovery. The expected account, deployment and owner come from trusted TOML.
The selected AWS identity must pass STS account verification. Cleanup does not
require a manifest, profile file, valid launch TTL, SSH key/tool, local receipts,
OpenTofu, SSM or a healthy scheduler.

```sh
devbox --config ./my-config.toml --aws-profile devbox-operator cleanup --dry-run --json > cleanup-preview.json
devbox --config ./my-config.toml --aws-profile devbox-operator cleanup --json > cleanup-result.json 2> cleanup-evidence.jsonl
```

The second invocation authorizes expiry-policy removal without a prompt. It
performs a fresh discovery, freezes candidate IDs, then rechecks each exact ID;
the preview never authorizes a stale set. There are no target/group/all selectors,
`--yes` or TTL overrides. Default timeout is 165s. A shorter `--timeout` wins;
values above 165s through 5m retain the service ceiling.

Actual cleanup acknowledges evidence on stderr before dispatch. Preserve the
result and evidence files even on nonzero exit: `termination_prepared` contains
scope, run ID, expiry, exact instance/root IDs, device names and deletion flags.
It records intent and evidence, not proof that termination was requested.
`outcome` and `summary` events report subsequent observations. Diagnostic write
failure prevents the affected termination; restore writable diagnostics and rerun.
Dry-run emits no evidence events and makes no AWS writes.

## Interpret and recover

| Observation | Meaning and next action |
| --- | --- |
| `expiry_future` | Deadline has not arrived; skip. Active work never extends it. |
| `expiry_missing` | Legacy worker has no automatic expiry. Inspect with `ls --json`; use explicit `down INSTANCE_ID` for deliberate teardown. |
| `expiry_invalid` / `expiry_duplicate` | Invalid/ambiguous metadata; no expiry termination. Inspect the known ID; explicit `down` remains available. |
| `scope_mismatch` / `identity_unverified` | Restore the intended credentials/config scope; do not broaden owner or filters. |
| `cleanup_no_candidates` | Complete clean scan, zero new candidates; no claim that a disk was deleted. |
| `scan_incomplete` | No authorized candidates. Retain observed IDs, restore connectivity/permissions and rerun. |
| `termination_denied` / protection | Resolve the specific scoped permission/protection issue before retrying. |
| `termination_unknown` / `cleanup_interrupted` | A request may have been accepted. Retain IDs and rerun cleanup to observe current state. |
| `root_volume_retained` | Automatic cleanup skips retained roots. Inspect exact mappings and use deliberate explicit teardown when appropriate. |
| `root_volume_unverified` / unavailable | EC2 termination alone does not prove deletion. Use earlier evidence to inspect the exact root ID; never broadly delete volumes. |
| Already terminated, mappings absent | May be a benign historical row. Root deletion remains unavailable and `cleaned_count` stays zero. |

```sh
devbox --config ./my-config.toml --aws-profile devbox-operator ls --json
devbox --config ./my-config.toml --aws-profile devbox-operator cleanup --json
# Deliberate teardown of one known legacy or non-expired worker:
devbox --config ./my-config.toml --aws-profile devbox-operator down INSTANCE_ID --timeout 5m --json
```

Independent valid expired workers can complete while peers fail. Inspect every
instance's status, root deletion and errors. `terminated_count` requires observed
EC2 termination; `cleaned_count` additionally requires verified root deletion.
`ok` means complete success, not that one API request was sent. Safe reruns never
recreate workers, alter expiry or delete result storage, permanent launch history
or unrelated volumes. If stdout fails, exit 1 and the complete result is attempted
on stderr; preserve that fallback JSON. An unwritable stdout cannot itself remain
parseable, and failures of both output destinations cannot preserve new evidence.

## Controlled validation

- CLI routing/argument fixtures reject unsupported and duplicate flags before the
  runner/factory can load credentials or mutate anything.
- Config/profile overrides and exact expected-account checks are exercised with
  missing launch/access files and invalid launch TTL/max-count settings.
- Shared in-memory STS/EC2 fixtures exercise clean emptiness, dry-run zero writes,
  missing/future/malformed expiry, mixed denial/success, unverified root deletion,
  account mismatch, cancellation and incomplete discovery timeout.
- Direct service and CLI-adapter dry-run results match for identical controlled
  fixtures and a fixed clock. `runCleanup` preserves `expiry.Result`; scheduled
  tests can use the same `New`/`NewAWS` service contract and controlled resources.
- Evidence tests verify complete mappings precede mutation, failed/short writes
  deny dispatch, concurrent writes remain valid JSON and blocked acknowledgment
  stays bounded with at most one underlying writer call.
- Output tests parse one result on mixed/timeout outcomes and recover every known
  instance/root ID from stderr after failed or short stdout writes.
- Real-binary subprocess tests use closed inherited pipe descriptors and loopback
  STS/EC2 fixtures. Closed stdout retains the fallback result and exact IDs on
  stderr, including after completed cleanup; closed stderr denies termination
  and returns the failure result on stdout. Closing both streams still returns
  an ordinary failure without authorizing termination. Non-cleanup workers keep
  their existing SIGPIPE behavior.

Run:

```sh
go test ./internal/cli ./internal/config ./internal/expirycleanup
go test -race ./cmd/devbox ./internal/cli ./internal/config ./internal/expirycleanup
make check
make build
git diff --check
```

Live restricted-role dry-run/manual comparison, scheduled fixture comparison in
its deployed environment, and laptop-offline removal/root evidence remain #48
acceptance work. Do not infer unattended readiness from these controlled tests.
