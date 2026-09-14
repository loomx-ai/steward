# Independent Bigtable table emulator

The nested `harness` module runs Google's unmodified `bttest` server at commit
`4dc7532ec124a797db0b78205a837a64ec49ed42`. Its dependencies are isolated from
Steward's runtime module. `provenance.json` records the module checksums, upstream
source URL and source hash; `harness/go.sum` pins the fetched dependencies.

## Reproduce

Requires Go 1.25 or newer and network access on the first dependency download.
From the repository root, in a dedicated terminal:

```sh
cd providers/gcp/fixtures/bigtable/harness
go run . -listen 127.0.0.1:8091
```

The harness prints its loopback HTTP origin. In another terminal at the
repository root:

```sh
STEWARD_BIGTABLE_EMULATOR_URL=http://127.0.0.1:8091 go test ./providers/gcp -run '^TestBigtableIndependentEmulator$' -count=1 -v
STEWARD_BIGTABLE_EMULATOR_URL=http://127.0.0.1:8091 go test -race ./providers/gcp -run '^TestBigtableIndependentEmulator$' -count=1 -v
```

The test skips unless that variable is set. It creates unique instance-name
prefixes, creates three tables, and removes only its own tables. Cleanup uses a
native fixture-side update to clear protection before deleting any leftover
protected fixture; Steward itself never clears the protection flag. Stop the
harness with Ctrl-C after testing: both listeners close and all in-memory data
is discarded. Use another loopback port if 8091 is occupied, or omit `-listen`
and use the printed randomly assigned port. No cloud credentials are required.

## Evidence boundary

All table Create/List/Get/Update/Delete responses and state are produced by
Google's server. The bridge uses generated gRPC clients and native `protojson`
encoding, translates the corresponding REST paths/body/query parameters and
maps gRPC errors to HTTP status codes. It does not construct table responses,
implement table lifecycle behavior, enrich metadata or add pagination results.

Steward's test supplies exactly two parent instance list records. The shared
`protocolRuntime` supplies OAuth and the connected project's CRM record. Every
other provider request must target a test-created table or its table list and
is forwarded to the bridge. It verifies:

- Same-named tables in two instances remain distinct.
- Names-only native lists are enriched with schema/granularity/protection from
  GetTable; declared properties remain searchable.
- The independent server rejects protected deletion, and Steward's preflight
  prevents sending that delete. An explicit fixture-side unprotect then allows
  ordinary deletion without a production protection override.
- Serialized action state can be restored into a new runtime, which confirms
  the table's own 404 and avoids repeated deletion. A final native list is empty.

The pinned server ignores `view`, `pageSize` and `pageToken`; GetTable returns
column families, granularity and protection but no replication or backup policy.
Thus this test cannot independently prove view semantics, pagination, replicas,
backups, IAM, instance/cluster administration or instance cascade behavior.
Native-view, permission, pagination, cascade and SQLite failure tests remain in
`bigtable_test.go`, `bigtable_inventory_worker_test.go` and
`service_lifecycle_test.go`. No real-cloud acceptance is claimed.

Sources:

- [Google emulator documentation](https://docs.cloud.google.com/bigtable/docs/emulator)
- [Pinned bttest implementation](https://github.com/googleapis/google-cloud-go/blob/4dc7532ec124a797db0b78205a837a64ec49ed42/bigtable/bttest/inmem.go)
- [Create table REST contract](https://docs.cloud.google.com/bigtable/docs/reference/admin/rest/v2/projects.instances.tables/create)
- [Update table REST contract](https://docs.cloud.google.com/bigtable/docs/reference/admin/rest/v2/projects.instances.tables/patch)
