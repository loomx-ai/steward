# Cloud Router route policy evidence

The native contract is retained in `catalog/source/discovery.json` from the
[Compute v1 Discovery document](https://www.googleapis.com/discovery/v1/apis/compute/v1/rest),
revision `20260908`, full-response SHA-256
`aa1078267f6ad9c82274e6c62572bae328b0de11c6f08861f20488b6617afda7`.
Three unchanged method objects and their twenty transitive schemas are selected in a
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
same asset. The router remains a dependency; native policy cleanup is covered below.

Independent emulator audit: [Google Config Connector's router server](https://github.com/GoogleCloudPlatform/k8s-config-connector/blob/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockcompute/routersv1.go)
implements Router Get/Insert/Patch/Update/Delete and embeds
`UnimplementedRoutersServer`. At that pinned revision it implements neither
Router List nor route-policy List/Get/Delete. The complete, non-truncated mockgcp
subtree `8b6f0584391a159b43ed7e5adf26580557872854` was checked for router handlers.
No independent route-policy emulator or real-cloud test is claimed.

Remaining work includes explicit BGP peer detachment, named-set dependencies,
parent-router cascade review, network-scan
application acceptance and independent/live-cloud behavior verification. These
milestones do not establish full Alibaba Cloud route-map cleanup parity.

## Independent native deletion

[routers.deleteRoutePolicy](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/deleteRoutePolicy)
is POST with `policy` and optional UUID `requestId` query parameters, an empty
body, and a regional Compute Operation response. It has no fingerprint input.
The preserved [Cloud SDK command](remove_route_policy.py) calls DeleteRoutePolicy
directly; it does not issue a Router Patch/Delete. This file is retained verbatim
from `lib/surface/compute/routers/remove_route_policy.py` in the official
[Cloud SDK core archive](https://dl.google.com/dl/cloudsdk/channels/rapid/components/google-cloud-sdk-core-20260831161632.tar.gz).
Archive SHA-256 is
`cecc5d244c10e3bc8ef7449937569c40339b90686b902fd48b4afc6f47ebfeb3`;
member SHA-256 is
`3a09e9f8097f905088e33a2bdc178c1e92577f27e423c08819c9e86e09e0d2fb`.
Its Google copyright header and [Apache 2.0 license](../../catalog/source/sdk/LICENSE)
apply. The file is evidence, not an executed runtime dependency.

The driver binds its review to the policy fingerprint/content and containing
router's numeric ID. Term order is normalized without reordering actions within
a term. Regional operation URLs come from the native catalog, and persisted
receipts bind the policy, connection, request ID and review. Optional native
target/scope/request echoes must agree. DONE or an expired operation alone does
not prove absence; the driver reads the policy and confirms the same parent
before and after that read. 403, changed identities and parent 404 remain failures.
Native DELETE 404 requires independent readback. Dependency conflicts are surfaced,
without issuing speculative BGP configuration updates. Concurrent external policy
edits cannot be atomically excluded by this API's request contract.

`route_policy_actions_test.go` retains protocol cases for native POST, missing
optional operation echoes, operation/receipt tampering, idempotent retries,
configuration drift, denial, dependency conflict, delayed visibility, operation
expiry and serialized driver restart. `route_policy_cleanup_worker_test.go` uses
real SQLite scan, graph, plan and execution services, reopening repositories and
recreating the runtime between action checkpoints. It verifies one policy-only
step, retained parent, a deletion tombstone and subsequent inventory reconciliation.
These are application/protocol tests, not live backend behavior validation.
