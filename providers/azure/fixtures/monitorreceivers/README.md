# Azure Monitor receiver references

Four unchanged official examples are retained from Microsoft specification
commit `e45039baa985c442877529906e705982a6e0099d`. `sources.json` binds their
bytes, native operations and the existing full-document checksums. The three
complete source documents at that commit were also checked against those
checksums. The older namespace/workspace catalog entries retain their original
`main` source URLs; normal builds and schema validation use the checked-in
documents without fetching those URLs.

The Action Group Create example supplies Event Hub, ITSM, Function and Runbook
receivers omitted by its native Get/List examples. This is authoring evidence
only: no PUT operation is registered or replayed. Both returned resource bodies
validate against the retained Action Group Get resource schema. Native Event
Hub and workspace Lists also pass offline response-schema validation.

The Workspace Get example incorrectly returns a two-element array where its
schema requires one workspace object. The source test requires exactly that
schema discrepancy and verifies that the runtime rejects the original array;
it cannot establish an empty receiver index. The workspace examples also use
an invalid subscription placeholder in their request parameters and different
subscriptions/groups in their responses. Runtime scenarios explicitly select
one original Get row and rebind its identity and customer ID. Event Hub scenarios
use the original List row with documented identity/name substitutions. The
retained source bytes are never repaired.

Runtime scenarios resolve Event Hub namespace names against the complete native
subscription namespace index, then derive the child path under the verified
namespace. ITSM's published `subscription|customerId` form and an unqualified
customer GUID resolve against native workspace identities and the receiver's
region. Matching resources may be in another resource group. Each index is
unfiltered, reconciles every row with Get and repeats the full identity index.
Read-only changes unrelated to receiver identity do not become ownership.

Foreign or missing references retain typed native selectors, including tenant
and subscription qualifiers, without a fabricated ARM resource group or any
foreign request. Function and non-global Runbook names form child references
under their explicit parent IDs. Global Runbook display names are not assumed
to be native runbook names; their actual webhook-to-runbook mapping remains open.
Receiver URLs, ticket payloads and connection settings stay private.

Resolved and unresolved references participate in the existing signed source
proof. Inventory cursors, graph contribution and source action preflight reject
changes in resolution, including a newly appearing target, a namespace move or
a recreated workspace with a different customer ID. Tests cover JSON recovery,
native incoming enumeration, ambiguity, malformed selectors/fields, duplicate
rows, paging, permission errors, listed-resource 404s, partial/asynchronous reads
and cancellation. SQLite scan-worker tests persist these references, rebuild
the graph, independently delete the Action Group and recover its native result.
Receiver resources remain untouched.

The existing non-Monitor ARM destination drivers now share these incoming guards.
After namespace/workspace absence, matching still uses the frozen ARM name or
authenticated workspace customer GUID; JSON recovery and foreign-tenant/scope
scenarios verify that absence cannot erase a reference or acquire a local target.
Managed-group tests repeat receiver resolution for reviewed members before
deletion and during residual readback. Complete native discovery of omitted group
members and global Runbook webhook mapping remain open. These are native-source,
protocol and application integration tests, not an emulator or live-cloud run.

Additional reference: Microsoft's [ITSM receiver example](https://learn.microsoft.com/en-us/powershell/module/az.monitor/new-azactiongroupitsmreceiverobject?view=azps-15.5.0)
retains the same compound workspace selector as the native specification example.
