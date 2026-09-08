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

All 64 current resource rules discover through native product List operations.
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
Private Link and file shares. The current catalog contains 258 operations from
49 root documents and 25 reference documents. Parent path parameters preserve
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
that group's resources with native ARM pagination. Group names and `MC_` prefixes
are not ownership evidence. The group, known children, and unknown resource kinds
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
and its node resource group. Only confirmed group absence permits reconciliation
of all contained resources, including kinds without independent action drivers.
ARM provides no atomic snapshot-and-delete transaction for group membership;
external writers must not add resources while a reviewed deletion is running.

These tests establish protocol behavior, including pagination, retention, child
scope/permission changes, worker state restoration, and group absence readback.
They are not real Azure or independent emulator verification. See Microsoft's
[AKS deletion behavior](https://learn.microsoft.com/en-us/azure/aks/delete-cluster)
and [node resource group lifecycle and retention](https://learn.microsoft.com/en-us/azure/aks/faq#can-i-restore-my-cluster-after-i-delete-it).
