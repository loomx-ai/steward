# Azure native API metadata

The runtime embeds `generated/catalog.json` and `../specs/*.yaml`. Catalog
operations come from Microsoft's versioned ARM Swagger files. Resource types,
classification, and their operation bindings are explicit in
`source/selection.json`; changes affect the catalog and spec bundle revisions.

Refresh upstream metadata deliberately, then review the source and generated
diffs:

```sh
python3 scripts/sync-azure-catalog.py
go generate ./providers/azure
go test ./providers/azure ./internal/provider/catalog
```

`source/swagger.json` keeps selected official operations and the transitive
parameter/schema objects they reference. Operation and schema objects retain
their original `$ref` values. Each root or dependency document records its
source URL and the SHA-256 of the complete upstream file. The importer resolves
root parameters and response schemas from this checked-in set; missing
dependencies fail generation. Nested schema references remain native metadata.

Normal builds, generation, and catalog regression tests do not access the
network. The catalog records API contracts, not proof that credentials have
permission, that a provider emulator supports every operation, or that live
deletion has been verified.

All 122 current resource rules discover through native product List operations.
The broad subscription resource index supplies unknown kinds and cannot overwrite
product observations. Subnets, Blob containers, SQL databases and elastic pools
enumerate their native parents first; detail reads supply lifecycle properties,
inherited locks, ownership and network references. Child location falls back to
the parent only when the native response omits it.

Product pagination binds the connection, subscription, selected scope and kind,
bundle revision and ordered parent set. Changed parents, returned incarnation
fields or etags require a fresh scan. Permission failures, missing collections,
partial HTTP responses, malformed identities, foreign pages and pagination cycles
fail the shard. Parent readback checks changes during child discovery. These are
change detectors using the fields provided by ARM, not an atomic cloud snapshot.
Network target selection uses the native VNet/Subnet collections directly.

Catalog refresh retries transient failures at most four times. Offline Python
tests preserve native operations, recursive references and source fingerprints;
Go tests retain product wire behavior, scan authority, paging and failure cases.

Thirty additional rules cover capacity reservations, dedicated hosts, SSH keys,
VPN/ExpressRoute, virtual WAN hubs and routing, firewall policies, DNS, flow logs,
Private Link and file shares. The current catalog contains 380 operations from
70 root documents and 30 reference documents. Parent path parameters preserve
the API's actual spelling and hierarchy, including resource-group-only lists.
Native detail responses may omit `type`; their full bound identity and any
present type must agree, and partial detail responses cannot authorize deletion.

Firewall inheritance, nested Traffic Manager endpoints and native network
references have explicit relationships. VPN, ExpressRoute and RADIUS secret
fields are removed from inventory and API logs. File-share deletion explicitly
uses `$include=none`, preserving snapshots until their independent lifecycle is
modeled. Service-parent cascade/prerequisite handling and the remaining service
families are still incomplete; these protocol checks do not close publication
or independent emulator acceptance.

VM managed disks and NICs, and a NIC's public IPs, contribute native lifecycle
impact from their `deleteOption` fields. A reviewed retention outcome changes
the option to `Detach` before deleting the controller. VM updates use the
versioned PATCH operation (with the ETag when provided); NIC updates use the
native PUT operation and preserve the pinned schema's writable network/DNS/IP
settings. The NIC API does not declare conditional request headers, so these
checks do not establish atomic protection against simultaneous external writes.

The waiter persists preparation phases, validates operation ownership, and
checks both provisioning completion and the changed deletion options. Live
child locks, managed ownership, missing plan impacts and attachment drift block
deletion. Unsupported unmanaged-disk deletion and unknown NIC fields fail
closed. Tests validate retention bodies against the full checked-in official
schemas and exercise delayed readback and restart behavior.

