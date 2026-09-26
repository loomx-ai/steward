---
title: "Google Cloud (GCP)"
description: "Connect a Google Cloud project, grant scan and cleanup permissions, and look up what Steward inventories and what cleanup does for each service."
navTitle: "Google Cloud"
---

Use this page to connect a Google Cloud project, grant the permissions Steward needs, run a first scan, and look up how each service is inventoried and cleaned up.

Each connection covers one project. Two optional settings extend it: a **Firewall scope** for hierarchical firewall policies in an organization or folder, and a **Group directory** for Cloud Identity groups. Steward also inventories some records outside the project: budgets in billing accounts the credential can see, the organization that contains the project, and the connection service account's OS Login SSH keys.

Steward reads Google Cloud in two ways:

- **Cloud Asset Inventory** provides broad discovery. Resource types without native coverage appear as read-only inventory.
- **Native product APIs** list the services in [Inventory and supported cleanup](#inventory-and-supported-cleanup) in more detail, and cleanup always goes through them.

## Connect a project

### Choose a credential

| Credential | Use it when |
| --- | --- |
| **Google service account** JSON key | You want an unattended, stable scope. The service account can belong to another project, as long as it has access to the target project. Steward accepts keys for the standard Google Cloud endpoints. |
| [Browser sign-in](./connections.md#browser) (**Sign in with your browser**) | You want to authorize your own Google account and pick one of its projects. The connection reads only what your account can read. |

Browser sign-in runs its own authorization. Steward never uses `gcloud` credentials already on the machine or credentials from the machine's metadata service.

Service account keys are encrypted with the deployment's credential-encryption key. To rotate a key, use **Replace credential**; the target project must stay the same. See Google's [service account key guidance](https://docs.cloud.google.com/iam/docs/best-practices-for-managing-service-account-keys).

### Add the connection

1. In the target project, enable **Cloud Asset Inventory**, **Cloud Resource Manager** and **Compute Engine API**. Enable a product's API before you use its cleanup actions.
2. Grant the service account the [base inventory permissions](#base-inventory-permissions) on the target project, plus the [product permissions](#product-permissions) for services you plan to scan or clean up.
3. Open user menu → **Settings** → **Cloud connections** → **Add connection** and choose **Google Cloud**. Enter the **Project ID** and the **Service account JSON key**, or sign in with your browser and pick a project.
4. Optionally fill in **Firewall scope** or **Group directory** (see below).
5. Validate the connection and refresh its regions.

**Result check:** Validation confirms project access and the inventory permission. It doesn't confirm that every cleanup API is allowed; the first scan and the cleanup review show that.

### Optional connection settings

- **Firewall scope** (`organizations/123` or `folders/456`): adds that organization or folder and its descendant folders to the connection's firewall-policy scope, so Steward can manage hierarchical firewall policies there. Project access alone does not enable this. See [Firewall policies](#firewall-policies) for the permissions and behavior.
- **Group directory** (`customers/C01234567` or `identitysources/source-1`): lets Steward manage Cloud Identity groups in that directory. Project access alone is not enough. See [Cloud Identity groups](#cloud-identity-groups).

Include the global scope in scans for both.

## Permissions

### Base inventory permissions

Grant these on the target project. They cover Cloud Asset Inventory discovery and region discovery.

| Permission | Used for |
| --- | --- |
| `cloudasset.assets.listResource` | Listing resources through Cloud Asset Inventory ([list permissions](https://docs.cloud.google.com/asset-inventory/docs/reference/rest/v1/assets/list)) |
| `resourcemanager.projects.get` | Reading the target project |
| `compute.regions.list` | Discovering regions |

### Product permissions

Native product scans also need each product's list and read permissions. For resources you intend to clean up, add the delete permission and the permission to read the resulting operation's status. Bucket cleanup also needs `storage.objects.list`.

Each section under [Service-specific notes](#service-specific-notes) lists the scan permissions first and then what cleanup adds, with a link to Google's permission index for that product. Some services need an extra API enabled:

| Service | API to enable |
| --- | --- |
| [Cloud Identity groups](#cloud-identity-groups) | Cloud Identity API |
| [OS Login SSH public keys](#os-login-ssh-public-keys) | OS Login API |
| [Security Command Center](#security-command-center) service settings and billing metadata | **Security Center Management API** |
| Security Command Center organization subscription | **Security Command Center API** |

## Run your first scan

1. Switch to the new Google Cloud connection and confirm the target project ID.
2. Scan a region that contains a known resource, such as a VM in `us-central1`.
3. In the scan details, resolve permission or disabled-API failures, then check the resource's name, project, region and zone.
4. Run **All active regions + global** for a complete inventory. Global VPCs, global addresses and other global resources need the global scope.

**Result check:** Expected resources appear under the right project and location, and the scan has no unresolved failures. Cloud Asset Inventory has collection delays, so a newly created resource may only appear in a later scan.

## Search resource properties

Select a resource type to see its supported property fields in search completion. Existing inventory gains newly supported fields after the next successful scan. For example:

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
type = "compute.googleapis.com/RoutePolicy" AND properties.type = "ROUTE_POLICY_TYPE_IMPORT"
type = "compute.googleapis.com/NamedSet" AND properties.type = "NAMED_SET_TYPE_PREFIX"
type = "compute.googleapis.com/RouterNat" AND properties.type = "PRIVATE"
```

General rules:

- Quote the value of capacity and count fields that Google returns as 64-bit integer strings.
- The top-level `state` field works across resource types but is empty when the API reports no state. Networks and firewall rules, for example, have no native state or labels property. When a route reports `routeStatus`, that becomes its searchable state.
- Managed certificate status, PSC connection status and load-balancer migration state have their own named properties; they don't describe general resource health. See the [route](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routes) and [certificate](https://docs.cloud.google.com/compute/docs/reference/rest/v1/sslCertificates) API fields.

Service-specific fields:

- **Firewall policies:** `properties.name` is the native numeric name; `properties.shortName` is the display name.
- **Cloud TPU queued resources:** the structured native `properties.state` is kept; search on `properties.lifecycleState`.
- **BigQuery:** `properties.name` is the short dataset or table ID. Full resource IDs tell apart same-named tables in different datasets.
- **Cloud Identity groups and memberships, Resource Manager organizations:** `properties.name` is the native resource name. Use `properties.displayName` for group and organization display names and `properties.memberId` for a membership's subject ID.
- **GKE:** labels come from the native cluster or node-pool configuration.
- **Discovery Engine:** a document's `properties.indexedAt` is the index-status timestamp; indexing error text and document content stay redacted. Target sites expose `properties.indexingStatus`. Conversations and sessions expose `startTime` and `endTime` instead of a creation time. See the [document index fields](https://docs.cloud.google.com/generative-ai-app-builder/docs/reference/rest/v1/projects.locations.collections.dataStores.branches.documents).
- **Infrastructure Manager:** resource changes expose `properties.intent`. See [resource-change intent](https://docs.cloud.google.com/infrastructure-manager/docs/reference/rest/v1/projects.locations.previews.resourceChanges).
- **Cloud KMS:** `properties.primaryState` describes only a CryptoKey's primary version. Key versions and import jobs don't inherit the key's labels.
- **Cloud Router named sets:** use `NAMED_SET_TYPE_COMMUNITY` for community sets.

## Inventory and supported cleanup

Steward lists the resources below through their native product APIs. Cloud Asset Inventory adds other types as read-only inventory. If a product's scan item fails, Steward reports it and doesn't treat that product's resources as deleted.

| Service | Recognized resources | Cleanup |
| --- | --- | --- |
| [Compute Engine](#compute-engine) | VM instances; zonal and regional persistent disks; snapshots; images; instance templates; managed instance groups, instance groups and autoscalers | Supported |
| [Hyperdisk Storage Pools](#hyperdisk-storage-pools) | Pools, capacity and performance usage, provisioning modes and member disks | Reviewed cleanup |
| Cloud Storage | Buckets | Empty buckets only |
| [Cloud TPU](#cloud-tpu) | Nodes, queued resources and native reservations | Nodes and queued resources support reviewed cleanup; data disks are detached and kept; reservations are read-only |
| [Batch](#batch) | Jobs and task records | Job cleanup cancels running work and verifies the reviewed task, VM and disk effects; tasks can't be deleted on their own |
| [Google Kubernetes Engine](#google-kubernetes-engine) | Clusters and node pools | Native controller cleanup with reviewed member impacts |
| Cloud Run | Services | Supported |
| Artifact Registry | Repositories | Supported |
| VPC | Networks, subnets, firewall rules, routes, Cloud Routers | Supported |
| [Cloud Router](#cloud-router) | Router configuration, NAT impacts and policy/named-set prerequisites | Reviewed router deletion; its NATs are deleted with it |
| [Cloud Router BGP policies](#route-policies) | Per-router import/export policies, CEL terms and fingerprint | Independent deletion, detaching the policy from BGP peers first |
| [Cloud Router named sets](#named-sets) | Per-router prefix and community sets, CEL elements and fingerprint | Reviewed deletion after the policies that refer to them |
| [Cloud NAT](#cloud-nat) | Per-router public and private NAT configurations, rules, subnet and address references | Independent removal; other NATs and the router are kept |
| [Firewall policies](#firewall-policies) | Hierarchical policies within the configured firewall scope; global and regional network policies; native associations | Reviewed associations are removed before the policy is deleted; associations can also be removed on their own |
| Load balancing and addresses | Regional and global IP addresses and forwarding rules; regional and global backend services; health checks, including legacy HTTP(S) checks; target pools; network endpoint groups; URL maps; HTTP/HTTPS proxies; SSL certificates | Supported |
| [BigQuery](#bigquery) | Datasets and tables | Supported |
| [Bigtable](#bigtable) | Instances, clusters and tables | Supported; table deletion protection is honored |
| Cloud SQL | Instances | Supported when deletion protection is off |
| Pub/Sub | Topics and subscriptions | Supported |
| [Data Fusion](#data-fusion) | Instances, DNS peerings and namespaces | Instance cleanup includes reviewed namespaces and DNS peerings; DNS peerings can also be deleted on their own |
| [Dataform](#dataform) | Folders, team folders, repositories, workspaces, release and workflow configurations, workflow invocations, compilation results | Reviewed folder and repository cleanup; running invocations are cancelled first; compilation results are deleted with their repository |
| [Dataproc](#dataproc) | Clusters, jobs, auxiliary node groups, autoscaling policies and workflow templates | Reviewed cluster cleanup; job history is kept unless selected; auxiliary node groups can't be deleted on their own |
| [Discovery Engine](#discovery-engine) | Collections, data stores, apps, schemas, controls, serving configurations, sessions, conversations, assistants, document branches and documents, website targets | Reviewed native cleanup; branches and site-search configurations are removed with their data store |
| [Cloud Monitoring](#cloud-monitoring) | Alert policies, custom dashboards, groups and uptime checks | Reviewed deletion; referring resources you select are deleted first |
| [Cloud Monitoring metrics scopes](#metrics-scopes) | The connection project's scope, monitored-project links and incoming scope references | Selected links can be removed; the scope and its own project link are kept |
| [Cloud Billing budgets](#cloud-billing-budgets) | Budgets in visible billing accounts and budgets for the connected project | Reviewed deletion |
| [Cloud Identity](#cloud-identity-groups) | Groups and member relationships in the configured directory | Reviewed group deletion; ordinary member links can also be removed on their own |
| Cloud KMS | Key rings, keys, versions and import jobs | Eligible resource records; import jobs are read-only |
| [Infrastructure Manager](#infrastructure-manager) | Deployment groups, deployments, revisions, resource records, previews and change/drift records | Reviewed group, deployment and preview cleanup; child metadata can't be deleted on its own |
| [OS Login](#os-login-ssh-public-keys) | SSH public keys in the connection service account's profile | Supported |
| [Resource Manager](#resource-manager-organizations) | The organization that contains the connected project | Read-only; the public v3 API has no organization delete method |
| Secret Manager | Global and regional secrets | Supported |
| Managed Service for Apache Kafka | Clusters, topics and consumer groups | Supported; a cluster's topics and consumer groups are deleted with it |
| Eventarc | Message buses, pipelines, enrollments and triggers | Supported; triggers that another service manages (labeled `goog-managed-by`) are removed through that service |
| Cloud Workstations | Workstation clusters, configurations and workstations | Reviewed cleanup: workstations are deleted before their configuration, and configurations before their cluster |
| Certificate Manager | Certificates, certificate maps and entries, trust configs | Supported; a trust config that a TLS policy still uses is rejected by the service |
| Cloud Asset Inventory feeds | Feeds of the connected project | Supported |
| API Keys | API keys of the connected project | Supported; a deleted key can be restored for 30 days and counts as deleted |
| Organization Policy | Policies set directly on the connected project | Supported; deletion restores the policy inherited from the folder or organization |
| [Security Command Center](#security-command-center) | Service settings, organization subscription, project and organization billing metadata | Read-only |

Inventory is eventually consistent. Newly created or deleted resources can take time to show up in Cloud Asset Inventory, even after another scan. Cleanup doesn't rely on inventory: it checks the product API directly, waits for asynchronous operations and confirms the resource is gone. See Google's [asset type and freshness documentation](https://docs.cloud.google.com/asset-inventory/docs/asset-types).

Before you start a cleanup task, review the actual selection and, afterwards, the [cleanup results](./cleanup.md). Product permissions, retention policies, dependencies and changes made in Google Cloud can still stop an action.

## Global VPCs and regional subnets

A Google VPC spans regions. Steward shows its boundary in each regional network view, with that region's subnets and attached resources. Global resources are also available in the global view.

A **regional VPC cleanup group** selects resources in that region and keeps the shared global VPC. To delete the VPC itself, review it as a separate global resource together with its remaining dependencies. A network scan can include the selected region's resources and the global network resources they reference. Shared VPC resources in another project need a separate connection; Steward doesn't combine cross-project cleanup automatically.

## Deletion protections

Cleanup stops rather than working around a protection. The main protections are listed here; each linked section has the full behavior.

- **Protected labels:** a resource labeled `steward-protected` or `steward_protected` with the value `true`, `1`, `yes`, `on` or `protected` is not deleted.
- **VM deletion protection:** during an authorized cleanup, Steward removes it as an explicit native preparation step before deleting the VM. See [Compute Engine](#compute-engine).
- **VM disks and managed instance groups:** disk and IP deletion policies appear in the impact plan, and retention is applied and verified before the controller is deleted. See [Compute Engine](#compute-engine).
- **Cloud SQL:** instance deletion protection blocks deletion.
- **Cloud Storage buckets:** nonempty buckets are rejected. Steward doesn't empty objects or object versions to make a bucket deletable.
- **Bigtable:** table deletion protection is honored. See [Bigtable](#bigtable).
- **Cloud Identity groups:** locked groups are protected, and dynamic memberships can't be removed individually. See [Cloud Identity groups](#cloud-identity-groups).
- **Firewall policies:** reviewed associations are removed first; a target outside the configured scope, or a changed policy, association or target, blocks the plan. See [Firewall policies](#firewall-policies).
- **Hyperdisk Storage Pools:** a plan that keeps a member disk can't delete its pool. See [Hyperdisk Storage Pools](#hyperdisk-storage-pools).
- **Cloud TPU, Batch, Dataproc:** existing data disks, and existing attached disks with `autoDelete=false`, are kept. See [Cloud TPU](#cloud-tpu), [Batch](#batch) and [Dataproc](#dataproc).
- **GKE:** persistent volumes and pre-existing IPs and certificates are kept according to their verified policy. See [Google Kubernetes Engine](#google-kubernetes-engine).
- **Cloud Router:** associated VPN tunnels and VLAN attachments block router deletion. See [Cloud Router](#cloud-router).
- **Cloud Monitoring:** resources that still refer to the target block deletion unless you select them too. See [Cloud Monitoring](#cloud-monitoring).
- **Metrics scopes:** the scope and its own project link are read-only. See [Metrics scopes](#metrics-scopes).
- **Infrastructure Manager:** partial retention is rejected. See [Infrastructure Manager](#infrastructure-manager).
- **Cloud Workstations:** a cluster or configuration is deleted only after its configurations or workstations, each through its own deletion; Steward doesn't use the force option.
- **Eventarc:** triggers labeled `goog-managed-by`, such as those that Cloud Run functions create, are protected.
- **Discovery Engine:** a data store with linked apps can't be deleted until every linked app is deleted or unlinked. See [Discovery Engine](#discovery-engine).
- **The connection's own identity:** the service account the connection uses, and its keys, are always protected. See [Clean up resources](./cleanup.md).

## Service-specific notes

Each section follows the same order: what is inventoried, the permissions needed to scan and to clean up, what cleanup does, and known limits.

| Area | Services |
| --- | --- |
| Compute, containers and storage | [Batch](#batch) · [Cloud TPU](#cloud-tpu) · [Compute Engine](#compute-engine) · [Google Kubernetes Engine](#google-kubernetes-engine) · [Hyperdisk Storage Pools](#hyperdisk-storage-pools) |
| Networking | [Cloud NAT](#cloud-nat) · [Cloud Router](#cloud-router) · [Firewall policies](#firewall-policies) |
| Data and AI | [BigQuery](#bigquery) · [Bigtable](#bigtable) · [Data Fusion](#data-fusion) · [Dataform](#dataform) · [Dataproc](#dataproc) · [Discovery Engine](#discovery-engine) |
| Monitoring | [Cloud Monitoring](#cloud-monitoring) |
| Management, identity, security and billing | [Cloud Billing budgets](#cloud-billing-budgets) · [Cloud Identity groups](#cloud-identity-groups) · [Infrastructure Manager](#infrastructure-manager) · [OS Login SSH public keys](#os-login-ssh-public-keys) · [Resource Manager organizations](#resource-manager-organizations) · [Security Command Center](#security-command-center) |

### Batch

**Inventory.** Jobs and their task records.

**Permissions.**

- Scan: `batch.locations.list`, `batch.jobs.list`, `batch.jobs.get`, `batch.tasks.list` and `batch.tasks.get`.
- Cleanup adds: `batch.jobs.delete` and `batch.operations.get`, plus `compute.instances.list`, `compute.instances.get`, `compute.disks.list` and `compute.disks.get` to verify the effects. Jobs that use instance templates also need `compute.instanceTemplates.get`.

Steward doesn't read referenced secrets. See the [Batch permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/batch).

**Cleanup.** Select the job to review its tasks, VMs and disks together; tasks can't be deleted on their own. Cleanup cancels running work, uses native job deletion and waits until the resources are gone.

- Existing attached disks with `autoDelete=false` are kept.
- If the plan would keep disks that Batch created, or an external disk has an unsafe deletion setting, cleanup stops.
- Instance templates, storage buckets, NFS data, secrets, Pub/Sub topics, logs and output data remain separate resources.

See Google's [job deletion behavior](https://docs.cloud.google.com/batch/docs/delete-job).

### Cloud TPU

**Inventory.** Nodes, queued resources and native reservations through the Cloud TPU API. Compute Engine and GKE TPUs, including TPU7x and later, use [separate management APIs](https://docs.cloud.google.com/tpu/docs/tpus-in-compute-engine) and aren't covered here. Private node and template metadata is redacted; Steward still compares it when checking for configuration changes.

**Permissions.**

- Scan: `tpu.locations.list`, `tpu.nodes.list`, `tpu.nodes.get`, and `compute.disks.get` for attached data disks. Queued-resource APIs use the Node permissions, and [reservation listing](https://docs.cloud.google.com/tpu/docs/reference/rest/v2alpha1/projects.locations.reservations/list) also requires `tpu.nodes.get`.
- Cleanup adds: `tpu.nodes.update`, `tpu.nodes.delete` and `tpu.operations.get`.

See the [TPU permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/tpu).

**Cleanup.** Nodes and queued resources can be cleaned up; reservations are read-only.

- Deleting a queued resource deletes its reviewed nodes first, then deletes the queued request once its nodes are gone.
- Node cleanup detaches existing data disks, verifies the disks still exist, then deletes the node and its boot disk. Keeping a disk keeps the disk resource; it doesn't create a backup.
- Network resources and reservations stay as separate resources.

See Google's [queued-resource deletion contract](https://docs.cloud.google.com/tpu/docs/reference/rest/v2/projects.locations.queuedResources/delete).

**Limits.** The Cloud TPU APIs can't make these writes conditional on configuration or resource identity. Avoid changing TPU resources while cleanup runs.

### Compute Engine

**Inventory.** VM instances, zonal and regional persistent disks, snapshots, images, instance templates, managed instance groups, instance groups and autoscalers. Google uses separate asset types for some regional and global resources, such as `RegionDisk`, `GlobalAddress` and `GlobalForwardingRule`. Full resource names include the project and zone or region, so VMs with the same name in different zones stay separate.

**Cleanup.**

- **Disks and IPs:** the impact plan shows each disk's and IP's native deletion policy. Where retention is supported, Steward applies it through native operations and verifies it before deleting the VM or managed instance group.
- **Deletion protection:** during an authorized cleanup, Steward removes VM deletion protection as an explicit native preparation step.
- Changed attachments or resource identities stop execution.

### Google Kubernetes Engine

**Inventory.** Clusters and node pools. The cluster's control-plane endpoint must be reachable.

**Permissions.**

- Scan: Kubernetes `get` on the `kube-system` Namespace and `list` on Services, Ingresses and installed Gateway resources.
- Cleanup adds: `delete` on those workload resources, native Container and Compute read, delete and operation permissions, and access to node-group membership.

Kubernetes Secret read permission is not required.

**Cleanup.** Cleanup runs through the native controller. The plan includes the verified node and network impacts. Before deleting the cluster, Steward waits for Kubernetes Service, Ingress and Gateway finalizers. Persistent volumes and pre-existing IPs and certificates are kept according to their verified policy. Unsupported retention or unverified ownership blocks cleanup.

### Hyperdisk Storage Pools

**Inventory.** Steward reads pools with native Compute aggregate lists and maps each zone to its scan region. It records provisioned capacity, IOPS and throughput, written and used capacity, disk counts, provisioning modes, Exapool capacity and sharing settings. Large integer values keep their full precision.

For members, Steward follows every page of `storagePools.listDisks` and records each disk's size, used bytes, IOPS, throughput, attachments and snapshot policies. Member disks must be in the pool's zone, and Steward rereads the pool after listing members to confirm it is the same pool. Disks shared from other projects appear as member summaries only; this connection doesn't read or manage those projects. The disk records themselves are updated by disk scans.

A disk's pool reference is an ordinary dependency; it doesn't mean deleting one deletes the other. Steward also doesn't treat pooled capacity and performance as equivalent to Alibaba Cloud's exclusive physical storage.

**Permissions.**

- Scan: `compute.storagePools.list` and `compute.storagePools.get` (for member lists and pool readback).
- Cleanup adds: `compute.storagePools.delete`, `compute.zoneOperations.get`, and `compute.disks.get` / `compute.disks.delete` for member cleanup.

**Cleanup.** Hyperdisk Balanced and Throughput pools can be deleted after review.

1. Select the pool's local member disks, or a supported VM, MIG or GKE controller that deletes those disks. The plan never selects a controller for you.
2. Cleanup waits until each member disk is gone, then deletes the pool. A plan that keeps a member disk can't delete its pool.

Snapshots stay. Exapools and pools with members from other projects must be cleaned up outside Steward; refresh inventory afterwards. An active future reservation can block deletion; Steward doesn't cancel it. See Google's [pool management guide](https://docs.cloud.google.com/compute/docs/disks/manage-storage-pools).

**Limits.** A denied or partial list fails the scan, and a failed member read also keeps the previous records. Changed configuration or membership requires a fresh scan. The native delete API has no conditional resource ID or etag, so avoid changing the pool while cleanup runs.

### Cloud NAT

**Inventory.** Each router's native `nats` array produces one record per NAT gateway, with public or private type, IP allocation and draining, subnet and NAT64 selection, rules, port allocation and logging. A NAT's identity combines project, region, router and NAT name; it is not a separate REST endpoint or Cloud Asset Inventory type. See the [native Router schema](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers).

Relationship lines to the router, VPC, subnets, addresses and NCC Hubs appear when those resources are scanned too:

- Steward reads literal `nexthop.hub` targets from native CEL equality expressions. Comments and ordinary strings don't count, and packet predicates are never executed.
- For a bare or computed Hub selector, Steward lists the project's NCC spokes, reads the matching VPC spokes, reads membership again and rechecks the router. Every matching Hub stays a possible dependency; a complete empty membership list adds none. Rereads catch changes Steward observes but are not an atomic snapshot.
- A regional NAT links to a global Hub only when the Hub's full identity is in the same connection and partition. Missing Hubs and Hubs in other projects stay unresolved references.
- Rescan NAT records from older versions before relationships for unbound Hub expressions can be rebuilt.

See the [Private NAT configuration guide](https://docs.cloud.google.com/nat/docs/set-up-private-nat).

**Permissions.**

- Scan: `compute.routers.list` and `compute.routers.get`. For bare or computed Hub selectors, also `networkconnectivity.spokes.list` and `networkconnectivity.spokes.get`.
- Cleanup adds: `compute.routers.get`, `compute.routers.update` and `compute.regionOperations.get`. Recovering after a missing or expired operation receipt also needs `compute.regionOperations.list`.

**Cleanup.** Steward removes only the reviewed NAT configuration through native Router PATCH, waits for the regional operation and confirms the NAT is gone in a complete router read. The request keeps the latest configuration of the other NATs and leaves BGP peers, interfaces, keys and the router intact. Manually reserved address resources are not deleted.

- Keeping a NAT that refers to a Hub blocks Hub deletion. Selecting both deletes the NAT first; selecting only the NAT keeps the Hub.
- Deleting the parent router also deletes its NATs; see [Cloud Router](#cloud-router).
- NAT changes are serialized with other changes to the same router. If a NAT action failed or was canceled and has no runnable jobs, Steward releases the router once it confirms that no update was sent or that the original operation finished. This doesn't mark the old task or the deletion as successful. Paused tasks and updates with an uncertain outcome stay blocked; you can still continue a failed task in its original task. Missing operation records, incomplete results and denied reads keep the block. See [Serialized changes and recovery](#serialized-changes-and-recovery) and the [operation lookup contract](https://docs.cloud.google.com/compute/docs/reference/rest/v1/regionOperations/list).

**Limits.** Malformed CEL, or an incomplete, denied, missing or changed router or spoke response, fails that scan item and keeps the previous records. Only a complete, matching router response shows that a NAT is gone. Native PATCH has no configuration revision precondition, so another writer can still race the final read and update; avoid changing the router's NAT configuration while cleanup runs. See the [PATCH and request-ID contract](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/patch).

### Cloud Router

This section covers routers, their BGP route policies and named sets, and how Steward orders changes to one router. Before cleaning up a router, scan it together with its NATs, route policies and named sets.

#### Routers

**Inventory.** Steward reads current details for each listed router, checks its identity and records the NAT, BGP and interface configuration. MD5 authentication material is redacted from inventory and API logs. See the [Router GET contract](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/get).

**Permissions.**

- Scan: `compute.routers.list` and `compute.routers.get`.
- Cleanup review adds: `compute.routers.listRoutePolicies`, `compute.routers.getRoutePolicy`, `compute.routers.listNamedSets` and `compute.routers.getNamedSet`.
- Router deletion adds: `compute.routers.delete` and `compute.regionOperations.get`, plus the permissions for its prerequisite policy and named-set deletions.

**Cleanup.** Steward lists and reads every native child of the router, checks it against the scan and reports children that aren't in inventory as blockers.

- Route policies and named sets become prerequisite deletions.
- Reviewed NATs are part of the deletion impact and can't be kept while deleting the router: Google [deletes a router's Cloud NAT gateways with the router](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/managing-routers). To remove only a NAT, use [Cloud NAT](#cloud-nat) cleanup.
- Steward uses native [Router DELETE](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/delete), resumes the regional operation after a restart, and confirms the router and its prerequisites are gone before recording the NATs as deleted. It sends no separate NAT or address delete request.
- Associated VPN tunnels and VLAN attachments block cleanup. Remove them and scan again before creating the task.
- Other configuration changes require a new review. Router tasks created before configuration reviews existed need a fresh scan before deleting.

**Limits.** A denied, missing, incomplete or mismatched detail response fails the scan item and keeps the previous records. Reads and native deletion aren't atomic, so avoid editing the router while cleanup runs.

#### Route policies

**Inventory.** Steward reads each router's import and export policies in the router's region and fetches every policy's details, including CEL terms and fingerprint. Policies with the same name on different routers stay separate. `bgpReferences` lists the peers and import/export directions that use a policy.

Steward also parses native CEL calls to `prefixSets('name')` and `communitySets('name')` into dependencies on named sets on the same router. Scan both policies and named sets to see these relationship lines. Strings and comments aren't treated as calls, and expressions are never executed.

See Google's [policy list API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/listRoutePolicies) and [policy detail API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/getRoutePolicy).

**Permissions.**

- Scan: `compute.routers.list`, `compute.routers.listRoutePolicies` and `compute.routers.getRoutePolicy`.
- Cleanup adds: `compute.routers.get`, `compute.routers.deleteRoutePolicy` and `compute.regionOperations.get`.
- If BGP peers use the policy, also `compute.routers.update`. Google's [audit permission list](https://docs.cloud.google.com/compute/docs/logging/audit-logging) also names `compute.networks.updatePolicy` on the router's network.

**Cleanup.** Run a fresh scan before creating the task, so it captures the policy fingerprint, the router ID and the BGP peer configuration.

1. If peers use the policy, Steward first removes its name from their import and export lists with [Router PATCH](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/patch). Every other policy keeps its original order, other peer settings are preserved and unrelated router fields aren't sent.
2. After that regional operation finishes and the updated peers are visible, Steward deletes the policy with the native [policy deletion API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/deleteRoutePolicy).
3. Steward confirms the policy is gone while the same router is still readable; a finished operation alone doesn't prove deletion. An unreadable router is reported as a dependency failure.

Both phases resume after a restart. Deleting a policy keeps its named sets and never selects other policies.

You can delete several policies one after another from the same scan. Before updating peers, Steward reads the current configuration and accepts earlier removals only after the API confirms those sibling policies are gone. New references, reordered policies, changed peers or settings, and unreadable sibling policies stop execution. Other configuration changes, native dependency conflicts and permission failures are reported for review.

**Limits.** A failed detail read keeps the last successful record; a successful empty policy list marks old policies as deleted. Malformed CEL, or a computed set name that can't be resolved, fails that policy scan item and keeps previous records. These changes have no fingerprint precondition, so avoid editing policies or BGP peers while cleanup runs.

#### Named sets

**Inventory.** Each router's prefix and community sets, with type, description, CEL expression elements and fingerprint. Names are unique only within a router, so the identity includes the region and router. See the [native set details](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/getNamedSet).

**Permissions.**

- Scan: `compute.routers.list`, `compute.routers.listNamedSets` and `compute.routers.getNamedSet`.
- Cleanup adds: `compute.routers.get`, `compute.routers.listRoutePolicies`, `compute.routers.getRoutePolicy`, `compute.routers.getNamedSet`, `compute.routers.deleteNamedSet` and `compute.regionOperations.get`.

**Cleanup.** Google [prevents removing a set that any policy on its router refers to](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/bgp-route-policies/update-named-sets). Steward checks every policy on the router, including policies you didn't scan.

- A known referring policy outside the cleanup selection blocks the plan. When you select both, the policy is deleted first. Deleting only a policy keeps its sets.
- Steward verifies the scanned set revision and router identity, waits for the regional operation and confirms the set is gone. See the [native deletion API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/deleteNamedSet).
- Incomplete policy reads, unresolved references, changed resources or provider conflicts stop cleanup.

**Limits.** Failed or incomplete reads keep the earlier records.

#### Serialized changes and recovery

Steward runs the selected Router, NAT, route-policy and named-set deletions on one router one at a time, even when execution concurrency is higher. Other routers proceed independently. A cleanup task that overlaps can't start or continue while that router has an unresolved change.

Tasks created by older Steward versions get this ordering added when they start or continue; their steps and reviewed snapshots don't change. If the added ordering would affect jobs that are still running, or cloud requests whose outcome isn't confirmed, the task can't continue.

When a task failed or was canceled and all its jobs have ended, Steward can release the router after read-only checks confirm that every change it might have made has ended:

- **Policy attached to peers:** both the original BGP detachment and the policy deletion, including a deletion sent before its receipt was saved. The detachment finishing alone doesn't release the router.
- **Unattached policy or named set:** its single deletion operation.
- **Router:** the original delete operation. Recovery also needs the NAT, policy and named-set reviews from the original task; missing or changed reviews keep the router blocked. For tasks from older versions that recorded only the operation, Google's operation record must echo the original request UUID, target and numeric router ID.

Recovery needs `compute.regionOperations.get`. Lost or expired receipts need `compute.regionOperations.list` as well. Missing, ambiguous, incomplete or inaccessible operation history, older receipts that can't be identified, and missing proof of the router's identity keep the router blocked. Recovery keeps the old execution status: it doesn't resume or retry the deletion and doesn't mark any resource deleted or the cleanup successful. See the [operation lookup API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/regionOperations/list).

### Firewall policies

**Inventory.** Hierarchical policies within the connection's **Firewall scope**, global and regional network policies, and their native associations. The scope explicitly adds the organization or folder and its descendant folders; project access alone doesn't enable it. Include the global scope when scanning.

Removing or changing the **Firewall scope** doesn't mark previously found policies as deleted, and their old cleanup plans must be reviewed again.

**Permissions.** Resource Manager read access for the organization or folder tree, and native firewall-policy permissions.

**Cleanup.** Steward first removes each reviewed association with its native API, then deletes the policy and its rules. Associations can also be removed on their own.

- Removing an association changes firewall enforcement for its target; the network, organization or folder stays.
- The target must stay within the configured scope. Changed policies, associations or target identities block the plan. Hierarchical cleanup also checks the target's association list.

See Google's [hierarchical policy guide](https://docs.cloud.google.com/firewall/docs/manage-hierarchical-firewall-policies) and [global network policy guide](https://docs.cloud.google.com/firewall/docs/use-network-firewall-policies).

**Limits.** The APIs can't atomically tie these reads to the deletion, and same-name associations have no creation token. Avoid changing policies while cleanup runs.

### BigQuery

**Inventory.** Datasets and tables. After listing, Steward reads each one's native details to get properties such as encryption settings, row counts and byte counts.

**Permissions.** Scan: list access plus `bigquery.datasets.get` and `bigquery.tables.get`. See the [dataset](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/datasets/get) and [table](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/tables/get) detail methods.

**Limits.** A denied, missing or mismatched detail response fails that scan item and keeps the existing inventory.

### Bigtable

**Inventory.** After listing tables, Steward reads their full native metadata, including column families, replication state, backup policy and deletion protection.

**Permissions.** Scan: `bigtable.tables.list` and `bigtable.tables.get`. See the native [list](https://docs.cloud.google.com/bigtable/docs/reference/admin/rest/v2/projects.instances.tables/list) and [detail](https://docs.cloud.google.com/bigtable/docs/reference/admin/rest/v2/projects.instances.tables/get) methods.

**Cleanup.** Table deletion and instance deletion both read full table metadata and honor table deletion protection.

**Limits.** A denied, missing or mismatched detail response fails the scan item and keeps the previous records.

### Data Fusion

**Inventory.** Instances, DNS peerings and namespaces through the Data Fusion management API. Individual CDAP pipelines, datasets, secure-store entries and their runtime compute aren't included; see the [CDAP API reference](https://docs.cloud.google.com/data-fusion/docs/reference/cdap-reference). Private options and namespace policies are redacted after configuration checks.

**Permissions.**

- Scan: `datafusion.locations.list`, `datafusion.instances.list`, `datafusion.instances.get`, `datafusion.namespaces.list` and `datafusion.namespaces.getIamPolicy`.
- Instance cleanup adds: `datafusion.instances.delete` and `datafusion.operations.get`.
- Deleting a DNS peering on its own uses `datafusion.instances.update`.

See the [native method permissions](https://docs.cloud.google.com/data-fusion/docs/how-to/audit-logging) and [permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/datafusion).

**Cleanup.** Instance cleanup includes the reviewed namespaces and DNS peerings and waits for the native operation and for the resources to disappear. DNS peerings can also be deleted on their own. Namespaces are removed only with their instance; Steward doesn't enable the unrecoverable reset that deleting a namespace on its own would need.

- Unreadable policies, new children or changed configuration block cleanup.
- User data and the referenced storage, networks, service accounts, keys and topics remain separate resources.

See Google's [instance deletion guide](https://docs.cloud.google.com/data-fusion/docs/how-to/delete-instance).

**Limits.** These delete APIs have no atomic configuration condition, so avoid changing the instance while cleanup runs.

### Dataform

**Inventory.** Folders, team folders, repositories, workspaces, release and workflow configurations, workflow invocations and compilation results. Folder searches reflect the connection's access: resources hidden by a sharing change stay recorded until their deletion is verified. Google's [team-folder search contract](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.teamFolders/search) describes this limit. Checks read secret references without fetching secret values.

**Permissions.**

- Scan: `dataform.locations.list` and the `list`/`get` permissions for repositories, workspaces, release configs, workflow configs, workflow invocations and compilation results.
- Folder discovery and cleanup: `dataform.folders.get`, `dataform.teamFolders.get` and `dataform.folders.queryContents` for the accessible tree.
- Cleanup adds: the `delete` permission of each type that can be deleted on its own, `dataform.workflowInvocations.cancel` for running work, and `dataform.folders.delete` and/or `dataform.teamFolders.delete` for the selected folders.

See the [Dataform permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/dataform).

**Cleanup.**

- **Repository:** Steward deletes its workspaces, release and workflow configurations and invocation records, then the repository and its reviewed compilation results. Running invocations are cancelled first and must reach a terminal state.
- **Folder:** includes nested folders and repositories; each member is deleted before its parent. A moved or recreated ancestor stops child actions and resumed cancellations.
- New members, changed configuration or unreadable dependencies block cleanup.
- Git remotes, referenced secrets and BigQuery output tables remain separate resources. Cancelling doesn't roll back BigQuery work that already finished.

See Google's [repository deletion contract](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.repositories/delete) and [cancellation behavior](https://docs.cloud.google.com/dataform/docs/reference/mcp/tools_list/cancel_workflow_invocation).

### Dataproc

**Inventory.** Clusters, jobs, auxiliary node groups, autoscaling policies and workflow templates.

**Permissions.**

- Scan: `compute.regions.list`, `dataproc.clusters.list` / `dataproc.clusters.get`, `dataproc.jobs.list` / `dataproc.jobs.get`, `dataproc.nodeGroups.get` and the policy and template `list/get` permissions.
- Cluster cleanup adds: `dataproc.clusters.delete`, `dataproc.operations.get`, and Compute read/list permissions for the reviewed VMs, disks, instance groups and templates.
- Selected jobs need `dataproc.jobs.cancel` / `dataproc.jobs.delete`; selected policies and templates need their own delete permissions.

See the [Dataproc permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/dataproc).

**Cleanup.** Cluster cleanup reviews the managed VMs, instance groups, generated templates and disks. Auxiliary node groups can't be deleted on their own.

- Job history is kept by default. If you also select job records, Steward cancels active jobs and deletes their records before the cluster.
- Existing attached disks with `autoDelete=false`, storage buckets, autoscaling policies and external services remain separate resources.
- Virtual-cluster cleanup keeps its GKE cluster and node pools.
- Deleting a workflow template doesn't cancel workflows already running from it.

See Google's [cluster deletion contract](https://docs.cloud.google.com/managed-spark/docs/reference/rest/v1/projects.regions.clusters/delete) and [GKE cleanup behavior](https://docs.cloud.google.com/managed-spark/docs/guides/dpgke/quickstarts/gke-quickstart-create-cluster).

**Limits.** Steward checks the cluster UUID and reviewed configuration, but the APIs can't lock jobs and Compute membership together. Keep the provider's identity metadata and labels intact and avoid changing the cluster while cleanup runs. Unexpected Compute autoscalers or stateful-group policies stop cleanup for review.

### Discovery Engine

**Inventory.** Collections, data stores, apps, schemas, controls, serving configurations, sessions, conversations, assistants, document branches and documents, and website targets, in the `global`, `us` and `eu` locations. US and EU appear in the region picker even before Cloud Asset Inventory finds a resource there. Preview agent and runtime resources, and every data-plane record, aren't inventoried separately.

Document text, conversation turns, prompts, schemas and connector configuration are redacted before inventory or logs are stored; configuration checks run before redaction.

**Permissions.**

- Scan: the native `list`/`get` permissions of the selected kinds, collection and ancestor reads, `discoveryengine.dataConnectors.get` for connector-backed collections, and website and sitemap reads for website data stores.
- Cleanup adds: the selected kinds' `delete` permission and `discoveryengine.operations.get`.

See the [permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/discoveryengine).

**Cleanup.**

- Deleting an app keeps its linked data stores.
- Delete or unlink every linked app before deleting a data store. Steward doesn't unlink an app you didn't select.
- The plan includes discovered child configurations, conversations and documents. Branches and site-search configuration have no delete API of their own and are removed with their data store. Deleting an app or data store also removes the service data it contains.
- Collection cleanup deletes the collection's apps and data stores first.
- Data-store deletion can take days. The task waits for the native operation and for the reviewed resources to disappear, including after a restart.

See Google's [data-store deletion requirements](https://docs.cloud.google.com/generative-ai-app-builder/docs/delete-a-data-store).

**Limits.** These delete methods have no atomic configuration or creation-ID condition, so avoid changing resources while cleanup runs.

### Cloud Monitoring

This section covers alert policies, custom dashboards, groups, metrics scopes and uptime checks. Except for metrics scopes, cleanup requires a fresh scan and deletes only if the resource still matches the configuration reviewed in the task. Deletion is synchronous; Steward confirms it with a separate read that returns 404, including after a worker restart. Google's DELETE methods have no revision or etag condition, so avoid editing these resources outside Steward while cleanup runs.

#### Alert policies

**Inventory.** Steward lists and reads every policy in the project, including disabled and invalid ones. It records enabled state, severity, user labels, condition types, notification-channel names and creation and mutation records. Documentation and condition expressions are reviewed and then redacted from stored assets and API output.

**Permissions.**

- Scan: `monitoring.alertPolicies.list` and `monitoring.alertPolicies.get`, plus `monitoring.dashboards.list/get` for the dashboard check below (it runs even for policy-only scans).
- Cleanup adds: `monitoring.alertPolicies.delete`.

**Cleanup.** Notification channels and monitored resources stay. Steward also checks dashboards in the connection project for references to the policy:

- `AlertChart.name` uses the full project and policy path; `IncidentList.policyNames` uses `alertPolicies/ID`. An incident list without a policy filter counts as a possible reference to every policy in the project.
- Text, logs and metric query strings don't count as references just because they contain a policy name. Unknown widget structures or malformed policy names block cleanup.
- A referring dashboard found in a fresh scan must be selected explicitly; it is deleted before the policy. Steward repeats the reference check before deleting and confirms each selected dashboard is gone. After a restart, the task keeps the same dashboard prerequisites.
- Partial dashboard reads or changes during the check block cleanup. If the policy inventory is stale, scan again before the relationships are refreshed.

See the [native deletion contract](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.alertPolicies/delete) and the [AlertChart and IncidentList contracts](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards#AlertChart).

**Limits.** Failed, incomplete or changed reads keep the previous records. Only dashboards in the connection project are checked; references from other projects' dashboards aren't covered yet.

#### Custom dashboards

**Inventory.** Custom dashboards whose native list and read results match, including `etag`, layouts and unknown fields. Queries, text, annotations and widget contents are redacted before storage and in API responses; display metadata and a configuration digest remain. System dashboards are outside project cleanup.

**Permissions.**

- Scan: `monitoring.dashboards.list` and `monitoring.dashboards.get`.
- Cleanup adds: `monitoring.dashboards.delete`.

**Cleanup.** Steward reads the dashboard twice, then sends a DELETE with an empty body and no caller parameters. Changed configuration or protected labels stop the action. After a restart the task resumes the same request. See the [dashboard deletion contract](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards/delete).

**Limits.** Including `etag` in the review can't stop an external write between the final read and the DELETE.

#### Groups

**Inventory.** The native group hierarchy and the monitored resources that were members during a fixed one-minute window. Compute instances are matched by their immutable numeric IDs, so a replacement VM with the same name doesn't inherit a reference. Group filters and descriptor-defined member labels are redacted. Unmapped member types, including AWS members, stay unresolved. Uptime checks that target a group join network scans through supported member references.

Membership is dynamic: it doesn't imply ownership, a retention policy or cascading deletion.

**Permissions.**

- Scan: `monitoring.groups.list` and `monitoring.groups.get` on the scoping project. Member listing uses the same `monitoring.groups.get`.
- Dependency refresh: `monitoring.uptimeCheckConfigs.list/get`, `monitoring.alertPolicies.list/get` and `monitoring.dashboards.list/get`.
- Cleanup adds: `monitoring.groups.delete`.

See the [native group contract](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.groups) and [member time-window contract](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.groups.members/list).

**Cleanup.** Before cleanup, Steward refreshes dependencies by reading every group, uptime check, alert policy and dashboard in the project, listing and reading each one and listing again to catch changes.

- Child groups, uptime checks that target the group, alert policies whose filters refer to it and dashboards that refer to it must be selected explicitly. Steward never selects members or parent groups automatically.
- In dashboards, time-series filters and ratio denominators are inspected through the native schema; text and log content don't count. Dynamic `GROUP` filters, template variables, unknown structures and unparsed MQL, PromQL or SQL queries block cleanup when their references can't be established.
- Selected consumers are deleted before the group, and Steward confirms each is gone. Right before deletion, Steward rereads the group and checks the consumers twice. Remaining consumers, changed configuration or uncertain reads prevent the DELETE.
- Deletion always uses `recursive=false`, and the value can't be overridden. Group members are kept.
- After a restart, the task resumes the same request with the same prerequisites.

See [Monitoring group selectors](https://docs.cloud.google.com/monitoring/api/v3/filters), the [dashboard contract](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards) and the [nonrecursive deletion contract](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.groups/delete).

**Limits.** Failed, inconsistent or incomplete member reads keep the previous record. Permission failures, incomplete pages or changes during the dependency refresh fail it and keep the previous relationships.

#### Metrics scopes

**Inventory.** The connection project's scope, its monitored-project links and references from other projects' scopes. Scans use the full scope response; a scope that can't be read doesn't count as proof that a link disappeared.

**Permissions.**

- Scan: `resourcemanager.projects.get` on the connection project.
- Cleanup: `monitoring.metricsScopes.link` on both the scoping project and the monitored project, plus access to the returned Monitoring operation.

**Cleanup.** You can remove selected monitored-project links; the scope and its own project link are read-only. Removing a link changes which metrics the scoping project can query. It keeps the monitored project, time-series data, dashboards and alerting configurations. Incoming references from other projects need their own connections to clean up. Steward compares the scope and link creation times again before deleting and while waiting.

See Google's [metrics-scope configuration guide](https://docs.cloud.google.com/monitoring/settings/multiple-projects) and the native [read](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/locations.global.metricsScopes/get) and [unlink](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/locations.global.metricsScopes.projects/delete) contracts.

**Limits.** The API has no atomic creation-time or etag condition, so avoid relinking projects while cleanup runs.

#### Uptime checks

**Inventory.** Project uptime checks, each listed record verified against its detail read. Steward records HTTP, TCP, monitored resource or group and synthetic monitor settings, schedules, checker regions and user labels. The native name is the identity; display names need not be unique. Request authentication, headers and bodies are redacted from assets and API output.

Relationship lines to targets cover GCE instances (by numeric ID), synthetic Cloud Run functions, Cloud Run services, Service Directory services, the cluster containing a Kubernetes Service, and Monitoring groups. Explicit internal checkers link to their peer-project VPCs. Network scans follow these identities; URLs in labels or response matchers don't establish network membership. Missing targets stay unresolved, and selecting a check never selects or deletes its target. App Engine and AWS targets, individual Kubernetes Services and implicit legacy internal-checker networks aren't resolved yet.

**Permissions.** `monitoring.uptimeCheckConfigs.list`, `monitoring.uptimeCheckConfigs.get` and, for cleanup, `monitoring.uptimeCheckConfigs.delete`.

**Cleanup.** Google rejects deleting a check that an alert policy still uses, so Steward looks for alert policies and dashboards that refer to the check. It reviews the connection project, every metrics-scoping project it discovers and the applicable log-routing destinations:

- Time-series filters and ratio denominators are parsed for the uptime metric and check ID. Log panels on the default host are checked through the verified log route and filter; an explicit monitored-project log source is recognized.
- Query templates, unsupported query languages, unknown structures and unreviewed sources in other projects or log views block cleanup.
- Referring policies and dashboards in the connection project must be selected explicitly; they are deleted before the check.
- Consumers in other projects stay blockers that need their own connection; they aren't deleted through this one.
- Steward rereads the metrics scopes and log routes around its reads; permission failures and changes block cleanup.

Deleting a synthetic check keeps its Cloud Run function. When you select both a check and its target, the check is deleted first. An empty DELETE response alone doesn't prove the check is gone. See the [Monitoring deletion contract](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.uptimeCheckConfigs/delete).

**Limits.** Failed or changed reads keep the previous records; only a complete empty list shows a check is gone. Secrets that Google masks can't be compared in plaintext. Dashboards outside the discovered projects, detailed log-view access and unsupported query languages aren't covered yet.

#### Write coordination

Within one connection and project, Steward runs writes to alert policies, dashboards, groups, uptime checks and notification channels one at a time. If a request's response is lost, or a failed or canceled request has an uncertain outcome, the project stays reserved; continue the original task so Steward can verify the result. An empty or lost response can't release the reservation on its own. Tasks from older versions that reserved each resource type separately are still recognized. Other connections to the same project and external clients are outside this coordination.

### Cloud Billing budgets

**Inventory.** Budgets in the billing accounts the credential can see, including closed accounts, plus the connected project's single-project budgets found through its billing account link. The project path works even when listing billing accounts returns nothing or is denied. Records show the actual billing account, amount, thresholds and ownership scope. They appear under the connection's global resources; the connected project doesn't own them. Notification delivery settings and spending filters are excluded from stored configuration and redacted from API output.

A budget that drops out of a visible list isn't treated as deleted. Steward rereads saved budgets and confirms their accounts are still readable; only the budget's own 404 marks it deleted. Permission errors keep the history.

A freshly scanned budget shows relationship lines to the notification channels it uses. Cleanup of email notification channels stays blocked, because Steward can't yet establish every budget that might use them; project-scoped discovery doesn't establish that either.

**Permissions.**

- Scan through billing accounts: billing-account visibility, `billing.accounts.get`, `billing.budgets.list` and `billing.budgets.get`.
- Scan through the project: `resourcemanager.projects.get` and `billing.resourcebudgets.read`.
- Cleanup adds: `billing.budgets.delete`. For budgets found through the project, `billing.resourcebudgets.write` in addition to the project read permissions. No account-close or project-delete permission is used.

See the [Budget access-control requirements](https://docs.cloud.google.com/billing/docs/how-to/budget-api-access-control).

**Cleanup.** Cleanup needs a fresh scan of both the budget and its account. Steward checks their configuration before DELETE and confirms the budget's own 404, including after a restart. Losing the account or a denied read isn't treated as proof of deletion. Notification channels and spending projects stay.

For a budget found through the project, Steward also checks its single-project spending filter and that the project's billing association hasn't changed. Relinking the project or losing project access blocks cleanup and the check that the budget is gone. Other billing configuration changes require a fresh cleanup review. See the [Budget deletion contract](https://docs.cloud.google.com/billing/docs/reference/budget/rest/v1/billingAccounts.budgets/delete).

**Limits.**

- A failed read inside an account that is already visible fails the scan and keeps the history.
- Writes to one account run one at a time within a connection. If a response is lost, continue the original task; failed or canceled attempts with an uncertain outcome keep the account reserved. Other connections and external clients are outside this coordination.
- The API has no conditional DELETE and doesn't expose some Console-only settings, so changes by other clients can race the final read.

### Cloud Identity groups

**Inventory.** Groups and member relationships in the connection's **Group directory**. Include the global scope in scans. If you change the directory or the service account loses access, Steward keeps the groups it found earlier rather than marking them deleted.

**Permissions.** Enable the Cloud Identity API and grant the service account suitable directory group permissions; project access is not enough. Google supports a service account with a Groups Admin role [without domain-wide delegation](https://docs.cloud.google.com/identity/docs/how-to/setup).

**Cleanup.** Groups can be deleted after review, and ordinary member links can also be removed on their own.

- Deleting a group removes its reviewed member relationships and keeps the member users, service accounts and nested group objects.
- Locked groups are protected. Dynamic memberships are managed by Google and can't be removed individually.
- Changes to the group's configuration or membership block older plans.
- Group deletion is irreversible and affects access. External IAM bindings and references in other products need separate review.
- A permission error alone isn't treated as deletion.

See Google's [group deletion contract](https://docs.cloud.google.com/identity/docs/reference/rest/v1/groups/delete) and [identity model](https://docs.cloud.google.com/architecture/identity/overview-google-authentication).

### Infrastructure Manager

**Inventory.** Deployment groups, deployments, revisions, resource records, previews, and change and drift records.

**Permissions.**

- Scan: `config.locations.list` and the native `get`/`list` permissions for deployments, revisions, resources, previews, resourcechanges and resourcedrifts. Deployment groups also need the native `config.deploymentgroups` and `config.deploymentgrouprevisions` read/list permissions.
- Cleanup adds: `config.deployments.delete` or `config.previews.delete`, `config.operations.get`, and read/list permissions for the physical resources. Deployment groups also need `config.deploymentgroups.deprovision` and `config.deploymentgroups.delete`.

Infrastructure Manager runs with the deployment's service account and source configuration, which must stay valid. See the [Config permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/config).

**Cleanup.** Child metadata such as revisions can't be deleted on its own.

- **Deployments:** cleanup reviews the resources the current revision provisioned and their nested effects. The native policy either deletes all of them or keeps all of them; deployment and revision metadata is removed either way. Partial retention is rejected. Deployments with opaque or unmapped resources require an explicit `retain_all_resources=true`. Execution accounts and source buckets remain dependencies. See Google's [deployment deletion guide](https://docs.cloud.google.com/infrastructure-manager/docs/delete-deployments).
- **Previews:** cleanup removes only the preview metadata.
- **Deployment groups:** cleanup reviews the current deployments and those removed since the last successful group revision, including their physical resources. Steward deprovisions the group before removing group and revision metadata. `retain_all_resources=true` keeps the physical resources and removes deployment metadata; explicitly keeping every referenced Deployment also keeps those deployments. Partial retention is rejected. A revision with an unknown outcome or an ambiguous success history blocks cleanup until its resource impact can be established. See Google's [deployment group guide](https://docs.cloud.google.com/infrastructure-manager/docs/deployment-groups).

Deployment and deployment-group cleanup stops when a child resource still needs VM or MIG retention or protection changes, GKE workload or network finalizers, or TPU data-disk detachment. Steward can't combine those steps with a deployment teardown. Keep all provisioned resources instead, then clean them up separately after the deployment is removed. The deployment's own protection and deletion policies can also prevent teardown. Final checks verify the actual metadata and physical-resource outcomes, including after a restart.

**Limits.** These APIs have no atomic configuration condition, so avoid changing deployments while cleanup runs.

### OS Login SSH public keys

**Inventory.** The SSH public keys in the connection service account's own OS Login profile, shown under the connection's global resources. Other users' profiles aren't read or changed, and project or instance `ssh-keys` metadata entries aren't separate resources. A key that disappears from the profile is marked deleted only after reading the key itself returns 404.

**Permissions.**

- Scan: enable the OS Login API; `oslogin.users.getLoginProfile` and `oslogin.users.sshPublicKeys.get`.
- Cleanup adds: `oslogin.users.sshPublicKeys.delete`.

**Cleanup.** Steward rereads the key and deletes it. A replaced key or a changed expiration requires a fresh scan. See the [OS Login API reference](https://docs.cloud.google.com/compute/docs/oslogin/rest/v1/users.sshPublicKeys/delete).

### Resource Manager organizations

**Inventory.** The organization that contains the connected project, found through its folder ancestry. If a read fails or the project moves, Steward keeps the earlier organization record.

**Permissions.** Resource Manager read access to every ancestor folder and the organization.

**Cleanup.** Read-only. The public v3 API has no organization delete method, and finding the organization doesn't authorize organization-level cleanup. See Google's [Organization API](https://docs.cloud.google.com/resource-manager/reference/rest/v3/organizations) and [standalone organization lifecycle guide](https://docs.cloud.google.com/resource-manager/docs/delete-standalone-org).

### Security Command Center

All Security Command Center records are read-only in Steward, and Steward modifies no settings. Include the global scope to read global settings.

#### Service settings

**Inventory.** Intended and effective enablement for each service, with module settings and update time. An inherited setting may have a different effective state because of onboarding or billing eligibility. `INGEST_ONLY` means findings are ingested without the service being enabled. These settings don't represent a subscription tier.

Project scans use the service's native locations. Steward also reads the settings of the project's ancestor folders and organization at the same locations; `configurationParent` shows which level owns each setting, and the project's effective settings are kept separately. This doesn't enumerate other projects or every private organization location. Private service configuration is redacted, including in extension fields.

A GKE cluster's service settings can be read only when the full `projects/.../locations/.../clusters/.../securityCenterServices/...` name is already known, through `securitycentermanagement.projects.locations.clusters.securityCenterServices.get`. The same checks, redaction and `securitycentermanagement.securityCenterServices.get` permission apply. Cluster services aren't enumerated automatically and GKE identifier mapping is unverified, so this doesn't establish cluster-wide threat-detector coverage.

**Permissions.** Enable **Security Center Management API** and grant:

- `securitycentermanagement.locations.list`, `securitycentermanagement.securityCenterServices.list` and `securitycentermanagement.securityCenterServices.get`, on the project and on its ancestors.
- `resourcemanager.projects.get`, `resourcemanager.folders.get` and `resourcemanager.organizations.get` to verify the ancestry.

See the [service settings contract](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/organizations.locations.securityCenterServices) and [read permissions](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/projects.locations.securityCenterServices/get).

**Limits.**

- A failed detail read keeps the previous record. A location or service that stops appearing in a list keeps its last record and original last-seen time, and updates when it becomes visible again.
- Each page is checked against the verified ancestry. Denied reads or a moved project fail the page without replacing earlier records. Ancestors that stop being visible keep their original last-seen time.
- Steward validates every record and page token before saving. Malformed or partial list or detail responses keep the last complete record.

#### Organization subscription

**Inventory.** The organization's current Security Command Center tier and the latest subscription's type, start time and end time. The latest subscription may already have ended, so its dates don't mean a paid tier is active; the native tier is shown separately. Steward follows the project's organization and rechecks the ancestry after reading; a moved project or lost access keeps the earlier record. These records don't describe a project's separate billing entitlement.

**Permissions.** Enable **Security Command Center API** and grant `securitycenter.subscription.get` on the organization, plus `resourcemanager.projects.get`, `resourcemanager.folders.get` on intervening folders, and `resourcemanager.organizations.get` for ancestry discovery. See the [subscription contract](https://docs.cloud.google.com/security-command-center/docs/reference/rest/v1beta2/organizations/getSubscription) and [Security Command Center permissions](https://docs.cloud.google.com/iam/docs/roles-permissions/securitycenter#securitycenter.subscription.get).

**Limits.** A permission failure or a missing subscription response fails the scan.

#### Billing metadata

**Inventory.** The Security Command Center tier explicitly set on each project and location, kept separate from the organization subscription. Steward doesn't infer an inherited tier, trial period or expiry date. Project scans follow the service's native location list, and global and regional records are separate. Organization billing is read through the project's verified ancestry at the same locations, and `configurationParent` tells project and organization values apart. There is no folder-level billing setting in the API. Organization billing is explicit configuration, not proof of project entitlement or trial history.

**Permissions.**

- Project: enable **Security Center Management API** and grant `securitycentermanagement.locations.list` and `securitycentermanagement.billingMetadata.get`.
- Organization: `securitycentermanagement.billingMetadata.get` on the organization, plus Resource Manager project, folder and organization GET permissions.

See the [project billing contract](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/projects.locations/getBillingMetadata).

**Limits.** Failed or changed reads keep earlier records, and a location that disappears from the visible list keeps its last record. A lost ancestor or hidden location keeps its last record and timestamp; a denied, changed or missing organization read fails that page.

## Troubleshooting

| Symptom | What to check |
| --- | --- |
| The JSON key cannot be validated | Use a complete service account JSON key; confirm that the account is enabled and the key has not been revoked. |
| A disabled API or `SERVICE_DISABLED` error | Enable the API named in the error in the target project, then **Retry**. |
| Validation or scanning returns `403` | Check the service account's grants on the target project, including for accounts from another project, and any organization policies. |
| A product's scan item fails with a permission error | Grant the scan permissions listed in that product's section under [Service-specific notes](#service-specific-notes), then **Retry**. |
| A newly created resource is missing | Check the project and region, resolve failed scan items, and scan again after Cloud Asset Inventory updates. |
| Multiple VMs have the same name | Compare projects and zones; different locations have distinct full resource names. |
| Protection settings block cleanup | Inspect the reported VM, Cloud SQL, disk `autoDelete` or bucket condition, or the protected label. Changing a local record doesn't resolve it. See [Deletion protections](#deletion-protections). |
| Cleanup is blocked because configuration changed or needs a refresh | Scan the resource and its dependencies again, then create a new cleanup task. |
| A router cleanup can't start because the router has an unresolved change | Another task still has a change pending on that router. **Continue** or finish that task; see [Serialized changes and recovery](#serialized-changes-and-recovery). |
| A Monitoring or budget cleanup stays reserved after a lost response | **Continue** the original task so Steward can verify the outcome. |
| A Security Command Center scan started before an upgrade fails | Start the scan again. |
| A Discovery Engine data store is still being deleted after hours | Data-store deletion can take days; the task keeps waiting, including after a restart. |

## Next steps

[Scan resources](./scans.md) · [Resource relationships](./topology.md) · [Clean up resources](./cleanup.md)
