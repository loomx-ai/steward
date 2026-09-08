# Google Cloud API metadata

`source/discovery.json` contains selected native method and schema objects from
Google's Discovery documents, their original source URLs, and SHA-256 hashes of
the complete upstream responses. Transitive schema references are retained.
`source/selection.json` is the reviewed method/resource selection for refreshing
that snapshot. These are metadata files; neither file contains credentials.

To refresh upstream metadata from the repository root:

```sh
python3 scripts/sync-google-catalog.py
go generate ./providers/gcp
go test ./providers/gcp ./internal/provider/catalog ./internal/provider/spec
```

Normal builds, generation, and tests are offline. Review source and generated
diffs together after a refresh. The catalog records real Google method IDs,
versions, HTTPS origins, paths, parameters, response schemas, paging, idempotency
parameters, and source provenance. Resource bindings enumerate the actual
global/regional methods and participate in the catalog and spec revisions.
Resource rules live in `../specs`, with explicit dependency targets and readback
operations. Runtime path matching checks those bindings and the selected project
before constructing a request.

Known resource kinds use their native product list methods. Compute aggregate
responses are routed by their actual zonal/regional/global identity; child kinds
use explicit parent discovery. Product cursors bind the connection, scope, rule
revision, list targets and parent identities. Partial-result warnings and
unreachable locations fail the shard. Cloud Asset Inventory remains a broad,
non-authoritative index for kinds without product rules; it does not overwrite
or close the resources owned by product shards. Network target selection uses
live Compute list methods.

The current catalog has 100 explicit resource rules and 393 selected methods
from 30 official Discovery documents. Extended Compute rules cover VPN and
Interconnect, Private Service Connect, reservations and sole-tenant resources,
network firewall/Cloud Armor policies, SSL policies and remaining proxy/backend
variants. Product rules also cover Redis, DNS, BigQuery, Firestore, Bigtable,
Spanner, Cloud Tasks/Functions, Filestore, AlloyDB, Managed Kafka, API Gateway,
Certificate Manager, IAM, fleets, Cloud Run jobs, Service Directory, Logging and
Monitoring. Registration and wire tests do not close the full parity matrix.

Cloud DNS record identity includes both the fully qualified name and the record
type, including wildcard names. BigQuery binds scalar project/dataset/table IDs
and verifies the response's project and parent dataset; hidden datasets are
included. Project-wide data resources retain their logical global scope and
physical location metadata, including BigQuery multi-regions and Firestore's
default database. Bigtable lists use their documented views and fail on
`failedLocations`. Global API Gateway and Certificate Manager parents are not
fanned out to Compute regions. Child cursors bind available native parent UIDs.

Native cascades for Bigtable and Spanner instances, AlloyDB clusters, Kafka
clusters and Service Directory namespaces/services contribute reviewed child
impacts. Child discovery includes all pages and fresh reads. Execution verifies
the current child set, available incarnation/etag fields, deletion protection,
labels and reviewed retention decisions. AlloyDB's native `force` parameter is
enabled only after these checks. Nested children must disappear after their
parent; a missing parent does not prove completion. The native API does not
support keeping these children while deleting their container, and the plan
reports that restriction. Additional child kinds and other product controllers
remain part of the unfinished parity work.

IAM custom-role `deleted` and Logging bucket `DELETE_REQUESTED` are native soft
deletion states. Inventory skips those records and readback reports
`soft_deleted`; it does not claim immediate physical purge. Firestore, Bigtable,
Spanner, Filestore and Redis deletion protection, locked/required log buckets and
system-managed service-account keys block direct cleanup. Kafka's
`__remote_log_metadata` topic requires cluster cleanup. Fleet membership deletion
through this API is supported only for its documented Google Cloud GKE endpoint.
Private key data, HTTP header maps and Redis authentication strings are redacted.

Cloud KMS rules include key rings, keys, versions and import jobs. The supported
delete operation removes eligible resource records and polls the native
operation before checking absence. It does not schedule destruction of key
material. Import jobs have no delete method. Version state/import restrictions,
automatic rotation, remaining versions/keys and unexpired import jobs are checked
against live APIs before deletion.

Compute instance attachments use native disk `source`, `deviceName` and
`autoDelete` fields. Boot and data disks, including regional disks, contribute
explicit lifecycle impact. Retention uses `compute.instances.setDiskAutoDelete`;
instance deletion protection is disabled through its native method. Preparation
and deletion operations have separate idempotency keys and resume through the
persisted waiter state. A completed operation must also be reflected in a live
read before deletion proceeds. Unreviewed or changed attachments block cleanup.
Local SSDs are integrated instance storage and have no separate disk resource.

Zonal and regional managed instance groups discover native members, per-instance
configurations, the complementary InstanceGroup, and their autoscaler. Effective
stateful disk/IP policy overrides VM attachment policy. Shared read-only disks
are retained. The autoscaler has a separate deletion prerequisite. Standalone
VM/InstanceGroup deletion verifies that a MIG no longer owns the resource.

Retention abandons selected VMs, patches per-instance deletion rules for stateful
disks/IPs, and updates ordinary disk autoDelete flags. Configurations preserve
live metadata and use native fingerprints. When manual application is required,
the driver validates a persisted configuration hash and applies only a REFRESH;
it disallows restart/replacement. Each stage waits for both the native operation
and effective configuration/membership, and resumes from persisted state.
The reviewed plan binds VM/disk/IP incarnations and the complete member set.
Controller completion also requires its complementary InstanceGroup to disappear.
Regional InstanceGroup has no native standalone delete method; its absence is
verified through the owning MIG's cleanup.

