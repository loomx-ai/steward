---
title: "Inventory AWS resources across regions with Steward"
description: "Build a verifiable AWS resource inventory: confirm the account, scan regional and global resources, and investigate coverage gaps."
navTitle: "AWS resource inventory"
---

An AWS inventory is useful when you can explain which account and regions it covers, when the resources were observed, and which reads failed. This tutorial uses Steward by LoomX to build that record without executing cleanup.

Steward combines AWS Cloud Control and Resource Explorer discovery. Coverage depends on supported resource types, regional availability, permissions, and Resource Explorer views. A resource count alone is not proof of complete account coverage.

## Before you start

- Use a [self-hosted Steward installation](../installation.md) or [Steward Cloud](https://steward.console.loomx.ai).
- Choose one AWS account in the commercial partition and an identity authorized to read the resources you want to inspect.
- Record one known resource's account ID, region, type, and native identifier from AWS. An existing EC2 instance is a useful starting point; no new resource is required.
- Check the [AWS discovery permissions and Resource Explorer setup](../aws.md#configure-discovery-permissions). Have the account administrator resolve missing indexes, default views, or permissions before broadening access.

Deletion permissions are not required for this inventory exercise. Connection validation identifies the account; individual scans determine whether the required resource APIs are readable.

## 1. Confirm the account behind the connection

Open the user menu → Settings → Cloud connections → Add connection, then choose AWS. Supply a dedicated access key pair or a complete temporary credential set, including its Session Token and expiration. Name the connection so you can recognize the account.

After validation, check the discovered AWS account identity. Return to the main interface and select that connection before scanning.

**Check:** The account ID matches the account containing your known resource. Steward does not automatically import local AWS profiles or SSO sessions, or refresh AssumeRole credentials. Replace expired temporary credentials as described in the [AWS guide](../aws.md#prepare-credentials).

## 2. Scan one region and verify the result

Open Scans → Start scan → Selected regions. Choose the known resource's region. Leave the resource type selection empty to use all indexed types within that scope, then open the submitted task.

Inspect target completion and logs. If a resource type fails, examine the specific permission or provider error before retrying. A completed task with failed targets still has inventory gaps.

Open Resources, search for the native identifier, and inspect the matching resource. Compare its region, type, identifier, properties, and last-seen time with AWS and the scan you just ran.

**Check:** You have found the same resource under the intended account, with a last-seen time from this scan. If it is missing, work through the checks below before concluding that it no longer exists.

## 3. Expand to the regions you actually use

Refresh the connection's regions and check the enabled regions. Once the first region is working, use All active regions + global, or explicitly select the required regions and Global.

Regional scopes and VPC scopes do not cover every global resource. For example, IAM resources are outside the VPC hierarchy. Resource Explorer's regional default view and filters also affect discovery.

For another AWS account, create or select a separate connection and repeat these checks. Each connection has its own inventory and scan tasks; this workflow does not automatically enroll every AWS Organizations account or merge them into one inventory view.

## 4. Keep a coverage record

Record the results in a table you maintain alongside your inventory review. This is a suggested review record, not an automatic Steward export.

| Account | Scope | Observation | Verification | Gaps |
| --- | --- | --- | --- | --- |
| Your AWS account ID | Regions and whether Global was included | Scan task and resource last-seen times | Known resource IDs checked against AWS | Failed targets, excluded view results, or unsupported types |

Repeat this row per connection. After cloud-side changes, scan the affected scope again. The inventory is a discovery snapshot; it does not establish utilization, exact costs, or that a resource is safe to delete.

## Investigate missing AWS resources

| Symptom | Next check |
| --- | --- |
| Validation passes but inventory reads fail | Cloud Control actions and the underlying service read permissions. Identity validation is narrower than resource discovery. |
| One region is missing | Whether it is enabled in AWS and whether region discovery is authorized. |
| Resource Explorer returns no matches | The regional index, default view, filters, and Search permission. |
| A global resource is absent | Whether Global was selected and that resource type is supported. |
| The same name appears more than once | Compare the connection, region, resource type, and native ID. |
| Records still contain earlier properties | Compare last-seen times and run a fresh scan. |

For precise credential and API requirements, use the [AWS integration reference](../aws.md). To inspect network placement, continue with [resource relationships](../topology.md). Before any deletion, follow [Review cleanup dependencies](./review-cleanup-dependencies.md).
