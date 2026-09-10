# Azure Monitor alert protocol evidence

This directory retains 36 unchanged examples from the official
`Azure/azure-rest-api-specs` commit
`e45039baa985c442877529906e705982a6e0099d`. `sources.json` binds 37 example
uses to 30 selected operations; Action Groups uses one list example at both
subscription and resource-group scopes. `documents.json` binds seven root
Swagger documents and four reference documents by their original SHA-256.
The catalog snapshot preserves the original selected paths and reachable
schemas. Source preparation verified the downloaded bytes against the Git
blob identities in Microsoft's pinned repository tree.

| Native family | API version | Selected operations |
| --- | --- | --- |
| `Microsoft.Insights/metricAlerts` | `2026-01-01` | Get, Delete, subscription/group List, status List/ListByName |
| `Microsoft.Insights/actionGroups` | `2023-01-01` | Get, Delete, subscription/group List |
| `Microsoft.Insights/activityLogAlerts` | `2026-01-01` | Get, Delete, subscription/group List |
| `Microsoft.Insights/scheduledQueryRules` | `2026-03-01` | Get, Delete, subscription/group List |
| `Microsoft.AlertsManagement/smartDetectorAlertRules` | `2021-04-01` | Get, Delete, subscription/group List |
| `Microsoft.AlertsManagement/prometheusRuleGroups` | `2023-03-01` | Get, Delete, subscription/group List |
| `Microsoft.AlertsManagement/actionRules` | `2021-08-08` | Get, Delete, subscription/group List |

The transport test binds each request to the selected catalog operation and
replays all 44 published responses. Thirty response bodies are validated
offline against the retained schemas. Fourteen DELETE responses are empty
HTTP 200/204, with no asynchronous operation. These source examples are not
evidence of a live Azure run, eventual resource absence, or an independent ARM
emulator. Inventory, dependency planning and deletion execution are separate
runtime work; this API foundation does not add executable resource rules.

The tests retain these published discrepancies explicitly:

- Subscription list examples for Action Groups, Metric Alerts, Prometheus
  Rule Groups and Smart Detector Alert Rules include an undeclared
  `resourceGroupName`. The test removes that request-only selector after
  asserting that the native operation does not declare it. A literal `subid`
  request placeholder is rebound to the test subscription. Response bodies
  remain unchanged.
- Both Smart Detector list examples return `nextLink: null`, although their
  schema declares a string without `x-nullable`. Validation expects exactly
  these two mismatches.
- The Smart Detector Get example publishes `actionGroups` as an array of
  `actionGroupId` objects. The Swagger describes an object with `groupIds`,
  `customEmailSubject` and `customWebhookPayload`. Its definition omits
  `type: object`, so JSON Schema's object-specific requirements do not reject
  the array. Passing schema validation does not reconcile these two shapes.
- Microsoft's Smart Detector routes spell the namespace
  `microsoft.alertsManagement`; native operation IDs retain that spelling.

Privacy tests invoke each family's subscription list with identity-free rows
and check both response logging and returned data. Notification receiver
arrays, criteria, conditions, queries, PromQL expressions, dimensions, labels,
annotations, detector parameters and custom notification payloads stay out of
inventory and diagnostics. Native private reads remain unchanged so later
configuration checks and reference extraction can use the complete content.
Tests also cover type-only and ID-only resource envelopes, mixed case,
preservation of public metadata, and unrelated resource families.
