# Issue #48: pre-handler failure and evidence-route protocol

**Live results:** Case B async pre-handler failure and Case C stopped-consumer
backlog/same-message recovery passed. Case A retained permanent denied delivery
with zero retries and no exhaustion attribute. Both proposed synchronous/streaming
A2 mechanisms were rejected by AWS. All fault campaigns are restored; final normal
scheduled completion,16healthy alarms and doctor24/24 passed.

Literal Scheduler retry exhaustion remains **UNPROVED**. The separately authorized
physical-offline retry and all worker/root cleanup passed. See the
[acceptance ledger](04-expiry.md) for exact evidence and revision boundaries.
PR55 remains draft and #48/#4 remain open pending independent final review and a
user decision on the unresolved live gate. No waiver or further AWS probe is
inferred. The procedures below preserve the completed campaign and recovery
instructions; they are not a request to repeat injections.

## Recommendations and the important distinction

1. **Scheduler denied delivery:** append an explicit Deny for only `lambda:InvokeFunction` on the exact cleanup function to the Scheduler role's existing inline policy. Retain its trust and exact-queue `sqs:SendMessage` allow. This tests Scheduler delivery failure reaching its DLQ and retained Logs before handler startup. IAM explicit denies override allows. [IAM evaluation](https://docs.aws.amazon.com/IAM/latest/UserGuide/reference_policies_evaluation-logic.html)
2. **Do not call that retry exhaustion unless the received record proves it.** Scheduler's `EXHAUSTED_RETRY_CONDITION` appears for retryable errors and is absent for permanent errors. A permission rejection may be permanent. Capture its actual classification; never require two retries from the Deny mechanism. [Scheduler DLQ attributes](https://docs.aws.amazon.com/scheduler/latest/UserGuide/configuring-schedule-dlq.html)
3. **The attempted synchronous A2 composition is unsupported.** AWS rejected universal `lambda:invoke` with `InvocationType=RequestResponse` and required `Event`. Do not rerun it, omit InvocationType to rely on a synchronous default, or substitute Lambda async failure for Scheduler exhaustion. A replacement mechanism needs technical validation and actual retained Scheduler retry/exhaustion evidence. Generic Lambda API support does not establish Scheduler integration support. [Lambda Scheduler integration](https://docs.aws.amazon.com/lambda/latest/dg/with-eventbridge-scheduler.html)
4. **Lambda async pre-handler failure:** reserved concurrency zero is explicitly documented to send **new asynchronous events** directly to the configured OnFailure destination, without retries or function triggering. Restore concurrency afterward; events already delivered to the destination are not automatically replayed. [Lambda retained invocation records](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html)
5. **Consumer failure:** prefer `StopPipe`, verify actual `STOPPED`, then generate one new correlated async failure. Observe backlog and failing doctor; `StartPipe`, verify `RUNNING`, and require the original failure record to arrive in retained Logs. This exercises stopped-consumer detection and recovery with fewer moving parts than IAM breakage. Stop/start are supported reversible operations on the exact pipe. [StopPipe](https://docs.aws.amazon.com/eventbridge/latest/pipes-reference/API_StopPipe.html), [StartPipe](https://docs.aws.amazon.com/eventbridge/latest/pipes-reference/API_StartPipe.html)

Run the first Case B canary before H1 health and any workers. Run the remaining cases while the operator is online after verified worker teardown, or with independently confirmed future workers only. Freeze the scope inventory and verify there are no eligible workers to clean up if a control-plane change propagates late. None of these commands allocates a worker or changes any worker tags, TTLs, instances, volumes, Fleet requests, results, or launch ledgers. Original batch/market evidence and the separately authorized physical-offline retry remain separate gates; these failure tests do not replace them.

## Current implementation handoff

These paths match merged #47 at `14bb326`. Live exported-manifest verification
passed for the installed foundation. Bind actual IDs from the current export
instead of guessing names:

| Resource | Manifest JSON path |
|---|---|
| Function ARN | `.cleanup.function.arn` |
| Scheduler name/group/ARN | `.cleanup.schedule.name`, `.group_name`, `.arn` |
| Scheduler role ARN/inline name | `.cleanup.scheduler_role.arn`, `.policy_name` |
| Handler log group | `.cleanup.logs.name` |
| Failure queue ARN/URL/name | `.cleanup.evidence.queue.arn`, `.url`, `.name` |
| Pipe name/ARN | `.cleanup.evidence.pipe.name`, `.arn` |
| Pipe role ARN/inline name | `.cleanup.evidence.pipe_role.arn`, `.policy_name` |
| Retained failure log group/stream | `.cleanup.evidence.logs.name`, `.cleanup.evidence.stream` |
| Alarm names/contracts | `.cleanup.evidence.alarms` |

Current route is standard SSE-SQS, 14-day retention, 1,800-second visibility; Pipe batch 1, no batching window/filter/enrichment; independent CloudWatch Logs target with fixed `failures` stream and retained log group. The retained JSON envelope is:

```json
{"schema_version":1,"kind":"invocation_failure","messageId":"...","body":{},"messageAttributes":{},"SentTimestamp":"...","ingested_at":"...","pipe_arn":"...","source_arn":"..."}
```

`body` can be a JSON object or string. Pipes implicitly parses JSON SQS bodies; the transformer wraps `<$.body>` as an object member and preserves attributes. Compare semantic JSON after at most one string decode, while retaining the untouched raw CloudWatch event. Do not require the original SQS body bytes/whitespace to survive transformation. AWS expressly permits unquoted string variables and adds their quotes; objects must remain unquoted. [Pipes input transformation](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-pipes-input-transformation.html)

The #46 Scheduler target has **literal** `<aws.scheduler.*>` transport keywords; its manifest hash is over canonical JSON. Restore the original fetched `Target.Input` string intact. Never replace literal brackets with JSON `\\u003c`/`\\u003e` sequences in the string sent to Scheduler. The final doctor canonicalizes fetched JSON before comparing the input hash.

## Setup and durable capture (reviewed procedure for each authorized phase)

These are staged fragments, not an unattended script. Source the actual wrapper below: every awsc call invokes the capture executable with explicit AWS argv. Captures use unique case/step labels, preserve stderr/status/times/hashes, snapshot file:// and fileb:// inputs, and optionally snapshot output files. Redirected stdout files are convenience copies; immutable commands/ captures are authoritative. Never try to execute a shell function through capture. Use a fresh private directory for each attempt; never overwrite a previous attempt's originals. `MANIFEST` must be the actual v6 export JSON, not the outer `tofu output -json` map. `SETUP_PROFILE` is the already authorized setup identity; the operator/health roles intentionally lack these mutation permissions. Do not add permissions to the operator to run the campaign.

```bash
set -euo pipefail
umask 077
: "${MANIFEST:?path to final exported manifest}"
: "${SETUP_PROFILE:?approved setup profile name}"
RUN_UTC=$(date -u +%Y%m%dT%H%M%SZ)
: "${ACCEPTANCE_RUN:?existing private durable acceptance run}"
: "${acceptance_repo:?final tested repository path}"
ACCEPTANCE_HELPER="$acceptance_repo/scripts/expiry-acceptance.py"
CAMPAIGN_DIR="$ACCEPTANCE_RUN/failure-$RUN_UTC-$(python3 -c 'import uuid; print(uuid.uuid4().hex[:8])')"
mkdir -p "$CAMPAIGN_DIR"
test ! -e "$CAMPAIGN_DIR/manifest.json"
cp -- "$MANIFEST" "$CAMPAIGN_DIR/manifest.json"
MANIFEST="$CAMPAIGN_DIR/manifest.json"
jq -e '.schema_version == 6 and .cleanup.schema_version == 1 and .cleanup.evidence.schema_version == 1' "$MANIFEST" >/dev/null
REGION=$(jq -er '.region' "$MANIFEST")
ACCOUNT=$(jq -er '.account' "$MANIFEST")
FUNCTION_ARN=$(jq -er '.cleanup.function.arn' "$MANIFEST")
FUNCTION_NAME=${FUNCTION_ARN##*:}
SCHEDULE_NAME=$(jq -er '.cleanup.schedule.name' "$MANIFEST")
SCHEDULE_GROUP=$(jq -er '.cleanup.schedule.group_name' "$MANIFEST")
SCHEDULE_ARN=$(jq -er '.cleanup.schedule.arn' "$MANIFEST")
SCHED_ROLE_ARN=$(jq -er '.cleanup.scheduler_role.arn' "$MANIFEST")
SCHED_ROLE_NAME=${SCHED_ROLE_ARN##*/}
SCHED_POLICY=$(jq -er '.cleanup.scheduler_role.policy_name' "$MANIFEST")
QUEUE_URL=$(jq -er '.cleanup.evidence.queue.url' "$MANIFEST")
QUEUE_ARN=$(jq -er '.cleanup.evidence.queue.arn' "$MANIFEST")
QUEUE_NAME=$(jq -er '.cleanup.evidence.queue.name' "$MANIFEST")
PIPE_NAME=$(jq -er '.cleanup.evidence.pipe.name' "$MANIFEST")
PIPE_ARN=$(jq -er '.cleanup.evidence.pipe.arn' "$MANIFEST")
FAILURE_LOG_GROUP=$(jq -er '.cleanup.evidence.logs.name' "$MANIFEST")
FAILURE_STREAM=$(jq -er '.cleanup.evidence.stream' "$MANIFEST")
HANDLER_LOG_GROUP=$(jq -er '.cleanup.logs.name' "$MANIFEST")
export AWS_PAGER='' AWS_RETRY_MODE=standard AWS_MAX_ATTEMPTS=1
source "$acceptance_repo/scripts/expiry-failure-capture.sh"
begin_failure_case baseline
awsc step-01 sts get-caller-identity > "$CAMPAIGN_DIR/caller.json"
test "$(jq -r '.Account' "$CAMPAIGN_DIR/caller.json")" = "$ACCOUNT"
awsc step-02 scheduler get-schedule --name "$SCHEDULE_NAME" --group-name "$SCHEDULE_GROUP" > "$CAMPAIGN_DIR/schedule.original.json"
awsc step-03 iam get-role --role-name "$SCHED_ROLE_NAME" > "$CAMPAIGN_DIR/scheduler-role.original.json"
awsc step-04 iam get-role-policy --role-name "$SCHED_ROLE_NAME" --policy-name "$SCHED_POLICY" > "$CAMPAIGN_DIR/scheduler-policy.response.original.json"
awsc step-05 lambda get-function-configuration --function-name "$FUNCTION_ARN" > "$CAMPAIGN_DIR/function.original.json"
awsc step-06 lambda get-function-concurrency --function-name "$FUNCTION_ARN" > "$CAMPAIGN_DIR/concurrency.original.json"
awsc step-07 lambda get-function-event-invoke-config --function-name "$FUNCTION_ARN" > "$CAMPAIGN_DIR/async.original.json"
awsc step-08 pipes describe-pipe --name "$PIPE_NAME" > "$CAMPAIGN_DIR/pipe.original.json"
awsc step-09 sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names All > "$CAMPAIGN_DIR/queue.original.json"
```

Capture original `describe-alarms`, baseline doctor output/exit status, full function/config digests, queue counts and finite-window logs too. For the first worker-free Case B canary, missing failure stream/recent success and unsettled alarms are expected pending states; verify actual configuration/role/route pins before invoking it. Finish/disarm the campaign afterward while parked, then require recent genuine scheduled completion and full doctor health before workers. Later failure cases start from that established healthy baseline. Before proceeding, assert all returned ARNs equal the manifest; both failure destinations equal `QUEUE_ARN`; pipe source/target/role/template match the manifest; pipe is actually RUNNING; concurrency is 1; queue has no known backlog; no unrelated actor is changing these resources. Capture all role inline/attached policy inventory when validating the baseline. Expected drift during injection must not be disguised by editing the manifest.

GetRolePolicy may represent the policy as a document or URL-encoded text depending on client handling. Normalize only for editing, preserving the complete original response separately; do not use `unquote_plus`, which changes literal `+`. [GetRolePolicy CLI](https://docs.aws.amazon.com/cli/latest/reference/iam/get-role-policy.html)

```bash
python3 - "$CAMPAIGN_DIR" <<'PY'
import json, pathlib, sys, urllib.parse
p = pathlib.Path(sys.argv[1])
raw = json.loads((p/'scheduler-policy.response.original.json').read_text())['PolicyDocument']
if isinstance(raw, str):
    try: raw = json.loads(raw)
    except json.JSONDecodeError: raw = json.loads(urllib.parse.unquote(raw))
assert isinstance(raw, dict) and raw.get('Version') == '2012-10-17'
(p/'scheduler-policy.original.json').write_text(json.dumps(raw, separators=(',', ':'))+'\n')
s = json.loads((p/'schedule.original.json').read_text())
# Every current UpdateSchedule input field, excluding output-only metadata.
keys = {'Name','GroupName','ActionAfterCompletion','Description','EndDate',
        'FlexibleTimeWindow','KmsKeyArn','ScheduleExpression',
        'ScheduleExpressionTimezone','StartDate','State','Target'}
u = {k:v for k,v in s.items() if k in keys}
assert {'Name','GroupName','FlexibleTimeWindow','ScheduleExpression','State','Target'} <= u.keys()
(p/'schedule.restore.json').write_text(json.dumps(u)+'\n')
u['State'] = 'DISABLED'
(p/'schedule.parked.json').write_text(json.dumps(u)+'\n')
PY
```

Schedule updates replace omitted fields with defaults. Always mutate a **full copy** of the captured input fields, including `StartDate`, `EndDate`, timezone, encryption key, description, complete Target and flexible window. Keep `ActionAfterCompletion=NONE` for a temporary one-time test so the existing schedule is never deleted. Capture full output, but do not pass output-only ARN/creation/modification timestamps back to UpdateSchedule. [UpdateSchedule contract](https://docs.aws.amazon.com/scheduler/latest/APIReference/API_UpdateSchedule.html)

## Restoration must exist before the first mutation

Create the saved recovery entry point after all originals above have been captured
and checked, before the first mutation:

```bash
prepare_failure_restore
arm_failure_restore
```

The sourced functions copy this exact wrapper/helper into the durable campaign,
write a standalone executable restore.sh with only scoped names/paths, and arm
EXIT/INT/TERM/HUP recovery. The active campaign holds the local lock; its trap
releases it before the recovery process acquires it. Repeated catchable signals
cannot interrupt recovery. Another live campaign holding that lock prevents
concurrent repairs. After shell loss, run the saved restore.sh directly.

Recovery attempts to park the full original schedule first, restores any armed
Scheduler policy and Pipe state, then keeps concurrency zero for the bounded
600-second quiescence interval after a successful park. It restores the captured
concurrency (or deletes the setting if originally absent), and captures schedule,
concurrency and Pipe readbacks. Failed repairs do not suppress later independent
steps; failed parking prevents removal of the concurrency guard. Every command,
including denied repair calls, has its own immutable capture.

**Recovery deliberately leaves the schedule parked.** The result is either
RESTORE_FAILED or RESTORE_PARKED_REVIEW_REQUIRED; neither claims health or normal
schedule restoration. Inspect the archived readbacks, exact policy bytes, actual
Pipe CurrentState and original settings before the separately reviewed final
normal-schedule update. Poll bounded transitions and keep unique captures.

In the active campaign shell, complete recovery and disarm its traps **before**
any normal schedule restoration, the canary-to-H1 transition, or offline activation:

```bash
finish_failure_campaign
```

This operation runs parked recovery while protection remains armed, verifies the
complete writable schedule against the parked snapshot, original concurrency,
and settled original Pipe state, then records `finished.json` and removes the
campaign traps/lock. A failed repair or mismatched readback returns nonzero and
keeps recovery armed; do not proceed to activation. After bounded polling resolves
transitions, retry finish while still parked. Exact policy comparison, delivery
backlog inspection, and later health checks remain required independently.

Only after finish succeeds and every required repair/readback agrees may the full
schedule.restore.json be sent through awsc; its original state/dates must remain
intact. Normal shell exit then cannot send another parking update. Never enable
an originally disabled schedule through this restoration. The initial canary
starts disabled: finish it before the separately reviewed enabled foundation
plan. Capture later genuine scheduled success and normal doctor/alarms.

The standalone restore.sh is emergency parked recovery; it cannot disarm traps in
another shell and must not be run after approved normal restoration. After shell
loss, use it and inspect the parked result before separately reviewed restoration
in a new shell. Start each later failure phase with a **new campaign directory and
fresh originals**; a finished campaign cannot be rearmed. Preserve the prior
campaign and its recovery evidence.

Arm the per-resource .armed marker **before** each mutation because a timeout may
follow AWS acceptance. Normal recovery does not erase markers, originals, errors,
or failure messages. If a repair cannot finish, the durable result and exact
commands remain available; stop and diagnose rather than silently claiming a
repaired deployment. SIGKILL, host suspension, kernel/process failures or power
loss cannot be repaired by shell traps. Keep these tests online and record any
interruption as unresolved until independently reconciled.

Do not replay destination messages into cleanup during recovery: the Pipe should drain them to retained Logs. Never purge the queue, delete messages manually, delete log streams/groups, shorten retention, delete/recreate the Pipe, or replace failed-event logs with a later success.

## Case A: exact-role denied Scheduler delivery

Actual `denied-delivery-88667b852400` retained permanent delivery failure for the
original `04:43:00Z` occurrence: `AccessDeniedException`, RETRY_ATTEMPTS0,
EXHAUSTED_RETRY_CONDITION absent, payload truncation/invalid flags false. The
111-predicate proof binds the real Scheduler execution ID across attributes and
the JSON-string Payload inside AWS's Lambda `Event` request wrapper, with no
matching handler invocation in the complete bounded window. Source:
`failure-20260916T043635Z-65620005/cases/denied-delivery-88667b852400/delivery-proof-03.json`.
**Delivery-route retention passed; literal retry exhaustion remains UNPROVED.**
Full guarded recovery and final normal health passed; sources are in
`migration/final-after-faults-proof.json`. Preserve this completed injection;
do not replay it merely to reproduce the row or reinterpret zero retries.


```bash
begin_failure_case denied-delivery
```

Start from the captured, healthy baseline. Park the schedule using the full copy and allow prior work to settle. Generate a policy that adds exactly one statement; preserve all prior statements and use the same inline policy name:

```bash
jq --arg f "$FUNCTION_ARN" '
  .Statement |= (if type == "array" then . else [.] end) |
  .Statement += [{Sid:"Acceptance48DenyCleanupInvoke",Effect:"Deny",
                 Action:"lambda:InvokeFunction",Resource:$f}]
' "$CAMPAIGN_DIR/scheduler-policy.original.json" > "$CASE_DIR/scheduler-policy.denied.json"
touch "$CAMPAIGN_DIR/restore-scheduler-policy.armed"
awsc step-16 iam put-role-policy --role-name "$SCHED_ROLE_NAME" --policy-name "$SCHED_POLICY" \
  --policy-document "file://$CASE_DIR/scheduler-policy.denied.json"
awsc step-17 iam get-role-policy --role-name "$SCHED_ROLE_NAME" --policy-name "$SCHED_POLICY" \
  > "$CASE_DIR/scheduler-policy.injected.json"
```

Verify the trust is unchanged and SQS SendMessage still allowed; do not replace the policy with a Deny-only document. IAM is eventually consistent, so successful readback is not a propagation guarantee. Allow a bounded settling period and use the actual delivery result as the assertion. A 120-second settling allowance is a campaign choice, not an AWS guarantee. [IAM eventual consistency](https://docs.aws.amazon.com/IAM/latest/UserGuide/troubleshoot.html)

Prepare one attempt on the **same schedule** using the complete captured input: copy the full original update, set `ScheduleExpression=at(T)` at a future UTC minute, timezone UTC, `State=ENABLED`, `ActionAfterCompletion=NONE`, retaining the complete original Target/DLQ/role/retries/input. The existing schedule ARN and group remain unchanged. An already approved finite existing schedule window is also usable, but may generate multiple events. No new schedule, role, function or queue is needed.

Concrete update, with the reviewed `CASE_AT_UTC` supplied as `YYYY-MM-DDTHH:MM:SS` (UTC, no trailing Z), sufficiently in the future for the controls to settle. Set `CASE_BASE` to the absolute captured `schedule.parked.json` for A or the current case's `schedule.sync-zero.parked.json` for A2. The original StartDate/EndDate stay captured and are ignored by the temporary `at` expression; the final restoration recovers them exactly.

```bash
: "${CASE_AT_UTC:?future UTC one-time instant from approved timing helper}"
: "${CASE_BASE:?full parked schedule input for this case}"
jq --arg at "$CASE_AT_UTC" '
  .ScheduleExpression = ("at(" + $at + ")") |
  .ScheduleExpressionTimezone = "UTC" |
  .State = "ENABLED" | .ActionAfterCompletion = "NONE"
' "$CASE_BASE" > "$CASE_DIR/schedule.case.once.json"
touch "$CAMPAIGN_DIR/restore-schedule.armed"
awsc step-18 scheduler update-schedule --cli-input-json "file://$CASE_DIR/schedule.case.once.json" \
  > "$CASE_DIR/schedule.case.update.json"
awsc step-19 scheduler get-schedule --name "$SCHEDULE_NAME" --group-name "$SCHEDULE_GROUP" \
  > "$CASE_DIR/schedule.case.readback.json"
```

Required retained-record predicates:

- Outer schema/kind, exact Pipe/source ARNs, nonempty `messageId`, original `SentTimestamp`, and ingestion time.
- `messageAttributes.SCHEDULE_ARN` matches the real schedule; scheduled time lies in the armed window; capture `EXECUTION_ID`, `ERROR_CODE`, `ERROR_MESSAGE`, `RETRY_ATTEMPTS`, truncation status and any exhaustion condition. In Pipes' event representation, attribute value members may be `stringValue`; preserve the raw representation and normalize only the predicate reader.
- Body represents the original target input; capture substituted correlation and actual target ARN representation. Do not assume the attribute uses the concrete Lambda function ARN: universal target attributes can use `arn:aws:scheduler:::aws-sdk:lambda:invoke`.
- No correlated handler `invocation_start` exists in the bounded handler log window, and the error is a pre-invocation authorization rejection. Log absence alone is insufficient proof.
- If `EXHAUSTED_RETRY_CONDITION` is absent, record **permanent denied-delivery retention passed; retry-exhaustion gate still pending**. If present, retain its actual value and actual retry count, without rewriting them.

Restore the whole original policy after capturing the event. `TargetErrorCount` and `InvocationsSentToDeadLetterCount` support the result; `InvocationsFailedToBeSentToDeadLetterCount` must have no observed failure. These are `AWS/Scheduler`, dimension `ScheduleGroup=$SCHEDULE_GROUP`, and are best-effort telemetry rather than an event ledger. [Scheduler metrics](https://docs.aws.amazon.com/scheduler/latest/UserGuide/monitoring-cloudwatch.html)

## Case A2: unsupported synchronous target; exhaustion unproved

The proposed same-function universal `lambda:invoke` target with
`InvocationType=RequestResponse` was **rejected** during `UpdateSchedule` on
September 16 at `03:54:21Z`. AWS returned `ValidationException` requiring `Event`.
The former executable recipe is removed because its integration assumption was
disproved. No scheduled retry/exhaustion claim follows from this control-plane
rejection, and the command's exit 254 is retained rather than relabeled success.

Raw capture: `commands/exhaustion-f5dceb2c89ff-arm-once-476b61b7/`.
Case directory: `failure-20260916T035130Z-de6b048a/`.
EXIT recovery held the 600-second guard, restored exact parked settings/concurrency/
Pipe by `04:04:25Z`, and the originating shell closed. Independent setting review
preceded normal restoration at `04:06:14Z`; exact readback and doctor 24/24 passed.
See `recovery-verified-after-shell-exit.json`, `normal-restoration-review.json`
and `commands/a2-rejected-normal-{update,readback,doctor-01}/`.

The literal exhaustion gate remains open. Any replacement must produce a retained
**Scheduler** DLQ envelope with exact original delivery correlation, an actual
retryable error, positive `RETRY_ATTEMPTS`, and `EXHAUSTED_RETRY_CONDITION` equal
to the documented `MaximumRetryAttempts` or `MaximumEventAgeInSeconds`, with no
corresponding handler startup. Unknown condition strings fail closed. See the
[Scheduler DLQ attributes](https://docs.aws.amazon.com/scheduler/latest/UserGuide/configuring-schedule-dlq.html). Age
exhaustion must not be described as two completed retries unless the event says so.
Case A permanent denial and Case B async OnFailure remain separate evidence.
A subsequent disabled capability probe at `04:13:53Z` rejected
`invokeWithResponseStream` as an invalid Scheduler `aws-sdk:lambda` API.
`failure-20260916T041206Z-09027586/cases/stream-capability-61b43c070827/`
retains the rejection and unchanged full DISABLED schedule/concurrency-1 readbacks.
No scheduled occurrence or function invocation was requested. This streaming
variant is not an actionable supported fixture. Normal settings were restored at `04:15:09Z`, doctor24/24 passed at `04:15:27Z`,
and scheduled invocation `636aaa17-cd77-48a3-acd1-b833fb7f3557` completed successfully
at `04:15:50Z` in the next campaign's baseline capture. Neither rejected mechanism
calls for production changes.
No replacement mechanism or scope waiver is established by this record.

`TestOpenTofuExport/scheduler_retry_exhausted_controlled` now verifies full
attributes/body/correlation retention, explicitly including positive retries and
an exhaustion condition, using controlled typed substitution in the actual
rendered Pipe template. Permanent-denial and Lambda async variants remain separate.
The isolated mock export and Go bridge passed; this is not live AWS classification
or retry-exhaustion proof.

## Case B: Lambda asynchronous failure before the handler starts

Run this worker-free canary before H1 full health: the Pipe must create its fixed
failures stream before doctor can verify it. Reuse the actual case evidence later
in the acceptance matrix; do not generate a second event just to fill a row.

```bash
begin_failure_case async-prehandler
```

Keep the full original schedule parked to make this a single controlled direct async invocation. This is **live destination-route evidence**, not the scheduled/offline worker proof. An actual normal Scheduler tick while concurrency is zero is a valid additional end-to-end variation: templated Scheduler invokes Lambda asynchronously. [Scheduler with Lambda](https://docs.aws.amazon.com/lambda/latest/dg/with-eventbridge-scheduler.html)

Set and read back concurrency zero with restoration armed:

```bash
touch "$CAMPAIGN_DIR/restore-concurrency.armed"
awsc step-20 lambda put-function-concurrency --function-name "$FUNCTION_ARN" --reserved-concurrent-executions 0
awsc step-21 lambda get-function-concurrency --function-name "$FUNCTION_ARN" > "$CASE_DIR/concurrency.zero.json"
jq -e '.ReservedConcurrentExecutions == 0' "$CASE_DIR/concurrency.zero.json" >/dev/null
```

Verify `get-function-event-invoke-config` still has the original OnFailure exact queue, maximum age 300 and retry attempts 0. Leave function code, execution role, environment, trust and SQS SendMessage intact. Create one unique, valid correlation-only input:

```bash
# CASE_ID and CASE_DIR come from begin_failure_case; Case C reuses these
# payload/invoke lines in its own fresh case, without changing its case directory.
jq -n --arg id "$CASE_ID" '{schema_version:1,execution_id:$id}' > "$CASE_DIR/async.payload.json"
date -u +%FT%TZ > "$CASE_DIR/async.started.utc"
awsc step-22 --capture-output "$CASE_DIR/async.invoke.payload" lambda invoke --function-name "$FUNCTION_ARN" --invocation-type Event \
  --payload "fileb://$CASE_DIR/async.payload.json" \
  "$CASE_DIR/async.invoke.payload" > "$CASE_DIR/async.invoke.response.json"
jq -e '.StatusCode == 202' "$CASE_DIR/async.invoke.response.json" >/dev/null
```

`202` proves acceptance only. Require the retained Logs envelope with body `requestPayload.execution_id == CASE_ID`, matching `requestContext.functionArn` (allow its actual `$LATEST` qualifier), nonempty request ID, failure condition and timestamp. Save `approximateInvokeCount`, response context and payload exactly as returned. AWS does not promise a specific condition/reason string for this bypass in the cited guidance; do not hardcode the example's `RetriesExhausted`/count 3 as this case's expectation.

The concurrency-zero rule concerns **new** async events. Ordinary async capacity/system failures can requeue independently of the configured function-error retry count; retry attempts 0 alone is not the bypass mechanism. [Async error handling](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-error-handling.html)

Capture the zero-concurrency readback covering the request, absence of its correlated handler start, the retained destination record, and metrics. `AsyncEventsDropped` includes reserved-concurrency-zero drops; `Invocations`/`Errors` exclude invocation rejection; `DestinationDeliveryFailures` would identify a broken destination. No function error metric is required for a handler that never starts. [Lambda metrics](https://docs.aws.amazon.com/lambda/latest/dg/monitoring-metrics-types.html)

For the normal Scheduler-tick variant, distinguish envelopes by structure: Scheduler DLQ uses SQS message attributes for delivery errors; Lambda OnFailure body contains `requestContext` and `requestPayload`. A successful Scheduler delivery can coexist with failed Lambda async execution. Preserve both correlation layers.

## Case C: stopped consumer, real backlog, and recovery of the original event

Actual `pipe-stopped-f5f816f3ad5b` verified the real stopped-consumer backlog,
visible-queue ALARM and failing doctor, then the original failure message reaching
retained Logs after restart with its pre-restart failure/SQS timestamps intact.
The recovered queue was empty; no correlated handler startup appears in the
complete bounded query. The 177-predicate proof and source hashes are under
`failure-20260916T041545Z-ae5c055f/cases/pipe-stopped-f5f816f3ad5b/transport-proof-01.json`.
**Case C transport and full normal recovery passed.** The 600-second guard
finished and recovery was disarmed; exact original settings and enabled schedule
were restored. Scheduled request `9c6aaa1c-4860-408b-b89c-b433ff8958d3` completed at
`04:34:57.448022126Z`, all 16 alarms were OK and doctor24/24 passed.
`failure-20260916T041545Z-ae5c055f/normal-recovery-proof.json` pins the sources.
Final health after the last executed fault also passed. See the acceptance ledger
for exact IDs/times. The procedure below documents the completed injection;
do not repeat it merely to reproduce a ledger row.


```bash
begin_failure_case pipe-stopped
```

Start with an empty, healthy route, parked schedule, and concurrency zero. Stop the exact Pipe and poll until **CurrentState and DesiredState are STOPPED** before producing a fresh Case B event with a new case ID:

```bash
touch "$CAMPAIGN_DIR/restore-pipe.armed"
awsc step-23 pipes stop-pipe --name "$PIPE_NAME" > "$CASE_DIR/pipe.stop.response.json"
# Poll describe-pipe every 10 seconds, at most 180 seconds, saving every response.
awsc step-24 pipes describe-pipe --name "$PIPE_NAME" > "$CASE_DIR/pipe.stopped.json"
jq -e '.CurrentState == "STOPPED" and .DesiredState == "STOPPED"' "$CASE_DIR/pipe.stopped.json" >/dev/null
# Produce exactly one new async event using Case B, with a distinct CASE_ID.
awsc step-25 sqs get-queue-attributes --queue-url "$QUEUE_URL" --attribute-names All > "$CASE_DIR/queue.stopped.json"
```

Require new queue backlog relative to the empty baseline (`ApproximateNumberOfMessages > 0`; also record not-visible/delayed), preserved failure record absent from the fixed log stream while stopped, and doctor `cleanup_evidence_route` failure. Save exact stopped state and StateReason if supplied. Approximate queue counts may take at least a minute to become consistent after producers stop; use repeated bounded observations, not one zero count as proof of emptiness. [GetQueueAttributes consistency](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/APIReference/API_GetQueueAttributes.html)

Run doctor using the final #48 command/config convention and retain its nonzero exit status. Merged #47 rejects either non-RUNNING state or any queue backlog. Capture the named queue-visible/backlog alarms, which should eventually enter ALARM; the age alarm needs a message older than 300 seconds. A deliberately stopped Pipe has no execution, so **do not require `ExecutionFailed` or `TargetStageFailed` to increase**. Those alarms test errors during execution, not administrative stop.

Repair:

```bash
awsc step-26 pipes start-pipe --name "$PIPE_NAME" > "$CASE_DIR/pipe.start.response.json"
# Poll at 10-second intervals up to 180 seconds for actual RUNNING.
awsc step-27 pipes describe-pipe --name "$PIPE_NAME" > "$CASE_DIR/pipe.restarted.json"
jq -e '.CurrentState == "RUNNING" and .DesiredState == "RUNNING"' "$CASE_DIR/pipe.restarted.json" >/dev/null
```

Then require the **same case ID** in retained Logs, original failure timestamp and SQS `SentTimestamp` predating restart, exact queue/Pipe ARNs, and a source `messageId`. Record CloudWatch `eventId`, log stream, timestamp and ingestion time. Repeated records with the same message ID are delivery duplicates; retain them. After delivery, observe all three queue counts reaching zero and eventual alarm/doctor recovery. A later clean summary is an additional recovery record and must not overwrite the original failure envelope.

Avoid `sqs receive-message` during this test: it becomes a competing consumer and changes visibility/receive counts. Let the Pipe recover the original. SQS messages are hidden when read, deleted after successful processing, and become available again after failed processing and visibility expiration. [Pipes SQS source](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-pipes-sqs.html)

## Finite observation and retained evidence

These are campaign deadlines, not AWS latency guarantees. Every poll should save a uniquely named response and UTC observation time, use the bounded `awsc` wrapper, and fail into restoration on timeout. Poll at 10–15 seconds; use short sleeps rather than an uninterruptible long waiter.

| Gate | Maximum observation window |
|---|---|
| Pipe stop/start actual-state transition | 180 seconds per transition |
| IAM settling before one-time delivery | 120 seconds allowance; actual outcome still decides |
| One-time Scheduler failure envelope | scheduled time + 60 seconds precision + captured 300-second age + 240 seconds transport/observation allowance |
| Direct async concurrency-zero envelope with running Pipe | 300 seconds after accepted request |
| Stopped Pipe backlog and visible alarm | 600 seconds; preserve observation if metric publication is delayed |
| Original event delivery after administrative restart | 300 seconds, provided it was produced after fully STOPPED and no in-flight messages existed |
| Restore quiescence with guarded function | 600 seconds after parking, then diagnose rather than silently retry forever |
| Full scheduled-summary/15-minute health-window recovery | up to 1,500 seconds after approved normal schedule restoration |

For reads, avoid server-side filtering that could hide schema differences. Fetch all events in the exact short window/fixed stream, save raw output, then parse locally:

```bash
# START_MS and END_MS are fixed epoch-millisecond bounds recorded per case.
awsc step-30 logs filter-log-events --log-group-name "$FAILURE_LOG_GROUP" \
  --log-stream-names "$FAILURE_STREAM" --start-time "$START_MS" --end-time "$END_MS" \
  > "$CASE_DIR/failures.window.raw.json"
awsc step-31 logs filter-log-events --log-group-name "$HANDLER_LOG_GROUP" \
  --start-time "$START_MS" --end-time "$END_MS" \
  > "$CASE_DIR/handler.window.raw.json"
jq '[.cleanup.evidence.alarms[] | .name]' "$MANIFEST" > "$CAMPAIGN_DIR/alarm-names.json"
awsc step-32 cloudwatch describe-alarms --alarm-names "file://$CAMPAIGN_DIR/alarm-names.json" \
  > "$CASE_DIR/alarms.observed.json"
awsc step-33 cloudwatch get-metric-statistics --namespace AWS/Lambda --metric-name AsyncEventsDropped \
  --dimensions "Name=FunctionName,Value=$FUNCTION_NAME" --statistics Sum --period 60 \
  --start-time "$START_UTC" --end-time "$END_UTC" > "$CASE_DIR/async-dropped.json"
```

CLI pagination must finish; if the wrapper times out, the incomplete output is not an empty result. Narrow the exact time window and retry into a new file. Keep raw logs with `eventId`, stream, event/ingestion times. A local parser may normalize JSON-string bodies and `stringValue`/`StringValue` for assertions, but raw captures remain immutable. Add SHA-256 files after each finalized capture set. No receipt handles, credentials, presigned URLs or unrelated log groups belong in the published report.

Current alarm mapping to check is exported, not guessed: Scheduler group; Lambda function name; Pipe name; SQS queue name; custom successful completion has namespace `Devbox/Cleanup/<function-name>` and no dimensions. Current queue alarms include visible, not-visible and age; two absence alarms each evaluate three 300-second periods with missing data treated as breaching. Thus doctor can remain unhealthy temporarily after repair until alarms and recent successful completion catch up. Do not invoke `set-alarm-state` to force a pass or erase the failure history.

## What the final acceptance record must say

- Separate **denied delivery**, **actual Scheduler retry-policy exhaustion**, **Lambda async pre-handler destination delivery**, and **consumer stopped/backlog/recovered** results. Record pending gates honestly.
- List exact revision/artifact/manifest hashes and immutable AWS resource IDs, snapshots, injection start/end, restore start/end, errors, all observed retry/condition fields, and fixed evidence windows.
- Link original failure events and separately link recovery summary/alarm/doctor outputs. No worker allocation is necessary for these failure cases.
- Record the synchronous A2 control-plane rejection separately from delivered failures; it proves neither retries nor exhaustion. Reserved concurrency zero on the normal asynchronous target exercises Lambda's independent destination.
- Complete the separate normal scheduled/offline expired-worker campaign and exact-root verification to establish user-facing cleanup behavior. These failure fixtures do not replace that human/offline gate.

The exhaustion gate remains literal: preserve the failed A2 attempt and require actual Scheduler retry/exhaustion evidence from a supported mechanism. Case A permanent denial cannot replace it. The executed campaign is restored. A concrete independent review and user decision are required before changing or deferring the unresolved gate; no further probe is inferred.
