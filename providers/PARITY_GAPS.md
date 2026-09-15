# Provider parity implementation gaps

Scope snapshot: 2026-09-15, updated for the Deployment Stacks inventory registration. This is a repository scope audit, not cloud feature acceptance. The earlier mapping repair was audited at `0c43def71dc2fdef7d6fafe6b59fd8124e9e81d9`.

The matrix covers all 159 Alibaba Cloud specifications. The repository now contains 204 GCP and 470 Azure specifications, but those counts do not prove equivalence. All 159 rows remain pending behavioral verification.

The earlier audit found invalid YAML, 28 Azure mapping references to 18 absent specifications, and an incorrect GCP SSH-key mapping to service-account keys. The corrected matrix keeps absent candidates in `unimplemented_resources`; it does not remove them from the requested scope.

The new `go test ./providers` check runs in the existing `go test ./...` CI job. It detects invalid YAML, omitted or duplicated baseline resources, drift in baseline source/class/scope/actions/hooks/enrichment/parent discovery, unresolved implemented-resource references and stale implementation backlogs. A passing check verifies matrix consistency only.

Synapse now includes workspace/pool and data-plane inventory, reviewed workspace and Spark/SQL cleanup, artifact handling and retained restore-point/backup observations. NetApp now has native specifications and inventory plus volume, pool, recovery-object, policy and vault cleanup. NetApp group/account cleanup and interface deletion effects remain unfinished. These implementations moved their existing candidates into `resources`; that change does not close behavioral acceptance. The current missing-specification table has 15 types and 18 matrix references affecting 17 Alibaba Cloud rows.

## Current progress measures

| Measure | GCP | Azure |
| --- | --- | --- |
| Explicit native specifications | 204 | 470 |
| Baseline rows with at least one existing mapped specification | 155/159 (97.5%) | 141/159 (88.7%) |
| Candidate types still without a specification | 0 | 15 |
| Baseline rows affected by missing candidate specifications | 0 | 17 |
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
| `Microsoft.DataProtection/backupVaults/backupPolicies` | `ACS::ECS::AutoSnapshotPolicy`, `ACS::DBS::BackupPlan` |
| `Microsoft.Graph/groups` | `ACS::CloudSSO::Group`, `ACS::RAM::Group` |
| `Microsoft.RecoveryServices/vaults/replicationFabrics/replicationProtectionContainers/replicationProtectedItems` | `ACS::EBS::DiskReplicaGroup`, `ACS::EBS::DiskReplicaPair` |
| `Microsoft.DataProtection/backupVaults` | `ACS::HBR::Vault` |
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
