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
