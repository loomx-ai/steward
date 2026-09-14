# Native Router review prerequisite

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
independent native deletions remain the intended prerequisite path; complete
Router lifecycle contribution and execution integration are still unfinished.

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
not fabricate configuration changes. Proofs are a prerequisite, not evidence
that Router cascade deletion is already implemented.

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
acceptance. Router lifecycle, concurrent parent/component mutations, computed CEL
references and the broader provider acceptance criteria remain open.
