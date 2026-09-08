# GCP and Azure provider completion

Objective: bring GCP and Azure to the engineering and functional completeness of
the Alibaba Cloud integration, with reproducible API metadata, explicit resource
rules, lifecycle-aware cleanup, and retained automated verification. Passing the
existing tests or preserving the initial 29/34 resource lists is not completion.

## Acceptance criteria

- Map every Alibaba Cloud resource family and its relevant inventory, networking,
  lifecycle, and cleanup behavior to the equivalent GCP and Azure services.
  Platform differences must have concrete API evidence; an unimplemented
  equivalent remains unfinished.
- Keep versioned source metadata and deterministic generated catalogs. Resource
  specifications must identify real provider operations, scopes, fields,
  relationships, and deletion semantics. Validate generation and all specs in CI.
- Discover resources through both broad inventory and appropriate product APIs,
  including omitted child resources. Cover global/regional identities, paging,
  failed scans, live network selection, and authoritative absence correctly.
- Implement controller/member lifecycle contributions, managed ownership,
  cascading deletion impact, retention, and dependency ordering. A blanket
  read-only restriction does not satisfy a supported lifecycle requirement.
- Preserve permission boundaries, explicit credential selection, secret
  sanitization, request IDs, API call logs, retry behavior, cancellation,
  asynchronous operations, and final resource readback.
- Retain provider unit/integration tests, API fixtures, and shared contract tests.
  Exercise supported operations and failure cases, rather than merely asserting
  registry counts or mirroring implementation tables.
- Verify supported services with independent emulators where available and with
  documented protocol fixtures where unavailable. Clearly separate emulator,
  protocol, and real-cloud evidence. Remove temporary running environments and
  generated test data; keep reproducible tests and fixtures.
- Verify end-to-end connection, scan, graph, plan, execution, and reconciliation
  behavior; run Go tests/race/vet and affected frontend checks. Update bilingual
  capability/permission documentation and publish only verified behavior.

## Baseline

At `3b5ba252843fd89974f4ceed648c738c2cba5818`, Alibaba Cloud has 159 YAML
specifications and 41 provider test files; AWS has 45 and 5 respectively. GCP has
29 resource definitions and Azure 34, constructed in Go, with no provider test
files. Their catalogs are constructed at runtime and neither provider is wired
into lifecycle contribution discovery. Both omit cloud request IDs from their
inventory/action diagnostics.

## Work and evidence

1. [ ] Build the service/behavior parity matrix and verify official API sources.
2. [ ] Implement catalog generation and explicit, validated specifications.
3. [ ] Complete inventory, resource properties, and network relationships.
4. [ ] Complete ownership, controller cleanup, retention, and deletion ordering.
5. [ ] Complete transport diagnostics, retry/error behavior, and credentials.
6. [ ] Retain comprehensive provider and shared-contract regression tests.
7. [ ] Run independent emulator and full application end-to-end verification.
8. [ ] Update documentation, audit every criterion, publish, and verify production.

No item is complete until its corresponding source and test/runtime evidence
prove the behavior. Temporary mock results from the initial integrations are
background evidence only, not acceptance evidence for this work.

### Foundation progress (incomplete acceptance scope)

- Native Google Discovery and Azure ARM Swagger importers now retain official
  operation IDs, call metadata, paging, source URLs, and source fingerprints.
  Google and Azure source-fragment regression fixtures check both importers.
- GCP now loads 30 explicit YAML specs and a deterministic catalog containing 132
  official operations from 10 upstream API documents. Runtime resource reads and
  deletes bind catalog paths; the generic Invoke boundary is implemented. The
  regional backend service kind and its dependency references are included.
- GCP has retained catalog, OAuth, transport, inventory, and deletion tests,
  including signed-credential selection, token caching/cancellation, snapshot
  pagination, project scans, request IDs, log/response redaction, operation
  polling, and final absence readback. These are protocol tests, not independent
  emulator or real-cloud evidence.
- `go test ./...` and targeted race tests passed after the initial migration.
  This does not close the acceptance items: Azure runtime migration, complete
  service mapping/coverage, product discovery, lifecycle contributions, shared
  contracts, independent emulators, full end-to-end verification, documentation,
  and publication remain outstanding.
- Azure now uses 34 explicit YAML specs and 167 native operations from 33
  versioned ARM Swagger documents, with 27 transitive reference documents.
  Every resource's read/delete binding is exercised. The transport retains
  request IDs, sanitizes API logs and results, separates ARM/Storage OAuth
  audiences, and binds token refresh to the current request's cancellation.
