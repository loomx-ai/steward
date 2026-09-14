# Cloud Monitoring groups and member observations

The native `Group` resource adds explicit global inventory, LIST/GET configuration
comparison and a canonical parent reference. Each group additionally reads all
member pages twice for the same explicit past-minute interval and rereads the
group. Changed configuration/membership, failed reads, partial data, malformed or
inconsistent totals, duplicates and paging loops fail the observation. List omissions
from failed scans cannot close assets. A successful complete group LIST retains
the existing product-source absence semantics.

Only provider target fields contribute references. Compute numeric member IDs
reuse the Uptime VM alias, avoiding name-reuse matches. Supported Uptime-style
native member targets join network closure and informational dependency edges.
Parent groups and Uptime group targets join the same graph. Unmapped member types
remain non-blocking unresolved observations with counts and the time interval;
private labels and filter text never enter graph evidence or stored configuration.
No member ownership, required deletion, cascade or retention binding is produced.
Native nonrecursive group deletion is now available through the reviewed action
described below; neither inventory nor membership grants cascading deletion.

## Pinned native metadata

- [Monitoring Discovery v3](https://monitoring.googleapis.com/$discovery/rest?version=v3),
  revision `20260903`, full document SHA-256
  `9273bd1f36bbc4c94fb948450a2cf63f876e04ca8a1b331fd80158b152907019`.
- Exact `monitoring.projects.groups.list/get/delete` and
  `monitoring.projects.groups.members.list` methods, with unchanged transitive
  schemas `Group`, `ListGroupsResponse`, `ListGroupMembersResponse`,
  `MonitoredResource` and `Empty`, are selected from that document. The adjacent
  schema fixture is compiled independently of the generated catalog. Previous
  native methods and other resource rules remain unchanged; the catalog has
  204 rules and 805 methods.
- [Group](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.groups)
  describes dynamic filtering and inherited parent membership.
- [Member LIST](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.groups.members/list)
  specifies the interval and totals. Repeated queries are not an atomic live
  snapshot or authority that an omitted member no longer exists.

Reads use `monitoring.groups.list` and
`monitoring.groups.get` (also for ListGroupMembers); see [native audit permission mapping](https://docs.cloud.google.com/monitoring/audit-logging).

## Evidence and limits

Protocol tests cover group and member paging, caller scope, aliases, parent identity,
LIST/GET and late GET drift, permission denial, native 404, malformed collections,
member total mismatches, null/fractional totals, duplicate members, repeated-index
drift, unknown resource types, filter/label redaction and schema compatibility.
Network/graph tests exercise Uptime-to-group-to-VM traversal, parent references,
unmapped AWS observations and immutable numeric identity matching. Real SQLite
scan/graph workers close/reopen the database through failures, member removal,
group absence and reappearance; failed reads preserve the previous asset and edge.
The server dispatch test covers a group-only scan without duplicate contributors.

`TestMonitoringGroupIndependentMockGCP` uses unmodified Google Config Connector
commit `673a61419de1b8e4f7d26070ce20dde2daa61da8`, through the existing shared harness.
Set `STEWARD_NOTIFICATION_CHANNEL_MOCKGCP_URL` to its loopback origin. The upstream
Group implementation provides native GET/Create/Update/Delete but **does not
implement Group LIST or member LIST**. The test verifies those native unsupported
responses (HTTP 500 with explicit unimplemented-method messages) do not become empty inventory, then uses seven explicit LIST/member
protocol responses alongside seven forwarded runtime native GETs to verify
configuration updates and own-404 inconsistency detection. Setup/teardown writes
are separate from the read-only runtime. No independent paging, member evaluation,
IAM, recursive-delete or real-cloud acceptance is claimed.

Cross-scope member mappings beyond current target types, group cleanup, full
consumer discovery and application/real-cloud parity acceptance remain open.

## Consumer graph milestone

The connected Monitoring contributor now snapshots unfiltered own-project Group,
UptimeCheckConfig, AlertPolicy and Dashboard collections. LIST/GET/re-LIST reviews
bind complete observable configurations, including unknown fields. The target
Group is independently reread before and after consumer discovery. Failures never
replace persisted graphs with an apparent empty set. Child groups, Uptime resource
group targets and native policy filter references yield explicit required-deletion
edges only for matching, fresh, same-connection inventoried consumers. Informational
child-to-parent edges and target-before-source consumer edges produce the same
cleanup ordering. No member edge acquires required deletion or ownership.

Dashboard query traversal uses the 61 unchanged reachable native schemas in
`catalog/native/monitoring-dashboard.json`, copied from the already selected v1
Discovery document (revision `20260827`, SHA-256
`af1250b3492d3b37abc8c8e440cada94d8227aa11ddeef0f96f5c9f1b44f35c4`).
The schema graph distinguishes native time-series filters and ratio parts from
text/Logging filters, including all native layout paths. Unsupported query unions,
unknown fields/enums, malformed structures and dynamic group selection remain
unresolved. A configured default for a dynamic GROUP filter cannot prove that a
different group is unused. Traversal is bounded by depth and node count. Raw query
strings are not persisted in dependency evidence.

The native [group selector contract](https://docs.cloud.google.com/monitoring/api/v3/filters)
binds group IDs to the request's scoping project. Known metric-condition selectors
are retained conservatively as policy references; this is not evidence that the
current API accepts group selectors in new AlertPolicy writes. MQL/PromQL/SQL and
unknown condition forms do not prove absence. Fresh known Dashboard references now
require explicit selection of their reviewed cleanup action; unknown references block.
[Dashboard native fields](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards)
and [unfiltered Dashboard LIST](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards/list)
provide the collection and query contracts.

Protocol tests cover all four collections, paging/continuation denial, own GET
403/404, duplicate/malformed/incomplete responses, LIST/GET/re-LIST drift and final
target drift. Real SQLite close/reopen graph tests retain edges and blockers on
failed refreshes, then clear and restore them after successful native observations.
The existing independent backend's missing native Group LIST remains a limit;
these new consumer snapshots use explicit protocol fixtures, not claimed independent
server or live-cloud coverage. External-writer race protection remains unfinished;
the reviewed Group and Dashboard DELETE milestones follow.


## Reviewed nonrecursive deletion

The exact native `monitoring.projects.groups.delete` method and `Empty` response
schema are retained from the same pinned v3 Discovery document. The Group rule
advertises the reviewed deletion action. Generic Invoke cannot bypass it. The
action rejects parameters, changed identities/proofs and missing request keys;
selected prerequisites must be independently deletable same-connection Group,
UptimeCheckConfig or AlertPolicy assets with frozen reviews. Their own native GETs
must confirm absence. Unselected or newly appearing consumers still block deletion.

Preflight and Execute both perform complete consumer snapshots; Execute then
rereads the group immediately before its empty-body DELETE with explicit
`recursive=false`. This uses the native descendant guard and never requests
recursive deletion or modifies group members. Native GET 403/500, snapshot errors,
configuration drift and nonempty/malformed DELETE responses cannot establish
success. DELETE 404 is followed by own GET; the group must actually be absent.
Wait and mutation settlement bind the receipt to the full frozen request, including
prerequisites. Empty/lost receipts cannot settle the shared write reservation.

Same-connection/project Group writes use the existing persisted scope mechanism,
including failed/canceled actions and frozen prerequisite reconstruction. Tests
exercise actual SQLite plans and close/reopen before every cleanup worker action:
policy precedes its Uptime check, selected child/Uptime/policy deletion precedes
the parent group, and all four assets are marked deleted only after own readback.
Separate tests cover failed-action reservations, stale settlement proofs, changed
prerequisite snapshots, late references, changed target configuration and receipt
substitution. Group members are never deletion targets.

The independent native backend test forwards 19 runtime calls (including one
actual Group DELETE), with 19 explicit Group/member/Uptime/Dashboard list fixtures.
It first verifies that the unsupported native list endpoints report Unimplemented.
Actual Group GET/update/delete/404, empty deletion response and restarted waiter
are exercised; the backend does not enforce descendant or IAM protections. The
harness dispatches v1 Dashboard paths to Monitoring and project billing-info paths
to Cloud Billing, preserving both native services. The runtime's query/body guard
is tested independently from that missing server enforcement.

Native permissions additionally require `monitoring.groups.delete`. The API lacks
a version-conditioned DELETE, so changes from external writers can race the final
GET. Cross-connection coordination and real-cloud/full-app
acceptance remain open; the protocol fixtures are not evidence of those guarantees.


## Reviewed Dashboard deletion

The existing native Dashboard GET/LIST/DELETE contracts and all 61 reachable schemas
are unchanged. Inventory requires native LIST/GET configuration agreement and binds
etag, labels, layout, filters, annotations and unknown fields into the review digest.
Native project-number names are canonicalized to the configured project identity.
Raw layouts, text, queries and unknown content are redacted after review, including
Invoke output. Display metadata and provider-derived proofs remain available.

Dashboard actions share the reviewed Monitoring receipt/readback flow, reject caller
parameters and require request keys. Two own GETs precede empty-body DELETE, with no
query parameters. Configuration drift and native protection labels stop deletion.
Synchronous receipts survive JSON/SQLite restart; own 404 confirms completion and
empty/lost receipts cannot independently release the shared project reservation.
Known group references require explicit Dashboard selection and deletion before the
group; stale/missing/closed/foreign/unknown consumers remain blocking. Actual SQLite
inventory-backed graph/plan/worker tests verify five ordered deletions and reopen the
database before each worker action. Failure/cancellation reservation tests cover
Dashboard writes alongside the existing Monitoring configuration cases.

`TestMonitoringDashboardIndependentMockGCP` uses the same pinned unmodified upstream
backend and harness. Native Create/GET/Update/DELETE/404 are exercised, including
server-generated etag and project-number identities. Nine runtime requests are
forwarded unchanged (one DELETE), with three explicitly substituted LIST responses.
The test first verifies the real backend's `ListDashboards not implemented` error;
that failure cannot become an empty authoritative inventory. Stale LIST after the
native DELETE fails on its own GET. No native LIST, paging, IAM, external concurrency
or live-cloud guarantee is claimed. Run with the existing loopback
`STEWARD_NOTIFICATION_CHANNEL_MOCKGCP_URL` harness configuration and
`go test ./providers/gcp -run '^TestMonitoringDashboardIndependentMockGCP$' -count=1 -v`.

Arbitrary cross-project policy mappings and unresolved query languages remain separate
work. The [native Dashboard DELETE](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards/delete)
has no etag precondition, so the last GET cannot close external-writer races.


## Dashboard policy references

The same 61 native schemas now support a separate policy-reference visitor. It
uses full project/policy paths for AlertChart and project-local `alertPolicies/ID`
paths for IncidentList. An unfiltered incident list can consume any local policy;
its resource selectors do not prove that a policy will never produce an incident.
Text, LogsPanel and metric query strings do not refer to policy objects by literal
name matching. Unknown structure or malformed names remain unresolved. Existing
Group query semantics, traversal bounds and native schemas remain unchanged.

The connected policy contributor reads complete Dashboard LIST/GET/re-LIST snapshots
between own target configuration reads. Fresh known consumers require explicit
selection; missing/stale/foreign/closed consumers block cleanup. Policy-only scans
activate this contributor. Policy actions repeat the snapshot, verify selected
Dashboard own-GET absence and bind the frozen prerequisite to receipts and shared
project recovery. SQLite coverage retains graph state after permission/configuration
failures, rescans changed/absent policies before reconciliation, and verifies
Dashboard → Policy → Uptime → Group execution across database reopening.

The pinned independent backend still lacks ListDashboards. The joint native test
`TestMonitoringDashboardPolicyIndependentMockGCP` forwards 21 runtime requests
(including Dashboard and Policy DELETE), with nine explicit Dashboard LIST fixtures.
Native Dashboard create/update/GET and Policy create/LIST/GET are used; a live chart
blocks policy deletion, then Dashboard removal enables reviewed policy deletion.
Both results survive JSON restart and own-404 readback. The standalone native Policy
test also verifies the backend's Unimplemented response before each of its four
explicit Dashboard LIST fixtures. Neither test claims native list/IAM enforcement,
arbitrary cross-project consumer discovery or live-cloud acceptance.


## Direct Uptime queries and Monitoring coordination

Uptime review now gathers native Dashboard snapshots alongside AlertPolicy snapshots
through the same metric-scoping-project, project-identity and log-routing readers.
Native time-series filters and ratio denominators use the existing metric/check-ID
parser. Default-host LogsPanel filters combine with known log routes; explicit
monitored-project sources are recognized. Template expressions, unsupported languages,
unknown structures and unreviewed foreign-project/log-view sources remain unresolved.
Fresh local references require explicit Dashboard deletion before Uptime; remote
consumers never authorize cross-project writes through the monitored connection.

Unit/protocol checks cover metric/check exclusions, ratio denominators, templates,
unknown dialects, log routes and source names; foreign-project LIST/GET errors, paging,
identity mismatch and reverse-scope drift; local graph boundaries, late references,
prerequisite review and conservative synchronous settlement. The SQLite ordered-worker
scenario now has direct Dashboard→Uptime and Dashboard→Policy→Uptime dependencies.
All five Monitoring kinds share one project write reservation. This fixes the previous
collection-specific reservation behavior; older saved collection suffixes are normalized
when read, and failed legacy reservations block new work of other Monitoring kinds.

`TestMonitoringDashboardUptimeIndependentMockGCP` forwards 26 native requests with
one Dashboard and one Uptime DELETE, verifying reference blocking, ordered removal,
JSON restart and own-404 settlement. Nine Dashboard LIST responses, six reverse-scope
responses and twelve Logging LIST responses are explicit fixtures. Before substitution,
the unchanged backend is probed: Dashboard/Uptime LIST return Unimplemented, while
reverse-scope and `in_scope("DEFAULT")` sink requests reject their native query bindings
with HTTP 400. These are backend limitations, not evidence that the real APIs reject
the cataloged requests. The standalone native Uptime test also proves each unsupported
read prevents any DELETE before fixtures are enabled. No native scope discovery, sink
filtering, IAM enforcement, external-write lock or live-cloud guarantee is claimed.
