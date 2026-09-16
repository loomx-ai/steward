# Provider parity implementation gaps

Scope snapshot: 2026-09-15, updated for the Deployment Stacks and Data Protection inventory registrations. This is a repository scope audit, not cloud feature acceptance. The earlier mapping repair was audited at `0c43def71dc2fdef7d6fafe6b59fd8124e9e81d9`.

The matrix covers all 159 Alibaba Cloud specifications. The repository now contains 204 GCP and 475 Azure specifications, but those counts do not prove equivalence. All 159 rows remain pending behavioral verification.

The earlier audit found invalid YAML, 28 Azure mapping references to 18 absent specifications, and an incorrect GCP SSH-key mapping to service-account keys. The corrected matrix keeps absent candidates in `unimplemented_resources`; it does not remove them from the requested scope.

The new `go test ./providers` check runs in the existing `go test ./...` CI job. It detects invalid YAML, omitted or duplicated baseline resources, drift in baseline source/class/scope/actions/hooks/enrichment/parent discovery, unresolved implemented-resource references and stale implementation backlogs. A passing check verifies matrix consistency only.

Synapse now includes workspace/pool and data-plane inventory, reviewed workspace and Spark/SQL cleanup, artifact handling and retained restore-point/backup observations. NetApp now has native specifications and inventory plus volume, pool, recovery-object, policy and vault cleanup. NetApp group/account cleanup and interface deletion effects remain unfinished. These implementations moved their existing candidates into `resources`; that change does not close behavioral acceptance. The current missing-specification table has 13 types and 15 matrix references affecting 15 Alibaba Cloud rows.

## Current progress measures

| Measure | GCP | Azure |
| --- | --- | --- |
| Explicit native specifications | 204 | 475 |
| Baseline rows with at least one existing mapped specification | 155/159 (97.5%) | 144/159 (90.6%) |
| Candidate types still without a specification | 0 | 13 |
| Baseline rows affected by missing candidate specifications | 0 | 15 |
| Empty mappings requiring research | 4 | 2 |

These are registration/mapping measures, not functional completion percentages.
A row can have both an implemented mapping and an absent candidate. All 159 rows
remain `pending_verification`; none has a closed, requirement-by-requirement
behavioral acceptance record. Existing protocol tests and implementation volume
must not be presented as a percentage of full delivery.

The eight overall gates remain open. Catalog generation, transport, inventory,
lifecycle actions and retained tests have substantial implementations, but their
full service scope has not been accepted. Remaining work includes the explicit
missing families below, six mapping questions, behavior audits of existing
families, independent emulator/application evidence where applicable, and final
release verification. A defensible calendar completion date is not available.

## Delivery order and acceptance records

The NetApp interface-correlation milestone is implemented in `0e8bb9d`; group
cleanup remains unfinished. Next prioritize the explicit missing Azure families
and six mapping questions. For every baseline
row, record the inventory, dependency, lifecycle, cleanup and reconciliation
requirements with exact source/test/runtime evidence and remaining failures.
Close a row only when those requirements are proved; keep a distinction between
implemented-but-unverified behavior and missing implementation. Reassess the
remaining work after this acceptance inventory instead of projecting total
completion from successive local changes.

## Azure candidates without explicit resource specifications

These are candidates already named by the matrix. Missing specification files mean they cannot be counted as implemented independent resource rules. API availability, exact equivalence and lifecycle behavior still require review; related functionality elsewhere in the provider does not establish coverage.

| Candidate | Alibaba Cloud rows affected |
| --- | --- |
| `Microsoft.Graph/groups` | `ACS::CloudSSO::Group`, `ACS::RAM::Group` |
| `Microsoft.RecoveryServices/vaults/replicationFabrics/replicationProtectionContainers/replicationProtectedItems` | `ACS::EBS::DiskReplicaGroup`, `ACS::EBS::DiskReplicaPair` |
| `Microsoft.Graph/users` | `ACS::RAM::User` |
| `Microsoft.KeyVault/vaults/certificates` | `ACS::SSLCertificatesService::Certificate` |
| `Microsoft.KeyVault/vaults/keys` | `ACS::KMS::Key` |
| `Microsoft.MachineLearningServices/workspaces/onlineEndpoints` | `ACS::PAI::Service` |
| `Microsoft.Management/managementGroups` | `ACS::ResourceManager::ResourceDirectory` |
| `Microsoft.Purview/accounts` | `ACS::SDDP::Instance` |
| `Microsoft.RecoveryServices/vaults` | `ACS::HBR::Vault` |
| `Microsoft.RecoveryServices/vaults/backupFabrics/protectionContainers/protectedItems` | `ACS::HBR::HanaInstance` |
| `Microsoft.ServiceFabric/clusters` | `ACS::MSE::Cluster` |
| `Microsoft.Solutions/applications` | `ACS::OOS::Application` |
| `Microsoft.StorageSync/storageSyncServices` | `ACS::CloudStorageGateway::Gateway` |

## Mapping research still required

| Baseline | Provider | Remaining work |
| --- | --- | --- |
| `ACS::ECP::Instance` | GCP and Azure | Hosted Android/cloud-phone inventory and lifecycle; a generic VM is not an established equivalent. |
| `ACS::ECS::KeyPair` | GCP | Project/instance SSH metadata and OS Login keys, including discovery, ownership, expiry and removal. Service-account keys are excluded from this mapping. |
| `ACS::CEN::TransitRouterMulticastDomain` | Azure | Native multicast product/API availability and member lifecycle. |
| `ACS::RTC::Application` | GCP | Real-time communications application lifecycle; unrelated media-processing resources do not establish coverage. |
| `ACS::SMS::Template` | GCP | Template inventory and lifecycle, including provider/region differences. |

## Behavioral work remains separate

Existing specifications still need per-family evidence for inventory completeness, dependencies, controller/member ownership, retention, deletion and reconciliation. Read-only native resources such as GCP MetricsScope, route tables, organization resources and deployment revisions require explicit platform semantics; the absence of a direct delete action alone does not prove a missing operation.

The previously identified gaps remain open, including CEN QoS and packet marking equivalence, automatic SCC cluster inventory, project trial history, Elastic SAN retained-group purge, cross-project consumer discovery, and independent/emulator/live application acceptance. This audit does not close any of the eight overall acceptance criteria.

## Implementation order

Prioritize the absent Azure backup/recovery families spanning policies, vaults, replication and protected items, alongside unfinished Deployment Stacks lifecycle support. Synapse and NetApp specification registration no longer belong in the missing-specification queue; their remaining behavior stays in the acceptance backlog. For each family, verify the official API contract and lifecycle first, implement inventory and cleanup with persistence tests, then move its candidate from `unimplemented_resources` to `resources`. Keep the behavioral status pending until its required evidence is complete.

Alongside these implementations, resolve the explicitly empty mappings and audit the existing mapped families. Do not replace missing functionality with unrelated resources or mark a row complete because its type is registered.

Deployment Stacks now has a registered subscription/resource-group inventory source with own-read reconciliation, member evidence, configuration-bound cursors and protected observations. Management-group inventory, graph ownership and cleanup remain open; registration does not establish full lifecycle acceptance.

### Deployment Stacks ownership acceptance

