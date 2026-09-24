---
title: "Scanning"
description: "Choose a scan scope, read scan progress and failed items, and know when to scan again before inventory or cleanup."
navTitle: "Scanning"
---

A scan reads your cloud provider's APIs for one connection and updates **Resources**, **Resource Panorama**, and the relationships between resources. Run one before you rely on the inventory, and again before you clean up.

<span id="scope"></span>

## Choose a scope

1. Check the **Active connection** at the top left. A scan covers only that connection.
2. Open **Scans** → **Start scan**.
3. Choose a **Scan scope** (see the table below) and, for region scopes, optionally limit the **Resource kind**.
4. Click **Schedule scan**. The scan's detail page opens.

For a first scan, choose one region you know has resources. Check permissions and results there before you expand the scope.

| Scope | What it covers | Use it when |
| --- | --- | --- |
| **All active regions + global** | Every region marked **Active** for the connection, plus global resources. Manage the list in **Settings** → **Cloud connections** → **Manage regions**. | You want a full inventory, before a broad cleanup, or before deleting a key or identity (cleanup [requires a complete scan](./cleanup.md#review) for those). |
| **Selected regions** | Only the regions you pick. Global resources, such as IAM identities, are included only if you also pick **global**. | Your first scan, or re-checking specific regions. |
| **Selected VPCs / vSwitches** | Resources inside the networks you pick. Choose a **Query region**, then search for VPCs and vSwitches (subnets on AWS) by name or ID. Selecting a VPC covers its vSwitches. Global resources are not included. | Re-checking one network quickly. |

With a region scope, leaving **Resource kind** empty scans **All indexed resource kinds**. A network scope chooses the resource kinds from its targets.

<span id="progress"></span>

## Check progress and failures

<figure class="docs-figure"><a href="../../assets/scan-en.png" target="_blank" rel="noreferrer" aria-label="Scan details show progress and resource counts by target (open full size)"><img src="../../assets/scan-en.png" alt="Scan details show progress and resource counts by target" width="1440" height="960"></a><figcaption>Scan details show progress and resource counts by target <span>· Sample data · click to enlarge</span></figcaption></figure>

The scan's detail page shows **Target progress**, **Region progress**, resource counts, and **Logs**. Each scan item reads one resource type in one region or network target.

### What a failed item means

A failed item leaves a gap in your inventory for that resource type and region:

- Steward updates records only from items that finish successfully. For a failed item, existing records stay as they were, with their earlier last-seen time.
- Steward removes a resource from the inventory only after a successful read no longer finds it. Resources deleted in the cloud stay listed until then, and resources created since the last successful read are missing.
- If Steward lists a resource but then fails to read its details, it keeps the resource without those details and logs a warning.

A scan that finished with failed items is therefore incomplete: search results, relationship lines, and cleanup dependency checks can all be missing resources from that gap.

To fix it, read the log for the failed item, correct the reported permission, credential, or connectivity problem, then click **Retry**. Retry reruns failed and unfinished work in the same scan.

While a scan runs, you can **Pause**, **Resume**, or **Cancel scan**, depending on its state. Canceling stops the scan but keeps completed results and logs; it does not undo data already discovered.

<span id="freshness"></span>

## When to scan again

- After you create, move, or delete cloud resources.
- Before a broad cleanup, across the whole scope you plan to clean up.
- When a resource's last-seen time is older than changes you know about. Verify it before acting on it.

To rescan one region from **Resource Panorama**, right-click it and choose **Rescan**. Steward creates a new scan for that region and opens its details.

<aside class="docs-note">An unscanned region or resource type is not evidence of an empty scope. With incomplete coverage, dependencies can be missing from the relationship graph and from cleanup tasks.</aside>

## Provider notes

- **Google Cloud:** inventory comes from Cloud Asset Inventory and can lag behind changes in the cloud. Start with **All active regions + global**. See [Google Cloud](./gcp.md).
- **Azure:** inventory uses ARM and product APIs, including explicit discovery of child resources. Start with **All active regions + global**. The network scope is called **Selected networks** and lists virtual or logical networks and subnets. See [Microsoft Azure](./azure.md).
