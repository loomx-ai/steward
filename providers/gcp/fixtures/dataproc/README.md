# Dataproc protocol fixtures

`resources.json` is synthetic, using the native Dataproc v1 and Compute v1
schemas. It includes two same-name clusters in different regions, a primary VM,
a managed secondary-worker group and template, an auxiliary node group identified
by its actual native ID, boot disks, a retained existing regional disk, active
job history and a job from an older cluster UUID, plus a policy and template.
Names, UUIDs, key material, scripts, properties and errors are fake.

Source: `https://dataproc.googleapis.com/$discovery/rest?version=v1`, revision
`20260818`, complete-source SHA-256
`0597a047b7b677c1f6ea5a8352aee06be438054c6af66bd7f24d3a0449a52cfe`.
Fifteen selected unmodified methods and their transitive schemas are retained
in `../../catalog/source/discovery.json`. The literal HTTP fixture does not
use the generated catalog to decide paths, payload shapes or response fields.

Official contracts checked:

- [Cluster deletion](https://docs.cloud.google.com/managed-spark/docs/reference/rest/v1/projects.regions.clusters/delete)
  supports a native `clusterUuid` condition and idempotent request ID, returning
  a regional long-running operation.
- [Jobs delete](https://docs.cloud.google.com/sdk/gcloud/reference/dataproc/jobs/delete)
  deletes inactive job records. Active jobs use native `jobs.cancel`, followed
  by polling until `DONE`, `ERROR` or `CANCELLED`, before record deletion.
- [Native cluster and job schemas](https://docs.cloud.google.com/managed-spark/docs/reference/rpc/google.cloud.dataproc.v1)
  expose immutable cluster/job UUIDs, native VM instance names/references,
  managed-group references and auxiliary node-group IDs. Node groups have GET
  but no separate LIST/DELETE. Policies/templates also have location aliases;
  these aliases must not duplicate the regional resource identity.
- [Provider labels](https://docs.cloud.google.com/dataproc/docs/guides/creating-managing-labels)
  and [VM metadata](https://docs.cloud.google.com/managed-spark/docs/concepts/configuring-clusters/metadata)
  correlate native Compute resources. Labels are mutable; correlation permits
  reviewing effects and preventing independent Compute writes, not authorizing
  independent deletes.
- [Dataproc on GKE](https://docs.cloud.google.com/managed-spark/docs/guides/dpgke/quickstarts/gke-quickstart-create-cluster)
  retains the underlying GKE cluster and node pools when the virtual Dataproc
  cluster is deleted.
- [Workflow behavior](https://docs.cloud.google.com/managed-spark/docs/concepts/workflows/use-workflows)
  keeps already-running workflows independent of deleting their template.
- [Permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/dataproc).

Run `go test ./providers/gcp -run '^TestDataproc' -count=1`.
Tests exercise OAuth credential resolution, regional discovery, paging, native
GET validation, redaction, complete Compute UID queries, lifecycle contribution,
the shared planner, native deletion and persisted/restarted waits. They cover
retained and selected history, auxiliary/MIG resources, external disks, virtual
GKE retention, orphan VM/disk ordering, identity/configuration changes,
protection, omitted or forged impacts, failed/partial reads, throttling and
operation scope/metadata binding. Parent 404 never hides remaining Compute
resources, and retained active job records must settle before completion.

The real SQLite inventory worker verifies that losing cluster permissions cannot
close observed node groups or discard their parent proofs. The shared SQLite
cleanup-worker tests verify default retention, explicit selection/retention,
prerequisite ordering and frozen request restoration across worker restart.

These are protocol and application-component tests. Google's
[emulator command catalog](https://docs.cloud.google.com/sdk/gcloud/reference/beta/emulators)
and the [floci-gcp supported-service list](https://github.com/floci-io/floci-gcp/blob/main/README.md)
were checked on 2026-09-09; neither lists a Dataproc control-plane emulator.
This fixture does not run Spark, independently emulate Dataproc's scheduler or
prove real-cloud cleanup. Dataproc does not atomically lock cluster, job and
Compute membership together. Removed correlation labels/metadata cannot always
be reconstructed from native parent configuration. Jobs/policies lack native
configuration preconditions; workflow deletion uses its version condition, but
policy/template GETs expose no immutable incarnation UUID. Keep these provider
limits separate from the protocol coverage.
