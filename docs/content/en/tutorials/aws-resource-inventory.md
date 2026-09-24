---
title: "Inventory AWS resources across regions with Steward"
description: "Build an AWS resource inventory you can verify: confirm the account, scan regional and global resources, and track coverage gaps."
navTitle: "AWS resource inventory"
---

A useful AWS inventory tells you which account and regions it covers, when resources were observed, and which reads failed. This tutorial uses Steward by LoomX to build that record. It does not run any cleanup.

Steward combines AWS Cloud Control and Resource Explorer discovery. What it finds depends on supported resource types, regional availability, permissions, and Resource Explorer views, so a resource count alone does not prove the whole account is covered. The steps below help you show what is covered and what is not.

## Before you start

- A [self-hosted Steward installation](../installation.md) or [Steward Cloud](https://steward.console.loomx.ai).
- One AWS account in the commercial partition, and an identity authorized to read the resources you want to inspect. Deletion permissions are not needed.
- One resource you know, such as an existing EC2 instance. Write down its account ID, region, type, and native identifier from AWS. You don't need to create anything.
- The [AWS discovery permissions and Resource Explorer setup](../aws.md#configure-discovery-permissions). Have the account administrator fix missing indexes, default views, or permissions before you widen access.

## 1. Confirm the account behind the connection

1. Open the user menu → **Settings** → **Cloud connections** → **Add connection** and choose **AWS**.
2. Pick a credential type, such as an access key for a dedicated IAM identity or a complete session credential with its Session Token and expiration. The [AWS guide](../aws.md#prepare-credentials) lists every type.
3. Name the connection so you can recognize the account, and create it.
4. Check the AWS account identity Steward discovered, then select the connection as the **Active connection**.

**Check the result:** The account ID matches the account that holds your known resource. Steward uses only the credentials you enter: it does not import local AWS profiles or SSO sessions, and does not refresh credentials through AssumeRole. When session credentials expire, use **Replace credential**.

## 2. Scan one region and verify the result

1. Open **Scans** → **Start scan** → **Selected regions** and pick your known resource's region.
2. Leave **Resource kind** empty to scan all indexed kinds in that region, then click **Schedule scan**.
3. On the scan's detail page, check each target and the logs. If a resource type fails, read the permission or provider error, fix it, and click **Retry**.
4. Open **Resources**, search for the native identifier, and open the resource. Compare its region, type, identifier, properties, and last-seen time with AWS and the scan you just ran.

**Check the result:** You found the same resource under the intended account, with a last-seen time from this scan, and the scan has no failed items. A finished scan with failed items still has gaps. If the resource is missing, use the table at the end before you conclude it no longer exists.

## 3. Expand to the regions you actually use

1. In **Settings** → **Cloud connections**, open **Manage regions** for the connection and click **Refresh from cloud API**. Check that the regions you use are marked **Active**.
2. Scan with **All active regions + global**, or choose **Selected regions** and pick the regions you need plus **global**.

Region and VPC scopes do not include global resources; IAM resources, for example, are outside the VPC hierarchy. Resource Explorer's regional default view and its filters also affect what Steward finds.

Each AWS account needs its own connection. Create or select one per account and repeat these checks. Every connection has its own inventory and scans; Steward does not enroll all AWS Organizations accounts automatically or merge them into one inventory view.

**Check the result:** The scan covers every region you use plus global, with no failed items.

## 4. Keep a coverage record

Keep a table like this with your inventory review. It is a suggested manual record, not something Steward exports.

| Account | Scope | Observation | Verification | Gaps |
| --- | --- | --- | --- | --- |
| Your AWS account ID | Regions, and whether global was included | Scan and resource last-seen times | Known resource IDs checked against AWS | Failed items, results excluded by views, or unsupported types |

Add one row per connection. After changes in the cloud, scan the affected scope again. The inventory is a snapshot of what Steward discovered; it does not tell you utilization, exact costs, or whether a resource is safe to delete.

## Investigate missing AWS resources

| Symptom | Next check |
| --- | --- |
| Validation passes but inventory reads fail | Cloud Control actions and the underlying service read permissions. Validation only confirms the identity. |
| One region is missing | Whether the region is enabled in AWS and whether region discovery is authorized. |
| Resource Explorer returns no matches | The regional index, default view, filters, and Search permission. |
| A global resource is absent | Whether global was selected and whether that resource type is supported. |
| The same name appears more than once | Compare the connection, region, resource type, and native ID. |
| Records still contain earlier properties | Compare last-seen times and run a new scan. |

## Next steps

- Credential and API details: [AWS integration reference](../aws.md).
- Network placement: [Resource relationships](../topology.md).
- Before any deletion: [Review cleanup dependencies](./review-cleanup-dependencies.md).
