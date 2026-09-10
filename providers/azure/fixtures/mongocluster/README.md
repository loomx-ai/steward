# Azure DocumentDB management API evidence

Four rules cover `Microsoft.DocumentDB/mongoClusters`, `firewallRules`,
`privateEndpointConnections` and `users`, using stable **2026-06-01**.
This is Azure DocumentDB, formerly Cosmos DB for MongoDB vCore. Its ARM API is
separate from Cosmos DB `databaseAccounts`; data-plane databases and collections
are not invented ARM child resources.

The 14 selected operations and 14 unchanged official examples come from
[azure-rest-api-specs commit e45039b](https://github.com/Azure/azure-rest-api-specs/tree/e45039baa985c442877529906e705982a6e0099d/specification/mongocluster/resource-manager/Microsoft.DocumentDB/MongoCluster/stable/2026-06-01).
The full `mongoCluster.json` SHA-256 is
`2d8659f23e60118c9ed2313146d93bfe5b91142c29e38c33c4937bfb5c9b7154`.
`sources.json` records each example URL and SHA-256. The selected snapshot
preserves native parameters, schemas and transitive common-type references.
Ten GET/LIST example bodies pass their original Draft 4 schemas offline.
The CLI's user GET returns `principalType: User`, whereas the Swagger enum uses
`user`. A test requires this exact discrepancy and validates a separate in-memory
copy using the enum spelling. Original responses and schemas remain unchanged;
runtime review binds the returned principal configuration.

The cluster has subscription and resource-group lists, GET and DELETE. Its
`replicas` operation returns references to separate clusters. The three proxy
collections each have List, GET and DELETE. Provider operation definitions and
private-link group definitions are service metadata, not independently managed
resources. Private-endpoint GET/DELETE operation names collide with Cosmos DB;
the catalog uses the official document titles `Cosmos_DB` and
`MongoClusterManagementClient` to distinguish them, retaining native operation
names, paths and schemas unchanged.

## Lifecycle and protection

The plan includes independent child DELETEs before their cluster. Each cluster
uses complete native firewall, connection, user and replica lists, reads listed
resources individually and reconciles any returned endpoint index. Two complete
walks reject changed membership, configuration and private values. Denied,
partial, malformed, duplicate, out-of-scope and cycling pages never establish
absence. Parent rereads and continuation bindings detect changes during scans.
The common product cursor limits page/resource hashes to 128 KiB; exceeding it
fails the scan rather than claiming completeness.

A replica is an independent cluster with its own children. The source's native
replica index must agree with the replica's current `sourceResourceId` and role.
A required-deletion relationship makes reviewed replica deletion precede source
deletion, without declaring exclusive ownership of the replica. Retaining or
protecting the replica blocks its source. Deleting the replica preserves its
source, including a protected source. Restored clusters and original creation
parameters do not establish current replica ownership. These semantics follow
Microsoft's [replication deletion guidance](https://learn.microsoft.com/en-us/azure/documentdb/troubleshoot-replication).
Cross-subscription dependencies cannot borrow the selected subscription's
credentials to perform cleanup.

Preflight binds creation metadata, configuration, role/source and parent
configuration. Credential-keyed digests include sensitive values without
publishing them. Proxy location can inherit its cluster's display region.
The moving `backup.earliestRestoreTime`, operation states and child indexes are
handled separately from stable configuration. Updating/provisioning clusters
and topology changes require a later retry; stable catch-up and broken replica
links do not themselves prevent deletion. Native tags, locks and provider-owned
resource-group protections remain effective. Each prerequisite must return 404
through its own API, including after its controller disappears.

Microsoft Entra users are cluster registrations with assigned database roles.
Deleting a registration does not delete the Entra principal. Native local MongoDB
users and individual database objects do not have separate ARM lifecycles here.
The official [authentication guide](https://learn.microsoft.com/en-us/azure/documentdb/how-to-connect-role-based-access-control)
explains registration removal and the additional database-role/ownership cleanup
that administrators may need. That data-plane cleanup is not claimed by an ARM
DELETE. Assigned identities, encryption-key URLs and external private endpoints
remain separate configuration/references. Cluster deletion removes its data;
Azure's retained backups and restore behavior remain outside these actions. See
[backup and restore](https://learn.microsoft.com/en-us/azure/documentdb/how-to-restore-cluster).

## Original CLI responses

`cli-recordings.json` contains **483** selected GET/DELETE responses from **10**
official `documentdb` extension recordings at
[azure-cli-extensions commit 0349eb6](https://github.com/Azure/azure-cli-extensions/tree/0349eb646d3225db5fd677114e200efdfd11e3f8/src/documentdb/azext_documentdb/tests/latest/recordings).
They cover cluster CRUD, firewall rules, users, identities, customer-managed
keys, properties, replicas, promotion, restore and missing resources. Original
request bodies and request headers are not copied. Response bodies and selected
response headers remain unchanged. Reproduce with the pinned upstream YAMLs:

```sh
python3 providers/azure/fixtures/mongocluster/reproduce_recordings.py /path/to/upstream-recordings
```

The extractor verifies full YAML hashes before selecting explicit interaction
indices. The fixture SHA-256 is
`905ce84dd7b00e5738b3933901cd86a586f009e697b8a7d0003ca3df14bfad95`.
Fourteen cases replay original 202 DELETEs and their signed pending/success
responses. Both the recordings and selected catalog use **2026-06-01**; there is
no API-version bridge. Only the zero subscription UUID is rebased in memory.
The selected GET bodies supply preflight state; these tests do not replay all
intervening create/update/promotion operations from the original CLI scenario.
Supporting empty child indexes, resource groups and locks are synthetic after
reviewed prerequisite removal. The recordings contain no private-endpoint
DELETE scenario; that action uses its native Swagger example and protocol tests.

The original recordings stop at LRO success. Tests first keep the target present
to prove that operation success alone cannot finish cleanup, then inject an
explicitly synthetic final GET 404 and verify idempotent resume. Requests and
results survive serialization and driver reconstruction. Polling accepts only
the two native DocumentDB operation collections, matching subscription, version
and region, with a resource-bound receipt. Signed `t/c/s/h` values stay out of
logs. Separate tests cover failed/canceled/expired operations, partial responses,
forbidden reads, identity/receipt changes and incomplete final readback.

## Independent verification boundary

The reviewed Floci-AZ **0.12.0** source at
[commit f6f0292](https://github.com/floci-io/floci-az/tree/f6f0292880c6eb4e7fe3d658185030665f98166e)
contains 331 Java files with no `Microsoft.DocumentDB` or `mongoClusters`
management routes. Its MongoDB engine is a data-protocol sidecar. Floci's
separately advertised DocumentDB management API implements Amazon's service;
it does not verify this Azure ARM contract. No independent emulator or live
Azure mutation is claimed here. The native DELETE declarations have no
conditional ETag parameter, so preflight cannot eliminate the final read/delete
race. Provider-wide acceptance remains open.
