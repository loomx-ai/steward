# Native REST metadata fixtures

`google-compute.json`, `azure-public-ip.json`, and `azure-dns-records.json` retain selected, unchanged
method/schema fragments from the official documents identified by each
`source_uri`. `source_sha256` is the SHA-256 of the complete upstream response
at capture time. Steward's `x-resource-types` classification is separate from
those API definitions.

The importer tests check actual operation IDs, endpoint/path construction,
API versions, pagination, idempotency parameters, nested resources, source
provenance, deterministic generation, and invalid-input rejection. They do not
claim to simulate a running cloud service. These fixtures and tests are permanent
regression coverage.

The DNS fixture was captured using the production Azure source synchronizer.
It includes the public DNS 2018-05-01 and private DNS 2024-06-01 record set
Get, Delete, and ListByType operations, plus their complete reference closure.
Both official documents name these operations `RecordSets_*`; the importer
qualifies their catalog IDs with the official document title while preserving
the native operation IDs, paths, parameters, and API versions.
