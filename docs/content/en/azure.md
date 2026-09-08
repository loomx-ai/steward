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
2. Run **All active regions + global**. Azure Resource Manager supplies the resource list; Steward reads the product APIs for supported resource details.
3. Check scan coverage and errors. Permission or pagination failures do not establish that previously known resources disappeared.
4. Open a regional VNet to inspect its subnets, NICs, VMs, and related resources. VM network placement is resolved through its NICs.

Steward explicitly enumerates VNet subnets, Blob containers, SQL databases, and elastic pools because subscription-wide lists can omit child resources. Resource groups appear in the global inventory; their Azure location describes the group's metadata location. Full ARM IDs preserve subscription and resource-group identity and are matched without case sensitivity. Same-name resources in different groups remain distinct.

## Inventory and cleanup coverage

Steward recognizes 34 resource types, with deletion support for 27. Additional ARM resource types appear as read-only inventory.

| Service | Resources | Cleanup |
| --- | --- | --- |
| Compute | VMs, managed disks, snapshots, managed images, availability sets | Supported, with attachment and ownership protections |
| Virtual networks | VNets, subnets, NICs, network security groups, route tables, public IPs, public IP prefixes, NAT gateways | Supported |
| Load balancing | Load balancers and Application Gateways | Supported |
| Storage | Storage accounts and Blob containers | Empty resources only |
| SQL | Databases and elastic pools | Supported; the `master` database is protected |
| PostgreSQL / MySQL | Flexible servers | Supported |
| App Service | Web Apps / Function Apps and App Service plans | Supported as separate resources |
| Containers | Container registries and Container Apps | Supported |
| Operations and identity | Log Analytics workspaces and user-assigned managed identities | Supported |
| Managed or aggregate resources | Resource groups, VM scale sets, private endpoints, SQL logical servers, AKS clusters, Key Vaults, Container Apps environments | Read-only |

Read-only types can contain data or manage other resources whose deletion effects are not yet fully represented in cleanup plans. Their presence does not authorize deleting their managed resources.

## Cleanup protections

- **Management locks:** Subscription, resource-group, resource, and relevant descendant locks block deletion. Steward checks locks during inventory and again immediately before deletion, and never removes them.
- **Managed resources:** Resources in a provider-managed resource group, scale-set VMs, private-endpoint NICs, and other provider-owned resources remain protected.
- **VM attachments:** A VM or NIC with `deleteOption: Delete` is protected. Review the settings in Azure and change them to `Detach` where appropriate, then scan again. Attached managed disks are ordered after their VM.
- **Storage:** A storage account must have no Blob containers, file shares, queues, or tables for its supported services. Blob containers must have no blobs, snapshots, versions, deleted entries, or uncommitted uploads. Legal holds and immutability policies block cleanup. Steward does not empty or purge data to make a resource deletable. Blob cleanup currently requires the standard `ACCOUNT.blob.core.windows.net` endpoint.
- **VNets:** Subnets must be deleted first. Selecting a VNet does not silently delete unselected subnets.
- **App Service:** Deleting an app explicitly preserves its App Service plan. Select the plan separately when it should also be removed.
- **Asynchronous operations:** Steward follows ARM operation-status headers and performs a fresh resource GET to confirm absence. Failed or canceled operations remain failures. Forced deletion and purge options are not enabled.

Review the [cleanup selection and results](./cleanup.md). Database and registry deletion can remove their contained data. Azure permissions, retention settings, dependencies, and concurrent changes can still prevent an action. See Microsoft's [management locks](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/lock-resources), [VM deletion settings](https://learn.microsoft.com/en-us/azure/virtual-machines/delete), and [asynchronous operation behavior](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/async-operations).
