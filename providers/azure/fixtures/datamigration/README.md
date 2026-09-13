# Data Migration native evidence

Eight rules cover classic services, projects, tasks, project files and service
tasks, SQL/Mongo migration services, and target-scoped database migrations.
The last rule binds five distinct native routes: SQL servers, managed instances,
SQL virtual machines, Cosmos DB Mongo RU accounts and Mongo vCore clusters.
Forty-five DMS operations use `2025-06-30`; two supporting target GETs use SQL
`2023-08-01` and SQL Virtual Machine `2023-10-01`. Supporting reads do not add
cleanup rules for those target types.

The pinned Azure REST specification commit is
`c20bf553ad64f20c6d5e3f56080380c086cb1fde`. The complete root sources are:

- [datamigration.json](https://github.com/Azure/azure-rest-api-specs/blob/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/datamigration/resource-manager/Microsoft.DataMigration/DataMigration/stable/2025-06-30/datamigration.json),
  SHA-256 `3aea3b0618b01bbac4df7cddbfc4b6f0d50fdd15dbf78b665dd8a1ed7e5e2645`.
- [sqlmigration.json](https://github.com/Azure/azure-rest-api-specs/blob/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/datamigration/resource-manager/Microsoft.DataMigration/DataMigration/stable/2025-06-30/sqlmigration.json),
  SHA-256 `8eb2a93a1f197e734db716ad96823aff3e66d8a6b4ad8269d0c6a596cc81c000`.

The offline catalog retains native operations, transitively referenced schemas
and explicitly selected task discriminator subtypes whose separate files only
reference the base type. `sources.json` pins 56 unchanged official example files.
Tests bind their requests and validate 43 bodies across 76 responses against the
retained schemas. Native source discrepancies remain explicit:

- Mongo RU GET examples copy vCore cluster IDs and scopes. SQL MI GET/DELETE
  examples use an inconsistent scope without its leading slash. Runtime identity
  validation rejects these responses; composed fixtures correct their identities.
- Mongo GET examples include an unsupported SQL-only `$expand`. SQL MI/VM DELETE
  examples carry an undeclared body, and SQL VM GET has an extra empty body.
  Request tests remove only those undeclared parameters; production sends only
  fields declared by the operation.
- Modern service list examples pass a resource path as `subscriptionId`.
  Binding rejects it before the example is tested with its UUID.
- Mongo migration `Creating` and one SQL MI target `Updating` example conflict
  with their schema enums. Tests assert the discrepancy and validate the other
  fields. Busy or unverified migration states cannot authorize cleanup.

`cli-recordings.json` contains 107 original response bodies: 93 GET, seven POST
and seven DELETE responses. Forty-eight come from the modern extension's
`test_datamigration_Scenario.yaml` at
`d2f60986756c939c3d6d7f85e89798cca158d935`, using `2025-06-30`.
The other 59 come from the classic CLI's project, service and task recordings at
`8bead7f93f086629efb160d56c25f508156925bf`, using `2021-06-30`.
Each complete YAML source hash and selected interaction index is pinned in
`reproduce_recordings.py`. Request credentials are excluded; signed URL values
`t`, `c`, `s`, `h` are replaced by deterministic `replay-*` placeholders in both
request URLs and response headers. Response bodies and selected POST bodies
remain unchanged. Only content type and operation-location headers are retained.

Replay covers 13 mutation receipts, 36 polls, 47 resource bodies, five collection
responses, five own-resource 404s and a monitoring response. The classic version
is retained as historical wire evidence, not current-version live acceptance.
Current runtime requests use `2025-06-30`; the old version is allowed only on a
fully signed classic service-delete polling URL received from ARM.

Reproduce the extracts and run the checks:

```sh
python3 providers/azure/fixtures/datamigration/reproduce_recordings.py --check
python3 -m unittest discover -s scripts -p test_sync_azure_catalog.py
go test ./providers/azure -run '^TestDataMigration' -count=1
go test -race ./providers/azure -run '^Test(DataMigration|DataFactoryRegistered)' -count=1
```

Native SQL Cancel can return HTTP 200 with a partial resource body and an async
receipt. The poller follows `Azure-AsyncOperation` when present, otherwise
`Location`, as specified by [ARM](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/async-operations).
It retains the exact receipt privately and validates host, subscription, version,
operation family, phase and UUID. Native regional operation locations may differ
from the service's execution region. The recording demonstrates typed SQL DB
cancel/delete URLs; the SQL MI/VM examples use generic operation URLs. Support
for corresponding typed MI/VM deletion paths is a bounded family mapping, not
recorded MI/VM deletion evidence. Empty successful Location responses do not
relax ordinary resource GET validation.

Composed protocol tests walk classic descendants, modern service indexes and
independent Mongo target indexes, then reread resources and known omissions.
SQL has no target-scoped migration LIST in the selected native contract; unknown
SQL migrations omitted from all service indexes cannot be recovered. Private
configuration, target, ancestor, group, lock and registered-node context is bound
across inventory, graph and execution. Typed SQL MI subnet and SQL VM compute
references preserve network scope without assigning ownership of those targets.
Arbitrary connection strings, scripts and input objects do not create edges.

Classic child deletion is reviewed and ordered; active tasks are canceled before
DELETE with `deleteRunningTasks=false`. File consumers and modern service-linked
migrations require reviewed prerequisite deletion. SQL migrations cancel using
their recorded migration operation ID. Mongo has no separate Cancel operation;
active selected migrations use its native `force=true` DELETE. SQL service
cleanup waits for zero running node jobs, removes reviewed registrations using
`deleteNode`, confirms their absence, then deletes the service. External runtime
machines and source/target databases remain independent.

The real SQLite scan, graph, plan and execution workers scan 16 regional shards
and execute a 12-step plan. Fifty-four fresh database/runtime/worker invocations
verify durable intent and receipts for 19 mutations: five cancellations, two node
removals and 12 deletions. Persisted accepted mutations are not repeated. Each
asset closes only after its own and every recorded descendant/required consumer's
absence, including after a parent disappears. Negative tests cover forbidden or
changed reads, new work/members, forged receipts, unselected consumers, locks,
protection, pagination, expired polls, surviving orphans and private-data logs.

These are protocol, official recording and SQLite worker tests. The
[Floci-AZ service list](https://floci.io/floci-az/services/) checked on 2026-09-13
does not list DMS. Its [generic ARM fallback](https://floci.io/floci-az/services/arm/)
does not establish DMS cancellation, node registration or deletion semantics.
Independent DMS emulator and live-cloud acceptance remain open.

Native DELETE has no conditional version parameter. Private comparison cannot
detect underlying changes to secrets Azure masks or omits; repeated observations
are not an atomic cloud snapshot. Verification is bounded to 24 hours and does
not establish exactly-once writes across a crash before receipt persistence,
purge, backup erasure, source-data deletion or when billing stops.
