# Synapse native ARM contract evidence

These 23 files are **unmodified Microsoft Swagger examples**, pinned to
`Azure/azure-rest-api-specs` commit
`c20bf553ad64f20c6d5e3f56080380c086cb1fde`. `sources.json` records each upstream
URL, SHA-256, source document, method, path and operation. They are not recordings
from a live subscription or responses from an independent Synapse emulator.

The catalog contains 22 operations from the stable **2021-06-01** contract:

- Workspaces: subscription/resource-group list, get and delete.
- Spark big-data pools: workspace list, get and delete.
- SQL pools: workspace list, get, delete, pause, resume and operation-result get.
- SQL pool restore points: list, get and delete.
- SQL pool replication links: list and get; management-operation list and user activity get.
- Restorable dropped SQL pools: workspace list and get.

The three ARM documents and four transitive reference documents carry the hashes
of their complete upstream files. Existing catalog operations and resource
bindings are unchanged. No Synapse resource specification, inventory source,
relationship, cleanup action driver or product-parity claim is added in this step.
The matrix's five Synapse mappings remain in `unimplemented_resources`.

## Reproduce the local checks

From the repository root:

```sh
python3 -B scripts/test_sync_azure_catalog.py
go test ./providers/azure -run 'TestSynapseNativeContracts|TestCatalogReproducibleAndSpecsExecutable' -count=1 -v
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
