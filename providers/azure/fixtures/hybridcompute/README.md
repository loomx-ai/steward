# Azure Arc HybridCompute contract evidence

Native inventory now covers machines, extensions, Run Commands, license profiles
and shared licenses. It includes own reads, known omission recovery, native
parent pagination, repeated snapshot/cursor validation, signed references and
SQLite scan/graph reconciliation. Cleanup remains unfinished and unavailable;
the ENS mapping is still pending. Cloud absence is not physical-server release.

## Pinned REST examples

The 19 unchanged JSON examples in `sources.json` come from Microsoft's
[`HybridCompute.json`, stable 2025-01-13](https://github.com/Azure/azure-rest-api-specs/blob/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/hybridcompute/resource-manager/Microsoft.HybridCompute/HybridCompute/stable/2025-01-13/HybridCompute.json).
The full Swagger SHA-256 is
`eecab3d393ce57ef584391b028c109e7066829d4e2bc3685e8e2918ade7cde66`.
Each example records its upstream URL, checksum, operation, method and path.

The catalog retains 18 operations: machine GET/DELETE and subscription /
resource-group Lists; extension, Run Command and license-profile GET/List/DELETE;
license GET/subscription List; network-profile GET; and hybrid identity metadata
GET/List. Sixteen operations extend the two machine reads used by Defender.
No shared-license deletion, script execution or agent configuration write was
selected. Five regional inventory mappings now reference these read operations; no
DELETE is bound to a resource action until native Arc cleanup is implemented.

`TestHybridComputeOfficialContracts` binds every request, checks API-version
rejection and provenance, and checks all 24 declared responses, including 15
bodies. DELETE metadata retains long-running semantics; Run Command also retains
`final-state-via: location`. The four DELETE examples contain callback
placeholders, not usable polling URLs. Tests preserve these inconsistencies:

- `LicenseProfile_List.json` supplies undeclared `licenseProfileName`, and
  `License_ListBySubscription.json` supplies undeclared `licenseName`. Binding
  must reject the original request; only that extra parameter is removed in
  memory before the remaining request is checked.
- Extension GET/List report level `Information`; the schema enum allows `Info`,
  `Warning`, `Error`. Validation must first fail; only the known level is adapted
  in memory before checking the rest of the body.
- Eight bodies contain 138 non-nullable-schema/null-value mismatches.
  `nonnullable-nulls.json` enumerates every exact JSON pointer. Tests require
  precisely those null type errors before removing those fields in memory.
  These include machine `kind`, connection status, agent/OS metadata, nullable
  list continuations, tags and an expanded license's tags. No saved example or
  schema is repaired, and unknown nulls are not removed recursively.

## Original CLI deletion recordings

`cli-recordings.json` extracts 25 interactions from three public
[Microsoft CLI recordings](https://github.com/Azure/azure-cli-extensions/tree/2aa1d8fc6417d0d5055acd88e9491e29734ad62b/src/connectedmachine/azext_connectedmachine/tests/latest/recordings).
Each source includes its full YAML SHA-256; each interaction retains its
zero-based index, request method/URI, status code, all response headers and
decoded JSON response body. An empty wire response becomes `null`. Request
bodies and request headers are not copied.

| Recording | Indices | Observations |
| --- | --- | --- |
| `test_machine_and_extension.yaml` | 39–50 | Extension 429, accepted retry, extension and machine polling |
| `test_esu_license.yaml` | 11–13, 23–25 | Two machine license-profile deletes and polling |
| `test_run_command.yaml` | 17–23 | Run Command delete and polling |

These recordings use **2026-07-15**, not the catalog's 2025-01-13. The original
transport test keeps that version and replaces only the zero subscription
placeholder with the local test subscription. It exercises shared HTTP
transport, throttling classification and signature redaction; it does not
establish cross-version lifecycle parity.

Native status URLs use subscription-scoped
`Microsoft.HybridCompute/locations/{region}/operationstatus/{uuid}`; result URLs
use `operationresults/{uuid}`. Both carry `api-version`, `t`, `c`, `s`, `h`.
Status bodies bind `name` to the operation UUID and pass through `Queued`,
`InProgress`, `Succeeded`. Intermediate signatures change while this CLI keeps
polling its initial status URL. Tests preserve the signed query bytes.

All five result reads are empty HTTP 200. Ordinary GET rejects these; the
transport accepts them only with explicit empty-result opt-in and the test's
exact recorded URL validator. The recordings contain no own-resource 404 after
deletion, so neither absence nor agent uninstallation follows from replay.

## Native deletion receipts and polling

The runtime's four catalog DELETE operations now validate their native response
statuses and callbacks. Run Command's accepted response requires a Location
callback. The polling helper authenticates saved receipts against the exact
resource and credential configuration, validates subscription/provider/region/
operation UUID/API version/URL role, preserves signed query bytes, and permits
signature rotation only within the same operation and previously recorded role.
Each returned progress receipt can be persisted and resumed by a fresh runtime.
Status success advances to a saved result URL when present. Empty result 200 or
204 completes the operation; errors, malformed payloads, redirects and operation
404 do not establish success. Final own-resource readback still belongs to the
unfinished cleanup driver; these helpers do not enable resource cleanup actions.

`TestHybridComputeRecordedPollingResume` explicitly adapts the recordings' API
version to 2025-01-13 in memory, retaining their status bodies and following the
new signatures instead of the CLI's initial URL. It replays five accepted
deletions and 19 polls, rebuilding the runtime and serializing/restoring the
receipt between every step. This adaptation tests the protocol implementation;
it is not independent same-version or live-cloud evidence. Separate boundary
tests cover URL/receipt tampering, credential/resource changes, callback
disagreement, synchronous responses, failed polls, final-result handling and
all four runtime DELETE operations.

## Lifecycle boundaries

- [Agent removal](https://learn.microsoft.com/en-us/azure/azure-arc/servers/uninstall-agent)
  requires extension removal, disconnect and uninstall. Cloud deletion alone
  does not reset local agent state; locally running
  `azcmagent disconnect --force-local-only` can clean up after cloud deletion.
- [Azure Local VMs have different deletion consequences](https://learn.microsoft.com/en-us/azure/azure-arc/servers/azcmagent-disconnect).
  Machine kind includes HCI, VMware, SCVMM and other variants. Their ownership
  and deletion behavior needs an explicit guard before independent cleanup.
- [Deleting a Run Command](https://learn.microsoft.com/en-us/azure/azure-arc/servers/run-command)
  terminates an executing script. That impact requires review before cleanup.
- [ESU licenses](https://learn.microsoft.com/en-us/azure/azure-arc/servers/license-extended-security-updates)
  may cover multiple machines. A profile reference does not establish ownership
  of the shared license. [Unlinking and license deletion](https://learn.microsoft.com/en-us/azure/azure-arc/servers/api-extended-security-updates)
  are separate operations; machine/profile absence does not prove billing ended.

Native inventory and signed snapshot/reference checks are implemented. Cleanup
incarnation checks, ownership/lock checks, planner ordering, resumed cleanup,
independent emulator and live-cloud verification remain open.
The [Floci-AZ service list](https://floci.io/floci-az/services/) and
[generic ARM fallback](https://floci.io/floci-az/services/arm/) were checked on
2026-09-13: they do not document Arc agent, extension-uninstall or signed
HybridCompute polling emulation. Generic ARM CRUD is insufficient evidence.

## Reproduction

The ordinary checks run offline from the repository root:

```sh
go generate ./providers/azure
go test ./providers/azure -run 'TestHybridCompute' -count=1
```

For online reproduction, download the pinned URLs in `sources.json` and
`cli-recordings.json`, verify complete-source hashes, and extract the indexed
YAML interactions using the field mapping above. Serialize the extraction as
JSON with sorted keys, two-space indentation and a trailing newline. Its SHA-256
is `8661a18c19364d01ff1719ebe280d5caccdd4d9590dd6abd75030982b7bb5eb5`.
The null-path manifest is a local discrepancy record, not an upstream example.
