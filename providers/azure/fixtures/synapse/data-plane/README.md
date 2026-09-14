# Synapse Spark and artifact data-plane contracts

The ten JSON examples are byte-for-byte copies from Azure's
[2020-12-01 data-plane specifications](https://github.com/Azure/azure-rest-api-specs/tree/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/synapse/data-plane/Microsoft.Synapse/stable/2020-12-01).
`sources.json` records each URL, SHA-256, operation and request path. The catalog
snapshots six new documents (three roots and three model dependencies), alongside
two already-pinned shared dependencies. Tests use only these local sources.

The selected operations are Spark batch/session list, get and cancel, plus
notebook and Spark-job-definition list and get. They establish the contracts
needed to inspect work affected by pool/workspace cleanup. The eight read
operations now execute through workspace-bound OAuth and validate their native
responses. Cancellation remains gated pending reviewed lifecycle support.
Data-plane asset inventory and resource cleanup remain unfinished. Existing ARM
inventory and resource actions are unchanged.

## Wire protocol

- Requests target an explicit HTTPS workspace development endpoint such as
  `https://workspace.dev.azuresynapse.net`. The binder rejects foreign origins,
  credentials, ports, paths, queries and fragments. A valid URL alone does not
  authorize access: runtime resolves the full subscription workspace index,
  reads the matching workspace, and verifies its returned development endpoint.
  Spark requests additionally read their owning pool. Workspace/pool configuration
  is rechecked after the data-plane call. Redirects are disabled.
- Spark uses `/livyApi/versions/2020-12-01/sparkPools/{sparkPoolName}` and numeric
  int32 batch/session IDs. It does **not** use an `api-version` query parameter.
  Names remain single segments; traversal and encoded path delimiters fail.
- Spark lists use `from`, `size` (1–20) and optional boolean `detailed`. They return
  `from`, `total` and `sessions`, including for batches. There is no native
  `nextLink` contract. Future inventory must establish complete, stable pagination
  and bind detailed identities to the current workspace/pool before cleanup.
- Artifact lists use `api-version=2020-12-01`, `value` and `nextLink`. Conditional
  GET uses the literal `If-None-Match` header and can return 304. A 304 is not a
  fresh detail body and cannot establish a current cleanup snapshot.
- Spark cancellation declares an empty 200 response with no LRO URL. Acceptance
  alone does not establish termination or authorize polling arbitrary headers.

## Original-source discrepancies pinned by tests

The original files are intentionally unchanged. Tests first reject invalid raw
requests, then separately bind a documented projection; they assert the exact
response-schema failures rather than bypassing schema validation.

- All ten examples omit the HTTPS scheme in `endpoint`. Tests use an explicit
  HTTPS lowercase-host projection for the successful request checks.
- Both cancellation examples use `2019-11-01-preview` and the undeclared query
  parameter `detailed`. The source's client default is also the preview version.
  The selected stable contract and current REST examples for list/get use
  `2020-12-01`; binding pins that version and rejects the preview override.
- Notebook and job-definition GET examples use `ifNoneMatch`, while their wire
  schema declares `If-None-Match`. The undeclared alias is rejected and only the
  declared header is bound in the successful projection.
- Both Spark GET examples request ID 123 but return ID 1. Artifact example ARM
  IDs also use a different workspace name from the development endpoint. These
  are not evidence for identity aliases or workspace ownership.
- Both Spark GET responses contain null `appInfo`, `livyInfo`, `pluginInfo`,
  `schedulerInfo` and `tags` where objects are declared, and a `state` outside
  the declared enum. Tests require exactly these six leaf schema errors per
  response. These examples do not justify relaxing future live identity/state
  validation.
- Job-definition GET/list have array `pyFiles`, while the declared model says
  object. Tests require exactly one matching leaf error in each response.
- Both Spark lists say `from=0`, `total=2`, but return an empty `sessions` array.
  Schema validation alone accepts this. Tests pin the discrepancy; it is not
  evidence of an empty pool or a completed inventory page.

Validation covers ten operations, ten original examples and eight response
schemas, plus endpoint/version/ID/pagination rejection, deterministic import,
and refusal to invoke cancellation before lifecycle support. This is offline
contract testing; no live Azure resources or independent Synapse emulator were used.

Official API documentation:
[Spark batch lists](https://learn.microsoft.com/en-us/rest/api/synapse/data-plane/spark-batch/get-spark-batch-jobs?view=rest-synapse-data-plane-2020-12-01),
[Spark session lists](https://learn.microsoft.com/en-us/rest/api/synapse/data-plane/spark-session/get-spark-sessions?view=rest-synapse-data-plane-2020-12-01),
[notebook lists](https://learn.microsoft.com/en-us/rest/api/synapse/data-plane/notebook/get-notebooks-by-workspace?view=rest-synapse-data-plane-2020-12-01).

## Authorized read transport

The read transport uses a separate cached OAuth client with
`https://dev.azuresynapse.net/.default`, matching the
[pinned official Python SDK configuration](https://github.com/Azure/azure-sdk-for-python/blob/419f6596a9a718f63321eeffcc23a762cbb039f4/sdk/synapse/azure-synapse-spark/azure/synapse/spark/_configuration.py)
and [REST audience guidance](https://learn.microsoft.com/en-us/rest/api/synapse/).
It uses the explicitly configured service principal, rotates its cache with the
connection credential, and propagates cancellation. Other credential transport
implementations are rejected without falling back to ARM credentials.

All eight reads require a complete unfiltered workspace index and matching fresh
workspace GET; list omission, authorization failure, duplicate IDs and changed
parent configuration fail the call. This endpoint lookup is not inventory absence
reconciliation and does not close any known asset. Spark reads additionally
require a matching native pool GET before and after the data-plane read.

Only 200 object responses qualify as current observations. 202/204/206/304,
provider errors and unexpected polling headers fail. Spark validates numeric IDs,
nonempty state, detailed owner/type fields and page offsets/totals/member counts;
it returns the next numeric offset when more rows remain. Optional detailed
objects may be null, as observed in official CLI recordings. Future state strings
remain observations, not evidence of termination. Artifact reads require the
matching workspace/type/name/ID, object properties, valid ETag shape and consistent
response-header ETags. Artifact continuations stay on the same workspace and
collection with the pinned API version; they remain native opaque URLs.

Results and API diagnostics expose identity, state and pool references. Code
cells, job arguments/configuration, logs, tags and unknown opaque fields stay
private. Tests exercise actual Runtime.Invoke routing, token audiences/caching/
rotation, both OAuth and service redirects, interrupted contexts, complete owner
lookup, parent drift, bad statuses/IDs/pages and canary redaction. These tests use
an in-process RoundTripper, not a live Azure service or independent emulator.

### Native data-plane inventory

The `synapse-data` source registers four resource kinds: Spark batches, Spark
sessions, notebooks and Spark job definitions. Spark identities are actual Livy
URLs; their resource kinds group them beneath the ARM pool without inventing ARM
job resources. Artifacts retain their documented ARM identities and native name
selectors. These four additions bring the specification count to 455; the native
operation count remains 1,521 and cleanup coverage remains 418.

The source reads complete native indexes, validates each member with its own GET,
resolves workspace/pool references, re-reads each member and parent, and compares
a second complete child index before returning. Spark uses bounded offsets and a
constant total; artifacts retain scoped native nextLink URLs. Duplicate members,
cycles, configuration drift and incomplete responses fail the scan. This detects
observed changes; the API does not provide an atomic cross-resource snapshot.
Client cursors contain only an offset and connection-private fingerprint, never
code or job configuration.

Known objects omitted from lists receive their own GET. Only that object's 404
closes its record; 403 or a missing/unreadable parent leaves the scan incomplete.
Saved connection/workspace/endpoint selectors must still match fresh ownership
reads. Region scans retain sibling-region objects. Missing artifact pool targets
remain explicit unresolved references; forbidden targets fail the scan. The
source is non-authoritative and emits explicit absence only on its final page.
Tests cover native and client paging, changing indexes/parents, private canaries,
known-object reconciliation, protection and actual SQLite worker/graph persistence.

All seven Synapse resource kinds remain non-actionable. Terminal classification,
cancellation, complete dependency coverage and pool/workspace cleanup remain
required next steps. No live cloud or independent emulator validation is claimed.

### Stable-version CLI artifact recordings

`cli-recordings.json` retains seven selected GET responses from the official
Azure CLI notebook and Spark-job-definition scenarios at commit
`c683a64f397974bae397d77a204e2ae86a908fa0`: five successful detail/list reads and
two subsequent 404s. Their actual wire version is `2020-12-01`; no versions,
identities or response-body values are rewritten. Source hashes and interaction
indices are recorded in the extraction. Tests bind the recorded requests, accept
their native identities in the matching workspace, reject a changed workspace,
and keep 404 distinct from a valid observation.

Reproduce with `python3 -B reproduce_recordings.py [directory-of-original-yaml]`
from this directory (PyYAML required). With no directory, the script downloads
only the two pinned official recording files. Request credentials and request
bodies are excluded; response metadata and complete JSON bodies are retained.
These historical CLI recordings complement local tests; they are not fresh
live-cloud validation of Steward.
