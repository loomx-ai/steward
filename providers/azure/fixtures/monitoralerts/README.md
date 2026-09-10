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

The native reader tests additionally exercise all seven alert families and
the retained Application Insights `2022-06-15` web-test API. Each unfiltered
subscription list is paged and reconciled against a full native GET. Tests
compose matching GETs from the original list rows; they do not claim that the
separately published Get examples describe every listed resource. Private
authored configuration participates in comparison, while explicitly read-only
fields such as receiver confirmation status do not.

Runtime scenarios document these additional source discrepancies:

- Seven metric Get examples and the ordinary metric List examples contain
  `/providers/providers/` in their resource IDs. The strict identity parser
  rejects those unchanged originals. Only the composed runtime scenarios
  remove the duplicate segment. Metric and Prometheus examples also omit
  the optional root `name`; the full resource ID supplies the local name.
- Activity Log scopes use `subscriptions/{id}` without a leading slash.
  This native scope spelling is accepted specifically for Activity Log rules.
- Alert Processing Get and List examples contain bare `actiongGroup1` and
  `actiongGroup2` placeholders. Reference validation rejects them. Composed
  scenarios substitute full action-group IDs in the source resource group.
- The original Alert Processing subscription continuation uses HTTPS port
  `443` and `ctoken`. Only that family and API version normalize the default
  port. Paging still binds the original subscription, collection and version;
  altered hosts, ports, filters, fragments and repeated tokens fail.
- Web-test scenarios substitute the literal `subid` and bind Get to the
  response's resource group, which differs from its example parameters.
  The recorded List has root `kind: ping` and `properties.Kind: standard`;
  these native fields are validated separately. Get requires test content.

Boundary tests cover malformed identities, foreign subscriptions, ambiguous
fields, missing configuration, private changes between List/Get, duplicate
rows, permission failures, listed-resource 404s and asynchronous/partial read
responses. Explicit action-group slots support both published Smart Detector
shapes and deduplicate a shared group across Prometheus rules. Queries,
conditions and webhook payloads do not create references from arbitrary text.
These reader tests do not establish inventory, graph or cleanup completion.
