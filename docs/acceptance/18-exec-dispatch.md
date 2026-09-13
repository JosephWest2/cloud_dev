# Issue #18 scoped exec live acceptance

This records actual CLI dispatch and independent publication for parent #2.
Detailed SSM observation and Ctrl-C process races belong to #19; the `logs`
interface and its byte-verifying exports belong to #20. This is not the final
parent acceptance gate in #21.

## Source and deployment

The final tested source is `21ad872`, built in a separate clean checkout. Its CLI
SHA-256 is `1a7879dfa0d3b00cd0965b997af69e0d1468ed993e978643b74ee00c91689e36`.
The runner SHA-256 is
`e21c0536522661235b6ea5cb22bfe3b9bd66081af640570f7a151f51db8a74f6`.
Fresh non-implementing subagents reviewed dispatch, CLI integration, configuration,
final observation and the live harness. The user selected detachment on Ctrl-C
and 30-day result retention, and authorized the infrastructure and scoped live
checks following the cost estimate.

Live testing found two issues before the final run. The isolated test PATH
initially omitted `sh`, which the SDK requires to launch the AWS credential
process. That attempt failed identity verification before any resource client,
request publication or dispatch. The reviewed helper correction keeps only
`sh` and `aws` in PATH; local SSH keys, workload profiles, SSH and Session Manager
Plugin remain absent for every exec case.

The next attempt submitted once, but the runner began at `22:16:09.707Z` while
the request's S3 LastModified was `22:16:10Z`. Its future-timestamp check reported
`runner_request_expired` before claiming or starting the workload. SSM recorded
wrapper failure; exact-prefix inspection found only request and acknowledgement.
The baseline CLI retained both IDs and reported its separate observation timeout.
Both failed attempts remain in private evidence; neither was blindly replayed.

The runner correction waits for the authoritative submission instant under its
existing preparation deadline, then rechecks true retention expiry before its
single start claim. It preserves the exact submission and expiry timestamps.
Controlled tests cover the observed 293 ms lead, a longer lead, cancellation,
preparation deadline and genuine expiry, with no premature or repeated claim.
A separate non-implementing reviewer approved the final correction.

The first temporary worker was terminated, its captured original root volume
returned `InvalidVolume.NotFound`, and independent scoped inventory showed zero
nonterminated workers and volumes before the artifact update. The actual reviewed
plan replaced one content-addressed runner object, rotated its exact ARN in both
IAM policies, and updated the launch template's three runner pins. Apply succeeded
with one addition, three updates and removal of the previous artifact. A real
follow-up plan returned exit 0 with no changes. Result storage, retention, SSM
documents, networking and the state backend did not change.

The fresh export passed all 14 doctor checks under `devbox-operator`. One new
On-Demand worker reached running, SSM online, bootstrap complete and readiness
ready on numeric template version 4. Independent inspection matched its exact
launch request, scope, AMI and AWS-managed template tags, captured its root volume,
and verified required IMDSv2, an encrypted 100 GiB gp3 root with deletion on
termination, and zero ingress on every attached security group.

## Live command and retained recovery gate

The first nine refreshed cases passed. They covered literal arguments, exits
1/2/4/255, a missing executable (127), invalid cwd, binary streams and true empty
streams. The tenth case passed its actual SIGKILL checks immediately after
acknowledgement: all local process groups stopped and no local JSON finalizer ran.
Its independent reader then encountered a publication race: GET returned
`NoSuchKey`, but an authenticated listing found the result published between the
two calls. The helper incorrectly treated that as ambiguous access.

The reviewed helper correction makes one required reread of the exact listed
key. A second failure still stops recovery. It also supports a named case in a
fresh output directory. A reviewed guard requires recovery of the ten attempted
cases, proves the sole remaining case has neither an attempt marker nor a public
ID, and binds the same frozen CLI, manifest and exact worker before submitting
only that final case. The original failed-read evidence and run definition remain
unchanged; no attempted command is replayed.

All **eleven distinct command cases passed**. The nine connected invocations
returned one schema-versioned metadata JSON envelope with the correct local exit
and separately recorded workload status, without workload bytes in CLI output.
Literal argv included empty/quoted/newline/Unicode arguments, shell metacharacters
and remote `--json`, `--help`, `--timeout` and `--` arguments. Actual execution
used nonroot `devbox`, the expected cwd and fixed environment, and stdin EOF.
Both binary streams matched their complete lengths and SHA-256: **131,072 bytes
stdout and 98,304 bytes stderr**. Empty streams were real zero-byte objects with
the empty-content digest. All records matched scope, instance, document, runner,
payload and command identities, with one exact 30-day deadline from submission.

For the first disconnect, SIGKILL arrived 9.84 ms after acknowledgement and the
remote workload finished 12.28 seconds after local death. For the second, the
workload began 5.64 seconds before SIGKILL and finished 24.36 seconds afterward.
Both local invocations exited -9, all dedicated local process groups stopped,
and no final local JSON appeared. Both remote workloads exited 0 and published
complete output with the original IDs. The guarded second run passed 34 checks;
there are exactly eleven distinct public IDs and eleven distinct SSM IDs across
the two result sets.

## Cleanup and retained recovery

The exact replacement worker was terminated successfully. Independent operator
EC2 calls verified it terminated, its captured original root volume absent with
`InvalidVolume.NotFound`, and zero scoped nonterminated workers and volumes.
Final `devbox ls` also succeeded with no active worker.

After teardown, read-only recovery passed **318 assertions across all eleven
submitted commands**, including full output lengths, hashes and unchanged command
bindings. The original run explicitly reports ten recovered and one unattempted
case skipped; the separate final-case run reports one recovered with no skips.
These are STS/S3 reads only: no worker, EC2/SSM observation, local finalizer,
redispatch or command cancellation is needed. Valid results remain under the
30-day retention policy. Fresh independent review verified the execution,
disconnect and cleanup evidence; final report review is recorded in the #18 plan.

## Evidence and automated validation

Private initial evidence is under `/tmp/devbox-issue18-live-checks`; the final run
uses `/tmp/devbox-issue18-clock-live-checks`. Actual plan/apply/drift evidence is in
the frozen checkout's ignored `infra/foundation/acceptance-output/issue18`.
Credentials, raw API responses and workload bytes are not committed. The reviewed
harness SHA-256 for the initial refreshed run is
`a92405122abe5ec4e48af0448bee28a5b5862f354f413e6c9010cdd137f4614b`;
the reviewed bounded-reread and named-case correction is
`62e2cb07728e893a02c723f57617233e8af485c3be3e2e4e660cc5d280fa68a3`.

`make check`, `make build`, execution/CLI/config race tests and pinned
`make infra-check TOFU=/tmp/devbox-tools/tofu` passed for the dispatch source.
After the runner correction, full checks, build, runner/protocol race tests and
the pinned infrastructure checks passed again. Offline infrastructure checks ran
outside the live backend checkout. Controlled SDK tests also prove one wire send
after a lost response, literal argument handling, strict result identity checks,
exit-code collisions and preservation of recovery IDs after submission failures.
