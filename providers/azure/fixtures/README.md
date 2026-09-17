# Azure protocol fixtures

The VM, public-IP, Uniform scale-set VM and scale-set NIC/IP-configuration/
public-IP, Virtual WAN connection, NAT-rule and VPN-link JSON files are unchanged examples
from Microsoft's versioned `Azure/azure-rest-api-specs` repository. `sources.json`
records their exact URLs and original SHA-256 values. Tests replace only example
subscription/name placeholders in memory so ARM identities satisfy validation.
The VPN-link fixture also retains the official response's `VpnSiteLinkConnections`
ID/type spelling. Tests verify its narrowly bound `vpnLinkConnections` request
route, complete parent identity, cascade readback, and duplicate rejection.

The retained tests also model native ARM list, OAuth, LRO, lock, and Blob XML
responses. These verify wire protocol behavior, error handling, and application
normalization. They are not evidence of an independent ARM emulator or a live
Azure account.

[Budget notifications](monitorbudgets/README.md) retain 20 original Consumption
and Cost Management examples, including subscription and billing boundaries.

[Monitor alerts](monitoralerts/README.md) retain 36 original examples for seven
alert and notification families. Thirty native operations, 44 responses,
explicit upstream schema discrepancies and endpoint-aware privacy are checked
separately from resource inventory and cleanup execution.

`servicebus-migration-revert-recording.json` selects request methods/URIs and
unchanged response bodies from an immutable official Azure CLI test recording.
It records the original file and body SHA-256 values and omits all headers. The
recorded API version is **2026-01-01**; it provides independent evidence that
`Active` with zero pending copies is still paired, and that Revert clears
`targetNamespace` while preserving `postMigrationName`. The runtime's native
Revert route is separately bound and tested against the **2024-01-01** Swagger.
These are distinct sources, not a claim of live testing of that pinned version.

`servicebus-recovery-break-recording.json` and
`eventhubs-recovery-break-recording.json` similarly retain unchanged bodies from
immutable official Azure CLI recordings. They show the reciprocal primary and
secondary alias views, the empty HTTP 200 response to BreakPairing, subsequent
`Accepted` reads, and the eventual `PrimaryNotReplicating` state with a cleared
partner. Their recorded versions are **2026-01-01** and **2026-07-01-preview**;
the runtime bindings use **2024-01-01**. These recordings do not read the
secondary alias after BreakPairing, so they do not establish when that view
disappears. No request or response headers are retained.

`eventhub-Clusters-*.json` retains five unchanged 2024-01-01 Swagger examples:
cluster Get/List/Delete, the namespace-ID list and singleton quota settings.
The member list explicitly contains namespaces in multiple resource groups.
Quota settings have no ARM ID and no independent DELETE; they are enriched
cluster properties. The Delete example contains a placeholder HTTP operation
URL, which is kept unchanged as source evidence; executable LRO tests use an
owned HTTPS ARM URL and verify completion with native resource readback.

LRO protocol reference:
https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/async-operations

Blob version/snapshot enumeration reference:
https://learn.microsoft.com/en-us/rest/api/storageservices/list-blobs

[CDN and Front Door](cdn/README.md) retain 42 original Swagger examples and 17
Microsoft CLI responses, including signed asynchronous deletion and the native
None-error placeholder. Schema inconsistencies, API-version rebinding and
injected lifecycle/readback cases are explicitly documented.

[Cognitive Services and Foundry](cognitive/README.md) retain 67 original Swagger
examples and 31 Microsoft CLI responses. Their native schemas, account/project
dependencies, managed-network cleanup, soft deletion and remaining connection
network gaps have documented checks and provenance.

[Cosmos DB](cosmos/README.md) retains 117 original Swagger examples and 386
Microsoft CLI responses. Its evidence distinguishes upstream trigger schema
defects, case-sensitive requests, signed operation replay, synthetic final
absence and the available emulators' data-plane boundary.

[Azure DocumentDB / MongoDB vCore](mongocluster/README.md) retains 14 original
Swagger examples and 483 responses from 10 official CLI recordings. Fourteen
exact-version DELETE/LRO replays complement replica-order, protection, paging,
private-configuration and synthetic final-absence checks. Its ARM surface is
separate from Cosmos DB accounts and from MongoDB data-protocol emulators.

[Azure Data Explorer](kusto/README.md) retains 42 original Swagger examples and
45 responses from the official Kusto CLI scenario. Eight native DELETE/status
poll chains, display-region URLs and an empty Location result complement the
follower, protection, retention, configuration and resumed-readback tests.
The evidence distinguishes the older CLI API bridge, composed topology,
synthetic indexes/final absence and query-engine-only emulator scope.

[Key Vault certificates](keyvault/sources.json) retain the two original data-plane
examples for certificate list and current-version reads. Tests check their
object identifiers against the vault origin; the certificate body is never stored.
