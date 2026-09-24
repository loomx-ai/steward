---
title: "Resource cleanup"
description: "Queue cleanup targets, create a cleanup task, resolve blockers and check retained resources, then confirm deletion and verify the results."
navTitle: "Resource cleanup"
---

Use this page when you are ready to delete cloud resources with Steward. Cleanup has five stages, and nothing is deleted until you confirm the last one:

1. **Select targets** in a resource list.
2. **Create a cleanup task.** Steward resolves the targets to resources and checks their dependencies. Nothing is deleted.
3. **Review** blockers, retained resources, and skipped resources.
4. **Confirm execution** with **Start cleanup**. Steward starts calling the provider's delete APIs.
5. **Verify** the results and scan again.

Before you start, scan the whole scope you plan to clean up. Cleanup checks dependencies only among resources Steward has discovered (see [When to scan again](./scans.md#freshness)).

<span id="select"></span>

## 1. Select targets and create a task

Start with a small set of resources you have confirmed are no longer needed. Targets are queued in the **Resource list**, which belongs to the active connection. If you switch connections, Steward asks first and then clears the list.

**From Resources:**

1. In **Resources**, select the rows you want to clean up.
2. Click **Add to resource list (N)**. The rows are marked **Pending cleanup**.
3. Click **Resource list** in the bottom-right corner, check the targets, and remove any you don't want.
4. Click **Create cleanup task**. In **New cleanup task**, check **Cleanup targets**, then click **Confirm**.

**From Resource Panorama:** right-click a region, VPC, subnet, or resource, or open its details, and choose **Add to resource list** (**Add all to resource list** for a group). To pick several nodes, choose **Box select** in the canvas toolbar, drag over them, and click **Add selected**. Then open **Resource list** → **Create cleanup task** → **Confirm**.

**From Cleanup:** click **New task**, then **Add individual resource** (enter a resource ID) or **Choose from resource panorama**, and click **Confirm**.

**Check the result:** The cleanup task page opens. Nothing has been deleted yet.

An account, project, region, or group target can resolve to many more resources than its name suggests. Check the task's resource count and open the **Cleanup targets** tab to see what each target resolved to.

<span id="review"></span>

## 2. Review blockers and retained resources

Review the task before you start it. **Start cleanup** appears only when the task has no blockers.

| Where to look | What to check |
| --- | --- |
| **Execution blockers** (top of the task page) | Every blocker and its evidence. **View N blocked resources** filters **Resource results** to blocked resources. **Add N dependent resources** lets you add them to the task (see the example below). |
| **Resource results** | Resource IDs, regions, and the planned **Action** for each resource. **Status reason** explains resources deleted with their controller, skipped resources (such as unsupported types), and dirty resources that cleanup ignores. |
| Retained and skipped resources | Why each one stays, and whether it may continue billing. |
| **Cleanup warnings** | Incomplete scan coverage. This is a warning, not a blocker; see the note below. |

<aside class="docs-note">Incomplete scan coverage does not stop <strong>Start cleanup</strong>: Steward cleans up using the resources it has discovered and can miss dependencies in unscanned regions or resource types. Zero blockers does not prove every dependency has been found, and a missing relationship line does not prove a resource is unused. Scan the relevant scope first.</aside>

### What blocks a task

| Blocker | What it means | What to do |
| --- | --- | --- |
| Unverified cleanup dependency | The provider reports a dependency Steward could not verify. This also protects resources deleted through a controller. | Check the resource ID and evidence, then scan that resource and its dependencies again. |
| A resource outside the task still uses a target | A network, key, or identity you selected is still used by an unselected resource, for example an AWS KMS key or IAM role, a Google Cloud KMS key or service account, or an Azure managed identity. This blocks even when you selected the target directly. | Add the dependent resource to the task, or keep the target by creating a new task without it. |
| Key or identity without a complete scan | Deleting a key or identity requires a complete scan of every active region and the global scope of the connection, because an unscanned resource could still use it. | Run a scan with **All active regions + global** and fix any failed items. |
| The connection's own identity | Steward never deletes the identity the connection uses (see the table below), because that would revoke its access in the middle of cleanup. | Create a new task without it. |
| Controller that cannot be cleaned up | If a selected controller cannot be cleaned up, the deletion steps for its dependent children are blocked too. | To clean up supported children independently, select them without the controller and review the new task. |

A new dependency finding invalidates a plan you already reviewed. Review the refreshed task before you start it.

Keys and identities that require a complete scan:

| Provider | Resource types |
| --- | --- |
| AWS | KMS keys, IAM roles, IAM users |
| Alibaba Cloud | KMS keys, RAM roles |
| Google Cloud | Cloud KMS crypto keys and key versions, service accounts |
| Azure | User-assigned managed identities, Key Vault vaults and keys |

The connection's own identity is always protected:

| Provider | Protected resources |
| --- | --- |
| AWS | The IAM user or role behind the connection, its groups, its attached managed policies, and its instance profile |
| Alibaba Cloud | The RAM user or role, the groups the user belongs to, and the custom policies attached to either directly or through those groups |
| Google Cloud | The service account and its keys |
| Azure | Role assignments to the connection's service principal |

### Example: an unselected instance still uses the subnet

The sample task selects `api-01`, `public-gateway`, and the `application` subnet. Another instance, `api-02`, is outside the selection but still uses that subnet, so the subnet deletion is blocked.

<figure class="docs-figure"><a href="../../assets/cleanup-dependency-en.svg" target="_blank" rel="noreferrer" aria-label="How api-02 outside the selection blocks subnet deletion (open full size)"><img src="../../assets/cleanup-dependency-en.svg" alt="api-01, public-gateway, and the application subnet are selected for cleanup. Unselected api-02 still uses the subnet, blocking its deletion." width="640" height="700"></a><figcaption>The resources involved in this blocker. Arrows indicate use; dependencies outside the selection also affect the plan.</figcaption></figure>

To resolve it, decide which resources should stay:

- **`api-02` must stay:** create a new task without the subnet. Remove the subnet from the **Resource list** (or from **Cleanup targets** in **New cleanup task**), create the task, and review it.
- **`api-02` should also go:** click **Add N dependent resources**, check the list, and click **Add and update task**. The task's scope and blockers can change, so review it again.

Adding a resource is a new scope decision. Don't add resources just to clear a blocker, and don't use [**Mark as dirty resource**](./resources.md#selection) to get around one.

The task page shows the blocker with the same sample data:

<figure class="docs-figure"><a href="../../assets/cleanup-en.png" target="_blank" rel="noreferrer" aria-label="The application subnet is still used by api-02 outside the cleanup selection (open full size)"><img src="../../assets/cleanup-en.png" alt="The application subnet is still used by api-02 outside the cleanup selection" width="1440" height="960"></a><figcaption>The application subnet is still used by api-02 outside the cleanup selection <span>· Sample data · click to enlarge</span></figcaption></figure>

<span id="execute"></span>

## 3. Confirm execution

1. On the task page, click **Start cleanup**.
2. In **Destructive action confirmation**, set **Concurrency**: the number of resources deleted at the same time, 1–100, default 20. Lower it if the provider throttles requests.
3. If the task targets an entire cloud account, or an account, project, subscription, or region scope, type the exact target name for each one.
4. Select **I have reviewed this cleanup task and its impacts** if it is shown.
5. Click **Start cleanup**.

Steward then calls the provider's delete APIs in dependency order and reads back each result.

<span id="results"></span>

## 4. Verify results

- **Resource results** shows each resource's status. To investigate a failure, open **Cleanup logs** and use **Filter by resource ID**.
- **Pause** stops resources that have not started yet. Deletions already in progress finish, and the task shows **Pausing** until they do. Pausing does not revoke requests already sent or restore deleted resources. Click **Resume** to continue.
- If resources fail, fix the cause and click **Continue**. Steward retries failed resources and continues those that have not started; completed resources do not run again.

When the task finishes, scan the scope again and check retained resources and billing. **Audit** records the actor, action, and result of each cleanup.

## Provider protections

- **Google Cloud:** [supported actions and deletion protections](./gcp.md#deletion-protections), including VM auto-delete disks, nonempty buckets, and GKE workload finalizers.
- **Azure:** [cleanup protections](./azure.md#cleanup-protections), including management locks, VM attachment settings, empty storage requirements, and managed resources.
- **AWS** and **Alibaba Cloud:** cleanup behavior and protections in [AWS](./aws.md) and [Alibaba Cloud](./alicloud.md).

## Next steps

To practice the review without deleting anything, follow [Review cloud resource dependencies before cleanup](./tutorials/review-cleanup-dependencies.md).
