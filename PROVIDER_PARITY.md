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

- Azure App Service now adds eight explicit kinds: deployment slots,
  production/slot functions, subscription/application/slot certificates and
  production/slot hostname bindings. Twenty-four new operations and the five
  existing WebApps operations use pinned stable 2025-05-01 native metadata.
  App/slot deletion reviews every modeled child and verifies its final absence;
  retention blocks the controller. Default hostnames require controller cleanup,
  and native function kind governs child discovery. Service plans, Key Vault
  data and external domains remain independent.
- App Service native configuration, private-content digests and parent/root
  context bind cleanup and nested pagination. Full native TLS state and hostname
  reads block certificate deletion when an ID or thumbprint matches, without
  inferring ownership or authorizing unrelated hostname deletion. Missing TLS
  evidence, malformed thumbprints, missing/new children, configuration drift,
  locks, foreign/cyclic/partial pages and failed child reads block cleanup.
  Individual function package/read-only errors are preserved. Persisted async
  operation tests require target-bound receipts and final resource absence.
- Fifteen unchanged native examples have provenance; all ten selected original
  GET/LIST bodies pass independent offline schema checks. Four synthetic
  function/hostname details also validate against native schemas. Eighteen
  unchanged CLI responses use the same selected API version and verify real
  TLS states, omitted unbound fields, slot response type aliases, certificate
  names containing spaces, DELETE 200 and plan retention. Final 404s, selected
  detail bodies and composed child collections are explicitly synthetic.
- A native SiteCertificates parameter pattern rejects legal hyphenated/leading-
  digit application names. The original metadata is preserved; a narrowly
  scoped runtime binding correction follows official site naming rules and is
  independently regression-tested. No actual SiteCertificates call with such a
  name, independent App Service ARM emulator or live deletion is claimed.
- Azure now has 155 rules, 143 native DELETE bindings and 482 operations, with
  79 source and 38 reference documents. A fresh four-document source snapshot
  matches. WebApps replaces its prior source; old common definitions are pruned
  to remaining references and required v5 definitions are added. All unrelated
  documents and prior type entries are unchanged. Repeated catalog generation
  produces SHA-256 `f1aa080ea33f22bea3fdc35792afc67d42d7c1fd39a9d7370ef8a1e7775a2c7f`;
  all 18 recorded-response extractions reproduce identically. Full Go tests,
  Azure/shared-contract race checks, vet, three offline importer tests and
  bilingual documentation checks pass. Remaining mapped families, controller
  gaps and full application/emulator/publication acceptance remain open.
- Azure Redis adds eleven native rules and 35 operations: classic caches,
  policies/assignments, firewall rules, links, patch schedules and private
  endpoint connections; Enterprise/Managed Redis clusters, databases,
  assignments and private endpoint connections. Native independent children
  are reviewed prerequisites; built-in policies require cache cleanup.
- Classic replica cleanup resolves the shared primary unlink from native
  subscription-wide cache/link indexes and verifies both peers. Selecting the
  secondary cache still includes that prerequisite once. Reciprocal views are
  reviewed impacts; retention, duplicate links, inconsistent indexes, missing
  primary views, changed peers and protection block deletion. Enterprise active
  replication verifies all members and their roots, permits sequential healthy
  deletion only with confirmed 404 departures, and checks surviving references
  after target absence. New members, live unlinking and degraded groups require
  a new review or separate recovery; no ForceUnlink action is introduced.
- Frozen public/private configuration and parent/root context protect direct
  actions and nested discovery. Native pagination failures, changing parent
  sets, mismatched identities, incomplete reads and unknown SKUs fail closed.
  Private persistence connection strings are removed from plans and API logs
  while keyed digests detect changes. Native regional/signed operation URLs,
  resource-bound persisted receipts, polling identities/errors and final
  target/peer absence are verified by retained protocol tests.
- Thirty-five original Swagger examples retain their hashes. Independent
  schemas validate 22 original GET/LIST/status bodies and expose two original
  nullability discrepancies; only private copies omit those nulls for secondary
  shape checks. A native Enterprise assignment example incorrectly carries a
  classic ARM identity; runtime rejects it and only the composite scenario
  repairs it. Sixty-one checksum-pinned Microsoft CLI responses reproduce
  byte-for-byte; nine recorded delete flows cover HTTP 200/202, signed polling
  and restart. Empty auxiliary collections, reciprocal/active topology cases
  and final GET 404s are explicitly synthetic. Native Enterprise name lookahead
  handling has separate length/ASCII/hyphen boundary tests.
- Azure now has 166 rules, 154 native DELETE bindings and 517 operations from
  81 source plus 41 reference documents. A fresh eight-document Redis snapshot
  exactly matches the selected source union; unrelated documents and prior
  resource type entries are unchanged. Repeated generation produces SHA-256
  `22c02582bfe05265ae6f5e30d8e13b87ceec794f4c808278d0f928aab24f0857`.
  Full Go tests, Azure/shared-contract/plan/cleanup race checks, vet, three
  offline catalog tests and bilingual documentation validation pass.
  The [Redis evidence](providers/azure/fixtures/redis/README.md) documents source
  versions, recording transformations, lifecycle boundaries and verification
  limits. Remaining mapped services, controller gaps and full application,
  independent-emulator and publication acceptance remain unfinished.
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

- Azure now has 66 rules and 264 native operations. Network Watcher includes
  connection monitors and packet captures, and contributes reviewed deletion
  impacts for all three native child collections. Retained tests cover root-only
  planning, paging, unknown/new/missing members, locks, retention, incarnation
  changes, native deletion, failed operations and recovered child absence checks.
  AKS group discovery includes these native service children. Malformed management
  locks now fail discovery/preflight instead of being ignored. Full Go tests,
  offline source tests and vet pass. SQL/other parent controllers, remaining
  service families and independent/application acceptance are still outstanding.

- Azure SQL logical servers now support native deletion with reviewed database
  and elastic-pool cascades, including the system `master` database. Retaining a
  database blocks its parent deletion; standalone `master` deletion remains
  prohibited. Database creation IDs, child permissions, native SQL operation
  polling and resumed descendant readback have retained protocol tests. Full Go
  tests, offline source tests, vet and targeted races pass. Other controllers,
  remaining families, independent emulators and application acceptance are open.

- Azure now has 86 rules and 276 native operations, adding public/private DNS
  record sets, private endpoint DNS zone groups and private DNS network links.
  Native conditional deletion, system records, metadata protection, exact
  externally managed DNS records and registration-link cascades have permanent
  protocol tests. Tests cover overlapping VNet spaces, foreign/retained impacts,
  source-ID collisions, protected external groups and delayed child readback.
  Parent lifecycle integration, remaining families, independent emulators and
  application acceptance remain open.

- Private Endpoint deletion now includes its native managed NIC and DNS zone
  group/record tree. Live reciprocal ownership, generation, locks, missing/new
  descendants, protected values, retention and resumed per-child absence have
  retained protocol tests. Standalone managed-NIC deletion remains prohibited.
  This enables an existing rule; the catalog still has 86 rules/276 operations.
  VM scale sets, other parent lifecycles, remaining service families and full
  emulator/application acceptance remain unfinished.

