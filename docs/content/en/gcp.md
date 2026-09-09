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

To manage hierarchical firewall policies, also enter the optional **Firewall scope**, such as `organizations/123` or `folders/456`. This explicitly adds that organization or folder and its descendant folders to the connection's firewall-policy scope. Project access alone does not enable it. The service account needs Resource Manager reads for this tree and native firewall-policy permissions. Include the global scope when scanning. Removing or changing this setting does not mark previously observed policies as deleted; their old cleanup plans must be reviewed again.

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
| Resource Manager | Organization containing the connected project, discovered through its folder ancestry | Read-only; the public v3 API has no organization delete method |
| Firewall policies | Hierarchical policies within the configured firewall scope; global and regional network policies; native associations | Remove reviewed associations before deleting a policy; associations can also be removed independently |
| Load balancing and addresses | Regional and global IP addresses and forwarding rules; regional/global backend services; health checks, including legacy HTTP(S) checks; target pools; network endpoint groups; URL maps; HTTP/HTTPS proxies; SSL certificates | Supported |
| Cloud Storage | Buckets | Empty buckets only |
| Pub/Sub | Topics and subscriptions | Supported |
| Cloud SQL | Instances | Supported when deletion protection is off |
| Cloud Run | Services | Supported |
| Artifact Registry | Repositories | Supported |
| Cloud Monitoring metrics scopes | The connection project’s scope, monitored-project links and incoming scope references | Selected links can be removed; the scope and its own project link are retained |
| Infrastructure Manager | Deployment groups, deployments, revisions, resource records, previews and change/drift records | Reviewed group, deployment and preview cleanup; child metadata has no independent delete |
| Cloud TPU | Nodes, queued resources and native reservations | Nodes and queued resources support reviewed cleanup; data disks are detached and retained; reservations are read-only |
| Data Fusion | Instances, DNS peerings and namespaces | Reviewed instance cleanup includes namespaces and DNS peerings; DNS peerings also support independent deletion |
| Batch | Jobs and task records | Job cleanup cancels running work and verifies reviewed task/VM/disk effects; tasks have no independent delete |
| Discovery Engine | Collections, data stores, apps, schemas, controls, serving configurations, sessions, conversations, assistants, document branches/documents and website targets | Reviewed native cleanup; branches and site-search configurations are removed with their data store |
| Dataproc | Clusters, jobs, auxiliary node groups, autoscaling policies and workflow templates | Reviewed cluster cleanup; job history is retained unless selected; auxiliary groups have no independent delete |
| Dataform | Folders, team folders, repositories, workspaces, release/workflow configurations, workflow invocations, compilation results | Reviewed folder and repository cleanup; running invocations are cancelled first; compilation results are deleted with their repository |
| Secret Manager | Global and regional secrets | Supported |
| Google Kubernetes Engine | Clusters and node pools | Native controller cleanup with reviewed member impacts |
| Cloud KMS | Key rings, keys, versions and import jobs | Eligible resource records; import jobs are read-only |

Google sometimes uses separate asset types for regional and global resources, including `RegionDisk`, `GlobalAddress`, and `GlobalForwardingRule`. Full resource names retain project and zone/region identity, so same-name VMs in different zones remain distinct.

Inventory is eventually consistent. Newly created or deleted resources may take time to appear in Cloud Asset Inventory even after another scan. Cleanup checks the product API directly and waits for asynchronous operations and resource absence; it does not use a stale inventory result as proof of deletion. See Google's [asset type and freshness documentation](https://docs.cloud.google.com/asset-inventory/docs/asset-types).

