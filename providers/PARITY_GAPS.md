# Provider parity implementation gaps

Audit base: `0c43def71dc2fdef7d6fafe6b59fd8124e9e81d9`. This is a repository scope audit, not cloud feature acceptance.

The matrix covers all 159 Alibaba Cloud specifications. The repository contains 204 GCP and 448 Azure specifications, but those counts do not prove equivalence. All 159 rows remain pending behavioral verification.

The audit found invalid YAML, 28 Azure mapping references to 18 absent specifications, and an incorrect GCP SSH-key mapping to service-account keys. The corrected matrix keeps absent candidates in `unimplemented_resources`; it does not remove them from the requested scope.

The new `go test ./providers` check runs in the existing `go test ./...` CI job. It detects invalid YAML, omitted or duplicated baseline resources, drift in baseline source/class/scope/actions/hooks/enrichment/parent discovery, unresolved implemented-resource references and stale implementation backlogs. A passing check verifies matrix consistency only.

## Azure candidates without explicit resource specifications

These are candidates already named by the matrix. Missing specification files mean they cannot be counted as implemented independent resource rules. API availability, exact equivalence and lifecycle behavior still require review; related functionality elsewhere in the provider does not establish coverage.

| Candidate | Alibaba Cloud rows affected |
| --- | --- |
| `Microsoft.Synapse/workspaces` | `ACS::DataHub::Project`, `ACS::DataV::Workspace`, `ACS::DataWorks::Project`, `ACS::EmrServerlessSpark::Workspace`, `ACS::MaxCompute::Project` |
| `Microsoft.Resources/deploymentStacks` | `ACS::BPStudio::Application`, `ACS::ROS::StackGroup`, `ACS::ROS::Stack` |
| `Microsoft.DataProtection/backupVaults/backupPolicies` | `ACS::ECS::AutoSnapshotPolicy`, `ACS::DBS::BackupPlan` |
| `Microsoft.Graph/groups` | `ACS::CloudSSO::Group`, `ACS::RAM::Group` |
| `Microsoft.NetApp/netAppAccounts/capacityPools/volumes` | `ACS::NAS::FileSystem`, `ACS::NAS::MountTarget` |
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

Start with the absent Azure families that affect multiple baseline rows: Synapse workspaces (five), deployment stacks (three), and backup/recovery resources spanning policies, vaults, replication and protected items. For each family, verify the official API contract and lifecycle first, implement inventory and cleanup with persistence tests, then move its candidate from `unimplemented_resources` to `resources`. Keep the behavioral status pending until its required evidence is complete.

Alongside these implementations, resolve the explicitly empty mappings and audit the existing mapped families. Do not replace missing functionality with unrelated resources or mark a row complete because its type is registered.
