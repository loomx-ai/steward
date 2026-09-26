---
title: "Microsoft Azure"
description: "Connect an Azure subscription, grant the RBAC and data-plane permissions Steward needs, run the first inventory, and look up what cleanup does to each Azure service."
navTitle: "Microsoft Azure"
---

Use this page to connect an Azure subscription to Steward, grant the permissions it needs, and look up what Steward finds and what cleanup does to each Azure service. [Troubleshooting](#troubleshooting) is near the end.

**What a connection covers.** Each Azure connection reads one subscription in the Azure public cloud. Sovereign clouds and Azure Stack endpoints are not supported. Regional resources appear in their Azure region. Resource groups and subscription-wide resources — such as RBAC role definitions and assignments, diagnostic settings, Communication and Email resources, registered domains and Cosmos DB accounts — appear under global scope.

**How Steward reads Azure.**

- **Azure Resource Manager (ARM)** gives a broad inventory of the subscription. For supported resource types, Steward also calls each product's own list and detail APIs. ARM resource types without native support appear as read-only inventory.
- **Product data planes** are read through their own endpoints, each with a separate Microsoft Entra token: Storage, Batch, Communication Services, Key Vault, Microsoft Graph and Synapse. See [Data-plane and directory access](#data-plane-and-directory-access).
- **Child resources are discovered explicitly.** Steward lists VNet subnets, Blob containers, SQL databases, scale-set instances, DNS records, Service Bus entities, Event Hubs consumer groups and other children itself instead of relying on the ARM resource list.
- **Known resources are not dropped silently.** When a list omits a resource Steward saw before, Steward reads that resource by ID. A permission or pagination failure never counts as proof that a resource disappeared: the previous record stays until the resource's own read confirms it is gone.
- **Private configuration stays private.** Secrets, scripts, connection details, message and notification content stay out of the public inventory and API logs. Each product section below names what it excludes.

## Connect a subscription

Open the user menu → **Settings** → **Cloud connections** → **Add connection**, choose **Microsoft Azure**, and pick a credential type:

| Credential type | Required fields | Use it for |
| --- | --- | --- |
| **Azure service principal** | **Subscription ID**, **Tenant ID**, **Application (client) ID** and **Client secret** | A dedicated identity with a stable, unattended scope. |
| **OAuth** | Sign in through the browser, then pick one of your subscriptions | [Browser sign-in](./connections.md#browser) as your own account. |
| **OIDC workload identity** | Subscription ID, Tenant ID and client ID; optional write client ID | Temporary credentials without a stored secret, when the server is configured for [OIDC](./oidc.md). |

Steward uses only the credential you enter. It does not use ambient Azure CLI credentials, managed identities, storage account keys or Communication Services account keys.

### Use a service principal

1. Create an application registration and service principal in the subscription's tenant, then create a client secret. Record the **secret value**, not its ID.
2. Assign roles on the subscription as described in [Permissions](#permissions). **Reader** is enough to start.
3. Open **Settings** → **Cloud connections** → **Add connection**, select **Microsoft Azure** → **Azure service principal**, and enter the **Subscription ID**, **Tenant ID**, **Application (client) ID** and **Client secret**.
4. Validate the connection, then refresh its regions.

Result check: validation confirms the identity, not that every resource API is authorized. Run the [first inventory](#first-inventory) and fix any permission errors it reports.

See Microsoft's [service principal authentication guide](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-client-creds-grant-flow).

### Sign in with your browser

Choose **OAuth** to connect by [browser sign-in](./connections.md#browser) instead of a service principal. Steward signs you in through the browser and lists the subscriptions your account can reach; pick one, and the tenant comes with it.

The connection acts as your own account and requests a separate token for each audience (ARM, Microsoft Graph, Storage, Batch, Communication Services and Key Vault). A data plane your account cannot reach — Key Vault or Microsoft Graph, for example — is reported as that source failing while the rest of the inventory continues. For an unattended, stable scope, a service principal is the better choice.

### Use OIDC workload identity

When the Steward server is configured for workload identity, choose **OIDC workload identity**. Steward exchanges a short-lived workload token for Azure credentials, so no client secret is stored. In Azure, add a federated identity credential to the application that matches the issuer, subject and audience shown in the connection's **OIDC trust configuration**, and grant that service principal the same RBAC roles as above. The read identity is used for validation and scans; the optional write identity is used for cleanup. See [OIDC connections](./oidc.md) for the full setup.

### Rotate credentials

Credentials are encrypted with the deployment's credential-encryption key. When you rotate a client secret, use **Replace credential**. The subscription, tenant and application must stay the same.

## Permissions

Grant access in layers: base read access for inventory, deletion permissions only for what you plan to clean up, and separate data-plane or directory permissions for the products that need them.

### Base access for inventory

Assign **Reader** at the subscription scope. It covers inventory, resource details, region discovery and management-lock checks.

A custom role needs equivalent read access, including:

- `Microsoft.Resources/subscriptions/read`
- read access to subscription resources and resource groups, locations and each resource provider's read operations
- `Microsoft.Authorization/locks/read`

Reader grants ARM read operations only. Some inventory calls use other actions — for example Azure NetApp Files network sibling sets need `Microsoft.NetApp/locations/queryNetworkSiblingSet/action`. Each product section under [Cleanup protections](#cleanup-protections) lists these extra permissions, and the reads a custom role must include.

Management groups are read-only inventory and need `Microsoft.Management/managementGroups/read` on the groups to inventory.

### Permissions for cleanup

Add deletion permissions only for the resource types you intend to clean up. For each selected resource, cleanup needs:

- the resource's native `delete` action;
- read access to its asynchronous operation status;
- the reads Steward uses to check dependencies before deleting: the resource, its parents, its resource group and management locks.

Some checks read other resource types than the one you selected, even outside the selected resource group:

- Every ARM resource cleanup reads the subscription's role-assignment and role-definition indexes (see [Azure RBAC](#azure-rbac)) and the six native alert-rule collections (see [Monitor alerts and budgets](#monitor-alerts-and-budgets)).
- Deleting an AKS cluster, subnet or user-assigned managed identity reads the subscription's Kubernetes Fleet collections (see [Kubernetes Fleet Manager](#kubernetes-fleet-manager)).
- Deleting an Application Insights component, Log Analytics workspace or data collection endpoint reads the subscription's Azure Monitor Private Link Scopes (see [Azure Monitor Private Link Scope](#azure-monitor-private-link-scope)).
- Deleting a resource that Data Migration can target reads the subscription's migration indexes (see [Data Migration](#data-migration)).
- Deleting a diagnostic-setting source, destination or ancestor reads diagnostic settings at each source scope (see [Diagnostic settings](#diagnostic-settings)).

Some products need more than a `delete` action — for example to stop, cancel, unassign or unpair something first:

| Product | Extra actions for cleanup |
| --- | --- |
| [Azure Batch](#azure-batch) | Batch data-plane permissions, such as **Azure Batch Data Contributor** |
| [Kubernetes Fleet Manager](#kubernetes-fleet-manager) | Stop update runs; write and Apply Cluster Mesh profiles |
| [Data Factory](#data-factory) | Preparation operations such as stopping triggers, CDC and runtimes, and canceling pipeline runs |
| [Data Migration](#data-migration) | Task and migration Cancel, SQL `deleteNode` |
| [Azure NetApp Files](#azure-netapp-files) | Volume update and latest-backup-status read for policy and vault cleanup |
| [Communication Services](#communication-services) | Data-plane deletion permissions for phone numbers, reservations and rooms |

Steward never deletes the role assignments that give the connection its own access; see [General protections](#general-protections).

### Data-plane and directory access

These products are read through their own endpoints with a separate token. Reader does not cover them.

| Product | Token audience | What to grant |
| --- | --- | --- |
| Blob containers | `https://storage.azure.com/.default` | Data-plane read access, such as **Storage Blob Data Reader**, and network access to the account's public Blob endpoint. Blob cleanup currently requires the standard `ACCOUNT.blob.core.windows.net` endpoint. |
| Azure Batch jobs, schedules, tasks and nodes | `https://batch.core.windows.net//.default` | Batch data permissions, such as **Azure Batch Data Contributor** for cleanup, in addition to ARM permissions for the selected account resources. See [Batch authentication](https://learn.microsoft.com/en-us/azure/batch/batch-aad-auth) and [Batch roles](https://learn.microsoft.com/en-us/azure/batch/batch-role-based-access-control). |
| Communication Services phone numbers, reservations and rooms | `https://communication.azure.com/.default` | Native data read permissions, including room participant lists, and the corresponding deletion permissions for cleanup. Steward obtains the data endpoint from the owned ARM account. See [Communication Services authentication](https://learn.microsoft.com/en-us/rest/api/communication/authentication). |
| Key Vault certificates | `https://vault.azure.net/.default` | Certificate list/get data-plane permission — the **Key Vault Reader** role, or an access policy with certificate **List** and **Get** — and network access to the vault. An unreadable vault fails the scan instead of appearing empty. |
| Microsoft Entra users and groups | `https://graph.microsoft.com/.default` | Microsoft Graph application permissions `User.Read.All` and `GroupMember.Read.All` (or `Directory.Read.All`), with admin consent. |
| Synapse Spark jobs and sessions, notebooks, Spark job definitions and pipelines | `https://dev.azuresynapse.net/.default` | Synapse data-plane list/read access; see [Azure Synapse Analytics](#azure-synapse-analytics). |

### Product-specific permissions

Each product section under [Cleanup protections](#cleanup-protections) lists the reads its inventory needs and the permissions each cleanup action adds. Grant a product's delete permissions only for the resources you select for cleanup.

## First inventory

1. Select the new connection and confirm the subscription.
2. Open **Scans** → **Start scan** and choose **All active regions + global**. Steward runs the native product lists and detail reads for supported resources alongside the broad ARM inventory.
3. Check scan coverage and errors. A permission or pagination failure does not mean previously known resources disappeared; fix the permission and scan again.
4. Open a regional VNet in **Resource Panorama** to inspect its subnets, NICs, VMs and related resources. A VM's network placement is resolved through its NICs.

What to expect:

- Resource groups appear in the global inventory. Their Azure location is where the group's metadata is stored. Full ARM IDs distinguish subscriptions and resource groups.
- Include global scope when you scan global alert rules and budgets.
- Cosmos DB requests preserve case-sensitive data-resource names. A scan fails when two names differ only in case and collide in the inventory identity.

**Scan selected networks.** Choose **Selected networks** to limit a scan to Azure virtual networks or Azure Local logical networks. The **Virtual / logical networks** list supports search by name or ARM ID, with pagination. Listing Local networks needs `Microsoft.AzureStackHCI/logicalNetworks/read`. Subnets configured inside a Local logical network are not separate selectable ARM resources. When you schedule the scan, Steward rereads the selected network and rejects a deleted or inaccessible target before the task is saved. Permission failures stay visible, and **Retry** keeps your selection.

## Inventory and cleanup coverage

Steward recognizes 502 Azure resource types; 451 have native cleanup actions, including Batch node removal, subject to the protections below. Other ARM resource types appear as read-only inventory. Coverage is still being expanded; this is not complete Azure service coverage.

| Service | Resources | Cleanup |
| --- | --- | --- |
| Compute | VMs and extensions, managed disks, snapshots, managed images, availability sets, dedicated hosts and capacity reservations | Supported, with [attachment and ownership protections](#general-protections); host and reservation groups require their members to be deleted first |
| VM scale sets | Uniform and Flexible sets, instances and extensions | Uniform members are part of the set's reviewed deletion; Flexible sets require their VMs to be deleted first |
| [Azure Batch](#azure-batch) | Accounts, pools, nodes, jobs, schedules, tasks, applications, package versions, private endpoint connections and network perimeter views | Reviewed prerequisites and cascades; removing an exact node requeues its running tasks; perimeter views go with account cleanup |
| [API Management](#api-management) | Services, workspaces, APIs and revisions, policies, products, subscriptions, portal content/configuration, credentials, notifications, associations, self-hosted gateway registrations and standalone workspace gateways | Reviewed cascades and ordered unlinks; fixed configurations go with their controller; service deletion uses soft-delete retention |
| Virtual networks | VNets, subnets, NICs, network security groups, route tables, public IPs, public IP prefixes, NAT gateways | Supported, with [network occupant checks](#general-protections) |
| Load balancing | Load balancers and Application Gateways | Supported |
| Storage | Storage accounts and Blob containers | [Empty resources only](#general-protections) |
| SQL | Logical servers, databases and elastic pools | Server cleanup includes its reviewed databases and pools; deleting `master` on its own is prohibited |
| PostgreSQL / MySQL | Flexible servers | Supported |
| [Cosmos DB](#cosmos-db) | Accounts and databases/containers for NoSQL, MongoDB, Cassandra, Gremlin and Table; roles, services, notebooks and private connections; managed Cassandra and Fleets | Reviewed children and shared dependencies precede parents; client encryption keys and built-in roles go with their controller; Fleet unlinking keeps the accounts |
| [Azure DocumentDB](#azure-documentdb) | MongoDB-compatible clusters and replicas, firewall rules, private endpoint connections and Microsoft Entra users | Reviewed replicas and children are deleted before their source or parent; deleting a replica keeps its source |
| [Azure Data Explorer](#azure-data-explorer) | Kusto clusters, databases, follower attachments, data connections, principals, scripts and private connections; custom sandbox images | Reviewed children and follower attachments precede source deletion; read-only databases and active images go with their controller |
| [Azure Synapse](#azure-synapse-analytics) | Workspaces, Spark pools, SQL pools, Spark jobs and sessions, notebooks, Spark job definitions, pipelines, recoverable dropped SQL pools and restore points | Workspace cleanup with its pools, code artifacts and Spark work; independent SQL pool, Spark pool and user-defined restore-point cleanup; code artifacts and Spark work have no independent cleanup |
| [Data Factory](#data-factory) | Factories, pipelines, datasets, dataflows, linked services, credentials, triggers, CDC, global parameters, integration runtimes/nodes and private connections | Reviewed factory cascades; runtimes and running work are prepared first; managed virtual networks go with the factory |
| [Data Migration](#data-migration) | Classic services, projects, tasks/files and service tasks; SQL/Mongo migration services and migrations to SQL or Cosmos DB targets | Reviewed children and migration prerequisites; cancellation and runtime-node preparation before deletion |
| [Defender for Cloud](#defender-for-cloud) | Subscription protection plans and supported resource-level plan states | Read-only service state, coverage, extensions and inheritance |
| [Azure Arc](#azure-arc) | Machines, extensions, Run Commands, license profiles and shared ESU licenses | Reviewed children precede removal of ordinary machine registrations; shared ESU licenses require cleared assignments; controller-managed machines go with their controller |
| [Azure Local](#azure-local) | VM instances, guest agents, identity metadata, NICs, disks, networks, storage paths and images | Guest, VM and Arc-registration cleanup; disk and NIC cleanup after VM references are verified; image cleanup without deleting deployed VMs; storage-path and logical-network cleanup after explicitly reviewed workloads |
| [Stream Analytics](#stream-analytics) | Jobs, inputs, outputs, functions, transformations, clusters and cluster private endpoints | Job definitions follow the job; jobs in a cluster must be selected explicitly or removed from the cluster first |
| [Foundry / Cognitive Services](#foundry-and-cognitive-services) | Accounts, deployments, projects, agents, connections, capability hosts, managed networks, content filters and commitment plans | Deployments and reviewed dependencies precede account soft deletion; no purge |
| [Azure AI Search](#azure-ai-search) | Services, private endpoint connections, shared private links and network perimeter configuration views | Reviewed links precede service deletion; perimeter views go with their service |
| [Redis](#redis) | Classic caches, access policies/assignments, firewall rules, replication links, patch schedules and private endpoint connections; Enterprise / Managed Redis clusters, databases, assignments and private endpoint connections | Reviewed children precede parents; classic replication is unlinked first; healthy active replication verifies all members |
| [App Service](#app-service) | Web Apps / Function Apps, deployment slots, functions, certificates, hostname bindings and service plans | App and slot cleanup reviews owned children; plans stay separate; certificates must have no TLS bindings |
| [Domain registration](#app-service-domains) | Registered domains and ownership identifiers | Reviewed identifiers and app/slot hostname bindings precede delayed registration deletion; DNS hosting stays independent |
| [Communication Services](#communication-services) | Communication accounts, SMTP usernames, phone numbers, reservations, rooms, Email resources, domains, sender usernames, suppression lists and addresses | Reviewed child prerequisites; account deletion releases its reviewed phone numbers; shared email-domain connections require explicit prior cleanup |
| [CDN and Front Door](#cdn-and-front-door) | Profiles, classic endpoints/origins/origin groups/domains; Front Door endpoints/routes/origin groups/origins/domains/rule sets/rules/security associations/certificate references | Profiles and owned child trees use reviewed cascades; shared references require ordered cleanup |
| [WAF](#waf-policies) | CDN and Front Door policies | Referring endpoints and security associations are deleted first; classic Front Door references still block cleanup |
| Containers | Container registries, Container Apps, [Container Instances](#container-instances) groups and AKS | AKS cleanup reviews its node resource group and known nested or externally managed descendants |
| [Kubernetes Fleet Manager](#kubernetes-fleet-manager) | Fleets, AKS and Arc members, managed namespaces, update runs, strategies, auto-upgrade profiles, Gates and cross-cluster networks | Reviewed native children are deleted before the Fleet; networks disconnect first, update runs remove their Gates, and verified Hub resources follow the Fleet |
| DNS and private endpoints | Public/private zones and records, private DNS links, private endpoints and DNS zone groups | Reviewed controller cascades include verified managed NICs and external DNS records; DNS system records cannot be deleted on their own |
| Virtual WAN gateways | VPN/ExpressRoute gateways, connections, VPN NAT rules and links | Connection and NAT prerequisites are explicit; VPN links belong to their connection |
| [Service Bus](#service-bus-and-event-hubs) | Namespaces, queues, topics, subscriptions, rules, authorization rules, recovery aliases, migration configurations and private endpoint connections | Native actions and reviewed namespace/entity cascades; migration cleanup aborts copying before deletion; paired recovery aliases are unpaired before deletion |
| [Event Hubs](#service-bus-and-event-hubs) | Dedicated clusters, namespaces, event hubs, consumer groups, authorization rules, recovery aliases, schema/application groups and private endpoint connections | Cluster cleanup first deletes reviewed member namespaces; native namespace/event-hub cascades and paired-alias unpairing |
| Operations and identity | Log Analytics workspaces and user-assigned managed identities | Supported |
| Log Analytics tables | Custom, restored and search-result tables of each workspace | Supported; built-in Azure Monitor tables belong to the workspace and are not listed |
| Event Grid | Custom topics, system topics, namespaces and topic event subscriptions | Deleting a topic deletes its event subscriptions; subscriptions can also be deleted on their own |
| Azure Virtual Desktop | Host pools, session hosts, application groups and workspaces | Session hosts and the application groups of a host pool are deleted before the host pool; removing a session host keeps its virtual machine |
| HDInsight | Clusters | Supported; storage accounts and the virtual network stay separate |
| [Managed Grafana](#managed-grafana) | Workspaces, managed private endpoints, private endpoint connections and integration fabrics | Reviewed children are deleted before the workspace; each resource also has its own native action |
| [Monitor workspace](#azure-monitor-workspace) | Workspace and its default ingestion managed group | Reviews every group member, unlinks external associations first, and verifies that the group and known resources are gone |
| [Monitor data collection](#monitor-data-collection) | Rules, endpoints and associations on monitored resources | Reviewed associations are deleted before rules and endpoints; shared associations are deleted once |
| [Monitor private links](#azure-monitor-private-link-scope) | Global private link scopes, scoped-resource associations and private endpoint connections | Reviewed children are deleted before the scope; linked workspaces and endpoints require association removal first |
| [Application Insights](#application-insights) | Components, analytics items, exports, favorites, work-item configurations, API keys and profiler storage links | Independent child prerequisites, AMPLS unlinking and deletion of the reviewed current managed-workspace group |
| [Azure Monitor workbooks](#workbooks) | Shared workbooks, private workbooks and workbook templates | Independent cleanup with full-content and revision checks; referenced storage and identities stay separate |
| [Azure Monitor alerts](#monitor-alerts-and-budgets) | Metric, activity-log, scheduled-query, smart-detector, Prometheus and processing rules; action groups and web tests | Independent cleanup; reviewed referencing rules must precede a shared Monitor destination |
| [Azure RBAC](#azure-rbac) | Custom and built-in role definitions; subscription, resource-group and resource role assignments | Independent deletion of eligible custom roles and assignments; built-in and shared-scope roles, PIM-managed assignments and the connection's own assignments stay protected |
| [Diagnostic settings](#diagnostic-settings) | Resource and subscription settings, including separate Blob, File, Queue and Table service scopes | Deleted before a referenced source, destination or ancestor; destinations stay separate resources |
| [Budgets](#monitor-alerts-and-budgets) | Consumption and Cost Management budgets at subscription and resource-group scopes | Independent cleanup; notification action groups stay separate |
| Management groups | The tenant's visible management group directory, with parent groups | Read-only; requires `Microsoft.Management/managementGroups/read` on the groups to inventory |
| Service Fabric and Storage Sync | Service Fabric clusters and Storage Sync services | Supported |
| Machine Learning | Workspaces and managed online endpoints | Endpoint cleanup; workspaces are read-only parents |
| Purview and managed applications | Microsoft Purview accounts and managed applications | Read-only: deleting them also deletes their managed resource group, which is not yet reviewed as a cascade |
| Key Vault keys | Keys listed through the ARM Keys API | Read-only: key deletion is a data-plane soft delete not exposed by ARM |
| Microsoft Entra users and groups | Users and groups read through Microsoft Graph, with direct group members | Read-only: directory objects affect the whole tenant. Requires [Graph permissions](#data-plane-and-directory-access). Only display name, user principal name, account state, user type, group type flags and creation time are stored |
| Key Vault certificates | Certificates read from each vault's data plane, with vault and managed-key references | Read-only: deletion also soft-deletes the managed key and secret. Requires [certificate data-plane access](#data-plane-and-directory-access) |
| Site Recovery | Replication protected items in Recovery Services vaults, with fabric, protection container and vault references | Read-only: disabling replication removes recovery-side replicas and is not yet reviewed; requires `Microsoft.RecoveryServices/vaults/replicationFabrics/replicationProtectionContainers/replicationProtectedItems/read` |
| Aggregate resources still awaiting lifecycle support | Resource groups, Key Vaults and Container Apps environments | Read-only: resource-group, Key Vault and Container Apps environment cleanup is not implemented |

**Resources without their own delete action.** Service Bus and Event Hubs network rule sets, Event Hubs network perimeter configurations, recovery-alias authorization views, Uniform scale-set network resources and VPN connection links cannot be deleted on their own. The default namespace authorization rule, `RootManageSharedAccessKey`, also goes only with its namespace. These resources appear in their owning controller's reviewed deletion impacts, and keeping one of them blocks deletion of that controller.

## Cleanup protections

This section explains what Steward checks before it deletes an Azure resource, and what deleting it does. Start with the [general protections](#general-protections), which apply to every service, then find your service below.

Before you run a task, review the [cleanup selection and results](./cleanup.md). Deleting a database, registry or storage resource can remove the data it contains. Azure permissions, retention settings, dependencies and concurrent changes can still prevent an action. See Microsoft's [management locks](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/lock-resources), [VM deletion settings](https://learn.microsoft.com/en-us/azure/virtual-machines/delete) and [asynchronous operation behavior](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/async-operations).

| Area | Services |
| --- | --- |
| Compute and containers | [Azure Batch](#azure-batch), [Container Instances](#container-instances), [Kubernetes Fleet Manager](#kubernetes-fleet-manager), [App Service](#app-service), [App Service domains](#app-service-domains) |
| Hybrid | [Azure Arc](#azure-arc), [Azure Local](#azure-local) |
| Networking and delivery | [CDN and Front Door](#cdn-and-front-door), [WAF policies](#waf-policies) |
| Storage | [Elastic SAN](#elastic-san), [Azure NetApp Files](#azure-netapp-files) |
| Databases and analytics | [Cosmos DB](#cosmos-db), [Azure DocumentDB](#azure-documentdb), [Azure Data Explorer](#azure-data-explorer), [Redis](#redis), [Azure Synapse Analytics](#azure-synapse-analytics), [Data Factory](#data-factory), [Data Migration](#data-migration), [Stream Analytics](#stream-analytics) |
| AI | [Foundry and Cognitive Services](#foundry-and-cognitive-services), [Azure AI Search](#azure-ai-search) |
| Messaging and communication | [Service Bus and Event Hubs](#service-bus-and-event-hubs), [API Management](#api-management), [Communication Services](#communication-services) |
| Monitoring | [Monitor alerts and budgets](#monitor-alerts-and-budgets), [Azure Monitor workspace](#azure-monitor-workspace), [Monitor data collection](#monitor-data-collection), [Azure Monitor Private Link Scope](#azure-monitor-private-link-scope), [Application Insights](#application-insights), [Workbooks](#workbooks), [Managed Grafana](#managed-grafana), [Diagnostic settings](#diagnostic-settings) |
| Security and governance | [Azure RBAC](#azure-rbac), [Defender for Cloud](#defender-for-cloud) |

### General protections

**Before deletion**

- **Management locks:** Subscription, resource-group, resource and relevant descendant locks block deletion. Steward checks locks during inventory and again immediately before deletion, and never removes them.
- **Protection tags:** A resource tagged `steward/protected` or `steward:protected` with the value `true`, `1`, `yes`, `on` or `protected` is protected from cleanup. Product sections refer to this as a protection tag.
- **The connection's own access:** Role assignments to the principal the connection signs in as are always protected, because deleting them would revoke Steward's access in the middle of cleanup. If Steward cannot read the principal's object ID from its token, it protects every role assignment.
- **Managed resources:** Provider-owned resources are cleaned up only through their supported owning controller, except members with an explicitly supported independent action. AKS deletion includes its reviewed node resource group; deleting arbitrary resources in a managed resource group remains prohibited.
- **Network occupants:** Immediately before deletion:
  - a subnet must have no IP configurations, private endpoints, service association or resource navigation links, application gateway IP configurations or IP configuration profiles;
  - a network security group, route table or NAT gateway must have no associated subnets or network interfaces;
  - a public IP address must not be attached to an IP configuration or NAT gateway.

  Occupants deleted earlier in the same task satisfy this check. A service association link must be removed by its owning service. A delegation alone does not block subnet deletion.
- **Network interfaces with hosted workloads:** A NIC reporting a nonempty `hostedWorkloads` list is protected from direct cleanup and from VM cascades that would delete or rewrite it. Malformed workload metadata is also protected; a missing, null or empty list does not by itself mean a workload is attached. Cleanup rereads the interface, so a workload that appears after inventory blocks execution. This metadata never lets a controller delete the NIC; NetApp group ownership and automatic interface deletion still need their own reviewed lifecycle (see [Azure NetApp Files](#azure-netapp-files)).
- **VNets and DNS zones:** Required subnets and private DNS links must be removed first. Selecting a VNet does not silently delete unselected subnets or links.
- **VM attachments:** Plans show the disks, NICs and public IPs that Azure deletes with the VM. Supported retention changes are applied with conditional native updates before deletion and survive a worker restart. VM extensions are part of the VM's deletion impacts. Cleanup of Uniform scale-set unmanaged VHDs and disk detachment are not implemented yet.
- **Storage accounts and Blob containers:** A storage account must have no Blob containers, file shares, queues or tables for its supported services. A Blob container must have no blobs, snapshots, versions, deleted entries or uncommitted uploads. Legal holds and immutability policies block cleanup. Steward does not empty or purge data to make a resource deletable. Blob cleanup currently requires the standard `ACCOUNT.blob.core.windows.net` endpoint and [data-plane access](#data-plane-and-directory-access).
- **App Service plans:** Deleting an app keeps its App Service plan. Select the plan separately when it should also be removed.

**References from other resources**

Deleting a resource is blocked while another resource still refers to it and that referrer is not in the task. Add the referrer to the task (it is deleted first), or remove the reference in Azure and scan again. These checks read the whole subscription:

| Referrer | Blocks deleting | Details |
| --- | --- | --- |
| Role assignments and custom roles | The scope resource, its ancestors, and managed identities and resources with a system-assigned identity named in an assignment | [Azure RBAC](#azure-rbac) |
| Diagnostic settings | Their source, destination or an ancestor | [Diagnostic settings](#diagnostic-settings) |
| Alert rules and budgets | The Action Group or Monitor resource they reference | [Monitor alerts and budgets](#monitor-alerts-and-budgets) |
| AMPLS associations | Application Insights components, Log Analytics workspaces and data collection endpoints | [Azure Monitor Private Link Scope](#azure-monitor-private-link-scope) |
| Kubernetes Fleet members and configurations | AKS clusters, subnets and user-assigned identities | [Kubernetes Fleet Manager](#kubernetes-fleet-manager) |
| Data Migration migrations | Their SQL or Cosmos DB target, or a resource containing it | [Data Migration](#data-migration) |

**During and after deletion**

- **Concurrent changes:** Native creation identifiers are rechecked before deletion. Reviewed service trees also verify generation, membership, locks and protections. A recreated resource, an unreviewed descendant, or a change to reviewed configuration, protection or locks requires a fresh scan and plan. Many Azure DELETE APIs have no conditional (If-Match) version guard — including Azure Arc, Data Migration, Data Factory, Communication Services, Azure RBAC, diagnostic settings, Managed Grafana, AMPLS, workbooks and registered domains — so repeated checks cannot rule out a change made between the final check and the deletion.
- **Asynchronous operations:** Steward follows ARM operation-status headers, then reads the resource again to confirm it is gone. Failed or canceled operations remain failures. Forced deletion and purge options are not enabled.
- **Completion:** A step completes only when the resource's own read reports it absent (for example HTTP 404). An accepted DELETE, a successful or expired operation callback, a missing parent or a missing list entry is never enough. The same applies to every recorded descendant and required consumer after its parent disappears.
- **Worker restarts:** Accepted operations and their progress are saved, so after a worker restart Steward resumes polling and verification. Service sections note where a restart never resends DELETE.
- **Upgrading with pending tasks:** Cleanup recovery checks both the full reviewed request and the saved operation receipt. Finish pending cleanup tasks before upgrading from a version without this check: older pending receipts fail the new recovery check. A new cleanup attempt needs a fresh scan and plan.

**Verification status**

Azure coverage is tested offline with native HTTP protocol tests and unchanged official Microsoft response fixtures. Managed Grafana and Monitor data collection also replay Microsoft's recorded CLI deletion responses, including signed operation URLs and delayed completion; the earlier recorded API version and the synthetic final absence response are documented with the test evidence. Unless a service section says otherwise, this is not independent emulator or live-cloud validation by Steward.

### Azure Batch

**Inventory.** Accounts, pools, nodes, jobs, schedules, tasks, applications, package versions, private endpoint connections and network perimeter views. Jobs, schedules, tasks and nodes are read from the account's Batch endpoint with a separate Batch token.

**Permissions.**

- ARM permissions for the selected account resources.
- Batch data permissions, such as **Azure Batch Data Contributor** for cleanup (see [Data-plane and directory access](#data-plane-and-directory-access)).
- For URL-based storage and key references: subscription-wide Storage and Key Vault list access, and reads of the matching resources.
- For user-subscription nodes: Compute and Network reads for their VM, disks and network resources.

**Cleanup.**

- Cleanup reviews the complete account hierarchy. Pools, applications, private endpoint connections, jobs and schedules have ordered deletion steps; package versions are deleted before their application. Network perimeter views go with the account.
- Deleting a job or schedule includes its reviewed tasks; deleting a pool includes its reviewed nodes.
- An auto pool follows its job or schedule only when the actual lifetime settings and membership establish that ownership.
- Shared pools, packages and task dependencies need an explicit cleanup choice for their consumers.
- **Removing a single node** uses the pool's current ETag and requeues its running tasks. Tasks that ran on the node earlier do not require deleting the task records you keep.
- **User-subscription nodes:** Cleanup reviews the documented Uniform VMSS instance, its disks, extensions and network resources. A missing VM identity, shared or detached disks, kept or protected children and configuration drift block cleanup. The containing scale set stays.
- **Multi-instance tasks:** Cleanup terminates the task, waits for all subtasks, and verifies their working directories after deletion. Removing the primary task record alone is not enough.

**Limits.**

- Batch's native deletion ignores task data-retention periods.
- External storage, key vaults and identities stay independent references. Steward never fetches file contents, storage keys or vault secret and key contents to resolve them.
- Verification covers native schemas, composed HTTP scenarios, official CLI response replays, application graph checks and restart tests. It is not an independent Batch emulator or a live Azure deployment test.

See Microsoft's [task deletion](https://learn.microsoft.com/en-us/rest/api/batchservice/tasks/delete-task?view=rest-batchservice-2025-06-01), [node removal](https://learn.microsoft.com/en-us/rest/api/batchservice/pools/remove-nodes?view=rest-batchservice-2025-06-01) and [application packages](https://learn.microsoft.com/en-us/azure/batch/batch-application-packages).

### Container Instances

**Inventory.** Container groups, with native inventory and deletion. Subnets, managed identities and supplied Log Analytics resource IDs are recorded as references. Commands, configuration values and credentials are removed from inventory and logs.

**Cleanup.**

- Containers and init containers share the group's lifetime and are deleted with it. External Azure Files shares stay independent.
- The configuration Azure returns must match the review, including sensitive values, which are compared through a connection-keyed digest. After a credential rotation, scan again.
- An HTTP 200 deletion response still requires a native read confirming the group is gone.

See the [container-group deletion contract](https://learn.microsoft.com/en-us/rest/api/container-instances/container-groups/delete?view=rest-container-instances-2025-09-01).

### Kubernetes Fleet Manager

**Inventory.**

- Fleets, AKS and Arc-enabled Kubernetes members, managed namespaces, update runs, update strategies, auto-upgrade profiles, Gates and cross-cluster networks (Cluster Mesh profiles).
- Proxy children use their Fleet's region; managed namespaces keep their native location.
- Update runs keep a copy of their strategy; Gates refer to their owning run.
- Dynamic namespace placement is shown explicitly, without claiming a verified member set. Namespace annotations and placement expressions stay out of inventory and logs.
- Most Fleet operations use the stable API `2026-06-01`; member reads and Cluster Mesh operations use `2026-06-02-preview` to observe actual network membership.
- An omitted or missing Fleet does not mean its children disappeared; known children are checked individually.
- **Hub clusters:** A Fleet root scan verifies Hub ownership from the managed resource group's `managedBy`, the Fleet and AKS API endpoints, and the AKS `nodeResourceGroup` with its reciprocal owner. It then enumerates both groups, expands native children and documented external descendants, and includes Monitor resources omitted from the generic ARM list. Missing or ambiguous ownership stays unverified; naming patterns never establish ownership. Known resources omitted from lists are recovered by individual reads. An unknown omitted resource type or incomplete external ownership evidence prevents the scan from completing. The Fleet record keeps Hub and member configurations only as private digests; each resource keeps its normal product fields.

**Permissions.**

| Task | Permissions |
| --- | --- |
| Inventory | `Microsoft.ContainerService/fleets/read`, reads of the seven child collections, resource-group and management-lock reads |
| Hub scans | Unfiltered resource-group and resource lists, native member reads and child lists, and the relevant subscription Monitor and DNS indexes |
| Deleting AKS clusters, subnets or user-assigned identities | Fleet read permissions, because these checks read the Fleet collections in the subscription, including Fleets missing from inventory |
| Deleting members, namespaces, update runs, strategies or auto-upgrade profiles | The child's delete permission; update runs in `Running`, `Pending` or `Skipped` state also need permission to stop the run |
| Deleting a Cluster Mesh profile | Profile read, write, Apply and delete; member reads |
| Deleting the Fleet | `Microsoft.ContainerService/fleets/delete` |

**Cleanup.**

- **Members, managed namespaces, update runs, update strategies and auto-upgrade profiles** use their native conditional delete operations.
- **Update runs:** A running, pending or skipped run is stopped first; a run already stopping is not stopped again. Steward waits for a terminal state before deletion, then checks that the run and its reviewed Gates are gone. Gates are removed with their update run. Polling and execution phases survive a worker restart.
- **Managed namespaces** keep the reviewed `deletePolicy`: `Keep` removes ARM management and keeps the Kubernetes namespaces; `Delete` removes the namespaces and their contents on the hub and member clusters. Both remove the associated Azure RBAC assignments. Changing the policy or placement configuration requires a fresh scan and review.
- **Members:** Removing a member only unregisters it; the AKS or Arc-enabled Kubernetes cluster is not deleted. Arc clusters and their Kubernetes extensions are not Fleet-owned. Dynamic namespace placement conservatively requires namespace cleanup before removing any member of that Fleet. A member attached to a Cluster Mesh profile requires the profile to be cleaned up first. Cross-subscription or missing clusters stay unresolved references.
- **Cluster Mesh (cross-cluster networks):** Cleanup disconnects the reviewed network before deleting its profile, which interrupts cross-cluster connectivity and service discovery. Attachment is read from the members' actual `meshProperties` through unfiltered native lists and individual reads; matching labels alone do not establish it. Cleanup waits for any current Apply, sets an empty member selector without changing other settings, applies the disconnection, then confirms that no members remain attached. Protected or locked members, new attachments, configuration changes and unreadable residual attachments block progress. The profile's own 404 plus individual checks of known attachments complete the step. Members and their clusters stay.
- **Fleet:** Root cleanup is available for a Fleet with a verified Hub, or a verified hubless Fleet. Selecting a Fleet adds separate reviewed deletion steps for its native child configurations; each must be confirmed gone before the Fleet DELETE. Steward verifies the exact Hub impact set, ownership, configuration, protection and locks. The Hub cluster, both managed groups and their owned descendants belong to the Fleet (not to a second AKS or attachment controller), and only the Fleet DELETE is sent for them. After the Fleet disappears, both groups and every known typed descendant still need their own 404, including external managed disks and DNS resources; unknown contained types rely on their group being gone. A kept resource, an unverified Hub or unreadable residual resources prevent completion. Shared resources and enrolled member clusters stay independent.
- **References from Fleets:** A live Fleet reference blocks deleting an AKS cluster, subnet or user-assigned identity. These checks never give a Fleet ownership of member clusters or shared networking.
- RBAC assignments and diagnostic settings keep their own independent cleanup requirements.

**Limits.** Resources missing from inventory stay unresolved references. A changed ownership proof or native state requires a new scan before cleanup.

See the [Fleet FAQ](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/faq), [Hub cluster overview](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/concepts-lifecycle), [cross-cluster network deletion](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/howto-configure-use-cross-cluster-networking#delete-a-cross-cluster-network), [documented member types](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/quickstart-create-fleet-and-members), [managed namespace deletion](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/howto-managed-namespaces#delete-a-managed-fleet-namespace) and [update-run states](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/concepts-update-orchestration#update-run-states).

### App Service

**Inventory.** Web Apps and Function Apps, deployment slots, functions, certificates, hostname bindings and service plans.

**Permissions.** Native child read and list permissions, plus the delete permissions for the actions you select. Deleting a certificate also needs read access to every app's and slot's TLS state and hostname bindings.

**Cleanup.**

- Deleting an app or slot includes its reviewed deployment slots, functions, application certificates and hostname bindings. Keeping a child blocks the app or slot deletion. Default hostnames go only with their app or slot.
- App Service plans stay separate; select a plan explicitly to remove it.
- A certificate cannot be deleted while any binding matches its certificate ID or thumbprint. Remove the binding and scan again before deleting the certificate.
- Deleting an individual function can be unavailable when the app runs from a deployment package; Steward shows Azure's error unchanged.

See the [application deletion contract](https://learn.microsoft.com/en-us/rest/api/appservice/web-apps/delete?view=rest-appservice-2025-05-01) and [deployment package behavior](https://learn.microsoft.com/en-us/azure/azure-functions/run-functions-from-deployment-package).

### App Service domains

**Inventory.** Registered domains and their ownership identifiers, in global inventory. Contact details, transfer authorization and ownership-token values stay out of inventory and logs.

**Permissions.** Native domain and identifier list and read permissions, subscription-wide App Service lists, app and slot detail and hostname-binding reads, resource-group reads and management-lock reads. Grant domain, identifier and binding delete permissions only for the reviewed steps.

**Cleanup.**

- Selecting a domain first deletes its ownership identifiers and the associated app and slot hostname bindings. Apps, certificates, service plans and the DNS zone stay separate.
- To delete a DNS zone that a registered domain refers to, select that domain too, or change the domain's DNS hosting and scan again.
- Deleting a domain releases its registration; someone else can then buy the name. Steward keeps Azure's purchase lock and uses `forceHardDeleteDomain=false`, so Azure's 24-hour deletion delay applies. Steward waits up to 48 hours and resumes the wait after worker restarts.
- Steward waits for already deleted, reviewed app bindings to leave the hostname index; unknown assignments still block deletion. The domain and every known dependency must each read as gone.
- Configuration changes require a new scan and plan. The domain DELETE has no If-Match condition.

See Microsoft's [domain management and cancellation guidance](https://learn.microsoft.com/en-us/azure/app-service/manage-custom-dns-buy-domain) and the pinned [DomainRegistration API contract](https://github.com/Azure/azure-rest-api-specs/blob/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/domainregistration/resource-manager/Microsoft.DomainRegistration/DomainRegistration/stable/2024-11-01/openapi.json).

### Azure Arc

**Inventory.** Machines and shared ESU licenses, plus the extensions, Run Commands and license profiles under each machine. Known resources and parents omitted from lists are reread individually; incomplete or denied reads fail the scan. Scripts, extension settings, protected parameters and agent proxy settings stay out of public inventory and API logs.

**Permissions.**

| Task | Permissions |
| --- | --- |
| Inventory | `Microsoft.HybridCompute/machines/read`, `Microsoft.HybridCompute/machines/extensions/read`, `Microsoft.HybridCompute/machines/runCommands/read`, `Microsoft.HybridCompute/machines/licenseProfiles/read` and `Microsoft.HybridCompute/licenses/read` for the selected types. Child scans need subscription-wide machine list and read access. Machine scans need read access to all three child collections, even when you scan machines alone. |
| Deleting an extension, Run Command or license profile | `Microsoft.HybridCompute/machines/extensions/delete`, `Microsoft.HybridCompute/machines/runCommands/delete` or `Microsoft.HybridCompute/machines/licenseProfiles/delete`; native child and machine reads; resource-group list and read; management-lock reads; and the applicable Monitor, diagnostic settings, RBAC, Fleet and Data Migration dependency reads |
| Deleting a machine registration | `Microsoft.HybridCompute/machines/delete` and read access to the three child collections |
| Deleting a shared ESU license | `Microsoft.HybridCompute/licenses/delete`, license reads, subscription-wide machine list and read, machine license-profile list and read, plus the resource-group, lock and incoming-dependency reads above |

**Cleanup.**

- **Extensions, Run Commands and license profiles:** Rules that refer to them must be reviewed and removed first. Steward verifies the machine registration, the child's private configuration, protection tags and ownership before deletion. The child's own GET must return 404; the machine disappearing or the operation succeeding is not enough. The plan warns that:
  - deleting a running Run Command terminates its script;
  - extension removal needs separate verification on the agent side;
  - removing a license profile changes the machine's licensing while keeping shared licenses, and the profile's absence does not prove billing ended.
- **Machine registrations:** Ordinary registrations (no kind, AWS or GCP) can be removed after their reviewed extensions, Run Commands and license profiles are deleted. If child inventory is missing, scan again before planning; keeping a child blocks machine deletion. Known and reviewed children are reread individually even after the machine returns 404. The plan warns that removing the cloud registration leaves the external host and local agent for separate removal.
- **Protected registrations:** Bare HCI hosts, VMware, SCVMM, AVS, EPS, unknown kinds and other registrations linked to a parent cluster stay protected. Controller-managed machines go only with their controller. HCI registrations of Azure Local VMs need verified VM context and follow the [Azure Local](#azure-local) order.
- **Shared ESU licenses:** Profiles that use a license must be selected for cleanup or unlinked separately; deleting a profile or machine alone keeps the shared license. The license's native assignment count must be present and zero before DELETE. A license can cover other subscriptions in the same tenant, so an empty local profile list is not enough; clear external assignments through their own subscription. The plan warns that deletion removes the license entitlement and that billing may continue for up to five calendar days. After the license returns 404, saved and reviewed profile references are still checked, and surviving assignments prevent completion.

**Limits.**

- The native DELETE has no conditional If-Match.
- Tests cover native protocol replay and the scan, graph, planning and restored execution paths. Live agent removal is unverified.
- ESU license tests include Microsoft's original CLI DELETE response and restored execution; they do not verify that billing actually ends.

See Microsoft's [agent removal guidance](https://learn.microsoft.com/en-us/azure/azure-arc/servers/uninstall-agent), [disconnect and Azure Local deletion guidance](https://learn.microsoft.com/en-us/azure/azure-arc/servers/azcmagent-disconnect), [ESU licensing scope](https://learn.microsoft.com/en-us/azure/azure-arc/servers/license-extended-security-updates) and [ESU billing behavior](https://learn.microsoft.com/en-us/azure/azure-arc/servers/billing-extended-security-updates).

### Azure Local

**Inventory.**

- VM instances, guest agents, guest identity metadata, NICs, disks, logical networks, storage paths and images, read through native APIs. Known resources are reread individually, and the singleton `default` resources are checked even when their collection is empty or missing. Guest resources inherit the verified Arc machine's region.
- Failed permissions, snapshots that change during the scan or malformed references fail the scan without closing existing records.
- Shown: VM capacity and power state, network addresses, disk and image metadata and storage capacity. Kept private: credentials, SSH keys, proxy configuration and local paths.
- **Logical networks** are read with API version `2025-06-01-preview` to get the native, read-only `networkType`: `Workload`, `Infrastructure`, or `Unknown` when the field is missing or unrecognized. Names, tags and an empty NIC list do not establish the type. A denied or unsupported API response fails the scan without closing existing records. Other Azure Local resource APIs stay pinned to `2024-01-01`.
- **Network scans** follow NIC and VM references to guest resources and attached virtual disks. Saved attachment evidence is reread to recover VMs omitted from a list; detaching a disk removes that VM from the network's references. Storage paths and images stay independent references. Network membership creates no reverse dependency or deletion ownership.
- **Relationships** distinguish references to machines, VM instances, NICs, disks, logical networks, storage paths, images and custom locations. Missing or cross-subscription targets stay unresolved, and ordinary references never grant deletion ownership.

**Permissions.** Every cleanup below also needs resource-group and management-lock reads.

| Task | Permissions |
| --- | --- |
| Inventory | `Microsoft.HybridCompute/machines/read` for VM and guest discovery, plus the corresponding `Microsoft.AzureStackHCI/<resource-type>/read` for each selected family (including `virtualMachineInstances/guestAgents/read` and `virtualMachineInstances/hybridIdentityMetadata/read`). `Microsoft.AzureStackHCI/logicalNetworks/read` to list Local networks in the scan dialog. |
| Disk membership in network scans | `Microsoft.AzureStackHCI/virtualMachineInstances/read` and `Microsoft.HybridCompute/machines/read` |
| Deleting a VM | `Microsoft.AzureStackHCI/virtualMachineInstances/delete`. Discovery and review also read both Local guest singletons, the referenced OS disk, and the Arc extension, Run Command and license-profile collections, even when you scan only VMs. Direct guest and Arc prerequisite steps need their own delete permissions. |
| Deleting a guest agent | `Microsoft.AzureStackHCI/virtualMachineInstances/guestAgents/delete`; read access to the guest, VM instance and Arc machine |
| Deleting an Arc registration | `Microsoft.HybridCompute/machines/delete`. HCI machine scans also read the native VM instance, its two Local guest singletons, its registered OS disk and the three Arc child collections. |
| Deleting a disk or NIC | `Microsoft.AzureStackHCI/virtualHardDisks/delete` or `Microsoft.AzureStackHCI/networkInterfaces/delete`; the resource's read; subscription-wide Arc machine and Local VM list and read |
| Deleting an image | `Microsoft.AzureStackHCI/galleryImages/delete` or `Microsoft.AzureStackHCI/marketplaceGalleryImages/delete`; the image's read. Image-only scans and actions need no VM or Arc-registration reads. |
| Deleting a storage path | `Microsoft.AzureStackHCI/storageContainers/delete`; storage-path read; subscription-wide list and read for disks, gallery images and marketplace images; Arc machine and Local VM list and read |
| Deleting a logical network | `Microsoft.AzureStackHCI/logicalNetworks/delete` (with `2025-06-01-preview`); subscription-wide logical-network and NIC list and read; `Microsoft.Kubernetes/connectedClusters/read`; `Microsoft.HybridContainerService/provisionedClusterInstances/read`. Infrastructure networks also need Arc machine and Local VM list and read. |

Resource-group protection and management locks are checked for the VM and every managed resource, including a disk in another resource group.

**Cleanup.**

- **VMs:** Cleanup removes the reviewed guest and Arc prerequisites, then sends the native VM DELETE. The OS disk is part of the VM's deletion impact: a [corrected Microsoft support response](https://learn.microsoft.com/en-us/answers/questions/5758576/what-happen-with-associated-data-disk-with-azure-l) reports engineering confirmation that the OS disk is removed with the VM, while data disks remain. Keeping or protecting the OS disk, a guest resource or an Arc prerequisite blocks VM deletion. Before cleanup, native machine and VM reads check for other consumers of the OS disk, including VMs seen earlier but omitted from the current parent list. The step completes only when the VM, the OS disk and the identity metadata each read as gone; identity metadata and the OS disk are verified through their own GET after the VM deletion. If the VM response has no registered OS-disk ID, there is no separate disk record to verify, and the deletion warning still describes losing the OS disk. Selecting only the VM keeps its Arc registration. Associated NICs and data disks stay for separate cleanup. Warnings distinguish OS-disk removal, kept data disks and NICs, and the separate Arc-registration step.
- **Arc registrations of Local VMs:** Selecting the registration also schedules the VM cleanup first, then deletes the registration natively, following the official CLI order. Registration deletion runs after the VM's own cleanup completes. The verified VM context stays bound to the registration after the VM disappears, so a later scan can finish registration cleanup. An HCI host without previously verified VM context stays protected. A replaced registration, unavailable reads, kept or protected VMs and surviving VM, guest, identity or OS-disk resources block deletion or completion. NICs and data disks stay separate resources.
- **Guest agents** can be cleaned up on their own after a verified HCI registration and VM configuration have been scanned. Cleanup rereads the reviewed configuration and checks protection and inherited locks. The plan warns that guest management can be interrupted while the VM, Arc registration and identity metadata remain. Deleting the ARM resource does not verify that the agent was removed inside the guest.
- **Disks and NICs** can be cleaned up on their own after every native VM reference to them is removed. A VM still using the resource must be selected for prior deletion, or detached with native management tools and scanned again; selecting a disk or NIC never selects a VM for deletion. OS disks stay managed impacts of their VM. Scans keep verified VM identities to recover VMs omitted from lists, without adding them to network membership. Protection, changed configuration, unavailable reads and new consumers block cleanup. Completion requires the resource's own absence and cleared VM references — also after a restart, a synchronous 204 or a DELETE 404. Deleting a disk can permanently remove data.
- **Gallery and marketplace images:** Existing VMs keep their copies when the source image is deleted, as documented in the [Azure Local FAQ](https://learn.microsoft.com/en-us/azure/azure-local/manage/azure-arc-vms-faq). Selecting only an image creates one cleanup step with no VM prerequisites or managed impacts; when a VM using it is also selected, the VM is cleaned up first. Reviewed image configuration, ETags, protection and locks are checked before deletion. Completion requires the image's own absence, including after a DELETE 404.
- **Storage paths:** Native `containerId` and `vmConfigStoragePathId` references identify workloads on the path. Because these placement fields are optional, a resource that returns no storage location is treated as a possible consumer; reconcile its placement or explicitly review its cleanup first. Known disk and image IDs and VM scopes survive omissions from lists and later scans. Workloads that use or might use the path must be selected for prior deletion or removed outside Steward; selecting only the path never selects them. An OS disk can satisfy this prerequisite through its already planned VM only when the native lifecycle declares and verifies its deletion; that disk impact survives inventory changes and worker restarts without a separate disk DELETE. Each workload's own absence is checked before the path is deleted and at completion, even when the path already returns 404. The operation does not request volume deletion.
- **Logical networks:** Cleanup requires a verified network type and custom location; an `Unknown` type keeps the network protected.
  - Workload networks are checked for NIC references and native AKS `vnetSubnetIds`.
  - Infrastructure networks are checked for VMs, NICs, other logical networks and AKS instances sharing the verified custom location. The instance's VMs, NICs and workload networks must be removed first. Deletion removes only the cloud projection; the on-premises network remains.
  - Missing placement or reference fields count as possible use: reconcile their evidence or remove the consumers first. Consumers must be selected for prior cleanup or removed outside Steward; selecting a network never selects its workloads.
  - AKS provisioned instances are tracked as unresolved native dependencies. Remove them with native tools before the network can be cleaned up; this workflow does not delete AKS.
  - Known consumer IDs survive list omissions and the Arc parent disappearing. The native subnet `ipConfigurationReferences[].ID` is checked independently of the NIC list, and stale references keep blocking cleanup.
  - Protection, configuration, locks and live dependencies are reread before DELETE and again after the network returns 404. The preview SDK's `Location` polling state is saved and restored without resending DELETE.

**Limits.** These workflows are tested offline: native SDK contracts (with stubs where needed), composed VM, Arc and network transports, network filtering, protection, retention and reference changes, and scan, graph, plan and execution with process-restart recovery. Not yet verified live: the real controller and physical removal of VMs, disks, images, storage and networks; compatibility with real operation callbacks; and billing termination.

See Microsoft's [Azure Local VM management](https://learn.microsoft.com/en-us/azure/azure-local/manage/manage-arc-virtual-machines?view=azloc-2607), [logical-network guidance](https://learn.microsoft.com/en-us/azure/azure-local/manage/manage-logical-networks?view=azloc-2604) and [logical-network API change log](https://learn.microsoft.com/en-us/azure/templates/microsoft.azurestackhci/change-log/logicalnetworks), [storage-path removal sequence](https://learn.microsoft.com/en-us/azure/azure-local/manage/create-storage-path?view=azloc-2606), and the native [disk delete](https://learn.microsoft.com/en-us/rest/api/stackhci/virtual-hard-disks/delete?view=rest-stackhci-2024-01-01), [NIC delete](https://learn.microsoft.com/en-us/rest/api/stackhci/network-interfaces/delete?view=rest-stackhci-2024-01-01) and [storage-path DELETE](https://learn.microsoft.com/en-us/rest/api/stackhci/storage-containers/delete?view=rest-stackhci-2024-01-01) contracts.

### CDN and Front Door

**Inventory.** CDN and Front Door profiles, read through separate native child collections according to SKU: classic endpoints, origins, origin groups and domains; Front Door endpoints, routes, origin groups, origins, domains, rule sets, rules, security associations and certificate references. Batch-mode Front Door rules are shown inside their rule set.

**Permissions.** Native read and list access to the profile and the relevant child collections, including referring routes and security associations, plus delete permissions for the selected resources.

**Cleanup.**

- **Profiles:** Cleanup reviews every contained resource, and keeping a member of the cascade blocks it. Deleting an entire profile can remove its internal references in the same reviewed cascade.
- **Domains, origin groups, rule sets and certificate references** deleted on their own bring along the routes, rules or associations that must be removed first. A prerequisite shared by several targets is deleted once.
- An active classic endpoint that references an origin group blocks deleting the origin group until you update the routing or select the endpoint.
- **Batch-mode rules** share their rule set's lifetime: to keep them, keep the entire rule set. An origin-group override in a batch rule adds the referring rule set to the required deletions, and routes using that rule set must be removed first. Classic-mode rules can still be deleted individually, with a fresh check of the parent rule set.
- External origins, Key Vault data, DNS zones and WAF policies stay independent.
- Signed asynchronous operations and the final checks that each resource and child is gone survive a restart.

See the native [profile deletion contract](https://learn.microsoft.com/en-us/rest/api/cdn/profiles/delete?view=rest-cdn-2025-04-15), [rule-set cleanup guidance](https://learn.microsoft.com/en-us/azure/frontdoor/standard-premium/how-to-configure-rule-set) and Microsoft's [batch rule management guide](https://learn.microsoft.com/en-us/azure/frontdoor/rule-set-batch).

### WAF policies

**Inventory.** CDN and Front Door WAF policies and the endpoints or security associations that refer to them.

**Permissions.** Policy and referrer read access for inventory; for cleanup, also their delete permissions and operation-status access.

**Cleanup.**

- Deleting a policy includes its embedded rules.
- Any referring CDN endpoint or Front Door security association must be reviewed and deleted first; keeping it blocks policy deletion.
- Active classic Front Door frontend or routing references keep blocking until you remove them outside Steward.
- Policy configuration, locks and all remaining associations are rechecked before deletion.

See the [Front Door policy deletion contract](https://learn.microsoft.com/en-us/rest/api/frontdoorservice/webapplicationfirewall/policies/delete?view=rest-frontdoorservice-webapplicationfirewall-2025-11-01).

### Elastic SAN

**Inventory.**

- SANs, volume groups, volumes, snapshots and private endpoint connections, read with API version `2026-04-01-preview`. Steward reads the active and the soft-deleted (retained) volume and volume-group lists separately. Retained resources stay visible under their native IDs. Restoring a volume can change its ID while keeping its `volumeId`. An empty active list does not prove permanent removal.
- Responses are checked for resource identity, record shape and pagination; foreign resources, malformed pages, nonterminal responses and unsafe continuation links fail the call.
- A SAN scan also reads all volume, snapshot and private-connection collections under its active and retained groups, and recovers known children omitted from lists through their own reads. A group scan reads active and retained volumes, snapshots and the SAN's private endpoint connections.
- Children inherit the verified SAN region. Parent, subnet, source-volume, private-endpoint and controller references appear as dependencies; controller references do not grant deletion ownership.
- Verified groups show member counts. Private connections whose volume-group mapping cannot be resolved stay visible as possible dependencies. Retained groups keep their historical membership without showing it as current counts. A volume still being created without a GUID leaves membership unverified.
- **When a list is unavailable:** If a retained group's snapshot list returns 404, membership is recorded as incomplete; the scan still completes, discoverable volumes stay visible but protected, and known snapshots are checked individually (a known snapshot's own 404 closes it, but an unavailable list does not prove unknown snapshots absent). Incomplete volume membership is saved as a cleanup constraint until a fresh scan reads the collection. Known snapshots stay selectable on their own. Denied reads, missing volume collections and a missing snapshot list on an active group fail the scan.
- **After a SAN or group is deleted outside Steward,** inventory still reads both active and retained lists and each known resource. Missing child collections are accepted only after the parent's own absence; live children keep their recorded region and network references. A retained resource that was only reachable through the retained list cannot be declared absent when that list becomes unavailable. If a parent is missing, only previously verified records can supply its historical region and network context, and child collections must still be readable.
- **Child-only scans** leave unscanned parent records open and save a cleanup constraint when native membership is stale or missing. Scan the full SAN family again to reconcile the parents. A changed SAN creation identity or writable configuration also requires a fresh review before child cleanup.

**Permissions.** Grant child list and read access even when you scan only SANs or groups. Every cleanup also needs SAN reads, resource-group reads and management-lock list access.

| Task | Permissions |
| --- | --- |
| Deleting a private endpoint connection | `Microsoft.ElasticSan/elasticSans/privateEndpointConnections/delete`; connection and volume-group reads. No Network-provider delete permission is needed. |
| Deleting a volume | `Microsoft.ElasticSan/elasticSans/volumegroups/volumes/delete`; volume and snapshot list and read; snapshot delete permission for reviewed snapshots; volume-group reads |
| Deleting a volume group | `Microsoft.ElasticSan/elasticSans/volumegroups/delete`; volume, snapshot and private-connection list and read; delete permissions for reviewed prerequisites |
| Deleting a SAN | `Microsoft.ElasticSan/elasticSans/delete`; group, volume, snapshot and private-connection list and read; delete permissions for reviewed children |

**Cleanup.**

- **Snapshots** with a verified creation identity can be deleted. This removes the selected restore point and keeps its source volume and parents. Protection tags, locks, changed configuration and incomplete reads block deletion. Operation receipts survive restarts without resending DELETE.
- **Private endpoint connections** can be deleted directly. Removing an approved connection can interrupt access to its mapped volume groups. The consumer's Network private endpoint, NICs, DNS records, and the SAN, groups, volumes and snapshots stay. Cleanup verifies the connection's creation identity, target, native volume-group IDs and configuration, then rereads the SAN and mapped groups for protection, state, region and locks. Connections with incomplete group mappings stay visible but cannot be deleted. A disconnected state is not proof of deletion.
- **Volumes:** Associated snapshots are planned as separate, reviewed prerequisite deletions; keeping one blocks the volume cleanup. The volume DELETE always sets snapshot deletion to false, because snapshots have their own steps.
  - Ordinary deletion follows the volume group's retention policy (reread and bound to the review) and never silently purges a retained copy. If the API omits the policy, the native default applies, and the outcome is read from the active and retained lists plus the volume's own GET.
  - A retained copy is reported by its native ID and `volumeId`, stays discoverable, and needs a separate selection for permanent deletion. Selecting an already retained volume uses `deleteType=permanent`.
  - Restored or recreated identities, incomplete lists and known snapshots that still exist block completion.
  - Active iSCSI sessions are not forced by default: disconnect clients first. API callers can set the volume cleanup option `force_delete: true`; the plan then shows that workloads can be interrupted. Force is rejected for purging a retained volume, and no client command is run on hosts.
  - Receipts record the soft-delete or absence outcome and prevent a repeated DELETE after restart.
- **Active volume groups:** The plan deletes active volumes (with their snapshots) and any remaining group snapshots before the group DELETE. Private endpoint connections need a separate selection and must be gone first; their consumer Network endpoints stay independent. Existing retained volumes are reviewed as retained and must still be present when cleanup completes. A group containing retained volumes needs a verified Enabled retention policy. The group DELETE has no force or permanent-delete option, and this workflow does not disconnect host clients. Changed child identity or configuration, new members, protected resources, locks and a changed retention policy prevent deletion. Active and retained lists plus own reads distinguish permanent absence from a same-ID soft deletion; a retained group is rediscovered under its original ID. After the group reaches a final state, a missing snapshot collection falls back to each known snapshot's own read, while volume lists must stay readable to verify retained identities. Stale group membership requires a fresh group scan first.
- **SANs** can be deleted after a complete member review. Volume groups become prior steps, and their volumes and snapshots keep their own order. Private endpoint connections must be selected separately. Steward verifies all of these are gone before calling `Microsoft.ElasticSan/elasticSans/delete`, then checks the native collections and every known child's own read again after the SAN disappears; a parent or collection 404 alone does not finish the task. Location receipts survive restarts without resending DELETE. Recreation, writable configuration changes, protection tags, locks and newly discovered children prevent deletion. Read-only capacity counters can change as children are removed without invalidating the review. A stale SAN membership record blocks planning until the full family is scanned again.

**Limits.**

- A SAN stays protected while it has retained children or a volume group with an Enabled retention policy: this path cannot promise their permanent removal. An unexpected retained result from a child step also stops the SAN DELETE.
- Force and permanent options on parents are not supported. Purging retained groups and SAN cleanup across retained resources are not finished.
- Original REST examples, preview soft-delete CLI recordings and stable `2025-09-01` snapshot recordings are tested offline, together with inventory and cleanup recovery. No live Elastic SAN or independent ARM emulator has been verified.

See Microsoft's [Elastic SAN deletion sequence](https://learn.microsoft.com/en-us/azure/storage/elastic-san/elastic-san-delete).

### Azure NetApp Files

**Inventory.**

- Accounts, capacity pools, volumes, snapshots, subvolumes, quota rules, volume groups, snapshot and backup policies, backup vaults and backups. Steward follows native parent APIs and reads individual resources, including known resources missing from lists. Failed parent reads keep existing records.
- Backup and subvolume locations come from their verified parents. Volume subnet and VNet links support network selection; links from backups to their source volume do not authorize cascading removal. AD credentials and unrecognized private fields are not displayed.
- **Policy assignments:** Backup-policy, snapshot-policy and backup-vault scans also read current volume assignments. A suspended policy or disabled enforcement still counts as an assignment. Historical policy IDs stored in backups do not establish current assignments. Incomplete native policy indexes stay unresolved. These dependencies do not authorize volume deletion.
- **Backup vaults:** Scans record the vault's complete native backup membership independently of current volume assignments, including backups whose source volumes no longer exist. A known backup omitted from a list is read by ID; only its own absence removes it. Membership changes fail the scan. Missing or stale backup records require a refresh.
- **Volume groups:** Scans read the group's embedded volume IDs and count, then read those volumes and the account's complete pool and volume indexes. A volume's current group name is checked against the group; when the group supplies a file-system UUID, it must match the volume's own read. Known members omitted from a list are read by ID; only a verified change of association or the volume's own absence removes membership. A group scan never deletes or closes the volume record itself. Missing graph records require a refresh.
- **Network sibling sets:** For volumes that expose a network sibling-set ID, Steward runs the read-only native query that identifies volumes sharing primary mount IPs. It verifies the returned subnet and set identity, current volume IDs, UUIDs and configuration through repeated reads, and the reported IP must agree with the volume's mount targets when they are supplied. Missing optional mount targets do not replace the query's membership evidence.
- **Group NIC correlation:** Group scans match NICs from complete subscription-wide NIC lists and individual interface reads. They combine the primary sibling-set IPs with every available mount target, then verify matching subnet and IP pairs, native interface GUIDs and linked workload IDs in two passes. Known interfaces omitted from the list are read individually. Missing mount metadata or interface matches, missing native GUIDs, unknown or shared workloads and interfaces in transition leave the correlation incomplete. Only canonical linked volume IDs are kept; arbitrary workload values and private NIC fields are excluded. Correlation does not prove exclusive group ownership or authorize deletion.
- Unavailable or changing reads fail these scans and keep the previous records.

**Permissions.** Every cleanup also needs resource-group and management-lock reads.

| Task | Permissions |
| --- | --- |
| Policy and vault scans | Account capacity-pool and volume list and read; snapshot policies also need their associated-volume list permission |
| Backup-vault scans | Backup list and read in each vault |
| Volume-group scans | Group reads, account pool and volume list and read, and `Microsoft.Network/networkInterfaces/read` across the connection's subscription |
| Volumes with a network sibling set | `Microsoft.NetApp/locations/queryNetworkSiblingSet/action` and reads of every returned volume |
| Deleting snapshots or backups | Snapshot or backup delete; resource and ancestor reads. Backup review also needs account-wide vault and backup lists and source-volume reads. |
| Deleting subvolumes or quota rules | Subvolume or quota-rule list, read and delete; volume, pool and account reads; active-replication list access |
| Deleting a capacity pool | Capacity-pool delete plus the volume cleanup permissions; pool and volume list and read; account reads |
| Deleting a snapshot policy | Volume update and snapshot-policy delete, plus the assignment-discovery reads |
| Deleting a backup policy | Volume update, latest-backup-status read and backup-policy delete, plus the assignment-discovery reads |
| Deleting a backup vault | Volume update, latest-backup-status read, backup-policy read, backup delete and vault delete, plus the complete discovery and protection reads |

**Cleanup.**

- **Volumes:** Eligible volumes can be deleted after review, including their snapshots, subvolumes and quota rules. Stop applications and unmount the volume from all hosts first. Backup-vault backups, capacity pools and accounts are kept. Active replication, restores, clones, protection tags and locks prevent cleanup. Changing the reviewed volume or its children requires a new plan. Execution resumes from saved acknowledgements and checks that the volume and children are gone.
  - Mount targets are read-only volume properties; deleting a volume is not a substitute for removing a mount.
  - For volumes in a network sibling set: a network migration or missing native UUID keeps the volume visible but prevents cleanup. Unavailable reads, new peers, changed state or live peers omitted from the query require a fresh review. During cleanup, a smaller set is accepted only after each removed reviewed peer returns 404, so earlier volume prerequisites can complete without silently dropping live peers. Scan existing volumes again to record this network review. Network IPs are not NIC resource IDs: verifying NIC ownership, protection and deletion effects is not finished.
- **Snapshots and backup-vault backups** can be deleted on their own. The plan warns that the selected recovery point is permanently lost; its source volume, other recovery points and parents stay. Deleting the final backup removes the reference point for future incremental backups. Backups stay eligible after their source volume is deleted.
  - The known latest backup, including ties on snapshot time, is protected while a backup policy is assigned to the live source volume, even if policy enforcement is disabled. A newer backup must have completed successfully to establish that the selected backup is older. When the optional chronology is missing, Azure's native DELETE enforces the final restriction.
  - Steward never forces deletion or changes the backup policy. Snapshot restore, clone and replication restrictions also stay with Azure's native checks. Unavailable reads prevent cleanup.
- **Subvolumes and quota rules** can be deleted on their own on volumes without active replication.
  - Deleting a subvolume removes its data and can interrupt its applications; the parent volume, other subvolumes and recovery points stay.
  - Deleting a quota rule changes the user or group storage limits; other quota rules can still apply, and files are kept. Default and individual user and group rules are supported, including failed rules that need removal. Quota changes on a replication source propagate to its destination.
  - Changing a reviewed path, quota configuration or parent requires a fresh plan. Disabled subvolume operations, restores, active clones, protection and incomplete reads prevent deletion.
  - Azure has [deprecated its subvolume CLI commands](https://learn.microsoft.com/en-us/cli/azure/netappfiles/subvolume), while the REST version Steward uses, `2025-12-01`, still exposes their deletion API.
- **Capacity pools:** Cleanup deletes each reviewed volume, then the empty pool. Review the volumes and their snapshots, subvolumes and quota rules in the plan, and stop applications and unmount these volumes before execution. Keeping a volume blocks pool removal. The NetApp account and backup-vault backups are kept. Missing permissions, new volumes, changed writable configuration or changed native identities require a fresh review. Completion requires the pool's own absence under readable parents.
- **Snapshot policies** can be deleted while keeping their assigned volumes. The plan lists those volumes and their existing snapshots, subvolumes and quota rules as kept. Execution removes the reviewed snapshot-policy assignment from each volume, confirms it is gone, then deletes the policy. Future snapshots scheduled by this policy stop; existing volume data, snapshots and vault backups stay. Selecting a volume still uses the volume workflow and does not select its snapshot policy. New consumers, another assigned policy, changed volume settings or identities, protection and unavailable parents prevent changes. A completed callback is not enough while the volume still shows the old assignment or the policy still exists.
- **Backup policies** can be deleted while keeping their volumes and existing backups. Execution waits for backup transfer to be idle, suspends policy enforcement on each reviewed volume, confirms the suspension, waits for transfer completion again, then clears the volume's backup-policy assignment. The policy is deleted only after every reviewed assignment is gone. Future policy backups stop; backup vaults, existing backups, snapshots, subvolumes, quota rules and volume data stay. A fresh scan must capture the native policy UUID and the complete list of consumers. Unknown transfer states, unavailable reads, resumed enforcement, new consumers or changed identities or settings stop further changes.
- **Backup vaults:** Cleanup stops scheduled backups on the reviewed volumes, removes their backup-policy assignments, and permanently deletes all reviewed backups in the vault. Only after those backups are confirmed gone are the vault assignments removed and the empty vault deleted. Volumes, their data, snapshots, subvolumes, quota rules and global backup policies stay. Removing the final backup also removes the reference point for future incremental backups. Keeping any vault backup blocks vault cleanup. New backups or consumers, changed identities or settings, protection, unknown transfer states and failed reads stop further changes; a backup that appears during cleanup requires a new review, even if scheduled backups have already stopped.
- Each accepted update and deletion is saved for restart recovery, and a completed callback never replaces reading the resource itself.
- **Volume groups** cannot be cleaned up yet. Azure requires all member volumes to be removed first and automatically removes related network interfaces when it deletes the group; those interface effects still need review. Groups stay protected until interface lifecycle effects are implemented.

**Limits.**

- Cleanup of replicated child rules, replication termination, clone management and export-policy editing are still being implemented.
- The policy and vault workflows have offline contract, fault-injection and restart coverage. Live Azure acceptance of the volume PATCH that removes an assignment is unverified.
- Snapshot policies and backup vaults expose no native UUID: an identical recreation with the same name and no creation metadata cannot be told apart.
- Group reviews recorded by earlier versions (four fields) need a new scan.

See Microsoft's [snapshot deletion](https://learn.microsoft.com/en-us/azure/azure-netapp-files/snapshots-delete), [backup deletion](https://learn.microsoft.com/en-us/azure/azure-netapp-files/backup-delete), [quota-rule semantics](https://learn.microsoft.com/en-us/azure/azure-netapp-files/manage-default-individual-user-group-quotas), [subvolume deletion](https://learn.microsoft.com/en-us/rest/api/netapp/subvolumes/delete?view=rest-netapp-2025-12-01), [NetApp permissions](https://learn.microsoft.com/en-us/azure/azure-netapp-files/network-attached-storage-permissions), [deleting volumes](https://learn.microsoft.com/en-us/azure/azure-netapp-files/volume-delete), [storage hierarchy](https://learn.microsoft.com/en-us/azure/azure-netapp-files/azure-netapp-files-understand-storage-hierarchy), [snapshot-policy deletion requirements](https://learn.microsoft.com/en-us/azure/azure-netapp-files/snapshots-manage-policy#delete-a-snapshot-policy), [backup policy management](https://learn.microsoft.com/en-us/azure/azure-netapp-files/backup-manage-policies), [vault management](https://learn.microsoft.com/en-us/azure/azure-netapp-files/backup-vault-manage) and [application volume-group deletion](https://learn.microsoft.com/en-us/azure/azure-netapp-files/application-volume-group-delete).

### Cosmos DB

**Inventory.** Accounts and databases/containers for NoSQL, MongoDB, Cassandra, Gremlin and Table; roles, services, notebooks and private connections; managed Cassandra and Fleets. Accounts appear under global scope; managed Cassandra data centers use their deployment region. Data-resource names are case-sensitive (see [First inventory](#first-inventory)).

**Permissions.** Reads must cover the applicable API collections, throughput settings, ancestors and incoming Fleet associations across the subscription.

**Cleanup.**

- Deleting accounts, databases, containers or tables removes their data.
- Plans review required children, role dependencies and Fleet associations first. Keeping a built-in role or client encryption key requires keeping its account or database.
- Deleting a Fleet unlinks its accounts without deleting them; a protected or locked account blocks unlinking.
- Throughput, backup migration, configuration and child membership are checked before deletion.
- No restore or purge is offered.

See the [resource model](https://learn.microsoft.com/en-us/azure/cosmos-db/resource-model) and [MongoDB roles](https://learn.microsoft.com/en-us/azure/cosmos-db/mongodb/role-based-access-control).

### Azure DocumentDB

Azure DocumentDB was formerly called MongoDB vCore.

**Inventory.** MongoDB-compatible clusters and replicas, firewall rules, private endpoint connections and Microsoft Entra user registrations.

**Permissions.** Reads must cover each cluster, its complete child and replica lists, and all referenced replicas.

**Cleanup.**

- Deleting a cluster removes its data.
- Replicas are independent clusters. The plan deletes reviewed replicas before their source; deleting a replica keeps the source.
- Firewall rules, private endpoint connections and Microsoft Entra user registrations have separate deletion steps. Keeping or protecting a required resource blocks its parent.
- Removing a user registration does not remove the Entra identity or clean up database roles.
- Configuration changes and ongoing topology edits require a new scan or a retry.
- Backup restore and purge are not offered.

See [replication deletion](https://learn.microsoft.com/en-us/azure/documentdb/troubleshoot-replication) and [authentication](https://learn.microsoft.com/en-us/azure/documentdb/how-to-connect-role-based-access-control).

### Azure Data Explorer

**Inventory.** Kusto clusters, databases, follower attachments, data connections, principals, scripts and private connections, and custom sandbox images.

**Permissions.** Reads must cover all child collections, ancestor resources, follower indexes and linked targets.

**Cleanup.**

- Plans delete reviewed database resources and other required children before their cluster.
- Deleting a followed source database or cluster first removes the reviewed attachments on follower clusters; the follower clusters stay. An attachment controls its local read-only database views, so keeping a view blocks detachment.
- Active custom images go only with cluster cleanup.
- Managed private endpoints check target configuration, locks and protection. External data sources stay separate.
- Deleting a script does not undo the commands it ran.
- Azure may soft-delete the cluster for 14 days, but restoring the cluster does not undo earlier database DELETE steps. Steward offers neither restore nor a soft-delete opt-out.

See [follower behavior](https://learn.microsoft.com/en-us/azure/data-explorer/follower), [scripts](https://learn.microsoft.com/en-us/azure/data-explorer/database-script) and [cluster deletion](https://learn.microsoft.com/en-us/azure/data-explorer/delete-cluster).

### Redis

**Inventory.** Classic caches with access policies and assignments, firewall rules, replication links, patch schedules and private endpoint connections; Enterprise and Managed Redis clusters, databases, assignments and private endpoint connections.

**Permissions.** Reads must cover subscription-wide classic caches and linked peers, including peers in other resource groups.

**Cleanup.**

- Deleting a cache or database removes its data.
- Independent children must be deleted first; built-in classic policies go only with cache cleanup.
- **Classic replication:** Selecting either replica includes the shared primary-side unlink, and any reciprocal view, in the review. Keeping a link view blocks unlinking.
- **Enterprise active replication:** Cleanup checks all participants. Later deletions accept a smaller group only after departed members return 404. Degraded groups need separate recovery; Steward does not force-unlink them.
- New or inconsistent membership, unhealthy links, changed configuration, locks and protected peers block cleanup.
- Completion also checks that surviving replicas no longer reference the deleted target.

See [classic replication](https://learn.microsoft.com/en-us/azure/azure-cache-for-redis/cache-how-to-geo-replication) and [active replication](https://learn.microsoft.com/en-us/azure/redis/how-to-active-geo-replication).

### Azure Synapse Analytics

**Inventory.**

- Workspaces, Spark pools and dedicated SQL pools, read through native lists and detail reads. A resource omitted from a list is not removed; only its own GET confirming absence closes its record. Default Data Lake storage stays a separate dependency.
- **Data-plane objects:** Spark jobs and sessions, notebooks and Spark job definitions are read from the workspace data plane, with workspace ownership checks and a [separate token](#data-plane-and-directory-access). They appear as inventory records with workspace and pool references. Scans check native pagination and keep known objects omitted from lists. Code, job configuration and logs are left out of returned data and diagnostics.
- **Pipelines:** Static references from pipelines to notebooks, Spark job definitions, other pipelines and Spark pools appear as dependencies, including references inside nested control activities. Dynamic expressions stay unresolved. Incomplete permissions or configuration that changes during the scan prevent a successful scan.
- Notebook and Spark job-definition discovery also reviews the workspace's Spark work and incoming pipeline references, for later cleanup review.
- **Backups:** Recoverable dropped SQL pools and SQL pool restore points, with creation and deletion times, earliest restore time, restore-point type and label, and service-level metadata where Azure supplies them. They are kept separate from live pools. Their links show the workspace or pool used to query them and never authorize cascading deletion. Known records omitted by lists are checked individually. A missing parent or unavailable collection fails the scan and keeps existing records — a deleted workspace may need to be recreated before its retained backups can be queried. A backup record closes only when the backup's own read, under a readable parent, reports it absent.

**Permissions.** Cleanup and cancellation also need resource-group reads and management-lock list access.

| Task | Permissions |
| --- | --- |
| Pipelines | Workspace and referenced-resource reads |
| Notebooks and Spark job definitions | List and read access to the workspace's Spark pools, jobs, sessions and pipelines |
| Backups | Workspace and SQL pool list and read, plus `Microsoft.Synapse/workspaces/restorableDroppedSqlPools/read` and `Microsoft.Synapse/workspaces/sqlPools/restorePoints/read` for the selected backup kinds |
| Deleting a workspace | Workspace delete; workspace reads; SQL and Spark pool list and read; SQL replication-link list and read; Synapse data-plane list and read for notebooks, job definitions, pipelines, batches and sessions |
| Deleting a dedicated SQL pool | `Microsoft.Synapse/workspaces/sqlPools/delete`; pool and workspace reads; replication-link list and read |
| Canceling Spark jobs and sessions | Synapse data-plane cancellation permission, ARM reads of the workspace and pool, and access to the workspace operation endpoints for asynchronous status and result reads |

**Cleanup.**

- **Workspaces** can be cleaned up after a full scan of their SQL and Spark pools, code artifacts and Spark work records. Review the complete impact list: deletion removes SQL pools, compute engines, notebooks, job definitions, pipelines and workspace metadata, and interrupts workspace workloads. Linked Data Lake storage is kept. Keeping a workspace member blocks the operation; selecting a child alone never selects the workspace. New members, changed configuration, protection or incomplete reads require a fresh review. The saved operation survives restarts, and cleanup confirms that the workspace and its pools are gone.
- **Workspace deletion and backups:** Cleanup verifies that the live workspace and pools are removed. It does not purge SQL backups or prove that all copies of SQL data are gone: Azure can keep recoverable SQL backups after workspace deletion. Recovery depends on available restore points and retention, and Steward does not guarantee it.
- **Dedicated SQL pools** in Online or Paused state can be cleaned up on their own. Selecting an eligible SQL or Spark pool keeps its own cleanup step even after the complete workspace has been scanned, and never selects the workspace. The plan warns that the database is removed and that queries and consumers lose access; the workspace, other pools and retained SQL backups are not deleted. This is native pool deletion, not a check that every consumer is idle or that all backups are purged. Creation identity, configuration, parent context and protection are checked again before deletion.
- **Replication links** block both pool-only and workspace cleanup; known links omitted from a list are checked individually. Remove or resolve replication separately, then scan again.
- **Spark pools** that Spark jobs or sessions still use can be removed only with their workspace.
- **User-defined restore points** can be deleted on their own when Azure supplies a DISCRETE type, a user-request label and a valid creation date. Protected or incomplete records cannot be cleaned up. Deleting a point removes that recovery option and keeps the SQL pool, workspace and other backups. Steward saves the deletion receipt and verifies the point is gone with unchanged, readable parents. Automatic restore points cannot be deleted by users, and `DISCRETE` alone is not treated as proof that a point was user-created or can be removed.
- Code artifacts (pipelines, notebooks, Spark job definitions) and Spark work have no independent cleanup while active pipeline coverage is incomplete; they are removed with their workspace.

**Limits.**

- Steward's native Synapse support includes canceling Spark jobs and sessions, with protection checks and a detail readback. Cancellation can leave a stopped historical record and does not prove deletion. Operation status and result reads validate the operation's scope and keep operation completion separate from resource absence.
- Backup restore and complete handling of retained backups are not finished.

See Microsoft's [workspace deletion scope](https://learn.microsoft.com/en-us/azure/synapse-analytics/quickstart-create-workspace-cli), [restoring from a deleted workspace](https://learn.microsoft.com/en-us/azure/synapse-analytics/backuprestore/restore-sql-pool-from-deleted-workspace) and [backup retention](https://learn.microsoft.com/en-us/azure/synapse-analytics/sql-data-warehouse/backup-and-restore).

### Data Factory

**Inventory.** 14 native resource kinds in the factory's region (see the [coverage table](#inventory-and-cleanup-coverage)), including runtime node registrations and managed virtual networks. Authored pipelines, connection values, run parameters and debug details stay out of public inventory and logs.

**Permissions.** Inventory and cleanup need complete factory and child lists, each resource's own read, runtime status, trigger event-subscription status, pipeline-run queries and reads, debug-session queries, resource-group reads and management-lock reads. Cleanup adds the selected DELETE and preparation operations.

**Cleanup.**

- **Factories:** Cleanup reviews all owned artifacts. Keeping an owned child blocks factory deletion. Managed virtual networks have no independent DELETE and go with the factory.
- **Triggers and CDC** are stopped first; event triggers also wait for event unsubscription.
- **SSIS runtimes** and the artifacts that refer to them are separate prerequisites. After the asynchronous Stop completes, deletion waits until the runtime itself reports stopped.
- **Running work:** Factory cleanup cancels reviewed active pipeline runs one by one and removes reviewed debug sessions. Pipeline-only cleanup cancels that pipeline's reviewed runs; other standalone artifacts wait for factory work to finish. Newly discovered work requires a fresh review.
- **Shared self-hosted runtimes:** Select the referring runtime resources or their factories explicitly. Only after their own reads confirm they are gone does cleanup remove the reviewed factory's links; unresolved or foreign-subscription links block the host. Deleting a node removes its registration.
- Source data, external compute, identities, networks and self-hosted machines stay independent.
- Preparation and deletion checks resume after worker restarts, with up to 24 hours for verification. Changed configuration, incarnation, protection, locks or unreadable native context block progress.

**Limits.**

- Queries cover work the service can see and reread known runs; they cannot establish history that is not accessible.
- Masked secrets returned by Azure and artifacts without creation identifiers limit change detection.
- Verification covers protocol tests, official recordings and worker tests. Live-cloud and independent Data Factory emulator acceptance remain open.

See Microsoft's [SSIS deletion sequence](https://learn.microsoft.com/en-us/azure/data-factory/manage-azure-ssis-integration-runtime), [event unsubscription API](https://learn.microsoft.com/en-us/rest/api/datafactory/triggers/unsubscribe-from-events?view=rest-datafactory-2018-06-01) and [shared runtime management](https://learn.microsoft.com/en-us/azure/data-factory/create-shared-self-hosted-integration-runtime-powershell).

### Data Migration

**Inventory.** Eight native resource kinds in the migration service's region: classic services, projects, tasks, files and service tasks; SQL and Mongo migration services and their migrations to SQL or Cosmos DB targets. Mongo discovery uses independent target-scoped lists as well as service indexes; SQL discovery supplements service indexes with reads of previously known migrations. Migration inputs and connection details stay out of public inventory and logs.

**Permissions.**

- Inventory and cleanup: complete native service, child and migration indexes; each resource's own read; SQL runtime monitoring; reads of referenced SQL and Cosmos DB targets; resource-group and management-lock reads.
- Execution also needs the selected DELETE, task and migration Cancel, SQL `deleteNode` and regional operation-status permissions.
- Deleting a resource that a migration can target needs the same migration discovery permissions across the subscription.

**Cleanup.**

- **Classic services and projects:** Children have separate, reviewed deletion steps; running tasks are canceled first. A schema file requires prior cleanup of the tasks that use it.
- **SQL and Mongo services:** Select their target-scoped migrations explicitly and delete them first. SQL migrations are canceled before deletion; active Mongo migrations use the native force-delete operation. SQL service cleanup waits for running node jobs to finish, removes the reviewed runtime registrations and verifies they are gone.
- **Migration targets:** Deleting a referenced resource, or a resource containing it, checks for incoming migrations — including the SQL database identified by the native migration route. Referencing migrations must be selected and deleted first, and a migration missing from inventory blocks cleanup. Unreadable indexes or changed migration context also block it; the target's own read or list cannot prove that no migration references it. The check runs again during execution and resumed verification, including after the target disappears.
- Source and target databases, backup storage, identities, networking and runtime machines stay separate resources.
- Changes to configuration, target identity, groups, protection or locks, new migrations or changed runtime nodes can require a new review. Accepted operations and verification resume after worker restarts, with a 24-hour verification limit.

**Limits.**

- Unknown migrations omitted from every available index cannot be recovered.
- Masked fields limit change detection.
- Current evidence includes official API examples, CLI recordings and worker tests. Independent DMS emulator and live-cloud acceptance remain open.

See Microsoft's [ARM asynchronous-operation tracking](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/async-operations).

### Stream Analytics

**Inventory.** Jobs, inputs, outputs, functions, transformations, clusters and cluster private endpoints.

**Permissions.** Reads must cover child collections, cluster job membership, parent resources and linked targets.

**Cleanup.**

- Deleting a job permanently removes its input and output definitions, functions and query; external data stores stay.
- To remove a transformation, select its owning job.
- Deleting an input, output or function on its own requires a job in the Created, Stopped or Failed state.
- Cluster deletion first removes reviewed private endpoints. Private endpoints check target configuration, locks and protection.
- Jobs in a cluster stay independent: select them explicitly for deletion, or stop the jobs you keep, remove them from the cluster in Azure, and scan again. Steward does not automatically stop, detach or delete unselected jobs.

See [job cleanup](https://learn.microsoft.com/en-us/azure/stream-analytics/stream-analytics-clean-up-your-job) and [removing jobs from clusters](https://learn.microsoft.com/en-us/azure/stream-analytics/manage-jobs-cluster).

### Foundry and Cognitive Services

**Inventory.** Accounts, model deployments, projects, agents, connections (including datastores), capability hosts, managed networks, content filters and commitment plans.

**Cleanup.**

- Account cleanup first removes model deployments and reviewed dependencies, then soft-deletes the account. Purge is not offered.
- Deleting a capability host makes dependent agent state inaccessible. Individual threads, files and orphaned storage data are not cleaned up separately.
- Key Vault connections wait until all other account and project connections are deleted.
- Connections that require or have active managed private endpoints stay protected while their endpoint effects are not modeled.
- Keeping a required child blocks its controller. Shared commitment plans and referenced storage stay separate resources.
- Managed-network cleanup includes its rules and verifies protection of private-endpoint targets; some derived rules can only be removed through their network.

**Limits.** Applicability to legacy account kinds, the private-endpoint effects of managed connections, and the lifecycle of external network-perimeter associations are not finished.

See [recovery and billing behavior](https://learn.microsoft.com/en-us/azure/ai-services/recover-purge-resources).

### Azure AI Search

**Inventory.** Services, private endpoint connections, shared private links and network perimeter configuration views.

**Permissions.** Reads must cover all child collections and each linked target.

**Cleanup.**

- Deleting a service removes its search content.
- Private endpoint connections and shared links are reviewed and deleted first. Keeping either, or a perimeter configuration view, blocks service deletion.
- Deleting a shared link also changes the target's connection metadata, so native target reads, inherited locks and protection must pass. Target data resources stay separate. Cosmos DB accounts have native target checks.

**Limits.** Unmodeled targets, cross-subscription links and the lifecycle of external perimeter associations are not finished.

See the [shared-link deletion behavior](https://learn.microsoft.com/en-us/azure/search/troubleshoot-shared-private-link-resources).

### Service Bus and Event Hubs

**Inventory.**

- Service Bus namespaces, queues, topics, subscriptions, rules, authorization rules, recovery aliases, migration configurations and private endpoint connections.
- Event Hubs dedicated clusters, namespaces, event hubs, consumer groups, authorization rules, recovery aliases, schema and application groups and private endpoint connections.
- Service Bus autoforwarding dependencies resolve to a queue or topic in the same namespace. Event Hubs Capture references its destination storage account and Blob container.
- Network rule sets, Event Hubs network perimeter configurations, recovery-alias authorization views and the default `RootManageSharedAccessKey` rule have no delete action of their own and go with their namespace.

**Permissions.**

- Inventory and cleanup must include every reviewed child's native read operation. A failed child list is not treated as an empty namespace.
- Dedicated clusters: the cluster's namespace-list and quota-configuration permissions, plus each namespace's lifecycle permissions.
- Paired recovery aliases: native namespace-list permission and reads of the peer namespace and alias, even outside the selected region, because bare partner names are resolved across the subscription.
- Service Bus migration: subscription-wide reads of Service Bus namespaces and migration configurations, including namespaces outside the selected region. Missing inventory or unreadable native lists block the plan or action.

**Cleanup.**

- **Namespaces and entities:** Deleting a namespace, topic or subscription can remove contained messages and configuration. Every modeled descendant is reviewed and later confirmed absent. Namespace deletion does not select Capture storage, user-assigned identities or the separate private endpoint.
- **Entities with replication:** Deleting an individual entity also checks the current replication configuration, because deletion can propagate to a paired namespace. Deleting an entity while replication is active stays blocked; namespace cleanup first resolves its reviewed recovery or migration configuration.
- **Dedicated Event Hubs clusters:** The native member list and each namespace's `clusterArmId` must agree, including members in other resource groups. Selecting a cluster adds those namespaces and their reviewed descendants; keeping a member blocks cluster deletion. Quota settings, membership, namespace creation identities, locks and protections are rechecked. Native namespace reads must confirm every prerequisite is gone before the cluster DELETE, and again at completion after a restart. Azure imposes a four-hour minimum cluster age: a known younger cluster is marked temporarily protected.
- **Geo-disaster recovery (paired aliases):** Cleanup of a paired primary alias waits for pending replication, calls native BreakPairing, verifies `PrimaryNotReplicating` with an empty partner, then deletes the alias. Both ARM alias views and their authorization views are reviewed and confirmed absent. Selecting either namespace includes this shared prerequisite once; the other namespace and its entities stay when not selected. Selecting only the secondary alias identifies the primary alias as its required controller. Keeping an alias view prevents pair cleanup. Both namespaces, resource groups, locks and protections are rechecked during the saved preparation phases and after a restart. Steward does not perform failover.
- **Service Bus migration:** Cleanup waits for migration synchronization, aborts copying with the native Revert operation, verifies that the target association is cleared, then deletes the configuration. Cleanup of either namespace includes this configuration as a reviewed prerequisite; selecting both namespaces deletes it once. Cleaning only the source keeps the target and its entities, and cleaning only the target keeps the source and its entities. Source and target creation identity, configuration, permissions, locks and protections are rechecked; a migration being committed or an unknown state blocks cleanup. Saved preparation phases survive a worker restart. Steward does not commit migrations.

See Microsoft's [autoforwarding](https://learn.microsoft.com/en-us/azure/service-bus-messaging/service-bus-auto-forwarding), [Capture](https://learn.microsoft.com/en-us/azure/event-hubs/event-hubs-capture-overview), native [namespace list](https://learn.microsoft.com/en-us/rest/api/eventhub/clusters/list-namespaces?view=rest-eventhub-2024-01-01), [dedicated cluster deletion](https://learn.microsoft.com/en-us/azure/event-hubs/event-hubs-dedicated-cluster-create-portal#delete-a-dedicated-cluster), [pairing and unpairing behavior](https://learn.microsoft.com/en-us/azure/event-hubs/configure-geo-disaster-recovery) and [migration behavior](https://learn.microsoft.com/en-us/azure/service-bus-messaging/service-bus-migrate-standard-premium).

### API Management

**Inventory.** Services, workspaces, APIs and revisions, policies, products, subscriptions, portal content and configuration, credentials, notifications, associations, self-hosted gateway registrations and standalone workspace gateways. Vault secret contents are not fetched; external policy URLs and policy expressions are never downloaded or executed.

**Permissions.** Reads must cover the complete subscription gateway index, service workspace links, applicable child lists, GET and HEAD existence checks, ancestors and referenced targets, including targets in other resource groups. Credential references also need native subscription lists and reads of the matching Key Vaults and managed identities.

**Cleanup.**

- Cleanup reviews the selected service's or workspace's API definitions, policies, content and configuration. Shared subscriptions, API revisions and referring resources use separate reviewed deletion steps.
- Removing an API, product, group or tag association, or a notification association, detaches that association; the referenced member stays unless you select it too.
- Built-in groups, the administrator user, the master subscription, email templates and fixed portal, notification and tenant configurations go only with their owning controller. Keeping a required resource blocks controller deletion.
- A portal revision being published, or a configuration that changes, blocks cleanup.
- **Workspaces:** Cleanup first removes the workspace's reviewed standalone-gateway configuration connections. A shared gateway and its other workspace connections stay.
- **Services:** Azure keeps a deleted API Management service for 48 hours. Steward offers no restore, purge or email-template reset, and restoring a service does not undo earlier independent DELETE steps. Resource and child absence are checked after the asynchronous operation completes.

**Limits.** Verification includes official schemas, CLI response replays and selected API Management paths in a pinned independent emulator. It is not a live Azure deployment test.

See [workspace gateways](https://learn.microsoft.com/en-us/azure/api-management/workspaces-overview) and [soft-delete behavior](https://learn.microsoft.com/en-us/azure/api-management/soft-delete).

### Communication Services

**Inventory.** Communication accounts, SMTP usernames, phone numbers, reservations, rooms, Email resources, domains, sender usernames, suppression lists and addresses, all under global scope. Phone numbers, reservations and rooms keep their native account URLs, including case-sensitive room IDs. Scans verify complete account families twice, including room participants, and read previously known resources omitted from a list individually. An account or domain being gone does not prove its recorded descendants disappeared. Private SMTP, email-recipient, verification and participant details stay out of public inventory and API logs.

**Permissions.**

- Phone numbers, reservations and rooms use the Communication data plane: native data read permissions, including room participant lists, and the corresponding deletion permissions for cleanup (see [Data-plane and directory access](#data-plane-and-directory-access)).
- ARM list and read access for Communication and Email resources, their children, resource groups and locks.
- Cleanup adds each selected resource's native DELETE and operation-status reads.

**Cleanup.**

- **Accounts:** Cleanup first deletes reviewed SMTP usernames, reservations and rooms. The account's native DELETE then releases its reviewed phone numbers. Selecting a phone number on its own uses its release API.
- **Email:** Addresses, suppression lists, sender usernames and domains are deleted before their parents. A domain connected to a Communication account requires selecting that account too, or unlinking it first and scanning again. Reverse connection checks cover the connected subscription and recover known omitted accounts by GET; they cannot rule out connections from other subscriptions.
- A linked Notification Hub stays an independent reference and is not selected with its Communication account. Changing this link invalidates the reviewed account configuration.
- Deleting a Communication resource is permanent and also removes associated application data. Chat and Identity data and Event Grid filters are not individually listed or reviewed as cleanup impacts of these ten resource types.
- Microsoft distinguishes releasing a phone number from its continued visibility through the billing cycle. Steward waits up to 40 days to verify account and phone deletion, rechecks pending accounts and numbers hourly after the operation completes, and requires the resource and every recorded descendant to return 404 on their own read. This does not tell you when charges end.
- Busy purchases, protection tags, locks, changed configuration or unreadable resources block cleanup.

**Limits.** SMS sending and template administration are not part of these resource rules.

See [connecting email domains](https://learn.microsoft.com/en-us/azure/communication-services/quickstarts/email/connect-email-communication-resource), [resource deletion](https://learn.microsoft.com/en-us/azure/communication-services/quickstarts/create-communication-resource) and [phone-number release](https://learn.microsoft.com/en-us/azure/communication-services/quickstarts/telephony/get-phone-number).

### Monitor alerts and budgets

**Inventory.** Metric, activity-log, scheduled-query, smart-detector, Prometheus and alert processing rules; action groups; web tests; and Consumption and Cost Management budgets at subscription and resource-group scopes. Include global scope when you scan global rules and budgets. Private queries, receivers and notification content stay out of inventory and logs.

**Permissions.**

- Native list and read access for rules that might refer to the target. Action Group checks also enumerate both budget APIs across the subscription and its resource groups.
- Every ARM resource cleanup reads the six native alert-rule collections across the subscription, including rules whose source is missing from inventory. Relevant receiver targets also need Action Group reads; Application Insights components need Web Test reads; linked sources need resource-group reads.
- Event Hub receivers need subscription-wide Event Hubs namespace list and read access; ITSM receivers need Log Analytics workspace list and read access.
- Managed resource groups: reads of all eight Monitor collections and both budget APIs, including subscription and group budget lists.
- Grant native delete permission only for the resources you select.

**Cleanup.**

- Alert rules, action groups, web tests and budgets can each be deleted on their own. Budget notification action groups stay separate.
- A kept alert rule or budget blocks cleanup of the Action Group it references. Selecting both deletes the referencing resource first, in ordered, separately reviewed steps.
- Failed or inconsistent dependency reads block cleanup, even after the target is gone.
- **Receivers:** A missing destination is still matched after a restart through its reviewed ARM identity or the workspace's authenticated customer GUID; selectors from outside the subscription cannot claim local resources. Scan existing Log Analytics workspaces again to record this identity. Function and non-global Runbook references are recorded separately from their parent; global Runbook action names still need native webhook mapping.
- **Monitor resources in managed resource groups:** Steward supplements the generic ARM list with two complete native Monitor and budget reads. A member missing from inventory must be scanned before cleanup, and new members, inconsistent configuration or unreadable collections block deletion. Cleanup checks incoming references to every reviewed member being deleted. A verified Monitor member of the same group can go with its controller's cascade, including alerts or web tests that reference the controller itself; references from outside the group still block it. Private configuration, group identity and receiver resolution are rechecked, including surviving known members after the controller and group are gone. Each member gets its own final absence check.
- Configuration or permission changes require a fresh successful scan and plan.

See Microsoft's [Action Group receiver contract](https://learn.microsoft.com/en-us/rest/api/monitor/action-groups/get?view=rest-monitor-2023-01-01).

### Azure Monitor workspace

**Inventory.** The workspace and its default ingestion managed resource group.

**Cleanup.**

- Deleting the workspace also removes its default ingestion managed resource group and every resource in it. The plan reviews that full impact, including contained types Steward does not recognize, and keeping a group member blocks deletion.
- Both native default-ingestion IDs and any `managedBy` value must agree.
- The workspace data has no soft-delete recovery.
- Private connections are frozen workspace configuration; their external network endpoints are not part of the managed group just because they are referenced.
- External associations are removed first, and cleanup verifies that the group and each known resource are gone. Data collection endpoints in the managed group get the same [AMPLS checks](#azure-monitor-private-link-scope).

See Microsoft's [workspace management guide](https://learn.microsoft.com/en-us/azure/azure-monitor/metrics/azure-monitor-workspace-manage).

### Monitor data collection

**Inventory.** Data collection rules (DCRs), data collection endpoints (DCEs) and their associations on monitored resources. A subscription-bound Resource Graph query supplements discovery of orphaned associations, and native GET or ListByResource confirms each result. That query is eventually consistent and returns only resources you can read, so a complete inventory needs read access throughout the subscription. Orphaned associations without a location appear under global scope. Blob reference URL credentials stay out of inventory and logs.

**Permissions.** Rule and endpoint list and read access across the subscription, both reverse association lists, and association reads at their monitored resource scopes. Cleanup also needs the selected rule or endpoint delete permission and association delete permission at those scopes.

**Cleanup.**

- Removing an association stops that collection link.
- Plans review every required unlink, share one step when an association links both targets, and block if you keep a required link.
- DCR deletion uses `deleteAssociations=false`; native reads confirm every prerequisite association is gone first.

**Limits.** Tenant-global monitored-object associations are not supported.

See the native [association operations](https://learn.microsoft.com/en-us/rest/api/monitor/data-collection-rule-associations?view=rest-monitor-2024-03-11) and [DCR deletion](https://learn.microsoft.com/en-us/rest/api/monitor/data-collection-rules/delete?view=rest-monitor-2024-03-11).

### Azure Monitor Private Link Scope

**Inventory.** Global Azure Monitor Private Link Scopes (AMPLS), their scoped-resource associations and private endpoint connections, and the private-link capability descriptions.

**Permissions.** Read access to the complete subscription AMPLS index, native association lists and reads, and the target's reverse references — also outside the selected resource group.

**Cleanup.**

- **Scopes:** Scoped-resource associations and private endpoint connections have their own deletion steps; keeping either blocks scope deletion. Linked Log Analytics workspaces, Application Insights components and consumer network endpoints stay independent. Configuration checks cover access modes and per-connection exclusions.
- **Linked resources:** Before deleting an Application Insights component, Log Analytics workspace or DCE, Steward reconciles the complete subscription AMPLS index, native association lists and reads, and the target's reverse references. Missing inventory, unreadable lists and backlinks from other subscriptions block deletion; remove those links in their own subscription and scan again. The same checks apply to DCEs in a Monitor workspace's managed group.
- Relative operation locations are validated, bound to the selected connection and resource, and saved.

**Limits.** Verification uses pinned official examples and composed protocol tests; no AMPLS emulator or live-cloud run.

See [AMPLS association requirements](https://learn.microsoft.com/en-us/azure/azure-monitor/fundamentals/private-link-configure#connect-resources-to-the-ampls).

### Application Insights

**Inventory.**

- Components, analytics and my-analytics items, continuous exports, favorites, work-item configurations, API keys, linked profiler storage and annotations.
- Component settings: current billing features, daily caps, pricing plans, quota status and legacy proactive-detection settings. These settings go with their component and have no cleanup of their own. Private notification recipients stay out of inventory and logs.
- **Annotations** are discovered in a fixed window within Azure's rolling 90-day limit. Each scan also rereads previously saved annotation IDs, including records outside that window, so the window never closes older records. A saved annotation closes only when its own native GET confirms absence, including after its component disappears. Steward cannot list history it has never seen beyond the native window, and deleting the component can remove that history.
- **Managed workspace:** Component scans also inspect the complete resource-group index and the current managed workspace's group members and AMPLS associations. Ownership requires both the component's workspace reference and a matching group `managedBy`; names alone are not enough. Shared workspaces and detached managed groups are kept distinct. Failed or inconsistent reads fail the scan, and references to other subscriptions never authorize reads there.

**Permissions.**

- Read access to the component endpoints for billing, daily cap, pricing, quota and proactive-detection settings.
- Annotation LIST and GET; DELETE for selected annotations.
- Resource-group, resource-list, member product-read and AMPLS read permissions, including the managed group outside the component's resource group.

**Cleanup.**

- **Child resources:** The eight child kinds can be deleted individually, with component, group and lock protection and a final native GET. Shared storage stays independent.
- **Annotations:** Component cleanup reviews recent and saved annotations as separate prerequisites, and keeping an annotation blocks deletion. Read failures and ambiguous GET arrays block cleanup; an empty array is not treated as proof of absence.
- **Components:** Deletion first removes the reviewed children and AMPLS associations, including associations targeting the current managed workspace. It includes the reviewed current managed group and its known descendants. Completion requires the component and the group to be gone, and a native GET absence for every known member. Locks or Azure Policy can leave the group behind; Steward keeps waiting and does not delete the workspace on its own. Detached groups and shared workspaces are kept. Nested managed controllers with unmodeled external groups block cleanup.
- Before deleting the component, Steward rechecks its authored settings, including private notification recipients; a change requires a fresh scan and plan. Quota and other read-only values can change without invalidating the plan.
- Migrated smart-detection alert rules and their action groups are discovered and deleted independently, like other [Monitor alerts](#monitor-alerts-and-budgets).

See the [native annotation API](https://learn.microsoft.com/en-us/python/api/azure-mgmt-applicationinsights/azure.mgmt.applicationinsights.v2015_05_01.operations.annotationsoperations?view=azure-python), [managed-workspace behavior](https://learn.microsoft.com/en-us/azure/azure-monitor/app/managed-workspaces) and the [smart-detection migration guide](https://learn.microsoft.com/en-us/azure/azure-monitor/alerts/alerts-smart-detections-migration).

### Workbooks

**Inventory.** Shared workbooks, private workbooks and workbook templates. Workbook discovery reads all four documented categories, plus custom categories found through ARM or saved IDs. A category omission cannot close a saved workbook; a successful scan closes only saved IDs whose own native GET confirms absence. Templates use complete resource-group enumeration. Full authored content and revision history stay private. Provider errors, including an unavailable private-workbook API, fail the scan.

**Permissions.** Subscription resource and resource-group lists, locks and native workbook LIST and GET permissions, including LIST and GET on shared-workbook revisions. Cleanup needs the selected native DELETE permission.

**Cleanup.**

- Content and revision history are checked again before deletion; a change requires a new scan and plan.
- Deleting a workbook removes the active resource; it does not prove permanent erasure. Azure normally keeps deleted workbooks for about 90 days.
- Bring-your-own-storage (BYOS) workbooks have no provider-managed version history or recycle-bin recovery; recovery can depend on storage soft deletion.
- Referenced source resources, storage accounts and containers, and assigned identities are separate dependencies and are not selected by workbook cleanup.
- A workbook inside a managed resource group can instead be deleted with its controller when native ownership is verified, with the same content and history checks and its own final GET.

See [workbook management](https://learn.microsoft.com/en-us/azure/azure-monitor/visualize/workbooks-manage) and [BYOS behavior](https://learn.microsoft.com/en-us/azure/azure-monitor/visualize/workbooks-bring-your-own-storage).

### Managed Grafana

**Inventory.** Workspaces, managed private endpoints, private endpoint connections and integration fabrics. SMTP passwords stay out of inventory and logs.

**Permissions.** Deleting a workspace needs native read, list and delete access to all three child collections.

**Cleanup.**

- Each child is deleted as a reviewed prerequisite, and keeping a child blocks workspace deletion. Each resource also has its own native action.
- Steward checks native configuration and parent identity, then confirms that the children and the workspace are gone.
- Linked data sources, AKS clusters and consumer private endpoints stay separate resources.

See the native [workspace](https://learn.microsoft.com/en-us/rest/api/managed-grafana/grafana/delete?view=rest-managed-grafana-2025-08-01), [managed private endpoint](https://learn.microsoft.com/en-us/rest/api/managed-grafana/managed-private-endpoints/delete?view=rest-managed-grafana-2025-08-01) and [integration fabric](https://learn.microsoft.com/en-us/rest/api/managed-grafana/integration-fabrics/delete?view=rest-managed-grafana-2025-08-01) operations.

### Diagnostic settings

**Inventory.** Resource and subscription diagnostic settings, including the separate Blob, File, Queue and Table service scopes, in global inventory. The subscription setting list does not enumerate every resource's settings, so Steward also uses native child APIs and previously saved setting IDs; a known setting can survive its source's deletion. Settings on an unknown source type appear protected until native source verification is supported. A never-seen orphan outside those sources has no supported subscription-wide index. A saved setting closes only after its own native GET confirms absence; the source or group being gone is not proof.

**Permissions.** Subscription resource-list access, native reads and child lists for discovered sources, resource-group and management-lock reads, and diagnostic-setting list and read access at each exact source scope. Cleanup also needs `Microsoft.Insights/diagnosticSettings/delete` for each selected setting.

**Cleanup.**

- Deleting a source, destination or ancestor requires selecting the settings that refer to it first, including settings inside a managed resource group.
- Deleting a setting stops its configured export. Shared storage, Event Hubs and workspaces stay separate resources.
- A failed list or detail read blocks cleanup.

See Microsoft's [diagnostic settings guide](https://learn.microsoft.com/en-us/azure/azure-monitor/platform/diagnostic-settings).

### Azure RBAC

**Inventory.** Custom and built-in role definitions, and role assignments at subscription, resource-group and resource scopes, in global inventory. Discovery uses the connected subscription's native Authorization APIs, including narrower resource scopes. Assignments inherited from the tenant or management groups are not managed by this connection. Permission expressions and authored configuration stay out of inventory and logs.

**Permissions.**

- `Microsoft.Authorization/roleDefinitions/read`, `Microsoft.Authorization/roleAssignments/read`, `Microsoft.Authorization/roleEligibilitySchedules/read` and `Microsoft.Authorization/roleAssignmentSchedules/read`, plus native reads for the referenced scopes, resource groups and management locks.
- Deleting a custom role: `Microsoft.Authorization/roleDefinitions/delete` on every assignable scope.
- Deleting an assignment: `Microsoft.Authorization/roleAssignments/delete` at its exact scope.
- User-assigned identity reads: `Microsoft.ManagedIdentity/userAssignedIdentities/read`. System-assigned identities use their resource's native read. This mapping uses ARM APIs and needs no Microsoft Graph access.

**Cleanup.**

- **Custom roles:** Select the assignments that refer to a role before deleting it.
- **Scopes:** Deleting a scope resource or one of its ancestors requires deleting the assignments and custom-role definitions that refer to it first, including extensions in managed resource groups. Every ARM dependency and cleanup check reads the subscription's role-assignment and role-definition indexes; linked sources also need scope and PIM reads. New, unindexed or unreadable references block cleanup, also after the target is deleted.
- **Managed identities:** Within the connected subscription, an assignment's `principalId` also identifies user-assigned managed identities and resources with system-assigned identities, even when the assignment is in another resource group. Delete those assignments first, including when an owning controller removes the identity-bearing resource. Microsoft notes that deleting a managed identity leaves its assignments behind.
- Before using these principal dependencies, scan existing ARM resources again. Steward verifies the saved principal and tenant GUIDs against native resource reads and keeps that verified identity after the resource disappears. Changed, missing or unreadable identity evidence blocks the dependency check. Application `clientId` values and attached shared identities do not establish ownership: deleting a resource that uses a shared identity keeps the identity and its assignments.
- **Protected from independent deletion:** built-in roles; roles with assignable scopes outside the connection; assignments with a matching PIM schedule; assignments to the connection's own principal ([General protections](#general-protections)); and resources with protection tags, management locks, or changed native configuration or scope identity.
- A native DELETE 200 or 204 only acknowledges the request; completion still requires the resource's own GET to report it absent.
- Shared destinations and external Entra principals stay independent.

**Limits.** Deleting PIM schedules and administering tenant or management-group RBAC are not implemented.

See Microsoft's [custom-role deletion requirements](https://learn.microsoft.com/en-us/azure/role-based-access-control/custom-roles-rest#delete-a-custom-role) and [managed identity maintenance](https://learn.microsoft.com/en-us/entra/identity/managed-identities-azure-resources/managed-identity-best-practice-recommendations#maintenance).

### Defender for Cloud

**Inventory.** Subscription protection plans, VM, VMSS and Arc machine scopes, and the Containers plan on AKS and ACR. Plans show the native Free or Standard tier, sub-plan, trial time, enablement time, extension status, inheritance and resource coverage. A Standard subscription plan does not mean every resource is covered: resource overrides can differ. Known plans are reread individually; a parent or list disappearing does not prove a plan is gone. Extension parameters and operation messages stay out of public inventory and logs.

**Permissions.** Subscription identity, native parent list and read, and `Microsoft.Security/pricings/read` across those scopes.

**Cleanup.** None. These are read-only service-state records, matching the read-only Alibaba Cloud Security Center baseline. Steward does not change protection tiers or remove resource overrides.

**Limits.** Current evidence consists of official examples and protocol and worker tests; independent Defender emulation and live-cloud verification remain open.

See [native plan state and inheritance](https://learn.microsoft.com/en-us/rest/api/defenderforcloud/pricings/list?view=rest-defenderforcloud-2024-01-01).

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| Validation fails for a service principal | Check that you entered the client secret **value**, not its ID, and that the service principal was created in the subscription's tenant. Sovereign clouds and Azure Stack are not supported. After rotating a secret, use **Replace credential** with the same subscription, tenant and application. |
| Validation succeeds, but scan items fail with permission errors | Validation confirms the identity only. Grant [Reader](#base-access-for-inventory) at the subscription, plus the product reads listed in the service's section under [Cleanup protections](#cleanup-protections). Scan again. |
| A browser sign-in connection reports Key Vault, Microsoft Graph or another data plane as failing | Your own account cannot reach that data plane; the rest of the inventory continues. Grant your account access, or use a service principal with the [data-plane permissions](#data-plane-and-directory-access). |
| Blob containers are missing or Blob cleanup fails | Grant a data-plane role such as **Storage Blob Data Reader**, and make sure Steward can reach the account's public Blob endpoint. Blob cleanup needs the standard `ACCOUNT.blob.core.windows.net` endpoint. |
| Batch jobs, tasks or nodes are missing, or Batch cleanup fails | Grant Batch data permissions, such as **Azure Batch Data Contributor**, in addition to ARM permissions. See [Azure Batch](#azure-batch). |
| Microsoft Entra users and groups are missing | Grant the Graph application permissions `User.Read.All` and `GroupMember.Read.All` (or `Directory.Read.All`) and give admin consent. |
| The scan fails on a Key Vault | Steward could not read the vault's certificates. Grant **Key Vault Reader** or an access policy with certificate **List** and **Get**, and check network access to the vault. |
| Management groups are missing | Grant `Microsoft.Management/managementGroups/read` on the groups to inventory. |
| A resource you deleted in Azure still appears | When a list omits a resource or a read fails, Steward keeps the previous record until the resource's own read confirms it is gone. Fix any permission errors in the scan and scan again. |
| A Cosmos DB scan fails | Two data-resource names that differ only in case collide in the inventory identity. |
| An Azure Local logical network cannot be selected in the scan dialog | Grant `Microsoft.AzureStackHCI/logicalNetworks/read`. After fixing permissions, use **Retry**; your selection is kept. |
| An Azure Local logical network shows the type `Unknown` and is protected | Steward could not read a recognized `networkType` with API version `2025-06-01-preview`. The network stays protected until its type and custom location are verified. |
| NetApp volumes in a network sibling set fail to scan | Grant `Microsoft.NetApp/locations/queryNetworkSiblingSet/action` and reads of every returned volume. |
| Cleanup is blocked by a management lock | Steward never removes locks. Remove the lock in Azure if the deletion is intended, then scan again. |
| A resource shows as protected | Check for a `steward/protected` or `steward:protected` tag, a lock, a managed resource group, or a product-specific protection in the service's section. |
| A role assignment cannot be cleaned up | It grants the connection its own access, belongs to a built-in role, has an assignable scope outside the connection, or matches a PIM schedule. See [Azure RBAC](#azure-rbac). |
| A subnet, NSG, route table, NAT gateway or public IP cannot be deleted | Something still occupies it — see [network occupants](#general-protections). Add the occupant to the task, or remove it in Azure and scan again. A service association link must be removed by its owning service. |
| A storage account or Blob container cannot be deleted | It is not empty, or a legal hold or immutability policy applies. Steward does not empty or purge data. |
| The task is blocked by a role assignment, diagnostic setting, alert rule, AMPLS association, Fleet or migration that refers to the target | Add the referring resource to the task so it is deleted first, or remove the reference in Azure and scan again. See [References from other resources](#general-protections). |
| The plan asks for a fresh scan | Reviewed configuration, protection, locks or membership changed, or the resource was recreated. Scan again and review the new task. |
| Pending cleanup fails its recovery check after an upgrade | Receipts from a version without full-request checks cannot be recovered. Scan again and create a new cleanup task; next time, finish pending tasks before upgrading. |
| A deletion stays in verification for a long time | Some services confirm deletion slowly: Communication accounts and phone numbers can take up to 40 days, domains up to 48 hours, and Data Factory and Data Migration verification up to 24 hours. A stuck managed group can also be held by a lock or Azure Policy. |
| An Event Hubs dedicated cluster is temporarily protected | Azure requires clusters to be at least four hours old before deletion. Try again later. |
| An Elastic SAN volume deletion fails with active iSCSI sessions | Disconnect the clients first. API callers can set `force_delete: true` on the volume cleanup request. |
| Deleting a single function fails | The app runs from a deployment package, which can prevent deleting individual functions. The Azure error is shown unchanged. |
| Container Instances cleanup asks for a new scan after a credential rotation | Sensitive values are compared with the review; scan again after rotating credentials. |
| A service is shown as read-only | Cleanup for it is not implemented, for example resource groups, Key Vaults, Container Apps environments, Purview accounts and managed applications. See the [coverage table](#inventory-and-cleanup-coverage). |

## Next steps

- [Scan resources](./scans.md)
- [Query resources](./resources.md)
- [Clean up resources](./cleanup.md)
- [OIDC connections](./oidc.md)
