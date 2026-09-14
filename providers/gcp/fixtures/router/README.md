# Native Router review and cascade cleanup

The [native Router GET contract](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/get)
returns the Router resource, including embedded NATs, BGP peers, interfaces and
MD5 authentication key metadata. The [Router schema](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers)
is already pinned in the first Compute Discovery document in
`catalog/source/discovery.json`. `RouterMd5AuthenticationKey.key` is input-only;
Steward also redacts this container in request logs and unexpected secret-bearing
responses. The live request/response remains available to native operations.

The [router deletion guide](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/managing-routers)
explicitly documents NAT cascade and prerequisite removal of VPN tunnels/VLAN
attachments. This motivates capturing complete parent configuration before
controller cleanup. It does not establish policy/named-set cascade. Their
independent native deletions are the prerequisite path used by the Router
lifecycle contributor and execution driver.

Native Router inventory now performs one GET per listed Router observation.
It checks the listed numeric incarnation against current detail, native
name/selfLink/region, complete array/object shapes, unique NAT/peer/interface/key
names and typed interface references. Unknown native fields remain available.
Temporary parent enumeration for child sources still uses native LIST; it does
not persist a Router observation or issue redundant Router GETs. Child sources
retain their existing independent detail/parent checks and cursor bindings.

Two opaque configuration digests are retained before redaction: a full review
and a base review excluding NAT/BGP-peer collections that subsequent reviewed
child cleanup can change. Unknown configuration, authentication changes and BGP
policy ordering affect the full proof. Native unordered named collections are
canonicalized; documented output-only bookkeeping and effective NAT timeout do
not fabricate configuration changes. The execution checks below consume these
proofs after separately reviewed policy removals.

`router_inventory_test.go` exercises the real runtime with explicit native
LIST/GET fixtures for regional, project, global and VPC scans, pagination,
LIST/detail drift, malformed native containers, partial/denied reads and
redaction. The Router fixture validates against the pinned native schema.
Request/response log tests verify that redaction does not modify wire data.
SQLite scan jobs reopen the database across successful, denied, missing,
incarnation-changed, empty and recovered observations; failures preserve history,
authoritative absence closes records, and recovery retains the same asset ID
with new review evidence. Existing NAT/policy/set scans and cleanup tests remain
in the regression scope.

```sh
go test ./providers/gcp ./internal/...
go test -race ./providers/gcp -run 'TestRouter|TestCloudNat|TestRoutePolicy|TestNamedSet|TestClientResponse' -count=1
go vet ./providers/gcp ./internal/...
node docs/check.mjs
```

All 60 native source documents and generated catalog bytes remain unchanged:
200 rules, 785 operations, catalog SHA-256
`f0e0eaef95da03d8ca1e7bb36803248b2d9e33321b004b35dccdd925adb84f04`.
These are protocol/application tests, not independent mock-server or live-cloud
acceptance. Uncertain legacy multi-phase recovery, computed CEL references and
the broader provider acceptance criteria remain open.


## Native lifecycle and parent deletion

The service contributor now registers Router. It reads the current native parent,
checks the full reviewed configuration and independently lists/reads both policy
and named-set collections, then repeats membership and parent checks. Query GETs
and embedded `Router.nats` use their native contracts, not fabricated child paths.
Unindexed members are cleanup blockers. Indexed members must match their scanned
configuration and numeric parent identity. VPN/VLAN interface references remain
blocking until their associated resources are removed and a new scan is reviewed.

NAT bindings are exclusive delegated deletes with parent-verified absence, while
policies/sets are direct prerequisites. Independent NAT deletion remains allowed;
retaining a child while deleting the Router is rejected by planning and execution.
The existing shared Router coordination orders these steps and excludes competing
cleanup attempts. Policy-to-set references continue to supply their own DAG edges.

The parent driver validates the connection/resource/action, full/base review,
unique child impact/prerequisite identities and all child incarnations. Native
reads must show prerequisites absent, only reviewed unchanged NATs and the original
BGP configuration with exactly the prerequisite policy references removed. Other
settings, interfaces and authentication changes stop execution. A complete Router
array can prove that a reviewed NAT was independently removed before deletion.

Native `compute.routers.delete` is an empty-body DELETE with a deterministic UUID
bound to the parent incarnation, full review and child intent. Regional operation
receipts reuse component validation and bind request, target, region, operation
name/type and original review. Router remains distinct from virtual/query-addressed
`isRouterComponent` classification. Wait performs no mutation. Pending operations
or still-visible parents cannot complete; malformed/foreign/failed operations and
modified receipts fail. Expired operations still require native absence readback.

After native parent 404, direct prerequisites are queried and parent absence is
confirmed again; documented embedded NAT cascade is then recorded. If deletion
becomes visible during child LIST or the last Router GET, a bounded native parent
confirmation handles that race without treating child 404 alone as proof. A
same-name replacement with another numeric ID never inherits deletion proof.
These reads have no atomic native configuration precondition. Older generic
Router receipts are not accepted as new bound receipts; legacy recovery remains
unfinished rather than guessing whether an old mutation can still execute.

`router_cascade_test.go` covers native dependency order, NAT retention/independent
cleanup, missing indexed children, permissions, malformed/partial/paged lists,
changing membership/configuration, parent/prerequisite/impact identity drift,
operation pending/failure/expiry/target/request/type errors, receipt tampering and
serialized runtime restart. The fixture has both referenced policies present;
the named set becomes deletable when its referring policy has gone, without
requiring deletion of an unrelated policy first.

`router_cascade_worker_test.go` uses real SQLite scan/graph/plan/execution services
with the native service contributor and CEL-derived policy/set relationship. A
single Router selection produces three prerequisite delete steps and a reviewed
NAT impact. With concurrency four it probes the Router job before every earlier
checkpoint, recreates the runtime/database, verifies all five deletion tombstones
and the NAT controller-deletion result, then rescans to prove reconciliation.
No separate NAT PATCH/address DELETE is issued for the parent cascade. These are
locally authored protocol/application tests, not independent emulator acceptance.
