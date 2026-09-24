---
title: "Complete your first resource inventory"
description: "Connect a cloud account, scan one region, find a resource you know, and check its identity, freshness, and network relationships."
navTitle: "First resource inventory"
---

## What you will accomplish

You will connect a cloud account, scan one region, find a resource you already know, and inspect its network placement and related resources. At the end you can say which account the resource belongs to, when Steward last saw it, and which dependencies still need checking.

This tutorial only connects, scans, and reads; it deletes nothing. Screenshots use AWS sample data. Steward does not create the sample resources such as `api-01`; use your own region and resource ID.

## Before you start

You need:

- Steward running through the [quick start](../quick-start.md), or a Steward Cloud workspace.
- An AWS, Alibaba Cloud, Google Cloud, or Azure account with credentials that have the required read permissions. Deletion permissions are not needed.
- A regional resource you know exists and that Steward can discover, such as an EC2 instance. Write down its **region, resource type, and native resource ID**.

Pick a small scope you know well. Without a cloud account, you can try the sample demo on the [LoomX homepage](https://loomx.ai/) to learn the inventory and relationship views; a real scan needs your own connection.

## 1. Add and select a cloud connection

1. Open the user menu → **Settings** → **Cloud connections** → **Add connection** and choose **AWS**.
2. Enter the credentials, give the connection a name you will recognize, and create it. Steward validates the credentials first. See [Cloud connections](../connections.md#credentials) for credential types.
3. Check that the connection's regions match your account.
4. Return to the main interface and select the connection as the **Active connection** at the top left.

**Check the result:** The active connection is the account you want to inspect, validation passed, and your resource's region is listed. If validation fails, check the credentials, their expiration, and the error details before you continue.

## 2. Scan the resource's region

1. Open **Scans** → **Start scan**.
2. Choose **Selected regions** and pick the region that contains your resource. The example uses `ap-southeast-1`.
3. Leave **Resource kind** empty to scan all indexed resource kinds in that region.
4. Click **Schedule scan**. The scan's detail page opens.

Global resources are not part of any region. To include them, also pick **global**.

<figure class="docs-figure"><a href="../../../assets/scan-en.png" target="_blank" rel="noreferrer" aria-label="Check the scan's scope, progress, and resource counts (open full size)"><img src="../../../assets/scan-en.png" alt="Scan details show target progress, discovered resource counts, and execution logs" width="1440" height="960"></a><figcaption>Check the target scope first, then completion and failure logs. Resource counts in this screenshot come from sample data.</figcaption></figure>

**Check the result:** Every target has finished and none has failed. If some fail, fix the cause in the log and click **Retry**. See [what a failed item means](../scans.md#progress).

## 3. Find and identify the resource

1. Open **Resources** and search for the resource's native ID. The ID tells apart resources that share a name.
2. Click the matching resource to open its details.

<figure class="docs-figure"><a href="../../../assets/inventory-en.png" target="_blank" rel="noreferrer" aria-label="Verify resource identity and the last-seen time (open full size)"><img src="../../../assets/inventory-en.png" alt="The inventory lists resource names, native IDs, types, regions, and last-seen times" width="1440" height="960"></a><figcaption>Use the resource ID and region to confirm identity, and the last-seen time to judge freshness. The name is only a label.</figcaption></figure>

Compare the record with your cloud console or your notes:

1. The active connection, region, and resource type are correct.
2. The native resource ID matches, and the name and key properties look right.
3. The last-seen time matches the scan you just ran.

**Check the result:** One Steward record matches the same resource in the cloud. If it is missing, check the connection, region, and failed scan items before you conclude that the resource no longer exists.

## 4. Inspect network placement and relationships

1. In the resource details, click **View in resource panorama**. Check the region, VPC, and subnet.
2. Click **Show relationship lines** in the canvas toolbar, or open the **Relationships** tab in the resource details.

<figure class="docs-figure"><a href="../../../assets/relationships-en.png" target="_blank" rel="noreferrer" aria-label="Two sample instances share a load balancer and security group (open full size)"><img src="../../../assets/relationships-en.png" alt="In the sample graph, public-gateway routes to api-01 and api-02, which both use the application-policy security group" width="1440" height="960"></a><figcaption>The sample instances share a load balancer and security group. Note shared relationships before you scope any later cleanup review.</figcaption></figure>

For a resource outside a VPC, read its properties and relationships in the details view. Your account does not need to match the network hierarchy in the screenshot.

**Check the result:** You can describe where the resource sits and which related resources Steward found. If no lines appear, check that the related resources were scanned; see [Use the relationships](../topology.md#interpret).

## 5. Record your inventory result

Write a short record for your resource:

| Item | What to record |
| --- | --- |
| Resource identity | Connection, region, resource type, and native resource ID |
| Observation time | Last-seen time and the scan it came from |
| Scan scope | Regions and resource kinds covered, and any gaps |
| Relationships | Shared resources you confirmed, and dependencies still to verify |

You now have an inventory you can verify. After changes in the cloud, scan the affected scope again before you compare records.

## When results differ from expectations

| Symptom | What to check first |
| --- | --- |
| The connection works, but one resource type fails to scan | Read permissions for that resource's API. Connection validation does not check every resource permission. |
| A known resource is missing from search | The active connection, region, resource type, and the scan items for that target |
| Properties still show an earlier value | The last-seen time; scan again, then refresh the page |
| An expected relationship line is missing | Whether the related resources were scanned and whether Steward's relationship rules cover that relationship |

## Next steps

- Learn advanced queries in [Resource inventory](../resources.md#query).
- Before any cleanup, read the [blocker example](../cleanup.md#review). This tutorial does not require deleting anything.
