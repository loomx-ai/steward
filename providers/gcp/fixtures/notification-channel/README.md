# Cloud Monitoring notification-channel inventory

This milestone adds read-only native inventory. Cleanup and complete incoming-scope coverage
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

The independent run initially passed with five forwarded native GETs. The incoming
policy milestone extends it to twelve forwarded GETs (0.15s test body), including
native AlertPolicy inventory and channel dependency discovery.
Native CREATE/UPDATE/DELETE only seed, change and remove local channel/policy fixtures;
the runtime performs read-only discovery and Invoke. No Monitoring response or
handler is replaced. The native mock does not reproduce masking, IAM, realistic
pagination, verification delivery, mutation history on update, or forced-delete
reference guards. It cannot establish live-cloud or production acceptance.

## Native AlertPolicy consumers

The application now loads the existing Monitoring dependency contributor whenever
active channels are present, including scans with no Uptime checks. It reads each
channel before and after a complete own-project policy snapshot: unfiltered LIST
across all pages, native GET for every policy, then repeated LIST. It reuses the
same policy snapshot implementation used by Uptime scope discovery. Failure,
partial data, malformed strategies, changed policy sets/configuration, or changed
channel configuration prevents a successful graph replacement.

Both `notificationChannels` and
[`alertStrategy.notificationChannelStrategy[].notificationChannelNames`](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.alertPolicies#NotificationChannelStrategy)
contribute references. Strategy-only references are conservatively retained even
if a stale/invalid native policy no longer has a matching primary entry. Project
ID/number aliases are canonicalized; disabled policies retain dependencies. Query
expressions and delivery status do not establish that a channel is unused.

A referenced current policy produces an authoritative required-deletion edge from
channel to policy, with policy-before-channel ordering and automatic selection
turned off. Missing, closed, foreign-connection or stale policy inventory stays
unresolved and blocks cleanup. Duplicate ambiguous inventory fails the graph.
Private channel labels and policy expressions never enter relationship evidence.
Channel deletion is still unavailable, so these edges do not grant new write
capabilities or implicitly select policies.

Retained protocol tests cover paging, list/detail denial, malformed/partial data,
configuration races, strategy shape, aliases, stale policy inventory and explicit
edge semantics. Server tests cover channel-only and mixed channel/Uptime dispatch.
Real SQLite close/reopen tests prove unresolved-to-resolved transitions, preservation
of existing edges after native permission loss, and removal after a complete
successful read establishes that the own-project reference was removed.

This proves concrete own-project AlertPolicy references, not global absence of all
consumers. Billing Budget references and possible external scopes remain outside
this discovery. The native Budget contract specifically limits
`monitoringNotificationChannels` to **email** channels; non-email delivery uses
other mechanisms such as Pub/Sub. That distinction is evidence for further
cleanup work, not authority to remove the current read-only restriction. Native
non-forced deletion, write coordination, receipts/readback, masked configuration
and complete reference-scope coverage still need implementation and verification.
