# Cloud Monitoring metrics-scope verification

This family exposes a project's `MetricsScope` and its `MonitoredProject` links.
The native scope API is project-owned and has no DELETE method. The independent
cleanup action removes a selected link; it does not delete either Google Cloud
project or monitoring data/configuration. Steward retains the default self membership as read-only. The
[scope data model](https://docs.cloud.google.com/monitoring/settings) describes
this project-owned default; the emulator does not enforce self-link protection.

## Native contracts and retained sources

- [MetricsScope](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/locations.global.metricsScopes)
  contains the full member set, creation/update timestamps and a project-number
  name. [MonitoredProject](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/locations.global.metricsScopes.projects)
  uses both scoping and monitored project numbers; its creation time identifies
  a link incarnation. A tombstoned-project link remains an inventory record.
- [GET](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/locations.global.metricsScopes/get)
  requires `resourcemanager.projects.get` on the scoping project. There is no
  independent member GET/LIST or pagination parameter. Missing self membership,
  malformed members, duplicates and unexpected continuations fail the scan.
- [Reverse lookup](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/locations.global.metricsScopes/listMetricsScopesByMonitoredProject)
  lists scopes monitoring the connection project, with its own scope first. It
  requires `resourcemanager.projects.get` on that project. Incoming scopes appear
  as informational references; they do not authorize reading or changing another
  project's scope under this connection.
- [Unlink](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/locations.global.metricsScopes.projects/delete)
  is `DELETE /v1/locations/global/metricsScopes/{scope}/projects/{project}` with
  an empty body and no etag/request-ID parameter. It requires
  `monitoring.metricsScopes.link` on both projects and returns a top-level
  `operations/{id}`. [Operation GET](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/operations/get)
  uses the same Monitoring v1 origin. The
  [RPC metadata schema](https://docs.cloud.google.com/monitoring/api/ref_v3/rpc/google.monitoring.metricsscope.v1)
  defines CREATED/RUNNING/DONE/CANCELLED state, not a resource target or verb.
- [Configuration behavior](https://docs.cloud.google.com/monitoring/settings/multiple-projects)
  keeps charts, dashboards, alerts and groups when a link is removed, while the
  metrics they can query change. For
  [app-enabled folders](https://docs.cloud.google.com/monitoring/settings/metrics-scope-app-enabled-folders),
  Google does not automatically undo an explicitly removed link.

`scope.json` and `reverse.json` are synthetic native responses, with three links
(self, another project and a tombstoned project) and an incoming external scope.
`native-schemas.json` retains the unmodified MetricsScope, MonitoredProject and
OperationMetadata schemas from the official
[Monitoring v1 Discovery response](https://monitoring.googleapis.com/$discovery/rest?version=v1),
revision `20260827`, SHA-256
`af1250b3492d3b37abc8c8e440cada94d8227aa11ddeef0f96f5c9f1b44f35c4`.
OperationMetadata is an Any payload and is not reached by transitive `$ref`
selection, so its native schema is also retained here. The catalog keeps the
four selected new methods and all their transitive schemas. Previously selected
Dashboard method/schema objects remain unchanged.

## Retained protocol and application tests

Run from the repository root:

```sh
go test ./providers/gcp -run TestMetricsScope -count=1
go test -race ./providers/gcp -run TestMetricsScope -count=1
```

The tests check literal native HTTP paths independently of the catalog, project
ID/number aliases, complete unpaged reads, reverse references, self protection,
actual dependency planning, unlinking a tombstoned link, serialized recovery,
request IDs and final absence. More than 75 fault cases cover incomplete scopes,
permissions, unexpected continuation, changed membership/incarnations, second-read
races, invalid plans, foreign operation URLs, malformed/failed/cancelled LROs,
expired operation records and absence while an operation is still pending.
A SQLite scan worker proves a failed scope read preserves all three prior links.
Native fixtures also validate against the retained official schemas.

The preflight and waiter read the entire scope twice and compare the selected
link and the scope's creation time. Scope 404 is a dependency failure. DELETE 404
is successful only after complete reads verify the link is absent. Changes to
unselected siblings do not authorize their deletion. Removing the link does not
prove that its metrics are no longer cached by every downstream query system.
No atomic API precondition spans the reads and DELETE; concurrent recreation
between the final read and mutation remains a native API limitation.

## Independent Google mockgcp run

The independent implementation is Google's
[Config Connector mockgcp](https://github.com/GoogleCloudPlatform/k8s-config-connector/tree/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockmonitoring),
pinned at `673a61419de1b8e4f7d26070ce20dde2daa61da8`.
Its [scopes.go](https://github.com/GoogleCloudPlatform/k8s-config-connector/blob/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockmonitoring/scopes.go)
implements native GET/create/delete and stores self membership and creation
identities. The retained `testdata/mockgcp/main.go` only supplies two synthetic
project identities and serves those unmodified Google handlers plus their native
operation reader. It contains no substitute Monitoring response logic.

The opt-in test creates a link through that implementation, then runs Steward's
complete child scan, DELETE, LRO GET, serialized recovery and final full readback
over real local HTTP. The verified run passed with 10 Monitoring calls and
preserved the scope's self link. OAuth and Resource Manager identity are test
fixtures; mockgcp does not validate production IAM. Its scope service does not
implement reverse lookup, and its unlink completes immediately, so those cases
remain covered by the separate protocol tests. This is independent emulator
coverage for this native path, not real-cloud acceptance or complete Monitoring
emulation.

To reproduce, use a temporary checkout of the pinned revision with `mockgcp`,
`pkg` and `third_party/github.com/hashicorp/terraform-provider-google-beta`
available. From the Steward repository root, copy
`providers/gcp/fixtures/metrics-scope/testdata/mockgcp/main.go` into
`mockgcp/steward-metrics-harness/main.go` in that checkout, then build from its
`mockgcp` directory:

```sh
GOWORK=off go build -o /tmp/steward-metrics-mockgcp ./steward-metrics-harness
/tmp/steward-metrics-mockgcp
```

`GOWORK=off` avoids loading unrelated Config Connector workspace tools. The
pinned upstream module selects Go 1.26.4; no Steward module dependency changes
are needed. The server prints a `http://127.0.0.1:<port>` origin. In a second
terminal, from Steward's root, use that exact origin:

```sh
STEWARD_METRICS_SCOPE_MOCKGCP_URL=http://127.0.0.1:PORT \
  go test ./providers/gcp -run '^TestMetricsScopeIndependentMockGCP$' -count=1 -v
```

Stop the temporary server after the test and remove its checkout and binary.
All resources are in memory; no cloud credentials or cloud resources are used.

The [Logging routing milestone](../logging-routing/README.md) extends this same
harness with unmodified `mocklogging` handlers and a third synthetic project,
`foreign-project` (987654). The earlier metrics-scope run remains historical
scope evidence. The added Logging integration explicitly identifies unsupported
sink LIST and its modeled pages; no native Logging discovery claim is made.
