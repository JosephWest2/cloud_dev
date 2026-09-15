# Issue #46: scoped Go cleanup deployment

Status: controlled/offline implementation. **No live apply, restricted-role
authorization proof, enabled schedule or unattended acceptance is claimed.**
The schedule defaults to `DISABLED`. Install #47's independent failure route
before enabling it through the reviewed #48 migration.

## Adapter and evidence

`cmd/devbox-cleanup` binds `internal/expirycleanup` directly to Lambda. It needs
only AWS credentials and the deployed environment: `DEVBOX_ACCOUNT`,
`DEVBOX_REGION`, `DEVBOX_DEPLOYMENT`, `DEVBOX_OWNER`, and `DEVBOX_LOG_GROUP`.
Region must equal Lambda's `AWS_REGION`; group name must match the trusted scope.
The shared service verifies STS and regional EC2 evidence before termination.
No CLI, SSH, launch receipt, worker role, result bucket or ledger is required.

Input is the #42 schema-1 correlation object. Unknown/duplicate keys, alternate
key casing, nulls, wrong types, trailing documents and authority overrides fail
before configuration loading or mutation. Optional Scheduler correlation values
have bounded syntax and are never used as scope, candidate IDs or evaluation time.
Only allowlisted failure codes escape; provider error text and workloads do not.

Each invocation creates `cleanup/<Lambda request ID>` in the retained handler
group. `invocation_start` and `invocation_end` carry trusted scope, correlation,
UTC time and final status. Shared decision, pre-dispatch mapping, outcome and
summary events are wrapped with the same request ID. The service's `run_id`
remains inside `event`. A clean no-candidate scan still emits summary/end success.
Incomplete cleanup returns a Lambda error, including partial/deadline outcomes.

The sink sends one immutable JSON event per bounded CloudWatch `PutLogEvents`
call, checks rejection fields even after HTTP success, and never truncates a
record while claiming persistence. A pre-dispatch evidence failure prevents that
instance's termination. Summaries larger than the sink's 1,000,000-byte guard
fail truthfully; per-instance records already retained remain available. Current
AWS supports concurrent calls to a stream without sequence tokens. [PutLogEvents](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_PutLogEvents.html)

