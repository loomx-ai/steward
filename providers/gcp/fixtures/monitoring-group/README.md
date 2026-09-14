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
Group delete is deliberately not advertised by this inventory milestone; native
nonrecursive deletion remains to be implemented. Consumer graph coverage is
described below.

## Pinned native metadata

- [Monitoring Discovery v3](https://monitoring.googleapis.com/$discovery/rest?version=v3),
  revision `20260903`, full document SHA-256
  `9273bd1f36bbc4c94fb948450a2cf63f876e04ca8a1b331fd80158b152907019`.
- Exact `monitoring.projects.groups.list/get` and
  `monitoring.projects.groups.members.list` methods, with unchanged transitive
  schemas `Group`, `ListGroupsResponse`, `ListGroupMembersResponse` and
  `MonitoredResource`, are selected from that document. The adjacent schema fixture
  is compiled independently of the generated catalog. Existing native fragments
  and resource rules remain unchanged; catalog now has 204 rules and 804 methods.
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
unknown condition forms do not prove absence. Dashboard references remain blockers
because their generic delete driver does not yet bind a full configuration review.
[Dashboard native fields](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards)
and [unfiltered Dashboard LIST](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards/list)
provide the collection and query contracts.

Protocol tests cover all four collections, paging/continuation denial, own GET
403/404, duplicate/malformed/incomplete responses, LIST/GET/re-LIST drift and final
target drift. Real SQLite close/reopen graph tests retain edges and blockers on
failed refreshes, then clear and restore them after successful native observations.
The existing independent backend's missing native Group LIST remains a limit;
these new consumer snapshots use explicit protocol fixtures, not claimed independent
server or live-cloud coverage. Group DELETE, dashboard reviewed deletion and
external-writer race protection remain unfinished.
