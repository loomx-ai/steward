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
this inventory prerequisite. At that inventory milestone, independent NAT removal and complete parent-router
cascade review remained unfinished; the extension below implements native removal.

## Native independent removal

The [Public NAT guide](https://docs.cloud.google.com/nat/docs/set-up-manage-network-address-translation)
and [Private NAT guide](https://docs.cloud.google.com/nat/docs/set-up-private-nat)
specify removal of the gateway configuration while retaining its router.
The native [Router PATCH API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/patch)
uses JSON merge patch. Its optional nonzero UUID `requestId` deduplicates retries;
there is no fingerprint/etag revision precondition. IAM requires Router get/update,
with `compute.regionOperations.get` for polling.

The original Google Cloud SDK NAT deletion implementation is retained in
[delete.py](delete.py), with [archive/member hashes](delete-provenance.json) and
[Apache license](../../catalog/source/sdk/LICENSE). It GETs the router, removes
named NAT entries, and explicitly includes the empty `nats` field when removing
the last NAT. The file is evidence, not executed by Steward. The archive SHA-256
is `cecc5d244c10e3bc8ef7449937569c40339b90686b902fd48b4afc6f47ebfeb3`;
the retained member SHA-256 is
`bf25c9a487ca4e0edcf7ff5bbae1f4f417787dabea7cf393fbf98acdf54ca96f`.

Steward rereads the complete matching Router immediately before constructing
`{ "nats": [...] }`, removes only the selected native name, and preserves the
latest other NAT configurations including unknown writable fields. It omits the
documented output-only `effectiveTcpTimeWaitTimeoutSec`. Other Router properties
and manual address resources are not mutated. Scans retain a native configuration
review, including unknown fields, separately from normalized query metadata.
Changes to the target or parent incarnation reject old cleanup requests. No NAT
UID or fingerprint is invented; a recreated identical configuration cannot be
distinguished by an unavailable native revision token.

Existing router-component receipt/operation validation binds identity, connection,
parent incarnation, reviewed configuration and deterministic request UUID. Waiters
handle RUNNING/DONE/expired operations but confirm deletion only after a complete,
matching Router GET omits the NAT. Parent GET/PATCH 404 and permission failures
cannot prove NAT absence. A missing PATCH fails even if an unrelated read would
suggest absence. Polling and worker restarts never resend a known operation.

Cleanup planning adds only execution-order edges between selected NAT deletions
on the same router. It uses the existing topological order and durable worker
prerequisites; no fictitious inventory dependency is added. Connection-locked
execution creation and continuation reject overlapping router updates from another
unresolved task. A completed matching action releases its scope even if other
actions failed. Failed/canceled tasks without verified completion conservatively
retain scope. Failed tasks can be continued; canceled-task reconciliation/release
is still unfinished, and cancellation before invocation can conservatively retain
scope as well. This limitation is not claimed as completed recovery. This coordination
covers Steward tasks, not independent external writers; native PATCH lacks CAS.

The shared protocol action matrix now covers NATs alongside policies and named
sets: receipt tampering, operation scope/target/request errors, native errors,
configuration changes, expiration, retries and absence. NAT-specific tests cover
latest sibling preservation, unknown/output-only fields, sequential removals and
an explicit empty final array. SQLite scan/graph/planning/execution tests reopen
the database at each asynchronous checkpoint and verify a single PATCH, final
NAT tombstone and retained router. Actual task creation rejects a competing NAT
execution; separate SQLite scope tests cover paused/failed/canceled runs and
release after verified success.

All 60 native source documents remain unchanged. The catalog still has 200 rules
and 785 operations; only RouterNat's deletion binding and the generated destructive
flag on `compute.routers.patch` change. Updated catalog SHA-256:
`f0e0eaef95da03d8ca1e7bb36803248b2d9e33321b004b35dccdd925adb84f04`.
The importer tests ensure a PATCH becomes destructive only when explicitly bound
as a resource deletion; GET cannot gain destructive classification this way.
Parent cascade, independent backend/live acceptance and broader parity remain open.
