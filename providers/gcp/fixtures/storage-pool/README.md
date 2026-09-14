# Hyperdisk Storage Pool evidence

Native Compute v1 metadata was fetched from
https://www.googleapis.com/discovery/v1/apis/compute/v1/rest at revision `20260908`.
The complete upstream response SHA-256 is
`30f29098aad84c7c4fb51eb657deebaec233799f13877e1cd0bb25738a02b4fd`.
The five unchanged `compute.storagePools` methods (`aggregatedList`, `list`,
`get`, `listDisks`, `delete`) and 26 transitive schemas are retained as a separate
fragment in `../../catalog/source/discovery.json`. Earlier source documents and
all 768 prior generated operations remain unchanged. The method selection is
retained in `../../catalog/source/selection.json`; ordinary generation is offline.

`storage_pool_test.go` uses composed native responses, not live recordings. It
checks actual REST paths, empty-page continuation, zonal/regional scope, native
state versus structured usage, large integer precision, alternate usage fields,
unknown/future pool metadata, labels, project/host/zone/type/name conflicts,
duplicates, partial/denied lists, cursor binding and disk-reference forms.
`storage_pool_inventory_worker_test.go` uses actual SQLite and the registered
inventory worker: failed/partial scans preserve resources after runtime/database
restart; a complete native list reconciles a missing pool. Member403 and partial
lists preserve prior member records; a complete empty member list updates them.
`storage_pool_members_test.go` verifies the native paginated `listDisks` wire
contract, empty-page continuation, large disk integer values, attachments and
policies, member network closure and out-of-region request isolation. It rejects
malformed response kinds/arrays/tokens, duplicate members across pages, failed
later pages, token cycles, invalid project paths, foreign hosts/zones and regional disk identities.
Final native pool GET rejects changed id, creation timestamp, selfLink or zone,
and denied/missing parents. These tests do not authorize deletion.

The previously verified Google mockgcp revision
`673a61419de1b8e4f7d26070ce20dde2daa61da8` has no Storage Pools implementation in
its [mockcompute directory](https://github.com/GoogleCloudPlatform/k8s-config-connector/tree/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockcompute).
Its service registration (`service.go`, SHA-256
`af6e717b093c7401b861178848685756b198108f2fba74d7f9789a247cf93a9e`) registers Disk
and RegionalDisk services but no StoragePools service. No emulator or live cloud
was started for this milestone. This is a limitation of that pinned mock, not a
claim that no other emulator can exist.

[Google's management guide](https://docs.cloud.google.com/compute/docs/disks/manage-storage-pools)
requires Storage Pool disks to be deleted before the pool and preserves disk
snapshots; Exapool deletion requires the account team. The native
[listDisks contract](https://docs.cloud.google.com/compute/docs/reference/rest/v1/storagePools/listDisks)
returns StoragePoolDisk summaries, uses maxResults/pageToken and requires
`compute.storagePools.get`. The [pool limitations](https://docs.cloud.google.com/compute/docs/disks/storage-pools)
describe same-project/zone disks and exclude regional disks, but the newer
[official error catalog](https://docs.cloud.google.com/compute/docs/reference/rest/v1/errors)
explicitly documents same-organization cross-project sharing and consumer-project
allowlists. Therefore member discovery accepts valid same-zone disk summaries in
other projects, retaining sharing metadata without foreign-project API requests
or actionable foreign assets. A dedicated test exercises that boundary. Member summaries do not replace authoritative
Disk inventory and do not provide a transactionally frozen membership snapshot.
Supported cleanup, independent emulator evidence and physical-isolation parity
remain unfinished. Inventory alone does not complete the provider parity matrix.
