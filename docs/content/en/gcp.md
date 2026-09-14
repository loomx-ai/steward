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

To manage Cloud Identity groups, enter the optional **Group directory**, such as `customers/C01234567` or `identitysources/source-1`, and enable the Cloud Identity API. Grant the service account suitable directory group permissions; project access alone is insufficient. Google supports a service account with a Groups Admin role [without domain-wide delegation](https://docs.cloud.google.com/identity/docs/how-to/setup). Include the global scope in scans. Changing the directory or losing visibility preserves earlier observations.

Steward accepts service account JSON keys for the standard Google Cloud endpoints. It does not use ambient `gcloud` credentials or credentials from the machine's metadata service. See Google's [service account key guidance](https://docs.cloud.google.com/iam/docs/best-practices-for-managing-service-account-keys) and [Cloud Asset Inventory list permissions](https://docs.cloud.google.com/asset-inventory/docs/reference/rest/v1/assets/list).

## Run your first scan

1. Switch to the new Google Cloud connection and confirm the target project ID.
2. Scan a region containing a known resource, such as a VM in `us-central1`.
3. Resolve permission or disabled-API failures in scan details, then confirm the resource's name, project, region, and zone.
4. After this check, run **All active regions + global** for complete inventory. Global VPCs and global addresses require the global scope.

**Success check:** Expected resources appear under the correct project and location, with no unresolved scan failures. Cloud Asset Inventory has collection delays, so newly created resources may require another scan later.

## Search resource properties

Select a resource type to see its supported property fields in search completion.
Existing inventory gains newly supported fields after the next successful scan.
For example:

```text
type = "compute.googleapis.com/StoragePool" AND properties.provisionedCapacityGiB = "20480"
type = "compute.googleapis.com/FirewallPolicy" AND properties.shortName = "hierarchical-policy"
type = "tpu.googleapis.com/QueuedResource" AND properties.lifecycleState = "ACTIVE"
type = "run.googleapis.com/Service" AND state = "CONDITION_FAILED"
type = "sqladmin.googleapis.com/Instance" AND tags.team = "analytics"
type = "bigquery.googleapis.com/Table" AND properties.name = "events" AND properties.numRows = "9007199254740993"
type = "compute.googleapis.com/SslCertificate" AND properties.managedStatus = "PROVISIONING_FAILED"
type = "discoveryengine.googleapis.com/Document" AND properties.indexedAt = "2026-08-01T13:00:00Z"
type = "cloudidentity.googleapis.com/Membership" AND properties.memberId = "member@example.test"
```

Capacity and count fields represented as native 64-bit integer strings use quoted
values. A firewall policy's `properties.name` is its native numeric name;
`properties.shortName` is its display name. TPU queued resources expose their
structured native `properties.state` alongside the searchable `properties.lifecycleState`.
The top-level `state` search field remains available across resource types, but
stays empty when the API provides no resource state. Networks and firewall rules,
for example, have no native state or labels property. When supplied, a route's
`routeStatus` is its searchable state. Managed certificate status, PSC connection
status and load-balancer migration state have their own named properties; they
are not general resource health. See the [route](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routes)
and [certificate](https://docs.cloud.google.com/compute/docs/reference/rest/v1/sslCertificates) API fields.

BigQuery dataset/table `properties.name` is the short dataset/table ID; full
resource IDs distinguish same-named tables in different datasets. Scans read
native details after listing to obtain properties such as encryption settings,
row counts and byte counts. The connection needs `bigquery.datasets.get` and
`bigquery.tables.get` in addition to list access. A denied, missing or mismatched
detail fails that scan shard and preserves existing inventory. See the
[dataset](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/datasets/get)
and [table](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/tables/get) detail methods.

Cloud Identity groups and memberships, and Resource Manager organizations, keep
their native resource name in `properties.name`. Use `properties.displayName`
for group/organization display names and `properties.memberId` for a membership's
subject ID. GKE labels come from the native cluster or node-pool configuration.

Discovery Engine document `properties.indexedAt` is the index-status timestamp;
indexing error text and document content remain redacted. Target sites expose
`properties.indexingStatus`, while conversations and sessions expose `startTime`
and `endTime` rather than creation time. Infrastructure Manager changes expose
`properties.intent`. KMS `properties.primaryState` describes only a CryptoKey's
primary version; key versions and import jobs do not inherit the key's labels.
See the [document index fields](https://docs.cloud.google.com/generative-ai-app-builder/docs/reference/rest/v1/projects.locations.collections.dataStores.branches.documents)
and [resource-change intent](https://docs.cloud.google.com/infrastructure-manager/docs/reference/rest/v1/projects.locations.previews.resourceChanges).

Bigtable table scans read full native metadata after listing tables, including
column families, replication state, backup policy and deletion protection. The
connection therefore needs `bigtable.tables.get` as well as `bigtable.tables.list`.
A denied, missing or mismatched detail response fails the scan shard and preserves
previous observations. Table deletion and instance cascade checks also read full
metadata and honor table deletion protection. See the native [list](https://docs.cloud.google.com/bigtable/docs/reference/admin/rest/v2/projects.instances.tables/list)
and [detail](https://docs.cloud.google.com/bigtable/docs/reference/admin/rest/v2/projects.instances.tables/get) methods.

## Inventory and supported cleanup

Steward lists the resources below through their native product APIs. Cloud Asset Inventory adds broad discovery for other types, which appear as read-only inventory. A failed product shard is reported and cannot establish resource absence.

| Service | Recognized resources | Cleanup |
| --- | --- | --- |
| Compute Engine | VM instances; zonal and regional persistent disks; snapshots; images; instance templates; managed instance groups, instance groups and autoscalers | Supported |
| Hyperdisk Storage Pools | Native pools, capacity/performance usage, provisioning modes and disk members | Inventory and reviewed cleanup |
| VPC | Networks, subnets, firewall rules, routes, Cloud Routers | Supported |
| Cloud Router | Native router configuration, NAT impacts and policy/set prerequisites | Reviewed parent deletion and NAT cascade |
| Cloud NAT | Per-router public/private NAT configurations, rules, subnet and address references | Independent removal; other NATs and the router retained |
| Cloud Router named sets | Per-router prefix/community sets, CEL elements and fingerprint | Reviewed deletion after referring policies |
| Cloud Router BGP policies | Per-router import/export policies, CEL terms and fingerprint | Independent native policy deletion with BGP reference detachment |
| Cloud Identity | Groups and member relationships in the configured directory | Reviewed group deletion; ordinary member links can also be removed independently |
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

- **Cloud Identity groups:** Deleting a group removes its reviewed member relationships and preserves the member users, service accounts and nested group objects. Locked groups are protected. Dynamic memberships are managed by Google and cannot be individually removed by Steward. Changes to group configuration or membership block old plans. Group deletion is irreversible and affects access; external IAM bindings and references in other products require separate review. A permission error alone cannot establish deletion. See Google’s [group deletion contract](https://docs.cloud.google.com/identity/docs/reference/rest/v1/groups/delete) and [identity model](https://docs.cloud.google.com/architecture/identity/overview-google-authentication).
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


## Hyperdisk Storage Pools

Pool inventory uses native Compute aggregate lists and maps each zone to its scan region. It preserves provisioned capacity, IOPS and throughput, written/used capacity, disk counts, provisioning modes, Exapool capacity and sharing settings. Large integer values retain their native precision. Disk pool references remain ordinary dependencies; they do not imply a deletion cascade.

Inventory requires `compute.storagePools.list` and `compute.storagePools.get` (for member lists and pool readback). Denied or partial lists fail the scan and preserve existing observations. Member discovery follows all pages of `storagePools.listDisks`, preserving disk size, used bytes, IOPS, throughput, attachments and snapshot policies. Disks must belong to the pool’s zone; pool identity is rechecked after paging. Shared disks in other projects remain member summaries, without reading or managing those projects through this connection. Failed member reads preserve the previous observations. Disk records are reconciled by their own disk scans.

Hyperdisk Balanced and Throughput pools support reviewed deletion. Select their local member disks or a supported VM, MIG, or GKE controller that deletes those disks. The plan never selects a controller automatically. Cleanup waits for each disk to be absent before deleting the pool; a plan that retains a member disk cannot delete its pool. Changed configuration or membership requires a fresh scan. Snapshots remain separate. Exapools and pools with foreign-project members require external cleanup; refresh inventory after that cleanup. See [Google's pool management guide](https://docs.cloud.google.com/compute/docs/disks/manage-storage-pools).

Deletion additionally requires `compute.storagePools.delete`, `compute.zoneOperations.get`, and `compute.disks.get` / `compute.disks.delete` for member cleanup. An active future reservation can block native deletion; Steward does not cancel it automatically. Avoid concurrent pool changes during cleanup: the native delete API has no conditional resource ID or etag. Pooling capacity and performance does not establish equivalence to Alibaba Cloud's exclusive physical storage.

Security Command Center service inventory shows intended and effective enablement
separately, including module settings and update time. An inherited setting may
have a different effective state because of onboarding or billing eligibility;
`INGEST_ONLY` means findings ingestion without the service being enabled. These
service settings are read-only in Steward and do not represent a subscription tier.
Enable **Security Center Management API** and grant
`securitycentermanagement.locations.list`,
`securitycentermanagement.securityCenterServices.list` and
`securitycentermanagement.securityCenterServices.get`. Project scans use native
service locations; include global scope for global settings. Failed detail reads
preserve the previous observation. If a location or service stops appearing in a
list, its last observation is retained with its original last-seen time; when it
becomes visible again, inventory updates its state. Restart older pending scans
if they fail after this update. See the [service settings contract](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/organizations.locations.securityCenterServices)
and [read permissions](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/projects.locations.securityCenterServices/get).

Organization subscription inventory shows the current Security Command Center tier
and the latest subscription's type, start time and end time. The latest subscription
may already have ended; its dates do not imply that a paid tier is currently active.
Steward preserves the native tier separately. Subscription inventory follows the
selected project's organization and rechecks the ancestry after reading. A project
move or lost access preserves the earlier observation. These records are read-only
and do not describe a project's separate billing entitlement.

Enable **Security Command Center API** and grant `securitycenter.subscription.get`
on the organization, plus `resourcemanager.projects.get`,
`resourcemanager.folders.get` on intervening folders, and
`resourcemanager.organizations.get` for ancestry discovery. Include global scope.
A permission failure or missing subscription response fails the scan. See the
[subscription contract](https://docs.cloud.google.com/security-command-center/docs/reference/rest/v1beta2/organizations/getSubscription)
and [Security Command Center permissions](https://docs.cloud.google.com/iam/docs/roles-permissions/securitycenter#securitycenter.subscription.get).

Project billing inventory shows the Security Command Center tier explicitly set
on each project/location. It keeps this value separate from the organization
subscription and does not infer an inherited tier, trial period, or expiry date.
Global and regional records retain distinct identities. Project scans follow the
service's native location list; failed or changed reads preserve earlier records.
A location disappearing from the visible list also preserves its last observation.
Enable **Security Center Management API** and grant
`securitycentermanagement.locations.list` and
`securitycentermanagement.billingMetadata.get`. Include global scope to read global
settings. These records are read-only. See the [project billing contract](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/projects.locations/getBillingMetadata).

## Cloud Router route policies

Scanning route policies requires `compute.routers.list`,
`compute.routers.listRoutePolicies` and `compute.routers.getRoutePolicy` in the
selected project. Steward reads each router's policies in its own region and
fetches every policy's details. Policies with the same name in different routers
remain separate resources. A failed detail read preserves the last successful
observation; a successful empty policy list marks the old policy absent.

Filter policies with `type = "compute.googleapis.com/RoutePolicy"` and
`properties.type = "ROUTE_POLICY_TYPE_IMPORT"`. See Google's [policy list API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/listRoutePolicies)
and [policy detail API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/getRoutePolicy).

Independent policy deletion additionally requires `compute.routers.get`,
`compute.routers.deleteRoutePolicy` and `compute.regionOperations.get`.
Run a fresh scan before creating the cleanup task so it includes the policy
fingerprint, containing router ID and BGP peer configuration. Other configuration
changes require a new review; completed sibling policy removals are handled below.
`bgpReferences` lists the affected peers and import/export directions.
Steward uses the native [policy deletion API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/deleteRoutePolicy),
resumes its regional operation after restart and confirms policy absence while
the same router remains readable. Operation completion alone does not prove
that deletion is visible. An unreadable parent is reported as a dependency failure.

When peers reference the selected policy, Steward first removes that name from
those import/export lists, retaining every other policy in its original order.
This uses [Router PATCH](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/patch)
and additionally needs `compute.routers.update`; Google's [audit permission list](https://docs.cloud.google.com/compute/docs/logging/audit-logging)
also identifies `compute.networks.updatePolicy` on the router's network. The
request preserves other peer settings and omits unrelated router fields. Only
after the regional operation finishes and the updated peers are visible does
policy deletion begin. Both phases resume from persisted receipts after restart.

Sequential deletions can use the same scan. Before updating peers, Steward reads
the current configuration and preserves earlier removals only after the native API
confirms those sibling policies are absent. New references, reordered policies,
changed peers or settings, and unreadable sibling policies stop execution.

Steward serializes selected Router, NAT, policy and named-set deletions on the
same router, even when execution concurrency is higher. Other routers can proceed
independently. An overlapping cleanup task cannot start or continue while that
router has an unresolved mutation.

Other configuration changes, native dependency conflicts and permission failures
are reported for review. These native mutations have no fingerprint precondition,
so avoid concurrent policy or BGP peer edits during cleanup. Named sets and other
policies are not selected for deletion by an independent policy action.

Older plans receive missing ordering dependencies before execution starts or
continues, retaining their step identities and reviewed resource snapshots. If
new dependencies would affect unfinished worker jobs or previously issued,
unsettled cloud actions, continuation is blocked. A completed policy-detachment
operation alone does not release the router: policy deletion has a second native
phase whose outcome must also be accounted for.

For failed or canceled executions with only terminal worker jobs, Steward can
release the router after read-only verification that every possible mutation
phase has ended. An attached policy requires both its original BGP-detachment
and deletion operations, including a deletion sent before its receipt was saved.
An unattached policy or named set requires its single deletion operation. Lost
or expired receipts need `compute.regionOperations.list` in addition to
`compute.regionOperations.get`. Missing, ambiguous, incomplete or inaccessible
operation history keeps the router blocked. Recovery preserves the old execution
status and does not retry deletion or mark resources as deleted. See the
[operation lookup API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/regionOperations/list).

## Cloud Router named sets

Named-set scans need `compute.routers.list`, `compute.routers.listNamedSets` and
`compute.routers.getNamedSet` in the selected project. Steward discovers each
router's sets independently, including their type, description, CEL expression
elements and fingerprint. Names are router-local; the inventory identity includes
the region and router. Failed or incomplete reads preserve earlier observations.

Filter with `type = "compute.googleapis.com/NamedSet"` and
`properties.type = "NAMED_SET_TYPE_PREFIX"` (or `NAMED_SET_TYPE_COMMUNITY`).
See [native set details](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/getNamedSet).
Google
[prevents removal of a set referenced by any policy on its router](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/bgp-route-policies/update-named-sets).

Route-policy scans parse native CEL calls to `prefixSets('name')` and
`communitySets('name')` into dependencies on sets in the same router. Include
both policies and named sets in a scan to see these graph relationships.
Deleting a policy preserves its referenced sets. Strings and comments are not
treated as calls, and expressions are not executed. Malformed CEL or a computed
set name that cannot be resolved stops that policy scan shard and preserves
previous observations.

Named-set cleanup checks every policy on its router, including policies outside
local scan selections. A known referring policy outside the cleanup selection
blocks the plan. When both are selected, the policy is deleted first; deleting
only a policy retains its sets. Deletion needs `compute.routers.get`,
`compute.routers.listRoutePolicies`, `compute.routers.getRoutePolicy`,
`compute.routers.getNamedSet`, `compute.routers.deleteNamedSet` and
`compute.regionOperations.get`. Steward verifies the scanned set revision and
router identity, waits for the regional operation, and confirms the set is absent.
Incomplete policy reads, unresolved references, changed resources or provider
conflicts stop cleanup. See the [native deletion API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/deleteNamedSet).
Selecting the parent Router includes its policies and sets as prerequisite deletions.

## Cloud Router inventory and cleanup

Router scans need `compute.routers.list` and `compute.routers.get`. Steward reads
current detail for each listed router and checks its identity before recording
NAT, BGP and interface configuration. A denied, missing, incomplete or mismatched
detail response fails the scan source and preserves previous observations. MD5
authentication material is redacted from inventory and API logs.

Scan the Router, NAT configurations, policies and named sets together before
selecting the Router for cleanup. Steward lists and reads every native child,
checks the scanned configuration and reports unindexed children as blockers.
Policies and named sets become independent prerequisite deletions. Reviewed NATs
are included in the parent deletion impact; they cannot be retained while deleting
the Router. Independent NAT cleanup remains available.

Router lifecycle review also needs `compute.routers.listRoutePolicies`,
`compute.routers.getRoutePolicy`, `compute.routers.listNamedSets` and
`compute.routers.getNamedSet`. Parent deletion needs `compute.routers.delete` and
`compute.regionOperations.get`, plus the permissions required by its prerequisite
steps. Steward uses native [Router DELETE](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/delete),
resumes the bound regional operation after restart, and confirms parent and
prerequisite absence before recording the NAT cascade. It issues no separate NAT
or address-deletion request for that cascade.

Associated VPN tunnels and VLAN attachments block this cleanup; remove them and
scan again before creating the Router task. Other configuration changes also
require renewed review. These reads and native deletion are not atomic, so avoid
external edits while cleanup runs. Old Router tasks without configuration reviews
need a fresh scan before new deletion. See the [Router GET contract](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/get)
and [router deletion guide](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/managing-routers).

Failed or canceled Router executions with terminal worker jobs can release their
mutation reservation after the original native delete operation is confirmed
terminal. Current recovery reconstructs the frozen NAT impacts and policy/set
prerequisites; changed or missing child reviews block it. Recognized older
operation-only receipts need the original request UUID, target and numeric Router
ID echoed by the native operation. This read-only recovery does not resume the old
delete or mark Router/NAT cleanup successful. `compute.regionOperations.get` is
required; lost or expired current receipts and expired older receipts also need
`compute.regionOperations.list`. Missing operation history, unidentifiable older
receipts and missing incarnation evidence remain blocked.

## Cloud NAT gateways

Cloud NAT scans need `compute.routers.list` and `compute.routers.get` in the
selected project. Each router's native `nats` array supplies independent gateway
records, including public/private type, IP allocation and draining, subnet and
NAT64 selection, rules, port allocation and logging. Filter with
`type = "compute.googleapis.com/RouterNat"` and, for example,
`properties.type = "PRIVATE"`. Identities include project, region, router and NAT
name; the inventory identity is not a separate REST endpoint or CAI asset type.
See the [native Router schema](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers).

Router, VPC, subnet, address and NCC Hub references form graph relationships when
the related resources are also scanned. Native CEL equality expressions identify
literal `nexthop.hub` targets; comments and ordinary strings do not create references.
For a bare or computed Hub selector, scans list the source project's NCC spokes,
read matching VPC spokes, repeat the membership read and recheck the Router.
This requires `networkconnectivity.spokes.list` and `networkconnectivity.spokes.get`
in addition to Router reads. All matching Hub candidates are retained as potential
dependencies; packet predicates are never executed. A complete empty membership
list adds no Hub. Rereads detect observed changes but provide no atomic snapshot.
See the [Private NAT configuration guide](https://docs.cloud.google.com/nat/docs/set-up-private-nat).

Regional NATs resolve global Hubs by complete native identity within the same
connection and partition. Missing or foreign-project Hubs remain unresolved references.
Keeping a referring NAT blocks Hub deletion; selecting both orders NAT before Hub.
Selecting only the NAT retains its Hub. Rescan older NAT records before rebuilding
relationships for unbound Hub expressions. Malformed CEL or incomplete, denied,
missing or changed Router/spoke responses fail the shard and preserve prior
observations. Only a complete matching Router response can establish NAT absence.
NAT cleanup removes the reviewed configuration through the native Router PATCH
API, waits for its regional operation and confirms absence in a complete router
read. It needs `compute.routers.get`, `compute.routers.update` and
`compute.regionOperations.get`. The request preserves the latest other NAT
configurations and leaves BGP peers, interfaces, keys and the router intact.
Manual address resources are not explicitly deleted by this action.

NAT deletion shares ordering and cross-task coordination with the containing
Router, route policies and named sets. An unresolved update in another cleanup
task blocks a new update on that router. For failed or canceled NAT actions with
no runnable jobs, Steward releases the block when it can verify that
no update was invoked or the original cloud operation has finished. This does
not mark the old task or deletion as successful. Paused tasks and uncertain
updates remain blocked; failed tasks can still be continued in their original task.

If the operation receipt is missing or expired, recovery also needs
`compute.regionOperations.list` to find the original request's operation. Missing
records, incomplete results and denied reads keep the block in place. See the
[native operation lookup contract](https://docs.cloud.google.com/compute/docs/reference/rest/v1/regionOperations/list).
Native PATCH has no configuration revision precondition, so concurrent external
writers can still race the final read/update. Avoid changing that
router's NAT configuration externally while cleanup runs. See the
[native PATCH and request-ID contract](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/patch).
Selecting the parent Router includes reviewed NATs in its deletion impact.
Google documents that [deleting a router also deletes its Cloud NAT gateways](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/managing-routers).

## Cloud Monitoring uptime checks

Uptime Check scans read native project-scoped configurations and verify each LIST
record against GET. HTTP, TCP, monitored resource/group and synthetic monitor
settings, schedules, checker regions and user labels are retained. The native
name is the identity; display names need not be unique. Failed or changed reads
preserve prior observations; only a complete empty list establishes absence.

Cleanup requires a freshly scanned configuration review. It rechecks the observable
configuration before deletion and confirms absence with GET, including after a
worker restart. Native DELETE is synchronous; an empty response alone does not
prove absence. Request authentication, headers and body are redacted from asset
and API output. Native masked secrets cannot be compared in plaintext, and the API
has no etag condition to prevent an external edit after the last read.

Permissions are `monitoring.uptimeCheckConfigs.list`,
`monitoring.uptimeCheckConfigs.get`, and `monitoring.uptimeCheckConfigs.delete`.
Delete associated alert policies first: the API rejects checks still referenced by
those policies. Deleting a synthetic check retains its Cloud Run function.
Steward currently deletes only the check; target/network relationships, group
membership and automatic alert-policy ordering remain unfinished. See the
[Monitoring deletion contract](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.uptimeCheckConfigs/delete).
