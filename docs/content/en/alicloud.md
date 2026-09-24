---
title: "Alibaba Cloud"
description: "Connect an Alibaba Cloud account, enable Resource Center, grant RAM permissions, run a first scan, and check resource coverage and cleanup behavior."
navTitle: "Alibaba Cloud"
---

Use this page to connect an Alibaba Cloud account to Steward, grant the RAM permissions it needs, and understand what it can find and clean up.

An Alibaba Cloud connection represents one cloud identity. Steward combines Resource Center search with service-specific API queries. Start with an inventory of one known region, then expand the scope or add cleanup permissions.

## Prepare credentials

Open the user menu → **Settings** → **Cloud connections** → **Add connection**, choose **Alibaba Cloud**, and pick the **Site** that matches the account: **China site** or **International site**. The site is separate from the resource regions you scan later.

| Credential type | Required fields | Considerations |
| --- | --- | --- |
| **Alibaba Cloud access key** | AccessKey ID and AccessKey Secret | Use a dedicated RAM identity; replace the connection credential after each rotation. |
| **Alibaba Cloud STS** | AccessKey ID, AccessKey Secret, Security Token, and expiration | All fields must come from the same issued credential. Leave enough lifetime for the scan or cleanup task. |
| **OAuth** | None; select **Sign in with your browser** | [Browser sign-in](./connections.md#browser): sign in and authorize on the matching site, confirm the identity, then return to Steward. |
| **OIDC workload identity** | Role ARN and OIDC provider ARN; optional write role ARN | Temporary credentials without stored keys, when the server is configured for [OIDC](./oidc.md). |

- **Replace credential** only accepts a credential for the original cloud identity. To connect a different account, create another connection so each account's inventory stays separate. See [Cloud connections](./connections.md).

## Configure permissions

A successful connection check only proves the cloud identity. Grant the read permissions below, then run a scan to confirm Steward can read the resources themselves.

### Enable Resource Center

Make sure **Resource Center** is enabled for the account. If it is not, an account administrator can activate it in the Resource Management console; wait for the initial resource collection to finish before scanning. See Alibaba Cloud's [activation guide](https://www.alibabacloud.com/help/en/resource-management/resource-center/user-guide/activate-resource-center).

### Read permissions for inventory

Grant RAM read permissions for the types you scan:

| Purpose | RAM actions | Why Steward needs them |
| --- | --- | --- |
| Resource Center search | `resourcecenter:SearchResources` | Finds accessible resources in the current account. |
| Resource configurations | `resourcecenter:GetResourceConfiguration` | Authorizes the `BatchGetResourceConfigurations` API (the permission and API names differ). |
| Regions and network selection | `vpc:DescribeRegions`, `vpc:DescribeVpcs`, `vpc:DescribeVSwitches` | Discovers regions, VPCs, and vSwitches. |
| ECS inventory | `ecs:DescribeInstances`, plus reads for the other types you select | Instances, disks, and network interfaces each use their own service API. |
| Database encryption keys | `rds:DescribeDBInstanceTDE`, `rds:DescribeDBInstanceEncryptionKey`, `polardb:DescribeDBClusterTDE`, `kvstore:DescribeInstanceTDEStatus`, `kvstore:DescribeEncryptionKey` | Identifies the KMS keys that RDS, PolarDB, and Redis use. Without them, scans of those types fail, and deleting a KMS key stays blocked until a complete scan succeeds. |
| RAM membership and attachments | `ram:ListUsersForGroup`, `ram:ListEntitiesForPolicy` | Identifies group members and the principals of custom policies, so the connection's own groups and policies stay protected. |
| Other services | The service's List, Get, and Describe permissions | Each additional type needs its own reads; there is no fixed minimum policy that covers every service. |

This table lists common permissions, not a complete policy. For the two Resource Center permissions, check the RAM authorization tables for [SearchResources](https://www.alibabacloud.com/help/en/resource-management/resource-center/developer-reference/api-resourcecenter-2022-12-01-searchresources) and [BatchGetResourceConfigurations](https://www.alibabacloud.com/help/en/resource-management/resource-center/developer-reference/api-resourcecenter-2022-12-01-batchgetresourceconfigurations).

### Permissions for cleanup

Inventory permissions do not allow cleanup. For each type you plan to delete, also grant:

- The delete action and the status read that confirms the deletion.
- Any detach or preparation operations the cleanup task lists.
- The operation that turns off [deletion protection](#deletion-protection), such as `ecs:ModifyInstanceAttribute` for ECS instances or `rds:ModifyDBInstanceDeletionProtection` for RDS.

## Run your first scan

Start small: one region with a resource you know, then expand.

1. Add the connection and give it a recognizable name, such as “Alibaba Cloud · Test”. Steward validates it before saving; confirm the account identity it shows.
2. On the connection, open **Manage regions** → **Refresh from cloud API**, and find a region with known resources, such as `cn-hangzhou`.
3. Switch to that connection and go to **Scans** → **Start scan**. Choose **Selected regions** and pick that region, or choose **Selected VPCs / vSwitches** and pick a known VPC or vSwitch.
4. Open the scan and review failed types and permission errors. Fix them, then select **Retry**.
5. In **Resources**, search for a known instance ID. Check its region, VPC, vSwitch, and last-seen time.

**Result check:** the expected resource appears under the right connection, its attributes match the Alibaba Cloud console, and the scan has no unresolved failures. Then expand to other regions and global resources.

For the complete workflow, follow [Your first inventory](./tutorials/first-inventory.md). For account and region coverage checks, follow [Alibaba Cloud resource inventory](./tutorials/alicloud-resource-inventory.md).

## Resource coverage

Steward identifies 203 Alibaba Cloud resource types, 177 of which have a native cleanup action. Other types that Resource Center returns appear as read-only inventory. Every service API call is pinned to the official metadata published at api.aliyun.com, and each list and read response path is checked against the official response schema and example. Coverage is still growing and does not yet include every Alibaba Cloud product.

Common types:

| Category | Resource types |
| --- | --- |
| Compute and applications | ECS instances, disks, and network interfaces; SAE; EMR; Elastic Desktop Service |
| Networking | VPCs, vSwitches, security groups, NAT gateways, EIPs |
| Load balancing | ALB, NLB, and CLB instances with their listeners and server groups |
| Databases | RDS, Redis, PolarDB |
| Messaging and events | Kafka, RocketMQ, and RabbitMQ with their topics and consumer groups; EventBridge |
| Storage, logs, and images | OSS, Simple Log Service, Container Registry |
| Monitoring and governance | CloudMonitor, Cloud Config |

What a scan finds depends on the supported types, the regions you select, and the current identity's permissions.

## Relationships

Use **Resource Panorama** to navigate regions, VPCs, and vSwitches, and open a resource's **Relationships** tab to inspect its dependencies. Beyond network placement, Steward reads:

- **Encryption keys:** a resource that names a KMS key as its encryption key, such as an ECS disk or snapshot, an OSS bucket with server-side encryption, or an RDS, PolarDB, or Redis instance, is shown as using that key. Deleting the key would make the resource's data unreadable. For RDS, PolarDB, and Redis, Steward reads the transparent data encryption key through the [database encryption-key permissions](#read-permissions-for-inventory).
- **RAM membership:** which users belong to each RAM group and which principals each custom policy is attached to, so a group or policy in use does not look unused.

A single region's view cannot show whether a cross-region connection or a global resource is still in use. Before you review cleanup, scan the relevant regions and their dependencies.

## Cleanup behavior and protections

Creating a cleanup task does not delete anything. Cloud operations begin only after you [review the task](./cleanup.md#review) and confirm execution.

<span id="deletion-protection"></span>

### Deletion protection

Cloud-side deletion protection does not stop a cleanup task you confirm: Steward turns it off right before deleting the resource. Your review of the task is the safeguard. Steward turns off deletion or release protection for:

ECS instances, ACK clusters, Auto Scaling groups, ALB, NLB, and CLB instances, EIPs, Internet Shared Bandwidth, NAT gateways, RDS, PolarDB, Redis, MongoDB, HBase, Lindorm, KMS keys, ROS stacks, Simple Log Service projects, EMR clusters.

### What deletion does, by service

| Resource | Behavior |
| --- | --- |
| ECS instances | Deletion force-stops a running instance and releases it. Review the effect on the instance and its related resources. |
| VPCs and vSwitches | Review instances, network interfaces, gateways, and other dependencies. Resolve blockers caused by dependencies outside your selection. |
| OSS buckets | Objects, versions, retention settings, and other provider conditions can prevent deletion. Follow the review findings and the OSS response. |
| ALB, NLB, and CLB instances | Listeners and CLB virtual server groups are deleted first, each as its own step. Server groups, CLB access control lists, and certificates are independent resources: the provider rejects deleting one that a listener or forwarding rule still uses. Server groups managed by an ALB Ingress controller are not deleted directly. |
| Kafka, RocketMQ 4.0, and RocketMQ 5.0 instances | Topics and consumer groups are deleted first, each as its own step, confirmed while the instance can still be queried. RocketMQ 4.0 topics are deleted only when this account owns them, never when another account authorized them. Deleting a topic discards its messages. |
| Container Registry namespaces | Deleting a namespace also deletes its repositories and images, so the task deletes each repository first. |
| Simple Log Service projects | Deleting a project deletes all its logstores, including service-created `internal-` logstores. The task lists them as deleted with the project and confirms each one afterward. |
| VPN gateways | SSL-VPN client certificates, SSL-VPN servers, and IPsec servers are deleted before their VPN gateway. |
| CloudMonitor | Custom application groups, alert rules, alert contacts, and contact groups can be cleaned up. Application groups synchronized from another service, tags, or resource groups are recreated by their source, so they are not deleted directly. Alert rules that name a deleted contact or contact group stop notifying it. |
| SAE | Applications are deleted before their namespace; each region's default namespace is kept. Application deletion is asynchronous; Steward waits up to 10 minutes to confirm it. |
| Cloud Config | Rules and compliance packs are inventoried in `cn-shanghai` and `ap-southeast-1`. Deleting a compliance pack also deletes the rules it created, which the task confirms afterward; those rules cannot be deleted on their own. Aggregators are inventoried only. |
| Elastic Desktop Service | Only pay-as-you-go desktops outside desktop pools are cleaned up; subscription desktops are released when they expire. An office network is deleted only after all its desktops are released. System policies are kept. |
| EventBridge | Event rules are deleted before their event bus. |
| RabbitMQ | Only pay-as-you-go instances are deleted; subscription instances are released when they expire. |
| EMR clusters | Only pay-as-you-go clusters are cleaned up. Release protection is turned off first. The cluster counts as deleted once it reports `TERMINATED`; Steward waits up to 10 minutes. |
| Clusters, resource stacks, and other managed resources | Review the controller and the objects it manages to understand the full effect. |

## Data freshness

Resource Center collects changes with a delay, so new resources appear in Steward only after Resource Center has recorded them. Service queries can also return incomplete results because of permissions or rate limits. A resource missing from one scan is not proof that it no longer exists in the cloud; check the scan's failed items before you draw conclusions.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| Connection validation fails | Check the account site, that the AccessKey pair matches, the STS token, and expiration. Read the provider error code in the error details. |
| `ServiceNotEnabled` | [Enable Resource Center](#enable-resource-center) and let its initial collection finish. |
| Validation passes but scans return `NoPermission` or `Forbidden` | Find the failing service and API in the scan details, grant that read, and select **Retry**. |
| Resources or relationships are missing | Check the connection, region, and scan scope, and resolve failed items. For new resources, rescan after Resource Center has collected them. |
| A KMS key cannot be deleted | Check that the database encryption-key reads are granted and the latest scan of RDS, PolarDB, and Redis completed. |
| Cleanup cannot finish | Check the task's blockers, provider error codes, and request IDs; then check dependencies, resource state, and operation permissions. |

## Next steps

- [Scan resources](./scans.md)
- [Query resources](./resources.md)
- [Resource relationships](./topology.md)
- [Clean up resources](./cleanup.md)
