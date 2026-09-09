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
before constructing a request. Some Media CDN and Cloud Multicast methods are
omitted from anonymous Discovery responses. Their method/message declarations
come from the official Cloud SDK archive, verified by SHA-256 and parsed without
execution. These operations explicitly record `source_format: google-cloud-sdk`;
[source members and provenance](source/sdk/README.md) are retained for offline
conversion tests and deterministic refresh.

Known resource kinds use their native product list methods. Compute aggregate
responses are routed by their actual zonal/regional/global identity; child kinds
use explicit parent discovery. Product cursors bind the connection, scope, rule
revision, list targets and parent identities. Partial-result warnings and
unreachable locations fail the shard. Cloud Asset Inventory remains a broad,
non-authoritative index for kinds without product rules; it does not overwrite
or close the resources owned by product shards. Network target selection uses
live Compute list methods.

The current catalog has 193 explicit resource rules and 768 selected methods
from 55 official Discovery documents and one pinned Cloud SDK archive. Extended Compute rules cover VPN and
Interconnect, Private Service Connect, reservations and sole-tenant resources,
network firewall/Cloud Armor policies, SSL policies and remaining proxy/backend
variants. Product rules also cover Redis, DNS, BigQuery, Firestore, Bigtable,
Spanner, Cloud Tasks/Functions, Filestore, AlloyDB, Managed Kafka, API Gateway,
Certificate Manager, IAM, fleets, Cloud Run jobs, Service Directory, Logging and
Monitoring, Vertex AI, App Hub, Backup and DR, Dataplex, Datastream,
Sensitive Data Protection, Cloud Domains, IAP, Network Connectivity Center,
Cloud NGFW, VPC Flow Logs, Network Services, Media CDN, Cloud Multicast and
Storage Transfer, Dataform, Batch, Dataproc, Discovery Engine, Cloud TPU and Data Fusion. Registration and wire tests do not close the full parity matrix.

[Cloud Identity evidence](../fixtures/identity-groups/README.md) covers explicitly
scoped directory groups and memberships, full native snapshots, reviewed member
link cascades, independent unlink and persisted deletion receipts. Permission
errors alone cannot prove absence. The pinned Google mock independently exercises
native GET/DELETE and done-only operation behavior through an explicit v1beta1
version alias; its missing list and cascade behavior is documented and tested.

[Firewall-policy evidence](../fixtures/firewall-policy/README.md) covers explicitly
scoped organization/folder policies, global/regional network policies and native
association prerequisites. Complete policy, membership and owner-tree reads bind
inventory and reviewed cleanup. Associations use native GET/removeAssociation
methods; their composite inventory names do not invent REST collection endpoints.
Persisted native operations and final readback validate policy incarnation and
target ownership after restart. The pinned Google mock independently verifies
unassociated hierarchical policy deletion; association and network-policy cases
use protocol fixtures. These APIs have no atomic configuration condition.

[Metrics-scope evidence](../fixtures/metrics-scope/README.md) covers project-number
identities, complete scope/member reads, incoming scope references, protected self
membership and independent project-link cleanup. The pinned Google mockgcp
implementation independently verifies native GET/DELETE/LRO and final absence;
its missing reverse-list method and IAM/delayed-operation cases use explicit
protocol fixtures. Neither test source proves real-cloud acceptance.

[Data Fusion protocol evidence](../fixtures/datafusion/README.md) covers regional
instances, DNS peerings and full namespace policy lists. Instance deletion uses
the native cascade only after two complete child sets match the reviewed plan.
DNS peerings also support independent deletion; their absence is verified through
complete lists because the API has no GET. Namespace records have no independent
management-plane delete and are included in instance cleanup. Native operations,
parent creation/configuration proofs and final readback remain bound after worker
restart. Storage, network, service-account, key and topic references remain
dependencies; CDAP data-plane resources and runtime Dataproc cleanup are separate
coverage. The fixture notes record these limits and the available emulator research.

Batch v1 uses native regional Job lists and embedded TaskGroup names to discover
Tasks. TaskGroups have no independent resource endpoint; Tasks have GET/LIST but
no DELETE. Job UID and immutable configuration bind child cursors and reviewed
cleanup, with configuration hashed before runnable code and environments are
redacted. The native Job DELETE also cancels queued/running work and removes its
task history. A persisted regional operation is checked against its name, scope,
available target/verb/version metadata and the frozen job incarnation; completion
requires native readback, including when an operation record expires.

Job cleanup correlates Compute VMs/disks using the documented Batch UID labels,
native creation identities, VM allocation settings and disk attachments/users.
Complete aggregate lists include all scopes, allowing legacy jobs whose VMs run
outside the job region. Native task and Compute membership are checked again
after detail reads. Only Batch Job DELETE is sent: mutable Compute labels never
authorize a Compute write or silently select an unselected Job. Frozen impacts
and fresh UID queries must confirm absence, including disks left after a VM
disappears. Existing attached disks with `autoDelete=false` are explicit retained
references; an existing disk with automatic deletion, or attempted retention of
a Batch-created disk, blocks cleanup. Referenced instance templates are read to
identify existing disks and remain separate resources, as do buckets, NFS,
secrets, Pub/Sub topics, logs and output data.

