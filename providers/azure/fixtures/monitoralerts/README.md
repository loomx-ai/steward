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

The dedicated runtime inventory entry now covers the seven alert families and
web tests using their native indexes. The composed inventory scenarios retain
the original Get properties, bind the example subscription to the test
credential, and add a second independently named resource. Native paging is
followed before projecting canonical identities, explicit references and
private configuration proofs. Resource-group List/Get, ownership, protected
tags and inherited locks participate in two complete observation passes.
The cursor binds both passes' resource/group/lock configuration, connection,
scope, requested kind and bundle revision; changing page size does not change
the snapshot. Lock ordering is immaterial. Permission failures and listed
resource/group 404s cannot authorize an absence sweep.

Native references include the exact action-group slots, resource/group scopes,
typed web-test metric criteria, web-test hidden component links, the declared
assigned identities of metric/scheduled rules, and full resource IDs in
Function, Logic App and Automation receivers. Subscription evaluation prefixes
do not create fictional assets.
Opaque URLs and authored conditions remain private. The lifecycle contributor
re-reads the source and resource group before resolving shared references;
foreign or missing targets remain unresolved. A resolved reference also
requires explicit selection of the referencing rule before destination
cleanup; it grants no ownership or automatic selection. Workbooks reuse the
same reference-resolution helper while keeping their original behavior.

The eight Monitor kinds are registered with native inventory and independent
deletion drivers. SQLite-backed scenarios use the real registry, scan creator,
worker, graph and plan, persist private configuration changes, serialize action
requests for recovery and reconcile native absence. Regional and global kinds
are selected through their actual scan scopes; a second resource survives
independent cleanup.

Native incoming indexes cover the six referencing rule families, both budget
APIs for Action Groups and web tests for components. Registered Monitor targets
check these indexes during graph contribution, preflight and readback, including
unindexed sources and rules created after DELETE. A target 404 cannot hide a
failed index or a retained source. Frozen source references are bound to the
private configuration/group proof, checked after JSON recovery and required to
name the actual destination before native source absence can satisfy a plan.

Deletion follows the retained empty synchronous 200/204 contracts and verifies
native absence. Tests reject asynchronous headers, unexpected bodies/statuses,
recreated configuration, altered receipts, changed protection/ownership and
late private drift. Explicit source deletion precedes shared destination
deletion without automatically selecting or deleting a receiver.

Event Hub receiver namespace/name lookup, ITSM workspace-GUID resolution,
Function/runbook child references, managed-group composition and integration
with the existing non-Monitor target drivers remain open. The sources do not
provide a receiver resource group that could safely be invented. These are
protocol and application integration tests, not emulator or live-cloud runs.
