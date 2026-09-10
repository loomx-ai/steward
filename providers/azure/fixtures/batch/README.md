# Azure Batch native API evidence

The 54 individual example JSON files are unchanged from `Azure/azure-rest-api-specs` at
commit `e45039baa985c442877529906e705982a6e0099d`. `sources.json` records each
original path, operation and SHA-256. The selected native documents are:

- ARM: `specification/batch/resource-manager/Microsoft.Batch/Batch/stable/2025-06-01/openapi.json`,
  SHA-256 `11f295a8e406d24ab90e8b623ed476b8000e7e37ac8e72e0ec8aab7d03c8832a`.
- Data plane: `specification/batch/data-plane/Batch/stable/2025-06-01/BatchService.json`,
  SHA-256 `8427acd8fd774462806e3a48ae1c4e85ff186b62ff02b9e831f698838a852717`.

The catalog keeps the data plane's native `{endpoint}` host parameter and uses
the separate `https://batch.core.windows.net//.default` OAuth scope, including
its documented double slash. An endpoint must resolve through a current ARM
account in the selected subscription before the runtime sends Batch requests.
Jobs, schedules, tasks and nodes retain their real data-plane URL identities;
the runtime does not manufacture ARM IDs for them.

## Native inconsistencies

The source test checks all 54 hashes and the 42 examples with response schemas.
It preserves these differences between the originals and their shared schemas:

- Seven job, schedule and task examples use the fractional duration
  `P10675199DT2H48M5.4775807S`. The JSON Schema library's duration validator
  rejects its fractional seconds. The test first asserts that failure, then
  checks a copy with whole seconds so the remaining fields are still validated.
- `PoolGet` and `PoolList` omit user passwords, while the shared `UserAccount`
  definition requires `password` and marks it `x-ms-secret`. The test asserts
  the original failure, then checks a copy with a placeholder password.
- `Tasks_GetTask` has `id: testTask` but its request and URL name `taskId`.
  The runtime rejects that identity mismatch. Scenarios compose corrected copies.
- `PoolGet` contains an application ID below the pool, although applications
  belong directly to the account. Runtime package references require the real
  account/application ID.
- `JobSchedules_GetJobSchedule.executionInfo.recentJob.url` names the
  `jobschedules` collection. Ownership uses `Jobs_ListJobsFromSchedule`, never
  this optional URL or a job-name prefix.
- `Tasks_ListSubTasks.nodeInfo` uses `poolId: mpiPool` but names `poolId` in its
  node and directory URLs. The runtime rejects that disagreement. MPI scenarios
  correct both fields in copies of the original subtask records.
- The file HEAD example still names its host parameter `batchUrl`; the 2025
  Swagger declares `endpoint`. Binding tests adapt only that parameter in a copy,
  and exercise its original Windows path through the native file HEAD route.

## Protocol tests

The Go scenarios compose copies into a consistent account. They cover ARM and
data-plane inventory, native continuation links, stable client cursors, private
configuration drift, redaction, native schedule membership, auto-pool ownership,
explicit choices for shared consumers, reviewed cascades, conditional task/job
deletion, pool-conditioned node removal, schedule disable before deletion,
and resource-bound ARM polling with signed Location rotation and restart.

Task placement uses the documented node URL and pool/node IDs, rejects conflicts
or another account, and remains discoverable after node disappearance. Placement
does not require deleting a task before native node removal/requeue. Task range
dependencies include leading-zero aliases (`4`, `04`, `004`) and actual int32
boundary members; huge ranges are never expanded. Explicit dependent selection,
deletion ordering, numeric type errors and malformed identities are tested.

External resource references use native Storage/Key Vault subscription lists and
matching ARM detail reads. Tests cover other resource groups, DNS-zone/secondary/
custom Blob domains, root containers, auto storage, Blob mounts, Azure Files,
output destinations, disk-encryption keys and managed identities. They exercise
the application graph with real ARM identities. Missing external names or typed
URLs remain unresolved; SAS values are removed and same-group identities are not
invented. Permission failures, partial/foreign continuation pages, stale details,
duplicate targets, conflicting names/services and encoded container separators
fail. Arbitrary resource-file contents and vault/key contents are never fetched.
Opaque extension settings do not supply reference authority. Shared sanitization
also removes the Azure Files `accountKey` field, which this coverage exposed.

Pool discovery reconciles complete ARM and Batch data-plane indexes, including
auto pools. Node removal binds its original allocation and other pool settings,
then reads the latest pool ETag and the node immediately before its one-node
`RemoveNodes` request. Desired node counts are excluded only from a node's parent
binding because node removal itself changes those counts; pool deletion continues
to bind the full pool configuration. If an independently selected node precedes
its pool, the runtime accepts only that exact dedicated/low-priority capacity
decrement after proving the node and every physical resource absent. Other pool
settings, unexpected scaling and altered node-capacity classifications fail.

User-subscription nodes use the documented `scaleSetVmResourceId` to review the
current Uniform VMSS instance and its native Compute/Network children. The shared
Compute walk verifies disk ownership/deletion policy, VM/NIC backlinks and complete
extension, NIC, IP-configuration and instance public-IP collections. Each node
binds the complete resource tree with keyed configuration digests. VM recreation,
private extension changes, shared/detached disks, mismatched regions/subscriptions,
duplicate node-to-VM ownership, locks and protected resources fail before mutation.
An allocated node without a readable native VM identity cannot establish this proof.

