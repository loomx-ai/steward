---
title: "Resource cleanup"
description: "Select targets, review the impact, then confirm execution."
navTitle: "Resource cleanup"
---

<span id="select"></span>

## 1. Select targets and create a task

Add targets from Resources or Resource panorama, then create a cleanup task. You can also start a task from Cleanup. Begin with a small set of resources you have confirmed are no longer needed.

Account, region, and group targets can resolve to many resources. Check the resolved list after creation, not just the target name. Creating the task does not execute deletion.

<span id="review"></span>

## 2. Review blockers and retained resources

-   Check resource IDs, regions, deletion actions, and scope.
-   Inspect dependencies that still use a target, and resources deleted through their owning controller.
-   Check skipped and retained resources, including residuals that may continue billing.

<figure class="docs-figure"><a href="../../assets/cleanup-en.png" target="_blank" rel="noreferrer" aria-label="The application vSwitch is still used by api-02 outside the cleanup selection (open full size)"><img src="../../assets/cleanup-en.png" alt="The application vSwitch is still used by api-02 outside the cleanup selection" width="1440" height="960"></a><figcaption>The application vSwitch is still used by api-02 outside the cleanup selection <span>· Sample data · click to enlarge</span></figcaption></figure>

Here, api-02 is outside the cleanup scope, so the vSwitch is blocked. If api-02 must stay, remove the vSwitch target. If api-02 should also be deleted, add the dependency and review the updated task.

<aside class="docs-note">Incomplete scan coverage is a warning and may not prevent execution. Scan the relevant scope first; zero blockers does not prove every dependency has been discovered.</aside>

<span id="execute"></span>

## 3. Confirm execution

After review, start execution and complete the confirmation dialog. Account-wide or region-wide scopes also require typing the exact target name. Submission starts deletion through the provider APIs.

Concurrency defaults to 20 and accepts 1–100. Reduce it if you encounter throttling. Execution follows dependency order and reads back results.

<span id="results"></span>

## 4. Verify results

Check each status in Resource results and filter Cleanup logs by resource ID to investigate failures. Pausing does not revoke requests already sent or restore deleted resources.

After fixing the cause, use the task’s available continue or resume action; completed resources are not executed again. Scan afterward and check retained resources and billing. Audits record the actor, action, and result.
