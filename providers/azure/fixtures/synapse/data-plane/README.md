# Synapse Spark and artifact data-plane contracts

The eighteen JSON examples are byte-for-byte copies from Azure's
[2020-12-01 data-plane specifications](https://github.com/Azure/azure-rest-api-specs/tree/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/synapse/data-plane/Microsoft.Synapse/stable/2020-12-01).
`sources.json` records each URL, SHA-256, operation and request path. The catalog
snapshots nine documents (five roots and four model dependencies), alongside
two already-pinned shared dependencies. Tests use only these local sources.

The selected operations are Spark batch/session list, get and cancel, plus
notebook, Spark-job-definition and Pipeline list/get/delete, and artifact operation
result/status reads. They establish the contracts needed for pool/workspace and
artifact cleanup. The thirteen read
operations now execute through workspace-bound OAuth and validate their native
responses. Native cancellation is available through Runtime.Invoke with ownership,
protection and readback checks. Four data-plane asset kinds have inventory support.
Spark pool cleanup now orchestrates reviewed cancellation and ARM deletion;
workspace/SQL cleanup remains unfinished. Pipeline reads do not register an
additional asset kind.

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
and rejection of the original preview cancellation parameters. This is offline
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
operation count is now 1,531 after the ARM and artifact polling additions; cleanup
coverage is now 419 with the reviewed Spark pool action.

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

Spark pools now have a reviewed action; the other six kinds remain non-actionable.
Complete dependency coverage, artifact actions and workspace/SQL cleanup remain
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


### Native cancellation and stopped-work evidence

Runtime.Invoke supports the two catalogued stable Spark cancellation operations.
Microsoft documents these as cancelling work, with a 200 acknowledgement and no
LRO URL: [batch cancellation](https://learn.microsoft.com/en-us/rest/api/synapse/data-plane/spark-batch/cancel-spark-batch-job?view=rest-synapse-data-plane-2020-12-01)
and [session cancellation](https://learn.microsoft.com/en-us/rest/api/synapse/data-plane/spark-session/cancel-spark-session?view=rest-synapse-data-plane-2020-12-01).
The runtime binds the exact native path, resolves current subscription workspace
ownership, reads the pool and detailed job, checks resource-group management,
inherited locks and protected tags, verifies parents again and re-reads the job
before cancellation. A valid submittedAt timestamp binds the observed job
incarnation. Authored configuration or incarnation changes fail the call.

The return data distinguishes `accepted`, `exists` and `quiesced`, with a redacted
`observation`. Acceptance requires 200 with an empty result and no polling headers;
202/204, errors and redirects do not qualify. Both cancellation 200 and 404 receive
an independent detail read. Only that GET's 404 establishes absence. A retained
record can be quiesced without being absent. Permission failures, missing parents
or changed job/parent configuration never report completed cancellation. The
native response request ID is preserved; no synthetic LRO ID is invented.

Stopped-work classification requires a recognized state, a final Synapse result
(Succeeded, Failed or Cancelled), and both scheduler and plugin currentState=Ended.
A Livy state alone is insufficient; idle, error or a cancellation request alone
cannot prove completion. Unknown or missing completion fields remain unproven.
The service fields are defined in the pinned Spark schema; [upstream Livy state
semantics](https://livy.apache.org/docs/latest/rest-api.html#session-state) explain
why idle is not terminal. An already-quiesced job is observed without another
DELETE. Otherwise this call reads back once; callers can observe later progress
through the native GET. It does not block, automatically retry a mutation, or
provide a durable reviewed cleanup phase. The native cancel contract supplies no
conditional incarnation header, so a concurrent replacement after the final
preflight read cannot be atomically excluded. Request IDs are correlation, not a
provider guarantee of idempotency.

`cli-cancellation-recordings.json` retains four unchanged responses from the
pinned official CLI Spark batch/session recordings: a cancel acknowledgement and
a subsequent 200 record for each. The cancelled queued batch still has Livy
state not_started; the cancelled session has state killed. Both have service
result Cancelled and scheduler/plugin Ended. These recordings use
2019-11-01-preview, remain labelled as such and are tested for response semantics
only; they are not replayed as stable-version wire fixtures. Reproduce with
`python3 -B reproduce_cancellation_recordings.py [original-yaml-directory]`.
Local runtime tests separately exercise stable paths, explicit OAuth, private
canaries, protection, status/receipt faults, before/after drift, final readback and
context interruption. No fresh live-cloud or independent emulator run is claimed.


### Spark work and incoming dependency snapshot

The work collector reads both Spark job/session indexes and all workspace
notebooks, Spark job definitions and pipelines. Every listed record gets a detail
read, followed by a second complete index and a final detail/configuration check.
Previously reviewed records omitted from lists receive their own GET; only their
own 404 removes them from this snapshot. Quiesced historical records remain.
Spark entries require a valid scheduler submittedAt incarnation. Private hashes
bind authored configuration and selectors without persisting code or arguments.

Nested Pipeline activities may supply a `sparkPool` BigDataPoolReference,
including an expression-valued referenceName. Matching references are recorded;
dynamic, null, malformed and unknown selectors remain unresolved. These artifacts
are independent consumers, not children authorized for cascading deletion.
The collector checks parent configuration again before returning. It detects
observed drift; the APIs offer no atomic snapshot against concurrent writers.

The two new original Pipeline examples are included in sources.json. Their
PipelineResource schema declares id, name, type and etag as objects while the
native examples contain strings. Tests preserve and assert these exact schema
differences; runtime requires canonical string identity and string etag. The
examples also omit HTTPS in endpoint and use the existing SDK header alias.
Native source bytes are never rewritten to make validation pass.

The collector now feeds the durable pool action and its inventory review. The
cleanup worker persists cancellation and ARM deletion phases and verifies the
pool's own absence. See [the pool cleanup evidence](../README.md#reviewed-spark-pool-cleanup).
Protocol tests cover restored
manifests, omitted records, forbidden reads, configuration/index/parent drift,
incarnations, nested Pipeline references and private payloads. No live service or
independent emulator validation is claimed.


### Native artifact deletion and data-plane polling

The catalog adds Notebook, Spark-job-definition and Pipeline DELETE and three
native data-plane result/status GETs. Original examples declare empty 200/202/204
DELETE responses and empty 200/201/202/204 operation responses. They contain no
conditional If-Match deletion parameter. The DELETE operations remain gated in
Runtime.Invoke until reviewed artifact cleanup and dependencies are integrated;
this transport milestone adds no cleanup binding. The three operation GETs are
available with native parameter binding, workspace authorization and separate
Synapse OAuth, correlation headers and post-read workspace configuration checks.

The pinned official CLI evidence shows why empty Swagger examples alone are
insufficient: Notebook and Spark-job-definition DELETE actually return 202 with
an artifact-state body and Location. The body includes native identity, type,
name, state=Deleting, recordId and operationId. Notebook uses
`/notebookOperationResults/{operationId}`; Spark job definitions use
`/operationResults/{operationId}`. A subsequent 202 carries status=InProgress,
followed by an empty 200; only the separate artifact GET then returns 404.
`cli-artifact-deletion-recordings.json` preserves these eight original interactions,
including body strings and real wire versions. It is reproduced with
`python3 -B reproduce_artifact_deletion_recordings.py [original-yaml-directory]`.
No request credentials or request bodies are exported. The two pinned CLI source
hashes are the same as the artifact-read recordings; extraction SHA-256 is
`d821c44e9a211ab3e76f388df0f30a9e68768f73e0964b946db772ec63eaefcd`.

Receipt validation binds canonical artifact/workspace identity, native operation
collection, operation ID and API version. Foreign, duplicate and changed callbacks
fail. Signed receipts survive JSON recovery and support status followed by result
polling; completed receipts do not repeat HTTP calls. Polling uses the Synapse
transport, including a scoped empty-201 adapter, never ARM tokens. Unknown or failed
states and callback 403/404 are not completion or asset-absence evidence. API logs
and returned receipts omit opaque properties. Protocol tests cover recorded
receipts, empty wire responses, token separation, correlation, retries, restored
receipts, tampering, error envelopes, invalid body shapes and private canaries.

Artifact deletion drivers still require reviewed incoming references, active-work
handling, protection, persistence integration and each artifact's own final GET.
Pipeline asset inventory and additional dependency families also remain open.
No fresh live-cloud or independent emulator validation is claimed.