Node, pool and account cleanup waits independently for the reviewed VM, disks,
extensions and network resources; a node or VM 404 cannot conceal a remaining
disk. The containing scale set has a separate lifetime and must explicitly select
the Batch node as a prerequisite. Its capacity may fall only by the verified node
count, with every other scale-set setting still checked. The planner also keeps
the frozen prerequisite when a node is selected explicitly and its pool is added
by account cleanup. These scenarios compose Batch examples with the existing
Uniform Compute/Network fixture; they are not UserSubscription deployment recordings.

Multi-instance cleanup uses the native task-termination API before deletion.
It waits for all subtasks to complete, checks both complete subtask reads, and
preserves every observed primary/subtask directory in an authenticated receipt.
Resumed waits verify the node's incarnation and HEAD each directory after task
absence. A primary-task 404, altered receipt, recreated node, partial response or
permission error cannot complete cleanup. The receipt contains directory identity
and keyed node fingerprints, never command lines or environment values.

Account cleanup independently removes its pools, applications, private endpoint
connections, jobs and schedules before deleting the account. Applications first
remove their package versions. The read-only network security perimeter
configuration is a controller impact, not an invented DELETE operation.

These are in-process HTTP protocol and application graph tests, not an independent
Batch emulator or a live Azure run. Broader GCP/Azure application acceptance remains
open; these fixtures do not establish live deployment, query/task execution,
provisioning, restoration or arbitrary external-service cleanup behavior.

## Official CLI recordings

`cli-recordings.json` retains 75 selected responses from eight official
[Azure CLI recordings](https://github.com/Azure/azure-cli/tree/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/src/azure-cli/azure/cli/command_modules/batch/tests/latest/recordings).
Each source has its original URI, interaction index and SHA-256. The reproducible
extraction has SHA-256
`e96fff896dabc1bb5cc05e86bf56c258293808d88fcc46af727cd210c9fc6759`.

```sh
python3 providers/azure/fixtures/batch/reproduce_recordings.py /path/to/upstream-recordings
```

The recordings use ARM `2024-02-01` and data-plane `2024-02-01.19.0`.
Replay tests explicitly bridge API-version query values in copied request and
polling URLs to `2025-06-01`. They preserve the original response bodies and
signed query fields; this tests protocol structure, not the live validity of an
older signature under a different version. Original extraction bytes are unchanged.
Supporting empty indexes and final native 404 responses are composed and labeled.
Filtered/projected pool recordings retain their original query; they are not
treated as complete inventory responses.

The executable replays cover account DELETE → rotating Location → empty 200 →
independent account absence; native task reads, termination and deletion; the
recorded `RemoveNodes` 202 response and one-node request shape; an actual
schedule-generated job's auto-pool lifetime specification; and application/package
deletion through independent final absence after JSON restart. Default-version
clearing between the recorded package/application deletions is composed from the
documented behavior. Private endpoint inventory replays its complete list and
detail responses and verifies the native network reference. That CLI scenario has
no private-endpoint DELETE, so no recorded deletion is claimed. Further pool
responses remain provenance, not completed end-to-end lifecycle replays.

## Emulator boundary

The evaluated [Floci-Az source](https://github.com/floci-io/floci-az/tree/f6f0292880c6eb4e7fe3d658185030665f98166e)
does not declare an Azure Batch service. Its source paths containing `Batch`
implement Cosmos transactions, Service Bus AMQP or storage batching.
[Azurite](https://github.com/Azure/Azurite) implements Azure Storage APIs.
Neither was used as evidence for Batch account/job/pool cleanup; the retained
Batch evidence is native-schema validation and HTTP response replay.

## Documented service behavior

- [Task dependencies](https://learn.microsoft.com/en-us/azure/batch/batch-task-dependencies)
  defines integer ranges and their leading-zero aliases.
- [Storage account lists](https://learn.microsoft.com/en-us/rest/api/storagerp/storage-accounts/list?view=rest-storagerp-2023-05-01)
  and [vault lists](https://learn.microsoft.com/en-us/rest/api/keyvault/keyvault/vaults/list-by-subscription?view=rest-keyvault-keyvault-2023-07-01)
  expose actual ARM identities and endpoint properties for external-reference resolution.
- [Root container addressing](https://learn.microsoft.com/en-us/rest/api/storageservices/working-with-the-root-container)
  permits a Blob URL to omit the literal `$root` container segment.

- [Delete job](https://learn.microsoft.com/en-us/rest/api/batchservice/jobs/delete-job?view=rest-batchservice-2025-06-01)
  and [delete job schedule](https://learn.microsoft.com/en-us/rest/api/batchservice/job-schedules/delete-job-schedule?view=rest-batchservice-2025-06-01)
  describe their task and working-directory cascades and ignored retention periods.
- [Delete task](https://learn.microsoft.com/en-us/rest/api/batchservice/tasks/delete-task?view=rest-batchservice-2025-06-01)
  distinguishes synchronous primary-task deletion from asynchronous subtask cleanup.
- [Remove nodes](https://learn.microsoft.com/en-us/rest/api/batchservice/pools/remove-nodes?view=rest-batchservice-2025-06-01)
  requires a steady pool and supports its ETag condition. The runtime selects the
  exact node and the native `requeue` deallocation option.
- [Batch account allocation modes](https://learn.microsoft.com/en-us/azure/batch/accounts)
  places user-subscription compute resources in the Batch account's subscription.
  The pinned native `VirtualMachineInfo.scaleSetVmResourceId` definition identifies
  the current VMSS VM only for that allocation mode. Shared network resources can
  belong to the VNet subscription and are not inferred as node-owned resources.
- [Application packages](https://learn.microsoft.com/en-us/azure/batch/batch-application-packages)
  describes `allowUpdates`, default-version clearing, linked storage and package
  installation behavior. Upload-only SAS fields do not define package identity.
