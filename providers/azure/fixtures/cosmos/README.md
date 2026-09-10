# Cosmos DB management API evidence

The 34 resource rules select 111 operations from the stable **2026-03-15**
Microsoft.DocumentDB specification at commit
`e45039baa985c442877529906e705982a6e0099d`. The original OpenAPI file's SHA-256 is
`8e2ae27e14553e04b3152de7aec4cf5e4b8cb551242a94d17fca69b82b74ae9b`.
The snapshot includes its transitive common-type definitions. `sources.json`
records exact URLs and complete file hashes for 117 unchanged official examples.

The rules cover accounts; NoSQL databases, containers, stored procedures,
triggers, functions and client encryption keys; MongoDB databases, collections,
custom roles and users; Cassandra keyspaces/tables; Gremlin databases/graphs;
Table tables; the five APIs' ARM role definitions/assignments; account services,
notebooks and private endpoint connections; managed Cassandra clusters/data
centers; and fleets, fleetspaces and account associations. Thirty-three types
have native DELETE operations. Client encryption keys require their database's
reviewed lifecycle. Nine throughput GETs enrich owners; throughput is not a
separately deletable resource. Managed Cassandra status is an additional read.

## Native schemas and identity

`TestCosmosNativeSchemasAndProvenance` verifies source hashes and validates 81
GET/LIST bodies against original Draft 4 schemas offline. Seventy-nine pass
unchanged. SqlTriggerGet and SqlTriggerList contain literal `triggerType` and
`triggerOperation` placeholders outside their enums. Tests require those exact
failures and validate a separate in-memory copy using `Pre` and `All`. Neither
source examples nor schemas are rewritten.

Several examples use different request/response names or groups and collection
aliases such as `sqlContainers`, including nested JavaScript resource paths.
Only explicit selected aliases are accepted. The composed scenario assigns
consistent parents and references while preserving source files unchanged.

Native requests preserve case-sensitive data-resource names. Canonical graph
identities remain compatible with ARM; same-page and cross-page case-only
collisions invalidate the scan. Credential-keyed bindings prevent changed
persisted selectors or operation receipts from reaching HTTP. Parent/ancestor
identities and configuration bind every continuation. The common 128 KiB product
cursor bounds retained page/resource hashes; oversized continuations fail instead
of claiming complete absence. See Microsoft's [case-sensitive names](https://learn.microsoft.com/en-us/azure/cosmos-db/troubleshoot-not-found)
and [resource model](https://learn.microsoft.com/en-us/azure/cosmos-db/resource-model).

Accounts and managed Cassandra clusters are global controllers. Their top-level
location describes resource-group metadata. Data-center deployment regions come
from `properties.dataCenterLocation`. Global parents remain available to regional
network scans, with explicit subnet/VNet references. Account kind/capabilities
select applicable child collections. Contradictory markers, unsupported kinds
and failed lists never become empty collections.

## Review, protection and deletion

Independently deletable children are reviewed prerequisites. Built-in roles and
client encryption keys are controller impacts with independent final GET checks.
Retaining a prerequisite or impact blocks its controller. Account endpoint indexes
reconcile with native lists; two complete child reads reject changed membership
or private configuration. Each reviewed child must disappear even after its
parent does.

Role assignments precede definitions. MongoDB inheritance and resident roles/users
add shared prerequisites for referenced roles/databases; its four documented
built-ins have no invented custom-role resource. Assignable scopes may name future
data resources, so they remain validated configuration rather than ownership.
Container encryption references stay within their database. See the native
[role schema](https://learn.microsoft.com/en-us/rest/api/cosmos-db-resource-provider/sql-resources/get-sql-role-definition)
and [MongoDB roles](https://learn.microsoft.com/en-us/azure/cosmos-db/mongodb/role-based-access-control).

Account cleanup discovers incoming Fleet associations throughout the subscription.
Fleet/fleetspace deletion unlinks associations and preserves target accounts.
Unlinking checks target configuration, tags, inherited locks, managed-group
ownership and a final target read. Search shared links can read and protect
Cosmos accounts through their actual API. External network endpoints, subnets,
identities and key vaults remain separate resources.

Owner/ancestor snapshots bind native creation IDs and configuration, including
private values through credential-keyed digests. Data-write ETags/timestamps,
controller indexes and service status clocks are checked separately. Large JSON
integers retain precision. JavaScript bodies, wrapped encryption keys and managed
Cassandra secrets/configuration stay out of inventory and HTTP logs while their
changes still invalidate review.

Throughput reads bind dedicated RU/s or autoscale settings. A 404 establishes no
dedicated offer only after the owner is re-read unchanged and its options do not
declare an offer. Denied, malformed, mismatched and partial reads are not absence.
Pending offer changes, invalid numbers, backup migration and nonterminal resource
states protect cleanup. No restore, purge, key retrieval, throughput mutation or
forced unprotection operation is selected.

## Original CLI responses

`cli-recordings.json` retains 386 selected GET/DELETE responses from 17 Microsoft
Azure CLI recordings at commit `dc50d475a00ded4a1a1980d4a10a9fbd9a750a81`.
The extractor verifies original YAML hashes, retains unchanged response bodies
and selected operation/request headers, and copies no request bodies. Reproduce
from the downloaded pinned YAMLs:

```sh
python3 providers/azure/fixtures/cosmos/reproduce_recordings.py /path/to/upstream-recordings
```

Twenty-four cases replay native 202 DELETE responses, signed operation URLs and
recorded queued/success states. The recorded **2026-03-15** API exactly matches
the selected version. Only the zero subscription UUID is rebased in memory.
Some ancestor GETs use bodies originally returned by LIST. Unrelated child/role/
Fleet collections, locks and resource groups are synthetic supporting responses;
these are response replays, not complete cloud scenario replays.

The recordings stop at operation success and contain no final target GET 404s.
Tests prove success does not finish while the target exists, then inject a labeled
synthetic final 404 and verify idempotent resume without another DELETE. Requests
and results survive serialization and driver restart. Separate tests cover
denied/partial responses, failed/canceled/expired operations, incomplete readback,
changed selectors/receipts and signed-query log sanitization. Polling permits
global ARM or a region-matching ARM host with a resource-bound receipt.

## Verification boundary

Microsoft's [Cosmos DB emulator](https://learn.microsoft.com/en-us/azure/cosmos-db/emulator)
provides data APIs rather than the selected ARM surface. Independently reviewed
Floci-AZ **0.12.0**, commit
[`f6f0292880c6eb4e7fe3d658185030665f98166e`](https://github.com/floci-io/floci-az/tree/f6f0292880c6eb4e7fe3d658185030665f98166e),
has Cosmos data handlers but no matching DocumentDB account management routes in
its Java source. Neither is claimed as independent verification of this ARM
implementation. No live Azure mutation was performed.

Separate MongoDB vCore/PostgreSQL offerings, backup/restore inventory and individual
data-plane documents are not covered by these rules. Account capability differences
and external endpoint effects still need live acceptance; unreadable collections
fail closed. Native DELETE has no conditional ETag parameter, so preflight cannot
eliminate the final read/delete race.
