# Billing Budget notification references

The dependency contributor reads all billing accounts visible to the selected
credential, including closed accounts, then all budgets under each account. It
uses unfiltered native LIST/GET, follows every page, binds complete returned
configurations and repeats account/budget indexes to detect observed changes.
Budget spending-project filters do not constrain notification-channel ownership,
so neither the selected project's billing account nor the budget `scope` filter
is used to limit this search. Own-project numeric channel aliases are canonicalized.

A concrete email-channel reference creates a blocking unresolved Budget reference
with its native identity. Account names, budget amounts, spending filters and
notification configuration are not copied into graph evidence. Native channel
GET determines whether Billing discovery is needed; a modified local `type`
cannot bypass it. Non-email channel refreshes require no Billing permissions.

## Native contracts and provenance

Selected native operations are `cloudbilling.billingAccounts.list/get` and
`billingbudgets.billingAccounts.budgets.list/get`. The inventory extension below adds a Budget resource rule; the subsequent reviewed-delete extension is described below. No IAM change or
notification-send operation is added.

- [Cloud Billing Discovery v1](https://cloudbilling.googleapis.com/$discovery/rest?version=v1),
  revision `20260904`, downloaded document SHA-256
  `14862c5e884d2f899280acdba70c187055a0d30052c10091b0428a2e92e65b25`.
- [Billing Budgets Discovery v1](https://billingbudgets.googleapis.com/$discovery/rest?version=v1),
  revision `20260906`, downloaded document SHA-256
  `43f6da9d356ad5d16fdcc1b1f10382f2c0b0a5e056a24719fcf25e1748ea03ce`.
- [Account LIST contract](https://docs.cloud.google.com/billing/docs/reference/rest/v1/billingAccounts/list)
  explicitly limits results to accounts the caller can view.

The adjacent schema fixtures retain native schema objects and transitive `$ref`
closure unchanged. Tests compile these independently from the generated catalog.
The read foundation added four methods; the inventory extension brings the catalog
to 203 resource rules; reviewed DELETE brings the method count to 800.

## Verification

Protocol tests exercise multiple accounts and multiple budget pages, closed
accounts, foreign spending projects, numeric channel aliases, empty visibility,
denials/404, null/partial/malformed responses, duplicate identities, token loops,
LIST/GET and repeated-index drift, wrong parents and malformed channel references.
Graph tests cover positive and unrelated references, private-field exclusion,
local type tampering and zero Billing calls for non-email delivery.

A real SQLite database is closed and reopened between graph refreshes. Budget
references survive a failed native read; a later successful read can remove the
concrete reference while retaining the distinct scope-coverage barrier. Policy
references and their existing graph transitions are checked independently.

`TestBillingBudgetIndependentMockGCP` uses the unmodified Google Config Connector
commit `673a61419de1b8e4f7d26070ce20dde2daa61da8`. The shared harness under
`../metrics-scope/testdata/mockgcp/main.go` registers native `mockbilling` and
`mockbillingbudgets` alongside Monitoring/Logging. Set
`STEWARD_NOTIFICATION_CHANNEL_MOCKGCP_URL` to its loopback origin. The test seeds
an account, an email channel, a policy and a budget through native HTTP, forwards
runtime GETs unchanged, then PATCHes the native budget and verifies reference
removal. It passed with 31 forwarded GETs and no Billing response fixtures.

The previous email Monitoring test deliberately uses two explicit empty-account
protocol responses; its 12 forwarded Monitoring GETs are not Billing evidence.
The non-email lifecycle test still passes with 30 forwarded native calls and no
Billing requests. Local setup/teardown writes are outside the runtime read path.

## Remaining boundary

Account LIST is IAM-visibility-filtered. Empty or stable results cannot establish
that no inaccessible account contains a budget referencing this channel. Every
email channel therefore retains `notification_channel_budget_scope_unverified`;
email cleanup remains unavailable. A silent visibility change between successful
refreshes may remove a previously observed concrete reference, but never removes
this coverage barrier. Explicit read failures preserve the previous graph.

The independent mock does not implement real IAM visibility, pagination or
concurrent external writers. Its v1 Budget facade adapts the upstream beta proto
internally; that upstream behavior is not modified by Steward. Its empty account
LIST fabricates a dummy account, so the independent test seeds a real mock account.
The native Budget API also omits some fields available only in Cloud Console.
Repeated reads detect observed drift but do not provide an atomic cross-service
snapshot. Full account-scope evidence, Budget lifecycle support and real-cloud
acceptance remain open; this milestone does not authorize email deletion.


## Budget asset inventory and saved-identity reconciliation

Budget now has an explicit bilingual `billing.budget` resource specification and
its own `billing-budgets-visible` source. It is indexed globally under the selected
connection for navigation, but retains its real billing-account identity and does
not claim that the selected project owns the budget. Scans expose name, account,
amount, thresholds and ownership scope. Delivery rules and spending filters are
redacted from escaping API payloads and omitted from stored asset configuration;
the complete native configuration contributes to the review fingerprint.

The source is not authoritative over list omissions. It uses the shared
`ReconcileKnownIDs` contract and rereads saved budgets outside the current visible
index. Each final budget read is bracketed by successful, matching account GETs.
Only a saved budget omitted from the index whose own GET returns 404 can appear
in `AbsentNativeIDs`. A listed budget returning 404 is an inconsistent snapshot
and fails the scan. Missing/denied/changed accounts and failed budget reads return
no complete batch or partial absence claims. Successful known-budget reads retain
native request IDs, including the own-404 reconciliation response when available.

A freshly scanned Budget whose configuration matches native discovery resolves
the channel's concrete reference to an explicit Budget-before-channel dependency.
It does not automatically select the budget. Stale, closed, foreign-connection or
missing budget assets remain unresolved; ambiguous duplicates fail the graph.
The separate email-channel scope barrier remains in either case.

Retained protocol tests exercise source/scope validation, invalid and duplicate
known identities, unrelated metadata, empty indexes, hidden-but-readable budgets,
parent denial/404, child denial/404, final-read drift, redaction, query projection,
read-only action boundaries and concrete graph resolution. A real SQLite scan
worker closes/reopens the database between successful, hidden, denied, missing,
deleted and reappeared states. It preserves prior observations on failure, closes
only the exact saved budget after own 404, preserves searchable account metadata
and reuses identity when the budget reappears.

`TestBillingBudgetInventoryIndependentMockGCP` reuses the unmodified pinned Google
backend. Native account/Budget creation, Budget LIST/GET, native PATCH and native
DELETE/GET-404 verify inventory update and saved-identity reconciliation. Running
all four channel/Budget independent cases passed; the inventory case forwarded
46 GETs with two mock billing accounts visible. No runtime Billing writes or
fabricated Billing responses are used. The mock's IAM/paging/concurrency limits
and beta-proto v1 adaptation remain unchanged.

Budget deletion was subsequently added with the review and restart safeguards
described below.
The account-scoped path is complemented by the project-permission path below. Neither indexed budgets nor successful
reconciliation establish complete external consumer coverage for email deletion.


## Reviewed Budget deletion

The Budget rule now exposes native `billingbudgets.billingAccounts.budgets.delete`
from the same pinned v1 Discovery document (including `GoogleProtobufEmpty`).
Previous method/schema fragments are unchanged. The native
[DELETE contract](https://docs.cloud.google.com/billing/docs/reference/budget/rest/v1/billingAccounts.budgets/delete)
requires an empty body and returns an empty JSON object; it accepts no etag/CAS
condition. Generic Invoke is blocked for this operation.

A new inventory scan stores separate fingerprints of the complete observable
budget and its account. Deletion requires both, the exact frozen asset and
connection/partition, a nonempty idempotency key, matching account selector, and
no action parameters, lifecycle impacts or prerequisites. Old assets lacking the
account fingerprint need rescan. Preflight and execution repeat native account
and budget GETs; account configuration changes, permission loss and budget drift
block the write. Cancellation before execution or during the final read prevents
DELETE. No channel, spending project or billing account is deleted by this action.

An empty synchronous response produces a receipt bound to the frozen budget and
account reviews, asset identity, action and idempotency key. Restart verifies the
receipt and requires the budget's own GET 404 between successful matching account
reads. DELETE 404 alone is not sufficient while GET still sees the budget.
Unexpected operation bodies, modified receipts and lost responses cannot settle
the mutation. Continue the original task to verify uncertain deletion; a lost
receipt cannot independently release a failed task's write reservation.

The existing persistent mutation reservation serializes Budget writes within one
connection and billing account. Other accounts stay independent. Shared tests
exercise ordering, invalid scope identities, blocked running/waiting/paused/failed/
canceled attempts and terminal settlement. This does not coordinate duplicate
connections to the same account or external cloud clients.

Protocol tests cover observed budget/account/private/unknown-field drift,
permission and identity failures, missing reviews, unsupported parameters/impacts,
cancellation, live/absent targets, rejected and lost DELETE responses, own-404
confirmation, JSON receipt restoration and settlement. A real SQLite cleanup
plan/worker closes and reopens its database between action phases, emits DELETE
once, waits while the target remains live and persists a tombstone after own 404.

`TestBillingBudgetDeleteIndependentMockGCP` adds native reviewed DELETE, JSON
restart, own-404 settlement, channel preservation and post-delete inventory
reconciliation to the pinned independent harness. In the five-case run it passed
with 71 forwarded calls and exactly one runtime Budget DELETE. The preceding
channel, dependency and inventory native cases also passed. Native fixture writes
remain outside the runtime path; no Billing response fixtures are used for the
Budget cases.

The API has no conditional DELETE and omits some Console-only settings. External
changes can race the last read, and masked/unexposed fields cannot be compared.
Budget lifecycle support does not establish complete email-channel consumer scope:
email deletion, project-only discovery and real-cloud acceptance remain open.


## Project-permission inventory and cleanup

Inventory additionally reads the selected project's native `getBillingInfo` and
pages Budget LIST with `scope=projects/{project_id}`. Every returned budget must
have exactly the selected project (ID or number) in `budgetFilter.projects`.
LIST/GET/re-LIST and repeated project information bind the snapshot. Initial
account-index 403 may use this successfully read project scope; malformed or
failed visible-account reads cannot silently fall back. Account and project
snapshots must agree for overlapping identities; account proofs take precedence.

Project observations retain separate project-configuration and project-identity
proofs, never an invented account-configuration proof. Known project budgets
outside the index still need their own GET and matching project billing context;
relinked/disconnected projects cannot close old-account budgets. Cleanup verifies
single-project scope, both proofs and current association before DELETE. Persisted
receipts include the project review; account receipts retain their prior format.
Parent failure after own 404 still fails readback. No new credential option or
account/project mutation is added. The native account index and the project path
remain non-authoritative over omissions; email coverage remains unresolved.

The added method and `ProjectBillingInfo` schema are unchanged fragments of the
pinned Cloud Billing document above. Catalog now has 203 rules and 801 methods.
[Project billing information](https://docs.cloud.google.com/billing/docs/reference/rest/v1/projects/getBillingInfo),
[Budget scope](https://docs.cloud.google.com/billing/docs/reference/budget/rest/v1/billingAccounts.budgets/list)
and [access control](https://docs.cloud.google.com/billing/docs/how-to/budget-api-access-control)
provide the native contract. Project read permissions are
`resourcemanager.projects.get` and `billing.resourcebudgets.read`; project cleanup
additionally requires `billing.resourcebudgets.write`.

Protocol cases cover denied/empty account visibility, paging, duplicate/partial
responses, wrong identities, non-single-project filters, two-scope drift, native
parent denial/404/relinking, failed late parent reads and saved-budget own 404.
Independent native schema compilation checks ProjectBillingInfo. Real SQLite
inventory and cleanup tests close/reopen the database through observation,
permission loss, exact absence, reappearance, execution and final tombstone.

`TestBillingBudgetProjectIndependentMockGCP` uses the same pinned, unmodified
Google backend and a native project billing association. Runtime project GET,
scoped Budget LIST/GET, DELETE and restarted readback are forwarded unchanged.
Three explicitly synthetic account-index 403s select the project-permission path.
The backend **does not enforce IAM or the LIST scope filter**; a single-project
native fixture verifies request compatibility and lifecycle only. Protocol tests
provide the scope/failure evidence. No real-cloud evidence or complete consumer
coverage is claimed. The shared harness only routes project billing endpoints to
the existing native Billing service.
