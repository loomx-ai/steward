# Cloud Identity group verification

Steward inventories native v1 Groups and Memberships within an explicitly selected
`customers/CUSTOMER_ID` or `identitysources/ID` directory. The connection's
`identity_group_parent` is optional and participates in credential-cache identity.
Project ownership does not establish directory authority. Google supports assigning
a service account a Groups Admin role without domain-wide delegation; Steward
uses the existing service-account OAuth flow and the native `cloud-platform`
scope. See Google's [authentication setup](https://docs.cloud.google.com/identity/docs/how-to/setup).

## Native contracts and provenance

| Contract | Native behavior |
| --- | --- |
| [Groups list](https://docs.cloud.google.com/identity/docs/reference/rest/v1/groups/list) | Customer or identity-source parent, `view=FULL`, `pageSize=500`, opaque page tokens |
| [Memberships list](https://docs.cloud.google.com/identity/docs/reference/rest/v1/groups.memberships/list) | Unique parent group, FULL view and complete pagination |
| [Membership resource](https://docs.cloud.google.com/identity/docs/reference/rest/v1/groups.memberships) | Unique membership name, entity key, creation/update times, roles, expiration and restriction evaluation; unspecified roles default to MEMBER |
| [Groups delete](https://docs.cloud.google.com/identity/docs/reference/rest/v1/groups/delete) | DELETE of `groups/ID`, empty body, native Operation result |
| [Membership delete](https://docs.cloud.google.com/identity/docs/reference/rest/v1/groups.memberships/delete) | DELETE of one relationship, empty body, native Operation result |
| [Operation](https://docs.cloud.google.com/identity/docs/reference/rest/Shared.Types/Operation) | `done` and error/response envelope; the public v1 Discovery has no operations GET method |
| [Group types](https://docs.cloud.google.com/identity/docs/groups) | Ordinary, security, locked, dynamic and external identity-mapped groups |

The selected seven methods and 15 retained native schemas come from the complete
[Cloud Identity v1 Discovery document](https://cloudidentity.googleapis.com/$discovery/rest?version=v1),
revision `20260906`, SHA-256
`29aa2b8422f6f11c5f5a10fcdeb601dbbd84dc16cb4c4c64a17bb9fa5a86ae3e`.
The retained schemas are unmodified, including transitive references. An
independent JSON Schema validator checks the protocol fixtures. All preceding
55 source documents remain unchanged. This brings the GCP catalog to 193 rules,
175 kinds with native deletion, and 768 selected methods from 55 Discovery
documents and one pinned Cloud SDK archive.

## Review and execution

The `identity-groups-visible` inventory source is non-authoritative. Permission
loss, a changed directory or a visibility-filtered empty list cannot close earlier
observations. The registry, inventory Creator, worker and SQLite tests verify two
global shards and persistence of native scope/configuration proofs. Directory
resources have no normalized project ownership fields.

Each group uses two complete membership lists, native member GETs, group GETs on
both sides of the membership reads, and security-settings GETs for security
groups. Both directory lists must agree. Reviewed proofs freeze the exact unique
IDs, native creation/configuration, aliases, member keys, roles, expiration,
security settings and dynamic queries. Native set ordering is immaterial. The
dynamic status timestamp is observational; it is not a configuration change.

The solver treats member relationships as native group-delete impacts. Every
reviewed link must match the plan before group deletion. Member users, service
accounts and nested group objects are not cascade deletion targets. Independent
membership cleanup removes only that link and verifies the containing group's
configuration remains. Locked groups stay protected; dynamic membership unlink
is denied and group deletion requires the dynamic membership state to be current.
Google documents that [deleting a group preserves its member users and groups](https://docs.cloud.google.com/architecture/identity/overview-google-authentication).

Group GET can return HTTP 403 with an ambiguous permission-or-nonexistence error
after successful deletion. That response alone never proves absence. The worker
passes its persisted execution result to readback through a transient,
non-JSON ActionRequest field. A native successful `done:true` receipt, bound to
the reviewed asset, configuration and idempotency key, can resolve this ambiguity.
All readable surviving groups or reviewed memberships still block completion.
OAuth failures, HTTP 401/5xx, malformed responses and failed/mismatched receipts
cannot be converted to success. A failure reading security settings of a live
group cannot masquerade as a failed group GET.

The final state is `delete_confirmed` when a successful native receipt establishes
completion. A missing/false done flag can complete only through actual resource
absence. No operations endpoint is invented. If an acknowledgement was lost before
it reached persistent storage, an ambiguous 403 remains a failure. The real
SQLite cleanup-worker tests restart between DELETE, wait and readback; lost or
corrupt receipts and native survivors cannot succeed or replay the deletion.

## Independent Google mock

Tests use unmodified [Google Config Connector mockcloudidentity](https://github.com/GoogleCloudPlatform/k8s-config-connector/tree/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockcloudidentity)
at commit `673a61419de1b8e4f7d26070ce20dde2daa61da8` in its own Go module.
The retained harness exposes its native gRPC/HTTP mux and in-memory storage over
loopback. Its four service files were verified against these SHA-256 hashes:

| File | SHA-256 |
| --- | --- |
| groups.go | `7e522583da9ef5ef74f9bd1bd7c07580706b05bd9c1e3c2009dfe889bc507bb3` |
| groupsmembership.go | `0f4b188604007baa04e0e806c3708690d6b3d267fc3f95a687b2690530435775` |
| service.go | `b5e8de781f33a22c9da98911b58b8159c917eea1c989153731fed00ec2b7ac74` |
| utils.go | `aa6922cf3985871e4c08e2cbe307d9c7be69fbba09a012157de7dc6c6c2c0bac` |

The upstream mock implements the relevant native GET/create/delete handlers in
v1beta1 only. The test explicitly aliases v1 to that version and supplies
GET-backed lists for its known fixture resources. Native mutations, unique IDs,
creation times, alias population, done-only operations and GET absence/error
responses are forwarded unchanged. This independently verifies membership unlink,
serialized restart and ambiguous group GET handling.

The mock does **not** cascade membership records when deleting a group. The test
asserts that this real surviving record blocks completion, then removes it using
a separate native fixture cleanup call. It does not replace that record with a
fabricated 404. Native group cascades, dynamic/security/locked groups, permission
changes and delayed completion have protocol coverage; this mock is not a full
v1 emulator or a real-cloud acceptance environment.

To reproduce, clone the pinned repository into a temporary directory, with
`mockgcp` in its sparse checkout. Copy `testdata/mockgcp/main.go` to
`mockgcp/cmd/steward-identity/main.go`, then run inside that module:

```sh
GOWORK=off go build -o /tmp/steward-identity-mock ./cmd/steward-identity
/tmp/steward-identity-mock
```

Use its printed port from the Steward checkout:

```sh
STEWARD_IDENTITY_MOCKGCP_URL=http://127.0.0.1:PORT \
  go test ./providers/gcp -run '^TestIdentityGroupsIndependentMockGCP$' -count=1 -v
go test -race ./providers/gcp ./internal/app/cleanup \
  -run 'TestIdentity|TestExecutionHandlerPersistsWaiterData' -count=1
```

Stop the fixture server and remove its temporary checkout and binary afterward.
Normal builds and tests remain offline and add no provider SDK dependency.

## Limits

[Group deletion is irreversible](https://docs.cloud.google.com/iam/docs/groups-in-cloud-console#deleting_a_group) and changes access granted through the group.
The API has no etag or request-ID condition on DELETE; concurrent changes after
the final read remain possible. Native deletion does not remove external IAM
policy bindings, shared-document policies or other products' references. A
[group membership reverse search](https://docs.cloud.google.com/identity/docs/reference/rest/v1/groups.memberships/searchDirectGroups)
silently filters groups the caller cannot view, so it cannot prove the absence of
incoming references across every directory. The reviewed cascade covers the
selected group's own member links; it does not claim an exhaustive cross-directory
access-impact analysis. Propagation of access changes, domain-wide delegation,
consumer Google Groups and real-cloud acceptance are separate coverage.
