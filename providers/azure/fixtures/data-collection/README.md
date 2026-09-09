# Azure Monitor data collection evidence

The eleven Monitor JSON examples are unchanged Microsoft REST examples at
[azure-rest-api-specs e45039b](https://github.com/Azure/azure-rest-api-specs/tree/e45039baa985c442877529906e705982a6e0099d/specification/monitor/resource-manager/Microsoft.Insights/Insights/stable/2024-03-11).
Each original URL and file SHA-256 is retained in sources.json. The full native
dataCollection.json SHA-256 is
78e41e00ae17c4099d855d933fc18ab2b88728e7730ed9760525cbc80893df8c.
The versioned catalog retains eleven GET/LIST/DELETE operations and their
transitive native schemas; normal generation and tests need no network.

The native association is an extension resource on its monitored ARM resource,
with a resourceUri path parameter. The rule and endpoint association lists
return these external IDs, not nested children of the collection target.
The official endpoint-list example has inconsistent request and response
endpoint names. The tests retain and reject that mismatch; positive fixtures
change only scoped identities and linkage to the explicit scenario values.
The three native GET examples also pass an independent JSON Schema validator
against the retained Swagger definitions.

Three additional unchanged Resource Graph examples at the same commit retain
the 2024-04-01 query and pagination envelopes. Their request and response bodies
pass the independent native-schema validator. The full resourcegraph.json
SHA-256 is b9cc4a440858bb51f10474cd36208dee324ddbddb8ce06fd31378efea5a6d111.
The paging test retains the native tokens, rebinds identities/types and changes
the total to six; completion after its second page is a synthetic test boundary.
The fixed association query uses Microsoft's documented
[insightsresources table](https://learn.microsoft.com/en-us/azure/governance/resource-graph/samples/samples-by-category#list-vms-with-data-collection-rule-associations)
and the native [Resources operation](https://learn.microsoft.com/en-us/rest/api/azureresourcegraph/resourcegraph/resources/resources?view=rest-azureresourcegraph-resourcegraph-2024-04-01).

## Recorded Microsoft CLI responses

cli-delete-recordings.json contains selected native responses from
[azure-cli-extensions b10329b](https://github.com/Azure/azure-cli-extensions/tree/b10329ba54a03c1d0da9e7ed862f83b36e8a177f/src/monitor-control-service/azext_amcs/tests/latest/recordings).
The manifest records the source URL, full YAML checksum and interaction indices.
The recorded version is 2023-03-11, earlier than the runtime's 2024-03-11.

| Native recording | Resource response | DELETE | Full YAML SHA-256 |
| --- | --- | --- | --- |
| test_amcs_data_collection_rule.yaml | GET 3 | 5 | 4f8d428504c2753dcaabb216cf94ebe87368db5a35c53f76499ce510f61cbff8 |
| test_amcs_data_collection_endpoint.yaml | GET 6 | 8 | d1d9000711991da88e0a8e87cd507aa0df605bad991d1222d260e7b9c0e958d1 |
| test_amcs_data_collection_endpoint_association.yaml | PUT response 22 | 23 | 192f5de256176dbfdee45698cd67506e68fa90b8ea2635d27b63ea574a30efb2 |

The association was updated after its last GET, so its fixture uses the native
response to that update; the test does not execute PUT. Audit user strings are
replaced with recording-user, unrelated headers are omitted, and native bodies,
creation timestamps, ETags, deletion URLs and HTTP 200 empty responses remain.
The DCR recording explicitly sends deleteAssociations=false. Runtime tests
rebind the subscription and API version, then compare the emitted native DELETE
path and query. Empty reverse lists, surrounding permission reads, replayed GET
responses and final GET 404 are protocol fixtures, not recorded cloud outcomes.

To reproduce the extraction, download the three immutable source URLs from the
manifest and run reproduce_recordings.py with that directory (requires PyYAML).
The script verifies all source checksums before writing the retained extraction.

## Verified behavior and limits

The native product scans use subscription-wide rule/endpoint lists, both reverse
association indexes, full resource GETs, paging and parent-bound cursors. A fixed
Resource Graph query names only the connected subscription and discovers orphan
links whose targets were deleted or belong to another subscription. Native GET
and ListByResource verify these seeds; ordinary transport and generic Invoke
continue to reject global query URLs. Results with a missing page, changed count,
duplicate identity, invalid scope, repeated token or failed native read fail the
scan. Cursors also bind the orphan identities and their native configuration.
Associations inherit their live collection target's region; orphans use the
indexed location, or global when absent. Dual-target associations prefer a live
local rule, then an endpoint. Their native indexes must agree before deduplication.
Foreign-subscription and tenant-global association IDs fail the scan. This
connection does not authorize tenant-level monitoredObjects operations.

The actual planner includes required association deletions without claiming
exclusive ownership. Shared prerequisites execute once when both targets are
selected, and retention blocks deletion of a referencing target. Native
preflight reconciles two complete lists and GETs, verifies frozen configuration,
creation identities, locks and managed-group restrictions, and checks every
reviewed prerequisite absent before deleting its target. DCR deletion fixes
deleteAssociations=false; no forced cascade or conditional header is invented.
Independent association deletion stops collection without deleting the monitored
VM/cluster, destination, storage account or identity. An AKS managed-group cascade
cannot infer ownership of an association outside its resource group.

Tests exercise native paths, all three delete actions, delayed absence and
serialized recovery, shared planning, retention, malformed/duplicate/foreign
members, backlink changes, partial and denied lists, changed configuration and
creation IDs, paging failures/cycles and changed parent sets. Blob enrichment
URL credentials, query strings and fragments are removed from inventory and
native API logs without changing the live response.

These are native recorded-response and protocol tests, not an independent
emulator deployment or Steward real-cloud acceptance. Native DELETE lacks an
atomic ETag condition. Configuration snapshots omit transient provisioning
state, audit modification fields and signed URL parameters; identical
recreation cannot be distinguished if the provider omits creation identity.
Default Azure Monitor workspace managed-group cleanup is a separate remaining
integration; registration of these three resource types does not complete it.
Resource Graph is eventually consistent and returns only resources readable by
the credential; allowPartialScopes=false does not override resource-level RBAC.
As the [official overview](https://learn.microsoft.com/en-us/azure/governance/resource-graph/overview)
explains, no result-level signal guarantees that restricted access is complete.
A subscription inventory credential therefore needs read access throughout the
subscription. This supplementary index cannot prove absence of an unindexed or
inaccessible orphan. Newly created links may require a fresh scan after indexing;
native action preflight and readback remain mandatory. Queries above 100,000
records fail explicitly instead of returning a partial completed inventory.
