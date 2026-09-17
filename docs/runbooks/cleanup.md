# Scheduled cleanup health and recovery

This runbook describes the v6 foundation's `cleanup.evidence` version 1 capability.
It also retains the controlled failure-test procedures used for #48. Actual
deployment/offline cleanup and failure outcomes are recorded in the
[acceptance ledger](../acceptance/04-expiry.md); literal live Scheduler retry
exhaustion is unproved and deferred to [#58](https://github.com/JosephWest2/cloud_dev/issues/58).
These procedures are not instructions to repeat the completed campaign.
Use the reviewed setup identity for repairs and controlled fault
injection; the operator and health roles intentionally cannot perform them.

## What success means

A healthy run has an acknowledged `invocation_start`, scoped `decision` and
`outcome` records, a service `summary`, and a successful `invocation_end` containing
the final result. An empty scan is `cleanup_no_candidates` and counts as success.
`ok=true`, `complete=true`, `scan_complete=true`, exit 0, no partial/deadline state
and the expected account/region/deployment/owner must agree. An API request for
termination is not proof of termination or root deletion. Inspect the exact
instance and captured root volume IDs independently.

The top-level `run_id` equals the Lambda `request_id`; `event.run_id` identifies
the shared service run. Both appear on service events. `correlation` preserves
Scheduler's schedule ARN, execution ID, scheduled time and attempt number. Input
is correlation only: trusted scope comes from Lambda configuration, and UTC
`event.instance.evaluated_at` / `event.summary.evaluated_at` comes from the service
clock. `invocation_end.result` retains the final scan, counts, errors and partial
outcomes. Configuration/payload/setup failures emit sanitized start/summary/end
records through runtime logging when the AWS journal cannot initialize; an absent
or invalid scope remains empty, never copied from invalid input. Such records are
failures, not successful scans. A hard crash can prevent a terminal record.

The journal bounds calls to five seconds and four concurrent writes. It serializes
an owned event snapshot, sends its complete JSON to CloudWatch Logs and rejects
nil/error/rejected-event responses. A failed `termination_prepared` acknowledgement
prevents that instance's mutation. Its exact mappings precede the final EC2
recheck and termination send. A later logging failure leaves the run incomplete.
[PutLogEvents supports parallel writes and reports rejected events](https://docs.aws.amazon.com/AmazonCloudWatchLogs/latest/APIReference/API_PutLogEvents.html).
No evidence is truncated to report success. Handler logs contain stable codes,
validated IDs and scope; no credentials, workload output or raw SDK errors.

Healthy delivery targets a termination **request** approximately nine minutes
from expiry: five-minute cadence + up to 59 seconds precision + up to 165 seconds
cleanup/API work = 524 seconds. EC2 termination and EBS deletion can take longer.
Failures, backlog, retries or lost evidence remove that timing bound. TTL is not a
hard spending cap. Active sessions and commands can terminate. Already-published
durable results remain available; incomplete jobs stay incomplete. Cleanup never
deletes result storage or launch records.

## Independent pre-handler evidence

Scheduler invokes Lambda asynchronously. Scheduler delivery success proves queue
acceptance, not handler startup or completion. Two producers feed one standard,
SSE-SQS queue: exhausted Scheduler delivery (`DeadLetterConfig`) and Lambda async
`OnFailure` (including startup failures). Delivery retries are two / max age 300s;
Lambda function-error retries are zero / max age 300s. Throttling and system-error
requeues are separately bounded by age; duplicates remain possible.

A RUNNING EventBridge Pipe consumes one message at a time with no filter or
enrichment and sends it to a **CloudWatch Logs target**, fixed stream `failures`.
It preserves `messageId`, implicitly decoded `body`, `messageAttributes` (including
Scheduler diagnostic attributes), `SentTimestamp`, ingestion time, Pipe ARN and
source ARN. It omits receipt handles. The original timestamp is metadata; the
Logs timestamp uses ingestion time so delayed recovery does not submit old events.

The queue retains messages for 14 days, with visibility 1800s. Successful Pipe
processing deletes the transport message; target failures leave it retryable.
Handler and failure log groups default to 30 days, configurable together with
`cleanup_log_retention_days` (7/14/30/60/90/120/150/180/365). Both use `skip_destroy`.
The Pipe creates its fixed stream with exact stream CreateLogStream/PutLogEvents
permission. Terraform does not manage/delete that stream on teardown. Group
retention continues aging records after infrastructure removal. Recover a stopped
route well before the queue's 14-day retention expires. Before deliberate foundation
teardown, drain the queue and retain the failure records; transport retention lasts
only while the queue exists.

The Pipe trusts only its deterministic ARN and account; Scheduler trusts the
schedule **group** ARN and account. Producer SendMessage and Pipe consume grants
name the exact queue. No queue resource policy or wildcard producer grant is needed
for these same-account roles. Pipe diagnostic logging is not the evidence target.

Sources: [Scheduler async invocation](https://docs.aws.amazon.com/lambda/latest/dg/with-eventbridge-scheduler.html),
[Lambda destinations](https://docs.aws.amazon.com/lambda/latest/dg/invocation-async-retain-records.html),
[Pipe SQS retries](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-pipes-sqs.html),
[typed input substitutions](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-pipes-input-transformation.html),
[Logs target](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-pipes-event-target.html),
[AWS CDK stream-name and grantWrite implementation](https://github.com/aws/aws-cdk/blob/main/packages/%40aws-cdk/aws-pipes-targets-alpha/lib/cloudwatch-logs.ts),
[pinned provider 6.64.0](https://github.com/hashicorp/terraform-provider-aws/blob/v6.64.0/website/docs/r/pipes_pipe.html.markdown).
The rendered Terraform template is tested for literal angle brackets and typed
substitution of both failure formats; an offline test is not proof of AWS delivery.

## Read health using the exported manifest

Set these non-secret names from your reviewed export, not copied example IDs:

```bash
manifest=/absolute/path/to/deployment.json
region=$(jq -r .region "$manifest")
function_arn=$(jq -r .cleanup.function.arn "$manifest")
schedule_name=$(jq -r .cleanup.schedule.name "$manifest")
schedule_group=$(jq -r .cleanup.schedule.group_name "$manifest")
queue_url=$(jq -r .cleanup.evidence.queue.url "$manifest")
pipe_name=$(jq -r .cleanup.evidence.pipe.name "$manifest")
handler_group=$(jq -r .cleanup.logs.name "$manifest")
failure_group=$(jq -r .cleanup.evidence.logs.name "$manifest")
health_arn=$(jq -r .cleanup.evidence.health_role.arn "$manifest")
aws configure set profile.devbox-cleanup-health.role_arn "$health_arn"
aws configure set profile.devbox-cleanup-health.source_profile devbox-operator
aws configure set profile.devbox-cleanup-health.role_session_name devbox-cleanup-health
aws configure set profile.devbox-cleanup-health.duration_seconds 900
aws configure set profile.devbox-cleanup-health.region "$region"
```

`devbox doctor --aws-profile devbox-operator --timeout 120s --json` verifies the
health role's exact exported IAM descriptor before assuming it once for 900s,
then uses it only within a bounded cleanup-health check. Top-level manifest roles
remain instance/operator. Health authority is read-only; emergency manual cleanup,
explicit `down`, EC2/EBS observation and result reads retain operator authority and
do not depend on scheduler health. An unsupported/absent evidence descriptor is
reported as unsupported/unhealthy without blocking those emergency operations.

The following reads work under the health profile:

```bash
aws --profile devbox-cleanup-health scheduler get-schedule --name "$schedule_name" --group-name "$schedule_group"
aws --profile devbox-cleanup-health lambda get-function-configuration --function-name "$function_arn"
aws --profile devbox-cleanup-health lambda get-function-concurrency --function-name "$function_arn"
aws --profile devbox-cleanup-health lambda get-function-event-invoke-config --function-name "$function_arn"
aws --profile devbox-cleanup-health pipes describe-pipe --name "$pipe_name"
aws --profile devbox-cleanup-health sqs get-queue-attributes --queue-url "$queue_url" --attribute-names All
aws --profile devbox-cleanup-health logs describe-log-groups --log-group-name-prefix "$handler_group"
aws --profile devbox-cleanup-health logs describe-log-groups --log-group-name-prefix "$failure_group"
aws --profile devbox-cleanup-health logs describe-log-streams --log-group-name "$failure_group" --log-stream-name-prefix failures
aws --profile devbox-cleanup-health logs describe-metric-filters --log-group-name "$handler_group" --filter-name-prefix successful-completion
jq '{AlarmNames: [.cleanup.evidence.alarms[].name]}' "$manifest" > /tmp/cleanup-alarm-read.json
aws --profile devbox-cleanup-health cloudwatch describe-alarms --cli-input-json file:///tmp/cleanup-alarm-read.json
```

Check enabled schedule, exact target/role/input/retries; Lambda Active/Successful,
reviewed code/runtime/environment, concurrency 1 and exact async destination;
Pipe desired **and current** RUNNING, `StateReason`, exact role/source/target,
stream/template, batch 1 and no filter/enrichment; encrypted standard queue,
retention/visibility and visible/in-flight/delayed backlog; both log retentions.
Doctor reports a missing stream as unverified until a controlled failure has
materialized the target stream; #48 records this for the accepted deployment.
`skip_destroy` is a Terraform lifecycle setting,
not an AWS log-group attribute; verify it in the reviewed configuration/state.

All sixteen exported alarms must be configured correctly and OK. Scheduler uses
only the dedicated `ScheduleGroup` dimension: InvocationAttemptCount absence,
TargetErrorCount, InvocationDroppedCount and InvocationsFailedToBeSentToDeadLetterCount.
Lambda uses `FunctionName`: Errors, Throttles, AsyncEventsDropped and
DestinationDeliveryFailures. Pipe uses `PipeName`: ExecutionFailed,
ExecutionTimeout, ExecutionPartiallyFailed and TargetStageFailed. Queue uses
`QueueName`: visible/in-flight backlog and ApproximateAgeOfOldestMessage (>300s).
Counter failure alarms use Sum >0 in 60s; backlog uses Maximum. Missing failure
counters are nonbreaching. Delivery absence and successful completion absence
alarm on three 5-minute periods below 1, missing data breaching. Alarms are
inspectable state signals; this deployment does not configure notification recipients.

The deployment-specific `Devbox/Cleanup/<function-name>` / SuccessfulCompletion
metric comes only from successful terminal records, including no-candidate runs.
Doctor also checks actual recent scope-correlated logs; zero errors or OK alarms
alone cannot establish health. Missing/error/partial terminal results fail health.
AWS metrics are approximate and can lag. Sources:
[Scheduler metrics](https://docs.aws.amazon.com/scheduler/latest/UserGuide/monitoring-cloudwatch.html),
[Lambda metrics](https://docs.aws.amazon.com/lambda/latest/dg/monitoring-metrics-types.html),
[Pipe dimensions](https://docs.aws.amazon.com/eventbridge/latest/userguide/eb-pipes-monitoring.html),
[SQS metrics](https://docs.aws.amazon.com/AWSSimpleQueueService/latest/SQSDeveloperGuide/sqs-available-cloudwatch-metrics.html),
[JSON boolean filter syntax](https://docs.aws.amazon.com/AmazonCloudWatch/latest/logs/FilterAndPatternSyntax.html).

Read all exported alarm metrics with their exact dimensions (health IAM grants
regional GetMetricStatistics because AWS does not support metric-level resource
ARN scoping). This includes delivery, runtime, route and recent-success evidence:

```bash
metric_begin=$(date -u -d '30 minutes ago' +%Y-%m-%dT%H:%M:%SZ)
metric_end=$(date -u +%Y-%m-%dT%H:%M:%SZ)
metric_dir=$(mktemp -d /tmp/cleanup-metrics.XXXXXX)
jq -r '.cleanup.evidence.alarms | keys[]' "$manifest" |
while IFS= read -r metric_key; do
  jq --arg key "$metric_key" --arg begin "$metric_begin" --arg end "$metric_end" '
    .cleanup.evidence.alarms[$key] |
    {Namespace:.namespace, MetricName:.metric_name,
     Dimensions:(.dimensions|to_entries|map({Name:.key,Value:.value})),
     StartTime:$begin, EndTime:$end, Period:.period, Statistics:[.statistic]}
  ' "$manifest" > "$metric_dir/request.json"
  aws --profile devbox-cleanup-health cloudwatch get-metric-statistics \
    --cli-input-json "file://$metric_dir/request.json" > "$metric_dir/$metric_key.json"
done
```

Inspect recent events and retain their event IDs, timestamps and stream names:

```bash
since_ms=$(date -u -d '20 minutes ago' +%s000)
aws --profile devbox-cleanup-health logs filter-log-events --log-group-name "$handler_group" --start-time "$since_ms" --filter-pattern '{ $.kind = "invocation_end" }' > /tmp/cleanup-completions.json
jq '.events[] | {eventId,logStreamName,timestamp,record:(.message|fromjson)}' /tmp/cleanup-completions.json
aws --profile devbox-cleanup-health logs filter-log-events --log-group-name "$failure_group" --log-stream-names failures --start-time "$since_ms" > /tmp/cleanup-failures.json
jq '.events[] | {eventId,logStreamName,timestamp,record:(.message|fromjson)}' /tmp/cleanup-failures.json
```

Select the latest terminal invocation within 15 minutes for exact exported scope
and schedule ARN, with consistent evaluation/completion times. Correlate failure
`body.requestContext.requestId` with Lambda invocation ID; correlate Scheduler
attributes and body execution ID with schedule context. Retain message ID and
SentTimestamp to distinguish delivery from delayed ingestion and deduplicate
retries. For one exact instance use `--filter-pattern` constructed from its validated
ID, e.g. `{ $.event.instance.instance_id = "i-0123456789abcdef0" }`. Follow the
`termination_prepared` root mapping through that invocation's `outcome` and final
result; later EC2 scans may no longer expose historical root mappings.

Console equivalents: Scheduler → dedicated group → schedule configuration;
Lambda → function → Monitor and Configuration → asynchronous invocation/concurrency;
EventBridge → Pipes → pipe details/current state; SQS → queue monitoring;
CloudWatch → exact log groups and Alarms → exported alarm names. Use the health
profile's exact CLI reads if console list calls exceed its intentionally narrow IAM.

## Recover and prove a later successful run

| Symptom | Evidence and repair |
| --- | --- |
| Disabled schedule | State DISABLED and delivery/no-success alarms. Confirm reviewed enabled-state intent; re-enable through the reviewed foundation after manual reconciliation. |
| Bad invoke/trust permissions | Scheduler TargetErrorCount, drops, DLQ record attributes. Restore exact Lambda InvokeFunction/queue SendMessage and schedule-group trust; inspect DLQ-delivery-failure alarm too. |
| Handler startup/runtime failure | Lambda Errors and retained async destination envelope, possibly no handler start. Restore reviewed bootstrap ZIP/runtime/environment/role. DestinationDeliveryFailures means inspect queue permission/size/config independently. |
| Throttling | Lambda Throttles, concurrency drift and AsyncEventsDropped. Restore reserved concurrency 1 and async age 300; zero function retries does not disable system-error/throttle requeues. |
| Termination denied/temporary API error | Exact outcome codes `termination_denied` / `termination_unresolved`; failed or partial summary. Repair exact tag-scoped IAM or temporary service issue; a later fresh scan safely retries. |
| Root deletion unverified/retained | `root_volume_unverified`, `root_volume_retained` or `volume_unresolved`, captured root ID/flags and observation status. Independently inspect exact EC2/EBS IDs. Do not infer deletion from instance absence or fabricate missing mappings. |
| Pipe stopped/broken | DescribePipe currentState/StateReason, failure/timeout metrics, queue visible/in-flight backlog and age. Repair exact queue-consume / stream-write permissions, target/template or state; restart and prove retained delivery before messages expire. A quiet stopped route may have zero error metrics: the explicit state read is required. |

During any outage, use the operator profile and the independently validated manual
service. Preview and then reconcile policy-eligible workers; `down` remains a
separate explicit-ID operation with its own confirmation semantics:

```bash
devbox cleanup --aws-profile devbox-operator --dry-run --json
devbox cleanup --aws-profile devbox-operator --json
# Choose these IDs from retained evidence, not a guessed regional volume scan:
instance_id=i-0123456789abcdef0
root_volume_id=vol-0123456789abcdef0
aws --profile devbox-operator --region "$region" ec2 describe-instances --instance-ids "$instance_id"
aws --profile devbox-operator --region "$region" ec2 describe-volumes --volume-ids "$root_volume_id"
# For explicit recovery of an independently verified worker:
devbox down "$instance_id" --aws-profile devbox-operator
```

An exact EC2 terminal state plus exact root `InvalidVolume.NotFound` (no contradictory
response) or verified deleted state establishes disposal. Preserve retained/unknown
roots as unresolved; do not manually delete guessed EBS volumes. Existing result
retrieval commands remain independent. After repairing configuration, review/apply
with setup, export the manifest and enable the intended schedule. Require a **later**
successful scheduled terminal record and cleared alarms/backlog; earlier success
before the outage is insufficient. Retain the failure and recovery event IDs.

<a id="future-48-controlled-live-demonstrations-not-executed"></a>

## Controlled live failure procedures

This preserves the original #48 procedure, including cases that were not proved
live. Consult the [acceptance ledger](../acceptance/04-expiry.md) for the final
case-by-case outcomes and #58 for deferred literal retry exhaustion. The historical
heading anchor above remains for links from earlier implementation records.

Run only after migration review, in the acceptance deployment with no valuable
active work. Use `devbox-setup` for deliberate faults and repairs; health reads stay
under the health profile. Preserve local files in a private evidence directory.
Do one fault at a time and restore it before proceeding. Do not delete/recreate the
queue or retained groups, and do not consume SQS messages manually.

```bash
umask 077
case_dir=$(mktemp -d /tmp/cleanup-acceptance.XXXXXX)
aws --profile devbox-setup --region "$region" scheduler get-schedule --name "$schedule_name" --group-name "$schedule_group" > "$case_dir/schedule-before.json"
jq 'del(.Arn,.CreationDate,.LastModificationDate)' "$case_dir/schedule-before.json" > "$case_dir/schedule-restore.json"
jq '.State="DISABLED"' "$case_dir/schedule-restore.json" > "$case_dir/schedule-disabled.json"
jq '.State="ENABLED"' "$case_dir/schedule-restore.json" > "$case_dir/schedule-enabled.json"
```

1. **Denied Scheduler delivery without handler startup.** Save the existing
   scheduler inline policy and add an exact function Deny while retaining queue
   SendMessage. Enable the schedule for at least one attempt and its bounded
   retries/age; inspect failures and alarms above. Record the Scheduler execution
   ID, ERROR_CODE/ERROR_MESSAGE attributes, SQS messageId, SentTimestamp and retained
   Logs event ID. Verify no corresponding handler start exists. Save observations
   before restoring the policy. Delivery can fail immediately rather than using
   all retries depending on the AWS error category. Do not call this retry
   exhaustion unless the record contains `EXHAUSTED_RETRY_CONDITION`; permanent
   errors can omit it. Keep literal retry exhaustion pending until case 1b passes.

```bash
scheduler_role=$(jq -r '.cleanup.scheduler_role.arn|split("/")[-1]' "$manifest")
scheduler_policy=$(jq -r .cleanup.scheduler_role.policy_name "$manifest")
aws --profile devbox-setup iam get-role-policy --role-name "$scheduler_role" --policy-name "$scheduler_policy" --query PolicyDocument --output json > "$case_dir/scheduler-policy.json"
jq --arg arn "$function_arn" '.Statement += [{Effect:"Deny",Action:"lambda:InvokeFunction",Resource:$arn}]' "$case_dir/scheduler-policy.json" > "$case_dir/scheduler-denied.json"
aws --profile devbox-setup iam put-role-policy --role-name "$scheduler_role" --policy-name "$scheduler_policy" --policy-document "file://$case_dir/scheduler-denied.json"
aws --profile devbox-setup --region "$region" scheduler update-schedule --cli-input-json "file://$case_dir/schedule-enabled.json"
# Observe one retained Scheduler failure and alarms; then restore immediately:
aws --profile devbox-setup --region "$region" scheduler update-schedule --cli-input-json "file://$case_dir/schedule-disabled.json"
aws --profile devbox-setup iam put-role-policy --role-name "$scheduler_role" --policy-name "$scheduler_policy" --policy-document "file://$case_dir/scheduler-policy.json"
```

**1b. Literal Scheduler retry-policy exhaustion.** Restore the original Scheduler
policy first; leave no Deny in place. Park the schedule, save concurrency and set
it to zero. Temporarily use the same schedule/role/function/DLQ with universal
Lambda Invoke in synchronous RequestResponse mode, retaining the 2-retry/300s
policy. Unlike the normal async target, this can return a retryable concurrency
429 before any handler starts. This composition is an **inference from supported
APIs**; its actual retry classification must be observed, not assumed.
Sources: [universal Lambda target](https://docs.aws.amazon.com/scheduler/latest/UserGuide/managing-targets-universal.html),
[Invoke concurrency errors](https://docs.aws.amazon.com/lambda/latest/api/API_Invoke.html),
[Scheduler DLQ exhaustion attributes](https://docs.aws.amazon.com/scheduler/latest/UserGuide/configuring-schedule-dlq.html).

```bash
aws --profile devbox-setup --region "$region" scheduler update-schedule --cli-input-json "file://$case_dir/schedule-disabled.json"
aws --profile devbox-setup --region "$region" lambda get-function-concurrency --function-name "$function_arn" > "$case_dir/concurrency-before.json"
jq -e '.ReservedConcurrentExecutions == 1' "$case_dir/concurrency-before.json"
aws --profile devbox-setup --region "$region" lambda put-function-concurrency --function-name "$function_arn" --reserved-concurrent-executions 0
aws --profile devbox-setup --region "$region" lambda get-function-concurrency --function-name "$function_arn" > "$case_dir/concurrency-zero.json"
jq -e '.ReservedConcurrentExecutions == 0' "$case_dir/concurrency-zero.json"
case_at=$(date -u -d '3 minutes' +%Y-%m-%dT%H:%M:%S)
python3 - "$case_dir" "$function_arn" "$case_at" <<'PYCODE'
import json, pathlib, sys
p = pathlib.Path(sys.argv[1])
schedule = json.loads((p / 'schedule-disabled.json').read_text())
target = schedule['Target']
target['Arn'] = 'arn:aws:scheduler:::aws-sdk:lambda:invoke'
target['Input'] = json.dumps({'FunctionName': sys.argv[2],
    'InvocationType': 'RequestResponse', 'Payload': target['Input']})
# Python preserves literal angle brackets, including within the Payload string.
schedule.update(ScheduleExpression='at(' + sys.argv[3] + ')',
    ScheduleExpressionTimezone='UTC', State='ENABLED', ActionAfterCompletion='NONE')
(p / 'schedule-sync-once.json').write_text(json.dumps(schedule) + '\n')
PYCODE
aws --profile devbox-setup --region "$region" scheduler update-schedule --cli-input-json "file://$case_dir/schedule-sync-once.json"
```

Retain the actual Scheduler envelope for this one-time instant, exact function,
RequestResponse input and universal target ARN. Require a throttling error and
`EXHAUSTED_RETRY_CONDITION` of `MaximumRetryAttempts` or
`MaximumEventAgeInSeconds`; capture `RETRY_ATTEMPTS` and require a positive count
to claim retries occurred. Do not infer two completed retries from the configured
limit. Observe through scheduled time + 60s precision + 300s age + 240s transport
allowance, using bounded reads. A permanent rejection, missing record or different
classification leaves this gate pending. Do not relabel the Deny or Lambda async
failure as this result. Restore the parked original target while concurrency is
still zero; allow attempts to settle before restoring concurrency and the saved
schedule state. Preserve the restore files for interruption recovery.

```bash
aws --profile devbox-setup --region "$region" scheduler update-schedule --cli-input-json "file://$case_dir/schedule-disabled.json"
# After the one-time retry window has settled, restore the verified original 1:
aws --profile devbox-setup --region "$region" lambda put-function-concurrency --function-name "$function_arn" --reserved-concurrent-executions 1
```

2. **Lambda startup failure independent of handler code.** With the schedule
   disabled, preserve the reviewed `bin/devbox-cleanup-linux-amd64.zip` whose digest
   matches the export. Upload a deliberate fixture ZIP with no `bootstrap`. Wait
   for update completion, enable one scheduled attempt, and observe the async
   OnFailure envelope in the fixed failure stream. Require Lambda request ID,
   condition/error type, original schedule context, SQS messageId and retained Logs
   event ID, and absence of a handler start for that request. Scheduler delivery
   success alongside this failure demonstrates why both producers are required.

```bash
cp bin/devbox-cleanup-linux-amd64.zip "$case_dir/cleanup-reviewed.zip"
python3 - "$manifest" "$case_dir/cleanup-reviewed.zip" "$case_dir/no-bootstrap.zip" <<'PY'
import hashlib, json, sys, zipfile
manifest = json.load(open(sys.argv[1]))
assert hashlib.sha256(open(sys.argv[2], 'rb').read()).hexdigest() == manifest['cleanup']['function']['code_sha256']
with zipfile.ZipFile(sys.argv[3], 'w') as archive:
    archive.writestr('acceptance-fixture.txt', 'Controlled missing-bootstrap startup failure; restore reviewed ZIP.')
PY
aws --profile devbox-setup --region "$region" lambda update-function-code --function-name "$function_arn" --zip-file "fileb://$case_dir/no-bootstrap.zip"
aws --profile devbox-setup --region "$region" lambda wait function-updated-v2 --function-name "$function_arn"
aws --profile devbox-setup --region "$region" scheduler update-schedule --cli-input-json "file://$case_dir/schedule-enabled.json"
# Observe retained startup failure, then disable and restore before further work:
aws --profile devbox-setup --region "$region" scheduler update-schedule --cli-input-json "file://$case_dir/schedule-disabled.json"
aws --profile devbox-setup --region "$region" lambda update-function-code --function-name "$function_arn" --zip-file "fileb://$case_dir/cleanup-reviewed.zip"
aws --profile devbox-setup --region "$region" lambda wait function-updated-v2 --function-name "$function_arn"
```

3. **Route outage detection and recovery.** Stop the Pipe. Submit a deliberately
   invalid, non-authorizing async payload to the restored handler; its decoder
   rejects unknown fields before service construction. Observe async failure queue
   backlog/age, stopped state and doctor failure, without consuming the message.
   Restart the Pipe, wait for currentState RUNNING and eventual retention (visibility
   can delay redelivery up to 1800s), and correlate the unique case marker in the
   retained Lambda destination `body.requestPayload`. Confirm backlog drains and
   alarms clear. This demonstrates that stopped-route silence is not health.

```bash
case_id="route-$(date -u +%Y%m%dT%H%M%SZ)"
aws --profile devbox-setup --region "$region" pipes stop-pipe --name "$pipe_name"
# Poll describe-pipe with bounded reads until CurrentState and DesiredState are STOPPED.
aws --profile devbox-cleanup-health pipes describe-pipe --name "$pipe_name"
jq -n --arg id "$case_id" '{schema_version:1,acceptance_case:$id}' > "$case_dir/invalid-input.json"
aws --profile devbox-setup --region "$region" lambda invoke --function-name "$function_arn" --invocation-type Event --payload "fileb://$case_dir/invalid-input.json" "$case_dir/invoke-response.json"
# Read stopped state, backlog, age and alarms; retain their timestamps.
aws --profile devbox-setup --region "$region" pipes start-pipe --name "$pipe_name"
# Read currentState/StateReason, retained failure record and drained backlog.
```

4. Restore the saved schedule state, then use the reviewed migration to enable
   unattended operation if approved. Wait for a later scheduled success, including
   a no-candidate run, and at least three healthy 5-minute periods. Re-run doctor,
   metric/log/retention reads and independent exact-ID EC2/EBS observations. Keep
   a new acceptance campaign incomplete if required route, root deletion or
   laptop-offline evidence is missing; do not reinterpret #48's recorded outcomes.

```bash
aws --profile devbox-setup --region "$region" scheduler update-schedule --cli-input-json "file://$case_dir/schedule-restore.json"
```

Record account/region/scope, reviewed commit and export digest, UTC fault/repair
intervals, exact IDs, CLI outputs, CloudWatch log group/stream/event IDs and query
windows, alarm states before/during/after, queue retention and group retention.
Store failure envelopes privately: AWS-generated diagnostic attributes may contain
service diagnostics. Do not paste arbitrary workload input or credentials into
injection payloads. No live evidence links or successful delivery claims should be
invented from the controlled tests in this repository.
