# Native REST metadata fixtures

`google-compute.json` and `azure-public-ip.json` retain selected, unchanged
method/schema fragments from the official documents identified by each
`source_uri`. `source_sha256` is the SHA-256 of the complete upstream response
at capture time. Steward's `x-resource-types` classification is separate from
those API definitions.

The importer tests check actual operation IDs, endpoint/path construction,
API versions, pagination, idempotency parameters, nested resources, source
provenance, deterministic generation, and invalid-input rejection. They do not
claim to simulate a running cloud service. These fixtures and tests are permanent
regression coverage.
