# Deployment Stacks native contracts

The nine unchanged examples and the selected List/Get/Delete contracts cover
resource-group, subscription and management-group scopes at API `2025-07-01`.
`sources.json` pins Azure REST API specs revision
`07a27fbba41f8597cdfe0f866fcbf9f7c37390f4` and each original payload's SHA-256.
The shared catalog contains the transitive definitions needed for offline schema
validation. No provision/update/export-template operation is invoked by this work.

## Observed upstream inconsistencies

- `DeploymentStackSubscriptionGet.json` returns a resource-group-scoped ID for a
  subscription-scoped request. The test preserves the original example and rejects
  this response identity. Resource-group and management-group Get examples match
  their request scopes.
- All three List examples use `succeededWithFailures` for their second entry. That
  value is absent from the same source's provisioning-state enum. Tests require
  this exact discrepancy, then validate an in-memory positive control with only
  that field changed to `succeeded`. The retained files remain unchanged. The
  enum is extensible; a future inventory implementation must preserve unknown
  states without treating them as successful cleanup authorization.
- Delete examples provide no unmanage query flags. They do not prove what omitted
  flags do. Binding tests separately verify explicit resource/resource-group/
  management-group `delete` and `detach` flags, `ResourcesWithoutDeleteSupport=fail`
  and `bypassStackOutOfSyncError=false`.
- The native DELETE declares empty 200/204 completion and 202 with Location-based
  polling. Its example Location is a subscription `/operationresults/{id}` URL
  using API `2018-08-01`. A later action driver must validate this native callback
  rather than assume it shares the stack API version or resource path.

## Implementation boundary

Scope parsing and own-response identity validation cover all three native forms.
Parsing a management-group ID is not authorization to access that management group
through a subscription connection. The runtime still needs explicit management
scope discovery/authorization and complete product inventory integration.

API diagnostics omit templates, parameters, outputs, parameter/template links,
extension configuration and identifiers, external inputs and free-form errors.
Only selected identity/state, managed-resource references and recognized unmanage/
deny settings survive. The native schema retains extension and external-resource
contracts; omitting those private values from logs does not remove them from the
remaining inventory and cleanup scope.

This milestone adds contracts and privacy/identity checks, not a registered
resource specification or a reviewed cleanup driver. Required follow-up includes
complete member observations (including failures and extensible resources), deny
assignment effects, deletion versus detachment review, persisted polling and own
resource readback. The three parity mappings are registered, with behavioral acceptance still open.
These are offline schema/protocol tests, not an independent emulator or live Azure
acceptance result.

See Microsoft's [deployment stack lifecycle documentation](https://learn.microsoft.com/en-us/azure/azure-resource-manager/bicep/deployment-stacks).


## Additional official CLI evidence for the next runtime stage

