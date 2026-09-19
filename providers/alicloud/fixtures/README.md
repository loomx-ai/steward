# Alibaba Cloud protocol fixtures

`official-examples.json` holds the unchanged JSON response examples that
Alibaba Cloud publishes in the `api-docs.json` metadata at
`https://api.aliyun.com/meta/v1`, one per read operation in the catalog. The
same run of `scripts/sync-alicloud-catalog.py` pins each source document's
URI and SHA-256 in `catalog/source/official.json`, together with the
operation's parameters, error codes and response field paths.

`TestSpecResponsePathsMatchOfficialMetadata` checks every list, enrichment and
readback path in `specs/` against those official response schemas, extracts
records from the official examples the way inventory does, and requires each
record to carry an identity. Every product API field must be a path of the
official list or enrichment response. The few operations without a usable
JSON example are listed in the test with the reason.

`resource-center-page.json` and `ack-cluster-resources.json` model Resource
Center and ACK responses for protocol tests. None of these fixtures is
evidence of a live Alibaba Cloud account.
