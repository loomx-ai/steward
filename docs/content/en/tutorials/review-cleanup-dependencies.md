---
title: "Review cloud resource dependencies before cleanup"
description: "Use Steward to find shared dependencies, read cleanup blockers, and record a keep-or-remove decision before you delete anything."
navTitle: "Review cleanup dependencies"
---

Removing one application can break resources that another application still uses. In this tutorial you use Steward's inventory, relationship graph, and cleanup review to find that boundary. You finish with a written scope decision; nothing is deleted.

The example uses AWS sample resources: two instances, `api-01` and `api-02`, share an application subnet, and a load balancer named `public-gateway` is also under review. These are sample records, not resources Steward creates in your account.

## Before you start

- A cloud connection with read permissions for the scope you want to review. See [Complete your first resource inventory](./first-inventory.md) if you haven't scanned before.
- The resources you intend to remove, identified by native resource ID.

## 1. Establish a current inventory

1. Select the intended connection as the **Active connection**.
2. Scan the relevant scope, including the surrounding network and related resources: **Scans** → **Start scan**.
3. On the scan's detail page, check for failed items, then check the last-seen times of the resources you plan to review. If a possible dependency is outside the scanned scope, scan it too.
4. Confirm each resource in the cloud console by native ID. Names are not enough when several accounts or regions follow the same naming convention.

**Check the result:** You can state the scan scope, when it ran, and any failures still unresolved. An unscanned area is not an empty one.

## 2. Inspect shared relationships

1. Open a resource's details in **Resources** and switch to **Relationships**, or find it in **Resource Panorama** and click **Show relationship lines**.
2. Follow the connections both to resources you intend to remove and to resources you intend to keep.

<figure class="docs-figure"><a href="../../../assets/relationships-en.png" aria-label="Inspect the sample shared load balancer and security group"><img src="../../../assets/relationships-en.png" alt="AWS sample data: api-01 and api-02 share a load balancer and a security group" width="1440" height="960"></a><figcaption>Shared resources connect the cleanup target to an instance that may need to stay. This screenshot contains sample data.</figcaption></figure>

In the example, removing `api-01` does not mean its subnet or security group is unused. Open `api-02` and ask its owner whether it is still needed. When the graph looks incomplete or stale, check the resource in the cloud console.

**Check the result:** You have listed the resources outside your intended scope that still depend on a selected resource. A missing line is not proof of independence; see [Use the relationships](../topology.md#interpret).

## 3. Create a review task and read the resolved targets

1. In **Resources**, select the resources you intend to remove and click **Add to resource list (N)**.
2. Click **Resource list** → **Create cleanup task**, then click **Confirm**. Creating the task does not delete anything.
3. On the task page, stop before **Start cleanup**. Check the resource IDs and planned actions in **Resource results**, and what each target resolved to in **Cleanup targets**.

An account, project, region, or group target can resolve to more resources than its name suggests. Also check retained and skipped resources, and resources handled through an owning controller or stack.

The sample task selects `api-01`, `public-gateway`, and the `application` subnet. Unselected `api-02` still needs that subnet, so the subnet deletion is blocked.

<figure class="docs-figure"><a href="../../../assets/cleanup-dependency-en.svg" aria-label="Review the dependency outside the cleanup selection"><img src="../../../assets/cleanup-dependency-en.svg" alt="Unselected api-02 uses the selected application subnet and blocks its deletion" width="640" height="700"></a><figcaption>The dependency outside the selection changes what can be removed. Confirm the resource IDs in your own task.</figcaption></figure>

**Check the result:** You have read every entry under **Execution blockers** and know which resource each one protects.

## 4. Make a scope decision

| Finding | Decision to record |
| --- | --- |
| `api-02` must stay | Remove the shared subnet from the targets, create a new task, and review it. |
| The owner confirms `api-02` can also go | Treat adding it as a new scope decision. Review its own dependencies and the expanded target list. |
| The dependency or owner is unclear | Leave the task unexecuted while you verify ownership and the cloud-side state. |
| A resource is retained or skipped | Record why it stays and whether it needs separate follow-up. |

Don't add resources only to make a blocker disappear. Zero blockers does not guarantee that every dependency was found; see the [coverage note](../cleanup.md#review).

## 5. Finish with a review record

For your own task, record the connection, the exact target IDs, the resources to keep, the unresolved dependencies, and the scan you based the review on. A good outcome can be "keep the shared network and review only the instance" or "don't execute until the missing region has been scanned."

You have finished when the proposed scope and the open questions are clear. No deletion needs to run.

## Next steps

- If you decide to proceed, follow [Confirm execution](../cleanup.md#execute) and [Verify results](../cleanup.md#results).
- Provider protections and retention rules still apply: see [AWS](../aws.md), [Alibaba Cloud](../alicloud.md), [Google Cloud](../gcp.md), or [Azure](../azure.md).
