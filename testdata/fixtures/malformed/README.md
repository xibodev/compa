# Malformed fixtures

Every file here is a document the host MUST REJECT.

They are raw bytes rather than marshalled structs, because the point is that
the correct types cannot produce them. They are how each lane proves it
validates module output rather than trusting it.

| File | Why it must be rejected |
| --- | --- |
| `two-json-documents.txt` | stdout must carry exactly one JSON envelope |
| `progress-line-before-envelope.txt` | progress belongs on stderr, never stdout |
| `trailing-log-line.txt` | trailing output after the envelope is a protocol violation |
| `empty-stdout.txt` | a module must always emit an envelope, even on failure |
| `not-json.txt` | stdout must be JSON |
| `null-collections.txt` | warnings and artifacts must be [] rather than null |
| `cost-zero-instead-of-unknown.txt` | an unpriced provider call reporting 0 rather than null under-reports real spend; not structurally detectable, caught by comparing against declared Effects.CostKnown |
| `wrong-protocol.txt` | the host refuses a protocol it does not speak |
| `request-id-not-echoed.txt` | request_id must be echoed verbatim so the host can correlate |
| `ok-true-with-error.txt` | exactly one of result/error, matching ok |
| `absolute-artifact-path.txt` | artifact paths are relative to a declared root; an absolute path makes confinement uncheckable |
| `bare-hex-digest.txt` | digests carry a mandatory sha256: prefix |
| `ok-true-with-neither-result-nor-error.txt` | ok:true must carry a result; the empty case is the one a real module hits by forgetting to set it, and it decodes to a typed zero value rather than an obvious error |
| `operation-is-capability-not-verb.txt` | operation names the VERB (describe|invoke), never the capability; the capability travels in the request and is correlated by request_id |
| `oversized-inline-payload.txt` | a large payload inlined in result instead of returned as an artifact pointer; NOT structurally invalid, so it is enforced by max_output_bytes at read time rather than by the validator -- this fixture exists to test that bound |
