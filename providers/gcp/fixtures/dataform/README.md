# Dataform native wire scenario

`resources.json` is a synthetic fixture written against the official Dataform
v1 response schemas. It is not a recording from a customer account or an official
response example. Native paths, field names, and the five independently deletable
resource types are asserted literally in `dataform_test.go` rather than derived
from the implementation's cascade table.

The pinned Discovery fragment in `catalog/source/discovery.json` retains the
official method and schema objects, original full-document SHA-256, and revision
20260829 from <https://dataform.googleapis.com/$discovery/rest?version=v1>.

Native contracts:

- [Repository deletion](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.repositories/delete): `force` covers compilation results and workflow invocations; workspaces, release configs and workflow configs must be deleted first.
- [Workflow cancellation](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.repositories.workflowInvocations/cancel): POST with an empty request, followed by GET until the invocation leaves RUNNING/CANCELING.
- [Compilation results](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.repositories.compilationResults): no independent DELETE method.

The fixture includes separate repositories in two regions, a running invocation,
scheduled release/workflow configurations, and references to existing Git, Secret
Manager, service-account and KMS resources. Tests preserve these external resources
and BigQuery outputs while cleaning only the selected repository tree.

`dataform_folders_test.go` adds a synthetic native folder server around this
fixture: two regions, personal and shared roots, a team folder, nested folders
and their repository. Search results and native content memberships are separate
from the implementation's catalog and lifecycle rules. Tests verify complete
paging, cursor scope/configuration binding, real parent backlinks, the complete
ancestor chain, ordered independent child deletion, cancellation recovery,
retention/protection blocks, partial/error replies, moves and recreation. The
SQLite inventory-worker test also checks that a successful empty search after
visibility loss does not mark an earlier folder observation deleted or closed.

Folder contracts:

- [Team-folder search](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.teamFolders/search) only returns folders accessible to the caller.
- [User-root contents](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations/queryUserRootContents) and [folder contents](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.folders/queryFolderContents) use a Folder/Repository entry union with native page tokens.
- [Team-folder contents](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.teamFolders/queryContents) uses the `dataform.folders.queryContents` permission.
- [Single folder deletion](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.folders/delete) and [team-folder deletion](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.teamFolders/delete) take only a resource name and return an empty JSON object. The runtime verifies reviewed children absent before these requests.
- [Code asset folders](https://docs.cloud.google.com/dataform/docs/organize-code-assets) organize single-file assets with hierarchical access controls.
