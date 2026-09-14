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
`billingbudgets.billingAccounts.budgets.list/get`. No Billing resource rule,
mutation, IAM change or notification-send operation is added.

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
The catalog retains 202 resource rules and adds four read methods (799 total).

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
