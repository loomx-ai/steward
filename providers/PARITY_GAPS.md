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
