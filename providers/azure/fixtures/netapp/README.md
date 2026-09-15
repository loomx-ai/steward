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

The original foundation enabled inventory only; reviewed ordinary volume deletion
and independent snapshot/backup deletion are now implemented as described below. Full replication,
clones, volume-group membership, identity/encryption references, policy consumers,
retained-backup lifecycle and export-policy changes still need
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

## Native deletion polling

`delete-recordings.json` extracts 13 interactions from four Azure CLI recordings
at commit `ea185727729efc032ad9d4eef9ec355ee74ebaae`, all using API `2025-12-01`.
Each extraction retains the original source URL, whole-source SHA-256 and
zero-based interaction indices. Bodies are unchanged; request headers are omitted
and only the `t`, `c`, `s`, `h` signing query values are redacted in URLs.
The volume recording explicitly requests `forceDelete=true`; it is evidence for
the acknowledgement/polling protocol, not for an ordinary volume cascade.

The native 202 acknowledgement contains Azure-AsyncOperation and Location URLs
under the same subscription, region and operationResults UUID. Status GET returns
an operation identity and DELETE target; after Succeeded, Location GET returns an
empty 200. Subvolume's recording stops after status success, without a Location
observation. Tests preserve this gap and keep that operation incomplete.

The shared NetApp helper validates endpoint roles, native signed query fields,
operation/target identity, response status and empty result shape. Receipts bind
the resource, region and credential context; serialized phases survive fresh
runtime instances. An operation's terminal receipt prevents repeated polling but
never proves resource absence. Expired callbacks, permissions errors, unknown
states, changed URLs and modified receipts cannot report successful deletion.
No resource cleanup action is enabled by this polling milestone.
All eleven unchanged native DELETE examples also drive acknowledgement tests.
In particular backup-policy and snapshot-policy deletion may return an empty 200
as well as 202/204; this does not enable their independent cleanup actions.


## Reviewed volume cleanup

