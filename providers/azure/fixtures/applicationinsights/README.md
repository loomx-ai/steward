# Application Insights and Azure Monitor private-link evidence

This evidence is pinned to Azure REST API specifications commit
`e45039baa985c442877529906e705982a6e0099d`. The current foundation adds 63
catalog operations from 16 root documents and six transitive reference
documents. Native response transport, content redaction and operation-location
checks are implemented. Monitor Private Link Scope inventory and lifecycle
bindings are implemented as described below. Application Insights resource
inventory and cleanup remain in progress; this directory does not establish
completed Application Insights support.

## Native schemas and examples

`sources.json` describes 69 operation/example bindings to 67 unchanged example
files. Each entry records the original source URI, SHA-256, operation, method
and path. `documents.json` records all 22 source-document hashes, including
their dependency status. The selected versions are:

- Components: `2020-02-02`.
- API keys, annotations, analytics items, favorites, continuous exports,
  proactive detection and work-item configuration: `2015-05-01`.
- Current pricing: `2017-10-01`, alongside the older billing-feature reads.
- Workbooks: `2023-06-01`; private workbooks: `2021-03-08`; workbook
  templates: `2020-11-20`; web tests: `2022-06-15`.
- Component linked storage: `2020-03-01-preview`, the published native
  interface for the fixed `ServiceProfiler` storage association.
- Azure Monitor Private Link Scopes, scoped-resource associations, private
  endpoint connections and operation status: `2021-09-01`.

`TestApplicationInsightsNativeSources` checks all 56 selected response bodies
that have a native schema. It interprets explicit Swagger `x-nullable` in the
in-memory validator without modifying the source files. The remaining 23
example/status cases contain 111 published nullability discrepancies, recorded
exactly in `schema-discrepancies.json`. The test compares every leaf validation
error with that pinned list; additional or disappearing errors fail the test.
Schema conformance is separate from runtime identity and ownership checks.

Examples establish several nonstandard response contracts:

- Analytics-item, continuous-export, favorite and proactive-detection lists
  return arrays directly. An annotation GET also returns an array.
- Private-workbook list examples also return arrays, although their schema
  describes a `value` envelope. That schema omits `type`, so ordinary JSON Schema
  validation does not reject the array. Only the two selected `2021-03-08`
  private-workbook list paths accept both published shapes.
- Analytics items select their identity with the `id` query parameter on the
  fixed `/item` endpoint. Shared and user paths are distinct.
- Annotation LIST requires both `start` and `end`; the source limits `start`
  to the previous 90 days. It does not provide an unbounded historical index.
- API keys have full ARM IDs but friendly `name` values. Exports, annotations,
  work-item configurations and favorites expose their own identifier fields.
- Workbook lists require a category; reads can explicitly request full
  content. Revisions are read-only views. Workbook and web-test roots remain
  independently addressable resources.
- Original source paths spell the provider namespace both `Microsoft.Insights`
  and `microsoft.insights`. Catalog operation IDs retain that source spelling.

`operation-coverage.json` audits all 43 top-level stable/preview documents in the
pinned Application Insights tree and the Monitor private-link document. All
downloaded Application Insights files were checked against their Git blob IDs.
The audit contains 244 operations across 44 documents, including 15 distinct
DELETE paths: 12 in Application Insights and three in Monitor private links.
All 15 have selected native catalog operations. The audit separates selected
reads/deletes, alternate versions, creation/update, capability reads, telemetry
purge, diagnostic tokens, billing migration and deleted-resource views.
`TestApplicationInsightsNativeOperationCoverage` checks the pinned audit against
the actual selected catalog in both directions. Selection is not evidence of
an executable resource cleanup binding.

`TestApplicationInsightsNativeTransport` exercises 80 example/status responses,
including nine direct-array bodies, through the real HTTP parser and selected
catalog request binder. The test makes these explicit fixture adaptations:

- Replace the request's literal `subid` placeholder with the test subscription;
  response bodies remain unchanged.
- Remove the undeclared `scope` from analytics GET/DELETE examples, resource-group
  parameters from subscription-level workbook/web-test examples, and `sourceId`
  from the subscription workbook-list example. Runtime binding still rejects
  undeclared parameters.
