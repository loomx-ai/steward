---
title: "Inventory Alibaba Cloud resources across regions with Steward"
description: "Verify the account and Resource Center, scan regional and global resources, and record the gaps in your Alibaba Cloud inventory."
navTitle: "Alibaba Cloud inventory"
---

Use this walkthrough when you manage Alibaba Cloud. For the default English example, follow [AWS resource inventory](./aws-resource-inventory.md).

A useful inventory tells you which account and regions it covers, when resources were observed, and which reads failed. This tutorial builds that record with Steward by LoomX. It does not run any cleanup.

Steward combines Resource Center with cloud product APIs. What it finds depends on supported types, permissions, regions, and Resource Center's collection state, so a resource count alone does not prove the whole account is covered.

## Before you start

- [Install and start Steward](../quick-start.md), or open a Steward Cloud workspace.
- An AccessKey pair for an authorized RAM identity, or a complete set of STS temporary credentials.
- One existing ECS instance. Write down its account, region, and native ID from the Alibaba Cloud console. You don't need to create anything.
- Resource Center enabled, and the [Alibaba Cloud inventory permissions](../alicloud.md). Read permissions are enough for this exercise.

The `api-01` record in the screenshots is sample data. Use an instance ID from your own account.

## 1. Verify the connected account

1. Open the user menu → **Settings** → **Cloud connections** → **Add connection** and choose **Alibaba Cloud**.
2. Choose the account's **China site** or **International site**. The site is the account system, not a resource region.
3. Enter the AccessKey ID and AccessKey Secret. For STS, also enter the Security Token and expiration from the same issuance. Alternatively, use **Sign in with your browser** if the interface offers it.
4. Give the connection a recognizable name, such as "Alibaba Cloud · Test", and create it. Steward validates the identity first.

**Check the result:** The connection belongs to the account that owns your known instance, and its region list includes the target region. Validation confirms the identity only, not that every resource API is readable.

## 2. Scan one known region

1. Select the connection as the **Active connection**.
2. Open **Scans** → **Start scan**, choose **Selected regions**, and pick the region that holds your instance, such as `cn-hangzhou`. For a smaller check, choose **Selected VPCs / vSwitches** and pick a known VPC or vSwitch instead.
3. Click **Schedule scan** and check the target status and logs. Fix permission, throttling, or service errors, then click **Retry**.
4. In **Resources**, search for the instance's native ID. Compare its type, region, VPC, vSwitch, properties, and last-seen time with the Alibaba Cloud console.

**Check the result:** The instance appears under the correct connection and region, its key properties match, and the relevant scan items have no unresolved failures. A finished scan with failed items still has gaps.

## 3. Expand and record coverage

1. Scan the other enabled regions and the global resources you need, for example with **All active regions + global**. Check regional and global results separately; one successful regional scan does not cover the whole account.
2. For another Alibaba Cloud account, create a separate connection and repeat the identity and resource checks. **Replace credential** only renews credentials for the same identity; a different account needs a new connection.
3. Record the result in your team's usual record system:

| Account | Scope | Observation | Verified samples | Remaining gaps |
| --- | --- | --- | --- | --- |
| Account identity | Regions, and whether global was included | Scan and last-seen times | Resource IDs checked in Alibaba Cloud | Failed types, permission gaps, or collection delays |

**Check the result:** Another operator can repeat the check with the same account and resource IDs, and can tell verified coverage apart from failed reads or collection delays.

## Troubleshoot missing resources

| Symptom | Check first |
| --- | --- |
| `ServiceNotEnabled` | Whether Resource Center is enabled and its initial collection has finished. |
| Validation succeeds but scans report permission errors | The failed API and its RAM action. Check both Resource Center and product read permissions. |
| An instance is absent | Account, region, selected type, and scan scope; search by native ID. |
| A new resource or property is missing | Resource Center collection delay and product API errors; scan again later. |
| A region has no data | Region discovery, the selected scope, read permissions, and target status. |
| Relationships are incomplete | Whether the related resources were scanned and their types are supported. |

## Next steps

- Credentials, Resource Center, and APIs: [Alibaba Cloud integration reference](../alicloud.md).
- Network placement: [Resource relationships](../topology.md).
- Before any deletion: [Review cleanup dependencies](./review-cleanup-dependencies.md).