- Azure retained tests cover subscription-wide and regional scans, child
  discovery, VM network closure, official VM/public-IP examples, pagination
  boundaries, inherited locks, protected ownership, Blob versions and snapshots,
  Storage account children, all three LRO headers, final absence, and operation
  URL ownership. These remain protocol evidence. The initial read-only controller
  restrictions and auto-delete restrictions still need lifecycle implementation.
- GCP now has 34 explicit rules and 155 official methods from 11 API documents.
  Known kinds use native product lists, including Compute aggregate scopes,
  separate Secret Manager endpoints, and recursive KMS parents. Product cursors
  reject changed scopes, parent sets and token cycles; incomplete responses fail
  their shard. Live network selection uses Compute directly.
- Network scans now calculate membership across all product shards, include
  global network resources, retain source authority, and preserve observations
  when a failed source makes the network closure incomplete. Retrying a failed
  network target includes its successful sources to rebuild the whole closure.
  Retained application tests cover cross-kind chains, partial failure, retry and
  protection against false absence during inventory-source migration.
- Full Go tests, targeted GCP/inventory/catalog/spec race tests and vet passed for
  this product-discovery step. Coverage is still incomplete: the service matrix,
  remaining GCP/Azure families, lifecycle controllers and independent emulator
  acceptance are not closed by these protocol tests.
- GCP instance disks and Azure VM/disk/NIC/public-IP attachments now contribute
  authoritative lifecycle bindings. Automatic deletion appears in the plan;
  retained attachments are ordered after their controller for separate cleanup.
  Missing attachments remain unresolved and cannot authorize a cascade.
- The executor supplies reviewed impact outcomes and immutable planned assets
  to provider drivers. Retaining a controller propagates to its descendants.
  Live attachment identities/policies must match this snapshot before deletion,
  including when inventory changes during a running cleanup.
- Native retention changes run as persisted, resumable action phases. GCP uses
  `setDiskAutoDelete` and disables instance deletion protection before deletion.
  Azure updates VM deletion options and NIC/public-IP deletion options, checks
  live child locks/ownership, and preserves NIC settings. Both wait for operation
  completion and retention readback before deleting the controller.
- Retained tests cover boot/data/regional disks, nested public-IP ownership,
  explicit/native-ID retention, controller retention inheritance, worker
  restarts, delayed readback, mutation failures, permission/lock changes, missing
  plan impacts and configuration drift. Azure retention bodies also validate
  against the checked-in official Swagger schemas without network access. The
  common lifecycle contract now covers all four providers; server and executor
  tests verify lifecycle wiring and immutable inputs. Cluster controllers,
  remaining service families, bucket draining and emulator acceptance remain
  unfinished.
- Full Go tests, targeted GCP/Azure/cleanup/plan/contracts/server race tests and
  vet passed after the attachment lifecycle and immutable-plan changes.
- AKS has native deletion and an authoritative node-resource-group contributor.
  Its impact plan includes unknown resource kinds and known nested resources;
  live group membership, locks, tags, identity/ownership and permissions are
  checked before deletion. Unsupported retention is blocked during planning.
  Controller readback must verify the complete node group is absent before the
  executor closes contained assets without independent resource drivers.
- GCP/Azure preflight errors now distinguish a missing dependent collection from
  a missing deletion target. Retained tests cover this boundary, native AKS
  paging, new resources, missing/foreign impacts, delayed node group deletion,
  failed operations and restart behavior. AKS external attachment retention,
  other controllers, GCP cluster lifecycle and the broader acceptance scope
  remain unfinished.
- GCP now includes zonal/regional managed instance groups, InstanceGroups and
  autoscalers. Native member discovery records stateful disk/IP policy and shared
  read-only ownership. Cleanup prepares retention through abandonment, native
  policy updates and verified manual application when needed. It checks frozen
  resource incarnations, member/policy drift, pagination completeness, protected
  labels and operation failures. Complementary InstanceGroup readback is required.
  Retained tests cover native product lists, plan ordering and retention,
  automatic/manual state application, stale configuration fingerprints, operation
  completion before effective state, cross-zone polling and resumed execution.
  These remain protocol tests; GKE, other controllers and broader service/emulator
  acceptance are still outstanding.
- GKE node pools now use native inventory and delete operations, cluster UID and
  node-pool etag checks, and authoritative instance-group ownership. Standard
  pools are separate cleanup steps; Autopilot pools require cluster cleanup.
  Native GKE operation polling and descendant absence checks survive restarts.
  Persistent volumes are retained according to live attachment policy; native
  node/boot-disk retention restrictions are expressed in the plan. Direct MIG
  deletion checks GKE ownership. Retained protocol tests cover these paths and
  their failure cases. Cluster network/load-balancer impacts and native cluster
  deletion remain incomplete, as does the full parity/emulator acceptance scope.

