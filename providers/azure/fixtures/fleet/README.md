# Azure Kubernetes Fleet native evidence

The 27 JSON examples are unchanged files from Microsoft's
[stable 2026-06-01 Fleet API](https://github.com/Azure/azure-rest-api-specs/tree/6005e166d7172cb62fd2894971dbfe1910ac5285/specification/containerservice/resource-manager/Microsoft.ContainerService/fleet/stable/2026-06-01) and its [2026-06-02-preview API](https://github.com/Azure/azure-rest-api-specs/tree/6005e166d7172cb62fd2894971dbfe1910ac5285/specification/containerservice/resource-manager/Microsoft.ContainerService/fleet/preview/2026-06-02-preview). Member GET/LIST and the five Cluster Mesh operations use the preview; other existing operations retain the stable version.
`sources.json` records each operation, route, original URI and SHA-256;
`documents.json` records the full-source fingerprints of the two root Swaggers and
their two shared common-type dependencies. The checked-in catalog retains the selected
operations and their transitive native schemas. Ordinary tests need no network.

Selected operations cover Fleet GET, both root LISTs and DELETE; member,
managed namespace, update strategy, auto-upgrade profile and update run
GET/LIST/DELETE; update-run Stop; Gate GET/LIST; and Cluster Mesh GET/LIST,
PUT CreateOrUpdate, POST Apply and DELETE. Gates have no native DELETE.
Mesh PUT and Apply are used only to prepare disconnection during cleanup.
Credentials, resource creation, run Start, Skip and Gate approval are not exposed.

`TestFleetNativeSources` verifies provenance, binds all 27 requests, and checks
the 41 native responses, including 21 bodies, against the retained schemas.
Two original list responses use `nextLink:null` although their schema says
string. The test asserts that exact discrepancy before checking the remaining
fields. Original placeholder subscription IDs, differing request/response
resource names, a singular `virtualNetwork` path, `userAssignedIdentities.key126`
and illustrative foreign continuation URLs remain unchanged. Protocol scenarios
compose real-format identities explicitly; production never accepts these
placeholders or follows a foreign continuation URL.

`TestFleetNativeIndexBoundaries` exercises seven resource families through the
actual HTTP request/response code. It checks paging, additional GET detail,
independent resource GETs, private LIST/GET disagreement, malformed and repeated
rows, incomplete responses, permission denial, dependency 404s, changed resource
identity, and continuation scope/version/filter boundaries. Other tests cover
native operation capability and scope, cross-subscription AKS member references,
same-Fleet strategy/profile/run references, private authored configuration and
fixed versus dynamic namespace placement. These are native-protocol tests with
composed HTTP responses, not independent emulator or live-cloud verification.

`cli-recordings.json` additionally retains 44 actual GET/DELETE responses from
Microsoft's `test_fleet_hubful.yaml` and `test_fleet_hubless.yaml` at immutable
CLI-extension commit `a20385bffcbb7403846af8a65dbfcaf96c717e4d`. Each row identifies
the original source file and hash, interaction index, method and URL, status,
selected protocol headers and unchanged body string. To reproduce, verify the
listed source hashes, parse each YAML `interactions` array, select GET/DELETE
URLs containing `/providers/Microsoft.ContainerService/fleets` ignoring case,
and copy the response fields. Retained headers are content type, ETag,
request/correlation IDs, Location, Azure-AsyncOperation and Retry-After.

`TestFleetRecordedResponseCompatibility` validates 44 returned resource objects
across all seven families, nine DELETE responses and a real 503 HTML failure.
The latter passes through the transport and cannot become resource absence.
Five managed-namespace responses use fixed member placement and contain an
additional `rolloutStrategy`; three Gate responses use `ScheduledStart`.
The native `gateType` schema is `modelAsString`: the read-only adapter preserves
the opaque subtype configuration and still exposes no Gate mutation. These
recordings use **2026-06-02-preview**. They demonstrate response compatibility,
not a stable-version live call; stable request binding remains independently
tested against **2026-06-01**. Extra private configuration remains in the digest.

The native lifecycle distinctions driving the subsequent registered integration:

- [Members reference existing clusters](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/concepts-fleet)
  and can be in different resource groups, regions and subscriptions within the
  same tenant. [Removing a member or Fleet](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/howto-configure-use-cross-cluster-networking)
  does not authorize separately deleting its AKS cluster.
- [Managed namespace deletion](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/howto-managed-namespaces#delete-a-managed-fleet-namespace)
  with `Keep` retains ordinary Kubernetes namespaces; `Delete` removes their
  namespaces and resources. Both policies delete associated Azure RBAC
  assignments. A policy change therefore changes the reviewed configuration.
- An omitted propagation policy deploys only to the hub. An explicit placement
  policy can select fixed members or use scheduling rules. The latter conservatively
  blocks deletion of members in the same Fleet until the namespace is removed;
  it cannot be treated as an empty fixed selection.
- [Hub and node resources](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/concepts-lifecycle)
  have separate managed resource groups. Group names alone are not ownership
  evidence. The native Fleet response exposes no managed resource-group ARM ID.
- Update runs snapshot their referenced strategy at creation; later strategy
  changes do not propagate to that stored run configuration. Runtime progress
  is separate from the authored configuration. Stopping a run is a distinct
  operation and is not a deletion receipt.
- [Gates are deleted with their associated update run](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/faq#how-do-i-delete-an-approval).
  Their ARM path is under the Fleet, but the run identified by `target.id` owns
  this lifecycle. Native resource nesting alone would assign the wrong owner.

All eight kinds are registered for native inventory with private configuration
and context proofs and non-authoritative known-ID reconciliation. The native
lifecycle graph verifies independent child cleanup and Gate ownership by its
referenced run. Reverse Fleet indexes also guard deletion of AKS clusters,
subnets, user-assigned identities, strategies and members. Dynamic namespace
placement conservatively blocks removal of any member of the same Fleet until
the namespace is explicitly removed; it does not assert actual placement.
Six independent child kinds now have registered native cleanup drivers.
Fleet root cleanup remains disabled pending its driver and residual readback; Gates
have no independent action and are removed with their reviewed owning run.

`cli-deletion-polls.json` retains 13 representative GET responses belonging to
the five asynchronous Fleet deletions in those same immutable recordings. For
each operation and endpoint collection, it keeps the first response for each
distinct HTTP status and operation state. Repeated identical pending states are
omitted; original URLs, body strings, protocol headers and interaction indexes
are unchanged. With PyYAML installed, reproduce it from the two original YAML
files using `python3 reproduce_poll_recordings.py HUBFUL.yaml HUBLESS.yaml`.
The script verifies both full-file SHA-256s before writing the fixture.

These recordings expose subscription-scoped ContainerService `operations` and
`operationresults` endpoints, signed with `api-version=2016-03-30` and four opaque
query parameters. The stable Swagger examples separately publish unsigned,
resource-group-scoped `operationResults` with `api-version=2022-02-01`.
`TestFleetRecordedDeletionPollingAndLogRedaction` replays the native DELETE
envelopes and all 13 retained polls through the actual transport and parser,
including Location-only fallback, and verifies that signing material stays out
of logs. Additional protocol tests cover changed scope/version/operation IDs,
malformed or failed status envelopes, expired operation URLs, refreshed signed
successors and serialized recovery receipts. Operation success or operation 404
only permits resource readback; it does not establish resource absence.

`TestFleetUpdateRunStopDeleteAndRecovery` separates native Stop and DELETE
receipts, including HTTP 200 with `Stopping`, HTTP 202 polling, an existing
stop, the documented Skipped-to-Stopped transition, and native terminal states. It serializes and restores every phase,
retains the original operation identity, clears prior phase polling successors,
and checks run and Gate residuals independently. Both mutations use the latest
verified native ETag as `If-Match`, as declared by the pinned operations.
Additional tests cover Keep/Delete namespace policies, conditional conflicts,
permission failures, own-resource versus mutation 404, incomplete dependencies,
changed context and private configuration, unknown status, and tampered phase
or request receipts. Gate approval/skip progress may change without changing
its private ownership proof; target and authored subtype settings stay bound.

`TestFleetRegisteredCleanupWorkerAndPhaseRecovery` uses the actual registered
scan, graph, cleanup planner, SQLite repository and execution workers. It
requires explicit namespace-before-member and profile-before-strategy deletion,
creates five native steps and one delegated Gate impact, resumes the run's Stop
and DELETE phases after worker/client recreation, and only closes the Gate after
its own GET confirms absence. A final scan retains only the Fleet root.
These are protocol fixture and persistence tests, not live Azure execution.

`TestFleetHubRegisteredInventoryAnchors` and its boundary/recovery tests join
native resource-group `managedBy`, the Fleet hub endpoint, the AKS public or
private endpoint, and its explicitly named node group with reciprocal ownership.
The tests use deliberately arbitrary group names. They cover missing or
ambiguous ownership, malformed/changed native details, asynchronous or forbidden
reads, filtered continuations, known LIST omissions, own-resource 404s, private
configuration changes, and changes during the independent observations. The
three anchors and controller are re-read after the join. Saved IDs and full
configuration digests are authenticated and survive JSON serialization; their
raw configuration and endpoints never enter the Fleet inventory or Hub-read
response logs. Full native responses remain available to the ownership checks.

The AKS and resource-group responses in these tests are composed protocol
fixtures. The retained Fleet CLI recordings contain no Hub AKS or managed-group
GETs, so these tests do not establish live-cloud compatibility of that ownership
join. Unverified joins remain protected, and root cleanup remains disabled until
the root driver and residual checks are ready.

`TestFleetHubInventoryCapturesNativeDescendants` composes the established native
Uniform scale-set and private-DNS protocol scenarios beneath the verified Hub.
It covers both groups, nested instances/NICs/IPs/extensions, an external managed
disk, a VNet's external DNS link and auto-registration record, an unknown
contained type, and an ARM-omitted Monitor action group. Shared DNS zones and
manual records stay outside the captured membership. IDs, owning group and full
private configuration digests survive serialization without exposing authored
settings or endpoints in inventory or response logs.

The same registered scan tests recover omitted known members by native GET,
require each removed known member's own 404, and refuse permission failures,
asynchronous reads, filtered continuations, duplicate or foreign members,
private configuration drift, changed external ownership and unsupported nested
managed controllers. A missing auto-registration record cannot be recovered
solely from its DNS zone: its product's native ownership walk must agree.
Unknown omitted kinds have no invented GET contract and prevent completion.
Native group pagination is tested independently, and RBAC assignments and
diagnostic settings are excluded from group-owned membership. These remain
composed protocol tests, not live Azure observations of Hub descendants.

`TestFleetHubScanWorkerPersistsAndRecoversMembers` reopens the SQLite database
and recreates the provider/client/worker for each registered scan. It checks
that the real known-metadata path restores an omitted member, retains the
previous Hub snapshot after a 403 failure, and updates membership only after
the member's own 404. The Fleet itself remains present and protected.

`TestFleetHubNativeLifecycleDelegation` normalizes every resource in the composed
17-member scenario and combines the production attachment, service and AKS
contributors. Every Hub member has exactly one exclusive Fleet controller;
ordinary members and shared DNS resources keep their separate relationships.
Twenty-one boundary cases cover omitted known members, missing or foreign
inventory assets, duplicate or invalid identities, private/generation changes,
altered proofs and node-group hints, new or unknown resources, read failures
and changes between native observations. Unresolved assets remain explicit;
failed observations cannot yield a usable graph.

`TestFleetHubGraphWorkerPersistsExclusiveOwnership` persists the product-normalized
assets, reopens SQLite and recreates the runtime and all three contributors for
the actual graph worker. Omitted known members retain their ownership; tampered
proofs and native 403s fail reconciliation and preserve the previous graph
revision and all 17 Hub bindings. This is a composed protocol/persistence test,
not a live-cloud Hub deletion. The Fleet root remains non-actionable and its
bindings do not claim that an unfinished root driver verifies member absence.

`TestFleetMemberClusterReferenceBoundaries`, `TestFleetArcMemberRegisteredLifecycle`
and `TestFleetArcReferenceScopeAndDrift` exercise documented AKS and Arc member
references through registered inventory, native graph contributions and the
existing member DELETE/recovery driver. Arc enrollment preserves its external
cluster and extensions; namespace prerequisites and each member's own absence
remain mandatory. Reverse-discovery tests run the same twelve unindexed,
omitted, absent, forbidden and changing-source cases for both cluster types,
including a lower-case unregistered Arc type. Foreign scopes retain unresolved
references, and changed or tampered cluster references cannot authorize cleanup.

These Arc responses are composed protocol evidence. The retained Swagger's
`ClusterResourceId` is an ARM-ID string, but its `x-ms-arm-id-details` annotation
lists only AKS (the inspected 2026-06-02 preview has the same annotation).
Microsoft's [current quickstart](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/quickstart-create-fleet-and-members)
explicitly supports `Microsoft.Kubernetes/connectedClusters`. The adapter accepts
exactly those two documented root resource types; it does not change the native
metadata or claim that a schema annotation or composed test proves live Arc
compatibility. No Arc recording or independent Arc emulator is retained.


`cli-mesh-recordings.json` retains 19 distinct responses from Microsoft's
`test_fleet_cluster_mesh.yaml` at the same immutable CLI-extension commit.
`reproduce_mesh_recordings.py` verifies the full source SHA-256 and retains the
first response for each method/path/status/selector/profile-state/member-state
combination, including list values and member error codes. It preserves native
request and response body strings and only the protocol response headers listed
above. Reproduce with `python3 reproduce_mesh_recordings.py ORIGINAL.yaml`.

`TestFleetMeshRecordedResponseCompatibility` replays these native envelopes,
including PUT 200/201, two HTTP 200 Apply responses that still say `Applying`,
DELETE 204 and a member GET returning HTML 503. The recorded connection attempt
ends in `Failed/ConnectivityTimeout`; it is not evidence of a successful
Connected network. The native disconnect changes the selector to `env=none`,
performs Apply and observes `NotConnected` before deletion. Its update also
replaces `systemData.createdAt`, so the action binds the actual returned
configuration to its saved phase. The recording has no individual member GET
after disconnection and no profile GET/404 after deletion; those residual checks
are covered by composed protocol responses.

`TestFleetMeshInventoryRecordsAppliedMembers` and the graph/boundary tests use
unfiltered preview member lists plus individual GETs to join actual
`meshProperties`. Selector matches are not applied membership. Known omitted
profiles and members are recovered by their own GETs; a surviving association
whose profile is absent remains a dependency error. Full authored configuration,
Cilium association and last-applied selector are privately bound. The graph
requires explicit Mesh cleanup before member removal and never makes a profile
the owner of its member or cluster.

`TestFleetMeshRegisteredDisconnectDeleteAndRecovery` covers conditional native
PUT, Apply and DELETE with serialized receipts, fresh drivers, synchronous and
asynchronous acknowledgments, pending Apply, delayed native disappearance,
protected members, configuration/membership drift and forged receipts. Empty
selectors mean no members according to the unchanged preview Swagger. The
composed cleanup sets `byLabel:""` instead of choosing a potentially matching
label, preserves other authored fields and rejects unknown selector semantics.
Already applying profiles wait without interruption. Empty terminal profiles
can be deleted without an unnecessary Apply. Profile or parent absence cannot
hide surviving known member attachments; 403s remain failures.

`TestFleetMeshRegisteredWorkersAndPhaseRecovery` runs native scan and graph jobs,
creates a real SQLite cleanup plan and executes its durable jobs with a fresh
provider and worker at every phase. Member cleanup requires both namespace and
Mesh prerequisites. It verifies exactly one PUT, Apply and DELETE for the mesh,
then independent namespace/member deletion, individual native absence, private
execution/log redaction and a final scan retaining the Fleet and unrelated
resources. This is composed end-to-end protocol evidence, not an independent
Mesh emulator or a live-cloud test.
