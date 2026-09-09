# Cloud Data Fusion protocol fixtures

`resources.json` contains synthetic native Instance, DnsPeering and Namespace
responses. It has two same-name instances in different regions, shared-VPC and
Private Service Connect dependencies, and both bare and full namespace names.
The `FUSION_PRIVATE_` values exercise redaction and are not real tenant data.

The retained [Discovery selection](../../catalog/source/selection.json) and
[source fragments](../../catalog/source/discovery.json) include eight native
methods from the following complete upstream documents (revision `20260811`):

| Source | Complete response SHA-256 |
| --- | --- |
| [Data Fusion v1](https://datafusion.googleapis.com/$discovery/rest?version=v1) | `e620dbfa571cf76bc6eb49724b01ca435430f692d3bca776ad80d70d44c6844b` |
| [Data Fusion v1beta1](https://datafusion.googleapis.com/$discovery/rest?version=v1beta1) | `2d9773452a2a6f21b75b585d54cd55115b62a4c8ce87ada11156587b88549686` |

## Native contracts

- Instances use v1 location/list/get/delete APIs and regional operation polling.
  `ACTIVE` is the v1 ready state; the beta name `RUNNING` is not substituted into
  a v1 response. Creation time and configuration bind the reviewed resource.
- DNS peerings have native list and synchronous delete methods, but no GET.
  Deletion returns an empty JSON object. Inventory and deletion readback resolve
  the exact child in complete lists; no single-resource GET is invented.
- [Namespace list](https://docs.cloud.google.com/data-fusion/docs/reference/rest/v1beta1/projects.locations.instances.namespaces/list)
  is available in v1beta1. Every list uses `NAMESPACE_VIEW_FULL`. An absent policy,
  malformed status, or embedded nonzero IAM status fails the read even when the
  HTTP response is successful. Raw configuration is hashed before options,
  descriptions and IAM policy contents are redacted.
- [Instance deletion](https://docs.cloud.google.com/data-fusion/docs/reference/rest/v1/projects.locations.instances/delete)
  exposes a `force` switch for nested resources. The lifecycle contributor
  reconciles two complete child sets and the parent configuration. The solver
  includes those children in the reviewed impact plan; execution rejects new,
  changed, retained, out-of-scope or unreadable members before using `force=true`.
  Independently selected DNS peerings can use their own native delete.
- Namespace resource records have no management-plane DELETE. Google's
  [CDAP namespace deletion instructions](https://docs.cloud.google.com/data-fusion/docs/reference/cdap-reference#delete_a_namespace)
  require enabling unrecoverable reset and restarting the instance. This driver
  does not enable that option. Namespace records are removed with their instance.
- Google's [instance deletion guide](https://docs.cloud.google.com/data-fusion/docs/how-to/delete-instance)
  states that user data accessed or created by the instance is retained. Buckets,
  network/PSC attachments, service accounts, CMEK keys and Pub/Sub topics are
  recorded as dependencies, including explicit cross-project references. They
  are not deleted by this driver.
- [Data Fusion audit logging](https://docs.cloud.google.com/data-fusion/docs/how-to/audit-logging)
  specifies `datafusion.instances.get` for DNS lists and
  `datafusion.instances.update` for DNS deletion. Instance deletion needs
  `datafusion.instances.delete`; namespace lists need `datafusion.namespaces.list`.
  Full policy reads also require namespace IAM read access. See the
  [permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/datafusion).

## Verification and limits

Run `go test ./providers/gcp -run '^TestDataFusion' -count=1`.
The literal HTTP scenario exercises native region discovery, empty pages with
continuation tokens, aliases, list-based child reads, the actual contributor and
solver, independent DNS cleanup, reviewed instance cascades, serialized restart,
pending/expired operations and final readback. Fault injection covers partial or
denied lists, embedded policy failures, duplicate/foreign names, parent and child
changes between reads, forged reviewed impacts and modified operation context.
A real SQLite scan worker preserves namespace observations after an embedded
policy permission failure. Inventory, Invoke and API logs are checked for redaction.

These are protocol and application-component tests. The official
[emulator catalog](https://docs.cloud.google.com/sdk/gcloud/reference/beta/emulators)
and [floci-gcp service list](https://github.com/floci-io/floci-gcp/blob/main/README.md)
were checked on 2026-09-09; neither lists Data Fusion. The upstream
[CDAP Sandbox](https://cdap.atlassian.net/wiki/spaces/DOCS/pages/480346167/CDAP+Sandbox)
supports local pipeline development and does not verify Google's managed instance,
DNS peering, IAM wrapper or long-running deletion APIs. No independent Data Fusion
emulator or real-cloud acceptance run is claimed.

This coverage is the management API. Individual CDAP pipelines, secure-store
entries, datasets and runtime compute profiles are not inventoried or independently
deleted here. Their data-plane APIs and Dataproc runtime lifecycle remain separate
work; deleting an instance is not evidence that every pipeline-created Dataproc
cluster or external output has disappeared. Google's
[batch-pipeline troubleshooting guide](https://docs.cloud.google.com/data-fusion/docs/troubleshoot-batch-pipelines)
describes ephemeral-cluster deletion failures and subsequent cleanup.

Neither instance nor DNS deletion offers an atomic configuration/incarnation
condition. Repeated complete reads reduce the race window but cannot eliminate
concurrent changes. DNS and namespace records have no creation token; an identical
same-name replacement within the same instance cannot be distinguished. A child
collection 404 is accepted only after the parent is absent and another parent GET
confirms absence. A permission failure is never substituted for an empty list.
