# Azure DomainRegistration evidence

The seven JSON examples are unchanged Microsoft ARM Swagger examples from
`Microsoft.DomainRegistration/DomainRegistration/stable/2024-11-01` at commit
`c20bf553ad64f20c6d5e3f56080380c086cb1fde`. `sources.json` records each original
URL, complete-file SHA-256, operation, method and native path. The retained
catalog source includes the selected native operations and their transitive
schemas. `TestDomainOfficialContracts` verifies all source hashes, binds all
seven operations, and validates nine example responses, including five bodies,
against those schemas without network access.

Two upstream example details remain unchanged: the DELETE example supplies
`forceHardDeleteDomain=true`, and the GET example supplies
`getOnlyIfReadyForDnsManagement` although that parameter is absent from this
operation's schema. The test omits that extra parameter only from its local
request-binding input. Production explicitly sends `forceHardDeleteDomain=false`;
independent wire tests verify it. The native contract declares DELETE 200/204,
no If-Match condition, and no operation-status endpoint. Its default deletion
delay is 24 hours. Completion requires the domain's named GET to return 404.
See the [pinned contract](https://github.com/Azure/azure-rest-api-specs/blob/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/domainregistration/resource-manager/Microsoft.DomainRegistration/DomainRegistration/stable/2024-11-01/openapi.json)
and Microsoft's [domain management guidance](https://learn.microsoft.com/en-us/azure/app-service/manage-custom-dns-buy-domain).

The native protocol scenarios compose consistent subscription/resource IDs,
an independent DNS zone, ownership identifiers and App Service/slot bindings.
Synthetic ETags, future settings, ownership tokens and operational transitions
are test inputs, not recorded Azure responses. Tests cover global discovery,
pagination with changed private parent configuration, two complete dependency
observations, named reads of known entries omitted from a list, inherited
protection and locks, denied/incomplete/async reads, recreation/configuration
changes, forged plans/receipts, delayed hostnames, and final child absence after
the root is gone. Unreviewed hostnames and Traffic Manager assignments block
deletion. A DNS-only plan cannot silently select a registered domain; explicit
selection establishes deletion order. Domain-only cleanup preserves DNS.

`TestDomainRegisteredWorkersAndDurableDelay` uses real SQLite persistence and
the registered inventory, graph, plan and execution workers. Twenty-one global
and regional shards produce 14 assets. A domain plan has three independent
prerequisites and four deletion steps, with no owned cascade impacts. Each
mutation verifies its previously saved idempotency intent and native request.
Every retry reopens SQLite, round-trips the job through JSON and recreates the
provider, registry and worker. The test retains the hostname index after app
binding removal, then clears it and observes exactly one domain DELETE across
saved-phase retries. It advances the stored wait clock beyond 25 hours, leaves
a known child alive after the root disappears, and verifies final closure of
the four selected assets with ten unrelated/shared assets retained. Journal,
plan and API log assertions exclude private domain configuration. Concurrent
crashes between an unconditioned native write and saving its receipt are not
proof of exactly-once Azure mutation.

The associated App Service regression removes parent read-only hostname-index
entries after one binding deletion and verifies that the next independently
reviewed binding still deletes. Parent authored settings and each binding's
own full configuration remain checked.

No independent emulator or live DomainRegistration CRUD execution is claimed.
The inspected Microsoft Azure CLI `test_domain_e2e.yaml` and
`test_domain_create.yaml` recordings at commit
`8bead7f93f086629efb160d56c25f508156925bf` contain availability/agreement calls,
not registration List/Get/Delete evidence; they are not used as CRUD fixtures.
