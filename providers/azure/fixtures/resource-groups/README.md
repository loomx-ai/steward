# Resource-group deletion operation evidence

`delete-recording.json` extracts the DELETE and subsequent operation-result GETs
from Azure CLI's `test_resource_group.yaml`, pinned to commit
`023a11c0c775ebba02b559ab8e5d9d489bf82133` (azure-cli-2.40.0).
The fixture retains native URLs, HTTP statuses, bodies, Location and Retry-After
headers. Unrelated requests and headers are omitted. `source_sha256` hashes the
complete upstream YAML bytes, not the extracted JSON. Azure CLI sanitized the
subscription and group name upstream; no additional URL normalization was applied.

The recorded API version is the runtime's `2021-04-01`. DELETE returns 202 with
Location, four GETs return 202, and the fifth returns an empty 200. The operation
identifier is an opaque, case-sensitive token, not a UUID. The native replay test
uses these exact callback URLs and response bodies across JSON checkpoint restores.

A newer [official recording](https://github.com/Azure/azure-cli/blob/27554ab8a5375aab7529e6862934bf26afbb8762/src/azure-cli/azure/cli/command_modules/resource/tests/latest/recordings/test_resource_group.yaml)
uses `2024-11-01` and includes `t`, `c`, `s`, `h` signing parameters. Its first
pending GET supplies a new Location with different signing material but the same
operation path. Complete source SHA-256:
`72b54f60427bcc7ec3ec0789041b605f4098d5c794ec807d430b5acc9e413b17`.
The synthetic rotation test exercises that behavior using dummy signatures at the
pinned runtime version. It is not a live 2021-04-01 signed-callback recording.
Versions are not rewritten in the native replay fixture or accepted arbitrarily.

[ResourceGroups_Delete](https://learn.microsoft.com/en-us/rest/api/resources/resource-groups/delete?view=rest-resources-2021-04-01)
defines 200/202. The [resource-group deletion lifecycle](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/delete-resource-group)
also describes synchronous 204. A missing initial Location on 202 is not treated
as success. Poll 404/403 and malformed results remain failures, not group absence.
No DELETE is retried by this polling helper. It does not enable group actions,
authorize native cascade, prove deleted-member outcomes, or close parity gates.
