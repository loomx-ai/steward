# Google Cloud API metadata

`source/discovery.json` contains selected native method and schema objects from
Google's Discovery documents, their original source URLs, and SHA-256 hashes of
the complete upstream responses. Transitive schema references are retained.
`source/selection.json` is the reviewed method/resource selection for refreshing
that snapshot. These are metadata files; neither file contains credentials.

To refresh upstream metadata from the repository root:

```sh
python3 scripts/sync-google-catalog.py
go generate ./providers/gcp
go test ./providers/gcp ./internal/provider/catalog ./internal/provider/spec
```

Normal builds, generation, and tests are offline. Review source and generated
diffs together after a refresh. The catalog records real Google method IDs,
versions, HTTPS origins, paths, parameters, response schemas, paging, idempotency
parameters, and source provenance. Resource bindings enumerate the actual
global/regional methods and participate in the catalog and spec revisions.
Resource rules live in `../specs`, with explicit dependency targets and readback
operations. Runtime path matching checks those bindings and the selected project
before constructing a request.

Known resource kinds use their native product list methods. Compute aggregate
responses are routed by their actual zonal/regional/global identity; child kinds
use explicit parent discovery. Product cursors bind the connection, scope, rule
revision, list targets and parent identities. Partial-result warnings and
unreachable locations fail the shard. Cloud Asset Inventory remains a broad,
non-authoritative index for kinds without product rules; it does not overwrite
or close the resources owned by product shards. Network target selection uses
live Compute list methods.

Cloud KMS rules include key rings, keys, versions and import jobs. The supported
delete operation removes eligible resource records and polls the native
operation before checking absence. It does not schedule destruction of key
material. Import jobs have no delete method. Version state/import restrictions,
automatic rotation, remaining versions/keys and unexpired import jobs are checked
against live APIs before deletion.

Compute instance attachments use native disk `source`, `deviceName` and
`autoDelete` fields. Boot and data disks, including regional disks, contribute
explicit lifecycle impact. Retention uses `compute.instances.setDiskAutoDelete`;
instance deletion protection is disabled through its native method. Preparation
and deletion operations have separate idempotency keys and resume through the
persisted waiter state. A completed operation must also be reflected in a live
read before deletion proceeds. Unreviewed or changed attachments block cleanup.
Local SSDs are integrated instance storage and have no separate disk resource.

Reference material:

- [Google Discovery directory](https://www.googleapis.com/discovery/v1/apis)
- [Cloud Asset Inventory asset types](https://docs.cloud.google.com/asset-inventory/docs/asset-types)
- [Compute REST API](https://docs.cloud.google.com/compute/docs/reference/rest/v1)
- [Disk deletion policy](https://docs.cloud.google.com/compute/docs/disks/modify-persistent-disk)
- [Google API resource names](https://cloud.google.com/apis/design/resource_names)
- [Cloud KMS resource deletion and restrictions](https://docs.cloud.google.com/kms/docs/delete-kms-resources)

The checked-in tests establish metadata consistency and protocol behavior. They
do not establish live permissions, eventual inventory consistency, service
availability, or independent emulator coverage.
