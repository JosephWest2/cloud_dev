# Issue #6 validation

Date: September 10, 2026. Local host: Arch Linux, linux/amd64.
Initial toolchain: `go1.27.0-X:nodwarf5`; module minimum: Go 1.24.0.

## Automated evidence

- `make check`: formatting, module checksums, build, vet and all tests pass.
- `make build`: produces `bin/devbox` with the workload profile embedded.
- `GOTOOLCHAIN=go1.24.0 go test ./...`: all tests pass with the documented minimum
  Go version, including building and exercising the real executable.
- Tests cover missing/malformed/unknown-field configuration, unsupported schemas,
  account/region/deployment/owner manifest mismatches, exact image/template
  requirements, profile market preservation, configuration precedence, and
  prevention of identity calls after invalid user configuration/profile inputs.
- Controlled STS responses cover wrong account, expired/invalid credentials,
  unknown errors and timeout. No EC2 API is imported or reachable in this slice.
- Pinned SDK tests verify named-profile selection over environment access keys,
  the default environment credential chain, and refusal to fall back from a
  nonexistent selected profile.
- CLI tests verify one JSON document, matching exit status, stderr separation,
  independent local checks, and redaction. A real child executable runs a failing
  credential_process and a noisy plugin containing secret sentinels; neither
  stream exposes them.
- GPT-6 high-effort review found misleading local-probe failures after an AWS
  timeout and credential helpers surviving process exit. Both are addressed:
  expired contexts produce explicit skipped probes, probe cancellation retains
  its timeout classification, and the Linux supervisor terminates the worker
  process group on exit. Tests verify a timed-out credential helper and its
  shell child are no longer running after the CLI returns.
- The GPT-6 high-effort follow-up review reported no remaining findings. The
  reviewer independently verified SIGINT and SIGTERM: each returned one valid
  JSON result, exit 4, and terminated the isolated credential helper.

## Clean configuration installation

Installed with `GOBIN=<temporary directory> go install -trimpath -buildvcs=false
./cmd/devbox`, set `XDG_CONFIG_HOME` to an empty temporary directory, and limited
PATH to the temporary install directory. `devbox version` returned
`devbox 0.1.0-dev`. `devbox doctor --json` returned one parseable JSON result and
exit 2, with actionable missing-config, missing-Session-Manager-plugin, and
missing-OpenSSH-client failures. Profile/manifest/identity checks were explicitly
skipped pending configuration; no AWS call was made. Temporary files were removed.

## Scope and limits

User confirmed one personal AWS account/region per config, a selected AWS profile,
a stable owner, Linux/Arch locally, and SSH over SSM with editor/file-transfer
support. These choices are recorded in `design-decisions.md`.

No live AWS identity, provisioning, launch, SSH, editor or file-transfer acceptance
was attempted. No actual account/profile values or secrets were needed. OpenTofu
and Session Manager plugin are absent on this host; the local missing-prerequisite
checks and executable fixtures were used. Deployed-resource validation and exact
region/network/Ubuntu image selection remain #7; lifecycle operations remain #8;
remote authentication, host verification, proxy integration and interactive JSON
behavior remain #9. This document does not establish the parent MVP's live
lifecycle acceptance gate.
