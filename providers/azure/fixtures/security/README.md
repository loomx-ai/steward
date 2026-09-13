# Defender plan state evidence

The native pricing API reports protection-plan enablement, sub-plan, trial time,
extensions, inheritance and resource coverage. `Standard` at subscription scope
does not establish coverage for every resource. This models the service state
queried by Alibaba Cloud's read-only `DescribeVersionConfig` baseline. It does
not implement security policy management, protection-tier changes or resource
override removal. A pricing DELETE removes an override on a supported resource;
it is not a subscription-level security-service deprovision operation.

Sources are pinned to Azure REST specifications commit
`c20bf553ad64f20c6d5e3f56080380c086cb1fde`:

- [pricings.json](https://github.com/Azure/azure-rest-api-specs/blob/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/security/resource-manager/Microsoft.Security/Security/stable/2024-01-01/pricings.json),
  API `2024-01-01`, SHA-256
  `5031087fb252e7243e0988d131da745ee755ec4e4ec2c7534573289ccb05be14`.
- [HybridCompute.json](https://github.com/Azure/azure-rest-api-specs/blob/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/hybridcompute/resource-manager/Microsoft.HybridCompute/HybridCompute/stable/2025-01-13/HybridCompute.json),
  supporting machine GET/List, API `2025-01-13`, SHA-256
  `eecab3d393ce57ef584391b028c109e7066829d4e2bc3685e8e2918ade7cde66`.

The catalog retains all four Pricing operations and two supporting Arc reads,
without adding a cleanup action or an Arc machine inventory rule. Every prior
catalog operation is unchanged. `sources.json` pins 18 unchanged official
examples with 26 responses and 22 response bodies. Offline tests bind requests
and validate the bodies, with explicit assertions for these native discrepancies:

- The filtered List example uses `$Filter`; the operation declares `$filter`.
  Tests first require rejection, then bind the documented spelling. Inventory
  requests the complete list and does not send a plan filter.
- Eight response bodies contain `inheritedFrom: null` with `inherited: False`,
  despite the non-nullable string schema. Tests assert the original failure,
  remove only that field in memory and validate the remaining body. Runtime
  accepts absent/null inheritance for an explicit override.
- The List/Get descriptions name VM, VMSS and Arc scopes. Current examples also
  demonstrate Containers GET/PUT/DELETE on AKS and PUT on ACR. Inventory uses
  the named Containers GET on AKS/ACR rather than inventing a container-scope
  LIST contract. ACR GET support is inferred from its resource configuration
  and generic GET route; there is no retained ACR GET recording or live evidence.

Native parent lists support pagination; version, host, collection and token-only
queries are checked. Every indexed plan gets its own read. Known plan and parent
list omissions are repaired with native reads, including subscription plans
explicitly named by `inheritedFrom`. Parent disappearance never proves a known
plan absent. Repeated snapshots and serialized pagination bind connection, scope,
kind, references and configuration; trial countdown is excluded from change
fingerprints. These reads are not an atomic cloud snapshot.

Extension parameters, arbitrary future settings and operation messages stay
private; native operation status codes remain visible. Resource and inheritance
references are signed and contribute `uses` edges without ownership or cleanup
prerequisites. A real SQLite global scan and graph worker test verifies seven
native plan assets, generated IDs, five inheritance edges, persisted read-only
capabilities, list omissions, a failed permission scan and own-404 reconciliation.
The application network-closure check preserves the selected resource's plan
without including unrelated subscription plans.

Run:

```sh
go test ./providers/azure -run '^TestDefender' -count=1
go test -race ./providers/azure -run '^TestDefender' -count=1
go generate ./providers/azure
```

The [Floci-AZ service list](https://floci.io/floci-az/services/) checked on
2026-09-13 does not list Defender plan emulation. Its
[generic ARM fallback](https://floci.io/floci-az/services/arm/) does not establish
plan inheritance, effective coverage or trial behavior. These fixtures and
workers are protocol evidence, not independent emulator or live-cloud acceptance.
