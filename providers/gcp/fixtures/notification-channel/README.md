# Cloud Monitoring notification-channel inventory

Native inventory, own-project AlertPolicy dependencies and reviewed non-email
channel cleanup are supported. Email channels remain protected until Billing
Budget consumer discovery is complete. Overall provider acceptance is unfinished.

## Native contracts

The retained fragment comes from
[Monitoring v3 Discovery](https://monitoring.googleapis.com/$discovery/rest?version=v3),
revision `20260903`, full upstream SHA-256
`9273bd1f36bbc4c94fb948450a2cf63f876e04ca8a1b331fd80158b152907019`.
The native `monitoring.projects.notificationChannels.list/get/delete` methods and
their four transitive schemas are retained unchanged. DELETE is an appended source
fragment; earlier fragments remain unchanged. The channel mapping now exposes
reviewed cleanup. The offline catalog has 202 rules and 795 methods,
SHA-256 `5170ca227650a228c5b55abb88a1f1f275856c8f8d8440ef6762f761f88f63dd`.

[LIST](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.notificationChannels/list)
uses the configured project, no filter, page tokens and pageSize. Each observation
requires a matching native GET. Duplicate rows, malformed lists/tokens, partial
responses, denied/missing detail and visible configuration drift fail the scan.
A complete empty list establishes absence; a failed scan preserves prior history.
Inventory needs `monitoring.notificationChannels.list/get` in addition to the
connection's Resource Manager identity access. Dependency discovery also needs
`monitoring.alertPolicies.list/get`; reviewed channel cleanup additionally needs
`monitoring.notificationChannels.delete`, and explicitly selected policy cleanup
needs `monitoring.alertPolicies.delete`.

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

## Reviewed deletion and the email boundary

[DELETE](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.notificationChannels/delete)
with `force=true` also changes alert policies. The driver always sends `force=false`, requires explicit reviewed dependencies,
coordinates project writes and verifies restart/readback and visible configuration.
[Billing budgets](https://docs.cloud.google.com/billing/docs/reference/budget/rest/v1/billingAccounts.budgets#NotificationsRule)
also reference channels through `monitoringNotificationChannels`; a scan of local
alert policies alone does not establish that a channel has no users. Budget and
other service references, masked configuration semantics, complete cloud-scope
coverage and external-writer behavior require further work. Create, patch, verification-code and verification methods remain unavailable.
Generic Invoke rejects the channel DELETE method; a reviewed action is required.

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
  go test ./providers/gcp -run '^TestNotificationChannel(Delete)?IndependentMockGCP$' -count=1 -v
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
These edges feed reviewed non-email cleanup without implicitly selecting policies.
Email inventory remains protected and non-actionable.

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
other mechanisms such as Pub/Sub. That distinction permits the non-email lifecycle below. Email cleanup remains
blocked. Masked configuration, external writers, complete consumer scope and
live-cloud acceptance remain bounded by the native API and need further work.

## Non-email channel lifecycle

A selected non-email channel uses native synchronous DELETE with `force=false`,
no body, and no user-supplied action parameters. Direct Invoke is rejected, so
`force=true` cannot bypass the reviewed lifecycle. Native protection labels and
visible configuration are rechecked. A live email channel is rejected even if
its persisted `type` was tampered with. Email rows are non-actionable, with an
explicit budget-scope protection reason.

Preflight and execution reread all own-project policies. Every reviewed policy
prerequisite must return GET 404, independently of the LIST result. The channel is
read again immediately before DELETE. Native non-forced deletion retains the
provider's guard against policies outside the observed snapshot or added by a
concurrent client. This guard does not establish Billing Budget absence.

An empty synchronous result records a receipt bound to the frozen asset, visible
configuration, action, idempotency key and complete prerequisite reviews. Restart
must confirm channel GET 404. DELETE 404 alone is not success while GET still
finds the channel. Native errors, unexpected operation bodies, changed receipts
and lost responses cannot authorize a new write or release project scope.

The existing persistent write-reservation mechanism serializes channel writes by
connection/project/channel collection. Separate projects remain independent;
AlertPolicy collection reservations retain their previous keys. Failed or canceled
attempts retain the reservation until no-write or validated receipt/readback proof
settles them. Recovery reconstructs frozen policy prerequisites in worker order;
changing those reviews invalidates a saved settlement. Duplicate connections to
the same physical project and external clients remain outside that reservation.

Tests cover normal/absent/denied/refused deletion, `force=false` wire behavior,
configuration and protection changes, email and direct-Invoke bypass attempts,
live/late/unknown policies, JSON receipt corruption, lost responses and settlement.
The real SQLite graph/plan/worker workflow closes and reopens the database between
steps, explicitly deletes the policy first, then deletes the channel once and
verifies both tombstones. Shared cleanup tests cover reservation persistence,
project isolation, frozen prerequisite recovery and settlement invalidation.

The additional independent mock test is `TestNotificationChannelDeleteIndependentMockGCP`.
It seeds a native Pub/Sub channel and policy, proves the live policy blocks channel
execution, deletes the selected policy, then performs native non-forced channel
DELETE, JSON restart, settlement and GET 404. The pinned mock does not implement
real force/reference guards, Pub/Sub IAM, masking or concurrent cloud writers;
those limits must not be confused with live-cloud acceptance.

The independent cleanup run passed in 0.17s with 30 forwarded native calls; the
email read-only/dependency case passed with 12 GETs. Native DELETE has no etag or
configuration precondition: external changes (including channel type changes)
can race the last read. `force=false` guards native policy references, not budget
references or arbitrary configuration races. The local write reservation cannot
make other cloud clients participate in its lock.