- Azure now has 92 rules and 291 native operations. VM scale sets support
  reviewed Uniform instance/disk/network/extension cascades and Flexible VM
  prerequisite deletion. Flexible disk retention flows through direct VM steps
  and native conditional preparation, with serialized restart tests. Native
  response aliases remain restricted to explicitly selected kinds and full IDs.
  Full Go/Python tests, vet and targeted races pass. AKS nested integration,
  Uniform VHD/disk-detachment support, other parent lifecycles, remaining
  families and independent emulator/application acceptance remain open.

- AKS group cleanup now recursively reviews native scale-set, private-endpoint
  and DNS trees, including verified external disks, links and records. Native
  detail reads supplement sparse group-index responses. Frozen scope, current
  membership, creation/generation, permissions, locks and protections have
  retained tests; missing external inventory remains unresolved. Resumed
  completion requires group absence and individual 404s for all known impacts.
  Shared DNS zones/manual records remain separate. Azure and GCP waiters no
  longer report completion alongside readback errors. Full Go/vet and targeted
  races pass. Remaining service families/parent lifecycles and independent
  emulator/application acceptance are still unfinished.

- Azure now has 93 rules/294 native operations, adding standard VM extensions.
  Independent extension deletion and reviewed VM/AKS cascades coexist with
  disk/NIC/public-IP retention. Native retention ETag changes are accepted only
  after the reviewed Delete-to-Detach transition; VM incarnation and complete
  extension/attachment membership remain checked. Retained native wire,
  failure/permission, server wiring and serialized restart tests pass, with
  full Go/Python/vet and targeted race checks. Other families, parent lifecycle
  gaps, independent emulators and application acceptance remain open.

- Azure now has 96 rules/302 native operations. Dedicated-host and reservation
  groups, and Virtual WAN VPN/ExpressRoute gateways, plan native child deletion
  prerequisites. VPN links remain owned by their connection; NAT references
  order connections before rules. Reviewed prerequisite snapshots survive closed
  inventory and worker restart, and every prerequisite must return native 404.
  Parent ETag changes require unchanged configuration/creation identity after
  accounting for the declared child collections. Official unchanged fixtures
  exposed the VPN link response-ID spelling difference, now handled with one
  narrowly bound alias. Full Go/vet, offline source checks and targeted races
  pass. Remaining families, service parents and independent acceptance are open.

- Azure now has 121 rules/372 native operations from 68 official root documents
  and 30 references, adding 25 Service Bus/Event Hubs resource kinds. Native
  namespace/entity cascades include nested subscriptions/rules and read-only
  configuration views, with per-child review, retention blocking and serialized
  absence verification. Explicit response-ID aliases handle only the documented
  final collection spelling; native 200 responses, ancestor identity and type
  checks remain required. Creation identifiers protect standalone actions from
  resource recreation while allowing ordinary updates. Forwarding, Capture,
  identity, network-rule and private-endpoint dependencies are explicit. Retained
  tests cover every added native Get/List/action contract, 24 unchanged official
  examples, child permissions and incomplete lists, drift, locks, external
  preservation and restarts. Full Go/vet, 10 source checks and targeted races
  pass; bilingual Azure docs reflect the current conditions. Active geo-recovery
  unpairing, migration workflows, dedicated Event Hubs clusters, other families
  and independent emulator/application/real-cloud acceptance remain unfinished.

- Azure now binds 373 native operations. Service Bus migration cleanup snapshots
  source/target namespace creation identity and migration configuration, waits
  for synchronization, submits native Revert, verifies the cleared target link,
  and deletes the configuration before its source namespace. Persisted phases
  resume without repeating an acknowledged Revert; the target and copied
  entities remain intact. Missing dependencies cannot become successful target
  absence in the cleanup worker. Official immutable Azure CLI response bodies
  independently establish the state transitions (recorded API 2026-01-01), while
  runtime route tests use the selected 2024-01-01 Swagger. Default namespace
  authorization rules require controller deletion, and standalone entity actions
  re-read replication context before mutation. Full Go/vet, targeted race tests,
  ten offline metadata tests and the upstream recording checksum pass. Automatic
  geo-recovery unpairing, remaining service families and independent application/
  emulator/real-cloud acceptance are still open.

- Incoming Service Bus migrations now use complete native subscription namespace
  and migration lists. Cleaning the target adds the source configuration as an
  authoritative shared prerequisite; selecting both namespaces deletes it once.
  The unselected namespace and its entities remain intact. New incoming copying,
  incomplete lists, changed namespace sets and missing configuration inventory
  cannot authorize deletion. Namespace deletion races yield a retry instead of
  successful absence. Native product inventory preserves both source and target
  references. The generic planner/worker keeps required deletions as independent
  frozen steps, honors retention, rejects cross-connection/partition expansion,
  and authorizes every planned deletion and delegated effect on start/continue.
  SQLite execution/restart tests and native Azure protocol tests cover shared
  ordering, frozen proofs, authorization, target-only cleanup and sibling source
  deletion. Full Go tests, vet and targeted races pass. Geo-recovery unpairing,
  remaining service families and independent acceptance remain unfinished.

- Azure now binds 375 native operations. Service Bus and Event Hubs Geo-DR
  inventory resolves full or bare partner names and verifies reciprocal primary/
  secondary alias views across resource groups and regions. Namespace cleanup
  shares one primary-alias prerequisite; the primary action waits for pending
  replication, issues native BreakPairing, verifies effective unpairing, deletes
  the alias, and confirms both alias/auth views absent. Unselected namespaces
  and entities are retained. Secondary-only alias selection identifies its
  primary controller using the existing managed-resource planning flow.
  Persisted preparation, namespace creation/configuration proofs, protections,
  peer membership and failed/partial reads have retained protocol tests for
  both products. Immutable official CLI recordings show native HTTP 200 followed
  by multiple Accepted reads before effective unpairing; the selected runtime
  routes use Swagger 2024-01-01. Full Go tests, vet, relevant races, offline Azure
  source tests and deterministic catalog regeneration pass. Standalone replicated
  entity cleanup, dedicated Event Hubs clusters, remaining service families and
  independent emulator/application/real-cloud acceptance remain unfinished.

- Azure now has 122 rules and 380 native operations from 70 official root
  documents. Dedicated Event Hubs cluster cleanup reads its native namespace
  membership and singleton quota settings, verifies reciprocal namespace
  associations across resource groups, and plans namespace deletion before
  the cluster. Nested messaging impacts and shared Geo-DR prerequisites reuse
  their native workflows; unselected recovery peers remain intact. Retained
  tests cover original Swagger fixtures, paging, member/configuration drift,
  retention, permission and lock failures, minimum cluster age, LRO failures
  and serialized restoration with complete native absence checks. Full Go
  tests, vet, Azure/shared-contract race tests, offline source tests, deterministic
  regeneration and bilingual documentation checks pass. Remaining service
  families, standalone replicated entities and independent emulator/
  application/real-cloud acceptance are still unfinished.

