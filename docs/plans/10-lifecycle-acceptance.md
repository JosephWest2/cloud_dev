# Issue #10 implementation plan

Status: reviewed September 13, 2026 UTC (September 12 US/Central) by a
GPT-6 Astra subagent at high reasoning effort; adjustments incorporated below.
Baseline: `61895cb` (merged #9).

## Outcome and scope

Complete the independent clean-checkout acceptance gate for parent #1. Another
developer must be able to provision, configure, launch, rediscover, connect and
remove one Ubuntu devbox using the README and linked setup instructions, without
the AWS Console or a local instance inventory. Record actual results separately
from procedures and historical #6–#9 evidence. An unrun or blocked live check
keeps #10 and the parent gate open.

Existing decisions are Linux locally, Go/AWS SDK v2, OpenTofu 1.12.6/AWS provider
6.64.0, TOML, Ohio, Canonical Ubuntu 24.04 amd64, public IPv4 with no inbound
rules, explicit On-Demand acceptance, and real SSH over SSM using a dedicated
local Ed25519 identity and probe-pinned host trust. Reuse the previously selected
test setup/operator profiles (`devbox-setup` / `devbox-operator`) and existing
deployment/owner/account/backend/AMI/key inputs after checking their consistency.
Keep actual account values and raw local artifacts private. Do not infer new
deployment choices or substitute example inputs for existing resources.

## Implementation sequence

1. Review the current commands, README/setup, decisions, prior acceptance results,
   and meaningful invariant tests. Have a GPT-6 Astra high subagent review this
   plan before implementation; incorporate actionable findings.
2. Finish the README's current product contract: prerequisites/key setup,
   state bootstrap/migration and manifest-v3 links, explicit profile selection,
   expected output, readiness, SSH/editor/file-transfer and shell exit semantics,
   receipt recovery, ID-based teardown and durable-resource retention. Remove
   stale claims that implemented or accepted #7–#9 work remains future/pending.
   Reconcile stale setup/decision wording with the recorded selections and live
   history without rewriting historical evidence as a new run.
3. Create `docs/acceptance/01-lifecycle.md` as the reproducible parent runbook and
   evidence ledger. Include a requirement-to-evidence matrix, dated tooling and
   source revision, selected non-secret scope descriptors, initial/final inventory,
   setup/state/plan/apply status, exact image/template/document pins, expected
   command behavior, failure/recovery procedures, cleanup status and blockers.
   Record historical setup/migration and SSH/editor evidence as historical; run
   the independent final workflow anew. Do not force a new apply or rebuild the
   retained foundation merely to manufacture an apply result if its plan is empty.
4. Verify a clean checkout with fresh devbox config/state directories: construct
   config from the example using the selected local scope/key, export the real
   manifest from the configured backend, and build/install from tracked files.
   Preserve existing config, state, keys and backend files. Use the normal AWS
   credential chain with explicit operator/setup flags; never print credentials,
   private keys, raw state or credential-process output. Verify operator doctor
   and setup-profile identity before live mutations. Review any nonempty live
   infrastructure plan before applying and resolve unexpected drift first.
5. Execute the live parent sequence on one disposable On-Demand smoke worker:
   up/list with separate readiness and pinned allocation fields; SSH, `uname -a`
   and `whoami`; new-process rediscovery with an empty local state directory;
   inspect all attached security groups for empty ingress; safely resume the same
   request; force and identify an actual readiness-phase timeout on that
   existing request (an earlier authentication/setup timeout does not qualify);
   inspect and terminate by retained instance ID; verify termination and exact
   root-volume absence through EC2/EBS APIs; repeat down and final inventory.
   If real terminal/editor confirmation is needed beyond the prior acceptance,
   pause with the exact commands and requested observations. Do not leave a
   worker allocated across a pause: clean it up first unless the user explicitly
   needs that worker for the pending test and understands manual teardown. If
   authentication or termination failure prevents cleanup, retain every known
   ID and immediately pause with credential-refresh and exact ID-based teardown
   commands; record cleanup incomplete and leave the gate open.
   A qualifying readiness-phase timeout must record exit 4, `operation_timeout`, retained
   request/instance/volume IDs and `observation_timeout` (or equivalent proof that
   `WaitReady` was entered after successful reconciliation). This diagnostic can
   precede status-probe dispatch; record that limit rather than claiming a known
   failing API or an in-flight probe. Adjust only the original receipt's timeout within bounded
   attempts; report a blocker if none reaches that assertion.
6. Run controlled SDK failure coverage for expired credentials, wrong account,
   invalid config, readiness timeout, safe launch replay, ambiguous names, and
   unmanaged/out-of-scope termination refusal. Reuse existing tests, identifying
   named cases and their actual guarantees. Use isolated configuration for real
   invalid-config/wrong-account checks where useful; never alter working IAM,
   expire real credentials deliberately or create unsafe extra workers. Add
   tests/code only for demonstrated integration gaps.
7. Run final `make check`, `make build` and `make infra-check` with the pinned
   OpenTofu executable from a clean checkout, recording its initial clean status
   and exact revision. Use separate offline and live checkout directories:
   `make infra-check` initializes with `-backend=false` and must never reuse the
   directory initialized for live S3 state. Record formatting/module integrity,
   build/vet/tests and infrastructure fmt/validation/mock/export checks with
   dates/versions/results. Keep live plan/apply/API evidence separate. On every
   live failure, reconcile the saved request before any further allocation and
   remove the known worker; verify the root volume and scoped inventory. List
   retained durable networking, IAM, launch template, readiness document and S3
   state/version history, with links to manual full teardown until MVP 4.
8. Commit the completed changes, open a PR referencing #10, and have a fresh
   agent review the actual PR diff and evidence. Fix findings and rerun affected
   checks; update the PR around the final change. Merge after the independent
   review, required checks and acceptance gate pass. Close #10 only with complete
   evidence; leave parent #1 open if any of its required checks remain blocked.

## Pause conditions

The user requested a pause whenever their testing or a decision is needed.
Stop with concise commands if credentials require interactive refresh, the
existing selected setup cannot be recovered, a deployment/AMI/key/scope choice
must change, live infrastructure changes need their decision, or a human-only
test lacks sufficient evidence. Explain the exact blocker and requested result;
never mark an unrun check passed or merge around an incomplete acceptance gate.
