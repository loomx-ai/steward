# Azure AI Search protocol evidence

Eleven unchanged Microsoft Swagger examples in `sources.json` accompany the
2025-05-01 Search management API, pinned at REST specification commit
`e45039baa985c442877529906e705982a6e0099d`. Four rules select eleven native
operations: service, private endpoint connection and shared private link
GET/LIST/DELETE, plus network security perimeter configuration GET/LIST.
The catalog retains the original operations, references and complete source
hashes, including the v6 common resource and perimeter definitions.

## Native schema checks

Eight GET/LIST responses are independently checked against their native Draft 4
schemas. Four pass unchanged; four contain optional null values whose schemas
lack nullability: service LIST `serviceUpgradedAt` in both entries, perimeter
configuration LIST `nextLink`, shared link GET `resourceRegion`, and shared link
LIST `resourceRegion`. Tests assert the original validation failures and validate
the remaining shape after removing only those values from private copies.
Source fixtures and catalog schemas remain unchanged.

The shared-link GET example omits `provisioningState`. A composed lifecycle
scenario adds `Succeeded`, because the documented delete operation permits only
`Succeeded` or `Failed`. A separate negative test rejects a missing state.
The scenario aligns the service's two embedded child indexes with the selected
child examples and supplies a synthetic Storage target. Source examples are
never rewritten to make them appear mutually consistent.

The native Search service name pattern uses a bounded ECMAScript lookahead.
The REST binder checks its 2–60 character bound and applies the remaining native
ASCII pattern without a backtracking engine. Tests cover the exact native bounds
and invalid names. Perimeter configuration IDs use their documented GUID/name
format rather than an invented generic resource name.

## Microsoft CLI response replay

`cli-recordings.json` retains nineteen selected GET/DELETE response bodies,
statuses, headers, original URIs and interaction indices from three upstream
recordings at Azure CLI commit `db34d9752ceddcde6db94eec5d681f6742d86403`.
Each full source has a checksum; no request bodies are copied.

The service recording uses the selected 2025-05-01 API, including DELETE 200 and
final GET 404. The two legacy CLI child commands still used 2022-09-01: connection
DELETE 200 followed by GET 404, and shared link DELETE 202 followed by signed
operation-status polling, an empty LIST and GET 404. This is a version bridge,
not exact 2025-05-01 wire evidence for the children. Runtime requests bind the
selected 2025-05-01 schema; the replay preserves recorded response bodies and
returned 2022-09-01 polling URLs. Subscription zeros are mapped to the test
connection. Supporting empty collections, inherited locks/resource-group reads
and the Storage target GET are synthetic. The final 404s are recorded responses.

Reproduce with PyYAML from the original YAMLs named `stable-<upstream filename>`:

```sh
python3 -B providers/azure/fixtures/search/reproduce_recordings.py /path/to/recordings
```

Three deletion replays serialize the action request/result and resume polling.
A terminal operation with a still-live target remains incomplete. Poll receipts
bind the resource and returned URL; signed query parameters are removed from
logs. Synthetic cases also reject 403/409/206, failed/canceled polls, altered
receipts, wrong operation identities, locations, providers and subscriptions.

## Lifecycle, protection and limits

Service deletion first requires its reviewed private endpoint connections and
shared links to be independently absent. Its read-only perimeter configuration
views share the service lifetime and must also return 404 at completion. Retaining
any of these resources blocks service deletion. The external perimeter and its
association are not claimed as service-owned or independently deleted; their
full native lifecycle remains unfinished.

Shared-link deletion removes the Search-managed private endpoint and DNS mappings
and asks the target provider to update its connection metadata. Native target
GETs, configuration digests, target and inherited locks, protected tags and
managed resource-group ownership are checked before DELETE, with a final target
re-read. The target's own data resource is never deleted by unlinking. See
Microsoft's [shared-link deletion contract](https://learn.microsoft.com/en-us/azure/search/troubleshoot-shared-private-link-resources).

Target reads require a modeled native resource API in the selected subscription.
Storage, SQL servers, App Service, AKS and Cognitive Services now have those
bindings. Cosmos DB and other unmodeled targets, or targets outside the selected
subscription, currently block link discovery/deletion rather than borrow an
unverified API version or credential. These remaining target families are an
explicit completeness gap, not evidence of full Search private-link coverage.

Native lists, detail reads, two reconciled child walks and frozen parent/context
checks reject changed membership, recreation, malformed/duplicate/foreign child
IDs, partial collections and changed paging scope/version. Child location inherits
the parent. Public and keyed private configuration checks remain strict while
allowing the documented runtime fields and service ETag to change as reviewed
children depart. No atomic conditional DELETE header is offered by this API;
identical recreation without a returned creation identity cannot be detected.

Deleting a Search service removes its search content. Indexes, indexers and
other data-plane objects are not independently inventoried by these ARM rules.
The hidden Search-managed endpoint/DNS resources have no identities in the
shared-link GET; final link absence verifies the RP contract, not independent
GETs for those hidden resources. All evidence here is schema, recorded-response
and protocol verification, not an independent ARM emulator or live Azure cleanup.
