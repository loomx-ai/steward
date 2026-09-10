# API Management protocol and emulator evidence

The selected stable API is `2024-05-01`. The current catalog models 100 APIM
resource kinds, including services, workspaces, standalone gateways, configuration
containers and independently addressable associations.
Email templates remain read-only: their DELETE resets the template instead of
removing it. Built-in groups, the administrator user and the master subscription
require their owning service's reviewed cascade.

## Original REST API examples

`sources.json` records the original URI and SHA-256 for 324 unchanged examples
from [azure-rest-api-specs at e45039b](https://github.com/Azure/azure-rest-api-specs/tree/e45039baa985c442877529906e705982a6e0099d/specification/apimanagement/resource-manager/Microsoft.ApiManagement/ApiManagement/stable/2024-05-01).
`documents.json` anchors the selected 45 APIM source documents, including shared
definitions. The offline schema test verifies all 223 response bodies that have
a native schema. Ten published examples have documented schema discrepancies:

- API tag description and an OAuth authorization-code `scopes` field are null
  despite non-nullable schemas.
- Private-endpoint-connection GET and LIST return provisioning state `Pending`,
  which is absent from the published provisioning-state enum.
- Portal configuration delegation fields, portal revision status details and a
  tenant settings dictionary value are null despite string-only schemas.

The test first proves each discrepancy in the unchanged example, removes only
that field in memory, and validates the rest. No fixture is silently repaired.
Some examples also use unrelated resource names between their requests and
responses; runtime fixtures explicitly compose matching identities.

`apimanagement_test.go` composes the 86 service/configuration kinds with explicit
native collections and ETag headers. Association and standalone-gateway tests
compose the remaining 14 kinds from their native LIST/GET/HEAD contracts. Identities and relationships in that scenario are
test data, not an Azure deployment recording. Tests cover ancestor and private
configuration drift, conditional deletion, controller impact review, protected
children, restart and final absence. API revision tests combine logical API
lists, complete native revision indexes and distinct GETs, including relative
`apiId` values, explicit aliases, non-current revisions and paging failures.

Shared references are separate reviewed prerequisites. API revisions, product
and user subscriptions, version sets, links, certificates and loggers have
native dependency checks. Policy XML references include backend, certificate,
fragment, logger, authorization connection and named-value dependencies. Named values resolve through
their native `displayName`; their secrets remain private. An expression-valued
identifier conservatively depends on every current resource in that collection.
External policy XML URLs and expressions are never fetched or executed.

Certificate thumbprints resolve through native certificate LIST/GET responses,
including backend credentials and Service Fabric references. Explicit backend
certificate IDs take precedence over the legacy thumbprint array. Authorization
connections remain scoped to their credential provider; policy expressions
preserve all current matching possibilities. These paths follow the official
[certificate policy](https://learn.microsoft.com/azure/api-management/authentication-certificate-policy)
and [authorization context policy](https://learn.microsoft.com/azure/api-management/get-authorization-context-policy)
contracts.

Key Vault URLs and managed-identity client IDs resolve through subscription ARM
lists and matching resource GETs, including cross-group and cross-region targets.
Unknown bindings retain an unresolved vault origin or client-ID selector; no
secret name, version, content or credential is exported or fetched. Service
hostnames, certificates, named values, policy identities and logger identities
are covered. Logger `SystemAssigned` is treated as the service identity, following
the [native logger examples](https://learn.microsoft.com/azure/api-management/api-management-howto-log-event-hubs).
Private-endpoint targets are checked for identity/configuration drift, locks and
protection, with a second GET before both direct connection deletion and a
reviewed service cascade.

Legacy product/API, product/group, gateway/API, group/user and workspace
group/user associations use native HEAD existence operations plus complete LIST
and matching member GETs. Tag associations use their native GET, whose response
identifies the service-level tag. Their DELETE operations detach only the
association. Member deletion requires reviewed prior unlinks, while parent
cascades review association impacts. Group/user ID-name discrepancies in the
original examples are rejected, not silently rebound.

Notification configurations remain read-only; user/email recipients use native
HEAD and DELETE. Parent recipient indexes must agree with native collections.
Recipient removal can proceed independently through the fixed notification
container, and removing a user first requires removing its subscriptions as a
notification recipient. Portal configuration, revisions, fixed portal settings
and tenant settings/access are read-only native resources included in service
cascade review. Publishing portal revisions block service deletion. Opaque
configuration contents remain in private digests, never exported.

Standalone gateways retain their own region and resource group. Their selected
native GET/DELETE examples use a singular `gateway` ID; the one explicit response
alias preserves the exact subscription, group and resource name while requests
use the declared `gateways` routes. Configuration connections use the exact body
ETag for their required If-Match header, and native LROs can poll the connection
URL itself. Both resource and child absence are required after operation success.
Configuration connections reference a workspace in the same subscription and
region, following the [workspace gateway contract](https://learn.microsoft.com/en-us/azure/api-management/workspaces-overview).
Their native subscription-wide incoming index makes connection removal a
prerequisite for deleting the workspace or service; the shared gateway survives.
The native `services/workspaces` source-ID typo is retained as fixture evidence
and rejected against the declared `service/workspaces` contract.

The service's read-only workspace-link view must agree with the complete gateway
connection index. Native LIST and GET identities, workspace ownership, source
reads and gateway membership are checked across both passes. Removing one
workspace preserves a shared gateway and its other workspace connections.
The service-wide issue view resolves through its explicit `apiId` to the actual
API-owned issue. Both indexes, detail bodies and ETags must agree; inventory and
deletion use the canonical issue resource. Neither view creates duplicate assets
or acquires an invented DELETE. Tests cover pagination, denied/partial reads,
missing members, retargeting, recreation, retention and restarted cleanup.

`operation-coverage.json` audits all 413 GET/HEAD/DELETE operations in 55 pinned
native documents. The catalog selects 309 of those operations. All 94 DELETEs
have an explicit disposition: 92 executable resource cleanup bindings, one
email-template reset retained as read-only, and one excluded soft-delete purge.
The coverage test checks actual catalog routes and executable resource bindings.
The remaining reads provide alternative ETag/tag/group views, runtime reports,
quotas, SKUs, region/network/tenant status, connection capabilities, external
login metadata or retained-service recovery state. These are not additional
independent cleanup resources. Regional operation-result polling covers the
native empty HTTP 200 result and HTTP 202 Location continuation.

Service deletion follows Azure's [48-hour soft-delete retention](https://learn.microsoft.com/en-us/azure/api-management/soft-delete).
No restore, purge, template reset, arbitrary credential-list operation or gateway
data-plane API invocation is offered. These APIM checks do not close the broader
GCP/Azure parity acceptance items or establish live Azure deployment validation.

## Official CLI recordings

`cli-recordings.json` retains 67 response-only records from
[Azure CLI at 8bead7f](https://github.com/Azure/azure-cli/blob/8bead7f93f086629efb160d56c25f508156925bf/src/azure-cli/azure/cli/command_modules/apim/tests/latest/recordings/test_apim_core_service.yaml).
The source recording was made on January 27, 2026, using API `2022-08-01`.
Bodies, response ETags, request IDs and signed polling headers remain unchanged
in the fixture. The extractor excludes credentials, request bodies and key-list
operations and checks the complete upstream file hash:

```sh
python3 providers/azure/fixtures/apimanagement/reproduce_recordings.py /path/to/upstream-recordings
go test ./providers/azure -run '^TestAPIMRecorded' -count=1
```

The replay uses the selected `2024-05-01` routes and changes only the subscription
identity and polling-header version in memory. This is response-shape
compatibility evidence, not a recording of the selected API version. Native
HTTP 200 empty DELETE responses, case-insensitive ARM IDs, omitted non-current
`isCurrent`, ETags and relative revision IDs are exercised. The service DELETE
returns 202, followed by six `InProgress` responses and operation-URL 404. Both
native polling headers carry rotating signatures for the same bound operation.
The replay persists and restores the receipt between waits.

The original CLI often enables `deleteRevisions` or `deleteSubscriptions`.
Steward explicitly sends false and reviews prerequisites separately. Empty
collections and target GET 404s are composed where the recording has only
collection absence; a successful operation or missing operation URL alone never
proves resource deletion.

## Independent emulator

The opt-in test targets the unmodified community
[azure-apim-emulator at a1aafcf](https://github.com/calvinchengx/azure-apim-emulator/tree/a1aafcf2d9743967684d9458a3371dcc86e09ef6).
Its own README describes a first implementation slice with a `2021-08-01` SDK
target, permissive defaults and incomplete live-Azure differential evidence.
It is useful independent implementation evidence, not full Azure conformance.

Build that exact commit using its required Go toolchain and run an ephemeral
loopback instance:

```sh
go build -o /tmp/apim-emulator ./cmd/azure-apim-emulator
/tmp/apim-emulator --disable-auth --disable-tls --addr 127.0.0.1:18085 --data-dir '' --location westus --default-service steward-test
```

From the Steward repository, in another terminal:

```sh
STEWARD_APIM_EMULATOR_URL=http://127.0.0.1:18085 go test ./providers/azure -run '^TestAPIMIndependentEmulator$' -count=1 -v
```

The test creates a uniquely named service and deletes that fixture on exit. Stop
the emulator process after the test. The adapter supplies fixture OAuth,
subscription/resource-group metadata and management locks; every APIM request
goes to the emulator unchanged apart from the loopback origin and Host header.
No Azure credentials or external gateway traffic are used.

Verified behavior includes native service identity via its ETag when optional
`createdAtUtc` is absent, multiple API revisions, parent-scoped inventory paging,
secret-safe named-value inventory, exact conditional subscription deletion and
GET absence. The emulator does not implement the policy collection API. The
test verifies this dependency read failure prevents named-value deletion and
that the value still exists. Unsupported endpoints are never changed to empty
collections. The emulator also routes some child collection names with case
sensitivity; requests use the selected native catalog paths.

## Deletion scope

Service deletion is ordinary active-service deletion. Azure retains the deleted
service for 48 hours under its documented
[soft-delete behavior](https://learn.microsoft.com/en-us/azure/api-management/soft-delete).
Steward does not silently issue permanent purge. No live Azure cleanup or
cloud-wide conformance claim is made by these fixtures or emulator checks.