Native `resources` membership does not establish exclusive ownership. In the
[Azure team's discussion of shared membership](https://github.com/Azure/deployment-stacks/issues/15#issuecomment-954081217),
a maintainer explained that multiple stacks can manage one resource. The team
[later declined to implement exclusivity](https://github.com/Azure/deployment-stacks/issues/15#issuecomment-1457053572)
and [confirmed parent and child resources can belong to different stacks](https://github.com/Azure/deployment-stacks/issues/15#issuecomment-1466320658).
These are historical maintainer statements; no current native contract or live
acceptance evidence in this repository establishes an exclusive-owner guarantee.
Deny settings and a single visible stack must not be treated as that guarantee.

Before enabling Stack cleanup, acceptance must cover shared members, members of
another stack beneath a deleted parent/group, and controllers outside the
connection's visible scope. Plans must preserve the affected resources and
controllers for review, reconcile product-level controller edges without
inventing exclusive ownership, and verify the chosen delete/retain consequences.
The sharing regression test exercises both valid membership shapes through
native own-read graph construction; it is a synthetic contract test, not a cloud
recording or a completed cleanup acceptance record. Full Stack cleanup remains
open alongside the rest of the 159-row scope.

### Deployment Stack retained resource-group readback (2026-09-16)

The native `ResourceGroups_Get` 2021-04-01 contract provides a resource ID and an
immutable location, but no creation identifier. Stack outcome observation now
accepts those available fields for an explicitly managed group whose reviewed
native category is `detach`. It reads the group again after its members and
records `resource_id_and_location` evidence separately from `creation_identity`.
This does not prove the original incarnation against an external same-ID,
same-location recreation. Any recorded creation identity must still match; the
exception does not apply to other retained resource types. Execution reconciliation
also requires the authenticated native operation to finish and deleted members
to be absent.

Protocol tests cover the documented field set, pending operations, missing groups,
changed locations, failed reads and lost/changed recorded creation identities.
The complete Stack action driver and end-to-end acceptance remain unfinished;
all 159 parity rows and eight overall gates remain open.

Source: [Resource Groups - Get (2021-04-01)](https://learn.microsoft.com/en-us/rest/api/resources/resource-groups/get?view=rest-resources-2021-04-01).

### Resource-group native operation recovery (2026-09-16)

Resource-group deletion now has a dedicated internal native receipt/polling
contract, replayed against the official Azure CLI 2021-04-01 recording. Its
subscription-level `operationresults` callback contains an opaque token, unlike
the Stack UUID callback. JSON-restored receipts bind the full request and job;
signed Location rotation may update the query but cannot change the subscription,
API version or operation token. Pending responses preserve retry timing, and
terminal operation receipts cause no further HTTP requests. Foreign callbacks,
changed requests, operation substitution, ambiguous headers, error statuses and
unexpected result bodies do not produce successful completion.

Resource-group product request projection now validates the explicit controller
tree before delegating any product scope: unique asset/native identities, matching
connection and partition, no cycles or orphan controllers, and no retained resource
inside a deleted group. External descendants require a typed native service or
VM/NIC attachment relation; external attachment deletion additionally requires
the explicit native Delete option. Retained external attachments remain in the
parent request without becoming independent delete requests. Product prerequisites
remain attached to their own controller, and group prerequisites cannot silently
authorize external deletion. Each projection has detached configuration maps and
preserves large JSON integers, so one product cannot mutate another's frozen plan.

Projection is still only a plan-shape check. Native membership/closure reads and
actual product preflight must confirm those relationships. No Stack membership or
execution receipt is fabricated for the resource group.

An internal resource-group lifecycle now connects these pieces to actual native
DELETE submission and read-only recovery. Inventory preserves a private full group
configuration fingerprint and its native location. Two live group indexes bracket
registered product preflights; native Monitor indexes additionally discover omitted
extension resources such as scoped budgets. Existing dependency wrappers retain
RBAC, diagnostic and incoming-reference checks. Locks, protected tags, managed
ownership, unreviewed resources and failed or incomplete reads prevent submission.
Intrinsic service children use only an already-checked native parent's context.
Native group deletion additionally checks VM/NIC attachment preparation readiness:
ordinary product Preflight can allow a later Execute to apply Delete-to-Detach,
but group DELETE skips that Execute. Live attachment evaluation must report no
remaining updates. VM/disk and NIC/public-IP tests prove unprepared retention stops
before DELETE, while prepared retention survives native deletion; missing or
recreated retained resources invalidate recovery. Removing the guard reproduced
an unsafe DELETE in both fixtures. Migration/recovery and specialized drivers still
require their explicit native-cascade preparation contract; they are not implicitly
opted in merely because their ordinary product preflight succeeds.

Recovery authenticates the original request/job receipt before polling, then checks
the group, every deleted member's actual product Readback plus own ARM read, retained
member creation identities and prerequisite product readbacks. Repeated observations
and a final group read prevent group 404 from hiding surviving or returning members.
Protocol tests exercise disk and SQL server/master cascades, synchronous/async
acceptance, JSON recovery, hidden budgets and failed/reappearing resources. SQL
live-resource absence still does not establish permanent purge of a soft-deleted
server. The group API's lack of a universal creation token still limits detecting
same-ID, same-configuration group recreation.

This remains an internal lifecycle, not a registered resource-group action. Full
prepared/prerequisite checkpoint recovery, all-family acceptance, native extension
inventory integration and graph/planner/executor wiring remain required before
catalog activation. In particular, the shared generic index still conservatively
rejects a reviewed top-level resource omitted by ARM even if its product index can
find it. Native operation completion alone never proves cleanup completion.
See `azure/fixtures/resource-groups/README.md` for pinned evidence and the explicit
boundary between recording replay and synthetic signing-rotation tests. All 159
parity rows and eight overall gates remain open.

### Deployment Stack product preflight integration

The Stack product-preflight stage now resolves each reviewed deleted member
through the actual Azure runtime and calls its registered driver, including
monitor/diagnostic dependency wrappers. It preserves native protection and
prerequisite failures and brackets the checks with fresh Stack/member reads.
Retained members receive existence/identity checks without deletion preflight.
Authenticated VM/NIC preparation checkpoints can explain only the corresponding
ETag change; configuration and creation identity are checked again afterward.

Tests exercise native disk and VM preflights, protected tags, locks, managed
groups, dependency-read failures, required host deletion before its host group,
missing members, concurrent changes and persisted preparation checkpoints.
These checks are a stage in the unfinished Stack action: they do not establish
that Stack DELETE performs a product's preparation or purge. Direct-child
execution, complete action/graph integration and end-to-end acceptance remain
open, together with all 159 parity rows and eight overall gates.

The independent-member execution stage now invokes the registered product's
Execute, Wait and Readback, with a checkpoint bound to the complete Stack request,
member and native result. A persisted checkpoint resumes without reissuing the
initial Execute. Completion requires product readback and a fresh member-own 404;
resuming a completed checkpoint repeats these reads. Errors never cause automatic
detachment, deny-setting changes or out-of-sync bypass. Complete Stack preflight,
dependency ordering, and integration of completed members into later Stack
closure checks are still required before this stage can enable Stack cleanup.

A historical Azure service regression could leave Stack deletion failing after
its resource group was already gone. The team
[identified the already-deleted-parent case in July 2025](https://github.com/Azure/deployment-stacks/issues/225#issuecomment-3050579345)
and [reported the fix rolled out in September 2025](https://github.com/Azure/deployment-stacks/issues/225#issuecomment-3286793197).
This is evidence for treating native operation status separately from resource
absence, not a current ordering guarantee or a reason to reproduce that historical
bug in the implementation. Member-execution tests are synthetic protocol tests;
they do not establish live-cloud acceptance or close any parity gate.

Completed member checkpoints are now integrated into product preflight. Every
checkpoint is authenticated before product reads, must be unique and complete,
and is followed by native product readback and member-own absence checks around
the remaining-member observations. Only the named member is covered; a completed
VM does not certify an absent NIC without its own evidence. Known native service
prerequisites are passed to the remaining parent's actual driver, whose recorded
parent configuration must still match before an ETag change can be accepted.
A changed configuration is rejected even if the original ETag is reused.

A native host/host-group test exercises child execution, JSON checkpoint resume,
parent ETag change, and successful parent product preflight. It also covers
missing/incomplete/duplicate/tampered receipts, child reappearance, read failures,
and parent/root changes. Member requests now use deterministic impact and
prerequisite ordering, preserving native receipt bindings when the semantically
identical Stack request is reordered between VM execution phases. Full service
and group closure, Stack action registration, graph integration and all overall
acceptance gates remain open.

Parent member execution now accepts verified progress at operation start and
persists the exact derived product request with its native result. Resume verifies
both the complete original Stack request and the permissible product projection,
then reuses that request for the native waiter and readback. Existing four-field
member receipts remain supported; new receipts with derived requests have five
fields. Progress cannot be replaced during resume. Original asset generations
remain in the native request so the actual product driver still checks its own
prerequisites and configuration before deletion.

Protocol tests cover host deletion followed by host-group deletion, JSON resume,
combined parent/child completion, native refusal, changed stored requests,
invalid projections and reappearing prerequisites. The VM case also covers prior
Stack retention preparation with a frozen pre-update generation, followed by
native execution without repeating the retention writes. This advances member
orchestration; full Stack service/group closure and controller/graph registration
remain unfinished, and no overall parity gate is closed.

### Deployment Stack closure after member completion (2026-09-16)

Service and resource-group closure can now consume authenticated, persisted
member execution progress. They re-read completed members through their actual
product readback and own resource endpoints before and after closure checks,
while preserving the complete original reviewed request. Only named completed
members are exempt from active membership requirements; a completed parent does
not certify its unrecorded children. Existing strict entry points remain intact.

The unfiltered, twice-read resource-group index tolerates a reviewed completed
member lingering in the index only after a fresh own GET returns 404. Duplicate
rows, unknown resources (including RBAC and diagnostics), reappearance, access
failures, changed groups and changed Stack reviews reject the whole result.
Service collectors keep their existing stricter native consistency checks: for
example, dedicated-host enumeration rejects a listed host whose own GET is 404.
That service list must converge before its closure succeeds.

Protocol regressions execute a real member driver, persist its checkpoint through
JSON, and exercise subsequent service/group closure without repeating DELETE.
These checks do not enable the complete Stack action, its graph bindings or final
end-to-end acceptance. All 159 parity rows and eight overall gates remain open.

### Flat Stack product prerequisites (2026-09-16)

A completed resource directly managed by the Stack can now be projected as an
execution prerequisite of another directly managed member when both appear in
the authenticated Stack review and the existing product-specific type/relation
checks match. The original controller graph, frozen request and completion
receipt are unchanged. Only the derived product request assigns the prerequisite
to the product parent; its native driver still verifies absence and permissions.
The same relation gates the recorded parent-configuration check after child
removal. Unrelated resources, incomplete members and unreviewed scope do not
acquire this projection merely by sharing a Stack or an ARM prefix.

The dedicated-host protocol fixture now runs both product-controller and flat
Stack-controller plans through child deletion, JSON checkpoint recovery, parent
preflight/closure, parent deletion and final member readback, including access
failures, changed configuration, changed receipts and reappearing children.
Complete Stack orchestration, graph registration and acceptance remain open;
this does not close any of the 159 parity rows or eight overall gates.

### Stack final product readback (2026-09-16)

Final Stack observation now joins its authenticated native operation and ARM
outcomes with the actual product Readback for every reviewed deleted member.
Completed independent member checkpoints are authenticated before native reads
and restore the exact product request, including completed prerequisites. Native
cascading without a member checkpoint leaves ExecutionResult absent, so product
readers retain their own requirements for operation evidence or residual reads;
no member operation is invented from a Stack receipt. Product readback and own
GET must both establish absence. A second ARM outcome pass catches Stack
recreation and retained-resource loss during product reads.

Protocol tests cover completed member JSON recovery, native cascading, pending
or failed Stack operations, lingering products despite ARM absence, forbidden
product reads, changed receipts, Stack recreation and lost retained groups.
Dedicated-host parent/child execution tests now continue through final readback
after Stack disappearance for both product and flat Stack controllers. Completed
native receipts still trigger fresh product and retained-resource observations.
ProductsReconciled is not a final action completion certificate: deny-assignment
release, complete orchestration and graph/action registration remain open, as do
all 159 parity rows and eight overall gates.

### Stack deny-assignment observation evidence (2026-09-16)

A pre-operation deny snapshot is now bound to the full reviewed Stack request and
job identity. It captures two unfiltered subscription observations between fresh
Stack review reads, persisting assignment IDs and private configuration hashes
rather than principals, conditions or descriptions. Subscription scope remains
queryable when a Stack's containing resource group disappears. Final observation
authenticates this snapshot and the native Stack execution receipt, then compares
two fresh deny snapshots using every original ID as an own-GET candidate. Missing
list entries alone cannot establish removal. Removed, unchanged, changed and new
assignment IDs are reported separately; errors discard all partial evidence.

This scope deliberately includes independent assignments. Their continued
presence is not a Stack cleanup failure, and a changed description is not proof
of Stack ownership. The [Authorization Get schema](https://learn.microsoft.com/en-us/rest/api/authorization/deny-assignments/get?view=rest-authorization-2022-04-01)
defines createdBy as creator identity and isSystemProtected as Azure management;
it has no typed owning Stack ID. The [Stack documentation](https://learn.microsoft.com/en-us/azure/azure-resource-manager/bicep/deployment-stacks)
places generated deny assignments at the Stack deployment scope, but does not
supply that missing identity relation. These sources were rechecked on 2026-09-16.
No deny mutation or inferred ownership is introduced. Assignment attribution and
release acceptance remain open alongside full action orchestration, all 159
parity rows and eight overall gates. Protocol tests cover both subscription and
resource-group Stacks, JSON recovery, missing-list/own-present disagreement,
forbidden reads, changing snapshots, tampered evidence and Stack recreation.

### Ordering independent Stack prerequisites (2026-09-16)

Native service closure now retains each parent's independently executed child
relationships, including one child required by multiple parents. This evidence
is separate from the original single execution-controller graph. A runtime
ordering stage obtains fresh closure with authenticated completed-member
progress, validates all dependency endpoints, and topologically orders the
remaining independent children. Shared prerequisites occur once, ties have
stable asset-ID order, and cycles or unverified edges yield no partial schedule.
The original impacts and controller IDs remain unchanged.

Tests exercise shared dependencies, input reordering, cycles, missing and
duplicate endpoints, retained candidates, and the actual dedicated-host native
enumeration before child execution for both flat Stack and product controllers.
This orders only independently executed service prerequisites. An empty schedule
does not waive retained-attachment preparation, other product lifecycles, group
closure, the native Stack operation, or final outcome/deny evidence. Complete
orchestration and action/graph registration remain unfinished; all 159 parity
rows and eight overall gates remain open.

### Resumable independent prerequisite execution (2026-09-16)

A prerequisite-stage executor now persists the active native member checkpoint
and completed progress together, bound to the entire frozen Stack request. Each
call advances at most one member lifecycle phase. An active Wait/Readback resumes
before any fresh service membership enumeration, because an accepted deletion
can already make the target's GET return 404. Only a verified complete member
checkpoint moves into completed progress. A later call rechecks native closure
and selects the next independently executed prerequisite in its verified order.
Even a completed stage checkpoint repeats current closure and member readbacks.

Both the outer state and nested preparation/member receipts are checked before
native HTTP. Changed jobs, unknown state fields, altered receipts, duplicate or
nonterminal completed entries, and attempts to replace progress on resume fail.
Two-host protocol tests persist every phase through JSON, cover synchronous and
pending/failed native operations, verify one DELETE per child, and reject lost
permissions, changed Stack state and reappearing completed children. The stage
never deletes a parent merely because it has no remaining prerequisites.

Stage Done is not Stack cleanup Done. Complete scope review and retention
preparation precede it; remaining product lifecycles, group closure, native Stack
execution and final product/deny evidence still need orchestration and action/
graph registration. All 159 parity rows and eight overall gates remain open.

### Resumable retained-attachment preparation stage (2026-09-16)

A preparation-stage checkpoint now binds completed member preparation receipts
and the active native update to the complete reviewed request. Active updates
resume through their native polling/configuration checks before whole-member
reconciliation; partially applied updates are not treated as complete evidence.
Each call advances one member preparation phase, with at most one PUT/PATCH and
no DELETE. VM preparation covers the deleted NICs in its reviewed product
projection; NICs outside that projection remain separate candidates. Original
controller relationships and retention decisions remain unchanged.

Completed member preparation now records configuration/creation fingerprints
for every covered VM/NIC target, including targets that needed no write. This
lets completed aggregate stages detect subsequent drift through read-only
reconciliation rather than invoking a mutation-capable preparation helper.
Protocol tests cover NIC PUT followed by VM PATCH, synchronous and pending/failed
updates, JSON recovery, forbidden writes, altered outer/nested state, changed
Stack/configuration, no-op preparation drift, and preparation selection under
reordered or flat native membership. Completed resumes issue no further writes.

Stage completion is only retention preparation. Full preflight, remaining native
product lifecycles, the Stack operation, final evidence, and action/graph/executor
integration remain open with all 159 parity rows and eight overall gates.

### Preparation-to-prerequisite handoff (2026-09-16)

A signed setup checkpoint now composes retained-attachment preparation with
independent prerequisite execution. It advances one nested stage phase per call
and persists the preparation-to-prerequisite transition before starting deletion.
The prerequisite stage must carry the exact completed preparation receipts from
the preparation stage; incomplete coverage, altered nested evidence and dropped
preparation context are rejected before native HTTP. Shared read-only checkpoint
decoders retain the validation behavior of the standalone stages.

After handoff, preparation evidence is authenticated without rerunning the
all-members-present preparation observer. The prerequisite stage instead checks
completed member receipts and remaining resources. This permits the expected
absence of independently deleted members without relaxing configuration checks.
A completed setup still repeats current prerequisite/member observations.

A combined VM/NIC/dedicated-host protocol fixture persists every phase and proves
PUT NIC -> PATCH VM -> DELETE independent host ordering, no chained/repeated
mutations, no preparation restart after deletion, rejection of phase skipping,
unknown state, changed job/nested evidence, configuration drift at handoff,
validly signed prerequisite state missing its original preparation context, and
reappearing completed prerequisites. Setup Done does not authorize native Stack
DELETE by itself: full scope/product/group acceptance, native execution, final
product/deny evidence and action/graph/executor registration remain open, along
with all 159 parity rows and eight overall gates.

### Native Stack DELETE submission after setup (2026-09-16)

A native submission helper now authenticates the completed setup checkpoint,
rejects an active prerequisite, and reruns service prerequisite enumeration,
resource-group closure and every remaining product preflight. It also checks
Stack-level management locks and protected tags, rereads the signed Stack
configuration, and resolves the operation region from the live Stack or a fresh
containing-group GET when a resource-group Stack omits location. The region must
match the reviewed asset. It sends the catalog-bound DELETE with exactly the
reviewed category consequences, unsupported-resource failure and out-of-sync
bypass disabled, using a stable client request ID. Accepted responses become the
existing full-request-bound native execution receipt; subsequent processing uses
polling/product readback, not submission.

Both subscription and resource-group protocol fixtures now exercise setup,
independent host deletion, native Stack DELETE, persisted receipt recovery,
Running-to-Succeeded polling and final product observations without repeating
either DELETE. Guards cover Stack/parent locks, protected Stack tags, missing or
changed region, missing/tampered/incomplete/active setup, changed job, returned
prerequisites, native 403 and incomplete 202 responses. A second submission after
Stack disappearance rejects before DELETE.

This helper preserves existing product guards and is not a registered complete
Stack action. Intrinsic/native-cascade product preflight distinctions, flat VM
attachment handling, full group behavior, deny-release attribution and final
catalog/graph/executor acceptance remain unfinished. All 159 parity rows and
eight overall gates remain open. Transport failure before a usable receipt does
not prove completion; this helper does not invent a receipt from later absence.

### Persisted native Stack deletion recovery (2026-09-16)

Native Stack submission now returns one authenticated checkpoint carrying the
completed setup, operation region and native execution receipt. Read-only recovery
validates all nested evidence and the full frozen request before HTTP, preserves
independent product execution receipts after root disappearance, and persists the
updated native polling phase. Recovery never calls preparation or DELETE. Even a
completed checkpoint repeats product and retained-resource observations; native
completion alone does not certify cleanup or deny-assignment release.

Subscription and resource-group protocol fixtures exercise actual prerequisite
and Stack DELETE, JSON persistence, pending/completed recovery, checkpoint/job
corruption rejected before HTTP, independent checkpoint ownership and a returning
child after completion. Full product cascade semantics, final orchestration and
ActionDriver/graph/planner/executor acceptance remain unfinished. All 159 parity
rows and eight overall gates remain open.

### Stack root protection before setup (2026-09-16)

Each authenticated setup advance now checks the live Stack configuration,
protected tags and applicable management locks before invoking preparation or
prerequisite work. Native submission uses the same root protection check. Tests
cover initial root/subscription locks, protected tags, root/lock read denial and
protection introduced after the first attachment update. A blocked advance emits
no additional mutation; clearing temporary protection permits the same saved
checkpoint to resume without repeating the NIC update.

This is the root protection gate, not complete scope permission/product preflight.
All-family cascade semantics and final ActionDriver/graph/planner/executor
acceptance remain required; all 159 rows and eight overall gates remain open.

### Intrinsic children in verified Stack cascades (2026-09-16)

Stack service closure now records non-prerequisite native children whose reviewed
execution controller is their product parent. Product preflight checks that parent
first. Only after its real product/dependency preflight succeeds may the child's
preflight interpret an existing `serviceIntrinsicChild` protection reason in that
parent context. All remaining child product, group, lock and monitoring checks
still execute. Standalone child Preflight/Execute never receive this context.
Flat Stack siblings and independently executed prerequisites do not inherit it.

The SQL protocol fixture covers a server with its `master` database: parent-first
preflight despite child-first ordering, completed setup, one native Stack DELETE,
JSON recovery and actual product readback. It also covers parent/child protection,
parent/child locks (including one introduced between preflights), failed child/list
reads, missing/new children and parent/child monitoring read denial. Independent
`master` deletion remains blocked before any mutation.

Official sources checked on 2026-09-16:
- [SQL logical-server lifetime semantics](https://learn.microsoft.com/en-us/azure/azure-sql/database/logical-servers?view=azuresql-db)
  establish server deletion cascades to databases and elastic pools.
- [SQL logical-server soft-delete preview](https://learn.microsoft.com/en-us/azure/azure-sql/database/deleted-logical-server-restore?view=azuresql)
  documents retained, restorable server state. Live-resource absence is not proof
  of permanent erasure; this change neither disables retention nor restores/deletes
  a soft-deleted server. Native deleted-server inventory and retention/purge
  acceptance remain separate unfinished SQL lifecycle work.

This removes an intrinsic-child preflight mismatch without accepting the overall
Stack feature. Flat member projection, VM/NIC cascading, specialized/read-only
child drivers, complete pre-setup scope checks and full execution acceptance still
need work. All 159 parity rows and eight overall gates remain open.

### Flat Stack members in product cascade requests (2026-09-16)

Product requests now project flat, explicitly listed Stack members through the
product's declared child types and native relationship rules. The projection is
execution-only: original controller IDs, native membership and full-request
receipt bindings remain unchanged. It supports multiple levels, preserves
explicit controllers and rejects ambiguous or cyclic relations. Static independent
prerequisite rules are excluded; their completed receipts still enter the separate
prerequisite context. Dynamic exceptions require further live product evidence.

Live service closure records a projected relationship only after the native
collection and each member are verified and the child is not a direct prerequisite.
This permits the existing parent-context preflight for flat intrinsic children.
Preparation, member execution and final product readback derive the same stable
product request; final readback does not reconstruct relationships from absent
resources or fabricate independent child execution receipts.

The SQL server/master protocol suite now runs with both original product-controller
and flat Stack-controller plans, including setup, native Stack DELETE, persisted
recovery, product readback and all protection/dependency failures. Additional tests
cover nested App Service projections, input reordering, unchanged signed requests,
unrelated types/parents, missing signed membership, independent host prerequisites
and ambiguous private-DNS cascade causes. These are request projection tests, not
full App Service or private-DNS lifecycle acceptance.

VM/NIC attachment relationships, dynamically intrinsic prerequisites, ambiguous
shared native cascade causes, specialized/read-only child drivers and complete
pre-setup scope/orchestration acceptance remain unfinished. All 159 parity rows
and eight overall gates remain open.

### Flat Stack attachment deletion options (2026-09-16)

Product projection now reuses `resourceAttachments` for reviewed VM/NIC native
members. Only explicit `Delete` options on a typed resource ID can create VM-to-
disk/NIC or NIC-to-public-IP cascade context; `Detach`, missing options and unrelated
IDs cannot. Malformed, duplicate, foreign-subscription or ambiguous references are
rejected. The existing preparation and product preflight still validate live
attachment identities, options and retained-resource settings before mutation.

Flat native NICs covered by their VM's verified Delete option are prepared through
that VM request, including retained public IPs, instead of receiving duplicate
independent preparation. The setup protocol suite now covers both original and
flat controllers through NIC PUT, VM PATCH, independent host prerequisite DELETE,
one native Stack DELETE, JSON recovery and actual product readback. Retained disks
and IPs are verified by native creation identity. A lost/recreated retained disk or
returning NIC invalidates reconciliation even after an earlier completed readback.
No independent NIC/VM DELETE receipt is fabricated for native Stack cascades.

[Azure VM attachment deletion documentation](https://learn.microsoft.com/en-us/azure/virtual-machines/delete),
checked on 2026-09-16, defines Delete versus Detach for VM disks/NICs and the NIC's
public-IP delete option. The implementation does not infer Delete from defaults or
change Detach to Delete. Projection tests cover all three attachment relationships;
protocol tests exercise retaining disks/IPs while deleting VM/NIC. They do not
constitute all-attachment-option or all-provider acceptance.

Independent VM prerequisite deletion that automatically removes NICs still needs
its own complete consequence-evidence integration. Full pre-setup scope review,
remaining product families and complete executor acceptance remain unfinished;
all 159 parity rows and eight overall gates remain open.


### Independently executed Stack attachment consequences

Progress observation now derives native VM/NIC Delete attachment candidates only
from an authenticated completed parent's exact product request. It then performs
each child's registered product readback and own ARM GET in both verification
passes. Cascaded children have explicit parent provenance and no fabricated
independent operation receipt; retained attachments remain subject to live checks.
All independent receipts are authenticated before any cloud reads. Any failed
readback clears the entire observation.

Protocol fixtures execute the actual retention preparation, VM deletion and JSON
checkpoint recovery for implicit and flat Stack membership, plus recursive VM,
disk, NIC and public-IP deletion. They reject pending/tampered/cross-job receipts,
a malformed second receipt before HTTP, product dependency and ARM permission
failures, returned resources, later NIC reappearance and missing retained IPs.
This is VM/NIC attachment progress support, not generic service cascade execution
or end-to-end Stack registration. All 159 parity rows and eight overall gates
remain open.

### Initial Stack setup scope preflight

Before the first preparation write, setup now checks group closure and every
remaining deleted member's actual registered product preflight, including monitor
wrappers. The service closure supplies an in-memory staging context for reviewed
live direct prerequisites. Generic service preflight still checks their native
membership, controller relationship, incarnation, protection, locks and nested
children; it only defers the requirement that those independently scheduled
children already be absent. Each child also receives its own product preflight.
Flat native Stack prerequisites are projected into temporary product requests;
the original plan is unchanged and this context never enters Execute or receipts.
Ordinary Preflight and Execute still reject a live direct prerequisite.

Setup fixtures cover nested and flat membership, protected/locked parents,
protected or unreadable children, denied service lists, unreviewed new children
and denied monitoring reads before any NIC PUT. Successful fixtures still execute
retention preparation, independent host deletion and native Stack deletion.
This check is at initial setup entry: resumed in-flight preparations retain their
existing checkpoint-specific checks. Revalidating unrelated scope protections
before later mutations, specialized prerequisite semantics and complete
ActionDriver/graph/planner/executor acceptance remain unfinished. All 159 parity
rows and eight overall gates remain open.

### Resumed Stack preparation scope checks

Setup now repeats group closure and actual staged product preflight after an
active retention operation has finished native polling and own readback, before
another preparation mutation. Its expected configuration remains authenticated
by the original pending preparation receipt; a private, nonserialized observation
flag permits checking that configuration without relabeling the receipt complete.
This observation cannot contain member execution receipts. While the write is
still in flight, setup returns the unchanged durable checkpoint and performs no
wider product preflight or new write. The normal prepared-configuration API still
requires complete receipts.

The same scope preflight also runs between independently executed prerequisites,
when no member execution is active, using their authenticated completed progress.
Nested and flat membership fixtures exercise late protection, monitor denial,
parent locks, child read denial, new unreviewed children, missing retained IPs,
asynchronous preparation, and protection before prerequisite deletion. Failed
checks preserve the request/checkpoint, and clearing a recoverable condition
resumes the saved phase without repeating the NIC write.

Active prerequisite drivers can have their own multi-phase waits and mutations;
those still need end-to-end scope validation and orchestration acceptance.
Specialized prerequisite semantics and Stack action/graph/planner/executor
registration remain unfinished. All 159 parity rows and eight gates remain open.

### Active Stack prerequisite protection snapshots

Before an authenticated active member enters its actual product Wait, the
prerequisite stage now rereads Stack protection, ARM locks and every reviewed
member's own endpoint. Explicit protection tags (including DNS metadata tags),
locks on reviewed deletions, changed recorded creation identities, unexpected
missing resources and missing retained resources stop phase advancement. A
recorded completed member or its deleted descendants returning live also stops
advancement. All nested receipts are authenticated before member HTTP reads.

Only the active product request's deleted subtree and authenticated prior
completed requests may be absent during this protection snapshot. Such absence
is not promoted to Completed and does not create an execution receipt. The actual
product Wait and readback still establish progress. Locks are checked even when
an active target already returns 404. Active child collections are not blindly
re-enumerated during their asynchronous deletion.

The real VM driver fixture now resumes through the prerequisite stage in nested
and flat layouts. It checks protection/lock/403/unexpected-404/recreation/retained
IP failures before Wait can issue its next PATCH, then completes the original
PUT/PATCH/DELETE sequence once after the condition clears. Removing the guard
reproduces the protected-sibling regression. Separate staged host fixtures check
an absent active target's lock and a prior completion returning while the second
operation is active. Serialized native checkpoints and malformed-receipt rejection
remain exercised.

This snapshot is explicit protection and presence validation, not full service
membership or specialized product preflight during an active mutation. Product
phase-specific checks remain in the actual driver; native group/service closure,
specialized semantics and full Stack action/graph/planner/executor acceptance
still need work. All 159 parity rows and eight overall gates remain open.

### Resource-group closure includes the known Stack controller

The unfiltered group index now requires a known Stack controller located inside
a reviewed deleted group to appear in that index. The controller is intentionally
not its own lifecycle impact, so the prior known-impact loop could accept an
index omitting it in both snapshots. The new check retains the existing native
own-read, listed-incarnation, duplicate, scope, protection and two-snapshot checks.
Protocol cases cover controller omission, second-snapshot omission, pagination,
duplicate/wrong-type/stale-creation rows, a neighboring group name, and a retained
group that must not acquire a delete-scope enumeration.

Official references checked 2026-09-16:
- https://learn.microsoft.com/en-us/rest/api/resources/resources/list-by-resource-group?view=rest-resources-2021-04-01
  defines the group resource list and optional filtering/paging.
- https://learn.microsoft.com/en-us/azure/azure-resource-manager/bicep/deployment-stacks-known-issues
  states that resource-group-scoped stacks do not manage their parent group by
  default, while deleting that group deletes the stack and managed resources;
  resource-group deletion can bypass deny assignments.
- https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/delete-resource-group
  describes native child/dependency deletion and final resource reads.

This is a defensive consistency check for supplied reviewed scope, not evidence
that a resource-group Stack can manage/delete its own containing group. Do not
infer group deletion authority from a Stack's location. Native group lifecycle
and full Stack action/graph/planner/executor acceptance remain unfinished; all
159 parity rows and eight overall gates remain open.


### Native resource-group cascades include reviewed Monitor indexes

The internal resource-group lifecycle now merges native Monitor indexes before
checking top-level reviewed-member completeness. A member absent from the generic
ARM group list requires a matching reviewed deletion, stable native index snapshots,
an independent own GET matching the native snapshot, and the actual registered
product preflight. Monitor drivers are eligible for native cascading because their
Execute path has no additional preparation phase. Other specialized preparation
requirements remain explicit; this does not bypass them.

Protocol fixtures exercise all ten Monitor kinds with an empty generic group
index, mixed generic/native membership with native pagination, late or unreviewed
members, omissions, duplicates, wrong types, changed configuration, permission
failures, serialized recovery, returning members and forged receipts. They perform
one native group DELETE and actual product readbacks, without independent member
DELETE requests or fabricated member execution receipts.

These are retained protocol tests, not real-cloud acceptance. Public resource-group
and Stack action/graph/planner integration, multi-stage product preparation, and
full end-to-end validation remain unfinished. All 159 parity rows and eight overall
acceptance gates remain open.


### Resource-group preflight recognizes verified internal Monitor references

Group deletion now passes a private, per-pass scope to the actual Monitor and
wrapped product preflights after native group/Monitor indexes and own reads have
been reconciled. A live incoming Monitor source can be covered only when both its
target and source are reviewed deleted members (or the target is the group), the
source resides in that group, and its configuration, group and reference proofs
match the current native observation and the reconciled index. External, retained,
unreviewed or changed sources still prevent deletion. Diagnostic, RBAC, migration
and other independent prerequisite families do not receive this exception.

The scope is not persisted, exposed in action parameters, or installed on a
reusable driver. Standalone Execute and final product Readback retain strict
incoming-reference checks. The retained protocol scenarios reproduce the former
failure for all eight Action Group source kinds, then exercise native group DELETE
and serialized outcome recovery, group/disk references, scope boundaries, changes
after indexing and during product preflight, standalone isolation and a surviving
source after group absence. They use real registered product drivers.

This does not complete public group/Stack action registration, graph/planner
integration, multi-phase product preparation or real-cloud end-to-end acceptance.
All 159 parity rows and eight overall acceptance gates remain open.


### Resource-group native graph and public action integration

Resource groups now declare their existing official Delete binding in the native
selection, generated catalog and resource specification, and resolve a dedicated
registered action driver. Native group graph discovery reconciles two unfiltered
ARM indexes with own resource reads and native Monitor indexes. Unknown members
remain cleanup-blocking unresolved references. Pagination, duplicate/type/scope
errors, changing snapshots and omitted known top-level members cannot become an
empty group. Managed groups and old group records without a private review do not
acquire these effects.

The graph uses selection-activated native deletion effects rather than exclusive
ownership: selecting a member remains independent, and an existing VM or service
controller remains its child's immediate controller. Group-local Monitor dependency
declarations compose with these verified effects. Indexed RBAC/diagnostic extension
resources remain independently executed prerequisites rather than delegated deletes.

The public driver caches an immutable native identity/configuration digest while
allowing inventory observation timestamps to advance. It preserves full
request/job-bound recovery, native operation
completion plus actual product readback, and applied-retention checks. An already
absent group requires its reviewed product outcomes too; no accepted-operation
receipt is fabricated. Request IDs and retry timing remain diagnostics, not native
operation identities. Retained protocol tests run actionable native group inventory,
ServiceLifecycle graph contribution, the real planner, ResolveAction, Preflight,
Execute, serialized Wait and Readback, including asynchronous callbacks and forged
receipt rejection. Separate graph cases retain unknown members, own-read failures,
paging and immediate VM/disk ownership.

This enables the verified native-cascade path; it is not full resource-group or
Deployment Stack completion. Specialized product preparation contracts, persisted
multi-stage setup and full application/real-cloud acceptance remain outstanding.
All 159 parity rows and eight overall acceptance gates remain open.

### Resource-group persisted cleanup recovery

A SQLite-backed cleanup test creates the real task and execution from native
inventory-normalized assets and lifecycle graph, then reopens the repository and
reconstructs the registered runtime between worker deliveries. Native operation
completion deliberately precedes the last member's disappearance. The test requires
one DELETE, a pending action while that member survives, and successful persisted
root/member closure only after final product readbacks.

This exposed a terminal dependency error during intermediate native cascade
readback. Resource-group outcome verification now checks every reviewed member's
own ARM presence before running final product dependency/readback checks. Existing
members keep the operation pending; read errors and changed creation identities
remain errors. Both verification passes still check retained assets, prerequisites
and the root, and all actual product readbacks remain required before completion.
No independent product execution receipt or dependency waiver is introduced.

This does not complete specialized product preparation, managed resource-group
composition, Deployment Stacks or real-cloud acceptance. All 159 parity rows and
eight overall acceptance gates remain open.

### Ordinary resource groups containing AKS managed subtrees

The resource-group request projection now recognizes the existing AKS flat
controller tree using the native nodeResourceGroup field and the same saved-member
scope checks as the AKS driver. It keeps the node group and unknown contained
members in the AKS request, without resolving independent group/unknown actions.
Known VM/NIC attachment chains receive their native Delete relationships in product-local
requests while the reviewed graph remains unchanged.

After the real AKS preflight succeeds, a private per-call context lets known
members and their reviewed attachments pass the managed-group-only protection
when current managedBy still identifies that AKS controller. Standalone preflight
and Execute do not receive this context; all other product, lock, readiness and
final outcome checks remain in place. The combined native graph/planner/action
fixture checks one containing-group DELETE, delayed node-group/member absence,
changed owners, omitted/extra members, forbidden reads and independent VM denial.

This follows Microsoft's AKS cluster deletion and Resource Manager deletion-order
documentation, reviewed 2026-09-17:
https://learn.microsoft.com/en-us/azure/aks/delete-cluster
https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/delete-resource-group

This is not full managed-product composition: other controller families, deeper
native child projections and specialized preparation still require integration.
All 159 parity rows and eight overall acceptance gates remain open.

### Resource-group composition with Azure Monitor Workspace

The managed product projection also handles Microsoft.Monitor/accounts. Its group
is derived from both native default-ingestion DCR/DCE IDs and must agree; no MA_
name convention grants ownership. The existing workspace native preflight and
managed-member checks remain mandatory. External association unlinks remain
independent required deletions and are included in the workspace product request.

A composed native graph/planner/registered-action test verifies the unlink followed
by one ordinary resource-group DELETE, then separately delayed managed-group and
member absence. Boundary cases cover changed owners/default ingestion, forbidden
rule/group reads and a new association after the reviewed unlink. Parent absence
cannot certify the managed group's deletion.

Official behavior reference (reviewed 2026-09-17):
https://learn.microsoft.com/en-us/azure/azure-monitor/metrics/azure-monitor-workspace-manage

Other specialized managed product composition and preparation remain
outstanding; Fleet composition is described below. Application Insights composition is described below. All 159 parity rows and eight acceptance gates
remain open.


### Resource-group composition with Application Insights

Ordinary resource-group cleanup now projects the registered Application Insights
component driver together with its authenticated managed workspace/group. Shared
workspaces remain unowned. The cleanup worker's group-level prerequisite bindings
are projected privately to the exact component using native child identities or
AMPLS target references; managed-workspace links must match the component's signed
incoming-association snapshot. The reviewed group request remains unchanged.

Legacy children retain their exact case-sensitive URL selectors and native read
contracts. All prerequisites execute independently and their registered Readback
and own native absence checks are required. Application Insights executes through
the single containing-group DELETE only after its real product preflight succeeds.
No independent component/workspace receipt or successful residual outcome is
invented. Managed group and workspace absence are observed separately on recovery.

The native inventory, SQLite graph, planner and registered action fixture covers
managed and shared workspaces plus AMPLS links in an external resource group. It
verifies prerequisite ordering, one group DELETE, serialized recovery, delayed
managed resource absence, shared workspace/scope retention and denial of changed
settings, owners, link proofs, identities, group tags/types and forbidden reads.
Generic controller member checks now compare AMPLS targets with their keyed native
configuration before tolerating unlink-induced ETag changes. Product association
absence checks still run. ResourceGroup comparison also supplies the same known
type as inventory when a native GET omits that field, after response identity
validation; real configuration changes and wrong types remain rejected.

Official behavior/schema references (reviewed 2026-09-17):
https://learn.microsoft.com/en-us/azure/azure-monitor/app/managed-workspaces
https://learn.microsoft.com/en-us/rest/api/resources/resource-groups/get?view=rest-resources-2021-04-01

This fixture evidence is not live-cloud acceptance, soft-delete purge support or
completion of all managed products and preparation protocols. Public Stack
integration and broader parity acceptance remain outstanding; Fleet composition
is described below. All 159 parity rows
and eight overall acceptance gates remain open.


### Resource-group composition with Fleet

Ordinary resource-group cleanup now composes Fleet's signed Hub manifest with its
separate Hub and AKS node resource groups. Native product preflights still verify
configuration, ownership, locks and dependencies. The private managed-group scope
is limited to authenticated members and does not enable their standalone deletion.
Product-local VMSS descendants and Monitor resources retain their native checks;
known read-only members remain subject to native absence reads without acquiring
an independent mutation driver. Private DNS links are accepted as VNet deletion
effects only when the signed manifest, exact reciprocal VNet reference and current
keyed configuration agree.

The shared planner preserves authoritative, explicitly permitted direct product
cleanup under an outer native deletion effect. Fleet update runs, profiles,
strategies and members retain their independent prerequisite ordering. Running
update runs use their real stop-and-resume driver before the containing resource
group deletion; the group cannot bypass that preparation. The public graph and
reviewed request remain unchanged by private product projections.

Native inventory, graph, planner and registered action fixtures cover minimal and
full Hub descendants, running update preparation, serialized recovery, separate
managed-group/member absence and surviving read-only descendants. Negative cases
reject changed owners/configuration, new members and forbidden reads before group
mutation. Group execution diagnostics are checked for private configuration leaks;
this does not claim a separate audit of standalone AKS diagnostics.

Validation on the unchanged implementation sources: `go test ./...` passed
(Azure 420.294s, GCP 343.572s), `go vet ./...` passed, and focused planner plus
resource-group/Fleet race tests passed (Azure 241.488s). These are offline protocol
fixtures, not independent emulator or live-cloud acceptance.

Official contract references (reviewed 2026-09-17):
https://learn.microsoft.com/en-us/rest/api/fleet/fleets/delete?view=rest-fleet-2025-03-01
https://learn.microsoft.com/en-us/azure/kubernetes-fleet/concepts-lifecycle
https://learn.microsoft.com/en-us/azure/dns/private-dns-virtual-network-links

Public Stack integration, missing backup/recovery and other resource families,
full application acceptance and the remaining parity audit are still required.
All 159 parity rows and eight overall acceptance gates remain open.


### Data Protection active and retained inventory

Five native inventory rules now cover backup vaults, policies, active
backup instances, deleted backup instances and region-scoped deleted vaults.
Ten official list/get operations plus native policy DELETE and their schema closure are pinned in the
catalog. Discovery uses native collections and own reads, not the general ARM
resource index. Child observations bind the live parent configuration and region.
Known omitted resources remain visible until their own read returns absence;
missing or forbidden parent/collection reads cannot erase children.

The registered source checks native pagination scope/version, duplicates, partial
responses, parent changes and private configuration drift across serialized client
continuations. Its source advertises known-ID reconciliation. Two observed passes
must agree, though this is not an atomic Azure snapshot. Native source credentials
and arbitrary workload settings are excluded from persisted payloads and API logs.

Deleted vault request paths use `locations/{location}/deletedVaults/{name}`.
Microsoft's unmodified examples instead return `deletedBackupVaults` in their IDs
and a shortened resource type. An exact response alias preserves the subscription,
region and deletion identity while requests retain the declared operation path.
The original vault ID is descriptive retention metadata, not ownership of an
active same-name replacement. Multiple retained deletion identities stay separate.
The unchanged examples, pinned hashes and discrepancy are documented under
`azure/fixtures/dataprotection/`.

Unused policies, active backup instances and vaults now have native deletion.
Vault deletion requires active instances and policies to have been removed.
Coordinated workload preparation and recovery/retention actions remain incomplete. The two naturally retained kinds have
no invented delete operation. Moving vault/policy candidates into the matrix's
registered resources closes their missing-specification entries only; full behavior
remains pending. Recovery Services vaults are still a separate missing family.

Official references (reviewed 2026-09-17):
https://learn.microsoft.com/en-us/rest/api/dataprotection/backup-vaults/get?view=rest-dataprotection-2026-03-01
https://learn.microsoft.com/en-us/rest/api/dataprotection/deleted-backup-vaults?view=rest-dataprotection-2026-03-01
https://learn.microsoft.com/en-us/azure/backup/secure-by-default

All 159 behavior rows and eight overall gates remain open. Offline examples and
protocol fixtures do not establish independent-emulator or live-cloud acceptance.

Validation for the Data Protection inventory milestone: full Azure tests, focused
race tests, Azure vet, provider parity/catalog checks and shared inventory tests
passed. No cloud mutation or live-cloud acceptance is claimed.

### Data Protection unused-policy cleanup

Native 2026-03-01 policy DELETE accepts only synchronous, empty 200/204
acknowledgements. A signed receipt is persisted before post-delete reads and is
reused after restart without issuing a second mutation. Completion requires the
policy's own absence with readable, unchanged parent configuration. Active and
soft-deleted backup instances are independently listed and read; any consumer
blocks standalone policy deletion. Known consumers omitted from lists require
own-read absence. Vault/group protection, locks and configuration drift are checked
again before invocation. No backup instance, retention setting or vault is mutated.

The native API supplies no If-Match precondition, and its examples supply no
universal incarnation identifier. Review checks cannot guarantee atomic protection
against a concurrent same-ID replacement. Coordinated instance/vault cleanup,
retained recovery and independent emulator/live acceptance remain unfinished.
A real SQLite scan/graph/plan/worker test exercises restart after a transient read
failure and verifies that other policies and active/retained backups survive.
Cross-region deleted-vault known IDs are filtered by the requested regional shard;
cross-subscription identities remain rejected.

Native contract:
https://learn.microsoft.com/en-us/rest/api/dataprotection/backup-policies/delete?view=rest-dataprotection-2026-03-01

### Data Protection backup-instance deletion

Backup instances use their native 2026-03-01 DELETE and six catalog-registered native
operation status/result routes. Signed receipts survive runtime and SQLite worker
restart, persist phase transitions and prevent duplicate mutation after acceptance.
Callbacks must retain the reviewed subscription, vault/group or region, API version
and case-sensitive operation token. Operation lookup 404 is not own-resource
absence. Async operation success still requires a direct active-instance read;
readable retained-instance collections and parents are required before reconciliation.

The deleted-instance counterpart is read and its datasource/policy identity checked.
Readback distinguishes active absence from a matching soft-deleted counterpart and
explicitly records that permanent purge was not verified. The source workload,
policy, vault, other backup instances and existing vault security settings are not
mutated. The plan includes English and Chinese warnings about stopped backups,
retention, recoverability and possible charges. Policy/vault/group configuration
changes, locks, protected tags and unreadable backup dependencies prevent invocation.

Native service permissions, Resource Guard and immutability enforcement remain in
force. There is no automatic weakening of those protections. Cross-tenant auxiliary
authorization, stop-with-retain/suspend/resume/restore flows and coordinated vault
cleanup remain unfinished; this is not full workload-family or live-cloud acceptance.
The native DELETE has no conditional If-Match contract or universally available
incarnation identifier. Repeated signed configuration review is not an atomic
same-ID replacement guarantee.

Official examples are preserved unchanged. DeleteBackupInstance's Location names a
different instance and omits its vault. GetOperationResult's 202 sample mixes another
subscription, operation token and 2021 API version. These inconsistent callbacks are
rejected; the source examples do not justify relaxing scope/version checks.

### Data Protection vault deletion and signed operation recovery

Vault GET/list/DELETE and read-only Resource Guard proxy operations are pinned to
2026-06-01. Policy, instance and deleted-vault inventory retain their separately
pinned 2026-03-01 contracts. Vault deletion does not automatically remove active
instances or policies; their presence prevents invocation. Retained instances remain
separate from active prerequisites. Proxy configuration, resource group, locks,
vault configuration and retained children are reviewed before the single mutation.
No Resource Guard resource or security setting is disabled or deleted by this action.

The unchanged Azure CLI vault recording exercises signed resource-group/regional
status callbacks, a regional result callback and Inprogress-to-Succeeded polling.
The callback boundary validates subscription, group, region, API version, operation
token and complete signing parameter set; serialized receipts preserve phase changes.
Signing material is excluded from diagnostics. This recording has no final own-vault
GET or retained-vault evidence and is not live acceptance of this application.

A separate registered-runtime/SQLite test covers scan, graph, plan, warning, worker
restart and final reconciliation. Async completion alone is insufficient: the active
vault must be absent with a stable readable resource group and readable regional
retained vaults. Known retained identities cannot disappear on list omission alone.
Multiple original-name histories stay distinct and are not cascade-owned by a new
active vault. The result explicitly says permanent purge was not verified. Both
English and Chinese warnings describe retention, possible charges and preserved
source workloads/security settings.

Coordinated vault/instance/policy cleanup, retained restore/purge workflows,
cross-tenant auxiliary authorization and independent-emulator/live acceptance remain
unfinished. The 159 behavior rows and eight overall gates remain open.
