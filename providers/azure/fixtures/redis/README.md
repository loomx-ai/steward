# Redis protocol evidence

The 35 unchanged Swagger examples in `sources.json` come from Microsoft REST
specification commit `e45039baa985c442877529906e705982a6e0099d`. The selected
roots are `Redis/stable/2024-11-01/redis.json` and
`RedisEnterprise/stable/2025-07-01/redisenterprise.json`. Source URLs and full
upstream file hashes are retained in the catalog snapshot. Eleven resource
rules select 35 native operations, including both operation-status APIs.

Classic Redis includes caches, access policies, access-policy assignments,
firewall rules, linked servers, patch schedules and private endpoint connections.
Enterprise / Managed Redis includes clusters, databases, database access-policy
assignments and private endpoint connections. Overlapping native operation IDs
are qualified by their source document title; their original wire operations
and schemas are unchanged.

## Native example inconsistencies

Twenty-four GET/LIST/status bodies are checked independently against the native
Draft 4 schemas. Twenty-two pass unchanged. The classic async-status example
supplies null for five optional, non-nullable fields: `endTime`, `error`,
`percentComplete`, `properties` and `startTime`. The Enterprise cluster LIST
example supplies a non-nullable `nextLink` as null. Tests first establish both
original validation failures, then remove only those null fields from private
copies and validate the remaining shape. Original fixture files are unchanged.

The Enterprise access-policy-assignment GET example incorrectly uses a classic
Redis ARM ID/type. Its schema permits those strings, but runtime identity
validation rejects the response. Only the composed lifecycle scenario corrects
that ID/type. Its embedded private endpoint IDs are also aligned to the selected
child examples. The classic reciprocal link view in this scenario is synthetic.

The native Enterprise cluster/database name regex has a bounded ECMAScript
lookahead. The REST binder checks its 1–60 character bound separately and applies
the remaining ASCII-only native pattern with Go's regex engine. Original source
metadata stays unchanged. Boundary tests cover length, internal and repeated
hyphens, non-ASCII text and invalid path characters.

## Microsoft CLI recordings

`cli-recordings.json` contains 61 selected response bodies/statuses from eight
checksum-pinned YAML files. Classic Redis sources use Azure CLI commit
`dc50d475a00ded4a1a1980d4a10a9fbd9a750a81`; Enterprise sources use Azure CLI
Extensions commit `5813689875f128709e8db10903e893138a064236`. The manifest retains
source URLs, hashes and interaction indices. No request bodies are retained.

- Classic authentication, firewall, patch-schedule and server-link recordings
  retain native policy/assignment relationships, built-in policy names with
  spaces, link roles, DELETE 200/202 and Location/Azure-AsyncOperation polling.
- Four Enterprise scenario recordings retain cluster, database and assignment
  reads, DELETE 202 and native regional operationsStatus polling.
- Nine delete flows replay the actual native API versions, returned signed
  polling URLs, pending/success responses and persisted execution state. Signed
  query values are kept privately for polling and removed from API logs. Native
  regional operation URLs can contain encoded spaces; region matching handles
  those names consistently with resource locations.

Reproduce from the original downloaded YAMLs with PyYAML:

```sh
python3 -B providers/azure/fixtures/redis/reproduce_recordings.py /path/to/recordings
```

Replay binds the recorded zero subscription to the test connection. Parent
responses are combined from the recorded scenarios; empty child/protection
collections and all final target GET 404s are synthetic. The server-link
recording exposes no reciprocal secondary child; runtime permits that native
case and separately tests a synthetic reciprocal view. The three-member active
geo-replication scenario and negative cases are synthetic protocol tests, not
cloud recordings or an independent Azure ARM emulator.

## Lifecycle and boundaries

Parent cleanup first deletes reviewed independent children. Built-in classic
access policies require cache cleanup and cannot be directly deleted. Custom
policy deletion requires its assignments to be absent. Native `accessPolicyName`
supplies the typed reference and shared prerequisite; policy ownership is not
inferred from a name prefix.

Classic links require Premium caches. `serverRole` describes the peer: a link
with `Secondary` is the deletable primary-side view. Subscription-wide native
cache/list/detail reads find that shared prerequisite when either cache is
selected, even without a reciprocal child. The secondary view belongs to the
primary unlink's reviewed impacts. Peer IDs, roles, native root/list indexes,
configuration, locks, protected tags and resource-group ownership must agree.
Duplicate links, missing primary views and unreadable peers block deletion.
Readback waits for both the primary target and any surviving peer reference to
disappear. See Microsoft's [classic geo-replication cleanup guidance](https://learn.microsoft.com/en-us/azure/azure-cache-for-redis/cache-how-to-geo-replication).

Native Enterprise database deletion removes that member from a healthy active
replication group. Every participant and root cluster is read and checked
against its frozen public/private configuration, membership and protections,
including participants in other resource groups. A later deletion may observe
a smaller group only after each departed member independently returns 404.
New members, live unlinking, contradictory views and unhealthy/unknown link
states block cleanup. The waiter verifies surviving members no longer refer to
the deleted target, including when preflight already finds the target absent.
Steward does not force-unlink a degraded group. See the native
[database DELETE](https://learn.microsoft.com/en-us/rest/api/redis/redisenterprisecache/databases/delete?view=rest-redis-redisenterprisecache-2025-07-01)
and [active geo-replication lifecycle](https://learn.microsoft.com/en-us/azure/redis/how-to-active-geo-replication).

Configuration and creation changes invalidate reviewed actions; nested discovery
also binds the full parent/root context. Partial or inaccessible lists, invalid
identities, foreign/version-changing continuations and cycles never prove an
empty collection. Private persistence connection strings are excluded from
inventory, plans and diagnostic logs; keyed digests still detect their changes.
Native operation errors, receipt substitution, wrong polling identities,
locations/providers/subscriptions and successful operations with live targets
have retained regression tests.

Deleting a cache/database removes its data. Redis key contents, persistence
files and external private endpoints are separate from this ARM cleanup model.
The selected native DELETEs offer no atomic If-Match contract; identical
recreation without returned creation identity cannot be distinguished. These
checks provide native-schema and recorded/protocol evidence, not live cloud
deletion acceptance. A Redis data-plane server does not emulate these ARM APIs.
