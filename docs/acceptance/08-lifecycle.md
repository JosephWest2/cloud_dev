# Issue #8 validation

Implementation and controlled tests: September 11, 2026.
**Live allocation/cleanup acceptance is pending. No worker was launched during
implementation. Keep issue #8 open until the live results are recorded.**

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

## Live procedure — pause for the user

Use the existing selected test configuration and restricted `devbox-operator`
profile from #7. Authenticate outside devbox. These commands allocate a billable
On-Demand instance until cleanup. Run from this checkout; keep result files outside
Git. `jq` is only used to inspect output in this procedure.

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
This review and automated evidence do not replace the pending live procedure.
