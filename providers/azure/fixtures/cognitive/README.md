# Cognitive Services / Foundry protocol evidence

The 23 rules select 67 unchanged operations from the stable 2026-05-01
Cognitive Services management API at REST specification commit
`e45039baa985c442877529906e705982a6e0099d`. `sources.json` records the source
URL and complete SHA-256 of each of the 67 unchanged official examples.
The catalog also retains the transitive common-type definitions. These are
management-plane resources, not model inference or Agent Service data-plane APIs.

`TestCognitiveNativeSchemasAndProvenance` checks all example hashes and validates
46 GET/LIST response bodies against their native Draft 4 schemas offline.
All 46 bodies pass. Schema validation does not establish request/response
identity: the upstream CommitmentPlans_Get, Deployments_Get, OutboundRule_Get,
NetworkSecurityPerimeterConfigurations_Get, PrivateEndpointConnections_Get,
RaiToolLabels_Get and CommitmentPlans_GetAssociation examples return IDs which
differ from their example request parameters. OutboundRule_Get additionally
reports `accounts/outboundRules` instead of its full nested ARM type. Several
RAI examples omit `type`; a missing type is allowed only with the full ID bound
to the selected operation. No malformed short type is accepted as an alias.
The composite scenario explicitly assigns consistent IDs, parent indexes,
connection references and locations; source files are never rewritten.

## Inventory and dependency checks

Every kind uses its native list, including both account and project connections
with `includeAll=true`. Subsequent pages must preserve that setting and may not
add target/category filters. Shards retain subscription, collection, API version,
parent generation and every Cognitive Services ancestor. The application
`name` parameter is explicitly bound to the agent deployment list's `appName`.
A denied, partial, malformed, cross-parent or changing collection is not empty.
Native GET locations remain bound separately from inherited display locations,
including location changes on projects and deeper ancestors during pagination.

Native child lists are reconciled twice and checked against available account
private-endpoint/project/commitment indexes and managed-network outbound-rule
indexes. Credential-keyed digests cover private configuration and ancestors.
Account creation identifiers, agent deployment IDs and other native creation
fields remain bound; removing reviewed children can change parent indexes and
ETags without discarding the rest of its configuration. A changed default
project or a newly associated project invalidates the review.

Account capability hosts require dependent projects to be removed first. Project
capability hosts require their agent applications; applications require agent
deployments. Account/project connection deletion checks referring capability
hosts. Bare connection names are resolved in their permitted account/project
scopes; missing and ambiguous names fail. Model parent/spillover deployment,
content-policy/blocklist and tool-label/project references supply explicit
shared prerequisites. Subscription-listed commitment plans and native account
associations provide the reverse association index. Removing an association
keeps the shared plan and checks the referenced account's identity, private
configuration, inherited protection and locks.
Key Vault connections require every other account/project connection to be
deleted first, because those connections depend on the stored secrets. Native
reference names reject malformed types and surrounding whitespace.

Independently deletable account/project children are reviewed before parent
deletion. Defender and perimeter configuration views, system content policies
and managed-network rules have owner-driven cleanup. A network's rule impacts
include target protection checks for managed private endpoints, even though the
network performs the deletion. Required/dependency rules do not expose direct
cleanup. A user rule with derived `parentRuleNames` dependents currently requires
reviewed network cleanup; individual removal of that shared derived-rule
lifecycle is unfinished. Retaining any required child blocks its controller.
Parent absence never substitutes for each reviewed managed resource's GET 404.
Connections declaring required or active managed private endpoints, or unknown
network states, remain protected from direct and controller cleanup until their
endpoint effects can be modeled. Ordinary connections retain native deletion.

## Native CLI responses

`cli-recordings.json` retains 31 original GET/DELETE response bodies from seven
Azure CLI recordings at commit
`dc50d475a00ded4a1a1980d4a10a9fbd9a750a81`. The extractor copies no request
bodies and keeps only relevant request-id/operation response headers. Reproduce
it with downloaded upstream YAML files and:

```sh
python3 providers/azure/fixtures/cognitive/reproduce_recordings.py /path/to/upstream-recordings
```

Seven replays exercise account, account commitment-plan, model deployment,
private-endpoint connection, account connection, project connection and project
deletes. Requests bind the selected stable 2026-05-01 API; original recordings
used 2026-05-15-preview, except the private-endpoint connection operations using
2022-03-01. This is an explicit version bridge, not proof that those recordings
were produced by the selected API version. Supporting account collections,
locks and resource groups use synthetic responses; the project GET is supplied
by its recorded LIST item. The recorded deletes return 200 and omit final target
GETs. Tests prove 200 does not complete cleanup while the resource still exists,
then supply an explicitly synthetic target GET 404 after serialization/restart.
The separate soft-delete recording preserves an original deleted-account GET.
No purge operation or credential-retrieval operation is selected.

Synthetic tests separately exercise asynchronous receipts, operation/target
identity, failed/canceled operations, nonterminal live states, inherited locks,
managed groups, sensitive configuration drift, retained members, target metadata
changes and incomplete final readback. They do not replace a native recording or
an independently implemented emulator.

## Lifecycle sources and remaining gaps

- [Recover or purge resources](https://learn.microsoft.com/en-us/azure/ai-services/recover-purge-resources):
  accounts are soft-deleted. Pre-provisioned deployments must be deleted before
  the account to avoid continued deployment charges during soft deletion.
- [Capability hosts](https://learn.microsoft.com/en-us/azure/foundry/agents/concepts/capability-hosts)
  and [Agent Service recovery](https://learn.microsoft.com/en-us/azure/foundry/how-to/agent-service-operator-disaster-recovery):
  host deletion removes access to dependent agent state. Agent definitions,
  threads, files and vector-store state are not separately inventoried here;
  orphaned customer data is not claimed deleted.
- [Managed virtual networks](https://learn.microsoft.com/en-us/azure/foundry/how-to/managed-virtual-network):
  Foundry owns the hidden network, firewall and managed private endpoints.
  Customer-visible rule GETs supply the verification boundary; hidden resources
  have no subscription NIC identities and are not independently read back.
- [Account properties](https://learn.microsoft.com/en-us/javascript/api/@azure/arm-cognitiveservices/accountproperties):
  account kind and project-management capability vary. This evidence does not
  prove every selected child API is available on every legacy account kind.
  Unsupported/denied collection reads remain incomplete; they are not silently
  accepted as empty collections.
- [Connection and Key Vault constraints](https://learn.microsoft.com/en-us/azure/foundry/how-to/connections-add#azure-key-vault-limitations):
  a Key Vault connection may be deleted only after all other resource/project
  connections. The external vault and its secrets are not owned deletion impacts.

External perimeter associations, subscription-level RAI policies/safety providers,
legacy-kind applicability, managed connection private-endpoint effects, and
unmodeled external target APIs (including Cosmos DB and Key Vault) still need
additional lifecycle/coverage evidence. Cross-subscription targets fail the
selected credential boundary. External storage, search services and shared
plans are not deleted merely because they are referenced. No live Azure
mutation or independent Foundry emulator was used for this evidence. Native
DELETE operations do not provide a conditional ETag parameter; preflight reduces
but cannot eliminate the final read/delete race.
