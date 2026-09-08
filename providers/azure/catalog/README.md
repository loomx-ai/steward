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
