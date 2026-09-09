# Azure Managed Grafana verification

The four resource rules use native ARM List/Get/Delete operations at API version
`2025-08-01`. The selected Swagger is pinned to Microsoft REST API specifications
commit `e45039baa985c442877529906e705982a6e0099d`; its full-file SHA-256 is
`cae55a9a4dc6c86cc7b056982ee7516f744209c7b379597f553563e08fb2b0c8`.
The catalog includes its three transitive common-type files. All preceding 100
Azure source documents remain unchanged.

| ARM resource | Native operation group | Cleanup |
| --- | --- | --- |
| `Microsoft.Dashboard/grafana` | `Grafana` | Delete after all three reviewed child collections are empty |
| `grafana/managedPrivateEndpoints` | `ManagedPrivateEndpoints` | Delete the outbound managed endpoint |
| `grafana/privateEndpointConnections` | `PrivateEndpointConnections` | Delete the service-side connection |
| `grafana/integrationFabrics` | `IntegrationFabrics` | Delete the integration configuration |

The subscription list discovers workspaces; native parent lists discover each
child kind. Selecting a workspace produces independent child prerequisites.
Their own drivers perform native deletes and final GETs, and workspace completion
rechecks their absence. Retaining a child blocks workspace deletion. No external
data source, AKS cluster, consumer private endpoint or user-assigned identity is
selected as an owned child. Private Link resource descriptors are service
capabilities, not independently deletable instances. Grafana data-plane dashboards,
users and data-source settings are not individually inventoried.

The reviewed configuration includes the native fields returned by ARM, with
creation identity where available. Provisioning progress, modification audit
fields and the separately listed connection collection do not identify a new
configuration. The service-side connection inherits its inventory region from
the workspace. Both native child passes must agree; parent configuration binds
pagination and independent child cleanup. Native GET responses do not provide a
conditional DELETE header. These checks cannot prevent an external write between
the last read and DELETE, or distinguish identical recreation without a native
creation field. SMTP passwords are removed before persistence and logging.

## Source examples and native recordings

The 12 `*_Get/List/Delete.json` files are unchanged official examples. Their
immutable URLs and SHA-256 values are in `sources.json`. Several example IDs omit
`/providers/`, the managed-endpoint example reports a singular type, and the
connection example uses `Accepted` outside its declared provisioning-state enum.
Tests retain and reject those identities and detect the schema inconsistency.
The positive protocol fixture keeps the example properties while explicitly
rebinding IDs, types, names, regions, external references and that invalid state
to the declared ARM routes. These corrections are not changes to source evidence.

`cli-delete-recordings.json` extracts three native deletion sequences from
Microsoft Azure CLI recordings, including every recorded poll (14, 5 and 2):

- Workspace: CLI commit `1681210b2e6e1cce1624d1c41e7b32c19d51650b`, source
  `test_amg_crud.yaml`, SHA-256
  `722d81efec8e3f9e563650de29e084202ed0c34d59c3a69474f8d6e26fd2a87c`.
- Both connection types: CLI commit `da13f11483dcc737691f78717dd3d340dd98f8ff`, source
  `test_amg_private_endpoint.yaml`, SHA-256
  `2428b5d42232ae06ad53c5291161d0ffe9d8a744abb169e061698abc5d91da89`.

These recordings use **2023-09-01**, not the selected 2025 API. Resource requests
in the replay use the selected version; native polling URLs retain the recorded
version. The redacted subscription placeholder is rebound to the fixture account.
Signing query values `c`, `s`, `h`, `t` are replaced with explicit fixture values;
irrelevant headers are omitted. Bodies, operation IDs, native paths and statuses
are retained. Final resource absence and the parent/child list surroundings are
explicit protocol fixtures, since the selected recordings do not prove them.
Integration Fabric deletion has Swagger/protocol coverage, without a CLI replay.

The recordings expose ProviderHub's signed, subscription-less operation URLs.
Only returned Grafana operations may use that exact provider/location/path shape.
Persisted receipts bind the URL to the subscription, tenant and target resource;
poll responses must identify the same resource and operation. Ordinary ARM
requests remain subscription-bound. Signing parameters are excluded from API logs.
Expired operation URLs still require native resource and prerequisite absence.

Download the two immutable source URLs recorded in the JSON to a temporary
directory, then reproduce the extraction (Python with PyYAML):

```sh
python3 providers/azure/fixtures/grafana/reproduce_recordings.py /path/to/recordings
go test ./providers/azure -run TestGrafana -count=1
go test -race ./providers/azure -run TestGrafana -count=1
```

The extractor verifies both full-file hashes before writing. Native schemas are
validated offline with an independent JSON Schema implementation. The actual
planner, product inventory, action driver, serialized receipt and final readback
are exercised, including retention, changed parents/children, permissions, locks,
partial/cyclic pages, wrong scope, failed/canceled operations and surviving assets.

The inspected `localaz` emulator at
`f95417983161924670d64e8f86de2bddd9cfb075` lists ARM and Monitor Logs support but has
no Grafana handler. No emulator was substituted with invented Grafana behavior.
These are native-recording and protocol checks; independent emulator, real-cloud
acceptance and full provider parity remain outstanding.

Native references: [workspace DELETE](https://learn.microsoft.com/en-us/rest/api/managed-grafana/grafana/delete?view=rest-managed-grafana-2025-08-01),
[managed endpoint DELETE](https://learn.microsoft.com/en-us/rest/api/managed-grafana/managed-private-endpoints/delete?view=rest-managed-grafana-2025-08-01),
[connection DELETE](https://learn.microsoft.com/en-us/rest/api/managed-grafana/private-endpoint-connections/delete?view=rest-managed-grafana-2025-08-01),
[integration DELETE](https://learn.microsoft.com/en-us/rest/api/managed-grafana/integration-fabrics/delete?view=rest-managed-grafana-2025-08-01).