Batch has no atomic condition spanning Job/task/Compute reads and Job deletion.
Batch GET does not expose an immutable list of VM IDs. Compute users can edit
correlation labels; labels removed before inventory cannot be reconstructed from
the Batch API. Keep the documented Batch labels intact. Changed or missing
reviewed evidence blocks execution, and lingering correlated resources prevent
successful readback. An explicitly selected orphan VM can use ordinary Compute
cleanup after native GETs in every Batch location prove its original Job UID no
longer exists; unreadable locations or jobs block this fallback.
Synthetic protocol and SQLite-worker coverage, source provenance and native
contracts are in [the Batch fixture notes](../fixtures/batch/README.md).

Dataform v1 uses service-native location and repository lists to discover its
workspaces, release/workflow configurations, workflow invocations and compilation
results. Detail reads bind configuration proofs before redaction; child cursors
and actions also bind the containing repository's configuration and creation
time. Native IAM/service-account/KMS/secret references remain external dependencies.
Compilation source fields are historical provenance, not invented live dependencies
on workspaces or release configurations.

Repository cleanup has four kinds of independently deleted prerequisites and a
reviewed compilation-result cascade. Both native membership passes must agree;
the second pass follows every child detail read, so a workflow created while
another collection is being read blocks deletion. Native GETs verify every
reviewed prerequisite absent before `force=true`, and all reviewed compilation
results absent afterwards. The API offers no atomic membership condition on
`force`; concurrent writers can still change membership after the final read.
ReleaseConfig exposes neither creation identity nor ETag, so an identically
recreated config in the same repository cannot be distinguished by its API.
These native limitations are not replaced with invented version tokens.

Running workflow invocations use POST `:cancel`, persist the cancellation phase,
wait for RUNNING/CANCELING to finish, then issue their native DELETE. Restarting a
saved phase verifies target/configuration/parent identity and does not resend
the acknowledged cancellation. Terminal invocations delete directly. Completion
requires native absence; cancellation does not roll back completed BigQuery work.
The repository's external Git remote, Secret Manager values and BigQuery outputs
are not deleted by this lifecycle. Native contracts and synthetic wire-fixture
provenance are linked in [the Dataform fixture notes](../fixtures/dataform/README.md).

Dataform Folder and TeamFolder discovery combines regional team-folder searches,
user-root content queries and recursive native content queries. These organize
BigQuery single-file code assets; the virtual user root is not a resource to
delete. Search is filtered by caller access, so the `dataform-visible` source
does not claim authoritative project coverage or close missing observations.
Permission loss can hide a still-existing folder even when the search succeeds.
The inventory worker retains those observations until absence is established.

Folder and repository IDs are physical siblings under a location. Native
`containingFolder` and inherited `teamFolderName` establish their actual tree;
configuration proofs bind the complete ancestor chain, including for repository
child actions and cancellation recovery. Each member is planned as a separate
prerequisite; its GET must return 404 before the folder's single-resource DELETE.
Two content passes and detail reads reject moved, recreated, duplicate, cyclic,
unreviewed or unreadable members. Folder-tree cleanup reuses the repository and
workflow cancellation lifecycle. The API exposes no atomic condition spanning
these separate native reads and writes, so concurrent changes after the final
check remain a native API limitation.

Cloud DNS record identity includes both the fully qualified name and the record
type, including wildcard names. BigQuery binds scalar project/dataset/table IDs
and verifies the response's project and parent dataset; hidden datasets are
included. Project-wide data resources retain their logical global scope and
physical location metadata, including BigQuery multi-regions and Firestore's
default database. Bigtable lists use their documented views and fail on
`failedLocations`. Global API Gateway and Certificate Manager parents are not
fanned out to Compute regions. Child cursors bind available native parent UIDs. Where selected native location
methods exist, service lists use those locations and map zones to their scan
region. Project fanout includes service multi-regions; malformed, foreign,
duplicate, cyclic or partial location results cannot establish absence.

Backup and DR data-source removal binds its documented POST `:remove` method
and request-ID body. Backup retention timestamps and unexpired appliance/service
locks block deletion. Cloud Domains accepts the documented terminal registration
states only. IAP uses project numbers on the wire and canonical project IDs in
inventory. DLP raw/wrapped key material is redacted. Catalog refresh retries
transient transport/429/5xx failures with a finite budget; offline Python tests
cover retries, permanent failures and native schema preservation.

Native cascades for Bigtable and Spanner instances, AlloyDB clusters, Kafka
clusters and Service Directory namespaces/services contribute reviewed child
impacts. Child discovery includes all pages and fresh reads. Execution verifies
the current child set, available incarnation/etag fields, deletion protection,
labels and reviewed retention decisions. AlloyDB's native `force` parameter is
enabled only after these checks. Nested children must disappear after their
parent; a missing parent does not prove completion. The native API does not
support keeping these children while deleting their container, and the plan
reports that restriction. Additional child kinds and other product controllers
remain part of the unfinished parity work. NCC hub groups, tables and routes
are native managed objects without independent DELETE methods. They have
inventory rules and reviewed Hub ownership; nested route absence is verified
before Hub cleanup completes. Retained tests cover a root-only Hub selection,
unsupported retention, unreviewed children and recovery after parent absence.

Regional network scans also execute global product bindings, including types
with a single method serving regional and global resources. Media CDN origin,
failover/keyset/certificate/secret dependencies and multicast domain, activation,
association, internal-range and VPC dependencies retain native identity formats.
Structured service state and creation-time incarnation checks are preserved.

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
