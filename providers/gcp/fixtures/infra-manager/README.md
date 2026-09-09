# Infrastructure Manager verification

The six native Config v1 resource rules cover Deployments, Revisions, Resource
records, Previews, ResourceChanges and ResourceDrifts. Deployments and Previews
have independent DELETE methods. Their child records disappear with the
controller; no child DELETE method is invented.

## Native contracts and source provenance

- [Deployment deletion](https://docs.cloud.google.com/infrastructure-manager/docs/reference/rest/v1/projects.locations.deployments/delete)
  accepts `force=true` to remove revisions and `deletePolicy=DELETE` or `ABANDON`.
  The first policy destroys actuated resources; the second keeps them while
  removing the deployment and its metadata. The request body is empty and
  `requestId` is a nonzero UUID. The native API does not provide per-resource
  retention or an etag/creation-time precondition.
- [Preview deletion](https://docs.cloud.google.com/infrastructure-manager/docs/reference/rest/v1/projects.locations.previews/delete)
  removes the preview and its child change/drift records. It accepts `requestId`
  but no deployment force or resource-retention policy. A preview's proposed
  changes do not establish ownership of the described physical resources.
- [Deployment resources](https://docs.cloud.google.com/infrastructure-manager/docs/reference/rest/v1/projects.locations.deployments.revisions.resources)
  expose Terraform address/type/state ID, reconciliation intent/state and CAI
  full names. Only reconciled current-revision resources can establish physical
  ownership. Old revisions remain metadata; completed DELETE records own no
  live resource. Terraform type and ID must agree with one whole-object CAI asset.
- The [RPC contract](https://docs.cloud.google.com/infrastructure-manager/docs/reference/rpc/google.cloud.config.v1)
  returns `google.longrunning.Operation`, with regional Config OperationMetadata.
  DeleteDeployment's response type is Deployment and DeletePreview's is Preview,
  rather than `google.protobuf.Empty`. Target, verb, API version, cancellation,
  failure, operation region and typed completion are checked before readback.
- Google's [deletion guide](https://docs.cloud.google.com/infrastructure-manager/docs/delete-deployments)
  requires the last deployment service account and Terraform configuration to
  remain valid. Both root and latest-revision account/source-bucket references
  remain dependencies. The adapter never exports Terraform state or fetches
  source objects, credentials or output values to discover those dependencies.

`native-schemas.json` retains unmodified selected schemas and their transitive
references from the official [Config v1 Discovery document](https://config.googleapis.com/$discovery/rest?version=v1),
revision `20260831`, SHA-256
`15c2ddd49765663081856686923abef3861993df467c711862b67795cf7217e5`.
The catalog selects 16 native methods. Its fragment exactly reproduces from that
raw response; all previously selected source documents remain unchanged.

`terraform-provenance.json` records the source URL, SHA-256 and SetId line for
each of the 68 mapped Terraform resources. Stable-provider implementations are
pinned to HashiCorp's `08e8f4f28ecac4783bd9174dded1480ba0a6061d`; the beta-only TPU
VM implementation is pinned to `c7a6d05ccb84424b850bb71c894db4826955a638` in the
Google beta provider. `physical-identities.json` records their state IDs, CAI
names, native identities and literal GET URLs. The [CAI name reference](https://docs.cloud.google.com/asset-inventory/docs/asset-names)
and [CAI type reference](https://cloud.google.com/asset-inventory/docs/asset-types)
are separately recorded with their source hashes. Distinct regional/global types
and their documented search/analysis aliases are both tested. This catches Spanner's short state
IDs, Bigtable's different CAI hostname, regional Compute aliases, GKE location
names, project-number aliases and IAM email/unique-ID aliases.

The mapping intentionally excludes resource fragments such as IAM members,
policies, peering and bucket objects: naming an asset does not prove ownership
of its containing object's deletion. There is no `google_batch_job` resource in
the inspected provider, and the inspected CAI name table has no TPU queued-resource
entry. Neither is advertised as a verified Terraform-to-CAI mapping.

## Inventory, planning and recovery tests

Run from the repository root:

```sh
go test ./providers/gcp -run TestInfraManager -count=1
go test -race ./providers/gcp -run TestInfraManager -count=1
```

`resources.json` and `physical.json` are synthetic native responses. The primary
scenario inventories 14 assets, including two same-name deployments in different
regions, current and historical revisions, provisioned resources, a preview,
source artifacts and an unrelated historical physical resource. Tests use
literal native HTTP contracts and the actual contributor/solver, then serialize
and resume actions through a new runtime. They verify destructive deployment
cleanup, ABANDON and independent preview cleanup with one controller DELETE.

Native collection enumeration follows all pages, including empty intermediate
pages. Each metadata item is read directly, all child collections are listed
again, and the root is re-read. Partial/unreachable/duplicate/malformed results,
changed parents and failed physical reads cannot establish inventory completeness.
A SQLite scan-worker test proves a failed revision read preserves prior resource
records and observations instead of closing them.

The reviewed manifest separates controller metadata, physical members, observed
physical absences and opaque Terraform records. Its hashes are calculated before
redacting configuration, Terraform inputs/outputs, provider configuration and
change/drift values. Only current-revision resources establish ownership; source
buckets and execution accounts remain dependencies. Metadata always has a delete
impact when its controller is deleted, including when provisioned resources are
retained. Explicit metadata retention remains invalid.

The 68 identity cases validate native absence at the exact product GET URL;
they do not claim successful live destruction of all 68 products. IAM alias tests
also validate live email/unique-ID matching, recreated accounts, project identity
and both aliases when an account is absent. More than 100 additional fault cases
exercise scan completeness, changed plans, options, native operation payloads,
permissions, expired operations, resource replacement and recovery.

Additional scenarios cover empty/failed deployments, prior physical absence,
settling updates, observing an already-running deletion, DELETE 404 while resources
remain, and original operation IDs surviving a settle-to-delete restart. The
existing GKE node-pool/Compute protocol fixture is nested under a deployment: the
plan includes its VMs and disks, a missing pool cannot hide surviving VMs, and
ABANDON verifies retained descendants. No direct child mutation is substituted
for Terraform destruction in these tests.

Readback checks every reviewed metadata/physical resource and native child-driver
cascade. A completed or expired LRO alone is never sufficient. Retained resources
must still match the reviewed configuration; an absent/recreated retained member
fails. During deletion, resources exposing immutable creation/UID fields may
change configuration while remaining pending, but replacements fail. APIs without
such fields fall back to configuration comparison. Root absence is rechecked
after children, and newly observed orphan metadata is rejected.

## Permissions and limits

The [Config IAM index](https://docs.cloud.google.com/iam/docs/roles-permissions/config)
lists `config.locations.list`, the `get`/`list` permissions for deployments,
revisions, resources, previews, `resourcechanges` and `resourcedrifts`, plus
`config.deployments.delete`, `config.previews.delete` and `config.operations.get`.
The resourcechanges/resourcedrifts IAM names are lowercase even though their REST
collections use camel case. Steward also needs native read/list permissions for
reviewed physical resources and their cascades; bucket checks include object
versions. Terraform executes using the deployment's service account, which needs
its own product permissions. Successful connection validation does not prove these
permissions or Terraform configuration remain valid.

The supported request options are typed `retain_all_resources` and
`retain_resources`. Retention must resolve to ABANDON for all physical members;
arbitrary partial retention is rejected. Opaque, multi-asset, non-reconciled or
unmapped Terraform records require explicit `retain_all_resources=true`, so an
unknown resource is never silently included in destructive cleanup. Direct-native
`deletePolicy`/`force` and unknown options are rejected rather than ignored.

Some native child drivers perform preparations before their own DELETE. Config's
Terraform delegation does not currently run those preparations: VM/MIG retention
changes and deletion-protection removal, pending GKE workload/network finalizers,
and TPU data-disk detachment block destructive deployment cleanup. This guard is
tested against the existing native drivers. Those composed preparation flows
remain unfinished; ABANDON keeps provisioned resources available for separate
reviewed cleanup after the deployment is removed. Native-ready cascades, including
GKE node pools and their default retained volumes, are exercised end to end.

Terraform's own deletion policies or protection settings can still prevent
destruction. The native response and physical readback report that outcome; the
adapter does not rewrite Terraform configuration. There is no atomic lock across
all product reads and Config DELETE, and the adapter does not unlock deployments.
Avoid concurrent deployment/resource changes during cleanup.

Google's inspected [Config Connector mockconfig](https://github.com/GoogleCloudPlatform/k8s-config-connector/tree/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockconfig)
implements DeploymentGroup behavior, but has no Deployment/Preview CRUD handlers.
Its presence is therefore not claimed as an independent emulator for this workflow.
The independent JSON Schema validator checks retained official payload schemas;
the deletion behavior uses Steward-owned native protocol scenarios. No independent
Config deletion server or real-cloud acceptance is claimed. DeploymentGroups and
DeploymentGroupRevisions, their deprovisioning policies, broader Terraform mappings
and composed preparation flows remain part of the overall parity work.
