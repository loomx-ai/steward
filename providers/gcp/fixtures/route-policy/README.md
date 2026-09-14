# Cloud Router route policy inventory evidence

The native contract is retained in `catalog/source/discovery.json` from the
[Compute v1 Discovery document](https://www.googleapis.com/discovery/v1/apis/compute/v1/rest),
revision `20260908`, full-response SHA-256
`aa1078267f6ad9c82274e6c62572bae328b0de11c6f08861f20488b6617afda7`.
Two unchanged method objects and their five transitive schemas are selected in a
separate fragment, preserving all earlier native metadata.

- [routers.listRoutePolicies](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/listRoutePolicies)
  uses the selected project, region and parent router. The collection is `result`,
  with `maxResults`, `pageToken` and `nextPageToken`; partial warnings and
  `unreachables` cannot prove absence.
- [routers.getRoutePolicy](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/getRoutePolicy)
  addresses the policy with the `policy` query parameter and wraps the detail in
  `resource`. The route policy's name is unique within its parent router.
- [Native CEL examples](https://docs.cloud.google.com/network-connectivity/docs/router/reference/bgp-route-policy-reference)
  support the fixture's destination equality match and `accept()` action. Priority
  orders terms; the server need not preserve their JSON array order.

`compute.googleapis.com/RoutePolicy` is Steward's independently indexed native
RoutePolicy kind. Its composite identity is
`//compute.googleapis.com/projects/{project}/regions/{region}/routers/{router}/routePolicies/{policy}`.
This is an inventory identity, not a REST endpoint. Reads bind the verified
native method above. No Cloud Asset Inventory support for this type is assumed.

Run from the repository root:

```sh
go test ./providers/gcp -run 'TestRoutePolicy|TestCatalogReproducible' -count=1
go test -race ./providers/gcp -run 'TestRoutePolicy' -count=1
```

The locally authored protocol fixtures exercise router and policy paging,
project/regional/global scope, same policy names in different routers,
project-number aliases, parent identity/UID changes, changed detail identity,
403/404, invalid response wrappers/fields, incomplete lists and cursor cycles.
Real SQLite workers verify failed details preserve searchable prior observations,
a successful empty list closes the old policy, and reappearance refreshes the
same asset. The router remains a dependency; cleanup is not implemented yet.

Independent emulator audit: [Google Config Connector's router server](https://github.com/GoogleCloudPlatform/k8s-config-connector/blob/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockcompute/routersv1.go)
implements Router Get/Insert/Patch/Update/Delete and embeds
`UnimplementedRoutersServer`. At that pinned revision it implements neither
Router List nor route-policy List/Get/Delete. The complete, non-truncated mockgcp
subtree `8b6f0584391a159b43ed7e5adf26580557872854` was checked for router handlers.
No independent route-policy emulator or real-cloud test is claimed.

Remaining work includes native deletion and operation readback, BGP peer
attachments, named-set dependencies, parent-router cascade review, network-scan
application acceptance and independent/live-cloud behavior verification. This
inventory milestone does not establish Alibaba Cloud route-map cleanup parity.
