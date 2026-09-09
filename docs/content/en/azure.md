---
title: "Microsoft Azure"
description: "Connect an Azure subscription, discover resources, and review supported cleanup actions."
navTitle: "Microsoft Azure"
---

## Connect a subscription

Each Azure connection accesses one subscription using a Microsoft Entra service principal. This integration uses Azure public cloud; sovereign clouds and Azure Stack endpoints are not supported.

1. Create an application registration and service principal in the subscription's tenant, then create a client secret. Record the **secret value**, not its ID.
2. Assign **Reader** at the subscription scope for inventory, resource details, region discovery, and management-lock checks. A custom role needs equivalent read access, including `Microsoft.Resources/subscriptions/read`, subscription resources and resource groups, locations, resource-provider read operations, and `Microsoft.Authorization/locks/read`.
3. Open **Settings → Cloud connections → Add connection**, select **Microsoft Azure**, and enter the **Subscription ID**, **Tenant ID**, **Application (client) ID**, and **Client secret**.
4. Validate the connection, then refresh its regions. Add product deletion and operation-status permissions only for resources you intend to clean up. Successful connection validation does not prove that every resource-specific API is authorized.

Blob container cleanup also requires data-plane read access, such as **Storage Blob Data Reader**, and network access to the account's public Blob endpoint. Steward requests separate ARM and Storage access tokens. It does not use ambient Azure CLI credentials, managed identities, or storage account keys.

Credentials are encrypted using the deployment's credential-encryption key. Use **Replace credential** when rotating a secret; the subscription, tenant, and application must remain the same. See Microsoft's [service principal authentication guide](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-client-creds-grant-flow).

## First inventory

1. Select the new connection and confirm the subscription.
2. Run **All active regions + global**. Steward uses native product lists and detail reads for supported resources, alongside the broad Azure Resource Manager inventory.
3. Check scan coverage and errors. Permission or pagination failures do not establish that previously known resources disappeared.
4. Open a regional VNet to inspect its subnets, NICs, VMs, and related resources. VM network placement is resolved through its NICs.

Native discovery includes child resources such as VNet subnets, Blob containers, SQL databases, scale-set instances, DNS records, Service Bus entities, and Event Hubs consumer groups. Resource groups appear in the global inventory; their Azure location describes the group's metadata location. Full ARM IDs preserve subscription and resource-group identity and are matched without case sensitivity. Same-name resources in different groups remain distinct.

## Inventory and cleanup coverage

Steward recognizes 121 resource types; 109 have native deletion actions, subject to the conditions below. Additional ARM resource types appear as read-only inventory. Coverage is still being expanded; this is not complete Azure service coverage.

| Service | Resources | Cleanup |
| --- | --- | --- |
| Compute | VMs and extensions, managed disks, snapshots, managed images, availability sets, dedicated hosts and capacity reservations | Supported, with attachment and ownership protections; host/reservation groups require their members to be deleted first |
| VM scale sets | Uniform and Flexible sets, instances and extensions | Reviewed cascades for Uniform members; prerequisite VM deletion for Flexible sets |
| Virtual networks | VNets, subnets, NICs, network security groups, route tables, public IPs, public IP prefixes, NAT gateways | Supported |
| Load balancing | Load balancers and Application Gateways | Supported |
| Storage | Storage accounts and Blob containers | Empty resources only |
| SQL | Logical servers, databases and elastic pools | Server cleanup includes its reviewed databases and pools; standalone `master` deletion is prohibited |
| PostgreSQL / MySQL | Flexible servers | Supported |
| App Service | Web Apps / Function Apps and App Service plans | Supported as separate resources |
| Containers | Container registries, Container Apps and AKS | AKS cleanup reviews its node resource group and known nested or externally managed descendants |
| DNS and private endpoints | Public/private zones and records, private DNS links, private endpoints and DNS zone groups | Reviewed controller cascades include verified managed NICs and external DNS records; DNS system records cannot be deleted independently |
| Virtual WAN gateways | VPN/ExpressRoute gateways, connections, VPN NAT rules and links | Connection/NAT prerequisites are explicit; VPN links belong to their connection |
| Service Bus | Namespaces, queues, topics, subscriptions, rules, authorization rules, recovery aliases, migration configurations and private endpoint connections | Independent native actions and reviewed namespace/entity cascades; active pairing or migration blocks deletion |
| Event Hubs | Namespaces, event hubs, consumer groups, authorization rules, recovery aliases, schema/application groups and private endpoint connections | Independent native actions and reviewed namespace/event-hub cascades; active pairing blocks deletion |
| Operations and identity | Log Analytics workspaces and user-assigned managed identities | Supported |
| Aggregate resources still awaiting lifecycle support | Resource groups, Key Vaults and Container Apps environments | Read-only |

