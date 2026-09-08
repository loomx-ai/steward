---
title: "Alibaba Cloud"
description: "Connect an Alibaba Cloud account, verify inventory permissions, and review cleanup effects."
navTitle: "Alibaba Cloud"
---

An Alibaba Cloud connection represents one cloud identity. Start with an inventory of a known region before expanding the scope or granting cleanup permissions.

## Prepare credentials

In **Settings → Cloud connections → Add connection**, choose **Alibaba Cloud** and the account's **China site** or **International site**. The account site is separate from the resource regions you select later.

| Connection method | Required fields | Considerations |
| --- | --- | --- |
| Access key | AccessKey ID and AccessKey Secret | Use a dedicated RAM identity; replace the connection credential after rotation. |
| STS credentials | AccessKey ID, AccessKey Secret, Security Token, and expiration | All fields must belong to the same issued credential; allow enough lifetime for the scan or task. |
| Browser authorization | Complete sign-in and authorization in the browser | Select the matching site, confirm the identity, and return to Steward. |

**Replace credential** requires the original cloud identity. Create another connection when changing accounts, so their inventory stays separate. See [Cloud connections](./connections.md).

## Configure inventory permissions

Confirm that **Resource Center** is enabled for the target account. If needed, an account administrator can activate it in the Resource Management console and wait for the initial resource collection. See Alibaba Cloud's [activation guide](https://www.alibabacloud.com/help/en/resource-management/resource-center/user-guide/activate-resource-center).

Steward combines Resource Center with service-specific queries. Grant RAM read permissions for the types you scan:

| Purpose | Common RAM actions | Usage |
| --- | --- | --- |
| Resource Center search | `resourcecenter:SearchResources` | Find accessible resources in the current account. |
| Resource configurations | `resourcecenter:GetResourceConfiguration` | Authorizes `BatchGetResourceConfigurations`; the permission and API names differ. |
| Region and network selection | `vpc:DescribeRegions`, `vpc:DescribeVpcs`, `vpc:DescribeVSwitches` | Discover regions, VPCs, and vSwitches. |
| ECS inventory | `ecs:DescribeInstances` and reads for other selected types | Instances, disks, and interfaces use their corresponding service APIs. |
| Other services | The service's List, Get, and Describe permissions | Additional types require additional reads; there is no fixed minimum policy for every service. |

This table identifies common permissions, rather than a complete policy. Check the RAM authorization tables for [SearchResources](https://www.alibabacloud.com/help/en/resource-management/resource-center/developer-reference/api-resourcecenter-2022-12-01-searchresources) and [BatchGetResourceConfigurations](https://www.alibabacloud.com/help/en/resource-management/resource-center/developer-reference/api-resourcecenter-2022-12-01-batchgetresourceconfigurations).

A successful connection check establishes the cloud identity. Run a scan to verify access to the resources themselves.

## Run your first scan

1. Create and validate the connection. Confirm its account identity and give it a recognizable name, such as “Alibaba Cloud · Test”.
2. Refresh regions and locate one containing known resources, such as `cn-hangzhou`.
3. Switch to that connection and open **Scans**. Start with the selected region, or a known VPC or vSwitch.
4. Review failed types and permission errors in the scan details, then search **Resources** for a known instance ID.
5. Confirm its region, VPC, vSwitch, and last-seen time. Expand to other regions and global resources after this check succeeds.

**Success check:** The expected resource appears under the right connection, its attributes match the Alibaba Cloud console, and the scan has no unresolved failures. Follow [Your first inventory](./tutorials/first-inventory.md) for the complete workflow.

## Resource coverage and relationships

Common types include ECS instances, disks, and interfaces; VPCs, vSwitches, security groups, NAT gateways, and EIPs; load balancers; RDS, Redis, and PolarDB; and storage, container, and orchestration resources. Discoverability depends on supported types, selected regions, and the current identity's permissions.

Use **Resource panorama** to navigate regions, VPCs, and vSwitches, and resource details to inspect relationships. A single regional view is insufficient to establish whether a cross-region connection or global resource remains in use. Scan the relevant regions and dependencies before reviewing cleanup.

Resource Center has collection delays, and service queries can be incomplete because of permissions or rate limits. A resource missing from one scan is not proof that it no longer exists in the cloud.

## Review cleanup effects

Inventory permissions do not authorize cleanup. Deletion also requires the resource's delete and status-read permissions, plus any detach or preparation operations in the plan.

- **ECS instances:** Supported deletion flows may forcibly stop and release an instance. Handling deletion protection may also modify instance attributes. Review effects on the instance and related resources; cloud-side deletion protection is not the sole execution barrier.
- **VPCs and vSwitches:** Review instances, interfaces, gateways, and other dependencies. Resolve blockers caused by dependencies outside the selection.
- **OSS buckets:** Objects, versions, retention settings, and other provider conditions may prevent deletion. Follow the review findings and OSS response.
- **Managed resources:** For clusters and resource stacks, review the controller and its managed objects to understand the full effect.

Creating a cleanup task does not delete resources. Cloud operations begin after [reviewing the plan](./cleanup.md) and confirming execution.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| Connection validation fails | Check the account site, matching access keys, STS token, and expiration; inspect the provider error code. |
| `ServiceNotEnabled` | Enable Resource Center and allow its initial collection to complete. |
| Validation passes but scans return `NoPermission` or `Forbidden` | Find the failing service and API in scan details, grant the required read, and retry. |
| Missing resources or relationships | Verify the connection, region, and scope; resolve failed items and rescan new resources after a delay. |
| Cleanup cannot finish | Inspect blockers, provider error codes, and request IDs; check dependencies, state, and operation permissions. |

Next: [Scan resources](./scans.md) · [Query resources](./resources.md) · [Resource relationships](./topology.md)
