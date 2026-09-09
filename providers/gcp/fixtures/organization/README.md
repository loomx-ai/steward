# Organization ancestry verification

Steward discovers the organization containing the connected project through native
Resource Manager v3 Project, Folder and Organization GETs. It follows the actual
parent chain. A project without an organization produces an empty result. A
missing or denied ancestor read fails the shard and cannot establish absence.

Two full ancestry reads must agree on the project creation/parent identity, every
folder creation/parent identity and the organization response. The global
`organization-ancestry` source is non-authoritative: moving a project or losing
ancestor visibility does not delete the previously observed organization.
Workspace customer IDs remain native organization metadata; standalone
organizations without that field are also supported. `DELETE_REQUESTED` is shown
as its native state, without claiming that the organization has been purged.

The organization has a read-only resource rule. The current public
[Resource Manager v3 Organization API](https://docs.cloud.google.com/resource-manager/reference/rest/v3/organizations)
has GET, search and IAM methods, but no organization DELETE. Google's
[standalone organization lifecycle guide](https://docs.cloud.google.com/resource-manager/docs/delete-standalone-org)
uses the console for owner-authorized deletion. That separate workflow is not a
Steward cleanup action. Organization ancestry grants no authority to mutate
organization-level firewall policies: they retain their explicit firewall scope.

## Source and tests

`native-schemas.json` retains three unmodified schemas from the official
[Resource Manager v3 Discovery response](https://cloudresourcemanager.googleapis.com/$discovery/rest?version=v3),
revision `20260820`, SHA-256
`e46acd311b9d23a7ddac98ab85e2d9edaec3df9105f66679821c055c8e1c6131`.
This uses the existing source fragment and methods; no upstream method/schema
objects were changed or newly selected. Tests validate the synthetic Project,
Folder and Organization responses with an independent JSON Schema validator.

Run from the repository root:

```sh
go test ./providers/gcp -run 'TestOrganization|TestFirewallInvokeExplicitHierarchyBoundary' -count=1
go test -race ./providers/gcp -run 'TestOrganization|TestFirewallInvokeExplicitHierarchyBoundary' -count=1
```

Four test functions cover direct/nested ancestry, unowned projects, standalone
organizations, deletion-requested state, malformed identities/timestamps, cycles,
concurrent changes, foreign scopes and 403/404/500 responses. The real provider
registry, inventory Creator, worker and SQLite test verifies global source
routing, read-only capabilities and preserved observations after permission loss
or a successful scan with no organization. Native GET invocation is restricted to
the actual project ancestor or the separately configured firewall boundary.

These are synthetic HTTP and persistence tests. The inspected pinned Google
Config Connector mockgcp has project/folder implementations but no Organization
handler. No independent organization emulator or real-cloud acceptance is claimed.
Inventory requires `resourcemanager.projects.get`, `resourcemanager.folders.get`
for each traversed folder, and `resourcemanager.organizations.get` for the root.
Connection validation alone does not prove ancestor permissions.