- Materialize subscription, group and operation-ID placeholders in the three
  relative Monitor deletion `Location` examples. These are examples, not
  recorded asynchronous operations.

The parser retains JSON number precision, rejects malformed/trailing content,
and limits array adaptation to the selected methods, paths and versions.
Endpoint-aware redaction covers flat legacy responses as well as ARM objects;
raw configuration remains available privately for later drift checks. Opaque
queries, annotation/work-item content, workbook/template bodies, web-test
request content and component keys cannot escape through API logs or Invoke.

Monitor deletion locations are resolved only to the native operation-status
path in the initiating subscription/resource group, with the selected version.
Missing, duplicated, conflicting or foreign locations fail validation. Resolved
locations use status polling, and operation responses must carry a status with
matching IDs/names when present. Executable Monitor actions additionally bind
persisted receipts to the selected connection, partition, credentials, resource
and polling protocol, and check any returned `resourceId` against that resource.

## Monitor private-link lifecycle

Three global resource kinds bind their original native GET/LIST/DELETE APIs:
`privateLinkScopes`, `privateLinkScopes/scopedResources` and
`privateLinkScopes/privateEndpointConnections`. Scope cleanup requires separate
child DELETEs. Two complete child inventories, native detail reads and any
embedded private-endpoint-connection list must agree. Capability descriptions
are read through both native APIs and reviewed with their scope; they have no
independent delete rule. Parent access modes, exclusions, creation fields and
child configuration are checked before actions. Private configuration uses a
credential-keyed digest. The native DELETE contracts have no conditional header.

Scoped-resource deletion retains its linked resource, and connection deletion
retains the network private endpoint. Workspaces and data collection endpoints
instead acquire explicit incoming-association prerequisites. The implementation
reconciles two complete subscription scope/association indexes and the target's
native reverse references. Omitted optional reverse arrays do not hide local
links; malformed, contradictory or stale indexes block cleanup. Foreign
subscription references remain unresolved and block target deletion without
issuing requests outside the selected subscription. A Monitor workspace's
managed DCE receives the same external-association review. Removing associations
does not transfer their scope's ownership to the monitored target.

