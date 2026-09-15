# Azure NetApp Files native evidence

The 2025-12-01 Swagger source is pinned to azure-rest-api-specs commit
`07a27fbba41f8597cdfe0f866fcbf9f7c37390f4`. `sources.json` records the complete
source SHA-256, the 37 selected operations and the original URLs and SHA-256
values of 40 unmodified Microsoft examples. The catalog retains the selected
source and its two transitive common-type documents. Existing provider sources
and operations are unchanged. `scripts/sync-azure-catalog.py` snapshots the
selection; `go generate ./providers/azure` reproduces the catalog offline.

The registered `netapp` source covers accounts, capacity pools, volumes,
snapshots, subvolumes, quota rules, volume groups, snapshot and backup policies,
backup vaults and their backups. Product indexes discover resources; every
resource gets an own GET. Known IDs recover omitted parent/child indexes. Parent
and resource snapshots are compared before returning observations; two complete
passes bind client pagination. A missing parent or collection fails the shard;
only own absence with readable parents can reconcile a known child. Backup and
subvolume proxy resources inherit location from their GET-verified parent, as
neither native resource has a location field. Other kinds require matching
parent/resource locations. Native compound names and volume-group short names
are both validated against the ARM identity.

Tests adapt example identities, region and volume subnet to two test accounts.
Unknown private fields are added as canaries and must remain absent from public
observations and API diagnostics. Tests cover native and client pages, known
omissions, sibling regions, changed private configuration, duplicate/foreign IDs,
filter and cross-host pages, incomplete/permission responses, typed network
references and actual SQLite scan/graph/reconciliation workers. The worker scans
22 resources and preserves them after a missing parent; restoring the parent and
independently observing one snapshot's absence closes only that snapshot.
These are local protocol/application tests, not recordings, independent NetApp
emulator evidence or live Azure acceptance.

This milestone enables inventory, not lifecycle cleanup. Native mutations remain
catalog contracts; resource action bindings are not yet enabled. Full replication,
clones, volume-group membership, identity/encryption references, policy consumers,
retained-backup lifecycle, export-policy changes and reviewed deletion still need
implementation and verification. The selected ListReplications operation is POST
with an optional body and has its own pageable contract; it is not a GET child
collection. No mock GET is substituted for it.

## Platform semantics

- `VolumeProperties.mountTargets` is read-only. Its creationToken/file-system ID
  and mount IP are volume properties, not independent ARM resources with DELETE.
- [NFS permissions](https://learn.microsoft.com/en-us/azure/azure-netapp-files/network-attached-storage-permissions)
  and [export-policy commands](https://learn.microsoft.com/en-us/cli/azure/netappfiles/volume/export-policy)
  describe access-rule updates. Removing one NAS mount must not delete its volume.
- [Volume deletion](https://learn.microsoft.com/en-us/azure/azure-netapp-files/volume-delete)
  requires users to stop applications and unmount clients; replication has a
  separate termination workflow. `Volumes_Delete.forceDelete` can also clean up
  connected resources and must not be silently enabled.
- [Backup deletion](https://learn.microsoft.com/en-us/azure/azure-netapp-files/backup-delete)
  has latest-backup restrictions and explicitly allows a deleted source volume.
  Backup-to-source-volume graph links therefore do not grant volume ownership or
  imply backup destruction when the source volume disappears.
- [Snapshot deletion](https://learn.microsoft.com/en-us/azure/azure-netapp-files/snapshots-delete)
  excludes active restores/clones and replication-generated/protected snapshots.

## Upstream example discrepancies

Five unchanged examples contain parameters that their own Swagger operation does
not declare. Tests require strict binding to reject each extra field, then bind
only the declared parameters. They do not modify fixtures or expand the API:

| Operation | Undeclared example parameter |
| --- | --- |
| Accounts_ListBySubscription | resourceGroupName |
| SnapshotPolicies_ListVolumes | body |
| Subvolumes_ListByVolume | subvolumeName |
| VolumeQuotaRules_ListByVolume | volumeQuotaRuleName |
| Volumes_ReplicationStatus | body |

Every retained example's declared request binds against the generated catalog.
In particular ListReplications remains POST with its declared optional body,
whereas the two GET operations above do not acquire a body from their examples.
