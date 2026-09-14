# Cloud Monitoring notification-channel inventory

This milestone adds read-only native inventory. Cleanup and incoming dependencies
remain unfinished; neither the resource rule nor the spec exposes a delete action.

## Native contracts

The retained fragment comes from
[Monitoring v3 Discovery](https://monitoring.googleapis.com/$discovery/rest?version=v3),
revision `20260903`, full upstream SHA-256
`9273bd1f36bbc4c94fb948450a2cf63f876e04ca8a1b331fd80158b152907019`.
Both `monitoring.projects.notificationChannels.list/get` methods and their three
transitive schemas are unchanged native values. Older documents and mappings are
unchanged. The offline generated catalog has 202 resource rules and 794 methods,
SHA-256 `2a3ab5fd51239aeb90316873ff9f9852e4adf2551efd9d5cf0d686dbdfda8303`.

[LIST](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.notificationChannels/list)
uses the configured project, no filter, page tokens and pageSize. Each observation
requires a matching native GET. Duplicate rows, malformed lists/tokens, partial
responses, denied/missing detail and visible configuration drift fail the scan.
A complete empty list establishes absence; a failed scan preserves prior history.
Only `monitoring.notificationChannels.list/get` are needed in addition to the
connection's existing Resource Manager identity access.

[The native resource](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.notificationChannels)
contains descriptor-specific `labels`, which are configuration rather than user
tags. They can contain contact information and credentials under ordinary keys.
All channel labels and descriptions are redacted from raw and normalized payloads
and Invoke responses. Creation/mutation actors are also redacted. `userLabels`
supply inventory tags and remain separately queryable. Enabled and verification
status remain inspectable, including disabled and unverified channels. Omitted
optional flags are not synthesized; future descriptor type names are allowed.

The visible-configuration fingerprint binds native fields, unknown fields and
mutation history before redaction, normalizing only the project ID/number alias
in the channel name. Google omits or truncates sensitive values, so this fingerprint
cannot prove that hidden values are unchanged. It does not authorize deletion.
LIST and GET representations must agree; different server masking representations
will fail conservatively until verified normalization rules are available.

## Why cleanup is still closed

[DELETE](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.notificationChannels/delete)
with `force=true` also changes alert policies. Future cleanup must retain the
non-forced native reference guard, explicit reviewed dependencies, project write
coordination, restart/readback and configuration-change checks.
[Billing budgets](https://docs.cloud.google.com/billing/docs/reference/budget/rest/v1/billingAccounts.budgets#NotificationsRule)
also reference channels through `monitoringNotificationChannels`; a scan of local
alert policies alone does not establish that a channel has no users. Budget and
other service references, masked configuration semantics, complete cloud-scope
coverage and external-writer behavior require further work. No create, patch,
delete, verification-code or verification method was added to the runtime catalog.

## Validation

```sh
go test ./providers/gcp -run 'TestNotificationChannel|TestCatalogReproducible|TestAlertPolicy|TestUptime' -count=1
go test -race ./providers/gcp ./internal/app/inventory -run 'TestNotificationChannel|TestNetworkClosure' -count=1
go test ./providers/gcp ./internal/...
go vet ./providers/gcp ./internal/...
node docs/check.mjs
```

Protocol tests cover native shape, aliases, optional fields, unknown descriptor
types, visible/private configuration changes, paging, denied/partial/malformed
reads, duplicate rows, property queries, redaction, and unavailable cleanup.
The independent native JSON schema validates the fixture. Real SQLite scan tests
verify failed-scan history, authoritative absence, recovery and read-only assets.
The shared Uptime/AlertPolicy tests continue to exercise their cleanup lifecycles.

Google Config Connector mockgcp is pinned at
`673a61419de1b8e4f7d26070ce20dde2daa61da8`. Its unchanged
[NotificationChannel service](https://github.com/GoogleCloudPlatform/k8s-config-connector/blob/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockmonitoring/notificationchannel.go)
implements native LIST/GET/CREATE/UPDATE/DELETE. Reuse the retained
[Monitoring harness](../metrics-scope/testdata/mockgcp/main.go) in a disposable
sparse checkout with `mockgcp`, `pkg`,
`third_party/github.com/hashicorp/terraform-provider-google-beta` and
`config/crds/resources`. Copy it to `mockgcp/steward-monitoring-harness/main.go`
and build inside `mockgcp` with `GOWORK=off go build ./steward-monitoring-harness`.
Run the harness and pass its printed loopback origin:

```sh
STEWARD_NOTIFICATION_CHANNEL_MOCKGCP_URL=http://127.0.0.1:PORT \
  go test ./providers/gcp -run '^TestNotificationChannelIndependentMockGCP$' -count=1 -v
```

The independent run passed with five forwarded native GETs (0.15s test body).
Native CREATE/UPDATE/DELETE only seed, change and remove the local mock fixture;
the runtime performs read-only discovery and Invoke. No Monitoring response or
handler is replaced. The native mock does not reproduce masking, IAM, realistic
pagination, verification delivery, mutation history on update, or forced-delete
reference guards. It cannot establish live-cloud or production acceptance.
