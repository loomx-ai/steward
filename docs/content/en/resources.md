---
title: "Resource inventory"
description: "Find a resource by name, ID, or query, check its properties and last-seen time, and queue it for cleanup."
navTitle: "Resource inventory"
---

Use **Resources** to find a specific resource, check what Steward recorded about it, and queue it for cleanup review.

<span id="find"></span>

## Find a resource

1. Check the **Active connection** at the top left. **Resources** lists only that connection's resources.
2. Open **Resources** and type a name, native resource ID, or type in the search field, such as `api-01` or `i-demo-api01`.

Search by native resource ID when the same name is used in several accounts or regions.

<figure class="docs-figure"><a href="../../assets/inventory-en.png" target="_blank" rel="noreferrer" aria-label="Inventory lists names, types, regions, and last-seen times (open full size)"><img src="../../assets/inventory-en.png" alt="Inventory lists names, types, regions, and last-seen times" width="1440" height="960"></a><figcaption>Inventory lists names, types, regions, and last-seen times <span>· Sample data · click to enlarge</span></figcaption></figure>

<span id="query"></span>

## Combine query conditions

1. Click the mode button on the left of the search field to switch to advanced query.
2. Type a query. Suggestions for fields and values appear as you type.
3. Press Enter to apply it.

```
name contains "api" and region = "ap-southeast-1"
```

Advanced queries support `and`, `or`, `not`, and parentheses. The public website demo supports simple search only; use a local server or Steward Cloud for advanced queries.

<span id="details"></span>

## Read resource details

Click a resource's name to open its details:

- **Overview** shows properties and tags.
- **Relationships** shows connected resources.
- **View in resource panorama** shows where the resource sits in its network.

**Last seen** is the time a scan last read the resource, not a live status. If it is older than recent changes, [scan again](./scans.md#freshness). Before you delete anything, check its account, region, and native ID.

<span id="selection"></span>

## Queue resources for cleanup

1. Select the rows you want to clean up. The header checkbox selects every available resource on the page.
2. Click **Add to resource list (N)**. The rows are marked **Pending cleanup**. If every selected row is already listed, the button changes to **Remove from resource list (N)**.
3. Click **Resource list** in the bottom-right corner to review the queued targets, then click **Create cleanup task**.

The resource list only collects targets. Nothing is deleted until you create the task, review it, and click **Start cleanup**. See [Resource cleanup](./cleanup.md).

### Mark as dirty resource

If you have verified that a record is invalid, click **Mark as dirty resource** on its row or details page. Cleanup ignores dirty resources. The mark does not delete the cloud resource, and you should not use it to get around a dependency blocker. To undo it, click **Remove dirty resource mark**.
