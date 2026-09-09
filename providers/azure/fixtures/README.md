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

LRO protocol reference:
https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/async-operations

Blob version/snapshot enumeration reference:
https://learn.microsoft.com/en-us/rest/api/storageservices/list-blobs
