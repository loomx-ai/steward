# Azure Container Instances evidence

The five unchanged examples and their sources.json manifest come from the pinned
[2025-09-01 native Swagger](https://github.com/Azure/azure-rest-api-specs/blob/e45039baa985c442877529906e705982a6e0099d/specification/containerinstance/resource-manager/Microsoft.ContainerInstance/ContainerInstance/stable/2025-09-01/containerInstance.json).
The complete source SHA-256 is
0543d3a66e6382cbe7956942a55d9f17760ef7b0fc108e39bda88c7367c8bcb3.
The snapshot keeps subscription List, Get and Delete with all referenced native
schemas. Five original response bodies pass independent JSON Schema validation;
normal builds, generation and tests are offline.

The native [Delete contract](https://learn.microsoft.com/en-us/rest/api/container-instances/container-groups/delete?view=rest-container-instances-2025-09-01)
excludes user-supplied external volumes from deletion. Containers, init containers
and group-local volumes share the [container-group lifecycle](https://learn.microsoft.com/en-us/azure/container-instances/container-instances-container-groups).
Steward retains their configuration inside the group's inventory, with no
invented per-container CRUD or ownership of external shares. Native subnet IDs,
managed identities and Log Analytics resource IDs form explicit references.
Azure Files account/share names, Git repositories and encryption vault URLs stay
in sanitized configuration; no resource-group identity is guessed from a name.

Native reads freeze configuration, supplied creation identity and ETag. A
connection-keyed HMAC additionally binds returned sensitive configuration because
ACI can omit both creation identity and ETag. Commands, environment/config-map
values, registry/volume/workspace keys, probe headers, extension settings and URL
credentials never enter inventory or diagnostic logs in plaintext. The HMAC is
not an unkeyed password hash. Credential rotation requires a fresh inventory.
Runtime instance views and assigned IP/FQDN are excluded from configuration
comparison. New containers, image/network/volume changes, protection, locks,
managed ownership, unreadable resources and changed sensitive settings block
deletion. These checks also apply when an AKS managed group contains ACI groups.

cli-recordings.json retains unchanged resource-group LIST interaction 6, GET
interaction 7 and DELETE interaction 9 from the [Microsoft CLI recording](https://github.com/Azure/azure-cli/blob/6e3b4eab87bc1ec9e58d76361507e9e0be27dd4f/src/azure-cli/azure/cli/command_modules/container/tests/latest/recordings/test_container_create.yaml).
The full YAML SHA-256 is
39f741f11a1bc0bab412d517b79002f18459439e5ce892a90eb8ffcc0b760bdd.
The recorded API is 2024-05-01-preview. Replay rebinds the subscription, selects
the supported 2025-09-01 contract and reuses the native LIST body on the
subscription List route. Permissions and final absence are synthetic. The native
DELETE returns HTTP 200 with the full old group; the runtime still waits for a
native 404 and rejects a survivor. To reproduce the extracts, download the
immutable YAML and run `python3 reproduce_recordings.py /path/to/recording.yaml`
with PyYAML. No credentials, create requests or signed create-operation URLs
are copied from the surrounding recording.

Separate asynchronous protocol tests use the original DELETE example's header
shape. Its placeholder subscription, resource group, region, operation ID and API
version are explicitly rebound. Poll responses are synthetic and cover progress,
failure, expired operations, foreign scope, survivor readback and serialized
resume. Inventory tests cover native pagination, malformed responses, foreign
identities, denied pages, API-version changes and detail mismatches. Tests use
the actual registry, planner, action adapter and request logging.

This is official-example, recorded-response and protocol evidence. It is not an
independent ACI emulator or Steward real-cloud acceptance. Microsoft's local
[Docker tutorial](https://learn.microsoft.com/en-us/azure/container-instances/container-instances-tutorial-prepare-app)
tests container images, not ARM group lifecycle. Native DELETE offers no atomic
If-Match condition. An identical recreation remains indistinguishable if Azure
omits immutable identity; omitted secret values cannot be compared. Full provider
parity and end-to-end cloud acceptance remain separate work.