The native source spells component reverse fields `PrivateLinkScopedResources`,
`ResourceId` and `ScopeId`, and uses camel case for workspaces/DCEs. `scopeId` is
an immutable identifier, not an ARM parent path. Component cleanup is still
pending its own resource adapters. The ordering requirement is documented in
[Microsoft's AMPLS configuration guide](https://learn.microsoft.com/en-us/azure/azure-monitor/fundamentals/private-link-configure#connect-resources-to-the-ampls).

`TestMonitorPrivateLinkNativeLifecycle` composes the three unchanged official
detail examples under one subscription and scope. Its asynchronous transitions,
lists and final absence responses are explicit test state, not CLI recordings.
Other lifecycle tests cover access/configuration/creation changes, capabilities,
retention, locks, malformed or changing indexes, missing assets, cross-subscription
links, restart, credential/connection receipt substitution, failed/canceled
operations, delayed absence and expired operation polling. Shared-target tests
cover separate workspace/DCE cleanup and the managed Monitor workspace workflow.
The published `PrivateEndpointConnectionList.json` contains two equal IDs with
different names; ordinary duplicate/identity checks reject that example as an
authoritative inventory. Its original bytes are retained, not corrected.

No native AMPLS recording was found in the pinned Azure CLI tree
`dc50d475a00ded4a1a1980d4a10a9fbd9a750a81`. Its
[private-link test](https://github.com/Azure/azure-cli/blob/dc50d475a00ded4a1a1980d4a10a9fbd9a750a81/src/azure-cli/azure/cli/command_modules/monitor/tests/latest/test_monitor_private_link_scope.py)
is explicitly skipped after a component-association failure. The downloaded
test source SHA-256 is
`c442341b7010423232db856f4b0f4b85140315093eaeb6488d5a7100f43036d6`.
That skipped scenario is not passing evidence. No independent AMPLS emulator
or real-cloud lifecycle verification is claimed.

## Official CLI recordings

`cli-recordings.json` retains 42 original response bodies and selected response
headers from ten public Azure CLI extension recordings at commit
`a40e7adf5e136e663273bf68a725e8f645554131`. It contains 33 successful GETs,
seven successful DELETEs and two GET 404s. Request credentials and request
bodies are not retained. API versions, response casing, opaque export IDs,
array bodies and empty deletion responses are unchanged.

The private-workbook LIST returns `InvalidResourceType` in its original
recording. That is evidence of an unavailable API in that recorded environment,
not evidence of an empty inventory. The linked-storage recording separately
contains actual GET absence after deletion. Other recordings must not be
described as proving final absence unless the replay identifies any composed
support reads and synthetic responses explicitly.

Download the ten named source YAML files from the URLs in the extraction,
then reproduce the response evidence with:

```sh
python3 providers/azure/fixtures/applicationinsights/reproduce_recordings.py /path/to/recordings
```

The extractor verifies every full recording hash before reading it. Its output
SHA-256 is `a212ab38fdca5f2612a1c81202b1f2a22767b142374dda822bec99304c4ac6bf`.
`TestApplicationInsightsRecordedTransport` replays all 42 responses through the
HTTP transport without changing their selectors or bodies, including both
original 404s. This proves transport compatibility, not complete inventory or
cleanup lifecycle behavior.

## Ownership and independent emulator research

Application Insights may use either a shared Log Analytics workspace or a
managed workspace. The [managed-workspace documentation](https://learn.microsoft.com/en-us/azure/azure-monitor/app/managed-workspaces)
states that component deletion initiates deletion of its managed resource group
and workspace. AMPLS associations, locks or policy restrictions can leave them
behind. Therefore a component's absence alone cannot prove completion of that
managed deletion. Shared workspaces require independent ownership evidence and
must not be inferred to be owned from `WorkspaceResourceId` alone.

[Topaz](https://github.com/TheCloudTheory/Topaz) provides an independent
Application Insights component emulator. The inspected `v1.10.222-preview`
release source is `79e0ff08ab8919daca6eed2a3b22642a3f8ea083`. Its component
control-plane implementation includes GET, LIST and DELETE, but does not
establish support for this family's nested configuration, managed-group
cascade or AMPLS behavior. The downloaded macOS ARM64 host binary matches the
release's SHA-256
`ca59b4fdc9c439ccbad83ec0ba7dc7e4e7bb6f7e1c270cb61aebc298ef920308`.
The release certificate fixture SHA-256 is
`7f0ffe33dc986b3a73bf1607394805aaac4a5a93b1df3c79c5ce91c5fdba0b50`.

`TestApplicationInsightsIndependentEmulatorTransport` passed against this
unmodified release on loopback. It creates an isolated group/component, invokes
the selected component GET and LIST, verifies key redaction, deletes the
component, and verifies a subsequent native GET 404. All Insights requests reach
Topaz. The adapter supplies Azure OAuth/subscription validation fixtures and
uses Topaz's published CLI test credential only for the local emulator.

The test also pins the emulator's limitations: API-key LIST returns 404,
`applicationType` differs from the official `Application_Type` property, and
`AppId`/`CreationDate` are absent. These are not accepted as proof of complete
Azure resource semantics. No live Azure resources were used.

Place the pinned host binary and `topaz.pfx` from the release in one isolated
directory, run the host from that directory with
`--default-subscription 11111111-2222-4333-8444-555555555555`, and extract its
public certificate for the test:

```sh
openssl pkcs12 -in topaz.pfx -passin pass:qwerty -clcerts -nokeys -out topaz-cert.pem
STEWARD_TOPAZ_EMULATOR_URL=https://127.0.0.1:8899 \
STEWARD_TOPAZ_EMULATOR_CA=/absolute/path/to/topaz-cert.pem \
go test -race -count=1 -v ./providers/azure -run '^TestApplicationInsightsIndependentEmulatorTransport$'
```

The test trusts that certificate only in its own HTTP client and verifies the
`management.topaz.local.dev` server name. The host's runtime data stays in its
working directory. Stop the host after testing.
