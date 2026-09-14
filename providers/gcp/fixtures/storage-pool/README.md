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
restart; a complete native list reconciles a missing pool. No pool deletion or
complete disk-member enumeration is claimed by these tests.

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
snapshots; Exapool deletion requires the account team. Complete native membership,
supported cleanup, cross-project sharing behavior and physical-isolation parity
remain to verify. Inventory alone does not complete the provider parity matrix.
