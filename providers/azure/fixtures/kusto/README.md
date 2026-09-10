# Azure Data Explorer management API evidence

Ten rules cover `Microsoft.Kusto/clusters` and its attached database
configurations, databases, data connections, database/cluster principal
assignments, scripts, managed private endpoints, private endpoint connections
and custom sandbox images. All use stable **2025-02-14**. The catalog retains
36 native operations and 42 unchanged examples from
[azure-rest-api-specs commit e45039b](https://github.com/Azure/azure-rest-api-specs/tree/e45039baa985c442877529906e705982a6e0099d/specification/azure-kusto/resource-manager/Microsoft.Kusto/Kusto/stable/2025-02-14).
The complete `kusto.json` SHA-256 is
`6c09537668b6efc3a76e6a96f29572aac2457d4188c162f8c74cd8f1690bc59b`.
`sources.json` records each original example URL and hash. Thirty-one native
response bodies pass their original schemas offline, with six additional
concrete database/data-connection discriminator checks. Provider operation
definitions, SKUs, private-link group definitions and outbound-network metadata
are not independent resource rules.

## Lifecycle and scope

Clusters and databases enumerate complete native child collections. Listed
children receive native detail reads; two complete walks compare membership and
private configuration, and reread the parent and its indexes. Denied, partial,
malformed, duplicate, foreign or cycling pages cannot establish absence.
Inventory continuations bind their ordered parent set, all ancestors, private
configuration and the existing 128 KiB cursor limit. Proxies inherit the actual
cluster region, including the native managed-endpoint `DummyLocation` value.
This value is also documented in Microsoft's
[managed endpoint guide](https://learn.microsoft.com/en-us/azure/data-explorer/security-network-managed-private-endpoint-create).

The source cluster's GET `listFollowerDatabases` index identifies attachments on
other clusters. Its nested `properties` shape is distinct from the POST list
response. Native attachment reads must confirm source, selector and any returned
table-sharing settings. Source cluster/database deletion requires the reviewed
attachments to be deleted first. These are shared prerequisites, not exclusive
ownership of the other cluster. The follower cluster remains independent.

An attachment is the sole controller of its local `ReadOnlyFollowing` database
views. Their source cluster, original database, attachment name, prefix/override
and returned `attachedDatabaseNames` index must agree. Wildcard attachments
review all matching views. Selecting a read-only view alone points to its actual
attachment controller. Retaining a view blocks detachment; a protected source
does not prevent deleting its follower attachment, but a protected follower
cluster does. Database `isFollowed` and cluster private-endpoint indexes must
agree with the complete lists, including the final parent reread. These choices
follow Microsoft's [follower lifecycle](https://learn.microsoft.com/en-us/azure/data-explorer/follower).

Independent children, including writable databases, have reviewed DELETE steps
before their parent. Active custom images are controlled by the cluster through
`languageExtensions`; they cannot be deleted independently. Their final native
absence remains necessary after cluster deletion. Script deletion removes the
ARM script registration and **does not undo commands it executed**, as described
in [database scripts](https://learn.microsoft.com/en-us/azure/data-explorer/database-script).
Concurrent script operations may return the service's maintenance error; no
query-engine rollback or data-plane cleanup is implemented.

Managed private endpoints bind the target's native resource API and private
configuration. Preflight checks target identity, inherited management locks,
protected tags and managed-group ownership, then rereads the target. External
data resources are preserved. Target families without a selected native API and
cross-subscription links fail closed; IoT Hub and Digital Twins target lifecycles
remain outside this batch. Other connections retain references to modeled
identities, subnets, Event Hubs/consumer groups, storage and Cosmos resources.
Unknown external kinds remain native configuration, not invented ownership.

Managed resource-group cleanup also checks Kusto readiness and linked-target
protection. It cannot claim an external follower attachment as a resource that
Azure deletes with the group. Such attachments must be detached separately
before reviewing the group cleanup; group deletion does not implement that
external prerequisite automatically.

Configuration digests retain exact large JSON integers, creation metadata,
policies and secret values. Public inventory/logs omit script content, script
SAS tokens, requirements content, and URL credentials/query fragments. Native
provisioning and cluster states gate cleanup separately from the stable digest;
moving database size, modification audit fields and validated child indexes are
handled separately. Updating, migrating or unknown configurations require a
later retry or fresh scan. No migration peer is treated as an owned cluster.

Cluster cleanup removes its data. Azure documents a 14-day soft-delete period
for clusters active for more than 14 days, with recovery through support. An
existing `opt-out-of-soft-delete=true` tag changes that behavior. Steward does
not set that tag, restore a cluster or purge retained state. The plan's separate
database DELETEs run first; cluster soft delete must not be treated as rollback
for those prior actions. See [cluster deletion](https://learn.microsoft.com/en-us/azure/data-explorer/delete-cluster).

## Original CLI responses

`cli-recordings.json` retains 45 selected response bodies and selected response
headers from the official
[Kusto CLI scenario](https://github.com/Azure/azure-cli-extensions/blob/0349eb646d3225db5fd677114e200efdfd11e3f8/src/kusto/azext_kusto/tests/latest/recordings/test_kusto_Scenario.yaml).
The full original YAML SHA-256 is
`653dc385bfeae90470db0f4749ca4d7c2f435b2401d300421a65a8fb4bb90ee1`.
The extracted fixture SHA-256 is
`9d6ca20104ca75ef77bf18abbbd354939c702037269174b8ba1a0879ab321599`.
Request bodies and headers are not extracted. Reproduce with the pinned YAML:

```sh
python3 providers/azure/fixtures/kusto/reproduce_recordings.py /path/to/upstream-recordings
```

Eight cases replay original accepted DELETEs and all their recorded pending and
successful status polls. The interaction indices are zero-based:

| Resource | GET state | DELETE | Status polls |
| --- | --- | --- | --- |
| Managed private endpoint | 183 | 285 | 286–288 |
| Private endpoint connection | 152 | 289 | 290 |
| Script | 190 | 291 | 292 |
| Database principal assignment | 148 | 294 | 295 |
| Cluster principal assignment | 145 | 296 | 297 |
| Data connection | 179 | 303 | 304 |
| Writable database | 63 | 305 | 306 |
| Cluster named `KustoClusterLeader` | 248 | 307 | 308–322 |

The recorded resource API is **2022-02-01**, explicitly bridged to the selected
2025-02-14 resource request version. The response bodies and native LRO URLs
are unchanged except for rebasing the all-zero subscription UUID in memory.
Parent GET 28 and database GET 63 are earlier terminal states, not a replay of
all intervening updates. GET 63 predates attachment and has `isFollowed:false`;
later GETs have `true` and cannot back a synthetic empty follower index.
Supporting resource groups, locks, empty child/follower collections, the managed
endpoint's target Storage account, and final GET 404s are explicitly synthetic.

DELETE 302 returned 204 for an attachment under a different cluster from the
actual recorded attachment; it is retained as an already-absent response and is
**not** a successful native attachment-deletion replay. POST 299 performs leader
detach and is retained with its status/result responses 300–301. Automatic
cleanup instead uses the attachment's native DELETE, covered by its original
Swagger example and the composed lifecycle tests. No native custom-image DELETE
recording is claimed.

Native status URLs contain display-region spaces. Location URLs percent-encode
them and add `operationResultResponseType=Location`. The latter's original GET
301 returns HTTP 200 with an empty body. Only the exact Kusto operationResults
collection, selected subscription/region, bounded native API versions and that
one optional query are accepted. Empty ordinary resource GETs and JSON null
remain invalid. Current examples also return operation API versions 2022-12-29
and 2023-05-02; these precise compatibility cases are bounded explicitly.

Operation receipts bind the target and survive serialized request/result
restoration. Failed/canceled/unknown or partial operations, wrong identities,
tampered receipts, denied final reads and live resources cannot complete cleanup.
Expired operation URLs still require native resource absence. Each reviewed
prerequisite and controller impact must also be absent, including after restart.
The native `CannotAlterFollowerDatabase` failure example is retained unchanged.

## Independent verification boundary

Microsoft's [Kusto emulator](https://learn.microsoft.com/en-us/azure/data-explorer/kusto-emulator-overview)
exposes the local query engine. It has no authentication or managed ingestion
pipeline and does not establish Azure ARM control-plane verification. It was not
run for these management APIs. The reviewed Floci-AZ 0.12.0
[source at f6f0292](https://github.com/floci-io/floci-az/tree/f6f0292880c6eb4e7fe3d658185030665f98166e)
has no Kusto identifiers or management routes in its 331 Java files.
No independent emulator or live Azure mutation is claimed. These native DELETE
contracts have no conditional ETag parameter; repeated reads cannot eliminate
the final read/delete race. Data-plane tables/functions, ingestion processing,
backup/restore and the remaining GCP/Azure application acceptance stay open.