At the volume milestone only the volume received a direct cleanup binding. Its signed review includes
full private hashes of the volume, capacity pool, account and resource group;
fileSystemId and poolId distinguish recreated resources. It enumerates active
replications with native POST `listReplications` and `{"exclude":"Deleted"}`;
continuations use GET with no body. This follows the pageable SDK contract in
[Azure SDK for Go](https://github.com/Azure/azure-sdk-for-go/blob/c949172cf1b28d69f911a4f78e0e88bcd98f5049/sdk/resourcemanager/netapp/armnetapp/volumes_client.go)
(the cited SDK uses 2025-12-15-preview; the selected REST operation and all local
requests use 2025-12-01). It is corroborating paging implementation evidence,
not a recorded stable-API multipage replication transaction.

Volume-local snapshots, subvolumes and quota rules form an exclusive lifecycle
ledger. Listed and previously reviewed members get own GETs; missing live assets
block planning. Native `enableSubvolumes` defaults to Disabled, so disabled
collections are skipped while known child IDs still require independent reads.
Parents, members and replications are checked again before any DELETE. Protected,
restoring, actively cloning, DataProtection/ShortTermClone and unknown-type volumes
cannot use this ordinary deletion action. It never sends forceDelete.

Native semantics: [volume deletion removes snapshots](https://learn.microsoft.com/en-us/azure/azure-netapp-files/volume-delete),
[volume deletion removes its quota rules](https://learn.microsoft.com/en-us/azure/azure-netapp-files/manage-default-individual-user-group-quotas),
and [backups survive volume deletion](https://learn.microsoft.com/en-us/azure/azure-netapp-files/backup-configure-policy-based).
The plan explains application shutdown/unmounting, volume-local data loss and
retained vault backups/parents in English and Chinese. Retaining a volume-local
member while deleting its volume is rejected. Independently selecting a snapshot
does not select or delete the volume.

Execution saves its signed acknowledgement before follow-up HTTP. It checkpoints
terminal native polling before own-resource readback. A readable pool with the
reviewed poolId, the volume's own 404 and every managed child's own 404 are required
for completion. Neither callback expiry nor the volume's disappearance alone
closes children. The actual SQLite scan/plan/execution test reopens the database
and creates a new provider runtime across accepted-response/503 failure and each
poll phase; one DELETE closes exactly four of 22 assets, retaining both backups.
These remain local protocol/application tests, not live Azure acceptance or an
independent NetApp emulator. Other native resource mutations and full parity remain
unfinished.


## Independent snapshot and backup cleanup

Both recovery kinds now have direct DELETE bindings with signed review and durable
receipts. Own immutable IDs and creation timestamps reject recreation; parent
reads, inherited locks/tags and private configuration are rechecked. Selecting a
snapshot alone no longer inherits the volume ledger's unselected-controller skip.
Volume selection still reviews and delegates its three native child kinds.
Independent recovery deletion has one step and no cascade impacts.

Backup review enumerates every vault in the account and reads all backups, using
snapshotCreationDate rather than creationDate. A distinct, successfully completed
newer backup proves that the target is older; Creating/Failed/Deleting backups and
aliases with the same backupId cannot provide that proof. Tied latest timestamps
remain protected while the live source has backupPolicyId, even if policyEnforced
is false. Historical backupPolicyResourceId does not establish current assignment.
A source volume's own 404 permits retained-backup cleanup; 403/5xx or malformed
assignment objects fail review. Nullable chronology does not prove known latest:
native DELETE is allowed to enforce that opaque restriction without force or
policy changes. Its refusal never closes the asset. Snapshot APIs do not expose a
complete active-file-restore/baseline ledger, so native DELETE also remains the
final authority for those restrictions.

Each complete inventory pass reuses its already verified backup collection and
source observations, avoiding repeated collection reads per backup. The second
pass starts fresh, and mutation review never receives the inventory cache.
Closed sibling review hints do not become ownership or permanent inventory scope;
actual KnownNativeIDs still recover omitted live resources. The request-count test
covers nine backups, native pagination and an omitted known backup.

`recovery-recordings.json` retains four snapshot DELETE/poll interactions and two
backup own GETs from Azure CLI commit
`ea185727729efc032ad9d4eef9ec355ee74ebaae`, API 2025-12-01. Each record includes its
source URL, whole-source SHA-256 and zero-based index. Snapshot source
`test_create_delete_snapshots.yaml` has SHA-256
`e005b63c13550051185d49fb5a6c6aa7590655f7103191a3c558f81d0e4bfe88`;
backup source `test_create_delete_backup.yaml` has SHA-256
`0e3fdbedf9da3e0cd9651753bcea87d1b54bade67a0910c158babf2d5f0c4160`.
Bodies are unchanged; request headers are omitted and signing query values alone
are redacted. Snapshot DELETE sends no force and returns empty 202, then status
Deleting/Succeeded and an empty Location 200. This recording does not contain an
own snapshot GET proving absence. Recorded backup IDs satisfy the generic native
UUID pattern but are not UUID-v4 bit patterns; tests preserve the real values.

SQLite scan/plan/execution tests reopen the database/runtime at each saved phase.
Snapshot selection closes one of 22 assets; backup selection with its source
already deleted closes one of six inventoried assets. Both send exactly one DELETE,
retain all other resources and expose irreversible-loss warnings in English and
Chinese. Separate tests cover read failures, stale context, recreation, missing
parents, native refusal, callback expiry and altered receipts. These are offline
native recording/protocol/application tests, not an independent emulator or live
Azure acceptance. Other resource mutations, combined protected-latest backup and
volume ordering, replication/clone/export workflows and full parity remain open.


## Independent subvolumes and quota rules

Two further direct bindings use the existing durable native leaf DELETE protocol,
without changing the proof namespace or saved receipts for snapshots/backups.
Full private configurations are reviewed before DELETE. The parent volume/pool
UUIDs, readable ancestors, inherited locks/tags, active replications, restore and
clone state constrain the boundary. Parent subvolume support must be Enabled.
Active replication blocks these independent actions: quota changes at a source
propagate to destinations, and no unreviewed remote quota effect is authorized.
Replicated-child cleanup remains implementation work.

These child APIs have no immutable child UUID. Identity checks bind path/parentPath
or quota type/target and native systemData.createdAt when supplied; parent UUIDs
remain separate. Missing optional creation metadata does not invent an ID or
silently disable the documented native operation. A same-name, identical child
recreated without creation metadata cannot be distinguished by this API. Own live
readback never proves deletion, and callbacks never substitute for own absence
under readable parents. Quota limits and lifecycle state can evolve during native
deletion; an existing rule remains present rather than being closed prematurely.
Malformed targets/creation data, unknown or busy states and protection block direct
actions. Failed rules/subvolumes can be removed; an omitted subvolume lifecycle
state is supported as in the unmodified Microsoft GET example.

Default and individual user/group quota targets are covered. Removing a rule can
expose a different applicable quota and does not delete files. Subvolume deletion
warns of data removal and application interruption while preserving the parent and
other children/recovery points. English/Chinese warnings and documentation preserve
these different consequences. The volume contributor grants direct permission only
to eligible leaves; a full native graph cannot skip selected supported children
or bypass their protection. Existing volume cascade remains separately reviewed.

`child-recordings.json` extracts four own GETs and four quota DELETE/status/result
interactions at Azure CLI revision `ea185727729efc032ad9d4eef9ec355ee74ebaae`.
`test_subvolume_crud.yaml` has SHA-256
`0ab3382b790841777ad6cab0b3d7a7c5348f6611296f6aa9d565d339005893e7`;
`test_create_volume_quota_rule.yaml` has SHA-256
`60d98b1270aa160e1ba02d96a7c78a9a07e443292513ca8790298fe8e682d1d7`.
Bodies and indices are unchanged; request headers are omitted and only signing
query values are redacted. Quota DELETE has no body or force flag, returns 202,
then Deleting/Succeeded and an empty Location 200. The recording has no own GET
proving absence. Subvolume creation metadata stays constant across its path update,
but the quota recording changes systemData.createdAt after its PUT: it is context
evidence, not proof of an immutable quota incarnation.

The registered SQLite worker scans all 22 fixture assets, selects one subvolume or
quota rule, resumes across failures and poll checkpoints and issues one DELETE.
Only the selected asset closes; the other 21 remain. Additional tests cover full
native graph blockers, disabled-subvolume known-ID reads, default/individual quota
rules, failed/busy/unknown states, changed targets, parent UUID changes and altered
receipts. Native recordings, protocol and application tests are distinct evidence;
no live Azure or independent NetApp emulator acceptance is claimed.

The CLI subvolume group is now deprecated, but the pinned stable 2025-12-01 REST
still declares its operations. The separate Subvolumes_GetMetadata POST provides
file metadata including creationTimeStamp; it is not substituted for an ordinary
GET, is not in this selected catalog and has a different asynchronous response
schema. More extensive clone/file metadata workflows remain unfinished.
