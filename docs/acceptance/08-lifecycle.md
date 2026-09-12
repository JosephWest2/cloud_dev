# Issue #8 validation

Implementation and controlled tests: September 11, 2026.
**Issue #8 live allocation/cleanup acceptance is complete.** The operator-profile
launch, new-terminal rediscovery, termination, root-volume deletion and repeated
teardown all passed on September 11, 2026 (September 12 UTC). The test worker is
terminated and its root volume is deleted. SSH/readiness acceptance belongs to #9.

## Controlled evidence

`make check` covers formatting, module checksums, build, vet and tests.
`go test -race ./internal/lifecycle ./internal/cli` exercises lifecycle concurrency.
Tests cover scoped pagination; unmanaged/wrong-owner/wrong-deployment/wrong-account
and ambiguous-name rejection; scope changes before termination; a lost termination
response; bounded waits retaining instance/volume IDs; deletion versus retained or
unavailable root evidence; lost launch response and temporary inventory invisibility;
restart recovery; original token/parameters; changed prepared-request parameters;
concurrent resumes; canceled file-lock acquisition; malformed receipts; the crash
before send with a dispatch marker; output failure before mutation; receipt-save
failure after allocation; cleanup without manifests/profiles/receipts; invalid
configuration/identity before allocation; and parseable, sanitized JSON failures.

