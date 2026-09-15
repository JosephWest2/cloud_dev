# Expiry-aware wire fixtures (#43)

Plan v2, receipt v3 and permanent v1 attempt records for a deterministic request
created at `2026-09-14T00:00:00Z`, expiring at `2026-09-14T02:00:00Z`.
The selected disk is 137 GiB and the pinned template version is 7.
The response preserves one successful worker and missing capacity for one.

These files pin canonical bytes, plan digest, token/input hashes and successful
worker equality. They are validated without consulting today's clock. The
separate allocation guard rejects an expired fixture; decoding never grants
allocation permission. Historical fixtures in `../expiry-legacy` are unchanged.
