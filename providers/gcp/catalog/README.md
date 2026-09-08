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

Reference material:

- [Google Discovery directory](https://www.googleapis.com/discovery/v1/apis)
- [Cloud Asset Inventory asset types](https://docs.cloud.google.com/asset-inventory/docs/asset-types)
- [Compute REST API](https://docs.cloud.google.com/compute/docs/reference/rest/v1)
- [Google API resource names](https://cloud.google.com/apis/design/resource_names)

The checked-in tests establish metadata consistency and protocol behavior. They
do not establish live permissions, eventual inventory consistency, service
availability, or independent emulator coverage.
