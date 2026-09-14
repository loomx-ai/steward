# Cloud Monitoring AlertPolicy evidence

## Native source

The additional Monitoring v3 fragment comes from
`https://monitoring.googleapis.com/$discovery/rest?version=v3`, revision `20260903`,
full upstream SHA-256
`2ddc86068cb62dcc8184287456c6f01dc82752652a79eed1dc52d03d418e49cb`.
It retains native `monitoring.projects.alertPolicies.list/get/delete` and all 26
transitively referenced schemas. Earlier fragments and resource mappings remain
unchanged; this separate selection avoids refreshing unrelated native contracts.
Offline generation is deterministic: 201 resource rules, 788 methods, catalog
SHA-256 `0d7ba03c095a042eb22b677e7e4cc82cda08bbea73d929732d7c0182fa95c58c`.

The [policy schema](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.alertPolicies)
uses project-scoped server-assigned policy/condition identities. The enabled flag
must be populated in complete reads. Six condition unions cover thresholds,
absence, logs, MQL, PromQL and SQL. Configuration review retains unknown fields,
creation/mutation records and notification-channel names, normalizes own-project
policy/condition ID/number aliases, and excludes only the output-only `validity` diagnostic.
Documentation and condition expressions/extractors are hashed before redaction.
They remain private in persisted assets and Invoke responses. Invalid-policy
diagnostic messages/details are also redacted because they can repeat expressions;
the validity status code remains available.

Native LIST uses `name=projects/PROJECT`, optional page tokens and no filter.
`totalSize` is an estimate, not an authoritative count. Lists and detail reads must
agree; missing, denied, partial, malformed or changed reads fail the scan and
preserve history. Complete empty lists establish absence. Unsupported condition
union shapes fail validation rather than silently removing their configuration.

[DELETE](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.alertPolicies/delete)
returns an empty object synchronously and recommends serializing policy writes
within a project. There is no etag precondition, request UUID or native operation.
The same native response/receipt/readback engine serves policies and Uptime checks.
A saved receipt binds identity, review, action and idempotency key, and restart
requires GET 404. DELETE 404 alone is insufficient. Only the selected policy is
deleted; channels and monitored targets are not cascaded.

## Project mutation coordination

The existing connection-locked cleanup mechanism also reserves each Monitoring
project's policy collection. Selected policies in that project are ordered; other
projects remain independent. Running, waiting and paused executions retain the
scope. Failed/canceled executions require proof that no request was issued or a
validated synchronous receipt plus confirmed absence. Missing receipts alone do
not release scope. Continuing the original task can verify its own result. A
persisted settlement proof never changes a failed action to success and is
invalidated by subsequent action changes. The legacy Router scope field remains
an internal persistence key; policy scopes use the Monitoring namespace.

Coordination covers the same configured connection, including multiple workers
using its database. Other clients and duplicate connections to the same cloud
project are outside that reservation; cross-connection project coordination is
still unfinished. External writes can race the final read because the API has no
atomic configuration precondition.

## Verification

```sh
go test ./providers/gcp -run 'TestAlertPolicy|TestUptime|TestCatalogReproducible' -count=1
go test ./internal/app/cleanup -run 'TestMonitoringPolicy|TestCloudNat|TestRouter' -count=1
go test -race ./providers/gcp ./internal/app/cleanup ./internal/app/inventory \
  -run 'TestAlertPolicy|TestUptime|TestMonitoringPolicy|TestCloudNat|TestRouter|TestNetworkClosure' -count=1
go test ./providers/gcp ./internal/...
go vet ./providers/gcp ./internal/...
node docs/check.mjs
```

Protocol tests exercise all supported union shapes, disabled/invalid policies,
identity aliases, redaction, property queries, pagination, list/detail failures,
configuration drift, protection labels, response shape, tampered receipts,
restart and missing-response settlement. The pinned native schema validates the
threshold fixture independently of the runtime checks. SQLite workers verify
failed-scan history, authoritative absence, recovery, cleanup and reconciliation;
coordination tests cover persisted ordering, project isolation, blocked unresolved
writes, read-only settlement and stale-proof invalidation after restart.

## Independent backend

Google Config Connector mockgcp is pinned at
`673a61419de1b8e4f7d26070ce20dde2daa61da8`.
Its unchanged [AlertPolicy implementation](https://github.com/GoogleCloudPlatform/k8s-config-connector/blob/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockmonitoring/alertpolicy.go)
provides CREATE/LIST/GET/UPDATE/DELETE. Reuse the retained
[Monitoring harness](../metrics-scope/testdata/mockgcp/main.go) in a disposable
sparse checkout containing `mockgcp`, `pkg`, and
`third_party/github.com/hashicorp/terraform-provider-google-beta`. Copy the harness
to `mockgcp/steward-alert-harness/main.go`, then from `mockgcp`:

```sh
GOWORK=off go build -o /tmp/steward-alert-mockgcp ./steward-alert-harness
/tmp/steward-alert-mockgcp
```

Use the printed loopback origin from Steward's root:

```sh
STEWARD_ALERT_POLICY_MOCKGCP_URL=http://127.0.0.1:PORT \
  go test ./providers/gcp -run '^TestAlertPolicyIndependentMockGCP$' -count=1 -v
```

The actual independent run passed in 0.621s with eight forwarded Monitoring calls:
LIST/GET/review/DELETE/JSON restart/404/empty LIST. No Monitoring body or endpoint
was substituted. Only OAuth and Resource Manager use fixture identity. The pinned
server does not implement IAM, realistic pagination/filtering or concurrent-writer
constraints; protocol/SQLite tests cover those local boundaries. Its tracked files
remained unchanged; the temporary process, binary and checkout were removed.

Condition-to-Uptime/metric/group/channel dependency extraction, native incoming
reference rechecks, alert-before-check planning, channel/group inventory, broader
provider parity and full application/live acceptance remain unfinished. This
milestone does not claim cloud alert evaluation or production IAM acceptance.


Milestone checks passed in the isolated checkout: full GCP 237.872s, all internal
packages (architecture 25.650s, cleanup 6.348s); focused race GCP 120.742s,
cleanup 9.576s, inventory 6.118s; vet; bilingual docs (30 chapters, 10 screenshots).
