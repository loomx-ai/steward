# Azure Monitor diagnostic settings evidence

The twelve JSON examples are unchanged Microsoft sources from
[`Azure/azure-rest-api-specs` at `6005e166d7172cb62fd2894971dbfe1910ac5285`](https://github.com/Azure/azure-rest-api-specs/tree/6005e166d7172cb62fd2894971dbfe1910ac5285/specification/monitor/resource-manager/Microsoft.Insights/Insights/preview/2021-05-01-preview).
`sources.json` records each example's immutable URL, SHA-256, operation and
path. `documents.json` records the two root Swagger documents and their two
common-type dependencies. The catalog keeps those documents and their source
digests; no examples or response schemas are repaired in place.

The eight selected operations are resource and subscription diagnostic-setting
GET/LIST/DELETE, plus resource category GET/LIST. Category resources describe
capabilities and have no DELETE operation. Both setting DELETE examples return
empty synchronous 200 and 204 responses. The source check replays all fourteen
responses through the transport and validates all ten response bodies against
the retained schemas using an offline resolver.

The selected TypeSpec-generated `openapi.json` declares `nextLink` pagination
for resource settings and categories. The older handwritten file at the same
API version still declares non-pageable lists; it is not selected alongside the
TypeSpec operations. The subscription Swagger remains non-pageable. Runtime
tests compose multiple resource pages from the original rows and reject
continuations for the non-pageable subscription collection. These composed
responses are test scenarios, not additional native recordings.

## Source discrepancies

- `getDiagnosticSettingCategory.json` is a **setting GET** example despite its
  filename. Its resource ID omits the `providers/Microsoft.Insights` segment and
  ends in `service`, while both its name and requested name are `mysetting`.
  Runtime identity validation rejects it unchanged.
- `listDiagnosticSettingsCategory.json` is similarly a **setting LIST** example.
  Its row omits the extension provider segment and reports the source workflow
  type. Runtime validation rejects that row unchanged.
- The four subscription GET/LIST examples return a slashless
  `subscriptions/.../providers/AzureResourceManager/diagnosticSettings/ds4` ID
  and `type: null`. Only this exact subscription alias is normalized. The four
  null type/schema disagreements are asserted explicitly in the source test.
  The two original GET requests ask for `mysetting`; their `ds4` responses are
  rejected for those requests. Composed runtime requests use the response's
  actual `ds4` identity without changing the native body.
- The normal resource GET and LIST report different Event Hubs authorization
  rule IDs for the same setting. Runtime LIST reconciliation uses its original
  row as the composed GET response; it does not silently merge these examples.
- The category LIST has `WorkflowRuntime` and `WorkflowMetric`; its companion
  GET only contains `WorkflowRuntime`. The runtime category index test replays
  each original list row as that category's GET response.

## Lifecycle and discovery contracts

Microsoft's [diagnostic settings documentation](https://learn.microsoft.com/en-us/azure/azure-monitor/platform/diagnostic-settings)
states that settings should be deleted when their source is deleted, renamed
or moved: a retained setting can apply to a recreated resource. Source/group
absence is therefore not a diagnostic-setting deletion receipt. Destinations
are shared references and can be in another subscription or tenant. Parsing
them does not authorize cross-subscription reads. An omitted `eventHubName`
selects a service default, so the parser retains the namespace and authorization
rule without inventing a hub ID. Explicit named hubs and the legacy
`serviceBusRuleId` resource slot remain references.

The resource LIST API requires an exact `resourceUri`; the subscription LIST
enumerates subscription settings, not settings on every resource. The current
[Resource Graph resource-type reference](https://learn.microsoft.com/en-us/azure/governance/resource-graph/reference/supported-tables-resources)
does not list `Microsoft.Insights/diagnosticSettings`. Its absence cannot be
used as proof that no native setting exists. Storage settings also have
[separate native service scopes](https://learn.microsoft.com/en-us/azure/azure-monitor/platform/resource-manager-diagnostic-settings)
for Blob, File, Queue and Table services; an account-only lookup cannot cover
those scopes.

The deterministic runtime tests cover exact scope/name/type, response status,
field casing, private configuration drift, destination shapes, duplicate and
filtered pages, and pagination host/subscription/collection/version binding.
An unreadable list or a listed row whose GET returns 404 is a dependency error,
never a successful empty inventory. These fixtures do not claim live Azure
execution or an independent Azure Monitor emulator.

## Registered application paths

`Microsoft.Insights/diagnosticSettings` has a global inventory specification and
an independent native action. The custom source reads exact source scopes from
the subscription ARM resource index, expands existing native child adapters and
Storage service scopes, and re-reads saved setting identities after source/group
absence. Two observations bind configuration, source incarnation, protection,
references and pagination. Unknown source kinds remain inventoried/protected;
there is no claim of discovering every never-seen orphan or every product root
omitted by the ARM index. Native 400, 403 or 404 collection failures are errors;
the available sources do not establish a GET/LIST unsupported-type response that
can safely become an empty collection.

All six native destination slots and every valid source/destination ancestor
produce independent required-deletion edges. The real graph contributor and
ARM action wrapper discover unindexed/late settings, reject forged saved
references and require a setting's own GET absence before target cleanup.
Diagnostic settings cannot borrow AKS, Monitor workspace or Application Insights
managed-group cascade authority, including when a generic group index lists the
setting as a descendant. Foreign destination IDs remain unresolved references;
the adapter does not issue foreign subscription reads.

The SQLite integration test uses the registered inventory source, scan and graph
workers, actual cleanup planning/execution jobs and recreated application
services for pending deletion recovery. Source/group deletion alone preserves
the setting. The native setting's separate GET controls final completion.
A later successful scan closes a saved setting only after its own GET returns
404 in both native observations. Explicit absent IDs appear only on the final
complete page, and the worker validates them against its fixed baseline.
The actual scan/graph scenario removes a different setting externally and
verifies that only its record closes, with no cleanup deletion tombstone.
`TestDiagnosticNativeSourceNamesAndOrphanSelectors` also composes native
case-sensitive source names and documented Cosmos response-ID aliases. Saved
selectors are authenticated with configuration/context and fixed across inventory
pages; this is a selector regression, not evidence that every Cosmos child
supports diagnostic settings. The shared worker test checks connection/kind/
partition isolation and deep copies saved metadata across pages.

The extension's native API version is validated against its own final provider
namespace. Composed two-page tests under API Management, Batch and Stream
Analytics parents ensure parent API versions cannot override Monitor's version;
those parents' own lists still reject a wrong native version. Fault scenarios
cover changed source names, private future properties, source/group protection,
management locks, hostile continuation URLs, unexpected DELETE bodies/statuses,
pending synchronous deletion, JSON recovery and authentication of absence paths.
No conditional DELETE header is invented: the native API still has an external
edit window between final validation and mutation.
