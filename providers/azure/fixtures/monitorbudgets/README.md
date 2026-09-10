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
