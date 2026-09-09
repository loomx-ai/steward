---
title: "Google Cloud (GCP)"
description: "Connect a project, scan Google Cloud resources, and review supported cleanup actions."
navTitle: "Google Cloud"
---

## Connect a project

Each GCP connection accesses one project with a service account JSON key. The service account may belong to another project if it has permission to access the target project.

1. Enable **Cloud Asset Inventory**, **Cloud Resource Manager**, and **Compute Engine API** in the target project. Enable the relevant product API before using its cleanup actions.
2. Grant the service account `cloudasset.assets.listResource`, `resourcemanager.projects.get`, and `compute.regions.list` on the target project. These permissions support inventory and region discovery. Native product scans also require each enabled product's list/read permissions. Add deletion and operation-status permissions for resources you intend to clean up. Bucket cleanup also requires `storage.objects.list`.
3. Open **Settings → Cloud connections → Add connection**, choose **Google Cloud**, and enter the **Project ID** and the service account's **JSON key**.
4. Validate the connection, refresh its regions, and verify the result with the first-scan steps below.

Steward validates project access and the inventory permission. A successful connection check does not prove that every cleanup API is allowed. Keys are encrypted using the deployment's credential-encryption key. Use **Replace credential** when rotating a key; the target project must remain the same.

Steward accepts service account JSON keys for the standard Google Cloud endpoints. It does not use ambient `gcloud` credentials or credentials from the machine's metadata service. See Google's [service account key guidance](https://docs.cloud.google.com/iam/docs/best-practices-for-managing-service-account-keys) and [Cloud Asset Inventory list permissions](https://docs.cloud.google.com/asset-inventory/docs/reference/rest/v1/assets/list).

## Run your first scan

1. Switch to the new Google Cloud connection and confirm the target project ID.
2. Scan a region containing a known resource, such as a VM in `us-central1`.
3. Resolve permission or disabled-API failures in scan details, then confirm the resource's name, project, region, and zone.
4. After this check, run **All active regions + global** for complete inventory. Global VPCs and global addresses require the global scope.

**Success check:** Expected resources appear under the correct project and location, with no unresolved scan failures. Cloud Asset Inventory has collection delays, so newly created resources may require another scan later.

## Inventory and supported cleanup

Steward lists the resources below through their native product APIs. Cloud Asset Inventory adds broad discovery for other types, which appear as read-only inventory. A failed product shard is reported and cannot establish resource absence.

| Service | Recognized resources | Cleanup |
| --- | --- | --- |
| Compute Engine | VM instances; zonal and regional persistent disks; snapshots; images; instance templates; managed instance groups, instance groups and autoscalers | Supported |
| VPC | Networks, subnets, firewall rules, routes, Cloud Routers | Supported |
| Load balancing and addresses | Regional and global IP addresses and forwarding rules; regional/global backend services; health checks, including legacy HTTP(S) checks; target pools; network endpoint groups; URL maps; HTTP/HTTPS proxies; SSL certificates | Supported |
| Cloud Storage | Buckets | Empty buckets only |
| Pub/Sub | Topics and subscriptions | Supported |
| Cloud SQL | Instances | Supported when deletion protection is off |
| Cloud Run | Services | Supported |
| Artifact Registry | Repositories | Supported |
| Dataform | Repositories, workspaces, release/workflow configurations, workflow invocations, compilation results | Reviewed repository cleanup; running invocations are cancelled first; compilation results are deleted with their repository |
| Secret Manager | Global and regional secrets | Supported |
| Google Kubernetes Engine | Clusters and node pools | Native controller cleanup with reviewed member impacts |
| Cloud KMS | Key rings, keys, versions and import jobs | Eligible resource records; import jobs are read-only |

Google sometimes uses separate asset types for regional and global resources, including `RegionDisk`, `GlobalAddress`, and `GlobalForwardingRule`. Full resource names retain project and zone/region identity, so same-name VMs in different zones remain distinct.

Inventory is eventually consistent. Newly created or deleted resources may take time to appear in Cloud Asset Inventory even after another scan. Cleanup checks the product API directly and waits for asynchronous operations and resource absence; it does not use a stale inventory result as proof of deletion. See Google's [asset type and freshness documentation](https://docs.cloud.google.com/asset-inventory/docs/asset-types).

## Global VPCs and regional subnets

A Google VPC spans regions. Steward shows its boundary in regional network views, with that region's subnets and attached resources. Global resources are also available through the global view.

A **regional VPC cleanup group** selects resources in that region and retains the shared global VPC. To delete the VPC itself, review it as a separate global resource together with its remaining dependencies. A network scan can include the selected region's resources and their referenced global network resources. Shared VPC resources in another project require a separate connection; cross-project cleanup is not combined automatically.

## Deletion protections

- **VM disks and managed groups:** Native disk/IP deletion policy appears in the impact plan. Supported retention is applied through native operations and verified before deleting the controller. Changed attachments or resource identities block execution.
- **Deletion protection:** VM deletion protection is removed as an explicit native preparation phase during authorized cleanup. Protected labels and Cloud SQL deletion protection still block deletion.
- **Storage buckets:** Nonempty buckets are rejected. Steward does not empty objects or object versions to make a bucket deletable.
- **Dataform:** Repository cleanup deletes its workspaces, release/workflow configurations and invocation records, then removes the repository and its reviewed compilation results. Running invocations are cancelled and must reach a terminal state first. New members, changed configuration or unreadable dependencies block cleanup. Git remotes, referenced secrets and BigQuery output tables remain separate resources. Cancellation does not roll back completed BigQuery work. See Google's [repository deletion contract](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.repositories/delete) and [cancellation behavior](https://docs.cloud.google.com/dataform/docs/reference/mcp/tools_list/cancel_workflow_invocation).
- **GKE:** The plan includes verified node and network impacts. Cluster cleanup waits for Kubernetes Service, Ingress and Gateway finalizers before deleting the cluster. Persistent volumes and pre-existing IPs/certificates are retained according to their verified policy. Unsupported retention or unverified ownership blocks cleanup. The control-plane endpoint must be reachable; inventory needs `get` on the `kube-system` Namespace and `list` on Services, Ingresses and installed Gateway resources. Cleanup also needs `delete` on those workload resources, native Container/Compute read/delete/operation permissions, and access to node-group membership. Kubernetes Secret read permission is not required.

Review the actual selection and [cleanup results](./cleanup.md) before proceeding. Product permissions, retention policies, dependencies, and provider-side changes can still prevent an action.

Dataform scans require `dataform.locations.list` and the `list`/`get` permissions for repositories, workspaces, release configs, workflow configs, workflow invocations and compilation results. Cleanup also needs each independently deletable type's `delete` permission and `dataform.workflowInvocations.cancel` for running work. These checks read secret references without fetching secret values. See the [Dataform permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/dataform).

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| The JSON key cannot be validated | Use a complete service account JSON key; confirm that the account is enabled and the key has not been revoked. |
| A disabled API or `SERVICE_DISABLED` error | Enable the API named in the error in the target project, then retry. |
| Validation or scanning returns `403` | Check the service account's target-project grants, including for cross-project accounts, and any organization policies. |
| A newly created resource is missing | Verify the project and region, resolve failed scan items, and rescan after Cloud Asset Inventory updates. |
| Multiple VMs have the same name | Compare projects and zones; different locations have distinct full resource names. |
| Protection settings block cleanup | Inspect the reported VM, Cloud SQL, disk `autoDelete`, or bucket condition; changing a local record does not resolve it. |

Next: [Scan resources](./scans.md) · [Resource relationships](./topology.md) · [Clean up resources](./cleanup.md)
