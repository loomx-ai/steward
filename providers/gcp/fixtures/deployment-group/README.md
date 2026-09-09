# Deployment Group verification

The native Config v1 DeploymentGroup and DeploymentGroupRevision rules add six
methods: group list/get/deprovision/delete and revision list/get. Revisions are
controller metadata and have no independent delete action. Deployment ownership,
Terraform identity matching and physical readback reuse the
[Infrastructure Manager implementation](../infra-manager/README.md).

## Native contracts and source provenance

- [Deprovision](https://docs.cloud.google.com/infrastructure-manager/docs/reference/rest/v1/projects.locations.deploymentGroups/deprovision)
  is `POST :deprovision` with JSON `force=true` and a global `deletePolicy` of
  `DELETE` or `ABANDON`. It also cleans up deployments removed from the last
  successful revision. The API exposes no request ID or configuration condition.
- [Group deletion](https://docs.cloud.google.com/infrastructure-manager/docs/reference/rest/v1/projects.locations.deploymentGroups/delete)
  is a separate DELETE with an empty body, query `force=true`, a nonzero UUID
  `requestId`, and an explicit `deploymentReferencePolicy`. Steward uses
  `FAIL_IF_ANY_REFERENCES_EXIST` after deprovision, or
  `IGNORE_DEPLOYMENT_REFERENCES` when all reviewed deployments are retained.
- The [native guide](https://docs.cloud.google.com/infrastructure-manager/docs/deployment-groups)
  shows deprovision LRO metadata with `verb=update`, per-unit progress and a typed
  DeploymentGroup response. Deprovision clears deployment references while
  retaining the units and DAG. Steward separately waits for physical outcomes
  before removing group metadata.
- [Group revisions](https://docs.cloud.google.com/infrastructure-manager/docs/reference/rest/v1/projects.locations.deploymentGroups.revisions)
  expose a creation timestamp and a group snapshot. There is no documented
  `latestSuccessfulRevision` field or documented meaning for each alternative ID.
  Steward infers the last successful revision from the greatest creation time
  among snapshots marked `PROVISIONED` or `DEPROVISIONED`. Failed attempts do not
  replace that snapshot. Unknown outcomes, missing required snapshots and an
  ambiguous latest timestamp block cleanup. This inference has protocol tests;
  it has not been confirmed by an independent deprovision server or real cloud.
- [Revision listing](https://docs.cloud.google.com/infrastructure-manager/docs/reference/rest/v1/projects.locations.deploymentGroups.revisions/list)
  uses `deploymentGroupRevisions`, not the deployment API's `revisions` field.
  All pages, detail responses and unreachable entries are checked. A child cursor
  remains bound to its original group and region.

`native-schemas.json` retains 44 unmodified official schemas, including explicit
OperationMetadata and all transitive references, from the
[Config v1 Discovery document](https://config.googleapis.com/$discovery/rest?version=v1),
revision `20260831`, SHA-256
`0a4d3eed2f6ff6b98863520cb9e3718949871c9bfaac7b9cb5f276db16cbfafd`.
The 22-method Config fragment reproduces exactly through the source refresher.
Its existing 16 methods and schemas and all 54 other source documents remain
unchanged. All 187 resource bindings reproduce from selection metadata. Two
catalog generations produced SHA-256
`6156b8e5ddb4f30155486e95068b722337ab24de0c7e87c05be1e72f5abca7a4`.

## Inventory, policies and recovery

Run from the repository root:

```sh
go test ./providers/gcp -run TestDeploymentGroup -count=1
go test -race ./providers/gcp -run TestDeploymentGroup -count=1
```

Thirteen protocol, schema and scan-worker test functions cover the actual native
HTTP boundary, contributor, plan solver and serialized action restart. The main
synthetic scenario has 22 assets and 15 lifecycle impacts. It includes current
deployments, a removed deployment in a different region, an older successful
revision and a newer failed attempt. The current/last-successful union includes
the removed deployment's bucket while leaving unrelated historical resources
and source artifacts outside the cascade. No direct physical-resource mutation
is substituted for Config's own deprovision operation.

| Reviewed choice | Native operations | Required final result |
| --- | --- | --- |
| Delete provisioned resources | Deprovision `DELETE`, then group DELETE | Reviewed deployments, metadata and destructive physical impacts absent |
| `retain_all_resources=true` | Deprovision `ABANDON`, then group DELETE | Deployment/group metadata absent; physical resources and retained descendants unchanged |
| Retain every referenced Deployment via `retain_resources` | Group DELETE with `IGNORE_DEPLOYMENT_REFERENCES` | Group/revision metadata absent; deployments and all descendants unchanged |

The native policy is global: partial deployment or physical retention is rejected.
An opaque Terraform record still requires explicit `retain_all_resources=true`.
Group revision metadata is always removed. Independently selected deployments
retain their existing native cleanup action through the actual solver.

Complete inventory compares two metadata sets and root configuration, hashes
unredacted configuration, and binds each child deployment's physical manifest.
Project-number aliases are accepted within the connection's project; foreign
projects, malformed names and invalid unit DAGs fail. Source configuration,
annotations, Terraform inputs and operation artifact paths are redacted before
export. A failed revision detail read in the SQLite scan test preserves all three
previous revision observations.

More than 100 fault cases cover incomplete lists, changed plans, protected labels,
locked deployments, invalid options, operation target/region/type/verb changes,
cancellation, errors, unexpected unit progress, retained-resource replacement,
permission loss and corrupted persisted phases. Recovery tests cover settling
updates, already-running deprovision, expired LROs, partial typed responses,
remaining deployments or physical resources, mutation 404s and a parent that
disappears between revision reads. An observed deprovision is not issued again.

Completion requires native child-driver readback plus group/revision absence.
The group may gain one new successful deprovision revision only when it describes
the reviewed group incarnation and DAG, has no deployment references and is newer
than every reviewed revision. Extra revisions or changed configurations block
metadata deletion. A missing group or completed operation alone is insufficient.

## Independent Google mock coverage

Google's unmodified [Config Connector mockconfig](https://github.com/GoogleCloudPlatform/k8s-config-connector/tree/673a61419de1b8e4f7d26070ce20dde2daa61da8/mockgcp/mockconfig)
at commit `673a61419de1b8e4f7d26070ce20dde2daa61da8` passed the opt-in
`TestDeploymentGroupIndependentMockGCP` over loopback HTTP. The retained harness
uses its native Config service, storage, HTTP routing and operation service.
This is a separate fixture build, not a new Steward dependency.

The run passed native group creation, GET, DELETE, regional LRO GET, typed
completion, serialized resume and GET absence: 14 forwarded native calls after
setup, with 12 explicitly counted read-only list substitutions. Upstream does
not implement location/group/revision LIST, revision CRUD or deprovision. The
test supplies one fixture location, wraps the native group GET in a singleton
list and supplies the empty revision collection of a never-provisioned group.
It does not replace detail, mutation, LRO or absence responses. This verifies
metadata deletion; it is not independent evidence for deprovision or a complete
inventory emulator. IAM, delayed operations and physical cleanup use local
protocol tests. No real-cloud acceptance is claimed.

To reproduce, check out that exact Config Connector commit in a temporary
directory, copy `testdata/mockgcp/main.go` into a new command directory beneath
its `mockgcp` Go module, then build and run that command using the upstream
module. The harness prints its loopback origin. In the Steward checkout, run:

```sh
STEWARD_DEPLOYMENT_GROUP_MOCKGCP_URL=http://127.0.0.1:PORT \
  go test ./providers/gcp -run '^TestDeploymentGroupIndependentMockGCP$' -count=1 -v
```

Replace `PORT` with the printed port. Stop the harness and remove its temporary
checkout/binary afterward. The recorded run checked that upstream service source
was unmodified and completed this cleanup.

## Permissions and remaining limits

The REST methods require `config.deploymentgroups.get`, `.list`, `.deprovision`
and `.delete`, plus `config.deploymentgrouprevisions.get` and `.list` (see the
revision [GET contract](https://docs.cloud.google.com/infrastructure-manager/docs/reference/rest/v1/projects.locations.deploymentGroups.revisions/get)
and [LIST contract](https://docs.cloud.google.com/infrastructure-manager/docs/reference/rest/v1/projects.locations.deploymentGroups.revisions/list)).
Location/operation reads and existing deployment/physical-resource permissions
are also needed. Referenced deployments still execute Terraform with their own
service accounts and source configurations.

The existing VM/MIG retention/protection changes, GKE finalizer cleanup and TPU
disk-detachment prerequisites are not yet composed with Terraform destruction.
The child preflight guards continue to block those cases. Terraform protection,
deletion policies or invalid source configuration can also prevent destruction.
No API supplies an atomic condition across the reviewed group, deployments and
physical resources. Deprovision has no native idempotency token; after an ambiguous
transport failure, recovery depends on observing the group's provisioning state
and the final resources. Concurrent changes remain an API-level limitation.
