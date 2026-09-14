# Cloud Router route policy evidence

The native contract is retained in `catalog/source/discovery.json` from the
[Compute v1 Discovery document](https://www.googleapis.com/discovery/v1/apis/compute/v1/rest),
revision `20260908`, full-response SHA-256
`aa1078267f6ad9c82274e6c62572bae328b0de11c6f08861f20488b6617afda7`.
Six unchanged method objects (including named-set GET/LIST) and their thirty-eight transitive schemas are selected in a
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

Remaining work includes computed named-set dependencies,
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
Native DELETE 404 requires independent readback. Dependency conflicts are surfaced.
Concurrent external policy edits cannot be atomically excluded by this API's
request contract. BGP reference detachment is covered below.

`route_policy_actions_test.go` retains protocol cases for native POST, missing
optional operation echoes, operation/receipt tampering, idempotent retries,
configuration drift, denial, dependency conflict, delayed visibility, operation
expiry and serialized driver restart. `route_policy_cleanup_worker_test.go` uses
real SQLite scan, graph, plan and execution services, reopening repositories and
recreating the runtime between action checkpoints. It verifies one policy-only
step, retained parent, a deletion tombstone and subsequent inventory reconciliation.
These are application/protocol tests, not live backend behavior validation.

## BGP reference detachment

[Router PATCH](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/patch)
uses JSON merge patch. Its Router schema has no fingerprint field and the method
has no conditional revision parameter. BGP peer arrays are replaced, so the
request retains all peer settings, excludes output-only `managementType`, and
removes only the selected policy name. Import/export policy order is preserved;
empty arrays explicitly clear the selected list. Unrelated Router fields,
including NAT, interfaces and authentication keys, are omitted from the patch.
The [policy application guide](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/bgp-route-policies/apply-policies)
confirms independent ordered import/export lists. Additional permissions are
`compute.routers.update` and, per the native audit permission list,
`compute.networks.updatePolicy` on the containing network.

The unmodified [Cloud SDK command](update_bgp_peer.py) reads the router, changes
peer policy fields, calls `ComputeRoutersPatchRequest` / `service.Patch`, and
waits on `compute.regionOperations`. It is retained from
`lib/surface/compute/routers/update_bgp_peer.py` in the same verified archive above;
member SHA-256 is
`18d2d0717fc8bc9fe1aeb1ee58dbfc05c0ae395c9aacb1457595758843026c48`.
The same Apache 2.0 license applies. It is source evidence, not a runtime dependency.

Inventory stores a peer snapshot and exposes `bgpReferences`; old reviews need a
new scan. Before mutation and during readback, live peers must retain the reviewed
membership, settings and policy order. The selected policy may already be partly
or fully detached; completed sibling deletions follow the checks below. Receipts bind both phases and use
separate deterministic native request IDs. PATCH DONE or operation expiry alone
cannot begin deletion; native peer readback must confirm detachment. A missing
policy with dangling reviewed references still requires unlinking. Receipt loss
reuses the same native UUID; persisted normal execution does not repeat mutation.
Concurrent external peer edits cannot be atomically excluded by Router PATCH.

`route_policy_bgp_test.go` checks exact replacement bodies, native schema
validation using the independent JSON Schema library, preserved peer settings,
import/export order, empty-list handling, lost receipts, changed membership,
permission/conflict failures, malformed/expired operations and serialized restart.
The SQLite scan/graph/cleanup/reconciliation test exercises both attached and
unattached policies, reopening storage and recreating the runtime at each phase.
The independent mockgcp audit above supplies no policy handlers; these tests
remain protocol/application evidence, not independent backend or live-cloud proof.


## Sequential policies from one scan

The [native BGP removal guide](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/disabling-removing-bgp)
requires GET followed by PATCH of the complete desired `bgpPeers` array. Reusing
one policy's original snapshot after a sibling was deleted would restore the
sibling reference; rejecting every such change instead prevented the second
policy's cleanup. The regression test reproduced that rejection before this fix.

The driver now constructs each detachment from a fresh native Router read. Live
import/export lists must be ordered subsequences of the reviewed lists, with
identical peer membership and other settings. Every removed sibling name must
return its own native policy 404; the Router is then reread to reject parent
recreation, incomplete responses or BGP changes during those absence checks.
A still-present, denied or malformed sibling cannot authorize the merge. The
immutable original review and both request UUIDs remain unchanged across restart.
This native merge preserves completed removals. Steward execution coordination
is described below; Router PATCH still supplies no atomic configuration condition.

`route_policy_multi_test.go` checks two policies sharing one scan, serialized
receipts/runtime recreation, preservation of earlier removals and unrelated NAT
and peer settings, partial target detachment, invalid sibling reads, parent
changes and a sibling deletion completing immediately before the final PATCH
read. `route_policy_multi_worker_test.go` runs real SQLite scan, graph, planning
and sequential execution with database/runtime reopening between checkpoints,
then verifies both tombstones, the retained Router and subsequent reconciliation.
The native fixture uses immediately visible completed operations; existing BGP
phase tests separately cover pending operations and delayed visibility. These
are locally authored protocol/application tests, not independent emulator proof.

```sh
go test ./providers/gcp -run 'TestRoutePolicySequential|TestRoutePolicySibling|TestRoutePolicyLastRead|TestRoutePolicySQLiteSequential' -count=1
```


## Shared Router mutation coordination

Router, RouterNat, RoutePolicy and NamedSet delete steps now share the same
connection/parent scope. Historical `gcp` and `google-cloud` partition names
normalize to one scope. The planner preserves existing DAG prerequisites before
adding same-parent serialization; other routers remain independent. The execution
creation/continuation guard uses frozen identities, including older tasks without
scope metadata, with inventory fallback for legacy tasks lacking snapshots.
Corrupt snapshots or mismatched native parent identities cannot erase a scope.

Before worker creation, old plans are upgraded in the same transaction without
changing step IDs or reviewed assets. Continuing a plan that needs new edges
requires terminal jobs and no unsettled previously invoked actions on the affected
router. Metadata-only upgrades do not block already correctly ordered work.
The connection/execution locks and durable worker dependencies are reused; no new
scheduler, runtime dependency, database schema or native CAS is introduced.

The existing read-only terminal-operation recovery remains NAT-only. In a policy
flow, an old detach receipt can coexist with an already-issued native deletion
whose later receipt was not saved. A DONE detach operation cannot release scope;
full recovery for uncertain legacy multi-phase writes remains unfinished.

`route_policy_multi_worker_test.go` now uses concurrency two, claims both worker
jobs and repeatedly tries the dependent policy before its predecessor settles.
It verifies no second mutation at each checkpoint, across database/runtime
reopening. Both current plans and old plans with removed scope/ordering metadata
complete, retain the Router and reconcile policy tombstones. The legacy case
also fails the original jobs before invocation, removes ordering again and uses
the real ContinueExecution service to persist the upgrade and resume the same
execution and step IDs. A second task's
execution request is rejected while the first occupies the Router.

`router_configuration_test.go` covers every parent/component kind, cross-partition
scope aliases, native/frozen/legacy inventory identities, forged annotations,
malformed snapshots, different routers, terminal success, uncertain detach,
legacy DAG ordering and continuation with resumed actions or expired worker
leases. `order_test.go` rejects missing dependencies, duplicate identities and
cycles. These are locally authored SQLite/protocol tests. Independent mock-server,
live-cloud acceptance and full parent Router cascade remain unfinished.

Execution coordination excludes only the exact attempt being continued, not all
attempts belonging to the same task. Regression coverage rejects a fresh attempt
against a still-occupied same-task scope while allowing the bound continuation.
