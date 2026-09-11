# Data Factory native evidence

Fourteen resource rules and 54 operations use the native `2018-06-01` API.
The unchanged [OpenAPI source](https://github.com/Azure/azure-rest-api-specs/blob/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/datafactory/resource-manager/Microsoft.DataFactory/DataFactory/stable/2018-06-01/openapi.json)
has SHA-256 `4909c6f99d7bb77fd1176d80961ae36a1000c22f2996d18d71ccd14111a597c0`.
Its selected operations and transitive definitions are retained in the offline
catalog. `sources.json` identifies and hashes 54 unchanged example files;
contract tests verify 76 responses, including 35 bodies against native schemas.
The schema projection contains 506 typed reference shapes, including native
discriminator variants. It excludes arbitrary user parameters, scripts and JSON
payloads when discovering dependencies.

`cli-recordings.json` retains 64 original response bodies from two Microsoft CLI
recordings at commit `d2f60986756c939c3d6d7f85e89798cca158d935`: 59 from
`test_datafactory_main.yaml` and five from
`test_datafactory_managedPrivateEndpoint.yaml`. Their complete-source SHA-256
values and zero-based interaction indexes are pinned in
`reproduce_recordings.py`. Only response content-type and operation-location
headers are retained. Selected POST request bodies are copied without changes;
request credentials and credential-producing operations are excluded. The set
contains 42 GET, eight POST and 14 DELETE responses, all at the selected API
version. Native Stop polling, final result and resource bodies are distinct.

Reproduce the extracts from the original sources, and run offline checks:

```sh
python3 providers/azure/fixtures/datafactory/reproduce_recordings.py --check
python3 scripts/sync-datafactory-references.py --check
go test ./providers/azure -run '^TestDataFactory' -count=1
```

The unchanged sources include discrepancies that tests preserve explicitly:

- Credential examples copy an unrelated linked-service name. Private endpoint
  connection examples return the parent factory ID. Runtime validation rejects
  those inconsistent identities; composed family fixtures correct them openly.
- `ifNoneMatch` is an SDK spelling for the native `if-none-match` header. Some
  list examples contain an extra null SDK parameter. Wire tests bind the actual
  header only where the native operation declares it.
- Unfinished pipeline runs have null completion fields despite non-nullable
  schema properties. CDC status is a JSON string, and recorded Cancel returns
  an empty JSON string. These operation-specific forms do not relax ordinary
  resource GET validation.
- Own-node and GetStatus examples represent different registration timestamps
  and limits. Composed fixtures join the same node incarnation explicitly.
- Native Stop finishes with an empty HTTP 200 at its final Location, after the
  separate status URL succeeds. An empty ordinary GET or JSON null cannot prove
  deletion. Recorded factory DELETEs leave dataflows or managed networks in the
  preceding inventory; the recordings do not contain their final own GETs.

Composed protocol tests exercise complete family reads, pagination, known omitted
resources, native node spelling, private configuration, typed references,
metadata cycles, shared Key/RBAC runtimes and work census. Graph/action tests
reject changed identity, configuration, members, locks, protection, new work and
unselected consumers. The real SQLite scan, graph, plan and execution workers
use generated asset IDs and resume with fresh runtimes and database handles.
SSIS Stop, run cancellation, debug deletion and whole-factory RemoveLinks receipts
remain durable across retries; parent absence still requires recorded descendant and required-consumer
GETs, including external consumers that reappear after host deletion. These are retained protocol and recording tests, not a live-cloud run or
independent Data Factory emulator verification.

The [Floci-AZ service list](https://floci.io/az/) checked on 2026-09-11 does not
list Data Factory. Its [generic ARM handler](https://floci.io/floci-az/services/arm/)
documents provider fallback, permissive authentication and non-cascading group
deletion; those behaviors do not establish Data Factory lifecycle compatibility.
Microsoft's [self-hosted runtime](https://learn.microsoft.com/en-us/azure/data-factory/create-self-hosted-integration-runtime)
is a registered worker connected to the cloud service, not an ARM emulator.

Native DELETE has no conditional version guard. Full returned configuration is
compared privately, but masked or omitted secrets cannot prove a secret's
underlying value; not every artifact has an independent creation identifier.
Double observations detect changes rather than provide an atomic snapshot.
Work discovery covers the service-visible run history and debug sessions and
rereads known run IDs; it does not establish inaccessible historical work.
Unresolved shared links, including foreign subscriptions, block host cleanup.
External data stores, compute, identities, networking and self-hosted runtime
machines remain independent; deleting a node removes its registration only.
No purge, backup erasure, source-data deletion or billing-stop guarantee is made.

Lifecycle references: [SSIS stop/delete sequence](https://learn.microsoft.com/en-us/azure/data-factory/manage-azure-ssis-integration-runtime),
[shared self-hosted runtimes](https://learn.microsoft.com/en-us/azure/data-factory/create-shared-self-hosted-integration-runtime-powershell),
[event unsubscription](https://learn.microsoft.com/en-us/rest/api/datafactory/triggers/unsubscribe-from-events?view=rest-datafactory-2018-06-01)
and [debug-session deletion](https://learn.microsoft.com/en-us/rest/api/datafactory/data-flow-debug-session/delete?view=rest-datafactory-2018-06-01).
