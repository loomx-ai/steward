---
title: "Google Cloud (GCP)"
description: "Connect a project, scan Google Cloud resources, and review supported cleanup actions."
navTitle: "Google Cloud"
---

## Connect a project

Each GCP connection accesses one project with a service account JSON key. The service account may belong to another project if it has permission to access the target project.

1. Enable **Cloud Asset Inventory**, **Cloud Resource Manager**, and **Compute Engine API** in the target project. Enable the relevant product API before using its cleanup actions.
2. Grant the service account `cloudasset.assets.listResource`, `resourcemanager.projects.get`, and `compute.regions.list` on the target project. These permissions support inventory and region discovery. Add the product's resource-read, deletion, and operation-status permissions only for resources you intend to clean up. Bucket cleanup also requires `storage.objects.list`.
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

Cloud Asset Inventory supplies resource metadata. Steward recognizes 29 resource types below, with deletion support for 28. Additional types returned by Cloud Asset Inventory appear as read-only inventory.

| Service | Recognized resources | Cleanup |
| --- | --- | --- |
| Compute Engine | VM instances; zonal and regional persistent disks; snapshots; images; instance templates | Supported |
| VPC | Networks, subnets, firewall rules, routes, Cloud Routers | Supported |
| Load balancing and addresses | Regional and global IP addresses and forwarding rules; backend services; health checks; URL maps; HTTP/HTTPS proxies; SSL certificates | Supported |
| Cloud Storage | Buckets | Empty buckets only |
| Pub/Sub | Topics and subscriptions | Supported |
| Cloud SQL | Instances | Supported when deletion protection is off |
| Cloud Run | Services | Supported |
| Artifact Registry | Repositories | Supported |
| Secret Manager | Global and regional secrets | Supported |
| Google Kubernetes Engine | Clusters | Read-only |

Google sometimes uses separate asset types for regional and global resources, including `RegionDisk`, `GlobalAddress`, and `GlobalForwardingRule`. Full resource names retain project and zone/region identity, so same-name VMs in different zones remain distinct.

Inventory is eventually consistent. Newly created or deleted resources may take time to appear in Cloud Asset Inventory even after another scan. Cleanup checks the product API directly and waits for asynchronous operations and resource absence; it does not use a stale inventory result as proof of deletion. See Google's [asset type and freshness documentation](https://docs.cloud.google.com/asset-inventory/docs/asset-types).

## Global VPCs and regional subnets

A Google VPC spans regions. Steward shows its boundary in regional network views, with that region's subnets and attached resources. Global resources are also available through the global view.

A **regional VPC cleanup group** selects resources in that region and retains the shared global VPC. To delete the VPC itself, review it as a separate global resource together with its remaining dependencies. A network scan can include the selected region's resources and their referenced global network resources. Shared VPC resources in another project require a separate connection; cross-project cleanup is not combined automatically.

## Deletion protections

- **VM disks:** A VM with an attached disk set to `autoDelete` is protected from cleanup. Review and change that setting in Google Cloud first, then scan again. Steward does not silently delete an attached disk through the VM.
- **Deletion protection:** Protected VMs and Cloud SQL instances remain blocked. Steward does not disable the provider's deletion protection.
- **Storage buckets:** Nonempty buckets are rejected. Steward does not empty objects or object versions to make a bucket deletable.
- **GKE:** Cluster deletion can remove managed nodes and other resources. Clusters remain read-only until those ownership effects can be represented in cleanup plans.

Review the actual selection and [cleanup results](./cleanup.md) before proceeding. Product permissions, retention policies, dependencies, and provider-side changes can still prevent an action.

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
