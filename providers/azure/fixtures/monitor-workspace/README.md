# Azure Monitor workspace evidence

The three unchanged examples come from the pinned
[2023-04-03 Microsoft Monitor workspace Swagger](https://github.com/Azure/azure-rest-api-specs/tree/e45039baa985c442877529906e705982a6e0099d/specification/monitoringservice/resource-manager/Microsoft.Monitor/Accounts/stable/2023-04-03).
The original URLs and checksums are in sources.json. The full source SHA-256 is
4aa6210b8356fdd786edcea65d4e5269cc3a7b7baf01669159927d3f4c15fb47.
The catalog keeps native GET, subscription LIST and DELETE with their transitive
schemas; ordinary builds and tests do not access the network.

The [official management guide](https://learn.microsoft.com/en-us/azure/azure-monitor/metrics/azure-monitor-workspace-manage)
documents automatic deletion of the default ingestion resource group and all
its resources. Workspace data has no soft-delete recovery. Steward derives that
group from both read-only defaultIngestionSettings resource IDs, requires them
to agree within the connection, and rejects any conflicting managedBy value.
The optional managedBy field can be absent. A captured owner must still match
at execution. No group-name prefix is used as ownership evidence.

Two full group scans and native detail reads bind the reviewed membership.
Group resources, including unknown contained kinds, are delegated impacts of
the workspace deletion. External DCR/DCE associations are shared, separate
required unlink steps. Their monitored machines and destinations acquire no
ownership binding. Retention, missing inventory, new members, changed native
configuration/creation identity, ownership changes, locks and protection block
cleanup. Native root, group, known child and external-association reads determine
completion even after a worker restart. Unknown contained kinds rely on native
group absence. No direct group/default-resource DELETE or forced cascade is sent.

Private endpoint connections remain part of the frozen workspace configuration;
their external network endpoints are references. No standalone connection CRUD
is invented from embedded records. The supported workspace management API is
2023-04-03. Newer issue and metrics-container APIs are not individually registered.
In the inspected Microsoft CLI [2025-10-03 issue recording](https://github.com/Azure/azure-cli/blob/6e3b4eab87bc1ec9e58d76361507e9e0be27dd4f/src/azure-cli/azure/cli/command_modules/monitor/tests/latest/recordings/test_monitor_account_issue.yaml),
the westus request returns NoRegisteredProviderFound; that observation is not a
claim about availability in every region.

## Native response verification

Independent JSON Schema checks validate the original workspace GET. The original
LIST has null nextLink despite a string-only optional schema. The test detects
that mismatch, then validates the rest without that field; runtime pagination
accepts null as end-of-list. The original DELETE example also contains polling
URLs for a different subscription than its parameters. Tests reject those URLs;
positive polling fixtures rebind the subscription/resource group and operation
UUID, and cover both
native headers, delayed progress, failure, expiry and actual resource readback.
Polling response bodies and final absence are synthetic protocol fixtures.

cli-read-recordings.json extracts GET interaction 4 and LIST interaction 5 from
[Microsoft CLI commit 27554ab](https://github.com/Azure/azure-cli/blob/27554ab8a5375aab7529e6862934bf26afbb8762/src/azure-cli/azure/cli/command_modules/monitor/tests/latest/recordings/test_monitor_account.yaml).
Its full YAML SHA-256 is
dcb807788e905124f7e77fa36be0d8fced7a5235a8c4213e9808f7e8e457454c.
Audit users are replaced with recording-user; the native 2023-04-03 bodies,
IDs, default ingestion settings, ETags and creation timestamps are preserved.
The replay rebinds only the subscription. Permission reads, absent defaults
and DELETE/readback are explicitly synthetic. This recording contains no
workspace DELETE and is not presented as native deletion evidence.

To reproduce the sanitized extract, download the immutable YAML above and run
`python3 reproduce_recordings.py /path/to/test_monitor_account.yaml` with PyYAML.

The tests exercise the actual planner, shared unlinking, whole-group impacts,
optional owners, missing groups, malformed and changing lists, configuration and
creation drift, retention, protected unknown resources, scoped polling and
serialized recovery. An ECMA-262 leading-hyphen name guard is evaluated explicitly
before the remaining pattern is compiled with Go's regular expression engine.

These are native-example, recorded-response and protocol tests, not independent
emulator or Steward real-cloud acceptance. Native DELETE exposes no atomic
If-Match condition. Identical recreation remains indistinguishable when the
provider omits creation identity; externally changing resources can race the
last preflight read. Broader provider parity and cloud acceptance remain open.