See [VM and attached-resource deletion](https://learn.microsoft.com/en-us/azure/virtual-machines/delete)
for the platform's disk, NIC and public-IP deletion policies.

AKS cleanup reads `properties.nodeResourceGroup` from the live cluster and lists
that group's resources with native ARM pagination. Known resources use full
product GETs and recursive native child collections. Group names and `MC_`
prefixes are not ownership evidence. The group, known children, and unknown resource kinds
must be present in the inventory and reviewed impact plan. Group ownership takes
precedence over VM/NIC `Detach` options inside the group. Live locks, protected
tags, newly discovered resources, changed groups, and inaccessible dependent
collections block deletion. A missing dependent collection is not proof that
the cluster is absent.

The pinned AKS deletion API cannot retain resources inside the node resource
group. A retention request is blocked at planning time; move such resources to a
different group and rescan before deleting the cluster. Automatic VM/NIC deletion
of attachments outside the group currently requires detaching those attachments
first. An operation marked successful is followed by reads of both the cluster
and its node resource group. Group absence covers unknown contained kinds;
every known reviewed descendant must independently return 404 before completion.
ARM provides no atomic snapshot-and-delete transaction for group membership;
external writers must not add resources while a reviewed deletion is running.

These tests establish protocol behavior, including pagination, retention, child
scope/permission changes, worker state restoration, and group absence readback.
They are not real Azure or independent emulator verification. See Microsoft's
[AKS deletion behavior](https://learn.microsoft.com/en-us/azure/aks/delete-cluster)
and [node resource group lifecycle and retention](https://learn.microsoft.com/en-us/azure/aks/faq#can-i-restore-my-cluster-after-i-delete-it).

Network Watcher has native packet-capture and connection-monitor rules alongside
flow logs. Its documented parent deletion contributes all three child collections
as reviewed impacts, with native paging, live detail reads, identity/generation
checks and parent readback. Retaining any of these children blocks parent cleanup.
Unlisted or unreadable children, malformed locks, changed incarnations, foreign
impacts and protected tags cannot authorize deletion. The same child discovery
is included when an AKS node resource group contains a Network Watcher.

After native operation completion and parent absence, every planned child must
also return 404 before reconciliation finishes. The frozen plan and operation
state support resumed execution. Packet captures refer to their VM/scale-set and
storage account; connection monitors refer to native endpoints and log workspaces.
HTTP request headers and SAS query values in capture storage paths are sanitized.
Deleting a capture session does not delete its stored capture file. See the
[Network Watcher deletion contract](https://learn.microsoft.com/en-us/azure/network-watcher/network-watcher-create)
and [packet-capture deletion behavior](https://learn.microsoft.com/en-us/azure/network-watcher/packet-capture-manage#delete-a-packet-capture).

SQL logical servers support their native DELETE with reviewed database and
elastic-pool impacts. The `master` database is restricted to server-owned cleanup,
while other databases and pools retain their independent native delete actions.
SQL database IDs and creation dates supplement ARM generation checks. Native
child permissions, locks, protected records, new members and retention requests
are checked before server deletion; every planned child must be absent afterward.
The service's backup/soft-delete retention remains governed by Azure; this action
does not purge retained backups. See [logical-server lifetime semantics](https://learn.microsoft.com/en-us/azure/azure-sql/database/logical-servers).

Public and private DNS records now use their native record-type collections,
including wildcard/apex names and conditional DELETE with the scanned ETag.
The overlapping official `RecordSets_*` operation names are qualified by the
source document title; native operation names and wire contracts are preserved.
DNS metadata protection applies to system and auto-registered records as well.
Zones own their system/custom record sets; private zones require network links
to be deleted first. VNet deletion checks links across all subscription zones.

Private endpoint DNS zone groups contribute only records identified in the
Network provider's read-only `recordSets` configuration. Exact native DNS reads
verify their addresses, TTL, FQDN and generation, including records in another
resource group. Registration links own their auto-registered records. Multiple
registration links require a unique match against all linked VNet address spaces;
overlapping, unknown or inaccessible address spaces block deletion. Manual
records remain outside registration-link ownership. When both a zone and an
external controller are in inventory, the external controller owns its records.

The plan, live preflight and resumed readback preserve those record impacts.
Retention, metadata protection, inherited locks, missing ETags, foreign impacts,
changed values and conditional conflicts have retained regression tests.
See [private endpoint DNS groups](https://learn.microsoft.com/en-us/azure/private-link/private-endpoint-dns-integration),
[registration-link deletion](https://learn.microsoft.com/en-us/rest/api/dns/privatedns/virtual-network-links/delete),
and [private DNS network-link lifetime](https://learn.microsoft.com/en-us/azure/dns/private-dns-virtual-network-links).

Private endpoints support native deletion with a reviewed NIC → endpoint and
record → DNS zone group → endpoint ownership tree. The read-only native
`networkInterfaces` collection and each NIC's reciprocal `privateEndpoint.id`
must agree. NIC generation, live IP configurations, groups, management locks
and protection are checked before deletion; unexpected public-IP attachments
cannot be silently cascaded. DNS records retain their exact external-group
validation. A missing endpoint still requires all planned NICs, DNS zone groups
and records to return 404, including after a worker restart. The private DNS
zone and manual records remain independent resources. Retaining a managed NIC,
group or record blocks deletion of its endpoint.

VM scale sets now expose native Uniform instances, root/instance extensions,
instance NICs, IP configurations and public IPs. Uniform deletion reviews and
verifies the full instance, managed-disk and network tree. Managed network types
have only native GET/List operations and are restricted to their owning cascade.
Only explicitly selected response-type aliases permit the standard VM type in
native nested-instance responses; the full ARM path remains mandatory.

Flexible instances use standard VM APIs and must be deleted before their scale
set. The planner preserves that order and passes disk retention through to each
instance action, including after serialized execution resumes. Live orchestration
mode, membership, VM/disk creation IDs, reciprocal ownership, generation,
protection and complete collections are checked. Uniform unmanaged VHD deletion
and disk detachment are not yet modeled; those configurations require detachment
before cleanup. Retention of Uniform managed children blocks parent deletion.
AKS group cleanup includes these nested trees and their verified external disks.

The Network instance APIs retain their declared `2018-10-01` version even though
the upstream files are stored under the `2024-05-01` source folder. Four unchanged
official examples supplement the retained lifecycle and native-wire tests. See
[Flexible deletion prerequisites](https://learn.microsoft.com/en-us/troubleshoot/azure/virtual-machine-scale-sets/delete/vmss-operation-not-allowed).

AKS review also expands documented cascades beyond the node resource group:
Uniform instance disks, private endpoint NIC/DNS resources, and VNet private-DNS
links with their auto-registered records. Frozen native relationships and live
membership checks bind every external impact. Shared zones and manual DNS
records remain independent. Native detail/generation checks prevent a sparse
ARM group index from hiding lifecycle state, and group ownership is re-read.

The same frozen AKS membership suppresses duplicate service/attachment ownership.
Tests combine 18 nested and external impacts, retained children, stale or missing
inventory, new extensions/records, inherited locks, external group permissions,
changed ownership and serialized worker restart. Known resources are read after
group absence, including external DNS records. A failed resource readback cannot
set the waiter's completion flag. Other service-parent lifecycles and independent
emulator/application acceptance remain open.

Standard VM extensions have explicit native product discovery and independent
Delete/Get operations. VM cleanup includes their reviewed cascade alongside its
separate disk/NIC/public-IP deletion policies. Service and attachment impacts
are partitioned only by the frozen native attachment references and controller
identities, so retaining a disk cannot silently retain or drop an extension.

The VM's normal generation check remains strict until a reviewed Delete-to-Detach
transition has taken effect. Native retention updates change its ETag; that
resumed phase still checks stable VM identity, all attachment policies and live
extension membership. Preparation rechecks extensions before another mutation.
After the VM is absent, its extensions must also return 404. AKS recursive group
cleanup includes standard VM extensions as well. Native list shards, permission
errors, changing extensions, missing/foreign impacts and retention have permanent
protocol tests, including server lifecycle discovery and serialized restart.

Dedicated-host and capacity-reservation groups now plan independent member
deletions before their own DELETE. Virtual WAN VPN gateways similarly require
connection and NAT-rule cleanup; ExpressRoute gateways require connection
cleanup. VPN connections own their read-only link connections, whose native
API has no independent DELETE. NAT references order link-owning connections
before their rules. Active VM associations and live NAT-link references block
deletion of the associated host, reservation or rule.

The executor passes frozen, reviewed direct-child steps as prerequisites, even
after those assets close or inventory changes. Parent preflight and resumed
readback require their individual native 404s. An ETag change can be attributed
to those prior deletions only when the sanitized parent configuration and
creation identity match, excluding its declared child collections and modification
audit fields. Other configuration changes still require a fresh plan. This
extends to Flexible scale-set prerequisites.

Unchanged official Get examples verify gateway connections and NAT rules. The
VPN link example uses `VpnSiteLinkConnections` in response IDs/types but
`vpnLinkConnections` in request paths. This one documented final-segment alias
is bound to the same subscription, group, gateway, connection and link name;
foreign identities and duplicate canonical/alias records are rejected. See the
[native VPN link contract](https://learn.microsoft.com/en-us/rest/api/virtualwan/vpn-site-link-connections/get),
[Virtual WAN cleanup sequence](https://learn.microsoft.com/en-us/azure/virtual-wan/virtual-wan-faq),
and [capacity-group deletion prerequisites](https://learn.microsoft.com/en-us/troubleshoot/azure/virtual-machines/windows/capacity-reservation-cant-delete-group).

Event Hubs dedicated clusters use `Clusters_ListBySubscription`, `Clusters_Get`,
`Clusters_ListNamespaces`, `Configuration_Get`, and `Clusters_Delete` from the
2024-01-01 API. The namespace-ID list is cross-resource-group; every namespace
GET must prove the reciprocal `clusterArmId`. Namespace steps preserve their
nested entity cascades and shared Geo-DR unpairing prerequisites. The cluster
DELETE waits for native namespace absence and its waiter repeats these checks.
Quota configuration has neither an ARM ID nor a DELETE; its string-valued
settings enrich cluster inventory and must still match the frozen plan.
Changing or unreadable lists/settings, missing or retained inventory, altered
namespace membership, recreation, inherited locks and protected resources block
cleanup. A known creation time less than four hours old prevents deletion in
accordance with Azure's minimum cluster lifetime. Five unchanged Swagger
examples and retained protocol/restart tests verify these contracts.
