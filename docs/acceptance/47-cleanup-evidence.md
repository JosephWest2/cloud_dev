# Issue #47: retained cleanup evidence

Status: controlled implementation and offline verification only. No AWS
credentials, live AWS calls, infrastructure apply, queue messages, log links or
laptop-offline results are claimed. Final live acceptance remains #48.

Implementation adds invocation start/end and safe initialization-failure summaries
to the shared service's decision/prepared/outcome/summary stream. The CloudWatch
journal acknowledges exact mappings before dispatch, limits concurrent writes and
fails closed on timeout/rejection. Tests cover empty scans, partial/deadline/error
terminal records, safe initialization errors and a transient termination failure
followed by a successful later run with verified disposable root deletion.

Both Scheduler DLQ and Lambda async OnFailure target a standard SSE-SQS queue.
An independent batch-1 RUNNING Pipe targets retained Logs, preserving diagnostic
attributes, body and exact IDs. Actual OpenTofu-rendered templates are checked for
literal placeholders, then exercised with Scheduler and Lambda failure fixtures.
No offline substitution test substitutes for AWS's runtime behavior.

The export/read tests cover typed evidence version 1 within manifest 6; exact
trust/policies/destinations, route state/settings/backlog, both log retentions,
success filters and all supported metric dimensions. Negative fixtures reject
missing/partial/foreign-scope success, route/metric drift and unsupported exports.
Health-only credentials are bounded and do not replace the operator configuration.
IAM checks retain one inline policy per role and the aggregate 10240-byte guard;
realistic three-AZ and maximum-label fixtures account for actual scoped ARN lengths.

Offline checks: relevant Go tests and race tests, `make check`, `make build`,
`make cleanup-check`, and `make infra-check TOFU=/tmp/devbox-tools/tofu` (backend
false, mock providers, pinned AWS provider 6.64.0, real export-to-Go validation).
Final offline checks passed on the integrated main `73076ea2a5332bcb1ca41b37f253f9165d50f65f` plus this implementation: all Go packages, the eight-package relevant race suite, build and deterministic cleanup packaging; OpenTofu bootstrap 1 test and foundation 28 tests plus the actual export bridge. The realistic three-AZ operator policy is 9,635 bytes; the maximum-label fixture correctly rejects aggregate quota overflow. The implementation handoff records the exact commit hash.

## #48 evidence still required

Follow [the recovery runbook](../runbooks/cleanup.md#future-48-controlled-live-demonstrations-not-executed).
Distinguish permanent Scheduler denial from observed retry-policy exhaustion
(case 1b); require the actual exhaustion attribute for the latter.
Independently retain both exhausted Scheduler delivery and missing-bootstrap Lambda
failure records; demonstrate a stopped Pipe, backlog detection, repair and delayed
retained delivery; then prove a later successful scope-correlated scheduled run.
Capture exact CloudWatch event IDs/query windows, all retry/state/retention settings,
alarm transitions, queue drain and independent instance/root-volume observations.
Keep the separate laptop-offline/expiry/results acceptance gates open until real
observations pass. TTL remains a best-effort cleanup policy, not a spending cap.