An accepted Lambda delivery is not a completed invocation. Startup failures,
hard runtime timeouts and failed event delivery still require #47's independent
Scheduler DLQ + Lambda OnFailure destination + Pipe-to-Logs route. The current
doctor deliberately reports unattended evidence as unverified. [Scheduler invokes Lambda asynchronously](https://docs.aws.amazon.com/lambda/latest/dg/with-eventbridge-scheduler.html)

## Fixed budgets and packaging

| Setting | Value |
|---|---|
| Runtime / architecture | `provided.al2023` / `x86_64` |
| Build | `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`, `lambda.norpc`, trimpath, no VCS metadata |
| ZIP | One executable `bootstrap`, fixed 1980 timestamp and POSIX 0755 mode |
| Lambda | 256 MB, 180s timeout, reserved concurrency 1 |
| Adapter/shared service | 165s total soft work budget, bounded setup and log calls |
| Scheduler | `rate(5 minutes)`, flexible window `OFF`, explicit state |
| Scheduler delivery | 2 retries, 300s maximum event age |
| Lambda async execution | 0 function-error retries, 300s maximum event age |
| Logs | 30-day default; selectable 7,14,30,60,90,120,150,180,365 days; retained on destroy |

SDK reads and service limits remain #44's limits; termination SDK retries remain
disabled. Async throttling/system retries are distinct from function-error
retries and can continue until event age expires. Repeated invocations rescan and
recheck current AWS state. [Go packaging](https://docs.aws.amazon.com/lambda/latest/dg/golang-package.html), [Lambda retries](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-error-handling.html)

`make cleanup` produces `bin/devbox-cleanup-linux-amd64.zip`.
`make cleanup-check` checks the real ELF/ZIP, executable mode, repeatable bytes,
and digest changes when content changes. OpenTofu uses `filebase64sha256` for
Lambda updates; the manifest exports `filesha256` as lowercase hex. The Go
verifier converts Lambda's base64 `CodeSha256` before comparing. Dependency
`aws-lambda-go` is pinned to 1.54.0, compatible with the existing Go 1.24 floor;
SDK service module versions match the repository's existing AWS SDK generation.

Healthy scheduling suggests roughly five minutes cadence + up to 59 seconds
precision + bounded service/API work before a termination request, approximately
nine minutes with these budgets. This is a planning target, not a hard spending
cap or a guarantee of completed instance/root-volume deletion. Failures remove
that bound; active work may terminate at expiry. [Scheduler precision](https://docs.aws.amazon.com/scheduler/latest/UserGuide/schedule-types.html)

## Authorization and manifest v6

The cleanup role grants only regional DescribeInstances/DescribeVolumes,
TerminateInstances on exact account/region instance ARNs with exact
ManagedBy/deployment/owner tag conditions, and writes to its exact log group.
Describe APIs require wildcard resources; requested-region conditions bound
them. It cannot launch, retag, delete arbitrary volumes or write S3 data.

The Scheduler role trusts `scheduler.amazonaws.com` with exact account and
**schedule-group ARN** conditions and may invoke only the exact cleanup function.
Lambda trusts its supported service principal without guessed source conditions.
Same-account Scheduler role authorization is used without an unnecessary Lambda
resource grant. [Scheduler trust conditions](https://docs.aws.amazon.com/scheduler/latest/UserGuide/cross-service-confused-deputy-prevention.html)

The operator keeps one inline policy and no managed attachments. The merged
`TagAtCreation` statement enforces identical complete creation tags, including
nonempty `ExpiresAt`, for Fleet and its dependent RunInstances authorization on
Fleet/instance/volume resources. It grants no post-creation CreateTags. Legacy
seven-tag allocation is no longer authorized. IAM does not parse expiry dates;
the approved shared policy and immutable launch guards do that.

Manifest v6 retains all v5 fields, resource addresses, top-level roles and
permanent storage contracts. `cleanup` is an independently decoded descriptor:

- `schema_version: 1`.
- `function`: ARN, runtime, architecture, hex `code_sha256`, canonical JSON
  `environment_sha256`, timeout/service timeout/concurrency/memory, async budgets.
- `schedule`: exact ARN/name/group name/group ARN/state/expression/flexible
  window, delivery budgets and canonical JSON `input_sha256`.
- `logs`: exact name/ARN/retention days.
- `execution_role`, `scheduler_role`: existing Role-shaped ARN, trust SHA256,
  inline policy name and SHA256.
- Optional `evidence`: reserved schema-1 capability, extended/verified by #47.

`config.DecodeCleanup` validates that descriptor separately; the raw JSON
boundary on Manifest keeps broken cleanup data from disabling recovery.
`foundation.VerifyCleanup` checks deployed function, schedule, logs and role
settings with bounded read APIs. `foundation.CheckDeployment` invokes it for
doctor; lifecycle's existing `foundation.Verify` stays independent. Missing or
unhealthy scheduling therefore does not block manual cleanup, inventory, down,
saved logs or fresh launch-pin verification. A matching disabled deployment is
reported as disabled; no descriptor alone is treated as recent run evidence.

## Migration preparation (execute only in #48)

1. Replace old allocating clients with the expiry-aware version. Keep old
   manifests/receipts for observation and explicit teardown; never edit their
   schema numbers or retroactively tag historical workers.
2. Build `make runner cleanup-check` using the selected pinned toolchain. Keep
   existing account/region/owner/deployment, networking, image, result retention
   and backend input files intact. Leave `cleanup_schedule_enabled = false`.
3. In the separate live deployment checkout, initialize the existing reviewed
   backend with the setup profile, then save a plan:

   ```sh
   AWS_PROFILE=devbox-setup tofu -chdir=infra/foundation plan -var-file=foundation.tfvars -out=expiry-upgrade.tfplan
   tofu -chdir=infra/foundation show -no-color expiry-upgrade.tfplan
   ```

4. Review the exact plan before apply: expect new cleanup Lambda/group/schedule/
   roles/log group and operator creation/read-policy changes. Verify no network,
   results bucket, existing workers or permanent ledger deletion/replacement;
   investigate any unrelated image/template/artifact change. Retained Logs use
   `skip_destroy`; their retention setting still expires old events normally.
5. #48 owns the user checkpoint and saved-plan apply. Re-export the real v6
   manifest afterward. Do not relabel a v5 file or trust a mock fixture for AWS
   authorization. Test restricted-role Spot and On-Demand Fleet creation, exact
   foreign-owner/region/account denial and scoped cleanup before acceptance.
6. Install #47 failure transport/alarms and inspect the reviewed second plan
   before enabling. Confirm later successful handler completion and each
   independent failure path in retained Logs. Keep manual cleanup available.

## Controlled verification

Adapter tests use the actual shared service with controlled EC2/STS/sink APIs:
no-candidate success, wrong identity, evidence failure before dispatch,
termination failure, and start/end logging failure. Strict input/environment
tests prove invalid requests never reach the factory; journal tests verify exact
mapping persistence and rejected-event handling.

Health tests cover descriptor scope/budget rejection before reads, exact resource
requests, runtime/architecture/environment/timeout/concurrency/async drift,
schedule/retry/window drift and retained-log drift. A descriptor with an enabled
state and evidence-shaped placeholder still cannot claim unattended readiness.

OpenTofu mock tests cover runtime/package/digest, restrictive termination and
invocation policies, required expiry creation tags, retained logs, retry budgets,
disabled state and invalid retention. Existing multi-AZ policy quota and
oversized-label rejection remain enforced. The real OpenTofu JSON export is
decoded by Go and role/code/environment/input digests are cross-checked.

The optional `infra/foundation/tests/verify_policy.py` IAM simulator now uses v6
expiry tags, tests missing ExpiresAt, and rejects legacy fresh allocation. It was
updated but **not run against AWS**; neither text assertions nor simulation can
prove EC2's live authorization context.

Commands passed: `make check`, `make build`, `make cleanup-check`,
`make infra-check TOFU=/tmp/devbox-tools/tofu` (bootstrap 1, foundation 26,
plus real export/digest bridge), and
`go test -race ./internal/cleanuplambda ./internal/foundation ./internal/config`.
The final #43 recovery correction was merged and affected lifecycle, CLI,
config, foundation and adapter tests passed afterward. `git diff --check` passed.

Rendered operator policy: normal two-AZ `test/test-owner` is **9,843 bytes**
(397 spare); actual three-AZ `personal-dev/joseph`, including all scoped service
role ARNs, is **10,101 bytes** (**139 spare**). Maximum-length labels still fail
the precondition before an oversized policy could be submitted. No second inline
policy or managed attachment was introduced.

#47 must budget new operator health permissions deliberately. Largest remaining
repetition includes seven exact result-object ARNs (`ReadResults`, about 1,045
bytes), the shared creation-tag condition (`TagAtCreation`, about 930 bytes),
and scope/ARN repetition across launch, SSH, probe and storage grants. Preserve
authorization boundaries if compacting; alternatively design a separate scoped
health reader role with exact AssumeRole authority and explicit verifier support.
Adding another inline policy would not evade the aggregate role quota.

No live acceptance evidence belongs to this slice.