GKE node pools are discovered through their native parent cluster and retain the
cluster's unique ID. Cluster and node-pool ownership uses authoritative
`nodePools.instanceGroupUrls`, including blue/green groups. Standard node pools
have separate GKE deletion steps before the cluster; Autopilot pools are owned
by the cluster. Node VMs, complementary groups and boot disks cannot be retained
through the node-pool API. Persistent volumes follow their live attachment
policy and appear as retained resources. The driver checks frozen incarnations,
pool etags, protected labels and native member policies before mutation. Native
GKE operation names, locations and targets are verified; operation completion or
a missing pool does not close live underlying resources. Direct MIG cleanup also
requires `container.clusters.list` to exclude active GKE ownership.

Cluster inventory also takes an authenticated Kubernetes snapshot of GKE-managed
LoadBalancer/NEG Services, GCE Ingresses, and Gateways. A fresh Container API read
binds the cluster UID, native endpoint and CA; DNS endpoints use system trust.
OAuth uses the selected connection. Kubernetes list paging requires a stable
resourceVersion, and deletion requires both UID and resourceVersion. No Secret
contents, kubeconfig, embedded client keys, or arbitrary inventory endpoints are
used. The snapshot stores object identities and configuration hashes only.

Published workload addresses, listeners and native Compute links identify the
frontend graph; internal frontends must agree with the native VPC. NEG ownership
uses the kube-system UID and network. Verified node/template tags and VPC identity
are required for generated firewall rules; routes require a native node next hop.
Address reservations and pre-shared certificates are retained unless their
native controller allocation is established. Secret-generated Gateway certificates
are joined through the exact live HTTPS proxy without reading Secret data.

Cluster deletion first removes Gateway/Ingress/Service objects through their
controllers, preserving finalizers and node availability. Only after objects
are absent may frozen, reviewed leftover Compute resources be recovered in
native dependency order. Cluster/node completion precedes remaining cluster
firewall and route cleanup. Every operation also requires final resource absence;
retained members must still have the original identity. All phases survive
serialization and restart. Changed workloads, identities, protection or membership
stop subsequent writes. Disabling L4 firewall creation does not change the native
controller's teardown of its previously generated rules.

The connection still authorizes one project. Foreign-project resources and
unverifiable ownership cannot be cascaded through that connection. Kubernetes
control-plane reachability and list/read/delete RBAC are required for this
cluster workflow. Shared-VPC host resources, multi-cluster controllers, and
additional GKE custom-resource controllers require further coverage before the
full provider parity acceptance is complete.

Reference material:

- [Google Discovery directory](https://www.googleapis.com/discovery/v1/apis)
- [Cloud Asset Inventory asset types](https://docs.cloud.google.com/asset-inventory/docs/asset-types)
- [Compute REST API](https://docs.cloud.google.com/compute/docs/reference/rest/v1)
- [Disk deletion policy](https://docs.cloud.google.com/compute/docs/disks/modify-persistent-disk)
- [MIG preserved-state deletion and abandonment](https://docs.cloud.google.com/compute/docs/instance-groups/preserved-state)
- [Applying and verifying stateful configuration](https://docs.cloud.google.com/compute/docs/instance-groups/applying-viewing-removing-stateful-config-in-migs)
- [Native per-instance configuration patch](https://docs.cloud.google.com/compute/docs/reference/rest/v1/instanceGroupManagers/patchPerInstanceConfigs)
- [Google API resource names](https://cloud.google.com/apis/design/resource_names)
- [GKE node pool deletion and Autopilot restrictions](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/node-pools)
- [GKE boot disk deletion](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/persistent-volumes/hyperdisk-storage-pools)
- [Native GKE node pool deletion](https://docs.cloud.google.com/kubernetes-engine/docs/reference/rest/v1/projects.locations.clusters.nodePools/delete)
- [GKE cluster deletion and persistent storage](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/deleting-a-cluster)
- [GKE Gateway TLS sources](https://docs.cloud.google.com/kubernetes-engine/docs/concepts/gateway-security)
- [GKE firewall reconciliation settings](https://docs.cloud.google.com/kubernetes-engine/docs/how-to/user-managed-firewall-rules)
- [Native ingress-gce cleanup implementation](https://github.com/kubernetes/ingress-gce/tree/93bdce86f8426bf4f2129c30de9923698351e04d/pkg/l4/resources)
- [Cloud KMS resource deletion and restrictions](https://docs.cloud.google.com/kms/docs/delete-kms-resources)
- [Spanner instance deletion](https://docs.cloud.google.com/spanner/docs/reference/rest/v1/projects.instances/delete)
- [Bigtable instance deletion](https://docs.cloud.google.com/bigtable/docs/deleting-instance)
- [AlloyDB cluster cascade parameter](https://docs.cloud.google.com/alloydb/docs/reference/rest/v1/projects.locations.clusters/delete)
- [Managed Kafka cluster deletion](https://docs.cloud.google.com/managed-service-for-apache-kafka/docs/delete-cluster)
- [Service Directory namespace cascade](https://docs.cloud.google.com/service-directory/docs/reference/rest/v1/projects.locations.namespaces/delete)
- [IAM custom-role soft deletion](https://docs.cloud.google.com/iam/docs/reference/rest/v1/projects.roles/delete)
- [Logging bucket deletion](https://docs.cloud.google.com/logging/docs/reference/v2/rest/v2/projects.locations.buckets/delete)

The checked-in tests establish metadata consistency and protocol behavior. They
do not establish live permissions, eventual inventory consistency, service
availability, or independent emulator coverage.
