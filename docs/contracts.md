# CLI contracts (config/profile/results v1, deployment v2)

## User configuration and workload profile

`examples/config.toml` is the complete user schema. `schema_version = 1`,
`expected_account`, `region`, `deployment`, and `owner` are required.
The expected account is a quoted 12-digit string. Deployment and owner are
1–63 ASCII letters, digits, underscores or hyphens, beginning with a letter or
digit. A region must have AWS region syntax; availability and partition-specific
resource checks belong to the foundation slice. `aws_profile`, `manifest`,
and `profile_file` are optional; precedence is documented in the README.
Unknown TOML fields, invalid types, malformed TOML and missing/unsupported
schema versions are rejected. This includes credential fields.

The embedded [agent profile](../profiles/agent.toml) is a separate versioned
document. An optional user profile_file uses the same contract:

| Field | Version 1 contract |
| --- | --- |
| `schema_version` | Integer `1` |
| `name` | `agent`; the only workload name in this slice |
| `market` | `spot` or `on-demand`; bundled agent defaults to **spot** |
| `instance_types` | Nonempty array of distinct EC2 type names; hardware compatibility checked later |
| `image` | Manifest image key, with the same character/length rules as deployment |
| `disk_gb` | Integer 8–16384; actual image size and volume constraints checked later |

The launch slice (#8) must preserve the selected market. Until Spot launches
are implemented, `devbox up agent` using the default profile must explain that
Spot is unsupported and show `devbox up agent --on-demand`. It must not launch
On-Demand implicitly. `--on-demand` is a future launch override, not a doctor
option. No market fallback is implemented here.

## Deployment manifest

The foundation exports a non-secret schema-v2 JSON object using
`tofu output -json deployment_manifest`. Schema 1 is deliberately rejected:
re-export after applying the foundation. Unknown fields and trailing JSON are
rejected. User config, workload profiles and result envelopes stay at version 1.

| Manifest field | Contract |
| --- | --- |
| `schema_version` | Integer 2 |
| `account`, `region`, `deployment`, `owner` | Must equal effective config; foundation currently Ohio only, labels at most 23 characters |
| `vpc_id`, `subnet_ids`, `security_group_id` | Exact EC2 IDs, exactly one subnet |
| `route_table_id`, `internet_gateway_id` | Exact EC2 IDs for explicit public route/association |
| `instance_profile_arn` | Exact commercial-AWS IAM profile ARN in expected account |
| `development_user` | `devbox` |
| `bootstrap_sha256` | Lowercase SHA-256 of the exact decoded template user-data bytes |
| `readiness` | `name`, positive numeric string `version`, `content_sha256` of canonical JSON |
| `roles` | Exactly `instance` and `operator`, each with distinct scoped `arn`, `trust_sha256`, `policy_name`, `policy_sha256` |
| `images` | Exactly `agent`, with the image fields below |

Image fields are `ami_id`, `architecture="x86_64"`, `ubuntu_release="24.04"`,
`owner_account="099720109477"`, exact Canonical `name`
(`ubuntu/images/hvm-ssd-gp3/ubuntu-noble-24.04-amd64-server-SERIAL`),
`root_device_name="/dev/sda1"`, `launch_template_id` and a positive signed-64-bit
numeric `launch_template_version` string. No `$Latest`, `$Default` or AMI aliases.
The standard Ubuntu LTS AMI is selected explicitly in the setup guide and its
regional ID and provenance are exported. All hashes have 64 lowercase hex digits.

The export is trusted local configuration, not a signed attestation. Doctor
verifies actual resource scope, configured routing/DNS/egress and zero ingress,
image provenance/state/architecture/root size, instance-type architecture,
template security settings and user-data digest, IAM role/profile membership,
trust/policy digests with no extra policies, and exact readiness document version
and content. IAM/document hashes use decoded JSON with sorted keys and compact
encoding equivalent to OpenTofu `jsonencode`; JSON formatting alone is ignored.
Policy array shapes remain significant. Any unexplained service normalization
must be investigated rather than bypassed. Read [IAM limitations](iam.md) and
[setup](setup.md) before treating these checks as a live acceptance gate.

Ordinary CLI commands read only this JSON export, never OpenTofu state or an
infrastructure apply. After an intentional foundation change, review, apply and
re-export. Invalid manifest scope/schema prevents all resource calls; independent
STS and local prerequisite checks can still run. STS identity must succeed before
resource validation. Timeout/cancellation retains exit 4; resource failures use
exit 1 and allowlisted diagnostics, never raw SDK errors. Foundation check names
are `foundation_network`, `foundation_image`, `foundation_template`,
`foundation_iam`, `foundation_readiness` (and `foundation_identity` when the
resource client's identity cannot be verified). Pass code is `foundation_verified`,
failure code `foundation_drift`, timeout code `foundation_timeout`.

Future inventory/mutations scope by expected account, region, deployment and
stable owner. STS identity verification must precede mutations. Required creation
tags include `ManagedBy=devbox`, `Deployment`, `Owner`, `Profile`, `Name`,
`RequestId`, and `CreatedAt`; tags do not replace corresponding IAM restrictions.

## Command results

`--json` produces exactly one JSON object on stdout, followed by a newline,
including on usage/configuration failures. All result envelopes have
`schema_version`, `command`, `ok`, and `exit_code`. Doctor and usage failures
include `checks`, an array with `name`, `status` (`pass`, `fail`, `skip`),
`code`, and `message`. Usage errors use an empty command rather than echoing an
untrusted command argument. `version` adds `version`; `--help` adds `help`.
Human messages may evolve; automation should use schema version, status and code.

```json
{"schema_version":1,"command":"doctor","ok":false,"exit_code":1,"checks":[{"name":"manifest","status":"fail","code":"manifest_unavailable","message":"deployment manifest missing; follow docs/setup.md to provision the OpenTofu foundation and export deployment.json beside the config, or set manifest to its path"}]}
```

This excerpt shows one check; real doctor results also contain the other checks.
Text mode renders the same command results on stdout. Diagnostic summaries go
to stderr. Credentials, SDK/provider error text, parser source excerpts, arbitrary
flag values and session tokens must never be included in either stream.

| Exit | Meaning |
| --- | --- |
| 0 | Command completed; all implemented doctor checks passed |
| 1 | Prerequisite, authentication/account, deployment manifest, service or output failure |
| 2 | Usage, user configuration or workload profile error |
| 3 | Reserved: completed remote command reported failure |
| 4 | Check timeout/cancellation; reserved for future readiness timeouts too |
| 5 | Reserved: partial batch completion |

When doctor has multiple failures, config/profile errors take priority over
timeouts, which take priority over other prerequisite failures. A local probe
that times out is reported as `probe_timeout` (4); probes not started before
the overall deadline/cancellation are `skip` with `check_canceled` (4), not
installation failures. Other local execution failures are prerequisites (1).

The agreed first-release access mode is real SSH over SSM, including editor and
file-transfer support. The foundation provides remote sshd and a `devbox` user; #9 must add SSH
authentication, host-key verification, and a proxy interface that OpenSSH-based tools can use
without opening inbound ports. Doctor probes the local `ssh` client and Session Manager plugin and verifies
foundation settings; runtime/key/editor checks belong to #9.

No interactive session command exists in this slice. Session bytes must have a
separate stream lifecycle from structured results; #9 must settle and document
whether interactive commands reject `--json` or provide a distinct control
channel before implementing them. Never mix terminal bytes with a JSON object.
