# Synapse native ARM contract evidence

The 23 original examples and two polling examples are **unmodified Microsoft Swagger examples**, pinned to
`Azure/azure-rest-api-specs` commit
`c20bf553ad64f20c6d5e3f56080380c086cb1fde`. `sources.json` records each upstream
URL, SHA-256, source document, method, path and operation. The two new
operation examples have a separate `polling-sources.json` manifest. They are not recordings
from a live subscription or responses from an independent Synapse emulator.

The catalog contains 24 operations from the stable **2021-06-01** contract:

- Workspaces: subscription/resource-group list, get and delete, plus operation status and result reads.
- Spark big-data pools: workspace list, get and delete.
- SQL pools: workspace list, get, delete, pause, resume and operation-result get.
- SQL pool restore points: list, get and delete.
- SQL pool replication links: list and get; management-operation list and user activity get.
- Restorable dropped SQL pools: workspace list and get.

The four ARM documents and four transitive reference documents carry the hashes
of their complete upstream files. The initial contract commit (`413ca97`) did
not enable inventory or cleanup. The next milestone adds three explicit resource
specifications and a `synapse` inventory source. Its five workspace matrix entries
now reference the existing specification, while behavioral verification remains
pending. Existing operations and original fixtures are unchanged.

Native inventory validates list/detail identities, parent membership, configuration
changes, pagination and incomplete responses. Known IDs are reconciled through
individual GETs; source lists have no authority to erase omitted assets. Default
Data Lake references resolve through the current subscription's Storage list and
matching detail reads, without following storage URLs or claiming ownership.
Unresolved storage remains visible. Workspaces are reread after dependency reads.

Spark pools now have a reviewed cleanup driver. Workspaces and SQL pools remain
non-actionable while their reviewed cleanup is unfinished.
This is an intermediate implementation stage, not the final read-only scope or a
claim of functional parity. Integration runtimes, private connectivity, code
artifacts, running jobs, recovery/retention and complete cleanup are still required.
Runtime tests use adapted native shapes and an in-process HTTP transport; the
registered scan tests use the real worker, registry and SQLite persistence.

## Reproduce the local checks

From the repository root:

```sh
python3 -B scripts/test_sync_azure_catalog.py
go test ./providers/azure -run 'TestSynapse|TestCatalogReproducibleAndSpecsExecutable' -count=1 -v
```

The checks bind the official requests, reject undeclared parameters, path traversal
and version changes, and validate 34 response bodies against their original
schemas. They check the full native example first and explicitly account for the
following source inconsistencies; no fixture or production schema is patched.

| Example | Pinned inconsistency | Test treatment |
| --- | --- | --- |
| `ListSqlPoolsInWorkspaceWithFilter.json` | `$filter` appears in the example but is not declared by `SqlPools_ListByWorkspace`. | The full request must be rejected. Separately bind only the declared parameters. |
| `ListSqlPoolRestorePoints.json`, `SqlPoolRestorePointsGet.json`, `SqlPoolRestorePointsDelete.json` | The examples supply an undeclared `location` parameter. | Require that exact rejection, then separately bind the declared parameters. |
| `RestorableDroppedSqlPoolGet.json`, `RestorableDroppedSqlpoolList.json` | `elasticPoolName` is `null`, but the original schema declares a string. | Require precisely the three null/string errors across the two responses, with no other schema failures. |

## Boundaries for the next implementation steps

Schema-valid examples do not establish safe inventory identities or deletion
completion. These additional inconsistencies remain evidence for the runtime work:

- Restore-point GET/list bodies omit the resource-group segment that their request
  paths require. The operation-result SQL pool body names a different subscription,
  workspace and pool from its request. Do not infer aliases or relax identity
  validation from those samples.
- Workspace/Spark/SQL delete and SQL pause/resume examples use an
  `azure-asyncoperation` URL of `https://ms.web.azuresynapse.net`. Workspace and SQL
  operations declare `final-state-via: location`, whereas Spark delete declares
  `azure-async-operation`. These illustrative URLs do not authorize credential
  forwarding or establish production polling scope. The existing ARM URL boundary
  remains unchanged. Native `200`/`202` bodies can still describe a deleting pool;
  an accepted mutation is not proof of absence.
- The filtered SQL list example returns a `master` pool. The example does not
  establish that this is an independently deletable dedicated pool. Workspace
  cleanup needs explicit treatment of managed/default resources, active work,
  restore information, integration runtimes and data-plane artifacts.
- Microsoft's [workspace cleanup documentation](https://learn.microsoft.com/en-us/azure/synapse-analytics/quickstart-create-workspace-cli#clean-up-resources)
  states that workspace deletion removes contained SQL data, workspace metadata
  and code artifacts, while preserving data in its linked Data Lake Storage Gen2
  account. Inventory and review must include those affected artifacts. Resolve
  the default Data Lake account URL/filesystem as an external dependency; neither
  this link nor the managed resource-group name grants ownership of the storage
  account, its data or every resource in a group.