- GCP now has 149 rules and 616 native methods from 45 Discovery documents and
  one pinned Cloud SDK archive. Dataform repositories, workspaces, release and
  workflow configurations, invocation records and compilation results use native
  regional discovery, complete paging, detail reads, canonical project aliases
  and configuration/parent proofs. Repository plans contain four independently
  deletable child kinds and reviewed compilation-result impacts; every child
  prerequisite must be absent before repository force deletion. Two complete
  membership passes catch changes while reading another child collection.
  Running invocations persist native cancellation, wait for a terminal state,
  delete their record and confirm native absence across serialized restarts.
  External Git, secrets and BigQuery outputs remain separate. Retained synthetic
  wire fixtures exercise both regions, empty/intermediate pages, child-only
  selection, retention, missing/foreign members, drift, malformed replies,
  permission/rate-limit/provider failures and recovery tampering. Native force
  has no atomic membership condition; ReleaseConfig has no creation token, so
  these API limitations are explicitly documented. Full Go tests, vet, relevant
  races, offline source checks, catalog regeneration and bilingual documentation
  checks pass. Dataform folder/team-folder trees, other service families and the
  independent emulator/application/real-cloud acceptance scope remain unfinished.

- GCP now has 151 rules, 146 kinds with native deletion and 624 selected methods.
  Dataform Folder and TeamFolder use their eight additional native methods for
  regional visible-root discovery, recursive content queries, detail reads and
  single-resource deletion. Physical sibling IDs are joined using native parent
  backlinks and inherited team identity. Every nested folder/repository is an
  independent prerequisite; repository descendants reuse cancellation and
  reviewed compilation-result cleanup. Frozen proofs include the complete
  ancestor chain, preventing a moved or recreated ancestor from authorizing
  child writes or resumed cancellation. Native searches are permission-filtered:
  the dedicated nonauthoritative source retains missing observations rather
  than treating visibility loss as deletion. Synthetic wire scenarios cover
  nested/shared roots, paging/cursor drift, cyclic or forged membership,
  partial and failed reads, retention/protection, ordered deletion and restart.
  A SQLite inventory-worker integration uses the real GCP adapter to confirm
  that an empty successful native scan does not close hidden folders. Full Go
  tests, vet, GCP/shared-contract race tests, 12 offline source tests,
  deterministic generation and bilingual documentation checks pass. Independent
  emulator/application/real-cloud acceptance and the remaining Azure/GCP
  resource families and lifecycle gaps are still unfinished.

- GCP now has 153 rules, 147 kinds with native deletion and 631 selected methods
  from 46 Discovery documents and one pinned Cloud SDK archive. Batch Job and
  Task inventory uses native regional discovery, actual embedded TaskGroup
  names, complete paging, detail reads and immutable Job UID/configuration
  proofs. Job cleanup reviews tasks, correlated VMs, automatic disks and
  retained external disks, then uses only native Job deletion. Serialized
  regional operations and fresh native readback verify all reviewed effects,
  including disks left after a VM disappears. Existing data disks and instance
  templates remain intact. Explicit orphan-VM cleanup requires native absence
  of the original Job UID across Batch locations; unreadable ownership blocks
  it. Synthetic protocol tests cover two regions, shared external data,
  pagination and cursor drift, changed or forged membership, protection,
  retention, malformed/failed reads, rate-limit metadata, delayed operations,
  recreation and restart. The real SQLite inventory worker retains existing
  Task observations when parent discovery fails. Full Go tests, vet, relevant
  races, 12 offline source tests, deterministic generation and bilingual
  documentation checks pass. Native Batch deletion has no atomic membership
  condition, and removed Compute correlation labels cannot be reconstructed
  from the Job API; these limits are documented. Ten mapped GCP kinds and
  49 mapped Azure kinds still lack specifications. The remaining lifecycle
  behavior and independent emulator/application/real-cloud acceptance are open.

- GCP now has 158 rules, 151 kinds with native deletion and 646 selected methods
  from 47 Discovery documents and one pinned Cloud SDK archive. Dataproc Cluster,
  Job, NodeGroup, AutoscalingPolicy and WorkflowTemplate use native regional APIs;
  auxiliary groups derive their actual IDs from cluster configuration. Cluster
  cleanup reviews native VM references, managed groups, generated templates,
  automatic disks and retained external disks, then uses only Dataproc deletion
  guarded by the cluster UUID. Job history is retained unless explicitly selected;
  selected active jobs are cancelled and their records deleted before the cluster.
  Historical jobs are matched by cluster UUID, not a reused cluster name. Virtual
  clusters retain their GKE cluster/pools. Standalone Compute cleanup checks for
  the original Dataproc controller. Frozen ancestry, configuration proofs, complete
  membership queries and restarted operation/readback checks reject changed,
  incomplete, protected or unreviewed effects, including remaining orphan disks
  after a VM or the cluster disappears. Native workflow deletion binds its version.
  Synthetic HTTP fixtures cover regional/alias identity, real auxiliary IDs,
  cancellation, retention, pagination/cursor drift, malformed/failed reads,
  operation errors, throttling, recreation and restart. SQLite scan tests preserve
  node-group observations after parent permission loss; shared SQLite cleanup
  tests verify optional direct-child retention and frozen prerequisite restoration.
  Full Go tests, vet, GCP/planner/cleanup/contracts race tests, 12 offline source
  tests, deterministic generation and bilingual documentation checks pass. The
  reviewed official emulator catalog and floci-gcp service list do not list
  Dataproc; these tests are not independent emulator or real-cloud acceptance.
  Native APIs cannot atomically lock cluster/job/Compute membership, and removed
  identity labels/metadata are not always recoverable. Policy/template GETs lack
  immutable incarnation UUIDs, while jobs/policies lack configuration preconditions.
  Nine mapped GCP kinds and 49 mapped Azure kinds still lack specifications;
  remaining lifecycle and independent environment acceptance work stays open.

- GCP now has 171 rules, 162 kinds with native deletion and 703 selected methods
  from 49 Discovery documents and one pinned Cloud SDK archive. Discovery Engine
  adds 13 native kinds: Collection, DataStore, Engine, Schema, Control,
  ServingConfig, Session, Conversation, Assistant, Branch, Document,
  SiteSearchEngine and TargetSite. Inventory uses actual collection/branch IDs,
  both supported app/data-store parent forms, complete paging, native detail
  reads and location-specific US/EU API origins. US/EU appear in region selection
  before CAI has observed resources; unsupported Compute-style regional calls
  are filtered. Collection cleanup orders app and data-store prerequisites.
  Apps retain their linked data stores, and data-store deletion rejects remaining
  app references. Read-only branches/site configuration delegate their reviewed
  descendants to native parent deletion. Child configuration, complete ancestry,
  connector/entity parameters and sitemap metadata are frozen before redaction.
  Document/schema and serving-config/control dependencies enter the shared plan.
  Root or operation absence cannot hide remaining reviewed descendants; serialized
  waits bind resource, location, configuration, impacts and prerequisites and
  recheck replacements. Native data-store/collection waits allow multi-day cleanup.
  Synthetic HTTP cases cover regional aliases, paging/cursor drift, linked-data
  retention, ordered deletion, standalone children, changed/protected/unreviewed
  effects, malformed/failed responses, operation errors, delayed effects and
  restart. A SQLite inventory-worker test preserves observations and ancestry
  after parent permission loss. Logs and stored inventory redact document,
  conversation, prompt, schema, sitemap and connector content, including malformed
  list records. Full Go tests, vet, GCP/planner/cleanup/contracts race tests,
  10 offline source tests, deterministic catalog
  generation and bilingual checks (30 chapters, 10 screenshots) pass. Native
  deletions have no atomic configuration/incarnation condition, and identical
  leaf replacements may lack an observable creation token. Preview agent/runtime
  families and all data-plane records are not individually inventoried. The
  checked official emulator catalog and floci-gcp list do not include Discovery
  Engine; protocol tests are not independent emulator or real-cloud acceptance.
  Seven mapped GCP kinds and 49 mapped Azure kinds still lack specifications;
  the remaining lifecycle and independent environment acceptance work stays open.

