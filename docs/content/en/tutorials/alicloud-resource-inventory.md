---
title: "Inventory Alibaba Cloud resources across regions with Steward"
description: "Verify account identity and Resource Center, scan regional and global resources, and record gaps in your Alibaba Cloud inventory."
navTitle: "Alibaba Cloud inventory"
---

Use this provider-specific walkthrough when you manage Alibaba Cloud. For the default English example, follow [AWS resource inventory](./aws-resource-inventory.md).

A useful inventory identifies the account, regions, observation time, and failed reads. Steward combines Resource Center with cloud product APIs; coverage depends on supported types, permissions, regions, and collection state. A resource count alone cannot establish complete account coverage.

## Before you start

- [Install and start Steward](../quick-start.md), or open a Steward Cloud workspace.
- Prepare an authorized RAM identity's AccessKey pair or complete STS temporary credentials.
- Record one existing ECS instance's account, region, and native ID from the Alibaba Cloud console. You do not need to create resources.
- Confirm that Resource Center is enabled and check the [Alibaba Cloud inventory permissions](../alicloud.md). Read permissions are sufficient for this exercise.

## 1. Verify the connected account

Open the user menu → Settings → Cloud connections → Add connection. Choose **Alibaba Cloud** and the **China** or **International** account site. The account site is separate from the resource region.

Enter the AccessKey ID and Secret. For STS, also enter the Security Token and expiration from the same issuance. Browser authorization is another option when available in the interface. Validate the identity and give the connection a recognizable name, such as “Alibaba Cloud · Test”.

**Result check:** The connection belongs to the account that owns your known instance, and its region list includes the target region. Identity validation does not prove that every resource API is readable.

## 2. Scan one known region

Select the connection and open **Scans**. Start with the region containing your known instance, such as `cn-hangzhou`, or a known VPC or vSwitch for a smaller check.

Review target status and logs. Resolve permission, throttling, or service errors before retrying. A finished task with failed targets still leaves inventory gaps.

In **Resources**, search by the instance's native ID. Compare its type, region, VPC, vSwitch, properties, and last-seen time with the Alibaba Cloud console.

**Result check:** The expected instance appears under the correct connection and region, key properties match, and relevant scan targets have no unresolved failures.

## 3. Expand and record coverage

After the first check succeeds, scan other enabled regions and the global resources you need. Check regional and global scopes separately; one successful regional scan cannot establish account-wide coverage.

Use a separate connection for each additional account and repeat the identity and resource checks. Replacing credentials maintains the existing identity; changing accounts requires a new connection.

Record the account, included regions, global coverage, scan time, verified resource IDs, and outstanding gaps in your team's existing record system.

**Result check:** Another operator can reproduce the check and distinguish verified coverage from failed reads or collection delays.

## Troubleshoot missing resources

| Symptom | Check first |
| --- | --- |
| `ServiceNotEnabled` | Resource Center activation and completion of its initial collection. |
| Validation succeeds but scans report permission errors | The failed API and RAM action; check both Resource Center and product read permissions. |
| An instance is absent | Account, region, selected type, and scan scope; search by native ID. |
| A new resource or property is missing | Resource Center collection delay, product API errors, and a later scan. |
| A region has no data | Region discovery, selected scope, read permissions, and target status. |
| Relationships are incomplete | Whether related resources were scanned and their types are supported. |

See the [Alibaba Cloud integration reference](../alicloud.md) for credentials and APIs, [resource relationships](../topology.md) for network context, and [cleanup dependency review](./review-cleanup-dependencies.md) before considering deletion.
