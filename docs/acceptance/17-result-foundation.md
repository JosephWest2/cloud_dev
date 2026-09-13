# Issue #17 storage and runner live acceptance

This records the foundation slice of parent #2, following #16's reviewed
contract. The public `exec`, observation and `logs` commands belong to #18–20;
this procedure does not claim those interfaces or #21's full acceptance gate.

## Source, authorization and deployment

The tested implementation is `7e672fd` in a separate clean live checkout. Fresh
subagents reviewed the runner, infrastructure and actual saved plan before the
user authorized applying it and running scoped checks with a disposable worker.
The user selected Ctrl-C detachment and a configurable default of 30 days from
submission. Durable costs were explained before authorization.

On September 13, 2026 UTC, both explicit AWS profiles authenticated to the
configured account in Ohio. The saved plan applied successfully: nine resources
created, three updated, none deleted. A subsequent real plan returned exit 0,
with no changes. The created result bucket has all public access blocks,
owner-enforced ownership, SSE-S3 encryption, never-enabled versioning, TLS and
conditional-creation enforcement, and result-prefix expiration plus multipart
abort. The runner artifact is outside the expiring result prefix. No networking
resources or state backend settings changed.

The frozen local build, uploaded artifact pin and installed worker binary share
SHA-256 `eede5e37b4bc9e552027a2ff90fc70eb73455688f6ea37b59dde5f9aef646940`.
The new manifest v4 passed all 14 doctor checks using `devbox-operator`. The
separate setup profile performed the authorized apply and seeded/deleted only
five tracked harmless IAM fixtures; it did not run the operator or worker tests.

## Actual enforcement and execution

One On-Demand `agent` worker reached EC2 running, SSM online, bootstrap complete
and readiness ready using the new numeric template version. Independent EC2
inspection recorded its instance/root-volume IDs, required IMDSv2, encrypted
100 GiB gp3 root with `DeleteOnTermination`, and zero ingress on every attached
security group.

A separately reviewed acceptance harness used the exported pinned execution
document and the literal payload/record contract directly. It wrote a request,
recorded a send-attempt marker, sent the command once with SDK/CLI retries
disabled, saved the returned SSM ID and wrote the acknowledgement. Recovery mode
only reads the saved command; it never redispatches an uncertain request.

All **57 operator/protocol assertions and 48 worker assertions passed**:

- The operator could create/read request and acknowledgement objects, and read
  all five worker output/metadata leaves. It could not write those worker leaves,
  unrelated leaves, or objects in other owner/deployment scopes.
- The worker used its actual IMDS instance role, could read the operator's bytes,
  create its five assigned leaves and read back published stdout. It could not
  create request or acknowledgement records, delete objects, or access
  unrelated/other-scope keys.
- Both roles received exact `AccessDenied` responses when reading existing
  harmless objects in another owner scope, another deployment scope, or an
  unrelated leaf. Setup had created those sentinels first, so these checks do
  not confuse absent objects with authorization denial. Cross-scope and unscoped
  listings were denied where tested; the owned prefix could be listed.
- Conditional overwrites returned `PreconditionFailed`, preserved original
  bytes, and writes without the conditional header returned `AccessDenied`.
  A mismatched expected bucket owner was denied. An authenticated exact-prefix
  listing proved an allowed missing result key absent.
- The workload's real/effective user was `devbox`, with literal special-character
  and Unicode arguments, expected cwd/HOME, fixed environment, stdin EOF and
  umask 022. It could not ordinarily read the protected root configuration.
  Root ownership/modes and the full installed runner hash were checked.
- The workload exited **4**. The independent publisher completed successfully
  (SSM response 0) and retained the workload's exit separately. The retrieved
  stdout was **133,663 bytes** and stderr **65,536 bytes**, including binary bytes
  beyond SSM inline limits. Entire streams matched their lengths and SHA-256 in
  both outcome and final result; capture and publication were complete.
- All five records matched the command, scope, instance, document, runner and
  payload bindings. Started/outcome/result copied the request's S3 submission
  time and one shared expiry exactly 30 days later.

A separate harmless `AWS-RunShellScript` SendCommand attempt on this worker was
denied with `AccessDeniedException` under the operator role, while the pinned
execution document succeeded. Other-owner/deployment **SSM instance targeting**
was reviewed statically; the live cross-scope tests above exercise **S3**, not
newly provisioned foreign workers.

## Cleanup and retained recovery

`devbox down` succeeded for the exact recorded instance. Independent operator EC2
calls then observed that instance terminated and its original root volume absent
(`InvalidVolume.NotFound`). Separate scoped inventory returned zero nonterminated
instances and zero volumes; final `devbox ls` agreed. The five setup-seeded IAM
fixtures still contained their original bytes and were deleted by exact key,
with exact-prefix absence verified afterward.

The Python harness's read-only recovery mode passed 28 assertions after teardown,
including all metadata and both complete output streams. A separately reviewed
Go probe then passed **38 assertions using production storage code**. It loaded
an exact six-field storage-only v4 manifest through `LoadResultManifest`, with
nonexistent SSH/profile paths and no launch/execution resources in the input.
Under the actual operator SDK identity, `S3Store` and `ReadRecord` retrieved and
validated all five records, shared retention, exit 4 and unchanged complete
stdout/stderr hashes. This probe issued nine S3 GETs and one LIST, plus STS
identity verification; it used no EC2, SSM or SSH calls and refused writes locally.

AWS returned `NoSuchKey` for the probe's missing object, and production storage
returned `ErrNotFound` directly. A separate authenticated exact-prefix listing
confirmed absence. The first helper incorrectly required a specific
`AccessDenied` response; its failed assertion was preserved and corrected after
review. Production code did not change. The 403-to-list fallback remains covered
by controlled tests rather than claimed as an observed live branch.

Valid command results and owned-prefix probes remain under the configured
retention policy. Worker teardown left them intact. A fresh non-implementing
subagent independently reviewed both acceptance helpers, actual evidence, final
documentation and PR description, and approved #17's merge gate with no blocking
findings. All #17 acceptance gates passed on September 13, 2026 UTC.

## Evidence and scope

Private local evidence lives under `/tmp/devbox-issue17-live-checks`, with the
actual plan/apply/drift logs under the live checkout's ignored
`infra/foundation/acceptance-output/issue17` directory. Credentials and raw state
are not committed or included in this report. The reviewed Python harness hash
is `72c3be4db6834dda4c16fc58532963801ef39e6ad0d7de998ca0ca5274ee4843`.

Before the live apply, `make check`, `make build`, the pinned `make infra-check`,
targeted runner/protocol race tests, vet and whitespace checks passed. Offline
infrastructure tests ran outside the live backend checkout: one state-bootstrap
mock, nine foundation mocks and the Go/OpenTofu export/hash bridge. Controlled
tests cover additional process, timeout, uncertain-claim and publication failures;
those are separate from the specific live cases recorded here. Thirty days have
not elapsed, so lifecycle expiration is verified through live configuration and
record deadlines rather than an observed 30-day deletion.