- GCP now has 174 rules, 164 kinds with native deletion and 713 selected methods
  from 51 Discovery documents and one pinned Cloud SDK archive. Cloud TPU adds
  native Node and QueuedResource cleanup plus read-only Reservation inventory.
  Native v2 locations.list enumerates zones for both v2 resources and the
  v2alpha1 reservation list; reservation detail uses the complete list because
  the API has no individual GET/DELETE. Queue inventory freezes actual Node IDs,
  incarnation/configuration proofs and retained-disk proofs, including allocated
  single-node names and documented multislice names. Cleanup uses reviewed direct
  Node prerequisites and plain queued-request DELETE. Each Node first detaches
  its existing data disks with native v2 PATCH, verifies attachment removal and
  the retained disk identities, then deletes the Node. The pinned official SDK
  provides independently retained evidence for the data_disks update mask.
  Shared disks remain referenced/retained; network, service-account, CMEK and
  reservation dependencies enter native inventory. Node/queue metadata is hashed
  before redaction in inventory, Invoke and logs. Serialized phases bind the
  reviewed resource/configuration/impacts, validate native LRO scope and metadata,
  and resume state settling, detachment and deletion without replaying completed
  mutations. Missing dependency reads cannot establish root absence; final
  resource reads also reject replacements appearing after an earlier 404.
  Tests cover native scopes/aliases/paging, read-only reservations, exact shared
  plans, retention, ordered mutation, orphaned nodes, incomplete/denied reads,
  changed membership/configuration/UIDs, corrupt operations/phases, expired LROs,
  delayed attachment removal and restart. A SQLite scan-worker test preserves
  observations after queue-read permission loss. Full Go tests and vet pass;
  GCP/planner/cleanup/contracts race checks pass, with all TPU race tests repeated
  after the final absence guard. Ten offline source tests, exact new-source
  selection checks, unchanged prior-source checks, deterministic generation and
  bilingual documentation checks pass. Official emulator and floci-gcp service
  lists do not include TPU; no independent TPU emulator or real-cloud acceptance
  is claimed. Native writes lack atomic configuration/incarnation conditions.
  Disk retention is not a backup; v2alpha1-only per-worker disk attachments and
  Compute/GKE-managed TPU hardware are outside this Cloud TPU API workflow.
  Six mapped GCP kinds and 49 mapped Azure kinds still lack specifications; the
  remaining lifecycle and independent environment acceptance work stays open.

- GCP now has 177 rules, 166 kinds with native deletion and 721 selected methods
  from 53 Discovery documents and one pinned Cloud SDK archive. Data Fusion adds
  native v1 regional instances and DNS peerings plus v1beta1 namespace records,
  with eight official methods and their complete transitive schemas. The root
  uses `data.pipeline`, matching the mapped Alibaba Cloud Logstash resource.
  Earlier source fragments are unchanged and the refresh selection reproduces
  the new method, schema and resource bindings exactly.
- Data Fusion inventory reconciles native list/detail responses, validates full
  project/region/parent identities, reads complete child lists and binds child
  cursors to the instance's creation/configuration proof. Namespace lists request
  the full IAM policy; an embedded policy failure fails a successful HTTP response.
  Configuration proofs are computed before private options, descriptions and
  policy bodies are redacted. Network, PSC, bucket, service-account, key and topic
  references are retained, including explicit shared-VPC/tenant-project identities.
- Instance lifecycle review reconciles two complete DNS/namespace sets and the
  parent proof. The shared solver includes those records in the cascade impact;
  the driver requires the reviewed identities and configurations before issuing
  native `force=true` deletion. DNS supports independent synchronous deletion
  and repeated complete-list readback. Namespace records have no independent
  management-plane delete and are removed with the instance; the adapter never
  enables unrecoverable reset or restarts an instance to delete a namespace.
- Transitional instance states settle before cleanup. Persistent phases bind
  the resource, configuration, reviewed effects and current/initial operation;
  regional LRO name, version, target, verb, cancellation and completion are checked.
  An expired or completed operation cannot prove resource absence. Parent absence
  is rechecked after child reads, including after an initial 404. A child-collection
  404 under a live parent is a dependency error, not successful target cleanup.
- Fifteen Data Fusion test functions exercise the native HTTP boundary, real
  contributor/solver, list-based child reads, independent DNS cleanup, reviewed
  cascades, native state/operation recovery, redaction and more than 100 injected
  failure cases. A SQLite namespace scan preserves observations after an embedded
  IAM error. A regression reproduces a DNS configuration change after its first
  response and verifies that a second complete read blocks deletion.
- Full Go tests, GCP/planner/cleanup/contracts race checks, vet, source-refresh/SDK
  tests, documentation checks and two identical catalog generations pass. The [Data Fusion fixtures](providers/gcp/fixtures/datafusion/README.md)
  retain sources, native contracts and verification limits. Neither the official
  emulator catalog nor floci-gcp lists Data Fusion. CDAP Sandbox supports pipeline
  development but does not exercise these managed Google APIs; no independent
  Data Fusion emulator or real-cloud acceptance is claimed.
- CDAP pipeline/dataset/secure-store inventory and runtime Dataproc lifecycle are
  separate unfinished behavior. Instance deletion retains user data and is not
  evidence that all external runtime/output resources disappeared. These delete
  APIs have no atomic configuration/incarnation condition, and identical same-name
  DNS/namespace replacement cannot be distinguished within one instance.
  Five mapped GCP and 49 mapped Azure kinds still lack specifications; remaining
  lifecycle and independent environment acceptance work stays open.

- GCP now has 179 explicit rules, 167 kinds with native deletion and 725 selected
  methods from 53 Discovery documents and one pinned Cloud SDK archive. Native
  Monitoring MetricsScope and MonitoredProject rules add four methods to the v1
  fragment. Its existing Dashboard methods/schemas and all other source documents
  remain unchanged; source refresh and both resource bindings reproduce exactly.
- Scope inventory reads the complete unpaged native member set twice, preserves
  project-number names, and validates parent identity, creation times, typed
  tombstones, duplicates and completeness. Reverse lookup records incoming scopes
  without extending the connection's mutation boundary. Unexpected continuations
  and malformed/failed reads fail the shard. A SQLite scan verifies that permission
  loss preserves all prior project-link observations.
- The scope has no native DELETE. Steward retains the project-owned default self
  link; independently selected external-project links have native unlink actions
  and depend on their local scope. The actual solver produces one unlink step
  without project/configuration cascade impacts. Both projects must authorize the
  native unlink. Tombstoned links remain visible and independently removable.
