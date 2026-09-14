# Cloud Router named-set evidence

Native method and transitive schema objects are retained without alteration in
`catalog/source/discovery.json`, from the [Compute v1 Discovery document](https://www.googleapis.com/discovery/v1/apis/compute/v1/rest),
revision `20260908`, full-response SHA-256
`aa1078267f6ad9c82274e6c62572bae328b0de11c6f08861f20488b6617afda7`.
GET/LIST extend the existing router-policy fragment; previous method/schema
objects and generated operations are unchanged.

- [listNamedSets](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/listNamedSets)
  uses the selected project, region and router. The native list is `result`, with
  `maxResults`, `pageToken` and `nextPageToken`. Partial warnings and `unreachables`
  fail the shard and cannot prove absence.
- [getNamedSet](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/getNamedSet)
  uses the `namedSet` query parameter and wraps detail in `resource`. The set's
  name is unique within its router. Types include `NAMED_SET_TYPE_PREFIX` and
  `NAMED_SET_TYPE_COMMUNITY`; elements are native CEL expression objects.
- [Manage named sets](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/bgp-route-policies/update-named-sets)
  documents prefix/community examples and the native restriction that a set
  cannot be removed while any policy on its router references it.

The independently indexed kind is `compute.googleapis.com/NamedSet`, with
inventory identity
`//compute.googleapis.com/projects/{project}/regions/{region}/routers/{router}/namedSets/{name}`.
This is not a REST endpoint or a claimed Cloud Asset Inventory asset type.
Native reads bind the actual GET/query contract. Inventory captures the parent
router's ID and dependency, full expression objects and opaque fingerprint.
Policy references are extracted from CEL syntax trees, as described below.

Run from the repository root:

```sh
go test ./providers/gcp -run 'TestNamedSet|TestRoutePolicy|TestCatalogReproducible|TestGCPChangedPropertyPaths' -count=1
go test -race ./providers/gcp -run 'TestNamedSet|TestRoutePolicy' -count=1
```

Locally authored protocol fixtures cover project/regional/global scope, nested
router/set paging, identical local names in different routers, parent UID and
cursor changes, project-number aliases, malformed identities/wrappers/fields,
denials, detail 404, partial results and token cycles. The independent JSON
Schema validator checks both native set types and rejects malformed element
shapes. Forward-compatible enum values are retained. SQLite scans and property
queries verify failed detail reads preserve prior history, an authoritative
empty list closes old observations, and reappearance reuses the original asset
ID with refreshed data. Shared tests also rerun route-policy behavior.

The [pinned mockgcp Router implementation](https://github.com/GoogleCloudPlatform/k8s-config-connector/blob/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockcompute/routersv1.go)
implements parent Get/Insert/Patch/Update/Delete and embeds the unimplemented
Router server; the inspected file has no named-set GET/LIST handlers. These
fixtures are protocol/application evidence, not an independent named-set
emulator or live-cloud verification.

The native [deleteNamedSet API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/deleteNamedSet)
exists, but independent deletion, executable dependency ordering and parent-router
cascade review remain unfinished. No delete action
is exposed by this inventory milestone; this is not full route-policy parity.

## Policy reference syntax and graph evidence

[Cloud Router's native attribute reference](https://docs.cloud.google.com/network-connectivity/docs/router/reference/bgp-route-policy-reference)
defines `prefixSets` and `communitySets` calls with router-local names. The
[official CEL implementation](https://cel.dev) is pinned as
`cel.dev/cel-go v0.32.0`; that release's module has moved from the historical
`github.com/google/cel-go` path. Module archive checksums are retained in
`go.sum`. Existing module selections are unchanged; the added parser modules
and their required dependencies are explicit in `go.mod`.

The parser preserves source call nodes without macro expansion or evaluation.
It decodes literal escapes, raw/triple-quoted strings and absolute function
names, traverses nested calls and both match/action expressions, and deduplicates
references. Comments and string contents do not create graph edges. Parser
resource/depth limits apply. Invalid syntax, member-style set calls and
non-literal set arguments fail the shard; computed-name resolution is still
unfinished. Native policy expression text is not exposed in validation errors.

`route_policy_references_test.go` covers these syntax distinctions. The SQLite
scan/graph/cleanup/reconciliation scenario persists the exact policy-to-set
`depends_on` edge, then restarts every BGP detach/delete checkpoint and verifies
that policy cleanup retains both router and set. An unresolved-reference scan
preserves the earlier policy observation and freshness. These are
parser/application tests, not independent cloud backend acceptance.
