# Synapse Spark and artifact data-plane contracts

The ten JSON examples are byte-for-byte copies from Azure's
[2020-12-01 data-plane specifications](https://github.com/Azure/azure-rest-api-specs/tree/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/synapse/data-plane/Microsoft.Synapse/stable/2020-12-01).
`sources.json` records each URL, SHA-256, operation and request path. The catalog
snapshots six new documents (three roots and three model dependencies), alongside
two already-pinned shared dependencies. Tests use only these local sources.

The selected operations are Spark batch/session list, get and cancel, plus
notebook and Spark-job-definition list and get. They establish the contracts
needed to inspect work affected by pool/workspace cleanup. This milestone does
not implement data-plane inventory, workspace-bound OAuth, cancellation or
resource cleanup. `Runtime.Invoke` explicitly rejects this transport until those
pieces exist. Existing ARM inventory and resource actions are unchanged.

## Wire protocol

- Requests target an explicit HTTPS workspace development endpoint such as
  `https://workspace.dev.azuresynapse.net`. The binder rejects foreign origins,
  credentials, ports, paths, queries and fragments. A valid URL alone does not
  authorize access: runtime work still needs a fresh ARM workspace ownership
  check, the Synapse OAuth audience and redirect restrictions.
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
and refusal to invoke the unfinished transport. This is offline contract testing;
no live Azure resources or independent Synapse emulator were used.

Official API documentation:
[Spark batch lists](https://learn.microsoft.com/en-us/rest/api/synapse/data-plane/spark-batch/get-spark-batch-jobs?view=rest-synapse-data-plane-2020-12-01),
[Spark session lists](https://learn.microsoft.com/en-us/rest/api/synapse/data-plane/spark-session/get-spark-sessions?view=rest-synapse-data-plane-2020-12-01),
[notebook lists](https://learn.microsoft.com/en-us/rest/api/synapse/data-plane/notebook/get-notebooks-by-workspace?view=rest-synapse-data-plane-2020-12-01).
