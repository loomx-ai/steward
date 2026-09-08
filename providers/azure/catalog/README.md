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
