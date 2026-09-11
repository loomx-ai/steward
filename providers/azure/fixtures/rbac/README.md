# Azure RBAC native evidence

The retained Authorization Swagger selection is pinned to Microsoft commit
`5da82d5c3687ac3cc845330aaf0f13d3a40ce47e`. It includes role-definition
GET/LIST/DELETE and role-assignment GET/LIST-for-subscription/LIST-for-scope/DELETE
from `2022-04-01`, plus both PIM schedule GET/LIST pairs from `2020-10-01`.
Four root documents and two transitive common-type documents retain their
original source hashes. `documents.json` identifies them; `sources.json` binds
11 unchanged examples to their native paths, methods and file hashes.

`TestRBACNativeSources` checks every selected operation, binds the original
example requests and validates all 11 response bodies against the retained
native schemas without networking. The 13 documented responses include both
DELETE 200 bodies and DELETE 204 absence responses. Example identifiers such as
`subID`, `roleDefinitionId` and role type `roletype` are upstream placeholders;
they are preserved rather than changed into deployable production identities.

`cli-recordings.json` extracts 14 original response bodies from three Microsoft
Azure CLI recordings at commit `5e912a20420a11f53b0b7bc9a2c1f03f98de6238`.
Each response retains the upstream file/hash, interaction index, request method
and URL, response status and a selected set of protocol headers. Request bodies,
authorization headers and unrelated interactions are not retained. The recorded
bodies themselves are unchanged strings. Reproduction downloads the listed
source files, verifies their SHA-256 values, parses the YAML `interactions`,
selects request URLs containing `/providers/Microsoft.Authorization/` without
regard to case, and copies each response body/status and protocol headers.

The recorded role-definition requests use `2022-05-01-preview`, whereas the
selected runtime contract is `2022-04-01`. The parser compatibility test reads
645 returned role objects, including 639 roles in one native list, with both
custom and built-in definitions. This is response compatibility evidence, not a
claim of a live `2022-04-01` role-definition call. Native Backup Contributor
permissions contain duplicate actions; the adapter preserves these valid source
values. Definitions also retain private conditions and unknown future fields in
the configuration comparison.

The role-assignment recording uses the exact selected `2022-04-01` API. Tests
read 167 returned assignment objects, including 109 tenant/management-group
inherited entries, and retain a native DELETE 200 body. The source recording's
principal-filtered and `atScope()` lists are not evidence of unfiltered complete
subscription discovery. No recorded role-definition DELETE or PIM execution is
claimed.

Native role GUIDs are invariant across documented tenant and subscription
read aliases. Requests stay at the connected subscription or its own resource
scopes. An inherited tenant/management-group assignment can be recognized in a
local list without issuing an out-of-bound read or delete. Foreign subscription
assignment rows fail the local index. Local lists use native individual GETs;
a listed resource's 404 is a dependency failure, never a successful empty list.
The role-definition list requests `atScopeAndBelow()` to include narrower scopes.
Paging binds the original collection, version and filter, rejects cross-tenant
parameters, and detects duplicate local identities and repeated pages.

The composed protocol tests cover all four read families, subscription/group/
resource identities, aliases, inherited assignments, private-data changes,
metadata errors, collection failures and paging boundaries. They do not run an
independent emulator or a live Azure account. Registered inventory, graph integration and native action workers now use these
contracts; their additional verification and remaining boundaries follow below.

Official behavior references:

