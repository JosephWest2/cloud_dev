# Version 1 CLI contracts

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

The foundation (#7) will export a non-secret JSON document with this exact
schema. The following IDs are illustrative fixtures, not deployable resources:

```json
{
  "schema_version": 1,
  "account": "123456789012",
  "region": "us-east-2",
  "deployment": "personal-dev",
  "owner": "stable-owner",
  "vpc_id": "vpc-0123456789abcdef0",
  "subnet_ids": ["subnet-0123456789abcdef0"],
  "security_group_id": "sg-0123456789abcdef0",
  "instance_profile_arn": "arn:aws:iam::123456789012:instance-profile/devbox",
  "images": {
    "agent": {
      "ami_id": "ami-0123456789abcdef0",
      "architecture": "x86_64",
      "launch_template_id": "lt-0123456789abcdef0",
      "launch_template_version": "1"
    }
  }
}
```

Every illustrated field is required. Unknown fields and missing/unsupported
schema versions are rejected. Exactly one JSON object is permitted. Account,
region, deployment and owner must match effective user config; the IAM instance
profile ARN must identify the same account. At least one subnet is required.
Each image entry pins an AMI ID, architecture (`x86_64` or `arm64`), launch-template
ID and positive numeric version string. `$Latest`, `$Default`, and AMI aliases
are invalid. The workload image key must exist in `images`.

The foundation must additionally validate the actual resources, region/partition,
image provenance, architecture and selected instance types, networking, IAM,
encrypted/delete-on-termination root volumes, and IMDSv2. This is not proof that
the chosen AMI is Ubuntu LTS; exact Ubuntu release/AMI and region/networking
remain decisions for #7. Ordinary CLI operations must never parse OpenTofu state
or invoke an infrastructure apply.

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
{"schema_version":1,"command":"doctor","ok":false,"exit_code":1,"checks":[{"name":"manifest","status":"fail","code":"manifest_unavailable","message":"deployment manifest missing; provision the OpenTofu foundation (issue #7) and export deployment.json beside the config, or set manifest to its path"}]}
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
file-transfer support. #7/#9 must provide remote sshd and SSH authentication,
host-key verification, and a proxy interface that OpenSSH-based tools can use
without opening inbound ports. Doctor currently probes the local `ssh` client
and Session Manager plugin; remote/key/editor checks belong to those slices.

No interactive session command exists in this slice. Session bytes must have a
separate stream lifecycle from structured results; #9 must settle and document
whether interactive commands reject `--json` or provide a distinct control
channel before implementing them. Never mix terminal bytes with a JSON object.
