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
independent emulator or a live Azure account. These native primitives and API
sources precede registered inventory, application graph integration and action
workers; those paths remain unfinished at this checkpoint.

Official behavior references:

- [Role definitions: List](https://learn.microsoft.com/en-us/rest/api/authorization/role-definitions/list?view=rest-authorization-2022-04-01)
- [Role definitions: Delete](https://learn.microsoft.com/en-us/rest/api/authorization/role-definitions/delete?view=rest-authorization-2022-04-01)
- [Role assignments: List for subscription](https://learn.microsoft.com/en-us/rest/api/authorization/role-assignments/list-for-subscription?view=rest-authorization-2022-04-01)
- [Custom-role assignment prerequisites and assignable scopes](https://learn.microsoft.com/en-us/azure/role-based-access-control/custom-roles)
- [Role-definition tenant/subscription ID forms](https://learn.microsoft.com/en-us/python/api/azure-mgmt-authorization/azure.mgmt.authorization.aio.operations.roledefinitionsoperations?view=azure-python-preview)
- [Role-assignment scope, conditions and orphaned principals](https://learn.microsoft.com/en-us/azure/role-based-access-control/role-assignments)