The CLI's [management-group](https://github.com/Azure/azure-cli/blob/ea185727729efc032ad9d4eef9ec355ee74ebaae/src/azure-cli/azure/cli/command_modules/resource/tests/latest/recordings/test_delete_deployment_stack_management_group.yaml),
[subscription](https://github.com/Azure/azure-cli/blob/ea185727729efc032ad9d4eef9ec355ee74ebaae/src/azure-cli/azure/cli/command_modules/resource/tests/latest/recordings/test_delete_deployment_stack_subscription.yaml)
and [resource-group](https://github.com/Azure/azure-cli/blob/ea185727729efc032ad9d4eef9ec355ee74ebaae/src/azure-cli/azure/cli/command_modules/resource/tests/latest/recordings/test_delete_deployment_stack_resource_group.yaml)
delete recordings were inspected separately. Their API 2025-07-01 asynchronous
deletes return empty 202 bodies with signed `Azure-AsyncOperation` URLs under
`Microsoft.Resources/locations/{region}/deploymentStackOperationStatus/{uuid}`.
Subscription and resource-group operations include a subscription prefix;
management-group operations use a provider-root URL. Native 200 detach responses
also occur. These observations require both recorded and declared callback
protocols in the later action driver. They have not yet been incorporated into
runtime replay tests, and are not live execution by Steward.

The subscription-bound inventory reader now consumes all native list pages,
validates collection identity, and performs an individual GET for each listed
stack and each known stack omitted by the index. Only a known, unlisted stack's
own 404 establishes absence. A listed stack returning 404 makes the observation
incomplete. Unknown provisioning states remain observations, not cleanup approval.
Fault tests cover scope escapes, pagination changes, duplicate identities and
failed or mismatched own reads. Native own-read objects remain private inputs to
member review; the runtime integration described below does not establish
management-group support or cleanup acceptance.

The reader now attaches a private-fingerprint-backed member review. It keeps
current members separate from deleted, detached and failed historical results;
missing current-member arrays do not become complete empty inventories. The
unchanged native resource-group GET example exercises a foreign-subscription
member, a subscription-scoped resource and an ID-less extensible member. All
three remain accounted for; extension identifiers stay private and their changes
invalidate the fingerprint. ARM-addressable membership completeness is not
cleanup readiness, ownership or authorization. Exclusive cleanup ownership,
management-group scope and cleanup remain outstanding.

Runtime registration now connects this reader to subscription/global scans and discovers resource-group stacks through the resource-group index, including known omitted groups. Two complete observations must agree before returning a batch. Continuation cursors bind the request, bundle revision, private configuration fingerprints and absence evidence. Returned inventory strips template inputs and extension identifiers. All stack observations remain protected while cleanup and graph ownership are unfinished. Management-group support is still outstanding.

Member reviews are now authenticated to their stack and connection before graph
reconciliation. The graph records observed `member_of` edges, with blocking
unresolved references for missing, foreign or unsupported members and unknown
states. These edges grant no cleanup delegation. The SQLite worker test covers
actual global shard creation, canonical subscription/global scope persistence,
member graph reconciliation, omitted-parent recovery and failed-rescan preservation.
It also verifies that the runtime's global scope matches the scan creator's scope.
Management-group inventory and native cleanup remain unimplemented.


`delete-recordings.json` retains selected DELETE and polling interactions from
the three pinned official CLI recordings above, including exact response headers,
bodies and signed callback URLs. Source hashes and a fixed extraction hash guard
provenance. Seven deletion episodes include 48 status polls; callback body IDs and
names match the operation path and UUID. The recorded states are `deleting`,
`deletingResources` and `succeeded`. Recorded action flags must match the original
DELETE query. URL tests reject altered scopes, regions, versions, flags and partial
signatures. Poll diagnostics remove free-form messages and private nested data.

These are offline protocol validators, not a wired deletion action. Their support
for management-group callback syntax does not authorize management-group operations.
Operation success alone does not establish stack or member absence: the recordings
do not immediately perform that own-resource readback. Persistent receipts, cleanup
consequence review and final own-resource checks still require implementation.

The HTTP polling client now accepts signed, owner-bound receipts that survive JSON
checkpoint storage. It supports synchronous empty DELETE responses, native status
callbacks, declared Location callbacks and the status-to-result transition when
both headers are present. A changed receipt is rejected before any HTTP request.
Five supported official subscription/resource-group episodes replay 34 real poll
responses through the client; the management-group episode and the episode using
`bypassStackOutOfSyncError=true` remain explicitly rejected by execution policy.
Legacy operation-result diagnostics also discard unexpected private payloads.
These primitives are not yet wired to a cleanup action, and poll completion does
not claim that the stack or its members are absent.

Resource-group stack inventory now fills an omitted stack location from the
current parent resource-group GET. It does not borrow a subscription location or
trust the resource-group index's location. A supplied stack location takes
precedence. Both snapshot comparison and persisted pagination cursors cover the
resolved location, so parent-location drift invalidates an incomplete scan.
Missing location remains missing; this inventory fallback is not authorization
for a deletion callback, which still requires execution-time scope revalidation.

The delete-plan compiler now binds authenticated direct membership and reviewed
lifecycle consequences to the pinned native DELETE operation at subscription or
resource-group scope. Resource, resource-group and management-group flags are
explicit; categories without reviewed members detach. Conflicting choices within
a category and retaining a descendant of a deleted ARM parent are rejected.
Reviewed nested controllers must form a chain to the stack; each native child
controller still needs to verify its own membership and cascade consequences.
`retain_all_resources`, `retain_resources` and typed `delete_options` must agree
with the executor-provided impacts. Unsupported-resource fallback remains `fail`
and out-of-sync bypass remains false; caller flags cannot override these. The
compiled parameters round-trip through authenticated polling receipts.

This is parameter compilation, not live preflight or a registered cleanup action.
Live member reads, protection/denial review, native child closure, prerequisite
validation and final own-resource readback remain required before enabling deletion.
