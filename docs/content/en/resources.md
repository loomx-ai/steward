---
title: "Resource inventory"
description: "Find resources by name or ID, then check their properties and last-seen time."
navTitle: "Resource inventory"
---

<span id="find"></span>

## Find a resource

Open Resources and check the selected connection at the top left. Simple search accepts names, native resource IDs, and types, such as api-01 or i-demo-api01.

<figure class="docs-figure"><a href="../../assets/inventory-en.png" target="_blank" rel="noreferrer" aria-label="Inventory lists names, types, regions, and last-seen times (open full size)"><img src="../../assets/inventory-en.png" alt="Inventory lists names, types, regions, and last-seen times" width="1440" height="960"></a><figcaption>Inventory lists names, types, regions, and last-seen times <span>· Sample data · click to enlarge</span></figcaption></figure>

<span id="query"></span>

## Combine query conditions

Use the mode button on the left of the search field to switch to advanced queries. Use field suggestions as you type, then press Enter to apply.

```
name contains "api" and region = "ap-southeast-1"
```

Advanced queries support and, or, not, and parentheses. Use the input suggestions for fields and values. The public website demo supports simple search only; use a local server or Steward Cloud for advanced queries.

<span id="details"></span>

## Read resource details

Open a resource by its name. Overview shows properties and tags; Relationships shows connected resources. View in resource panorama locates it in its network.

Last seen is the scan observation time, not a live status. Before deleting anything, check its account, region, and native ID.

<span id="selection"></span>

## Add to the cleanup list

Select resources and choose Add to cleanup list. This saves targets for review. Deletion starts only after you create a task, review it, and confirm execution.

Mark as dirty excludes a record you have verified is invalid; cleanup ignores it. It does not delete the cloud resource and should not be used to bypass a dependency blocker.