Service Bus/Event Hubs network rule sets, Event Hubs network perimeter configurations, recovery-alias authorization views, Uniform scale-set network resources and VPN connection links have no independent native delete action. They appear in the owning controller's reviewed deletion impacts. Retaining an intrinsic child blocks that controller's deletion. Resource-group, Key Vault and Container Apps environment cleanup remains unimplemented.

Service Bus autoforwarding dependencies resolve to a queue or topic in the same namespace. Event Hubs Capture references its destination storage account and Blob container. Namespace deletion does not select those storage resources, user-assigned identities or the separate private endpoint for deletion. Inventory and action permissions must include every reviewed child's native read operation; a failed child list is not an empty namespace. See Microsoft's [autoforwarding](https://learn.microsoft.com/en-us/azure/service-bus-messaging/service-bus-auto-forwarding) and [Capture](https://learn.microsoft.com/en-us/azure/event-hubs/event-hubs-capture-overview) documentation.

## Cleanup protections

- **Management locks:** Subscription, resource-group, resource, and relevant descendant locks block deletion. Steward checks locks during inventory and again immediately before deletion, and never removes them.
- **Managed resources:** Provider-owned resources require their supported owning controller, except members with an explicitly supported independent action. AKS deletion includes its reviewed node resource group; arbitrary deletion of managed-group resources remains prohibited.
- **VM attachments:** Plans show native auto-delete disks, NICs and public IPs. Supported retention changes use conditional native updates before deletion and survive worker restart. VM extensions remain part of the VM's deletion impacts. Uniform scale-set unmanaged VHD cleanup and disk detachment are not yet implemented.
- **Storage:** A storage account must have no Blob containers, file shares, queues, or tables for its supported services. Blob containers must have no blobs, snapshots, versions, deleted entries, or uncommitted uploads. Legal holds and immutability policies block cleanup. Steward does not empty or purge data to make a resource deletable. Blob cleanup currently requires the standard `ACCOUNT.blob.core.windows.net` endpoint.
- **VNets and DNS zones:** Required subnets and private DNS links must be removed first. Selecting a VNet does not silently delete unselected subnets or links.
- **Messaging:** Namespace, topic and subscription deletion can remove contained messages and configuration. Every modeled descendant must be reviewed and later confirmed absent. Steward does not automatically break geo-recovery pairings, fail over, or complete/abort an active Service Bus migration; those workflows remain unimplemented.
- **Concurrent changes:** Native creation identifiers are rechecked before deletion. Reviewed service trees also verify generation, membership, locks and protections. A recreated resource or an unreviewed descendant requires a fresh scan and plan.
- **App Service:** Deleting an app explicitly preserves its App Service plan. Select the plan separately when it should also be removed.
- **Asynchronous operations:** Steward follows ARM operation-status headers and performs a fresh resource GET to confirm absence. Failed or canceled operations remain failures. Forced deletion and purge options are not enabled.

Review the [cleanup selection and results](./cleanup.md). Database and registry deletion can remove their contained data. Azure permissions, retention settings, dependencies, and concurrent changes can still prevent an action. See Microsoft's [management locks](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/lock-resources), [VM deletion settings](https://learn.microsoft.com/en-us/azure/virtual-machines/delete), and [asynchronous operation behavior](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/async-operations).

The expanded coverage has retained native HTTP protocol tests and unchanged official response fixtures. Independent emulator and real-cloud validation of these additions remains outstanding.