- Unlink preflight and readback validate both scope and link creation identities
  through complete reads. Persisted phases bind the selected link, timestamps,
  tombstone value and top-level Monitoring operation. Pending, malformed, failed,
  cancelled, foreign and expired operations cannot replace final absence checks.
  Scope 404 is a dependency failure; a DELETE 404 requires confirmed link absence.
  Native DELETE has no etag/creation-time condition and cannot prevent relinking
  between the final read and mutation. Unlink does not delete projects, metric
  data or monitoring configuration, or prove downstream query caches converged.
- Ten MetricsScope test functions include native schema validation, actual plan
  solving, serialized restart, request IDs, complete reads and more than 75 fault
  cases. Full Go tests, GCP/planner/cleanup/contracts/spec race tests, vet, source
  refresh tests, documentation checks and deterministic generation pass.
- Google's unmodified Config Connector mockgcp at
  `673a61419de1b8e4f7d26070ce20dde2daa61da8` independently passed native project-link
  creation, Steward inventory, unlink, LRO GET, serialized resume and absence over
  local HTTP. The fixture harness and opt-in test are retained in the
  [metrics-scope evidence](providers/gcp/fixtures/metrics-scope/README.md).
  Its missing reverse-list method, IAM behavior and delayed operations still use
  protocol tests; it does not enforce Steward's self-link protection. The
  temporary server was stopped and its checkout/binary removed. No real-cloud
  acceptance or complete Monitoring emulator coverage is claimed.
- Four mapped GCP kinds and 49 mapped Azure kinds still lack specifications.
  Remaining lifecycle behavior, broader monitoring configuration, independent
  environment/end-to-end acceptance and final publication stay open.

- GCP now has 185 explicit rules, 169 kinds with native deletion and 741 selected
  methods from 54 Discovery documents and one pinned Cloud SDK archive. Infra
  Manager adds native regional Config v1 Deployments, Revisions, Resources,
  Previews, ResourceChanges and ResourceDrifts with 16 methods. The selected
  fragment and retained schemas reproduce exactly from the official source;
  all earlier source documents remain unchanged.
- Inventory reconciles native metadata list/detail responses, two complete child
  sets and the root configuration. Child cursors bind their revision/deployment
  identity. Physical ownership requires one current reconciled Terraform record,
  an explicitly supported whole-object type, matching state/CAI identities and
  native product readback. Historical revisions and preview proposals establish
  no physical ownership. Root/latest execution accounts and source buckets remain
  dependencies; Terraform state, source objects and credentials are not exported.
- The actual solver includes controller metadata and native physical cascades.
  Deployment DELETE uses native `force=true`, UUID request IDs and whole-deployment
  `DELETE` or `ABANDON`; preview deletion removes only preview metadata. Retaining
  provisioned resources still deletes controller metadata. Unknown Terraform
  records require explicit ABANDON, partial retention and unsupported options are
  rejected, and locks are never removed. Physical child drivers check their own
  reviewed cascades; preparation-dependent VM/MIG, GKE and TPU flows currently
  block Terraform destruction and remain unfinished composition work.
- Persisted phases handle settling deployments, already-running deletion and
  serialized restart. Config operation target, region, verb, version, cancellation,
  failure and Deployment/Preview response types are validated. Completion requires
  metadata, physical and transitive native readback; a missing root or expired LRO
  does not prove physical cleanup. ABANDON checks retained descendants against the
  reviewed configuration. Immutable identity fields distinguish replacement during
  legitimate deletion transitions where the product exposes them.
- Seventeen Infra Manager test functions cover the native HTTP boundary, actual
  contributor/solver, SQLite scan recovery, serialized actions and more than 100
  fault cases. Sixty-eight source-backed Terraform state-ID/CAI contracts also
  test foreign identities, non-owning types and documented CAI API aliases. These
  verify exact native GET routing, not successful deletion of every mapped product.
  A deployment containing a GKE node pool exercises nested VM/disk deletion and
  retention through the existing native fixtures. Official payload schemas are
  checked by an independent JSON Schema validator.
- Full Go tests, GCP/planner/cleanup/contracts/spec race checks, vet, ten offline
  source tests, documentation checks and deterministic generation pass. Infra
  Manager race tests were repeated after the final CAI alias correction. The
  [Infra Manager evidence](providers/gcp/fixtures/infra-manager/README.md) records
  pinned Discovery/HashiCorp sources, hashes, contracts and verification limits.
  Google's inspected Config Connector mock implements DeploymentGroup, but has no
  Deployment/Preview CRUD handlers; no independent Config deletion emulator or
  real-cloud acceptance is claimed. The APIs provide no atomic configuration
  condition across Terraform's native product mutations.
- The parity map now also names native DeploymentGroup and DeploymentGroupRevision,
  exposing their still-unimplemented deprovisioning workflow. Five mapped GCP kinds
  and 49 mapped Azure kinds lack specifications. Composed preparations, broader
  Terraform mappings, remaining lifecycle behavior, independent environment and
  end-to-end acceptance, and final publication remain open.

- GCP now has 187 explicit rules, 170 kinds with native deletion and 747 selected
  methods from 54 Discovery documents and one pinned Cloud SDK archive. Native
  DeploymentGroup and DeploymentGroupRevision add six Config methods. The updated
  22-method Config fragment and 187 resource bindings reproduce from their source;
  the earlier 16 Config methods/schemas and all 54 other documents are unchanged.
- Group inventory reviews its current deployments and the deployments referenced
  by the last successful group revision, including removed cross-region members
  that native deprovision also deletes. It binds each child's existing Terraform
  and physical-resource manifest, validates native unit DAG order, reconciles two
  complete revision sets and re-reads the group. Unknown revision outcomes and
  ambiguous success history block cleanup. Selecting the latest successful
  snapshot by creation time is an inference from the native revision fields;
  independent deprovision acceptance remains outstanding.
- The actual solver supports whole-group destructive deprovision, ABANDON of all
  physical resources, and retention of all referenced Deployment controllers.
  The first two remove deployment metadata before group metadata. The third uses
  only native group DELETE with `IGNORE_DEPLOYMENT_REFERENCES`, retaining the
  reviewed deployments and descendants. Partial retention is rejected. Existing
  deployment drivers enforce their own Terraform mappings, locks, protection and
  native preparation guards; no direct physical DELETE is substituted for Config.
- Persisted settle/deprovision/delete phases validate operation region, target,
  type, verb, per-unit progress, failure and cancellation. Already-running
  deprovision is observed without repeating the POST. Physical and nested native
  readback gates metadata DELETE and final completion, including after serialized
  restart, expired operations or root disappearance. One new successful
  deprovision revision is accepted only for the reviewed incarnation and DAG,
  with cleared references and a creation time after every reviewed revision.
- Thirteen protocol/schema/scan-worker test functions cover the 22-asset,
  15-impact scenario and more than 100 fault cases. A SQLite scan failure preserves
  all prior group revisions. Full Go tests, GCP/planner/cleanup/contracts/spec race
  tests, vet, ten offline source tests, documentation checks and two deterministic
  catalog generations pass. The [Deployment Group evidence](providers/gcp/fixtures/deployment-group/README.md)
  records request policies, provenance, commands and remaining limits.