Official contract entry points:
[workspaces](https://learn.microsoft.com/en-us/rest/api/synapse/resourcemanager/workspaces?view=rest-synapse-resourcemanager-2021-06-01),
[Spark pools](https://learn.microsoft.com/en-us/rest/api/synapse/resourcemanager/big-data-pools?view=rest-synapse-resourcemanager-2021-06-01),
[SQL pools](https://learn.microsoft.com/en-us/rest/api/synapse/resourcemanager/sql-pools?view=rest-synapse-resourcemanager-2021-06-01).

The [Spark and artifact data-plane evidence](data-plane/README.md) covers eighteen
additional native operations. It has a separate transport contract and original
example manifest; scoped native reads/cancellation and asset inventory are
implemented. Spark pool cleanup composes these protocols; workspace/SQL cleanup
and additional child resources remain open.


## Scoped asynchronous operation receipts

The pinned `operations.json` supplies workspace-scoped operationStatuses and
operationResults. The existing SQL pool contract also supplies a pool-scoped
operationResults endpoint. The newly retained examples define a 200 status body
with InProgress, and empty Location results with 200/201/202/204; the native
OperationResource schema supplies Succeeded, Failed and Canceled. Original
placeholder URLs and mismatching SQL-pool example identities remain unchanged.

Workspace, Spark-pool and SQL-pool native DELETE invocations now validate their
200/202/204 responses before returning a signed `_synapse_operation` receipt.
Optional resource bodies must identify the deleted resource; neither a body nor
an acknowledgement proves absence. Initial 202 requires a valid status/result
URL. URLs stay in the owner's subscription and workspace (or that exact SQL pool
for its result endpoint), use 2021-06-01 and carry one operation ID shared across
headers. Duplicate/empty headers, other origins, versions, collections, query
fields and conflicting operation IDs fail validation. Operation-Location is not
part of these selected contracts.

Receipts bind resource identity and every saved field to the resolved connection.
The poller verifies this binding before all reads and before accepting an already
completed receipt. When both headers exist, it waits for status success and then
for the result endpoint. It preserves Retry-After, can serialize state and resume
with a fresh runtime, and never treats a callback 404 as resource absence. Failed,
canceled, unknown or malformed status responses do not complete the operation.
Native status/result invocations retain request IDs and expose only a redacted
observation plus `_synapse_operation_done`; private operation properties stay out
of responses and API logs. This flag concerns the operation, not the asset.

Empty 201 Location responses receive a narrow internal empty-object normalization
because the shared ARM reader otherwise accepts only empty 200/202/204. The HTTP
status, headers and nonempty body bytes remain unchanged. Tests exercise actual
empty wire bodies rather than substituting JSON null. Existing GET behavior for
ordinary resources and status endpoints is unchanged.

The new tests cover native examples, strict owner/operation boundaries, two-stage
polling, restart/receipt tampering, request IDs, private canaries, failed/missing
callbacks and operation success while the resource remains present. This is local
protocol evidence, not live-cloud or independent-emulator validation. The cleanup
controller must still compose reviewed work cancellation, dependencies, deletion
and every resource's own absence readback; no Synapse cleanup binding is enabled
by this transport milestone.


### Reviewed Spark pool cleanup

The Spark pool specification now binds its native DELETE to a specialized action.
Inventory drains the native pool pages and adds a privately signed review before
computing its client cursor. The review includes pool/workspace/resource-group
configuration and all observed job/session and independent artifact dependencies.
Known work survives list omission until its own GET proves absence. Credential
rotation allows a new inventory review after fresh scoped reads; old action proofs
and receipts cannot be reused with a changed credential incarnation.

Only ready, unprotected contexts without matching or unresolved artifact consumers
are actionable. The driver repeats live configuration, work, protection and lock
checks. New work, changed incarnations/configuration and new consumers require a
fresh review. It cancels one job/session at a time, returns the acknowledgement
before further reads, and persists a signed phase with its accepted targets. On
restart it waits for the accepted target to stop or disappear, never resending
that cancellation. It deletes the pool after reviewed work has stopped, persists
the native ARM receipt, and queries its scoped operation. Final completion still
requires the pool's own GET 404; historical job records are not closed implicitly.

Real SQLite scan/plan/execution tests reopen the database and instantiate a fresh
runtime at each phase. They cover transient read failure after acceptance, one
native mutation per target, polling success while the pool still exists, final
own absence and retention of historical job/session assets. Protocol tests also
cover changed reviews, new locks/consumers, altered receipts, credential rotation
and cursor drift. These are local tests; no fresh live-cloud or independent emulator
validation is claimed. Native APIs do not provide an atomic cross-resource lock
or conditional cancellation incarnation, so concurrent changes after the last
validated read cannot be excluded atomically. Complete Synapse parity remains open.
