---
title: "Scanning"
description: "Scans read provider APIs to update inventory and relationships."
navTitle: "Scanning"
---

<span id="scope"></span>

## Choose a scope

Select a connection, then open Scans → Start scan. Begin with one region to check permissions and results before expanding the scope.

-   **All active regions + global**: includes the connection’s active regions and global resources.
-   **Selected regions**: scans only your selection. Include Global when you need global resources.
-   **Selected VPCs / vSwitches**: scans a network scope. Choose a region, then find targets by name or ID.

Region scans can be limited to resource types; leaving the selection empty uses all indexed types. Network scans derive the types from their targets.

<span id="progress"></span>

## Check progress and failures

<figure class="docs-figure"><a href="../../assets/scan-en.png" target="_blank" rel="noreferrer" aria-label="Scan details show progress and resource counts by target (open full size)"><img src="../../assets/scan-en.png" alt="Scan details show progress and resource counts by target" width="1440" height="960"></a><figcaption>Scan details show progress and resource counts by target <span>· Sample data · click to enlarge</span></figcaption></figure>

Open the task for region progress, resource counts, and logs. If some targets fail, fix the reported permissions, credentials, or connectivity issue, then use Retry when available.

Running tasks offer pause, resume, or cancel according to their state. Canceling does not undo data already discovered.

<span id="freshness"></span>

## When to scan again

Scan the relevant scope after creating, moving, or deleting cloud resources, and before a broad cleanup. Verify resources whose last-seen time is old.

<aside class="docs-note">An unscanned region or resource type is not evidence of an empty scope. Incomplete coverage can leave dependencies missing from the graph and cleanup tasks.</aside>