Organization discovery requires Resource Manager read access to every ancestor
folder and the organization. Failed reads or a moved project preserve prior
organization observations. This does not authorize organization-level cleanup.
See Google's [Organization API](https://docs.cloud.google.com/resource-manager/reference/rest/v3/organizations)
and [standalone organization lifecycle guide](https://docs.cloud.google.com/resource-manager/docs/delete-standalone-org).

## Global VPCs and regional subnets

A Google VPC spans regions. Steward shows its boundary in regional network views, with that region's subnets and attached resources. Global resources are also available through the global view.

A **regional VPC cleanup group** selects resources in that region and retains the shared global VPC. To delete the VPC itself, review it as a separate global resource together with its remaining dependencies. A network scan can include the selected region's resources and their referenced global network resources. Shared VPC resources in another project require a separate connection; cross-project cleanup is not combined automatically.

## Deletion protections

- **Firewall policies:** Cleanup first removes each reviewed association using its native API, then deletes the policy and its rules. Removing an association changes firewall enforcement for its target; the network, organization or folder remains. The target must stay within the configured scope, and changed policies, associations or target identities block the plan. Hierarchical cleanup also checks the target's association list. The APIs cannot atomically bind these reads to deletion, and same-name associations have no creation token. Avoid concurrent policy changes during cleanup. See Google's [hierarchical policy guide](https://docs.cloud.google.com/firewall/docs/manage-hierarchical-firewall-policies) and [global network policy guide](https://docs.cloud.google.com/firewall/docs/use-network-firewall-policies).
- **VM disks and managed groups:** Native disk/IP deletion policy appears in the impact plan. Supported retention is applied through native operations and verified before deleting the controller. Changed attachments or resource identities block execution.
- **Deletion protection:** VM deletion protection is removed as an explicit native preparation phase during authorized cleanup. Protected labels and Cloud SQL deletion protection still block deletion.
- **Storage buckets:** Nonempty buckets are rejected. Steward does not empty objects or object versions to make a bucket deletable.
- **Metrics scopes:** Removing a project link changes which metrics the scoping project can query. It keeps the monitored project, time-series data, dashboards and alerting configurations. The scope and its own project link are read-only; incoming references from other projects need their own connections for cleanup. See Google’s [metrics-scope configuration guide](https://docs.cloud.google.com/monitoring/settings/multiple-projects).
- **Infrastructure Manager:** Deployment cleanup reviews the current revision’s provisioned resources and nested native effects. The native policy deletes those resources or retains all of them; deployment/revision metadata is removed in both cases. Preview cleanup removes only preview metadata. Partial retention is rejected. Opaque or unmapped Terraform resources require explicit `retain_all_resources=true`. Execution accounts and source buckets remain dependencies. See Google’s [deployment deletion guide](https://docs.cloud.google.com/infrastructure-manager/docs/delete-deployments).
- **Deployment groups:** Cleanup reviews current deployments and those removed since the last successful group revision, including their physical resources. It deprovisions the group before removing group/revision metadata. `retain_all_resources=true` retains physical resources while removing deployment metadata; explicitly retaining every referenced Deployment also keeps those deployments. Partial retention is rejected. See Google’s [deployment group guide](https://docs.cloud.google.com/infrastructure-manager/docs/deployment-groups).
- **Cloud TPU:** A queued-resource plan deletes its reviewed nodes first. Node cleanup detaches existing data disks, verifies that the disks remain, then deletes the node and its boot disk. The queued request is deleted after its nodes are absent. Network resources and reservations remain separate. See Google’s [queued-resource deletion contract](https://docs.cloud.google.com/tpu/docs/reference/rest/v2/projects.locations.queuedResources/delete).
- **Data Fusion:** Instance cleanup includes its reviewed namespaces and DNS peerings, and waits for the native operation and resource absence. Unreadable policies, new children or changed configuration block cleanup. User data and referenced storage, networks, service accounts, keys and topics remain separate. See Google’s [instance deletion guide](https://docs.cloud.google.com/data-fusion/docs/how-to/delete-instance).
- **Batch:** Select the job to review its tasks, VMs and disks together. Cleanup uses native job deletion and waits for resource absence. Existing attached disks with `autoDelete=false` remain retained; retention of Batch-created disks or unsafe external-disk deletion settings blocks cleanup. Instance templates, storage buckets, NFS data, secrets, Pub/Sub topics, logs and output data remain separate resources. See Google's [job deletion behavior](https://docs.cloud.google.com/batch/docs/delete-job).
- **Discovery Engine:** Deleting an app keeps its linked data stores. Delete or unlink every linked app before deleting a data store; Steward does not unlink an unselected app. The plan includes discovered child configurations, conversations and documents. Data-store deletion can take days; the task waits for the native operation and reviewed resources to disappear, including after restart. Collection cleanup first deletes its apps and data stores. See Google’s [data-store deletion requirements](https://docs.cloud.google.com/generative-ai-app-builder/docs/delete-a-data-store).
- **Dataproc:** Cluster cleanup reviews managed VMs, instance groups, generated templates and disks. Job history is retained by default; selecting job records also cancels active jobs and deletes their records before the cluster. Existing attached disks with `autoDelete=false`, storage buckets, autoscaling policies and external services remain separate resources. Virtual-cluster cleanup retains its GKE cluster and node pools. See Google’s [cluster deletion contract](https://docs.cloud.google.com/managed-spark/docs/reference/rest/v1/projects.regions.clusters/delete) and [GKE cleanup behavior](https://docs.cloud.google.com/managed-spark/docs/guides/dpgke/quickstarts/gke-quickstart-create-cluster).
- **Dataform:** Repository cleanup deletes its workspaces, release/workflow configurations and invocation records, then removes the repository and its reviewed compilation results. Running invocations are cancelled and must reach a terminal state first. New members, changed configuration or unreadable dependencies block cleanup. Git remotes, referenced secrets and BigQuery output tables remain separate resources. Cancellation does not roll back completed BigQuery work. See Google's [repository deletion contract](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.repositories/delete) and [cancellation behavior](https://docs.cloud.google.com/dataform/docs/reference/mcp/tools_list/cancel_workflow_invocation).

Dataform folder cleanup includes its nested folders and repositories, with each member deleted before its parent. A moved or recreated ancestor also stops child actions and resumed cancellations. Folder searches reflect the connection's access: resources hidden by a sharing change remain recorded until deletion is verified. Google's [team-folder search contract](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.teamFolders/search) describes this visibility limit.

- **GKE:** The plan includes verified node and network impacts. Cluster cleanup waits for Kubernetes Service, Ingress and Gateway finalizers before deleting the cluster. Persistent volumes and pre-existing IPs/certificates are retained according to their verified policy. Unsupported retention or unverified ownership blocks cleanup. The control-plane endpoint must be reachable; inventory needs `get` on the `kube-system` Namespace and `list` on Services, Ingresses and installed Gateway resources. Cleanup also needs `delete` on those workload resources, native Container/Compute read/delete/operation permissions, and access to node-group membership. Kubernetes Secret read permission is not required.

Review the actual selection and [cleanup results](./cleanup.md) before proceeding. Product permissions, retention policies, dependencies, and provider-side changes can still prevent an action.

Metrics-scope inventory requires `resourcemanager.projects.get` on the connection project. Removing a monitored-project link requires `monitoring.metricsScopes.link` on both the scoping project and the monitored project, plus access to the returned Monitoring operation. Scans use the full scope response; unreadable scopes cannot prove a link disappeared. Cleanup compares the scope and link creation times again before deletion and while waiting. This API has no atomic creation-time or etag condition, so avoid concurrent relinking during cleanup. See the native [read](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/locations.global.metricsScopes/get) and [unlink](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/locations.global.metricsScopes.projects/delete) contracts.

Infrastructure Manager requires `config.locations.list` and the native `get`/`list` permissions for deployments, revisions, resources, previews, resourcechanges and resourcedrifts. Cleanup adds `config.deployments.delete` or `config.previews.delete`, `config.operations.get`, and physical-resource read/list permissions. Terraform uses the deployment’s service account and source configuration, which must remain valid. See the [Config permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/config).

Deployment groups additionally require the native `config.deploymentgroups` and `config.deploymentgrouprevisions` read/list permissions, plus `config.deploymentgroups.deprovision` and `config.deploymentgroups.delete` for cleanup. A revision with an unknown outcome or ambiguous success history blocks cleanup until its resource impact can be established.

Deployment and deployment-group cleanup stop when a child still needs VM/MIG retention or protection changes, GKE workload/network finalizers, or TPU data-disk detachment. Combining those preparations with Terraform destruction remains unsupported; retaining all provisioned resources leaves them available for separate cleanup after the deployment is removed. Terraform protection/deletion policies can also prevent destruction. Final checks verify actual metadata and physical-resource outcomes, including after restart. These APIs have no atomic configuration condition; avoid concurrent changes.

Dataform scans require `dataform.locations.list` and the `list`/`get` permissions for repositories, workspaces, release configs, workflow configs, workflow invocations and compilation results. Cleanup also needs each independently deletable type's `delete` permission and `dataform.workflowInvocations.cancel` for running work. These checks read secret references without fetching secret values. See the [Dataform permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/dataform).

Folder discovery and cleanup additionally require `dataform.folders.get`, `dataform.teamFolders.get` and `dataform.folders.queryContents` for the accessible tree; deletion requires `dataform.folders.delete` and/or `dataform.teamFolders.delete` for the selected folders.

Batch inventory needs `batch.locations.list`, `batch.jobs.list`, `batch.jobs.get` and `batch.tasks.list`, `batch.tasks.get`. Cleanup also needs `batch.jobs.delete` and `batch.operations.get`, plus `compute.instances.list`, `compute.instances.get` and `compute.disks.list`, `compute.disks.get` to verify its effects. Jobs using instance templates require `compute.instanceTemplates.get`. Referenced secrets are not read. See the [Batch permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/batch).

Dataproc scans need `compute.regions.list`, `dataproc.clusters.list` / `dataproc.clusters.get`, `dataproc.jobs.list` / `dataproc.jobs.get`, `dataproc.nodeGroups.get` and the corresponding policy/template `list/get` permissions. Cluster cleanup additionally needs `dataproc.clusters.delete`, `dataproc.operations.get` and Compute read/list permissions for the reviewed VMs, disks, instance groups and templates. Selected jobs need `dataproc.jobs.cancel` / `dataproc.jobs.delete`; selected policies and templates need their own delete permissions. See the [Dataproc permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/dataproc).

Dataproc checks bind the cluster UUID and reviewed configuration, but its APIs cannot atomically lock jobs and Compute membership together. Keep provider identity metadata and labels intact, and avoid concurrent cluster changes during cleanup. Unexpected Compute autoscalers or stateful-group policies stop cleanup for review. Deleting a workflow template does not cancel workflows already running from it.

Discovery Engine uses the `global`, `us` and `eu` locations; US/EU are available in the region picker even before Cloud Asset Inventory observes a resource. The connection needs the selected kinds' native `list`/`get` permissions, collection and ancestor reads, `discoveryengine.dataConnectors.get` for connector-backed collections, and website/sitemap reads for website data stores. Cleanup also needs the selected kinds' `delete` and `discoveryengine.operations.get`. See the [permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/discoveryengine). Document text, conversation turns, prompts, schemas and connector configuration are redacted before inventory or logs are stored. Configuration checks happen before redaction.

Data Fusion inventory needs `datafusion.locations.list`, `datafusion.instances.list`, `datafusion.instances.get`, `datafusion.namespaces.list` and `datafusion.namespaces.getIamPolicy`. Instance cleanup needs `datafusion.instances.delete` and `datafusion.operations.get`; independent DNS deletion uses `datafusion.instances.update`. See the [native method permissions](https://docs.cloud.google.com/data-fusion/docs/how-to/audit-logging) and [permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/datafusion). Private options and namespace policies are redacted after configuration checks.

Data Fusion coverage uses its management API. Individual CDAP pipelines, datasets, secure-store entries and their runtime compute are not included in this workflow. Namespace records are removed with the instance; Steward does not enable unrecoverable reset for independent namespace deletion. These delete APIs have no atomic configuration condition, so avoid concurrent changes during cleanup. See the [CDAP API reference](https://docs.cloud.google.com/data-fusion/docs/reference/cdap-reference).

Discovery Engine does not expose an atomic configuration or creation-ID condition on these deletion methods. Avoid concurrent resource changes during cleanup. Branches and site-search configuration have no independent delete API. The listed coverage does not separately inventory preview agent/runtime resources or every data-plane record; deleting the containing app or data store also removes its contained service data.

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

Cloud TPU inventory requires `tpu.locations.list`, `tpu.nodes.list`, `tpu.nodes.get`, and `compute.disks.get` for attached data disks. Queued-resource APIs reuse Node permissions; [reservation listing](https://docs.cloud.google.com/tpu/docs/reference/rest/v2alpha1/projects.locations.reservations/list) also requires `tpu.nodes.get`. Cleanup additionally needs `tpu.nodes.update`, `tpu.nodes.delete`, and `tpu.operations.get`. See the [TPU permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/tpu). Private node and template metadata is redacted after configuration checks.

Cloud TPU APIs do not offer an atomic configuration or incarnation condition for these writes; avoid concurrent changes during cleanup. Disk retention preserves the disk resource and does not create a backup. This coverage uses the Cloud TPU API; Compute Engine/GKE TPUs, including TPU7x and later, use [separate management APIs](https://docs.cloud.google.com/tpu/docs/tpus-in-compute-engine).
