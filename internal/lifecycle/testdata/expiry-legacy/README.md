# Historical immutable launch fixtures

Captured from `origin/main` at `a0988bf`, before expiry was introduced. The
request ID and creation timestamp are fixed; each JSON file contains compact
Go struct-order serialization followed by one newline. The SHA-256 file hashes
the plan JSON **without** its trailing newline, matching `LaunchPlan.Digest`.

These are synthetic offline fixtures containing public example IDs only.
The plan, schema-2 batch receipt, schema-1 single-worker receipt, and permanent
prepared/claim/response envelopes must round-trip without inserting expiry
fields, changing field order, recomputing identities, or altering old hashes.
Do not regenerate them to accommodate implementation changes. New schema-2
plans and schema-3 batch receipts get separate fixtures in #43.

`response-v1.json` records zero fulfillment; `response-worker-v1.json` records
one successfully allocated worker and a matching attempt ID set. The latter
was serialized and validated in a detached checkout of `a0988bf`; it pins
embedded `WorkerOutcome`/`Instance` fields as well as the response envelope.
Both fixtures must remain unchanged during output-schema migrations.
