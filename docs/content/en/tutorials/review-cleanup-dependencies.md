---
title: "Review cloud resource dependencies before cleanup"
description: "Use Steward to inspect shared dependencies, resolve cleanup blockers, and record a keep-or-remove decision before executing deletion."
navTitle: "Review cleanup dependencies"
---

Removing an application can affect resources that other applications still use. This tutorial uses Steward's inventory, relationship graph, and cleanup review to examine that boundary. You will finish with a review decision; executing deletion is outside this exercise.

The example uses Alibaba Cloud sample resources: two instances, `api-01` and `api-02`, share an application vSwitch. A load balancer named `public-gateway` is also selected for review. These are illustrative records, not resources Steward creates in your account.

## 1. Establish a current inventory

Select the intended cloud connection and scan the relevant scope. Include the surrounding network and related resources, then check scan failures and resource last-seen times. If a possible dependency is outside the scanned scope, fill that gap first.

Use native IDs to confirm the resources in the cloud console. Names alone are insufficient when multiple accounts or regions use the same naming conventions. The [first inventory tutorial](./first-inventory.md) walks through these checks.

**Check:** You can explain the scan scope, its observation time, and any unresolved failures. Do not treat an unscanned area as empty.

## 2. Inspect shared relationships

Open a resource's Relationships view, or find it in Resource panorama and enable relationship lines. Trace connections to resources you intend to keep as well as those you intend to remove.

<figure class="docs-figure"><a href="../../../assets/relationships-en.png" aria-label="Inspect the sample shared load balancer and security group"><img src="../../../assets/relationships-en.png" alt="Alibaba Cloud sample data: api-01 and api-02 share a load balancer and a security group" width="1440" height="960"></a><figcaption>Shared resources connect the intended cleanup target to an instance that may need to stay. This screenshot contains sample data.</figcaption></figure>

In the example, removing `api-01` does not establish that its shared vSwitch or security group is unused. Open the other instance and verify whether its owner still needs it. Check provider-side state when the graph is incomplete or stale.

**Check:** You have identified resources outside the intended deletion scope that still depend on a selected resource. Relationship lines come from scanned data and supported rules; missing lines do not establish independence.

## 3. Create a review task and read the resolved targets

Add the intended resources to the cleanup list and create a cleanup task. Creating the task does not execute deletion. Stop at the review stage.

Check actual resource IDs and planned actions. A broad account, project, region, or group target can resolve to more resources than its label suggests. Review retained and skipped resources, and resources managed through an owning controller or stack.

The sample task selects `api-01`, `public-gateway`, and the `application` vSwitch. Unselected `api-02` still needs that vSwitch, so the vSwitch deletion is blocked.

<figure class="docs-figure"><a href="../../../assets/cleanup-dependency-en.svg" aria-label="Review the dependency outside the cleanup selection"><img src="../../../assets/cleanup-dependency-en.svg" alt="Unselected api-02 uses the selected application vSwitch and blocks its deletion" width="640" height="700"></a><figcaption>The dependency outside the selection changes what can be removed. Confirm the resource IDs in your actual task.</figcaption></figure>

## 4. Make a scope decision

| Finding | Decision to record |
| --- | --- |
| `api-02` must remain | Remove the shared vSwitch from the targets and review the updated plan. |
| The owner confirms `api-02` can also be removed | Treat adding it as a new scope decision. Review its own dependencies and the expanded target list. |
| The dependency or owner is unclear | Leave execution pending while you verify ownership and cloud-side state. |
| A resource is retained or skipped | Record why it remains and whether it needs separate follow-up. |

Do not add resources solely to make a blocker disappear. An incomplete scan warning may not prevent execution, and zero blockers is not a guarantee that every dependency has been discovered.

## 5. Finish with a review record

For your own task, record the connection, exact target IDs, resources to keep, unresolved dependencies, and the scan used for review. A useful outcome can be “keep the shared network and review only the instance” or “do not execute until the missing region has been scanned.”

You have completed this tutorial when the proposed scope and outstanding questions are clear. No deletion needs to run.

If you later choose to proceed, follow the [cleanup execution and result verification guide](../cleanup.md#execute). Provider protections and retention rules still apply; consult [AWS](../aws.md), [Alibaba Cloud](../alicloud.md), [Google Cloud](../gcp.md), or [Azure](../azure.md) for the relevant limits. After execution, inspect per-resource results and scan again. Pausing a task does not undo requests already sent or restore deleted resources.
