# Logging routing and incoming Uptime alert policies

Steward follows the connected project's native Resource Manager ancestry and
reads each project's, folder's and organization's own sinks with
`filter=in_scope("DEFAULT")`. Every page is read, each row is compared with GET,
and the complete LIST is repeated. The ancestry and sink snapshot are repeated
after reading the incoming alert policies. Missing permissions, partial responses,
invalid identities, null collections/tokens, pagination loops, duplicate sinks,
changed configuration and project/folder moves cannot establish absence.

This uses the existing ancestry reader. According to the native
[sinks.list contract](https://docs.cloud.google.com/logging/docs/reference/v2/rest/v2/projects.sinks/list),
`in_scope("ALL")` includes only intercepting ancestor sinks; it omits ordinary
non-intercepting aggregates. Reading each ancestor's own sinks covers both forms.
Ancestor sinks without `includeChildren` do not route this project's logs. Sink
names are bound to their parent, including the output `resourceName` when present.
Invalid intercept configurations fail closed. Private filters, exclusions and
writer identities are never added to graph evidence or stored in action receipts.

The [routing contract](https://docs.cloud.google.com/logging/docs/routing/overview)
limits project-to-project routing to one hop. Direct log-bucket, Cloud Storage,
BigQuery and Pub/Sub destinations do not rerun the destination project's router.
System sinks can target [native folder/organization/billing-account buckets](https://docs.cloud.google.com/logging/docs/reference/v2/rest/v2/organizations.locations.buckets/get);
these URI forms are recognized without inventing project routing.
The native sink inclusion/exclusion filters are evaluated conservatively for the
selected check IDs. Unknown values remain unknown through exclusion NOT. A route
that conclusively excludes all selected checks does not require reading its target
project. Disabled sinks and potentially intercepted child routes remain possible
configuration dependencies; delivery state, destination IAM, bucket availability
and current log contents are not treated as proof that a policy has no dependency.

Project destinations are resolved through Resource Manager, including project-ID
and project-number aliases. Multiple aliases merge into one policy read. The
unfiltered AlertPolicy LIST/GET/repeated-LIST checks also cover these destinations.
The [log-based alert scope](https://docs.cloud.google.com/logging/docs/alerting/monitoring-logs#available-log-entries)
is separate from MetricsScope: a project discovered only through a log route does
not make its ordinary metric or MQL/PromQL conditions dependencies of this source
project. Conversely, a foreign MetricsScope alone does not establish a log route.
The source project's log policies are always considered. SQL remains unresolved
where the discovered scope could expose the check's logs or metrics.

Foreign matching policies block cleanup and require their own configured project;
no policy is automatically selected or deleted through the source connection.
Both graph rebuild and Uptime preflight/final execute use these native reads.
Every reviewed local prerequisite still requires GET 404 and retains its existing
frozen receipt binding. No transaction or etag spans sink reads, policy reads and
Uptime DELETE; external writers can still race the final read. This is dependency
coverage, not a native log-delivery simulator or an atomic cross-service lock.

Additional read permissions are required for Resource Manager project/ancestor
GET, Logging sink LIST/GET on the source and each ancestor, and Monitoring policy
LIST/GET on relevant destination projects. A denied read is not an empty result.
No sink write permission is used by this runtime path.

## Native metadata and retained checks

`native-schemas.json` retains four unmodified methods (`folders.sinks.list/get`
and `organizations.sinks.list/get`) and their schema closure from the official
[Logging v2 Discovery document](https://logging.googleapis.com/$discovery/rest?version=v2),
revision `20260818`, full-source SHA-256
`da8b8dafbac40c8b0c4bf0568a7b6ccd9654045ddac92a94040164e0accf9191`.
The previous Discovery fragments and resource rules are unchanged. The generated
catalog has 201 rules and 792 methods; the four added operations are read-only.
Native sink samples are checked with the independent JSON Schema validator.

Tests cover direct/folder/organization routing, aliases, disabled sinks, strict
native reads, route appearance between preflight and execute, exclusions under
NOT, unrelated metric policies, destination validation and safe graph evidence.
SQLite is actually closed and reopened: foreign-policy blockers survive an
ancestor read failure and disappear only after a successful complete graph rebuild
observes the route gone. Existing local policy-before-check cleanup and receipt
restart tests also run with the new mandatory routing reads.

```sh
go test ./providers/gcp ./internal/... -count=1
go test ./providers/gcp -run 'TestLoggingRouting|TestMonitoringDependency|TestServiceResourceWireLifecycles' -count=1
go test -race ./providers/gcp -run 'TestLoggingRouting|TestMonitoringDependencySQLite' -count=1
go test ./providers/gcp -run '^$' -fuzz '^FuzzLoggingDestination$' -fuzztime=20s -parallel=2
```

## Independent Google implementation

The retained [Monitoring/Logging harness](../metrics-scope/testdata/mockgcp/main.go)
uses unchanged Google Config Connector handlers at revision
`673a61419de1b8e4f7d26070ce20dde2daa61da8`. It now registers `mocklogging` alongside
`mockmonitoring` and supplies three synthetic project identities. Google's
[LogSink implementation](https://github.com/GoogleCloudPlatform/k8s-config-connector/blob/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mocklogging/logsink.go)
implements CREATE/GET/UPDATE/DELETE but not LIST. The test first forwards that real
unsupported LIST and verifies failure. Only then does it enable explicitly modeled
LIST pages populated with the actual CREATE responses. All sink GET responses
and foreign AlertPolicy LIST/GET responses remain native Google responses.
Resource Manager ancestry and reverse MetricsScope reads are explicit fixtures.

The independent routing run passed: 25 native Monitoring/Logging requests,
24 explicit sink LIST fixture calls and 4 reverse-scope fixtures, with zero runtime
DELETEs. Five native CREATE seed requests and their cleanup DELETEs are outside
those counters. Native project/folder/organization sink reads lead to a foreign log
policy blocking the actual Uptime execute path. The existing Uptime integration
also passed: 28 native Monitoring/Logging requests, 7 reverse-scope fixtures and
12 explicit empty sink LIST fixtures. It independently verifies that unsupported
sink discovery blocks writes before testing policy DELETE/404 and Uptime cleanup.

This does not claim independent sink LIST/filter/routing evaluation, native ancestor
IAM, live-cloud acceptance or a complete Monitoring/Logging emulator. MQL/PromQL/SQL
analysis, groups/channels, duplicate-connection coordination, other provider gaps,
and all eight overall acceptance criteria remain open.
