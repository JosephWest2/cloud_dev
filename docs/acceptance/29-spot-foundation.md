# Issue #29 multi-AZ foundation verification

This slice follows #28 in [PR #35](https://github.com/JosephWest2/cloud_dev/pull/35).
Its contract is [documented here](../contracts.md#spot-batch-contract-28).
It owns durable infrastructure, exact manifest exports and read-only resource
verification. The allocator/recovery/CLI and fresh restricted-operator launch
evidence belong to #30–#34. No mock result proves live launch authorization.

## Migration scope and prerequisites

The reviewed migration preserves the original a subnet/resource address/CIDR
and adds selected b/c subnets with stable AZ keys and separate route associations.
It pins a subnet-independent primary template interface, encrypted disposable
gp3 roots, exact image/version and the existing runner/bootstrap. Results/state
storage and command retention are retained. Permanent launch records share the
private results bucket in a separate prefix with conditional-creation enforcement
and no runtime deletion or worker access.

September 13, 2026 (US/Central) read-only setup checks found:

- OpenTofu 1.12.6 at `/tmp/devbox-tools/tofu`; AWS provider pinned to 6.64.0.
- Explicit `devbox-setup` and `devbox-operator` identities authenticate in the
  intended Ohio account/scope, `personal-dev` / `joseph`.
- Ohio a/b/c are available; scoped nonterminated worker inventory is empty.
- `AWSServiceRoleForEC2Spot` is absent (`NoSuchEntity`). The setup identity must
  create it through the [documented prerequisite](../setup.md#upgrade-to-the-multi-az-spot-foundation-29).
  The operator cannot create it; it is not owned/deleted by worker cleanup.
- The latest acceptance export is v4/template 4, while the user's default export
  remains v3/template 2. Use the matching latest config to prepare migration and
  explicitly re-export into the intended CLI configuration after apply.

## Checks and review

`make check`, `make build`, and `make infra-check TOFU=/tmp/devbox-tools/tofu`
passed. Infrastructure checks include one state-bootstrap run, 23 foundation
runs, and the Go bridge that verifies actual mock resource exports, policy and
document hashes, placement and template settings. Controlled Go checks cover
legacy v4 behavior and v5 resource, image, capability, offering, Spot-role and
ledger drift. The optional read-only IAM simulator harness passed 25 modeled
authorization chains, including Spot/On-Demand, scoped tags/dependencies,
encrypted volumes, legacy RunInstances, termination and PassRole denials.
Simulation uses synthetic ARNs and supplied context; effective EC2 permissions
still require the live acceptance gate.

A separate live checkout and `TF_DATA_DIR` produced a saved migration plan with
five additions, four updates and one deletion. The additions are b/c subnets and
route associations plus the new content-addressed runner artifact. The deletion
is only the previous runner artifact, after its replacement is created. Updates
cover the two inline role policies, result bucket policy and launch template.
The original VPC, a subnet, route association, security group, state/result
buckets and result retention have no replacement or deletion. Scoped worker
inventory was empty before planning. The new export has schema 5 and a numeric
AMI root minimum.

The saved plan is `/tmp/devbox-issue29-live/infra/foundation/issue29.tfplan`,
SHA-256 `476247d86ac1b1abde91f26bbaa9c2df9b261cb49d04b6d978711500dfc530a9`.
Local text/JSON evidence is `/tmp/devbox-issue29-live-plan.log` and
`/tmp/devbox-issue29-live-plan.json`; these temporary artifacts are not committed.
Replan the final acceptance revision before applying because later slices may
change the runner. Preserve the exact built runner between that plan and apply.

Fresh independent subagents `review29_go` and `review29_infra` reviewed the
implementation. Review found that actual live labels plus three AZs exceeded
IAM's 10,240-character inline-role limit by seven characters. Shortening only
statement identifiers leaves identical permissions and a 10,144-character
policy. A three-AZ fixture models the actual label and ARN lengths; an oversized
scope fixture verifies rejection. With unresolved new subnet IDs the resource
precondition can defer until apply, so a successful live plan alone does not
prove the policy fits. Review also corrected the export bridge for provider
string-valued optional booleans and root volume size.

No infrastructure has been applied, and no Spot worker has been launched for
this slice. Setup must establish the missing Spot role, review/apply a current
plan, explicitly re-export next to the intended config, and run operator doctor.
Those actions and actual restricted-operator Spot evidence remain requirements
of #34; the parent is not complete here.