- Google's unmodified Config Connector mock at
  `673a61419de1b8e4f7d26070ce20dde2daa61da8` independently passed native group
  creation, GET, metadata DELETE, regional LRO GET, typed response, serialized
  resume and GET absence. This opt-in test forwarded 14 native calls after setup
  and counted 12 explicit read-only list substitutions. Upstream lacks
  location/group/revision LIST and deprovision, so this proves metadata deletion
  only. The harness is retained; its temporary server was stopped and its
  checkout/binary removed. No independent deprovision or real-cloud acceptance
  is claimed, and deprovision has no native request ID or atomic condition.
- Three mapped GCP kinds and 49 mapped Azure kinds still lack specifications.
  Composed VM/MIG/GKE/TPU preparations, broader Terraform mappings, other lifecycle
  behavior, independent environment/end-to-end acceptance and publication remain
  open. The parity rows remain pending; resource-rule counts do not establish
  complete behavioral parity.

- GCP now has 190 explicit rules, 173 kinds with native deletion and 761 selected
  methods from 54 Discovery documents and one pinned Cloud SDK archive. Three
  rules add hierarchical FirewallPolicy and native hierarchical/network policy
  associations. Fourteen new methods cover Compute and Resource Manager. The
  selected fragments and 35 retained schemas match their official sources; all
  earlier 747 generated operations and the other 53 source documents are unchanged.
- Hierarchical policy inventory requires an explicit `firewall_policy_parent`
  organization/folder scope, includes its descendant folders, and verifies live
  ownership and target ancestry. Project access does not imply this authority.
  Global scans use a non-authoritative hierarchy source so changed configuration
  or lost visibility cannot close prior observations. Global/regional network
  policies retain their native project source. Complete lists, detail reads,
  target identities, membership and configuration bind cursor/review snapshots.
- The actual solver creates one native removal prerequisite per association,
  followed by policy DELETE. Independent association removal is also supported.
  Rules and unreviewed association changes block old plans; networks, organizations,
  folders and unrelated policies remain. Native GETs and hierarchical target-side
  association lists verify absence. Persisted native operation phases bind policy
  numeric identity, target, request UUID, actual operation type and reviewed
  configuration through JSON restart, delayed completion and expired LROs.
- Twelve protocol/schema/integration test functions cover all three policy scopes,
  native failures, changed membership/ownership, malformed responses, altered plans,
  retention, operation recovery and independent absence. The actual registry,
  inventory Creator, worker and SQLite test exercises six shards and nine assets,
  preserves observations after denied reads/scope removal, and solves an executable
  persisted plan. This exposed and fixed common inventory projection overwriting
  native names and structured configuration with display metadata. Regressions
  also verify validated `gcp` connection partitions and persisted MetricsScope,
  Deployment, Preview and DeploymentGroup actions.
- Full Go tests, GCP/inventory/planner/cleanup/contracts/spec/runtime race tests,
  vet, 12 offline source/release tests, Web contract and 663 component tests,
  bilingual documentation checks and deterministic catalog generation pass.
  Google's unmodified pinned Config Connector mock independently passed native
  unassociated hierarchical policy creation, GET/DELETE, global organization LRO,
  serialized restart and final GET absence. The run forwarded 14 native calls,
  with two explicit GET-wrapped list shims and 16 Resource Manager fixture reads.
  The server was stopped and its temporary checkout/binary removed.
- The [firewall-policy evidence](providers/gcp/fixtures/firewall-policy/README.md)
  retains contracts, permissions, source hashes, harness and verification limits.
  Upstream lacks association and global/regional network-policy handlers; those
  workflows have protocol coverage, without independent emulator or real-cloud
  acceptance. Native writes have no atomic fingerprint condition; identical
  same-name associations have no creation token. Management-plane absence does
  not prove packet-processing convergence. Cloud NGFW remains a functional mapping
  to Alibaba network ACLs, with different packet-filtering behavior.
- Two mapped GCP kinds and 49 mapped Azure kinds still lack specifications.
  Composed preparations, remaining lifecycle behavior, independent environment
  and end-to-end acceptance, and publication remain open. The parity rows remain
  pending; rule counts alone do not establish complete behavioral parity.

- GCP now has 191 resource rules, 173 kinds with native deletion and 761 selected
  methods. Organization inventory follows the connected project's native parent
  chain and compares two complete Project/Folder/Organization reads. The global
  source preserves prior observations after project moves or denied ancestors.
  Standalone organizations and native `DELETE_REQUESTED` state are represented.
- Organization is read-only: the current public Resource Manager v3 API has no
  organization DELETE. The separately documented standalone-organization console
  lifecycle is not exposed as a provider action. Project ancestry does not grant
  organization-level firewall mutation authority. This records an API capability
  difference from Alibaba ResourceDirectory, rather than claiming cleanup parity.
- Four native protocol/schema/SQLite test functions cover scope and identity
  validation, incomplete/changing ancestry, preserved observations and the actual
  registry/Creator/worker. Full Go tests, vet, focused organization/Invoke race
  tests, documentation checks and two deterministic catalog generations pass.
  All 55 source documents and 761 methods are unchanged. The retained three
  official schemas and verification limits are in the
  [organization evidence](providers/gcp/fixtures/organization/README.md).
  The inspected Google mock has no Organization handler; no independent emulator
  or real-cloud organization acceptance is claimed.
- One mapped GCP kind (Cloud Identity Group) and 49 mapped Azure kinds still lack
  specifications. Remaining lifecycle composition, API capability differences,
  independent environment/end-to-end acceptance and publication stay open.

- GCP now has 193 resource rules, 175 kinds with native deletion and 768 selected
  methods. Cloud Identity Group and Membership use the optional explicit
  customer/identity-source directory, native FULL views and non-authoritative
  visibility observations. Service-account Groups Admin access does not require
  domain-wide delegation. Project ownership does not grant directory authority.
- Reviewed group cleanup includes its native member-link cascade. Independent
  ordinary membership unlink retains the containing group and all member entities;
  locked groups remain protected and dynamic membership management stays native.
  Complete snapshots freeze native IDs, creation/configuration, aliases, roles,
  expiration, security settings and dynamic queries. Scope changes, partial pages,
  unreadable resources, changed plans and surviving native links block completion.
- The cleanup worker now passes its persisted execution receipt to readback using
  a transient non-JSON field. A matched native done receipt can resolve Cloud
  Identity's ambiguous post-delete 403; a permission error alone cannot prove
  absence. OAuth/401/5xx errors and failed, lost or corrupt receipts remain errors.
  SQLite worker restarts between DELETE, wait and readback verify no replay and
  correct group/member tombstones; live survivors cannot succeed.
- Full Go tests, vet, focused races including the native mock and SQLite worker,
  frontend type/contracts and documentation checks pass. All preceding 55 source
  documents remain unchanged. The seven new methods reproduce exactly from the
  official v1 document, and two catalog generations produce SHA-256
  `8b2126219876844c2b13427659fde625a994f47918ad6b405b09f3fe4ab06cfd`.
- Google's pinned mockcloudidentity independently verifies unique IDs, native
  GET/DELETE, done-only responses, membership unlink and ambiguous group absence.
  Its explicit v1beta1 version alias and GET-backed lists are disclosed; its
  missing membership cascade is detected as a survivor, not replaced with false
  absence. The [identity-group evidence](providers/gcp/fixtures/identity-groups/README.md)
  retains 15 native schemas, source hashes, the harness and reproduction commands.
  Native DELETE has no atomic configuration condition. Incoming/cross-directory
  access effects, external product references and real-cloud acceptance remain
  separate coverage.
