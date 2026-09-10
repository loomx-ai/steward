# Azure Stream Analytics management API evidence

Seven rules cover jobs, inputs, outputs, functions, transformations, clusters and
cluster private endpoints using stable **2020-03-01**. Six have native DELETE;
transformations have no native list or DELETE operation. The catalog retains
23 native operations and 41 unchanged examples from
[azure-rest-api-specs commit e45039b](https://github.com/Azure/azure-rest-api-specs/tree/e45039baa985c442877529906e705982a6e0099d/specification/streamanalytics/resource-manager/Microsoft.StreamAnalytics/StreamAnalytics/stable/2020-03-01).
`sources.json` records each original example URL and SHA-256. The complete
upstream document hashes are:

| Document | SHA-256 |
| --- | --- |
| `clusters.json` | `852308bfe4e8c8f88eaf7b23fcb2ec0d9ecacbbe8bd851f4fce5d6a7032be48c` |
| `common/v1/definitions.json` | `45e42539d13ca5eb167613fa3203a5e143554680feae5af8c53b8c7d701ea253` |
| `functions.json` | `af51174d4c5d7ba922d6ce45cb4b3bfbce4b6de3f732bd1c6f94814225ebd086` |
| `inputs.json` | `b4fb358e5fc51aa5a4d9d0e4bf0b03c03b7c8670a18e77dd8367c2045db2b310` |
| `outputs.json` | `5b5119f8d62d3840286eab709beb9987fc047e97baa8edda8851705c8389b2aa` |
| `privateEndpoints.json` | `9c6d751553b01e7add13712254575fa6bc22ad4a2a340f563c22eb27be0bc0d7` |
| `streamingjobs.json` | `d46874af8d68bbf3ad3aff3e0328ceba491505b892edeba3cfce982606dd0d50` |
| `transformations.json` | `971ff6ad9d1b6b1fbbd330b6fd92ec6a2af5d950c6b25cb8ba628026f4c40f3c` |

Offline tests check 34 response bodies and 20 concrete discriminator variants.
Four list examples return `nextLink:null` where the schema declares a string;
the Azure Function output example likewise returns `apiKey:null`. Tests assert
these exact discrepancies before validating copies without only those optional
null fields. Original fixtures and schemas are unchanged. Functions nest their
binding under `properties.properties`, as the original API defines.

## Lifecycle and dependencies

A job's native DELETE removes its four child definition kinds. The transformation
name comes from job GET with `$expand=transformation`, followed by its native
GET. Steward neither guesses a name nor invents a collection API. A genuinely
absent transformation in a newly created job yields an empty collection. Selecting
an existing transformation requires its owning job. Job deletion can run for
Running or Degraded jobs; standalone input/output/function deletion requires a
Created, Stopped or Failed parent. Starting, stopping and unknown states block
cleanup. See [job states](https://learn.microsoft.com/en-us/azure/stream-analytics/job-states)
and [job cleanup](https://learn.microsoft.com/en-us/azure/stream-analytics/stream-analytics-clean-up-your-job).

Deleting a job is irreversible and does not delete its external input or output
data stores. Its native child collections, the expanded transformation and any
embedded indexes must agree. Two complete walks compare identities and private
configuration and reread the parent. Every reviewed child must independently
return 404 after a cascade; job absence alone cannot establish completion.
Retained, protected, changed or unreviewed children block the parent action.

A cluster owns its private endpoints, which receive reviewed DELETE steps before
the cluster. Associated jobs remain independent. The native `listStreamingJobs`
index starts with POST and follows next links using GET, without a request body.
This method sequence comes from the pinned
[official SDK](https://github.com/Azure/azure-cli-extensions/blob/0349eb646d3225db5fd677114e200efdfd11e3f8/src/stream-analytics/azext_stream_analytics/vendored_sdks/streamanalytics/operations/_clusters_operations.py)
(SHA-256 `5439342ab812380fe0c468ae6412d0efadddbe8b33b41d209287fc55d32e61a2`).
Each job GET must confirm cluster ID, region and listed job state. Repeated,
partial, inaccessible, filtered, foreign or malformed pages fail closed.

Azure's [cluster deletion guide](https://learn.microsoft.com/en-us/azure/stream-analytics/create-cluster#delete-your-cluster)
requires stopping running jobs. Steward additionally requires that no retained
association remain when it deletes a cluster: either explicitly select the jobs
for deletion too, or stop and remove retained jobs from the cluster in Azure,
then rescan. This is Steward's retention boundary, not an Azure requirement to
delete associated jobs. [Removing a stopped job](https://learn.microsoft.com/en-us/azure/stream-analytics/manage-jobs-cluster)
returns it to the standard multi-tenant environment. Steward does not automatically
stop, detach or delete an unselected job. The required-deletion graph marks such
jobs `automatic_selection:false`; persistence and execution tests preserve that
constraint and the frozen job prerequisites across restart.

Proxy children inherit their owning job/cluster region. Public inventory removes
query text, full/delta reference queries, JavaScript and connection credentials,
while private digests preserve their exact values, large JSON integers and
creation metadata. ETags, diagnostics and moving capacity counters are separated
from stable authored configuration. Parent and linked-target preflight checks
include final rereads, readiness, inherited locks and protection. A managed
resource group cannot absorb an associated job outside the group; its cascade
also checks Stream Analytics private-endpoint targets and final child absence.

Named Blob/Table, Event Hub, Service Bus, SQL, Cosmos and Azure Function data
sources resolve against native subscription collections, including other resource
groups. Matching parent names must be unique. Explicit foreign ARM IDs remain
references without an HTTP lookup; absent names do not become fabricated local
IDs. Cosmos case-sensitive database and collection names are not converted into
invented ARM child identities. Unknown or unmodeled connector endpoints remain
configuration. Private endpoints require a selected target API in the current
subscription, with unchanged private configuration and protection checks; target
data resources are retained.

## Original CLI responses and polling

`cli-recordings.json` contains 72 selected responses from eight official
[CLI recordings](https://github.com/Azure/azure-cli-extensions/tree/0349eb646d3225db5fd677114e200efdfd11e3f8/src/stream-analytics/azext_stream_analytics/tests/latest/recordings).
Each entry records the original source URL, full YAML hash and zero-based
interaction index. No request bodies, request headers, key-listing responses or
unrelated data-plane interactions are extracted. Its SHA-256 is
`77741db29207efc2c4d87185731d71c35779abbbcf6e7ee056cdaece94818377`.
Reproduce with the original pinned YAML files:

```sh
python3 providers/azure/fixtures/streamanalytics/reproduce_recordings.py /path/to/upstream-recordings
```

Five deletion chains replay all their original responses:

| Recording | Earlier parent | Resource GET | DELETE | Polls |
| --- | --- | --- | --- | --- |
| `test_job_crud.yaml` | 5 | 5 | 6: 200 empty | None |
| `test_input_crud.yaml` | 1: PUT response | 11 | 12: 200 empty | None |
| `test_output_crud.yaml` | 1: PUT response | 10 | 11: 200 empty | None |
| `test_private_endpoint_crud.yaml` | 62 | 67 | 68: 202 empty | 69–85 |
| `test_cluster_crud.yaml` | 337 | 337 | 338: 202 empty | 339–371 |

The input recording uses **2021-10-01-preview**, explicitly bridged to the
selected stable resource request version. Native response bodies and signed
operation URLs are preserved except for rebasing the all-zero subscription UUID
in memory. Earlier parent creation/GET states, group/lock support, empty child
and membership indexes, target Storage reads and final resource 404s are
explicitly synthetic. These are protocol replays, not complete CLI timelines.
The function recording's DELETE targets the **job**, not the function; it and
the transformation GET are retained for provenance, without claiming native
standalone function deletion. That DELETE has the original Swagger example and
composed runtime tests.

Cluster and private-endpoint polls refresh their signed `Location` on each 202.
Steward validates the complete resource, operation name, version and bounded
signature parameters before persisting each successor in `Wait.Data`. Serialized
receipts retain the original and rotated target-bound signatures through worker
restart. A changed operation, foreign resource, extra/missing query, duplicate
header, protocol change or forged receipt cannot initiate the successor request.

The private-endpoint sequence ends with HTTP 200 and
`{"status":"InProgress","error":null}`. Only this exact Location envelope may
proceed to an independent resource GET; it cannot prove completion itself.
The cluster sequence ends with an operation GET 404, which also requires resource
absence. Job stop's recorded final operation response (17) is HTTP 200 with a
literally empty body. The client permits this only on the exact signed native
operationResults URL; JSON null and empty ordinary resource GETs stay invalid.
Stop is a catalog operation, not an automatic cleanup preparation.

Provider API logs, execution acceptance logs, action audit events and public
operation details omit polling query credentials. Execution persistence retains
the full original receipts. Application and HTTP tests exercise this distinction
through handler restart and action-detail retrieval. Failed, canceled, partial,
unknown and inaccessible responses cannot complete deletion, and repeated
DELETE after confirmed absence is idempotent.

## Independent verification boundary

Microsoft's [ASA Tools local runner](https://learn.microsoft.com/en-us/azure/stream-analytics/visual-studio-code-local-run-all)
executes queries with local or live inputs/outputs. It does not provide an ARM
management endpoint for these deletion protocols. The reviewed Floci-AZ 0.12.0
[source at f6f0292](https://github.com/floci-io/floci-az/tree/f6f0292880c6eb4e7fe3d658185030665f98166e)
has no StreamAnalytics/streamingjobs management routes in its 331 Java files.
Neither was run as an ARM emulator. No live Azure deletion or independent
emulator acceptance is claimed. Query processing, data-plane cleanup, restore,
unmodeled connector targets and full GCP/Azure application parity remain open.
These native DELETE contracts have no conditional ETag parameter; rereads cannot
eliminate the final read/delete race.