- [Role definitions: List](https://learn.microsoft.com/en-us/rest/api/authorization/role-definitions/list?view=rest-authorization-2022-04-01)
- [Role definitions: Delete](https://learn.microsoft.com/en-us/rest/api/authorization/role-definitions/delete?view=rest-authorization-2022-04-01)
- [Role assignments: List for subscription](https://learn.microsoft.com/en-us/rest/api/authorization/role-assignments/list-for-subscription?view=rest-authorization-2022-04-01)
- [Custom-role assignment prerequisites and assignable scopes](https://learn.microsoft.com/en-us/azure/role-based-access-control/custom-roles)
- [Role-definition tenant/subscription ID forms](https://learn.microsoft.com/en-us/python/api/azure-mgmt-authorization/azure.mgmt.authorization.aio.operations.roledefinitionsoperations?view=azure-python-preview)
- [Role-assignment scope, conditions and orphaned principals](https://learn.microsoft.com/en-us/azure/role-based-access-control/role-assignments)

## Registered native inventory and cleanup

Two global product sources register role definitions and role assignments. Each
page observes the native collection, individual GETs, scope contexts, locks and
PIM schedules twice and binds the resulting private configuration into the
continuation. Shared assignable scopes outside the connection, built-in roles,
unknown native scope readers, protected tags, management locks and matching PIM
schedules remain protected. Resource/group absence does not remove a surviving
assignment. Native Cosmos scope names and response aliases survive projection,
requests and JSON recovery without lowercasing the source selector.

Reviewed references create independent deletion prerequisites for role,
assignable scope, assignment scope, their resource ancestors and delegated
identity ARM IDs. Native reverse indexes discover sources that have not been
saved. Known sources also receive their own GET even if omitted from LIST.
Unreadable lists/details, changed observations and late references block target
cleanup and recovery. Managed-group discovery excludes RBAC extensions from
ownership; an AKS composition test deletes a reviewed assignment separately
before permitting the controller's existing native cascade.

Both native DELETE success variants are covered: a 200 resource body must match
the reviewed private configuration, and a 204 response has no resource body.
Neither acknowledgement establishes absence. The resource's own GET, authenticated
request and durable receipt govern waiter/readback completion. Tests reject
changed configuration/scopes/PIM, forged references/selectors/receipts, unexpected
async headers/statuses and resources recreated after deletion. Invocation, log
and normalized/Raw projections do not expose conditions, descriptions,
permissions or unknown authored fields.

The SQLite integration uses registered global scan sources, the scheduled graph
worker, persisted cleanup plans and real execution workers. A role-only plan is
blocked by retained assignments; selecting the two assignments and custom role
produces three ordered native jobs. Every job is restarted from JSON and saved
state while deletion remains pending, and only individual native absence closes
the resource. The built-in role and independent scope resources remain. A later
403 does not close the surviving inventory record.

These are composed protocol and application tests, not an independent RBAC
emulator or live deployment. PIM schedule deletion and tenant/management-group
administration remain unfinished. Native DELETE has no conditional version
guard, so the preflight-to-mutation edit window cannot be eliminated.

## Managed identity principal dependencies

`identities/IdentityGet.json` and `identities/IdentityListBySubscription.json`
retain the Microsoft examples from the same pinned commit, under
`specification/msi/resource-manager/Microsoft.ManagedIdentity/ManagedIdentity/stable/2023-01-31/examples/`.
The source tests check both original byte digests, bind the native requests and
validate response bodies against the selected 2023-01-31 schema. The retained
ManagedIdentity document digest is
`e5d776b4b7c62740fa4793bccbde62ab5a5fda40be27de2a2c815b711aa429c9`.
Its `principalId` identifies the service principal; `clientId` identifies an
application and cannot join an assignment to an identity.

Inventory authenticates the native principal/tenant tuple and its exact ARM
selector. Assignment sources retain authenticated tenant/principal selectors.
A native subscription assignment index and individual target reads join these
to user-assigned identity resources or root system-assigned resource identities.
The empty identity is also bound, so enabling an identity after review cannot
hide a newly relevant assignment. Group membership, attached shared identities,
application IDs, User/Group principal types and other tenants cannot create this
ownership. Unmatched external principals remain unresolved Uses references; no
Microsoft Graph operation or principal deletion is introduced.

Tests cover independently scoped assignments, ordinary and Monitor action
drivers, unindexed/omitted/late sources, missing or altered proofs, changed or
unreadable identities, native own-resource absence, JSON/client recovery, and
AKS controller deletion of a VM with a system identity. The SQLite worker
scenario scans role definitions, assignments and a user-assigned identity, blocks
an identity-only plan, and completes three independent jobs after native
readback across restarts. The assignment precedes both its role and identity;
built-in roles and scope resources remain.

APIM and Monitor receiver compositions replace the upstream recordings’
non-UUID tenant/principal placeholders with fixture GUIDs. Original evidence
files remain unchanged. Native UserAssigned/None responses can retain root
principal fields; those fields do not make the host own a system identity.
Existing ARM inventory needs a rescan before principal matching. An unknown
resource reader cannot prove its identity. Matching is limited to assignments
within the connected subscription; tenant-wide and cross-subscription identity
administration is not claimed.