- No mapped GCP kind now lacks a specification; 49 mapped Azure kinds still do.
  This closes the GCP resource-type registration gap, not all behavior parity.
  Remaining lifecycle composition, API capability differences, independent
  environment/end-to-end acceptance and publication are still open.

- Azure now has 126 explicit rules, 114 kinds with native deletion and 392
  selected operations. Grafana workspace, managed private endpoint, service-side
  connection and integration fabric add 12 operations from pinned 2025-08-01
  Swagger. The new root and three common-type documents reproduce exactly;
  all preceding 100 source documents are unchanged.
- Native parent/child product inventory freezes management configuration and
  creation identity where exposed. Child cursors bind their current parent.
  Two complete native child passes feed the actual solver; all three kinds use
  independent deletion prerequisites before workspace DELETE. Locks, protected
  tags, changed configuration/parents, missing inventory, retention and unreadable
  collections prevent unsafe execution. External data sources, AKS clusters,
  consumer private endpoints and user-assigned identities remain dependencies.
- Microsoft CLI recordings exposed signed ProviderHub operation URLs without
  subscription paths. Grafana polling now accepts only the native provider/region
  shape with a persisted resource binding, validates returned operation/target
  identity, and hides signing parameters in API logs. Ordinary ARM requests remain
  subscription-bound. Serialized resume, failure/cancellation, expired operations
  and final native child/resource absence have retained regression coverage.
- Seven Grafana test functions cover native inventory and planning, independent
  child deletion, pagination and more than 50 fault/variant cases. The 12 original
  Swagger examples are retained, including their invalid IDs/type and connection
  state; tests detect these inconsistencies and use explicit corrected protocol
  identities. Three Microsoft CLI sequences retain 21 native polling responses.
  Their 2023-09-01 version, replaced signing values, synthetic surrounding lists
  and final absence are disclosed in the [Grafana evidence](providers/azure/fixtures/grafana/README.md).
  This is recorded/protocol evidence, not independent emulator or real-cloud
  verification. The inspected localaz emulator has no Grafana handler.
- Full Go tests, focused Azure/cleanup/contracts race checks, vet, three offline
  Azure source-refresh tests and documentation checks pass. Two catalog generations
  produce SHA-256 `0f7a1bce94febfbcdff2a7f203b8b54e67003c626c7b66de35e865231dcd77da`.
  Grafana data-plane objects are not individually inventoried; native DELETE has
  no atomic configuration condition. There are still 48 mapped Azure kinds without
  specifications. Remaining lifecycle composition, independent environment and
  end-to-end acceptance, and publication remain open.

### Azure Monitor data collection progress (acceptance remains open)

- Azure now has 129 explicit rules, 117 native deletion actions and 404 native
  operations from 73 root Swagger documents and 34 reference documents. Three
  data-collection rules bind the official 2024-03-11 API. The new source fragment
  reproduces independently; all prior source values are preserved.
- Inventory combines rule/endpoint reverse indexes, supports native extension
  resourceUri paths and validates linkage, scope, pagination and parent sets.
  A fixed subscription-bound Resource Graph query adds orphan discovery, with
  native GET/ListByResource confirmation and orphan-bound cursors; its eventual
  consistency and resource-level RBAC limits remain explicit.
  Dual-target indexes must agree. Shared required-deletion relationships include
  each reviewed association once; they claim no ownership of monitored resources.
  DCR DELETE fixes deleteAssociations=false. Native configuration, immutable
  creation IDs, locks, managed groups and final prerequisite absence are verified.
  Blob enrichment URL credentials are removed from inventory and API logs.
- Nine retained test functions cover the actual planner, all three native
  actions, retention, serialized recovery, drift and failed/inconsistent
  inventory. Fourteen unchanged Swagger examples and three Microsoft CLI response
  extracts have checksum provenance; native GET payloads pass independent schema
  validation. The earlier recorded API, updated association response and
  synthetic surrounding reads are disclosed in the
  [data collection evidence](providers/azure/fixtures/data-collection/README.md).
- Full Go tests, Azure/plan/cleanup/contracts targeted race tests and vet pass.
  Repeated generation produces catalog SHA-256
  675db1670c8271c0bf3387c09b8dc88345dd9288d848e09ec4dc49c61e00c11e.
  Azure Monitor workspace/default managed-group ownership, remaining mapped
  Azure services, tenant-global association support, independent environment and
  end-to-end acceptance, and publication remain open.

### Azure Monitor workspace progress (acceptance remains open)

- Azure Monitor workspace adds native subscription inventory, detail and deletion
  from the pinned 2023-04-03 API. Azure now has 130 rules, 118 native deletion
  actions and 407 operations from 74 root and 36 reference Swagger documents.
- The two native default-ingestion IDs identify the managed resource group.
  Repeated native membership/detail reads freeze all contained resources,
  including unknown kinds, as reviewed workspace deletion impacts. An optional
  group owner must agree with the workspace and remain unchanged. External
  DCR/DCE associations become shared unlink prerequisites without acquiring
  ownership of their monitored resources or destinations.
- Native configuration and creation identity, locks, protection, missing inventory,
  changed membership and retention block execution. Serialized recovery checks
  workspace, group, every known child and external association before completion.
  Native DELETE removes the entire managed group; no direct group deletion or
  forced cascade is sent. Private endpoint connections remain frozen embedded
  configuration with references to external endpoints.
- Six test functions exercise actual planning, native protocol faults, recorded
  reads, scoped asynchronous deletion, retention and restart readback. Three
  unchanged Swagger examples and two Microsoft CLI responses have reproducible
  provenance. Example pagination/schema and polling-scope inconsistencies are
  explicitly tested. The [workspace evidence](providers/azure/fixtures/monitor-workspace/README.md)
  distinguishes native evidence from synthetic polling/readback and records
  API/version limits; no independent emulator or real-cloud acceptance is claimed.
- Full Go tests, focused Azure/AKS/data-collection/plan/cleanup/catalog/contracts
  race tests and vet pass. The new native fragment reproduces independently and
  preserves all previous sources. Repeated catalog generation produces SHA-256
  `3b9296e651a61aa53c04e733a94d2b675de888aaa7a66547315c33318ef300c6`.
  Remaining mapped services, lifecycle composition, independent environment and
  end-to-end acceptance, and publication remain open.

### Azure Container Instances progress (acceptance remains open)

- Container instance groups now use native 2025-09-01 subscription List, Get and
  Delete. Azure has 131 explicit rules, 119 native deletion actions and 410
  operations from 75 root and 36 reference Swagger documents. The new source
  fragment reproduces independently and preserves all 110 preceding documents.
- Embedded containers and init containers share the native group lifecycle;
  external volumes remain independent. Native subnet, managed-identity and
  supplied Log Analytics resource IDs become references. No external ARM identity
  is guessed from storage names or vault URLs. Native configuration, creation
  identity, ETag, protection, locks and managed ownership are checked before
  deletion. Configuration excludes runtime instance state and assigned IP/FQDN.
