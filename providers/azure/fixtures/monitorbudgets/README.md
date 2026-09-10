# Budget notification protocol evidence

This directory retains 20 unchanged examples from Microsoft's
`Azure/azure-rest-api-specs` commit
`e45039baa985c442877529906e705982a6e0099d`. They cover Get, List and Delete
for `Microsoft.Consumption/budgets` (`2024-08-01`) and
`Microsoft.CostManagement/budgets` (`2025-03-01`), the newest stable versions
in those directories at that commit. Source preparation verifies downloaded
bytes against the pinned repository's Git blob identities. `sources.json`
records example hashes and operation bindings; `documents.json` records two
root documents and two common-type dependencies. The previous 211 catalog
documents remain semantically unchanged.

Both notification schemas declare `contactGroups` as fully qualified Action
Group IDs, supported only at subscription and resource-group scopes. Budgets
therefore participate in incoming notification references alongside Monitor
alert rules. See Microsoft's [Consumption Budget API](https://learn.microsoft.com/en-us/rest/api/consumption/budgets/get?view=rest-consumption-2024-08-01)
and [Cost Management Budget API](https://learn.microsoft.com/en-us/rest/api/cost-management/budgets/get?view=rest-cost-management-2025-03-01).

The offline schema test validates all 18 original response bodies without
schema exceptions. It replays seven native responses at subscription and
resource-group scopes, including the two empty synchronous HTTP 200 Delete
responses. The other 13 examples use billing or management-group scopes;
binding their requests succeeds, but this provider's subscription-scoped
transport rejects them before issuing any request. Retaining their complete
source evidence does not authorize access outside the selected connection.

Known differences between examples and usable runtime requests are explicit:

- All 20 examples carry extra scope selectors not declared by their selected
  operation. The test first requires their unchanged parameters to fail
  binding, then removes only the per-example named extras. These are
  `subscriptionId`, `resourceGroupName`, `billingAccountId`, `billingProfileId`,
  `departmentId`, `enrollmentAccountId`, `customerId` or `invoiceSectionId`.
  The complete native `scope` remains the path selector. Response bodies are
  unchanged throughout replay and schema validation.
- Consumption response IDs omit a leading slash. Both cost-budget Get
  examples request a subscription budget but return a resource-group ID.
  Passing transport/schema tests does not prove that these returned identities
  match the individual request; runtime reconciliation must check that.
- Subscription List examples include both subscription and resource-group
  budgets. A group-scoped List must still be limited to that resource group.
  The retained Management Group List also mixes multiple scopes, and one
  Customer List row omits the terminal budget resource path. Neither can
  establish inventory authority for this subscription connection.
- No selected List example publishes a continuation. The schemas declare
  `nextLink`; any runtime paging scenario must be described as composed.

Privacy tests cover both native namespaces, identity-free List responses,
Invoke return values, API logs, type-only/ID-only resource envelopes, mixed
case and unrelated resource families. Entire notification dictionaries and
dimension/tag filters stay private. Amount, category and current-spend
observations remain visible. Sanitization leaves original private reads
untouched for subsequent dependency extraction and configuration checks.

This evidence establishes native catalog, transport and privacy behavior.
It is not an independent emulator or live-cloud run and does not establish
completed budget discovery, Action Group dependency checks or cleanup.

The dedicated native budget readers validate canonical subscription/group
identities, matching Get responses and required configuration before extracting
`notifications.*.contactGroups`. Consumption's missing leading slash is
normalized only for that native family. List scenarios bind Get to each
original row and split the original rows into two explicitly composed pages;
the retained source files remain unchanged. Subscription lists keep group
budgets, while a group list remains inside its exact group.

Private comparison removes only the declared read-only `currentSpend` and
`forecastSpend`. Changes to notification recipients, filters, amount, eTag
or other retained authored fields still matter. Repeated references across
notification thresholds are deduplicated, including case differences. ARM
strings in filter values do not invent Action Group links.

Reader tests cover malformed IDs/configuration, empty or partial responses,
Get/List mismatches, permission failures, listed-resource 404s, private drift,
ambiguous response-field casing and altered continuation scopes, methods,
versions or query filters. A listed budget's 404 is a dependency-read failure,
never evidence of an empty budget collection. These helpers still require
inventory, graph and independent cleanup integration.

The runtime inventory integration now enumerates subscription and every
validated resource-group index for each budget API. Group budgets missing
from the subscription List remain discoverable; duplicates across scopes
must have identical private configuration. Two complete observations bind
all resource/group configurations and management locks to the inventory
cursor. Subscription budgets keep a global inventory scope and have no
fabricated resource group. Native `scope` parameters retain the full
subscription/group path. A narrowly validated budget-ID path also permits
resource-level management locks on subscription budgets without extending
the generic ARM resource parser.

Composed tests cover both native Lists, group-only visibility, pagination,
private notification changes between reads/pages, disagreement between
subscription and group indexes, changed or missing groups, changed locks,
404/403 responses and cursor scope/connection/kind changes. Reordered locks
and read-only spend updates retain the same cursor binding. Notification
dictionaries remain private while explicit action-group references survive
projection. Native graph tests re-read source/group proofs, reject private
changes, and keep missing/foreign destinations unresolved.

Both budget APIs now have registered global-scope rules and native independent
deletion. Registered operation tests bind Get/Delete to the complete
subscription or resource-group path and reject foreign, nested or malformed
scopes. The generic ARM identity parser remains unchanged.

The shared-plan scenarios use real registered inventory assets: a budget can be
selected independently; a retained budget blocks Action Group cleanup;
selecting both orders the budget first and freezes its signed reference proof.
Native source absence is checked after JSON recovery before the destination
can execute. Subscription and every group budget index also protect against
unindexed or newly created sources, even when the destination is already gone.

Both APIs require the retained empty synchronous HTTP 200 Delete response.
Unexpected bodies, asynchronous headers/statuses and HTTP 204 fail validation;
an acknowledgement or DELETE 404 still requires final native Get absence.
Tests cover private drift, locks/tags/ownership, invalid or transplanted
prerequisites, receipt alteration, late references and recreated budgets.
SQLite-backed scan-creator/worker scenarios persist updates and reconcile
successful deletion without removing independent siblings. Failed collection
reads preserve saved observations. These remain protocol/application tests,
not independent emulator or real-cloud verification.
