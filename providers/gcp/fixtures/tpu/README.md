# Cloud TPU protocol fixtures

`resources.json` contains seven synthetic resources: a two-node queued request,
its native reservation, three TPU nodes in two zones, and two existing Compute
Engine data disks. Resource names, unique IDs, timestamps, metadata and network
configuration are fake. HTTP fixtures use literal native endpoints and request
bodies independently of the generated catalog.

The complete official Discovery responses were retrieved on 2026-09-09:

| Source | Revision | Complete-source SHA-256 |
| --- | --- | --- |
| [v2](https://tpu.googleapis.com/$discovery/rest?version=v2) | `20260127` | `a326889542536af0661d7e5413181fb6958f506800846b046c81499c77562c96` |
| [v2alpha1](https://tpu.googleapis.com/$discovery/rest?version=v2alpha1) | `20260127` | `620a261f64c54e3e3b9557b3cf057f736777c08bb2ec9e9b8680bca300667386` |

The source catalog retains ten unmodified methods and their referenced schemas:
v2 locations.list, Node list/get/patch/delete, QueuedResource list/get/delete and
operations.get; v2alpha1 reservations.list. Reservations have no selected native
GET or DELETE: detail verification searches the complete native list. TPU
locations are zones; project-wide scans enumerate them with v2 locations.list,
and regional scans select the corresponding zones.

Official contracts checked:

- [QueuedResource deletion](https://docs.cloud.google.com/tpu/docs/reference/rest/v2/projects.locations.queuedResources/delete)
  documents a force cascade. Steward instead creates reviewed Node prerequisites
  and issues plain QueuedResource DELETE after their absence is verified.
  [Queued-resource management](https://docs.cloud.google.com/tpu/docs/queued-resources)
  describes individual Node deletion and the subsequent suspended request state.
  Single-node requests can have service-assigned IDs; native Node.queuedResource
  backlinks identify them. MultisliceParams defines the prefix-index naming rule.
- [Durable storage](https://docs.cloud.google.com/tpu/docs/attach-durable-block-storage)
  describes attach/detach operations. Storage guides have differing deletion
  retention wording, so existing data disks are explicitly detached and read
  back before Node deletion. The boot disk is part of the deleted TPU VM.
- [Node patch](https://docs.cloud.google.com/tpu/docs/reference/rest/v2/projects.locations.nodes/patch)
  exposes a field-mask update. Its descriptive supported-field list omits
  dataDisks, but the pinned official Cloud SDK's GA v2 update command uses
  `updateMask=data_disks` for detachment. [sdk-detach-evidence.json](sdk-detach-evidence.json)
  retains exact source excerpts, archive/file hashes and line numbers. The
  complete SDK archive hash is
  `cecc5d244c10e3bc8ef7449937569c40339b90686b902fd48b4afc6f47ebfeb3`.
  The archive and source members were verified without executing SDK code.
- [Node deletion](https://docs.cloud.google.com/tpu/docs/reference/rest/v2/projects.locations.nodes/delete),
  [reservation listing](https://docs.cloud.google.com/tpu/docs/reference/rest/v2alpha1/projects.locations.reservations/list)
  and the [TPU permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/tpu).
  Queued-resource methods reuse Node permissions; reservation listing requires
  `tpu.nodes.get`, not an invented `tpu.reservations.list` IAM permission.

Run `go test ./providers/gcp -run '^TestTPU' -count=1`.
Tests exercise OAuth/HTTP, native inventory and aliases, complete paging, zonal
scope selection, the real lifecycle contributor and shared solver, reviewed
Node prerequisites, shared-disk retention, detach/delete ordering, serialized
restart, pending/expired operations, actual attachment readback, same-name
replacement, changed configuration and malformed or incomplete responses.
A SQLite scan-worker test preserves Node observations and disk/queue proofs after
queue-read permission loss. Metadata and template content are checked for
redaction in inventory, Invoke and logs. Dependency 404s cannot be reported as
successful Node or queued-request cleanup.

These are protocol and application-component tests. The official
[emulator command catalog](https://docs.cloud.google.com/sdk/gcloud/reference/beta/emulators)
and [floci-gcp supported services](https://github.com/floci-io/floci-gcp/blob/main/README.md)
were checked on 2026-09-09; neither lists a TPU emulator. No independent TPU
emulator, TPU hardware or real-cloud acceptance run is claimed. Native Node
patch/delete and queued-request delete cannot atomically condition writes on
configuration or Node/queue incarnation; repeated proofs reduce the race window
but cannot eliminate concurrent changes. Disk retention verifies resource
identity and attachment removal, and does not create a backup. The v2 attachment
schema is enforced; alpha-only per-worker attachments are outside this workflow.
These rules cover the Cloud TPU API. Google's [Compute Engine TPU documentation](https://docs.cloud.google.com/tpu/docs/tpus-in-compute-engine)
explains that Compute/GKE TPU resources use separate management APIs; TPU7x and
later are outside the Cloud TPU API's supported hardware.