- Connection-keyed HMACs bind returned sensitive configuration when ACI supplies
  no native ETag. Commands, environment/config-map values, keys, probe headers,
  extension settings and URL credentials are removed from inventory and logs.
  Sensitive-value drift also blocks AKS managed-group planning and execution;
  credential rotation requires a rescan. Omitted native values and identical
  recreation without an immutable ID remain unobservable.
- Eight retained test functions cover actual planning, native inventory,
  pagination/scope faults, configuration/creation drift, managed-group integration,
  logging, native 200/202 deletion, asynchronous failure and serialized readback.
  Five unchanged Swagger examples pass independent schema validation; three
  original Microsoft CLI responses reproduce byte-for-byte. The older recorded
  API, LIST route transformation and synthetic polling/absence are documented in
  the [Container Instances evidence](providers/azure/fixtures/container-instances/README.md).
  HTTP 200 with an old resource body and successful polling cannot hide a survivor.
- Full Go tests, Azure/AKS/Monitor/plan/cleanup/contracts focused race tests, vet,
  three offline refresh tests and deterministic generation pass. The catalog
  SHA-256 is `d74987d4c53c74e6d52dfc9a99416d7f47f6ad465c231baa97d21ade9d8bdf87`.
  This is native-example, recorded-response and protocol evidence; independent
  emulator/cloud acceptance, remaining mapped services, lifecycle composition,
  end-to-end verification and publication remain open.

- Azure CDN / Front Door now adds fourteen explicit resource rules and 42 native
  operations from pinned 2025-04-15 CDN/AFD Swagger. Profiles select the appropriate
  SKU-specific collections. Native child detail reads, complete repeated lists,
  classic embedded membership and nested profile-bound cursors guard discovery.
  Profile/endpoint/origin-group/rule-set cleanup reviews and verifies its child
  tree. Routes, rules, certificate references and security associations contribute
  shared prerequisites without claiming ownership of their targets. An explicitly
  declared common cascade can cover an internal prerequisite only when both
  resources are already reviewed deletes of the same controller; independent
  cleanup retains separate steps, frozen snapshots and final native readback.
- CDN configuration, optional creation identity, profile context, locks, retention,
  managed ownership and sensitive-value digests are checked before mutation.
  Migrating profiles and active classic default/override origin-group references
  block the applicable independent deletion. Returned rule values and validation
  or signing secrets are not persisted. Repeated reference discovery shares each
  referring collection across targets, avoiding one full route scan per domain.
- The retained CDN evidence includes 42 unchanged examples and independent schema
  checks for all 28 read/list bodies. Ten documented optional-null/enum source
  inconsistencies are asserted before narrowly corrected in-memory validation.
  Seventeen Microsoft CLI responses independently exposed the native CDN
  `error:{code:"None",message:null}` placeholder on pending/successful polls.
  This exact CDN-only case is accepted; real errors and incomplete responses
  remain failures. Signed polling, persisted resource binding, header fallbacks,
  expiration, surviving descendants, actual prerequisite action ordering, scope,
  pagination and ancestor/configuration drift have retained regression tests.
- At this step Azure has 145 resource rules, 133 native DELETE bindings and 452
  operations from 77 root plus 38 reference documents. All 111 preceding source
  documents and 131 preceding type entries are unchanged. A fresh independent
  four-document native import matches; repeated generation and CLI extraction
  are byte-reproducible. Full Go tests, relevant race checks, vet, offline catalog
  refresh tests and bilingual documentation checks pass. These are protocol and
  recorded-response results, not an independent CDN lifecycle emulator or live
  Azure validation. Opt-in batch rule sets described in the August 2026 guidance,
  external WAF/DNS lifecycles and the remaining provider/application acceptance
  are tracked follow-ups; this does not close the overall parity criteria.

- Front Door batch rule sets now use the pinned stable 2025-12-01 RuleSets
  GET/LIST/DELETE contract. Native CLI responses confirm that LIST omits the
  embedded rules and GET supplies them. Inventory preserves the sanitized
  array as rule-set configuration; batch members share the whole rule-set
  lifetime. No individual rule identities, collection reads or DELETEs are
  synthesized. Classic rules bind their parent's configuration and reject a
  parent recreated in batch mode before individual deletion.
- Batch origin-group overrides contribute the entire referring rule set as a
  prerequisite, including its own route prerequisites. Retention and missing
  observations block the affected cleanup. Native schema validation, unchanged
  recorded batch deletion/404 responses, JSON recovery, missing/malformed detail,
  new/changed rules, sensitive content, reference changes and runtime-only status
  changes have retained tests. Empty rule arrays remain valid. These are protocol
  and recorded-response evidence, not independent emulator or live-cloud tests.
- This follow-up keeps 145 rules, 133 native DELETE bindings and 452 operations;
  the source bundle has 78 root plus 38 reference documents. Every unrelated
  source document and resource-type entry is unchanged. A fresh five-document
  native CDN snapshot matches, and repeated generation produces SHA-256
  `eed49659eb8d6b74ea8950128493673d684fa5700fb3b0ec90728099015f4b07`.
  Full Go tests, CDN/shared-contract race checks, vet, offline importer tests and
  bilingual documentation checks pass. External WAF/DNS lifecycles, remaining
  service families and provider/application acceptance are still outstanding.

- Azure now supports both CDN and Front Door WAF policies with six native
  GET/LIST/DELETE bindings from pinned stable 2025-12-01 and 2025-11-01 Swagger.
  Native reverse association indexes and reciprocal referrer reads establish
  explicit deletion prerequisites for CDN endpoints and Front Door security
  policies. These are shared references, not ownership. Retention, incomplete
  indexes, missing observations and unsupported classic Front Door references
  block cleanup. Embedded policy rules share the policy lifetime.
- WAF cleanup allows reviewed reverse-index/ETag changes from unlinking while
  preserving the configuration, location, available creation identity and keyed
  private-content digest. Match values and response bodies are removed from
  inventory/logs. Native final absence, prerequisite readback and operation
  receipts survive JSON restart. Tests also exercise both collection scopes,
  changing RG sets, paging/permission failures, foreign continuations, locks,
  private/configuration drift and synchronous/asynchronous deletion failures.
- Six unchanged examples and four unchanged CLI response bodies are retained
  with provenance and a reproducible extractor. Independent offline schema
  checks document the four original null-field inconsistencies. The CLI uses
  the selected 2025-11-01 API and proves DELETE 204 plus subsequent LIST absence;
  final resource GET 404, reciprocal associations and poll status bodies are
  injected protocol scenarios. The official DELETE example's older
  `frontdoors/.../operationResults` URL is also exercised. No independent WAF
  lifecycle emulator or live Azure deletion is claimed.
- At this step Azure has 147 rules, 135 native DELETE bindings and 458 operations,
  with 79 source and 38 reference documents. A fresh four-document native WAF
  snapshot matches; unrelated documents and existing resource-type entries are
  unchanged. Full regression caught a shared-reference overwrite during snapshot
  assembly; the original definitions were restored before validation passed.
  Repeated catalog generation produces SHA-256
  `eeadd8fd6297e2ab6259e5cc04eb6286cdcce71de82152d7437527cbe0afb94e`.
  Full Go tests, Azure/shared-contract race checks, vet, all three offline
  importer tests and bilingual documentation checks pass. Classic Front Door
  lifecycles, remaining mapped families and full provider/application acceptance
  remain unfinished.
