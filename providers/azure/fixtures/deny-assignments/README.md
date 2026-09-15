# Native deny-assignment read contracts

The two examples are unchanged from Azure REST API specifications commit
`07a27fbba41f8597cdfe0f866fcbf9f7c37390f4`, Authorization API `2022-04-01`.
`sources.json` records source and example URLs and SHA-256 fingerprints. Native
placeholder IDs remain unchanged: example binding tests do not treat them as
connection-authorized resource identities.

Only `DenyAssignments_Get` and `DenyAssignments_ListForScope` are selected. These
cover own reads and paginated scope enumeration; no deny-assignment mutation or
cleanup resource spec is introduced. The runtime reader validates subscription
ownership, IDs, resource scopes and response types before returning private data.
It reads every listed or known ID, accepts absence only for an unlisted known ID
whose own GET is 404, and compares two complete observations. Permissions errors,
foreign/inherited scopes outside the connection, changed state and incomplete
pages fail explicitly. They never become an empty successful snapshot.

Enumeration is unfiltered. In particular, `atScope()` includes the requested
scope and ancestors; it cannot prove that descendant deny assignments are absent.
Changed filters, API versions, origins, repeated pages and Cosmos source-name
case changes are rejected. Cosmos scope spelling follows the existing RBAC
adapter's native identity rules. Public diagnostics retain transport/identity
metadata while dropping principals, exclusions, permissions, conditions,
descriptions and continuation URLs. Internal reads retain complete native data.

These are native observations, not evidence that a particular Stack owns an
assignment. The schema has no typed owning Stack ID, and description matching
alone must not authorize removal. Independent denies can legitimately remain on
retained resources. Stack consequence review, assignment ownership correlation
and post-operation verification are still required; this reader is not yet wired
to a Stack cleanup action.