The prior foundation's infrastructure is unchanged. Its validation and deployed
resource evidence remains in [#7 acceptance](07-foundation.md). This slice requires
no infrastructure apply. Controlled tests cannot establish live IAM authorization,
EC2 token behavior, root deletion, or bootstrap/SSM readiness.

## Live acceptance procedure

Use the existing selected test configuration and restricted `devbox-operator`
profile from #7. Authenticate outside devbox. These commands allocate a billable
On-Demand instance until cleanup. Run from this checkout; keep result files outside
Git.

```sh
make build
./bin/devbox doctor --aws-profile devbox-operator --timeout 60s --json
./bin/devbox ls --aws-profile devbox-operator --json
./bin/devbox up agent --on-demand --name smoke --aws-profile devbox-operator --timeout 5m --json > /tmp/devbox-up.json
cat /tmp/devbox-up.json
```

Stop if doctor fails. If up fails, retain the result and the stderr request ID;
**do not issue another fresh up as a retry**. Use `up --resume REQUEST_ID` with the
same config/profile to reconcile. An allocation with a readiness value of
`not_observed` is expected in #8. Inspect image, instance type, `market=on-demand`,
exact template ID/version, EC2 state, root device/volume ID, and deletion flag.

Open a new terminal (or restart the local CLI environment). `ls` must rediscover
the machine with the same configuration/profile, without loading any request
receipts or cached instance IDs. Save the inventory for comparison:

```sh
./bin/devbox ls --aws-profile devbox-operator --json > /tmp/devbox-ls.json
cat /tmp/devbox-ls.json
./bin/devbox down smoke --aws-profile devbox-operator --timeout 5m --json > /tmp/devbox-down.json
cat /tmp/devbox-down.json
./bin/devbox down smoke --aws-profile devbox-operator --timeout 5m --json
```

For an ambiguous name, use the explicit instance ID from up/ls for cleanup of
each intended worker. Expect the first teardown to report `status=terminated`,
`ec2_state=terminated`, and `root_volume_deletion=deleted`. A repeated teardown may
report `already_terminated` while AWS still exposes it, or `no_managed_match`
after inventory expires. Neither repeats previously observed deletion evidence.

If down times out or cannot verify deletion, preserve its instance and volume
IDs. Retry `down INSTANCE_ID --timeout 5m` and verify the saved root volume with
an independent API call (replace these placeholders with the saved IDs):

```sh
aws ec2 describe-instances --profile devbox-operator --region us-east-2 --instance-ids INSTANCE_ID --query 'Reservations[].Instances[].{ID:InstanceId,State:State.Name,Root:RootDeviceName,Volumes:BlockDeviceMappings}'
aws ec2 describe-volumes --profile devbox-operator --region us-east-2 --volume-ids ROOT_VOLUME_ID
```

`InvalidVolume.NotFound` for that exact saved volume is deletion evidence.
`InvalidInstanceID.NotFound` alone is not evidence of termination or volume deletion.
A volume still present must be investigated; devbox never deletes a volume directly.
If necessary, scoped manual cleanup is `aws ec2 terminate-instances --profile
 devbox-operator --region us-east-2 --instance-ids INSTANCE_ID`, then repeat the
independent observations above. Verify account and tags before manual mutation.

Report the up/ls/down results and any errors, excluding credentials. Record the
actual test date, chosen scope locally, observed identities/pins/market, restart
rediscovery, termination/deletion, and repeat-teardown outcome here before closing
#8. SSH/bootstrap readiness remains #9's acceptance gate.

## Independent PR review

A separate agent reviewed PR #13 after creation and found one teardown race:
the fresh scope-validation read could omit EBS mappings after another caller
started termination, discarding root-volume IDs captured in the initial lookup.
The fix retains previously captured mappings while preferring fresh flags for
matching volume IDs. Controlled cases cover both terminated and shutting-down
revalidation responses without mappings. The reviewer rechecked the fix and
reported no remaining actionable findings.

Additional tests exercise the pinned EC2 SDK's real request serialization and
503 retry (unchanged token and full request), process-to-process lock contention
after atomic receipt replacement, and duplicate request IDs, unexpected tokens,
concurrent friendly-name collisions, and terminated-request replay. Final
`make check` and `go test -race ./internal/lifecycle ./internal/cli` pass.
These controlled checks complement the live results recorded below.

## Initial rejected attempt — September 11, 2026 (September 12 UTC)

After refreshing the browser login for the setup profile, all 12 doctor checks
passed under `devbox-operator`. The first live On-Demand request for `smoke`
selected the manifest's pinned AMI/template and `c7i.2xlarge`.

CloudTrail records the original RunInstances call at **2026-09-12 01:53:25 UTC**
with **Client.PendingVerification**. AWS reported that regional account validation
was in progress and would be confirmed by email, normally within minutes but
potentially taking four hours; unresolved validation requires AWS Support.
No matching instance was found through scoped request reconciliation or a separate
EC2 client-token inventory query. The exact SDK request with `DryRun=true` returned
`DryRunOperation`, establishing permission-check success without allocating.
Regional Standard On-Demand quota was 32 vCPUs; this was not a quota rejection.
No follow-up allocation was attempted while verification was pending, and no
permissions were broadened. The original receipt remains dispatched. After the
user received AWS confirmation, scoped inventory was empty and reconciliation
found no original request match. The separately confirmed CloudTrail rejection
and completed verification justified a new request for the successful test below.

The live failure exposed a diagnostic gap: PendingVerification was reduced to an
unresolved outcome. The CLI now preserves this specific allowlisted service code
in the receipt and gives static account-verification guidance after bounded
reconciliation. Restart keeps the original diagnostic; AWS inventory takes
precedence if an instance appears. Arbitrary service codes/messages are neither
stored nor echoed. The receipt remains dispatched and cannot launch again.
The original receipt predates this fix and is intentionally not manually edited;
its rejection evidence comes from CloudTrail. Controlled original/restart and
redaction tests cover the new behavior. The completed lifecycle results follow.

The independent reviewer also rechecked this diagnostic fix and found no actionable
issues. `make check`, lifecycle/CLI race tests, and `make build` pass after the fix.


## Completed live lifecycle — September 11, 2026 (September 12 UTC)

The user ran the workflow under `devbox-operator` in the existing Ohio foundation
and supplied the JSON results. No changes to the selected account, deployment,
owner, infrastructure or operator permissions were needed after AWS verification.

| Observation | Result |
| --- | --- |
| Doctor | All 12 local, identity and deployed-foundation checks passed |
| New launch | `up agent --on-demand --name smoke --timeout 5m --json` returned `allocated`, exit 0 |
| Request | `8536eaf9e14db5dca5df8f3a4fd3f923`, created `2026-09-12T03:24:41.563592553Z` |
| Instance | `i-03552eeeb6ee46dc6`, profile `agent`, name `smoke` |
| Exact image | `ami-00adec9774170bad2` |
| Exact template | `lt-0ef8072b1bcc495b1`, numeric version `1` |
| Type and actual market | `c7i.2xlarge`, `on-demand` |
| Restart rediscovery | In a new terminal, `ls --json` found the same instance/request/pins and reported `running`, exit 0 |
| Root mapping | `/dev/sda1`, `vol-0dd89fc7073abd22d`, root=true, delete_on_termination=true |
| First teardown | `down smoke --timeout 5m --json` returned `terminated`, EC2 state `terminated`, root deletion `deleted`, exit 0 |
| Repeated teardown | The same command returned `already_terminated`, EC2 state `terminated`, exit 0 |

The initial pending launch response had no root mappings and honestly reported
`root_volume_deletion=unavailable`. The new-terminal inventory supplied the root
volume ID. First teardown retained that ID and explicitly verified deletion.
Repeated teardown no longer received block-device mappings from EC2 and returned
`root_volume_deletion=unavailable` with an empty volume list. That describes the
later observation; it does not retract the earlier verified deletion or falsely
claim new deletion evidence. The controlled no-match test separately covers the
case after terminated inventory disappears.

Each invocation emitted parseable JSON; the request receipt announcement remained
on stderr. `ls` obtains inventory from AWS and does not read receipts or cached
instance IDs. SSM, bootstrap and readiness stayed `not_observed`, as expected for
#8; no SSH/readiness result is claimed. The durable foundation is retained for #9.
