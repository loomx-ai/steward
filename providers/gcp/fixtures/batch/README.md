# Batch protocol fixtures

`resources.json` is synthetic data using the official Batch v1 and Compute v1
resource schemas. It includes same-name jobs with different UIDs in two regions,
native task-group names, pending/running task records, a VM in a different region
from its job, automatically deleted disks, an orphan disk, and a shared existing
regional disk. Secrets, commands, log text, identities and timestamps are fake.

The selected Batch Discovery source is
`https://batch.googleapis.com/$discovery/rest?version=v1`, revision `20260820`.
The retained complete-source SHA-256 is
`68f5fa94ab8eb7708947bf8a6d5c97290d119c2378ee94e43f83393e2ae2ab2a`.
The seven unmodified methods and transitive schemas are stored in
`../../catalog/source/discovery.json`; method selection is separate from the
literal paths and response behavior in `batch_test.go`.

Native contracts used:

- [Job deletion](https://docs.cloud.google.com/batch/docs/delete-job): deleting
  running or queued jobs also cancels them and removes job/task history.
  Pub/Sub, BigQuery outputs and Cloud Logging records remain separate.
- [Batch labels](https://docs.cloud.google.com/batch/docs/organize-resources-using-labels):
  job ID, job UID and `batch-node` correlate created compute resources.
- [Storage volumes](https://docs.cloud.google.com/batch/docs/create-run-job-storage):
  existing persistent disks, newly created disks, instance templates, GCS and NFS
  are different native inputs. Legacy cross-region allocation remains possible
  for eligible projects until the documented 2027 transition.
- [Compute disk aggregate listing](https://docs.cloud.google.com/compute/docs/reference/rest/v1/disks/aggregatedList):
  include all visible scopes and reject partial results; native disk labels are
  mutable and are not an authorization to issue independent Compute deletes.
- [Batch IAM permissions](https://docs.cloud.google.com/iam/docs/roles-permissions/batch).

Run `go test ./providers/gcp -run '^TestBatch' -count=1`. Tests use real provider
credentials resolution, OAuth headers, catalog binding, inventory normalization,
native lifecycle contributions and the shared plan solver with a controlled
HTTP transport. They cover empty/intermediate pages, parent/cursor binding,
redaction, missing/ambiguous members, changed configuration, native protections,
retention, incomplete/error replies, rate limits, operation binding, JSON
persistence and restarted waits. Job 404 and operation completion are checked
while tasks/disks remain; final absence requires every reviewed deletion and a
fresh native UID query, while the external disk and other job remain intact.
Separate orphan-VM tests require native absence of its original Job UID in all
Batch locations before permitting the explicitly selected VM's Compute DELETE.
They reject permission failures, incomplete locations and an existing owner.
An instance-template scenario reads the template's native disk sources and
verifies that both the template and its existing regional disk survive cleanup.

The SQLite inventory-worker test verifies that failed parent-job discovery
does not close existing task observations or discard their parent proofs.
These are protocol and application-component tests. They do not emulate the
Batch scheduler, run an independent cloud emulator or prove a real-cloud run.