- GKE cluster cleanup now records a Kubernetes network snapshot and native
  Compute impacts, waits for Gateway/Ingress/Service finalizers, and cleans only
  planned leftovers before/after native cluster deletion as appropriate.
  Real local TLS/OAuth tests cover certificate and endpoint validation, native
  API bodies, paging, incarnation/configuration drift, finalizers, generated
  certificates, NEGs, zero-node template ownership, protected resources, retained
  IPs/certificates, operation failure and serialized recovery. The current GCP
  bundle has 42 specs and 200 selected methods. Full Go tests passed for this
  step. Shared-VPC host and multi-cluster/custom-controller coverage, broader
  service families, independent emulators and final publication remain open.

- The GCP catalog now contains 100 explicit resource specs and 393 native
  methods from 30 official documents. Nineteen Compute rules and 39 managed
  service rules extend coverage across networking, capacity, databases, identity,
  DNS, API gateways, service discovery and observability. Retained wire tests
  cover every added type, relevant regional/global variants, DNS composite names,
  BigQuery scalar/project/parent identity, Firestore default names, soft deletion,
  partial lists, parent generations and secret-bearing service settings.
- Bigtable, Spanner, AlloyDB, Kafka and Service Directory have reviewed native
  child cascades, live list/read validation, protected/retained-child checks and
  descendant readback after parent absence. The server resolves these contributors
  through the explicit GCP connection. Nested plan and serialized-recovery tests
  are retained. Remaining child kinds, other service controllers, bucket draining,
  the remaining service matrix, Azure expansion, independent emulators and full
  application acceptance remain unfinished. No publication is implied by this
  intermediate implementation checkpoint.

- GCP now has 127 explicit kinds, 552 native methods and 44 upstream documents.
  Twenty-seven additional rules include application metadata, AI endpoints,
  backup plans/vaults/sources/backups, data lakes/replication, DLP, domain
  registration, IAP and network services. Retained tests exercise each native
  path, custom POST deletion, terminal states, retention/lock expiry, dependency
  formats, numeric projects, location paging/fanout/drift and DLP key redaction.
  Product location lists supplement Compute where available; metadata refresh
  has bounded retries with offline tests in CI. Parent prerequisites and cascades
  for these additional products, remaining mapped services, Azure expansion and
  independent/full-application acceptance remain outstanding.

- GCP now has 143 kinds and 597 methods. The official, checksum-pinned Cloud SDK
  supplies 36 Media CDN/Multicast methods missing from anonymous Discovery; an
  offline AST conversion preserves native paths, message types, maps and enums
  and records distinct SDK provenance. Twelve service rules plus internal ranges
  and NCC managed groups/tables/routes have retained wire/dependency tests. NCC
  Hub cleanup verifies reviewed nested intrinsic resources, including root-only
  selection and recovery after parent absence. Network scans now include global
  product locations for global-only and mixed-scope services. Other service
  lifecycle work, remaining families, Azure expansion and emulator/E2E acceptance
  are still open.

- Azure's 34 current kinds now use native product lists, including recursive
  Subnet, Blob container and SQL child discovery. Per-kind product shards are
  authoritative; the broad ARM index preserves unknown kinds without replacing
  product observations. Live details, inherited locks and managed ownership are
  retained. Parent-bound cursors and readback reject drift, missing or unreadable
  collections, invalid identities, partial responses and repeated pages. Network
  selection uses VNet/Subnet product APIs and network scans include global
  bindings. Retained protocol tests, offline source-refresh tests, full Go tests,
  targeted race tests and vet pass; 33 official API files and 27 reference files
  refreshed successfully. Remaining Azure families, lifecycle controllers and
  independent emulator/application acceptance remain open.

- Azure now contains 64 explicit rules and 258 native operations from 49 root
  Swagger documents and 25 reference documents. Thirty additions cover network,
  DNS, capacity/hosts and file shares, with literal native CRUD wire tests,
  multi-level parent parameters, typed properties and dependency references.
  Native optional `type` fields are supported without accepting foreign IDs,
  wrong types or partial responses; resource-group read identity is also checked.
  File-share deletion preserves snapshots with the native `$include=none`
  option. Network secret redaction, full Go tests, targeted races, vet and source
  regeneration passed. The remaining service families, parent lifecycle work,
  snapshots, emulator/application acceptance and publication remain unfinished.
