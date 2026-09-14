# Cloud NAT inventory evidence

The native [Router schema](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers)
embeds `RouterNat` objects in `nats`. Existing `compute.routers.get` supplies the
complete array; only the parent Router LIST paginates. No child GET/LIST/DELETE
endpoint is invented. Native read permissions are `compute.routers.list` and
`compute.routers.get`.

The existing pinned Compute Discovery document has revision `20260828` and source
SHA-256 `5cee2d2fedf69f756fbc23aaef9153db01c2a2caffdbd6f63681912b65ca8139`.
Its RouterNat schema equals the retained revision `20260908` schema. All 60 native
source documents, 785 generated operations and 199 prior resource rules remain
unchanged. The new rule produces 200 resource types; generated catalog SHA-256 is
`2c96b58da0d7f550d427f170bbcce6d0f96f6e37c4d21c556c683c26ab2080c3`.

The logical inventory type is `compute.googleapis.com/RouterNat`, with identity
`//compute.googleapis.com/projects/{project}/regions/{region}/routers/{router}/nats/{nat}`.
This is neither a public REST endpoint nor a claimed CAI asset type. Native NAT
configuration is preserved separately from parent BGP peers/authentication keys.
Explicit subnet/NAT64 references, active/draining IPs and private source ranges
become dependencies. Hub references in CEL are not yet resolved.

`cloud_nat_test.go` supplies native-shaped protocol fixtures. Tests cover regional,
project, global and network scopes, parent pagination, distinct same-name NATs,
parent-incarnation-bound cursors, configuration property queries, malformed/partial
responses and parent identity changes. JSON Schema checks use the pinned native
RouterNat definition; these are shape checks, not server semantic acceptance.
SQLite scan jobs verify failure history, authoritative absence and recovery. The
real graph worker persists the scanned NAT's dependency on a seeded matching
router asset. No cloud resource is created or deleted by these tests.

```sh
go test ./providers/gcp -run 'TestCloudNat|TestRoutePolicy|TestNamedSet|TestCatalogReproducible|TestGCPChangedPropertyPaths' -count=1
go test -race ./providers/gcp -run 'TestCloudNat|TestRoutePolicy|TestNamedSet' -count=1
go test ./providers/gcp ./internal/...
go vet ./providers/gcp ./internal/...
```

The inspected pinned mockgcp Router implementation provides GET/PATCH but no
Router LIST; no independent NAT mock-server or live-cloud acceptance is claimed.
Google's [router management guide](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/managing-routers)
explicitly states that deleting a router deletes its Cloud NAT gateways, motivating
this inventory prerequisite. Independent NAT removal, complete parent-router
cascade review and the overall provider parity criteria remain unfinished.
