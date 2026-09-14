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

- Azure AI Search adds four rules and eleven selected native operations from
  stable 2025-05-01: service, private endpoint connection, shared private link,
  and read-only network security perimeter configuration views. Product lists
  inherit parent location and bind cursors to native service context. Parent
  cleanup requires reviewed connection/link deletions and verifies every view's
  final absence, including after serialization and worker restart. Retaining a
  prerequisite or managed view blocks parent cleanup.
- Shared-link cleanup binds the actual target native API in the selected
  subscription and checks its configuration, optional region, protected tags,
  inherited locks and managed-group ownership before a final target re-read.
  This follows Microsoft's documented target-provider metadata update; the data
  resource is not an owned deletion impact. Unknown target APIs, including
  still-unimplemented Cosmos DB and Cognitive Services, and cross-subscription
  links remain gaps. External network perimeter association lifecycles are also
  unfinished; the read-only Search view does not authorize their deletion.
- Eleven unchanged official examples retain provenance and eight independent
  native schema checks. Four responses pass directly and four expose documented
  optional null/non-nullable discrepancies. Nineteen responses from three pinned
  Microsoft CLI recordings include native final GET 404s and signed asynchronous
  polling. The root recording uses 2025-05-01; child recordings use 2022-09-01 and
  are explicitly version-bridged to selected native 2025-05-01 requests. Empty
  supporting collections and Storage target reads are synthetic. The
  [Search evidence](providers/azure/fixtures/search/README.md) records those limits.
- The Azure catalog now has 170 rules, 157 native DELETE bindings and 528
  operations from 82 source plus 42 reference documents. A fresh four-document
  Search snapshot matches; unrelated documents and earlier type entries remain
  unchanged. Repeated generation produces SHA-256
  `7a140ea99b601c3f7ca989b37dad3fbfbdb737f738cceb344acca296c42e67f5`;
  two independent extractions reproduce the checked-in recordings byte-for-byte.
  Full Go tests, Azure/catalog/shared-contract/plan/cleanup race checks, vet,
  three offline importer tests and bilingual documentation checks pass.
  These are protocol/schema/recording results, not independent ARM emulator,
  live-cloud or full application acceptance. Remaining mapped resource families,
  data-plane parity and provider/application verification are still open.

### Azure Cognitive Services / Foundry progress (acceptance remains open)

- Twenty-three rules select 67 native operations from the pinned stable
  2026-05-01 API. Accounts, projects, applications, agent deployments, capability
  hosts, connections, model deployments, content-filter resources, managed
  networks and shared/local commitment plans have explicit product discovery.
  Both connection lists include datastores. Native locations, every ancestor,
  private configuration, creation identifiers and scoped references remain
  bound through pagination and execution. Malformed references fail closed.
- Account cleanup requires reviewed deployment and child deletions before
  soft deletion. Capability hosts wait for dependent projects/applications;
  connections wait for referring hosts. Key Vault connections wait for all
  other account/project connections. Content-policy, deployment and shared-plan
  associations are prerequisites, not inferred exclusive ownership. Native
  indexes reconcile against two complete collection reads, while reviewed child
  departures can update parent indexes without permitting configuration drift.
- Managed-network deletion reviews its outbound-rule impacts and protects
  referenced private-endpoint targets through their actual native API, inherited
  locks, managed-group ownership and final re-read. Every managed view and child
  must independently become absent. A required/active connection private
  endpoint, unknown connection network state, or unresolved dependent rule
  lifecycle blocks the affected cleanup. External target resources remain
  separate. The previously unknown Search Cognitive Services target now has a
  native API binding; Cosmos DB and Key Vault target support remains unfinished.
- Sixty-seven unchanged official examples retain source hashes; all 46 native
  GET/LIST bodies pass independent offline schema validation. The evidence
  documents inconsistent example request/response identities. Thirty-one
  unchanged Microsoft CLI responses replay seven native DELETE paths; most
  recordings use a newer preview and are explicitly version-bridged. Final
  target GET 404s and supporting collections are labeled synthetic. Tests also
  cover restart, asynchronous failures, secret sanitization, paging, retained
  resources, deep-ancestor drift and incomplete final absence. No independent
  Foundry emulator or live Azure deletion is claimed.
- Azure now has 193 rules, 178 native DELETE bindings and 595 operations from
  83 root plus 42 reference documents. A fresh three-document native import
  matches, with earlier source/type entries preserved. Catalog generation is
  byte-identical twice (SHA-256
  `f9f06966e6e466de9a685c68213ebaeb075b6b5ac05665785bc3316d84d3373e`),
  as are two CLI recording extractions. Full Go tests, Azure/catalog/shared
  contract/plan/cleanup race checks, vet, three offline importer tests and
  bilingual documentation checks pass. Legacy account-kind applicability,
  connection endpoint effects, external perimeter/RAI services, agent data-plane
  cleanup and remaining provider/application acceptance are still open. See
  [the Cognitive Services evidence](providers/azure/fixtures/cognitive/README.md).

### Azure Cosmos DB progress (acceptance remains open)

- Thirty-four rules select 111 operations from stable 2026-03-15, covering the
  five account APIs, JavaScript resources, client encryption keys, API roles,
  services/notebooks/private connections, managed Cassandra and Fleet resources.
  Native kind/capability checks select applicable lists. Account/cluster metadata
  is global; managed Cassandra data centers use their actual deployment region.
  Regional network scans retain global parents and explicit subnet/VNet links.
- Native requests retain case-sensitive data-resource names and explicit response
  aliases. Cross-page case collisions fail the scan. Credential-keyed selectors,
  ancestor/configuration/throughput bindings and operation receipts survive
  serialization/restart. Changed selectors are rejected before polling or absent
  readback. The existing bounded cursor fails closed for oversized continuations.
- Independently deletable children precede their parents; built-in roles and
  client encryption keys require reviewed controller cleanup and independent final
  absence. Role assignments, MongoDB inheritance/resident principals and Fleet
  associations are explicit shared prerequisites. Fleet unlinking preserves
  accounts and checks target protection, inherited locks, managed groups and a
  final re-read. Search links now have a tested native Cosmos account target API.
- Dedicated throughput reads bind RU/s/autoscale settings and distinguish verified
  no-offer 404s from incomplete reads. Pending offers, backup migration, unknown
  API markers and transitioning resources block affected cleanup. Two complete
  child reads reconcile account endpoint indexes and private configuration.
  Data-write clocks are excluded; large JSON integers retain precision. Script
  bodies, wrapped keys and Cassandra secrets/configuration stay out of inventory
  and logs while private changes still invalidate review.
- Tests retain 117 unchanged official examples and 81 independent native schema
  checks. Two trigger examples expose their original out-of-enum placeholders;
  only separate test copies are corrected. Seventeen immutable Azure CLI files
  supply 386 responses and 24 native asynchronous DELETE replay cases, all at the
  selected 2026-03-15 API. Supporting collections and final GET 404s are explicitly
  synthetic. The [Cosmos evidence](providers/azure/fixtures/cosmos/README.md)
  documents these boundaries, signed polling, retention, concurrent change,
  partial/denied responses, protected targets and incomplete final readback.
- A new regression caught case-sensitive management-lock matching; the common
  matcher now normalizes every supplied ARM ID. Additional regressions caught
  partial DELETE acceptance and missing selector validation before resumed polls.
  All are fixed and covered through actual action paths.
- Azure now has 227 rules, 211 native DELETE bindings and 706 operations from
  84 root plus 42 reference documents. A fresh two-document Cosmos snapshot
  matches, preserving all earlier 193 type entries and 125 documents' content.
  Two generations are byte-identical, SHA-256
  `aa8858e46809840c952fe3a9635e170b14ce390733f36f9b217584b40a3712a0`.
  Two independent CLI extractions reproduce SHA-256
  `8dbf274d5a27051e936a6bbd7ed1186da211cbd3e899adf0e2e6b9ce047d5cf8`.
  Full Go tests, Azure/catalog/contracts/plan/cleanup race checks, vet, three
  offline importer suites and bilingual documentation checks pass.
- Microsoft's Cosmos emulator and reviewed Floci-AZ 0.12.0 data handlers do not
  establish independent ARM verification. No live Azure mutation is claimed.
  Separate MongoDB vCore/PostgreSQL, backup/restore inventory, data-plane parity,
  remaining mapped services and full provider/application acceptance stay open.

### Azure DocumentDB / MongoDB vCore progress (acceptance remains open)

- Added four native 2026-06-01 rules: clusters, firewall rules, private endpoint
  connections and Microsoft Entra user registrations. Fourteen selected
  operations include subscription/resource-group cluster lists and the native
  replica index. This management API is independent of Cosmos DB accounts.
- Replica membership must agree between the source index and the replica's
  current source/role. Reviewed replica deletion precedes source deletion through
  a required dependency, without declaring exclusive ownership. Deleting a
  replica preserves its source, including a protected source. Independent proxy
  resources have reviewed DELETE prerequisites; restored clusters and original
  creation parameters do not establish current replica ownership.
- Complete native child walks, private-endpoint indexes, live detail reads and
  repeated parent/configuration checks reject membership changes, foreign or
  duplicate resources, partial results and unreadable dependencies. Private
  configuration binds the resource and its parent while preserving large JSON
  integers. Backup restore clocks and operation states are handled separately.
  Cross-page duplicate detection now applies to all Azure product inventory,
  retaining the existing 128 KiB bounded cursor behavior.
- Fourteen unchanged Microsoft examples and their source hashes are retained;
  ten GET/LIST bodies validate against the original schemas offline. The native
  CLI's `User` principal type disagrees with Swagger's `user`; an explicit test
  requires that discrepancy and validates only a separate in-memory correction.
  Snapshot extraction now retains discriminator subtypes across known source
  documents, including transitive inheritance and later-discovered dependencies.
  A catalog-name collision with Cosmos private-endpoint GET/DELETE is resolved
  with official document titles; original operation names and schemas remain
  unchanged. The importer rejects normalized-title collisions deterministically.
- Ten official CLI extension recordings at commit
  `0349eb646d3225db5fd677114e200efdfd11e3f8` provide 483 selected response bodies.
  Fourteen native 202 DELETE/LRO cases use the exact selected API version and
  retain signed polling URLs through restart. Original GETs are replayed as
  preflight state; supporting empty indexes, resource groups, locks and final
  GET 404s are explicitly synthetic. The [DocumentDB evidence](providers/azure/fixtures/mongocluster/README.md)
  describes the source boundary and principal-type mismatch.
- Boundary tests cover independent/protected replicas, direct-child retention,
  unreviewed and unreadable prerequisites, parent/private/configuration changes,
  duplicate and invalid replica indexes, pagination changes, pending topology,
  partial DELETE/poll/readback responses, denied reads, failed/canceled/expired
  operations and changed operation/request identities before HTTP. Native LRO
  success cannot complete while the resource still exists. Signed polling
  material and database connection credentials do not enter logs or inventory.
- Azure now has 231 rules, 215 native DELETE bindings and 720 operations from
  85 root plus 44 reference documents. A fresh four-document snapshot matched;
  all 126 previous source documents stayed unchanged apart from additive shared
  definitions, and all 227 earlier types were retained. Two Cosmos operation
  binding names acquired their required document-title qualifier.
  Generated catalog SHA-256:
  `58852fc6fe9a6feca7697accfbc49a22aea0f3aea376b2f6c864600ed49964de`.
  CLI fixture SHA-256:
  `905ce84dd7b00e5738b3933901cd86a586f009e697b8a7d0003ca3df14bfad95`.
  Two independent extractions reproduced the CLI hash; repeated generation was
  deterministic. Full Go tests, Azure/catalog/contracts/plan/cleanup race tests,
  `go vet`, seven Azure/GCP snapshot-import tests, four SDK metadata tests and the
  bilingual documentation check passed.
- Reviewed Floci-AZ 0.12.0 source at commit
  `f6f0292880c6eb4e7fe3d658185030665f98166e` has no matching DocumentDB management
  routes in 331 Java files. Its MongoDB sidecar and Amazon DocumentDB emulator
  are not Azure ARM acceptance. No live cloud mutation was performed. Databases,
  documents, local MongoDB users, extra role/ownership cleanup, retained backups,
  restore and purge are not separate implemented lifecycles. Native DELETE has
  no conditional ETag parameter; concurrent final read/delete races and
  independent end-to-end cloud acceptance remain open, alongside the rest of
  GCP/Azure behavior parity. No provider acceptance box is closed by this batch.

### Azure Data Explorer / Kusto progress (acceptance remains open)

- Added ten native 2025-02-14 rules for clusters, databases, follower attachments,
  data connections, database/cluster principal assignments, scripts, managed
  private endpoints, private endpoint connections and custom sandbox images.
  Thirty-six operations include native child collections, the GET follower
  index, original principal/list alternatives and regional operation results.
- Full child/detail walks, parent rereads and native indexes bind each reviewed
  cascade or prerequisite. A source's follower index must agree with the actual
  foreign attachment. Reviewed attachment deletion precedes source deletion
  without owning the follower cluster. Attachments control only matching local
  read-only database views; source, selector, prefix/override, sharing and native
  names must agree. Wildcard attachments review all matching views. Retention,
  unreviewed members and contradictory or inaccessible indexes block cleanup.
- Read-only database selection identifies its attachment controller. Active
  custom images require cluster cleanup and independent final absence. Other
  reviewed child DELETEs precede their parent. Script registration deletion does
  not roll back executed KQL. Cluster soft delete is not rollback for earlier
  database deletions, and no soft-delete opt-out, restore or purge is offered.
- Native/proxy region handling uses the cluster's real region, including the
  recorded `DummyLocation` endpoint response. Configuration, ancestor and target
  digests retain exact large integers and sensitive values without exposing
  scripts, SAS tokens, custom-image requirements or URL credentials. Operation
  states and independently reconciled child indexes do not hide configuration
  changes. Managed private endpoint deletion checks the target's actual API,
  private configuration, inherited locks, protection and managed ownership.
- Managed resource-group preflight also checks Kusto readiness and linked-target
  protection. Group ownership cannot absorb an external follower attachment;
  that attachment requires separate cleanup before group review. Tests exercise
  the AKS plan, execution and final readback with a retained external data target,
  including target protection, private drift and denied reads.
- Forty-two unchanged official examples from commit
  `e45039baa985c442877529906e705982a6e0099d` retain original schemas and hashes.
  Thirty-one bodies pass native schemas offline, plus six concrete discriminator
  checks. The full Kusto Swagger SHA-256 is
  `6c09537668b6efc3a76e6a96f29572aac2457d4188c162f8c74cd8f1690bc59b`.
  A fresh two-document snapshot matches the checked-in source, preserving all
  231 earlier types and 129 earlier documents apart from additive shared content.
- One pinned official CLI scenario supplies 45 original responses. Eight DELETE
  chains replay every recorded status poll. The selected resource API bridges
  2022-02-01 to 2025-02-14 while preserving the native response bodies and LRO
  URLs. Earlier parent/database GETs, synthetic indexes/target Storage state and
  final 404s are distinguished in the [Kusto evidence](providers/azure/fixtures/kusto/README.md).
  The recording's already-absent attachment DELETE is not counted as successful
  native attachment removal. No native custom-image deletion replay is claimed.
- Bounded polling accepts the exact native operationResults collection and
  documented version/display-region formats, including the Location query and
  its recorded empty HTTP 200 result. Ordinary empty resource reads and JSON null
  stay invalid. Target-bound receipts and request identity checks survive restart;
  partial, denied, failed/canceled, forged and mismatched responses cannot finish
  cleanup. Operation success and expiry still require final native absence of
  the target and every reviewed dependency. The generic detach invocation also
  applies the Kusto operation boundary despite its non-resource action path.
- Azure now has 241 rules, 225 native DELETE bindings and 756 operations from
  86 root plus 44 reference documents. Two generations reproduce SHA-256
  `a27b61df236291108f405eeaf5288f12268563e0d09d8a151b77b2e165a43c76`.
  Two independent CLI extractions reproduce SHA-256
  `9d6ca20104ca75ef77bf18abbbd354939c702037269174b8ba1a0879ab321599`.
  Final full Go tests, Azure/catalog/contracts/plan/cleanup race checks, `go vet`,
  eleven offline importer/SDK metadata tests and bilingual documentation checks
  passed, including the managed-group additions.
- Microsoft's Kusto emulator exposes query APIs, not this ARM control plane.
  Reviewed Floci-AZ 0.12.0 source has no Kusto routes in 331 Java files. No live
  Azure mutation or independent emulator acceptance is claimed. Unmodeled IoT
  Hub/Digital Twins link targets, data-plane tables/functions, ingestion behavior,
  backup/restore, the final read/delete race and full GCP/Azure application parity
  remain open. Native DELETE does not declare conditional ETag protection, and
  no provider acceptance item is closed by this batch.

### Azure Stream Analytics progress (acceptance remains open)

- Added seven stable 2020-03-01 rules for jobs, inputs, outputs, functions,
  transformations, clusters and cluster private endpoints, with 23 original
  operations. Transformations use the actual named singleton from an expanded
  job GET and have no invented list or DELETE. Job cleanup reviews all four
  definition kinds as native cascade impacts, then verifies each final absence.
- Native child lists, expanded definitions and embedded indexes must agree.
  Two complete walks reread the parent and compare private configuration.
  Standalone definition deletion requires a Created, Stopped or Failed job;
  native job deletion also supports Running/Degraded. Unknown or transitional
  states, retained/protected children and incomplete discovery block cleanup.
- Cluster private endpoints are reviewed prerequisites. Associated jobs remain
  independent: native POST-first/GET-next membership walks verify the job's
  cluster backlink, region and state. The core graph's optional strict boolean
  `automatic_selection:false` prevents a cluster from silently selecting jobs.
  SQLite planning/execution tests preserve this constraint, explicit selections,
  frozen prerequisites, authorization and restart behavior. Azure itself only
  requires stopping running jobs before cluster deletion; Steward requires
  retained jobs to be stopped and removed in Azure before rescanning. No
  automatic stop or detach is inferred from an ambiguous PATCH contract.
- Named external sources resolve through complete native subscription lists,
  including other resource groups. Missing names never become guessed ARM IDs;
  duplicate names, malformed indexes and denied reads fail. Explicit foreign IDs
  remain references without HTTP calls. Modeled Blob/Table, Event Hub, Service
  Bus, SQL, Cosmos and Azure Function references preserve external data resources.
  Private endpoints bind their target's actual native API, configuration, locks,
  protection and final reread. Managed resource groups cannot absorb external
  associated jobs, and their cascades retain the same target checks.
- Public configuration omits queries, scripts, connection credentials and URL
  signing values. Private digests retain these fields and exact large JSON
  integers. Runtime states, diagnostics, ETags and moving capacity counters do
  not hide authored changes. Proxy location follows the real job/cluster.
- Forty-one unchanged examples and eight full-source hashes come from pinned
  REST API commit `e45039baa985c442877529906e705982a6e0099d`. Thirty-four response
  schemas and twenty concrete variants are checked offline. Four native null
  next links and one null Azure Function API key are explicit schema/example
  discrepancies; only validation copies omit those optional nulls. A fresh
  eight-document snapshot matches the source, preserving every one of the
  earlier 241 types and 130 documents unchanged.
- Eight pinned official CLI files supply 72 selected responses; five deletion
  chains replay all native synchronous results and asynchronous polls. Input
  preview requests are explicitly bridged to stable 2020-03-01. Earlier parent
  states, supporting indexes/storage and final GET 404s are distinguished from
  original responses. The function recording deletes the job, not the function;
  no native standalone function-delete recording is claimed. See the
  [Stream Analytics evidence](providers/azure/fixtures/streamanalytics/README.md).
- Native Location signatures change on each 202. Each successor must retain its
  resource/operation identity and bounded query contract before persistence in
  `Wait.Data`; restart tests follow the original recorded URLs exactly. The
  private-endpoint final HTTP 200/InProgress/error-null envelope only permits
  resource readback. Cluster operation expiry also requires independent resource
  absence. The native stop result's empty HTTP 200 is limited to the signed
  operation URL; JSON null and empty normal GETs stay invalid. Failed/canceled,
  partial, foreign, forged and unreadable responses cannot complete cleanup.
- General execution acceptance logs, action audit events and HTTP action details
  now show operation identities without signed query credentials. The journal
  retains full original and rotated receipts for the executor. Application tests
  restore the next polling receipt from SQLite after handler restart; HTTP tests
  confirm that viewing details cannot alter stored receipts.
- Azure now has 248 rules, 231 native DELETE bindings and 779 operations from
  93 root plus 45 reference documents. Two generations reproduce SHA-256
  `f158575f54fb038d7278117204f7b02fe75534cba2387b356faa5b367b666460`.
  Two official CLI extractions reproduce SHA-256
  `77741db29207efc2c4d87185731d71c35779abbbcf6e7ee056cdaece94818377`.
  Final full Go tests, `go vet`, full Azure/plan/cleanup/catalog/contracts race
  checks, subsequent execution/HTTP and Stream Analytics race checks, eleven
  offline importer/SDK metadata tests and bilingual documentation checks passed.
- Microsoft's ASA Tools local runner tests query execution, not these ARM APIs.
  The reviewed Floci-AZ 0.12.0 source has no StreamAnalytics/streamingjobs routes
  in 331 Java files. No independent emulator or live-cloud mutation is claimed.
  Query processing, data-plane cleanup, restore, unmodeled connector targets and
  full GCP/Azure application acceptance remain open. Native DELETE contracts
  have no conditional ETag protection, so final read/delete races remain.

### Azure Batch progress (acceptance remains open)

- Added ten 2025-06-01 rules and 37 native operations for accounts, pools,
  nodes, applications/packages, private endpoint connections, network perimeter
  configuration views, jobs, schedules and tasks. Data resources retain real
  Batch URL identities. Current ARM account authority precedes Batch requests
  and the distinct Batch OAuth audience; arbitrary supplied hosts cannot receive
  credentials. Perimeter views have no invented DELETE operation.
- Complete ARM/data pool indexes, native schedule-job membership, two full
  hierarchy walks and parent rereads establish ownership. Actual auto-pool
  settings determine job/schedule lifetime. Account cleanup independently
  removes reviewed pools, applications, connections, jobs and schedules;
  packages precede applications. Task/node cascades and shared consumer choices
  preserve explicit selection, retention and independent final absence.
- Task/job deletion uses current ETags. Node removal conditions the current
  pool, selects one reviewed node and requeues running tasks. Historical task
  placement remains a graph reference without forcing task deletion. Task
  dependencies include real integer-ID range members, including leading-zero
  aliases, without expanding a potentially enormous int32 range. Changed or
  malformed dependencies and required retained consumers block cleanup.
- Multi-instance task cleanup terminates tasks, verifies complete subtask sets
  and persists primary/subtask directories with keyed node bindings. Restarted
  waits check actual directories after task absence. A primary-task 404,
  recreated node, forged receipt, partial response or denied HEAD cannot hide
  remaining working directories. Native deletion response statuses are checked
  per operation, rather than treating every successful HTTP status as equivalent.
- User-subscription nodes use the documented Uniform VMSS VM resource ID and
  existing Compute/Network child verification. Their complete VM/disk/extension/
  NIC/IP-configuration/public-IP trees are reviewed and bound to the node.
  Native deletion policy, exclusive ownership, backlinks, region, configuration,
  management locks and protections are checked again immediately before removal.
  A missing current VM cannot prove orphan-disk ownership. Shared/detached disks,
  duplicate node ownership, unknown allocation modes and incomplete trees fail.
- Node, pool and account completion independently verifies every physical
  resource; node/VM absence cannot conceal a remaining disk. The containing
  scale set remains independent and requires explicit node selection before its
  own deletion. Native pool/scale-set capacity decrements are accepted only for
  reviewed, independently absent nodes with all physical resources absent.
  Other settings and unexpected capacity changes remain bound to the plan.
- Fixed the shared planner's direct-fallback case: when an explicit child step
  precedes a parent added through ancestor cleanup, its controller metadata and
  frozen prerequisite now survive execution/restart. The child keeps its own
  reviewed cascade impacts. Core planning and existing cleanup-worker tests
  verify this path without adding a new executor contract.
- Documented ARM IDs, task placement and managed identity references reach
  inventory and the application graph. Storage/Key Vault URLs resolve through
  complete native subscription lists and matching detail reads, including other
  resource groups, storage DNS-zone/secondary/custom domains, file shares and
  the Blob root container. Missing external names/typed URLs remain unresolved;
  no same-group ARM IDs are guessed. SAS queries, mount keys and opaque user
  configuration do not become graph credentials or reference authorities. An
  uncovered `accountKey` redaction gap was fixed in the shared Azure sanitizer.
- Fifty-four unchanged native examples and full source hashes use REST API
  commit `e45039baa985c442877529906e705982a6e0099d`. Forty-two response examples
  are schema checked; original fractional-duration, optional-password and native
  identity inconsistencies are retained and documented separately from adapted
  scenario copies. Two fresh source snapshots reproduce the selected fragment.
- Seventy-five responses from eight pinned official CLI recordings retain
  their native bodies and provenance. Replays cover account deletion with
  rotating signed Location headers, package/application deletion, task reads/
  termination/deletion, node removal, auto-pool ownership and private endpoint
  discovery. API-version bridging, supporting indexes, default-version clearing
  and final GET 404s are explicitly identified as composed behavior. The private
  endpoint recording contains no deletion, so none is claimed. See the
  [Batch evidence](providers/azure/fixtures/batch/README.md).
- Azure now has 258 rules, 239 native DELETE bindings plus one native POST
  cleanup binding, and 816 operations from 95 root plus 46 reference documents.
  The deterministic catalog SHA-256 is
  `7ec3594760366c52d0266766c17e39166ebcc5d9eb68c994f9fd035a5ea66252`;
  the reproducible CLI extraction SHA-256 is
  `e96fff896dabc1bb5cc05e86bf56c258293808d88fcc46af727cd210c9fc6759`.
- Full Go tests, full Azure/plan/cleanup/governance/catalog/spec race tests,
  `go vet`, five offline catalog-sync tests and bilingual documentation checks
  passed. A final numeric-parser correction accepts large integral float64
  values restored from JSON without exponent-format rejection; subsequent full
  Go tests, Batch race tests and vet also passed. Native catalog and recording
  hashes remain unchanged.
- Reviewed Floci-AZ source has no Azure Batch service; Azurite implements
  Storage, not Batch account/job/pool APIs. This evidence is native-schema,
  in-process protocol, application graph and restart testing, not an independent
  Batch emulator or a live Azure deployment. Thirty-one mapped Azure kinds
  still lack specifications, and all eight provider acceptance items remain
  open. ARM deletion's final read/delete race, unsupported external services
  and full GCP/Azure application verification remain broader unfinished work.

### Azure API Management progress (acceptance remains open)

- Added 100 explicit API Management rules using stable API `2024-05-01`:
  services, workspaces, APIs/revisions/operations, policies and fragments,
  products, subscriptions, users/groups/tags, credentials, portal content and
  configuration, notifications, associations, gateway registrations, standalone
  gateways and their workspace configuration connections. Ninety-two kinds
  have independent native cleanup actions. Fixed settings and email templates
  remain in reviewed controller impacts; template reset is not resource removal.
- Native API and revision indexes preserve current and non-current identities,
  relative revision IDs, complete paging and original request IDs. Native HEAD
  existence operations handle legacy and recipient associations; tag GETs
  normalize the referenced tag into an association identity only after checking
  its exact parent and member. Missing or changing LIST/GET/HEAD evidence fails
  the operation. No invented GET or DELETE replaces an unsupported native route.
- Service/workspace cleanup reviews complete child trees and private
  configuration snapshots. Shared subscriptions, revision families, product
  links, policy references, certificates, loggers and authorization connections
  have explicit dependency ordering. Native force/cascade flags remain disabled.
  Built-in groups, the administrator, master subscription and fixed
  configuration retain their actual controller. Retention and protection also
  apply to independently addressable associations and notification recipients.
- Named-value references resolve through their native display names; expression
  identifiers preserve all current matching resources. Certificate thumbprints
  and explicit IDs use native indexes. Key Vault URLs and managed-identity
  client IDs resolve through subscription-wide native reads, including other
  resource groups and regions. Missing external bindings remain unresolved;
  vault secret contents, external policy URLs and expressions are not fetched
  or executed. Opaque policy/credential/portal content remains in keyed digests,
  without exposing secrets in inventory or API logs.
- Private endpoint connection cleanup rechecks the target's identity, private
  configuration, locks and protection. Standalone gateways preserve their real
  group and region and the documented singular response-ID alias. Configuration
  connections use their native body ETags for conditional deletion. Their
  workspace/service references require prior unlinking before deleting a source,
  while a shared gateway and other workspace connections remain independent.
- Complete subscription gateway indexes agree with the service's read-only
  workspace-link view. LIST/GET identities, source workspaces, gateways and
  repeated snapshots reject missing, retargeted, duplicate or changing links.
  Service-wide issue views resolve through native `apiId` into actual API-owned
  issues; both indexes, detail bodies and ETags must agree. These views do not
  create duplicate assets or non-existent deletion operations.
- Native synchronous responses, signed asynchronous URLs, rotating polling
  signatures, tenant/regional operation results and same-resource gateway
  connection polling survive JSON persistence and worker restart. Regional
  operation-result HTTP 200 has an empty body, while HTTP 202 can continue through
  Location. Completion still requires independent resource and child absence.
- Retained 324 unchanged REST examples with source hashes from API-spec commit
  `e45039baa985c442877529906e705982a6e0099d`. The offline schema test checks 223
  response bodies across 45 selected APIM documents, preserving ten published
  schema discrepancies and separately rejecting inconsistent native identities.
  The operation audit covers all 413 GET/HEAD/DELETE routes in 55 native
  documents and verifies the 309 catalog-selected routes. All 94 native DELETEs
  are accounted for: 92 cleanup bindings, email-template reset and excluded
  retained-service purge. Operational reports, alternative views, ETag-only
  reads, capabilities and recovery metadata are not additional cleanup kinds.
- Replayed 67 original official CLI responses from pinned CLI commit
  `8bead7f93f086629efb160d56c25f508156925bf`, with the older `2022-08-01` API
  version, composed support indexes and synthetic final GET absence identified
  explicitly. The retained extraction hash is
  `9ed235982b164e0493dac6f756084aa998991026e60a11178d59ebd6d9b61a91`.
  The unmodified independent `azure-apim-emulator` at
  `a1aafcf2d9743967684d9458a3371dcc86e09ef6` verifies native revision inventory,
  conditional subscription deletion and absence; its unsupported policy
  collection correctly blocks named-value deletion. The ephemeral emulator was
  stopped after verification. See [APIM evidence](providers/azure/fixtures/apimanagement/README.md).
- Azure now has 358 rules, 331 native DELETE bindings plus one native POST
  cleanup binding, and 1,125 operations from 138 root plus 48 reference
  documents. The deterministic catalog SHA-256 is
  `86801149d78454828d0fdbcbdd040499e33df1717a171d51321119e18d173a66`.
  Full Go tests, vet, five catalog-sync tests, bilingual documentation checks,
  APIM protocol/replay tests and the independent emulator test passed. Full
  Azure and shared planning/execution race tests also passed. The Azure race
  suite completed in 1,140 seconds with the explicit 20-minute package timeout;
  CI allows 30 minutes to accommodate slower runners. Repeated baseline plan
  construction is reused only within sequential test
  tables, while every execution and native preflight still runs independently.
- Service deletion uses Azure's ordinary 48-hour soft-delete retention. No
  purge, restore or email-template reset is offered. Thirty mapped Azure kinds
  still lack specifications; all eight provider acceptance items remain open.
  The service/behavior matrix, broader GCP/Azure full-application verification,
  live-cloud validation and publication remain unfinished. Native APIs without
  conditional deletion retain a final read/delete concurrency window.

### Azure Application Insights native foundation (implementation in progress)

- Added 63 original catalog operations from 16 pinned Application Insights and
  Azure Monitor Private Link Scope documents. The current Azure catalog has
  1,188 operations from 154 root and 49 reference documents; its SHA-256 is
  `ad1beea7deb84aa3f4d5296a0cb987d59154794a95059da629ef756993312510`.
- Retained 67 unchanged REST examples, 69 operation/example bindings and 42
  original CLI response records. Native-schema tests check 56 response bodies,
  handle explicit Swagger nullability and pin the 23 remaining published
  discrepancy cases. Catalog reproducibility, all existing resource bindings
  and the APIM operation audit pass with the additional sources. A separate
  complete stable/preview source audit classifies 244 operations across 44
  documents and verifies all 15 distinct native DELETE paths in the catalog.
- Transport tests exercise 80 example/status responses, all 42 recorded CLI
  responses, native arrays, private-workbook shape discrepancies, malformed
  payloads and content redaction. Monitor relative deletion locations are bound
  to their native operation-status path, subscription, group and API version;
  missing/conflicting locations and mismatched operation responses are rejected.
- The unmodified Topaz `v1.10.222-preview` emulator passed component GET/LIST,
  Invoke deletion and final GET absence checks. Its unsupported API-key list,
  nonstandard field casing and missing native creation identifiers are explicit
  test assertions. This verifies transport, not the pending cleanup planner.
- Full Go tests and vet passed after these transport changes. Scoped race tests
  passed for native sources, example/recording transport, Monitor polling
  boundaries, shared HTTP guards and the actual independent Topaz test.
- Inventory adapters, executable resource rules, managed-workspace impacts,
  AMPLS prerequisites, complete lifecycle replay and broader acceptance
  remain in progress. These additions do not increase the 358 resource
  rules or reduce the 30 mapped Azure kinds still lacking specifications.
  See [Application Insights evidence](providers/azure/fixtures/applicationinsights/README.md).

### Azure Monitor Private Link Scope lifecycle

- Added three global native resource rules: private link scopes, scoped-resource
  associations and private endpoint connections. Each binds the pinned
  `2021-09-01` GET/LIST/DELETE operations. Scope cleanup reviews two complete
  native child inventories and executes the two child kinds before the scope.
  Capability descriptions are separately read and frozen with their scope;
  they do not acquire invented deletion rules. Linked monitored resources and
  consumer network endpoints remain independent.
- Configuration checks bind access modes, exclusions, native creation fields,
  parent identity and private content. Relative asynchronous locations are
  resolved to their native operation-status path. Persisted receipts use a
  credential-keyed binding to the connection, partition, subscription, resource
  and status protocol. Failed/canceled operations, substituted receipts and
  mismatched response IDs fail; final resource and prerequisite GET absence
  remains necessary after successful or expired operation polling.
- Workspaces and DCEs acquire shared incoming-association prerequisites through
  two complete subscription scope/association indexes and native target reverse
  references. Missing inventory, permissions, contradictory indexes and stale
  backlinks block deletion. Foreign-subscription links remain unresolved; the
  selected connection never crosses its subscription boundary. A Monitor
  workspace's managed DCE receives the same external association review, without
  transferring ownership of the association or its scope to the workspace.
- Native examples and composed lifecycle tests verify plan ordering, retention,
  restart, drift, malformed/changing indexes, missing assets, credential
  substitution, shared-target cleanup and the managed-workspace case. The
  published connection-list duplicate ID/name defect is retained and rejected.
  The pinned CLI AMPLS test is skipped upstream and has no recording in that
  tree; it is not passing evidence. No AMPLS emulator or real-cloud lifecycle
  verification is claimed. Native DELETE lacks an atomic configuration condition.
- Azure now has 361 rules and 335 cleanup bindings (334 native DELETEs and one
  native POST). The catalog retains 1,188 operations from 154 root and 49
  reference documents; its SHA-256 is
  `74efc909d8a440f44a3065fc29c4bf06bf4a61622c729a09e166f434f268d970`.
  Full Go tests and vet passed, followed by scoped checks for the final identity
  guard and spec fields. Monitor/private-link/data-collection race tests and
  shared asset/graph/planning/execution/governance race checks passed. Five native
  catalog refresh tests and bilingual documentation checks passed.
- Application Insights resource adapters, legacy-child identities and managed
  workspace impacts remain in progress. Thirty mapped Azure kinds still lack
  specifications; the eight provider acceptance criteria remain open.

### Azure Application Insights legacy adapter progress

- Implemented native URL identities for analytics items, user analytics items,
  continuous exports, favorites, work-item configurations and annotations.
  Opaque query/path selectors retain case; only the ARM parent and fixed URL
  segments are canonicalized. Existing Batch IDs retain their prior keys.
- Native LIST/GET reconciliation includes private content, component rechecks,
  all 18 favorite scope/source combinations and explicitly bounded annotation
  windows. Unexpected continuation and incomplete or changing collections fail
  closed. A 90-day annotation index is not an all-history inventory.
- Direct leaf drivers bind the selected connection, partition, native identity,
  parent and private configuration; enforce group/lock protection; validate
  synchronous responses; and verify exact child absence with resumable receipts.
  Published examples, original CLI GETs and composed tests using the native
  DELETE bodies cover these protocols. The export recording's intermediate
  destination update remains visible and is not treated as a volatile field.
- Full Go tests and vet passed (Azure 149.230s, GCP 167.969s). Scoped Azure race
  checks passed, followed by race checks for the final recorded-body tests;
  shared identity, graph, planning, execution, inventory, SQLite persistence
  and catalog race checks passed. The optional PostgreSQL and independent
  emulator checks were not exercised in this adapter run.
- Resource registration, inventory-to-graph integration, component cleanup and
  managed-workspace effects remain in progress. These adapters do not yet add
  resource rules: the total remains 361 rules, 335 cleanup bindings and 30
  mapped Azure kinds lacking specifications. All eight acceptance criteria
  remain open.

### Azure Application Insights inventory and graph integration

- Registered component inventory and five native legacy leaf kinds. Components
  remain read-only while their native children and managed-workspace cleanup
  are implemented; this is an unfinished lifecycle, not completed support.
  Annotations remain unregistered because the bounded native index cannot
  prove older persisted records absent.
- Dedicated inventory preserves opaque URL selectors and original flat payload
  fields, reconciles two complete native snapshots, inherits component/group/
  lock protection, and binds cursors to connection, scope, kind, network and
  private configuration. Filtered, partial, duplicate, cyclic or asynchronous
  component pages and configuration/membership drift fail the scan.
- Actual provider batches pass through application projection and SQLite,
  authoritative shard completion, graph rebuilding with Azure contributors,
  native leaf planning, registered action resolution, deletion and resumed
  readback. Ten case-distinct children retain separate assets and graph edges.
  Shared storage/account/container references and component workspace links
  preserve target retention and explicit-selection deletion ordering. AMPLS
  associations now resolve their component graph targets.
- Bare export destination names resolve through native subscription LIST/GET;
  missing or foreign names remain unresolved. The original CLI export bodies
  contain contradictory destination-subscription fields after upstream
  sanitization. A dedicated test rejects them as storage-relationship evidence
  without modifying the recorded response bodies.
- Full Go tests and vet passed (Azure 149.877s; GCP cached), followed by final
  native-inventory/spec checks and scoped Azure race tests (13.944s). Shared
  inventory, governance, cleanup, asset, graph, plan and SQLite race checks
  passed; five native catalog refresh tests passed. These composed tests do
  not claim a new independent emulator or cloud run.
- Azure now has 367 rules and 340 cleanup bindings (339 native DELETEs and one
  native POST). The catalog retains 1,188 operations, 154 root documents and
  49 reference documents; its content SHA-256 is
  `aaa42c2456b6892fac38427fc1301affc5c9ec769ce6f763f6c20ecf380d0b04`.
  Rechecking the parity matrix finds 29 mapped Azure kinds without specs;
  the five new child rules are additional native coverage, not five more
  completed matrix roots. All eight acceptance criteria remain open.

### Azure Application Insights API keys and linked storage

- Registered native API-key and linked-storage inventory/cleanup rules. API keys
  keep their flat payload, full ARM identity and friendly name; permission paths
  do not create resource or ownership edges. The fixed linked-storage singleton
  uses its native GET and the exact `ServiceProfiler` enum. Absence requires a
  live, unchanged component before and after that GET.
- Both kinds reuse component/group/lock protection, private snapshot binding,
  synchronous native delete validation and resumable final absence checks.
  API-key DELETE validates the returned object against the reviewed key;
  linked-storage DELETE accepts empty 200/204. Shared storage remains independent.
  Legacy opaque URL identities and operation receipts retain their prior form.
- Tests retain the original API-key GET/DELETE and linked-storage GET example
  shapes, with explicitly substituted scope identifiers. Native batches pass
  through SQLite projection, authoritative shards, Azure graph contributors,
  planning, registered actions and resumed readback. Coverage includes private,
  permission, creation and membership changes, missing/replaced parents,
  protection, malformed targets, unexpected continuation, response protocols,
  foreign-subscription boundaries and explicit shared-target deletion ordering.
  These are composed protocol tests; the inspected independent Topaz release
  lacks these nested APIs, and no new emulator or real-cloud run is claimed.
- Full Go tests and vet passed (Azure 148.982s; GCP cached). Application Insights
  and native catalog-binding race checks passed (17.065s), including the SQLite
  graph/action scenarios; five native catalog refresh tests passed.
- Azure now has 369 rules and 342 cleanup bindings (341 native DELETEs and one
  native POST). The catalog retains 1,188 operations, 154 root documents and
  49 reference documents; its content SHA-256 is
  `498a0be604ee87b3999dfacc85f9fdf2d4c3db1b1cd16d4f292e4a1abf572849`.
  The two child kinds do not reduce the 29 mapped Azure roots lacking specs.
  Component cleanup, managed-workspace effects, annotation coverage and the
  remaining service families still require work. All eight acceptance criteria
  remain open.

### Azure Application Insights managed-workspace inventory

- Component inventory now reconciles the native resource-group index with the
  current workspace reference and each relevant group's `managedBy`. Shared,
  foreign, current managed and detached managed groups remain distinct; names
  never establish ownership. A workspace switch leaves the old group's
  identity/configuration visible without claiming component deletion removes it.
- The current managed group's unfiltered, paginated ARM member index includes
  unknown kinds. Known members receive product GETs, and group reads bracket
  membership discovery. Existing native AMPLS forward/reverse reconciliation
  captures required associations, including unresolved foreign references.
  Private member/group configuration stays behind credential-keyed digests.
- Two complete component snapshots and continuation cursors bind ownership,
  group membership and association state. Tests cover permission failures,
  incomplete/filtered/cyclic pages, malformed references, private/creation/
  membership changes, changing owners, native ID case, unrelated group churn
  and AMPLS contradictions. Real application projection and SQLite persistence
  preserve the populated managed-workspace snapshot and its digest.
- Full Go tests and vet passed (Azure 148.522s; GCP cached). Application Insights
  and AMPLS race checks passed (15.325s), including the SQLite scenarios.
  Bilingual documentation validation passed for 40 chapters and 10 original
  screenshots. The component and AMPLS example shapes are retained; managed
  group and Log Analytics bodies are composed protocol data, not independent
  emulator or live-cloud evidence. No new emulator/cloud run was performed.
- Counts remain 369 rules, 342 cleanup bindings and 1,188 catalog operations.
  Member product-specific descendants, component child/configuration impacts,
  controller deletion and final managed-group/workspace absence still need
  lifecycle integration. Components remain read-only. The 29 missing mapped
  Azure roots and all eight provider acceptance criteria remain unfinished.

### Azure Application Insights native child lifecycle bindings

- The service lifecycle contributor now reconciles the seven registered child
  kinds twice through their native LIST/GET adapters. It binds exact opaque
  selectors, parent/child private configuration and location. Each matching
  asset gets an exclusive, independent-delete binding; no controller-delete or
  delegated-absence guarantee is invented. Missing assets remain unresolved.
- Persisted children omitted by the native index require their own GET 404
  before the binding can disappear. Permission failures, incomplete lists,
  changed membership/private configuration, ambiguous identities and changed
  parents fail the contribution. Native protection remains effective for
  independent child actions. Shared storage relationships retain their existing
  explicit-selection ordering.
- Existing SQLite projection, graph rebuild and registered-action tests now
  plan against these real lifecycle bindings. A combined native-shape test
  preserves ten case-distinct legacy children and three original ARM child
  examples. The real solver produces 13 separate prerequisites before a test-only
  actionable component, preserves individual selection and blocks retention.
  The production component remains read-only; this is not an enabled component
  deletion or evidence of managed-group absence.
- Full Go tests and vet passed (Azure 149.477s; GCP cached). Application Insights
  and AMPLS race checks passed (17.676s); supplemental native group-protection
  checks exercise the registered leaf driver. Documentation validation passed
  for 40 bilingual chapters and 10 original screenshots. These are composed
  protocol tests; no additional emulator or real-cloud run was performed.
- Counts remain 369 rules, 342 cleanup bindings and 1,188 catalog operations.
  Managed-group descendant bindings, component deletion/residual checks,
  annotation coverage and remaining service families still require work. The
  29 missing mapped Azure roots and all eight acceptance criteria remain open.

### Azure Application Insights component and managed-workspace deletion

- The component now resolves a registered native deletion driver. Its seven
  child kinds remain separate prerequisites, as do AMPLS associations targeting
  the component or current managed workspace. Current managed groups, workspaces
  and known recursive product descendants contribute reviewed delegated impacts.
  Shared workspaces, detached old groups, storage targets and AMPLS scopes stay
  independent. Nested managed controllers with unmodeled external groups block
  inventory instead of receiving an unsupported deletion guarantee.
- Complete native child/workspace discovery is repeated before deletion. Frozen
  private configuration and ownership, native protection, subscription locks and
  every reviewed prerequisite's exact absence are required. Separate lifecycle
  and inventory-generation digests tolerate expected ETag/backlink changes after
  reviewed AMPLS removal without ignoring other private configuration changes.
- The original component API has synchronous empty 200/204 responses. Its driver
  rejects invented asynchronous completion and persists a credential-bound
  receipt containing the reviewed resource/prerequisite/impact identities. After
  serialization and driver reconstruction, native readback still requires the
  component, managed group and every known member to be absent. Unknown contained
  kinds use group absence. Residual group/workspace/VM/extension resources remain
  pending; changed identities or invalid responses fail verification. An already
  absent component does not bypass residual checks or trigger another DELETE.
- Actual native inventory, SQLite projection, application graph rebuilding and
  real planning produce 13 child steps followed by component deletion. The AMPLS
  variant adds two independent unlinks and preserves the shared scope. Further
  tests cover native descendants omitted from the ARM root index, duplicate
  index/child entries, broad-inventory unknown kinds, private drift, managed
  retention, shared/foreign/detached workspaces, locks, replacement during
  readback and receipt substitution. Original example bodies are unchanged;
  group ownership and lifecycle transitions are composed protocol data. No new
  independent emulator or real-cloud lifecycle run is claimed.
- Full Go tests and vet passed (Azure 151.214s; GCP cached). Application Insights,
  AMPLS, AKS and Monitor workspace race checks passed (55.189s); additional managed
  retention/nested-controller race tests passed (4.110s). Five offline catalog
  refresh tests, deterministic generation and catalog executability checks passed.
  Bilingual documentation validation passed for 40 chapters and 10 screenshots.
- Azure has 369 rules and 343 cleanup bindings (342 DELETEs and one Batch node
  removal POST), with 1,188 operations from 154 root and 49 reference documents.
  The generated catalog SHA-256 is
  `0c72863393a6c58bfdce302f2315f393fea93937d8529ddccfb6a4d20911467a`.
  Automatic cleanup of a group left by Azure policy/locks, annotation history,
  other component configuration, independent web tests/workbooks and remaining
  service families are unfinished. The 29 missing mapped Azure roots and all
  eight provider acceptance criteria remain open.

### Azure Application Insights component configuration

- Component inventory now reads native billing features, current pricing,
  capabilities, available features, quota and proactive detection configuration.
  Fixed GETs bind the selected component through the generated catalog; the
  pricing operation retains its lowercase native namespace. Proactive rules use
  the original array LIST and an exact GET per name. Invalid/partial reads,
  permission failures, duplicate names, wrong identities, LIST/GET disagreement
  and component replacement fail the scan.
- Authored billing/cap and proactive settings, including redacted email values,
  are bound to both inventory snapshots and scan cursors. Component preflight
  rechecks that private digest twice and persists it in the deletion receipt.
  Read-only cap limits/reset hours, capabilities, available features, quota,
  static rule definitions and update timestamps remain observations. Native
  fixed configurations receive no invented independent deletion operation.
- Tests use the retained original configuration bodies with explicit component
  scope/AppId substitutions. Successful proactive enumeration composes the
  original one-rule GET; the unchanged official LIST's duplicate names are
  explicitly rejected. Tests exercise privacy, stale cursors, malformed reads,
  authored/public/private drift, observation changes, real SQLite projection,
  planning, native deletion and receipt verification after serialization/restart.
  A second component's independent configuration is included in cursor tests.
- Full Go tests and vet passed (Azure 156.651s; GCP cached). Application Insights
  and AMPLS race checks passed (54.946s); the complete Application Insights suite
  passed (10.215s). Bilingual documentation validation passed for 40 chapters and
  10 original screenshots. No new emulator or live-cloud run was performed.
- Counts remain 369 rules, 343 cleanup bindings and 1,188 catalog operations.
  Migrated smart-detection alerts and action groups are independent resources,
  outside the legacy configuration adapter. Annotation history, independent
  web tests/workbooks, remaining service families and all eight provider
  acceptance criteria remain unfinished; 29 mapped Azure roots are still absent.


### Azure Application Insights annotations and historical identity refresh

- Annotations now have a registered native leaf rule and a dedicated bounded,
  non-authoritative inventory source. Native LIST keeps its value envelope,
  individual GET keeps its original array shape, and DELETE remains synchronous.
  Every page/snapshot uses one fixed window inside Azure's rolling 90-day limit;
  cursors bind that window, known identities and the current private snapshot.
  Opaque Properties stay out of inventory/logs while remaining in private checks.
- The real scan worker supplies a sorted, fixed, cloned set of active identities
  only for opted-in sources and the selected connection/kind/partition/provider.
  It restores the source's non-authoritative setting, so a stale persisted shard
  cannot close older records. Azure rereads saved IDs omitted from the window;
  missing indexed parents need exact parent and child GET 404s. Permission errors,
  ambiguous empty/multiple arrays, wrong identity and private drift fail scans.
- Native child lifecycle discovery and component preflight now review recent and
  previously saved annotations as independent prerequisites. Retention blocks
  component deletion. Exact prerequisite reads cannot be bypassed by window
  omission or component absence; late and surviving annotations block completion.
  Previously unseen history outside the native window cannot be enumerated and
  can be removed by component deletion. No all-history index is claimed.
- Tests preserve original annotation bodies and compose matching GET arrays.
  Their old EventTime values do not establish current native date filtering.
  Actual scan creation/worker execution, SQLite projection, graph rebuilding,
  planning and native DELETE/readback cover historical refresh and retention.
  The managed-component scenario plans 15 child deletions followed by the root;
  request serialization and driver reconstruction preserve absence checks.
  Focused tests cover cross-connection/source isolation, stable known IDs across
  pages, stale authority, changed windows, omitted parents, reads and races.
- Full Go tests and vet passed (Azure 159.376s; GCP 170.235s). Inventory worker,
  Application Insights and AMPLS race checks passed (Azure 69.956s; inventory
  3.691s). The focused Application Insights suite passed (8.077s). Five offline
  catalog refresh tests, deterministic generation and executable-spec checks
  passed. Bilingual documentation validation passed for 40 chapters and 10
  screenshots. No new independent emulator or live-cloud run was performed.
- Azure now has 370 rules and 344 cleanup bindings (343 DELETEs and one Batch node
  removal POST), with 1,188 catalog operations. The generated catalog SHA-256 is
  `c7b24b855a54caef290cfa9da661c355009ec1b68e702ea586c398cb177cd11c`.
  Independent web tests/workbooks, migrated alerts, remaining service families,
  the 29 missing mapped Azure roots and all eight acceptance criteria remain open.

### Azure Monitor workbooks and templates

- Three executable native rules now cover shared workbooks, private workbooks
  and workbook templates. Category-filtered workbook discovery reads the four
  documented categories plus categories found through ARM and saved IDs. Its
  dedicated non-authoritative source reconciles existing IDs without closing
  unobserved custom-category assets. Templates enumerate every native resource
  group because Azure has no subscription template LIST.
- Inventory requests full content and binds current configuration and all
  native shared-workbook revisions. Revision summaries and GETs keep the root
  identity and opaque revision selector; they have no invented child DELETE.
  Revision read failure/404 cannot establish root absence. Native array lists,
  singular template type aliases and recorded null workbook types are supported
  without allowing mismatched IDs or incomplete content to authorize deletion.
- Independent synchronous DELETE accepts only the native empty 200/204 contract.
  Persisted receipts bind the connection, partition, resource, configuration,
  group and protocol. A live or replaced resource, malformed response or changed
  receipt cannot complete deletion. Group ownership, tags and locks are checked
  again. Managed-group cleanup also rechecks each contained workbook's full
  private history and requires its final native GET absence after root deletion.
- Shared source/storage/identity references are declared in resource rules and
  reconciled through native lifecycle contributions. Explicit selections are
  ordered without transferring ownership. Opaque authored content is private
  and does not invent links; URL credentials/query/fragment data are sanitized
  without altering the private configuration used for drift checks.
- Tests cover native and client paging, custom categories, stale authority,
  historical drift/failure, protection, ambiguous responses, receipt restoration,
  shared dependency ordering and managed-component deletion with live residuals.
  Real scan creation and workers, SQLite projection, graph rebuilding, planning
  and serialized actions verify the complete independent cleanup path. Native
  examples and selected CLI metadata are retained with explicit composed reads;
  no new independent emulator or real-cloud verification is claimed.
- Full Go tests and vet passed (Azure 155.053s; GCP cached). Application Insights,
  AMPLS, AKS and Monitor workspace race checks passed (88.410s). The full focused
  Application Insights suite passed before the final managed-history checks
  (9.371s); final workbook/managed-history/privacy checks passed (2.088s), and
  the full Go/race runs include those final changes. Five offline catalog tests,
  deterministic generation, executable-spec checks and bilingual documentation
  validation (40 chapters and 10 screenshots) passed.
- Azure now has 373 rules, 347 cleanup bindings (346 DELETEs and one Batch node
  removal POST) and 1,188 catalog operations. Generated catalog SHA-256:
  `8388492db66f3c03cdc369a0c93898578f0c4ba801b88943864fae32f69e86a8`.
  Active workbook absence is not permanent erasure: ordinary workbooks can be
  recovered for approximately 90 days; BYOS recovery depends on storage behavior
  and has no provider-managed revision/recycle-bin support. Independent web
  tests, migrated alerts, remaining service families, the 29 missing mapped
  Azure roots and all eight acceptance criteria remain open.

### Azure Monitor alert API and privacy foundation

- Seven native families now have retained API operations: metric alerts,
  action groups, activity-log alerts, scheduled-query rules, smart-detector
  alerts, Prometheus rule groups and alert-processing rules. The selected
  stable versions are respectively 2026-01-01, 2023-01-01, 2026-01-01,
  2026-03-01, 2021-04-01, 2023-03-01 and 2021-08-08, from the same immutable
  Microsoft specification commit as the Application Insights evidence.
- Thirty operations bind to 36 unchanged examples used at 37 operation/example
  bindings. Tests replay all 44 native responses and validate 30 response
  bodies offline; all 14 published DELETE responses are empty HTTP 200/204.
  Four undeclared request selectors, two null-pagination schema discrepancies
  and the Smart Detector array/object action-group mismatch are documented.
  Native samples are not evidence of completed runtime cleanup or live Azure.
- Endpoint-aware redaction covers identity-free list/status responses and
  returned Invoke data. Notification receivers, private alert conditions,
  queries, expressions, labels, dimensions, detector parameters and custom
  payloads remain private. Tests preserve the original private transport
  content and verify that unrelated resource families are unaffected.
- Full Go tests and vet passed (Azure 159.702s; GCP cached). Relevant privacy
  and native-source race tests passed (4.898s). Restoring the source refresh
  tool's canonical document ordering passed all five offline catalog checks;
  final native-source/catalog/privacy tests passed (1.292s). Repeated catalog
  generation produced SHA-256
  `9394f0fac4c714dfa5c5153308bff98b1dc9a2eaec7b49ed39e6f9dfe19d5253`.
- This foundation raises catalog operations to 1,218 across 161 root and 50
  reference documents. Executable rule and cleanup counts remain 373 and 347.
  Native inventory, alert references, shared action-group dependencies,
  independent web-test/alert cleanup, the remaining provider families and all
  eight acceptance criteria remain open.

### Azure Monitor native rule reads and reference validation

- Dedicated readers bind full GET and unfiltered subscription LIST operations
  for the seven alert families and independent web tests. Lists reconcile each
  row against complete private authored configuration from GET. Identity,
  subscription, collection, version, paging and response completeness are
  checked before accepting the collection. Explicit native action-group slots
  preserve shared references and both published Smart Detector shapes.
- Tests retain original evidence and name every composed substitution. Invalid
  duplicated metric IDs and bare action-group placeholders fail validation;
  optional root names and native Activity Log scope spelling are supported.
  The shared list transport narrowly normalizes the published Alert Processing
  default HTTPS port. Other families and nonstandard ports keep their existing
  boundary checks. Read-only observation changes do not replace the private
  authored configuration used for drift detection.
- Full Go tests and vet passed (Azure 162.343s; GCP cached). Focused native,
  privacy and transport tests passed (0.935s), and alert reader/privacy race
  checks passed (6.054s). No new emulator or live-cloud verification was run.
- These readers are not yet registered inventory or cleanup implementations.
  Catalog, rule and cleanup counts remain 1,218, 373 and 347 respectively.
  Shared Action Group incoming references must also include Consumption and
  Cost Management budgets at subscription and resource-group scopes, as
  documented in their native `notifications.contactGroups` contracts. Graph,
  inventory and independent cleanup integration remain open with the full
  provider acceptance criteria.

### Azure budget notification API and privacy foundation

- Consumption (`2024-08-01`) and Cost Management (`2025-03-01`) budget Get,
  List and Delete operations now retain 20 unchanged official examples from
  the existing immutable Microsoft specification commit. Two root documents
  and two common-type dependencies are hash-bound; all previous 211 source
  documents remain semantically unchanged. Both APIs declare Action Group
  references at subscription and resource-group scopes.
- Eighteen original response bodies pass offline schema validation. Seven
  subscription/group requests replay their native responses, including two
  empty synchronous HTTP 200 Deletes. Thirteen billing/management-group
  requests bind to the catalog but fail the connection's subscription boundary
  before transport. Each example's undeclared scope selectors are explicitly
  enumerated and removed only after proving the original request cannot bind.
  Response scope discrepancies and composed-runtime requirements are documented.
- Notification dictionaries and arbitrary budget dimension/tag filters remain
  private in resource projections, Invoke results and logs, including lists
  without row identities. Native private reads remain unchanged. Tests check
  public observations, mixed-case fields and unrelated resource families.
- Full Go tests and vet passed (Azure 161.086s; GCP cached). Native source,
  reader and privacy tests passed (3.959s); corresponding race checks passed
  (6.908s). Five offline catalog refresh checks passed. Repeated generation
  produced SHA-256
  `0782b35808b1299b2b7624435ab1134ee0b9c1e89382fc73eeffb6d03d5be46b`.
  No new independent emulator or real-cloud test was run.
- Catalog operations increase to 1,224 across 163 root and 50 dependency
  documents. Executable rules and cleanup bindings remain 373 and 347.
  Budget inventory/reference reconciliation and independent cleanup, Monitor
  integration, remaining service coverage and all eight acceptance criteria
  remain open.

### Azure budget native reads and incoming-reference prerequisites

- Native budget readers now support subscription and resource-group identities
  for both retained APIs. Consumption's missing leading slash is normalized
  within that family only. Get checks its exact requested identity and required
  configuration. Unfiltered lists reconcile every row against Get, allow group
  budgets in a subscription list and bind group lists to their exact group.
  Composed paging is explicit because the retained examples have no nextLink.
- Notification contact groups are extracted only from native dictionary slots
  and deduplicated across thresholds. Filter text does not invent references.
  Current and forecast spend remain read-only observations; private recipients,
  filters, amount and other authored fields remain in configuration comparison.
- Tests exercise original response identity differences, native reads, composed
  paging, foreign/malformed scopes, incomplete notifications, private drift,
  permission failures, duplicate identities and altered continuation context.
  Both Monitor and budget indexes reject ambiguous response-field casing and
  preserve listed-resource read errors as dependency failures rather than
  allowing a child 404 to imply collection absence.
- Full Go tests and vet passed for the budget readers (Azure 161.433s; GCP
  cached). After adding the final Monitor index dependency-error wrapper,
  the combined Monitor/budget/source/privacy tests passed (0.868s), race checks
  passed (7.058s) and Azure vet passed. The earlier complete reader race run
  passed (7.097s). No new emulator or live-cloud verification was performed.
- Counts remain 1,224 catalog operations, 373 executable rules and 347 cleanup
  bindings. These helpers still need registration, full inventory snapshots,
  lifecycle graph contributions and independent cleanup. The remaining provider
  families and all eight acceptance criteria remain open.

### Azure native Monitor/budget inventory and reference integration

- The dedicated `Runtime.List` path now enumerates the eight retained Monitor
  kinds and both budget APIs through their native unfiltered indexes. Budget
  discovery reconciles subscription and every validated resource-group index,
  preserving group budgets omitted by the subscription response. Subscription
  budgets use global inventory scope without a fabricated resource group or a
  broader generic ARM identity parser.
- Two complete native observations bind resource configuration, all resource
  groups and inherited locks to an opaque inventory cursor. The binding covers
  connection, requested kind, scope and bundle revision. Private recipient or
  query changes, missing/new resources, group changes, lock changes and failed
  pages invalidate the scan; listed-resource/group 404s remain dependency-read
  errors. Read-only spend changes and reordered locks do not invalidate it.
  Resource-level locks also accept strictly validated subscription budget IDs.
- Projection retains private configuration/group proofs and explicit native
  references while removing notification dictionaries, receiver arrays,
  queries, expressions and web-test content from public inventory. Reference
  slots include action groups, full evaluation scopes, typed web-test metric
  criteria, hidden component links, the declared assigned identities of metric
  and scheduled rules, and explicit ARM IDs
  in Function, Logic App and Automation receivers. Subscription evaluation
  prefixes do not invent assets; foreign targets remain unresolved.
- The native lifecycle contributor re-reads the resource and group proofs and
  contributes shared usage plus required prior cleanup with automatic selection
  disabled. A plan test exposed that a plain `uses` edge alone did not protect
  an unselected referencing budget; the explicit reverse requirement fixes
  that gap. A budget may be selected independently, a retained budget blocks
  action-group cleanup, and explicit selection of both freezes the budget
  prerequisite and orders it first without creating ownership. The shared
  reference resolver is also reused by the existing workbook contributor.
- Tests retain the original native property bodies with documented identity
  composition, exercise all ten runtime inventory paths, pagination/protection
  and concurrent-change boundaries, and rebuild/solve native budget graphs.
  The plan fixtures explicitly supply action capabilities while registration
  is staged; they are not scan-worker or deletion-driver acceptance evidence.
- Full Azure/GCP tests passed after native graph integration (Azure 159.791s;
  GCP cached), and repository-wide vet passed. After restricting scope/identity
  reference slots to their actual schemas, the combined Monitor/workbook tests
  passed (6.953s), the final Monitor race run passed (23.443s), and Azure vet
  passed. The earlier combined Monitor/workbook race run passed (30.359s).
  No new independent emulator or real-cloud verification was performed.
- Runtime resource-rule registration, incoming indexes during cleanup,
  Event Hub receiver namespace resolution, ITSM workspace GUID resolution,
  independent deletion, managed-group composition and worker acceptance are
  still open. Catalog/spec counts remain 1,224 operations and 373 types/rules
  with 347 cleanup rules. The 29 missing mapped Azure roots, remaining provider
  families and all eight acceptance criteria remain unfinished.

### Azure Monitor/budget registration and independent cleanup

- Eight Monitor kinds and both budget APIs now have explicit executable rules.
  The catalog has 1,224 operations, 383 types/specs and 357 cleanup bindings.
  All 213 retained native documents and the preceding 373 type bindings remain
  semantically unchanged. Budget list/read/delete selectors use the full native
  subscription or resource-group path; subscription budgets remain global.
- Independent cleanup binds the private resource configuration, resource group
  and reference proof to the reviewed asset. Preflight repeats native incoming
  collections, full reads and protection/lock checks, then performs a final
  private read. Native synchronous response contracts and signed recovery
  receipts are checked before final absence readback. Referenced destinations
  remain independently selected shared resources.
- Monitor target graph, preflight and readback enumerate incoming alert rules,
  plus both budget APIs for Action Groups. Unindexed or late-created sources
  block cleanup; target absence cannot conceal failed dependency reads. Frozen
  prerequisites authenticate their original references, identity and connection
  after JSON recovery and require native source absence. Real graph/plan tests
  execute a selected budget before its selected Action Group.
- SQLite-backed tests exercise the actual registry, scan creator/worker,
  configuration updates, graph/plan, action recovery and subsequent absence
  reconciliation for all ten kinds. Tests preserve independent siblings,
  retain observations on a failed scan, and keep private recipients/content out
  of persisted public fields. These are protocol/application integration tests;
  no new independent emulator or live-cloud verification was performed.
- Full Go provider/spec tests passed (Azure 160.396s, GCP 158.276s, spec 2.510s).
  The added scan-worker tests passed (5.100s); the complete Monitor race run
  passed (43.981s), and repository-wide vet passed. Five offline source-refresh
  tests passed. Two catalog generations produced SHA-256
  `b5114d9d888da21c1ab51226bd16fbb69c9daf62d7d2b7756b5163bce6b46365`.
- Event Hub and ITSM receiver resolution, Function/runbook child references,
  existing non-Monitor target drivers and managed-group composition still need
  integration. Remaining service coverage, bilingual capability/permission
  documentation and all eight provider acceptance criteria remain open.

### Azure Monitor receiver identity resolution

- Event Hub receivers now resolve namespace names through two complete native
  subscription indexes reconciled with Get. Child references use the verified
  namespace's resource group. ITSM receivers support the published compound
  workspace selector and an unqualified customer GUID, matched to native
  workspace identity and region. Missing or foreign destinations retain typed
  unresolved selectors; they do not borrow the local subscription or invent
  ARM resource groups.
- Function and non-global Runbook child references supplement their explicit
  parent references. Global Runbook display names remain unexpanded until the
  native webhook mapping is implemented. Lookup outcomes participate in signed
  source references, inventory cursors, graph verification and action preflight,
  so namespace moves, workspace recreation and newly appearing destinations
  invalidate stale evidence even without a source configuration change.
- Four additional unchanged official examples and three existing document
  checksums retain provenance. Four original response bodies pass offline schema
  validation; the Workspace Get example's incorrect array is a required, named
  discrepancy and is rejected by the runtime. The Action Group Create example
  supplies receiver evidence only; no additional write operation is exposed.
  Protocol scenarios explicitly compose identities from the original examples.
- Tests cover native pages and Get reconciliation, ambiguous or missing targets,
  foreign scope/tenant, invalid native selector shapes, failed/incomplete reads,
  cancellation and resolution drift. The registered Action Group scan-worker
  scenario now persists real receiver references, rebuilds the graph and
  independently deletes/reloads the referring resource while retaining targets.
- Full Go tests passed (Azure 164.254s; GCP cached), repository-wide vet passed,
  and all Monitor race tests passed (47.748s). After the Action Group spec and
  worker integration updates, all Monitor tests passed (5.673s); the final
  scan-worker race run passed (18.970s). No new independent emulator or live-cloud
  verification was performed. Bilingual Azure capability/permission docs now
  describe the ten registered kinds and receiver-read requirements.
- Counts remain 1,224 operations, 383 types/specs and 357 cleanup bindings.
  Existing non-Monitor destination guards, unresolved targets after destination
  absence, managed-group composition, global Runbook webhook mapping, remaining
  provider families and all eight acceptance criteria remain open.

### Azure Monitor configuration checks in managed groups

- The shared managed-resource configuration check now authenticates Monitor
  resource configuration and recorded reference proofs. Native group graph
  binding and deletion preflight additionally repeat the exact product GET,
  compare the original resource-group proof and recompute receiver resolution.
  The same private configuration and receiver checks apply to surviving known
  members after native controller and group absence. A generic ARM response
  cannot substitute for a complete Monitor/budget read contract.
- Composed AKS scenarios retain the native example properties for all ten
  registered Monitor/budget kinds. They cover graph/preflight configuration
  changes, group changes, altered reference proofs, native 202/LRO/aliased/error
  responses, disappearance during repeated reads, and delayed residual absence
  after JSON recovery. A private receiver change cannot reach controller DELETE.
  Namespace relocation independently invalidates unchanged Action Group content
  during graph, preflight and residual readback.
- The actual SQLite-backed Application Insights inventory, graph and plan path
  also includes a managed Action Group. Only the component receives a DELETE;
  the recovered action waits for the group's known Monitor member to disappear.
  The existing managed-workbook history path remains covered by regression tests.
- Full Go tests passed (Azure 166.481s; GCP cached). The managed Monitor, AKS and
  managed-workbook race run passed (20.804s); repository-wide vet and diff checks
  passed. The new focused native scenarios passed (3.781s), and the actual
  Application Insights/SQLite integration passed separately (3.800s).
- This closes a configuration-validation gap for already discovered members.
  It does not yet complete native enumeration of omitted group Monitor/budget
  members, external incoming-reference checks, non-Monitor target-driver guards,
  or receiver matching after destination absence. Counts remain 1,224 operations,
  383 types/specs and 357 cleanup bindings; all eight acceptance criteria remain
  open. No new independent emulator or live-cloud verification was performed.

### Azure Monitor guards for all native ARM destinations

- Graph contribution now batches two complete native Monitor observations for
  all ARM destinations. Source LISTs reconcile full private GETs; Event Hubs and
  workspace indexes are reused within one observation and then checked again.
  Unindexed sources become required, explicitly selected dependencies. Missing
  source groups never grant ownership or erase a surviving reference.
- Every registered non-Monitor ARM action now shares incoming-reference checks
  for its root and reviewed deleting impacts before mutation, polling and final
  readback. Existing native drivers retain their ownership, protection,
  prerequisite, retention and polling behavior. Ordered Monitor prerequisites
  authenticate the original source reference and require native absence; the
  complete reviewed request is bound to a recovery receipt, preserved through
  replacement wait results. Pre-upgrade pending receipts require a new scan and
  plan; they cannot bypass the added authentication.
- Missing Event Hub destinations remain matched to frozen namespace/hub identity.
  ITSM destinations use the scanned, authenticated workspace customer GUID after
  native workspace absence and JSON recovery. Foreign selectors cannot acquire a
  local target. Workspace inventory now records the additional identity proof,
  requiring existing workspaces to be rescanned.
- Already verified Monitor members and destinations inside the same managed group
  may share a provider-declared controller cascade. The solver still requires
  both assets to be deleted by that exact selected action and applies normal
  retention and protection. Outside-group references and forged impacts block
  mutation; surviving Monitor members keep native controller readback pending.
- Tests exercise batching across ten targets, real graph/plan ordering, native
  disk and Monitor actions, JSON request/receipt recovery, retained references,
  late sources, source-group absence, dependency 403/404, altered proofs and
  foreign identities. Managed-group composition verifies internal references,
  external blockers and residual absence with actual native drivers. Existing
  Batch, Stream Analytics, Grafana and retention recovery tests cover shared
  guards with their distinct operation protocols.
- Full Go tests passed (Azure 170.904s; GCP cached), the Monitor/native recovery
  race run passed (63.626s), and repository-wide vet passed. These are composed
  native protocol tests; no new independent emulator or real-cloud verification
  was performed. Bilingual Azure docs describe added dependency-read permissions,
  workspace rescans and pending-receipt upgrade constraints.
- Native discovery of omitted group members, references directly to managed
  controllers, global Runbook webhook mapping, remaining provider families and
  all eight acceptance criteria remain open. Counts are unchanged at 1,224
  operations, 383 types/specs and 357 cleanup bindings.

### Azure Monitor references to their managed controller

- A provider-declared native cascade can now satisfy a prerequisite that refers
  directly to the selected controller. The prerequisite must still be a reviewed
  delegated member of that same action. The declaration neither grants ownership
  nor selects a controller; direct prerequisites keep their separate order.
- Core tests preserve this behavior after JSON graph recovery and reject missing
  ownership, inferred authority, malformed declarations, foreign connections,
  retained or protected members and an unselected controller. The existing
  same-controller member-to-member case is unchanged.
- Native AKS tests cover rules referencing the group or controller, outside-group
  unindexed references, forged impacts and delayed member absence. SQLite-backed
  Application Insights tests include a managed Web Test with its native hidden
  component link; only the component receives DELETE, and recovery waits for the
  Web Test's native absence. Private test-content changes block mutation.
- The initial focused core/native tests passed (core 0.173s, Azure 1.465s), full
  Go tests passed (Azure 173.388s, GCP 165.544s), and repository-wide vet passed.
  A broad API Management/CDN race selection exhausted the default ten-minute
  package timeout during notification-index tests without a reported data race.
  The narrower cascade/prerequisite regression run passed (Azure 126.073s), and
  core required-deletion plus managed Monitor race tests passed separately
  (core 1.274s, cleanup 4.639s, Azure 18.296s). Those final race runs also included
  the subsequent native-member discovery work under development.
- Counts remain 1,224 operations, 383 types/specs and 357 cleanup bindings. Native
  group-member discovery, global Runbook webhook mapping, remaining service
  families and all eight acceptance criteria remain open at this checkpoint.
  No independent emulator or real-cloud verification was performed.

### Native Monitor discovery inside managed resource groups

- AKS/Monitor workspace group walks and Application Insights managed-workspace
  discovery now supplement generic ARM resources with all ten native Monitor and
  budget collections. Two full observations reconcile LIST with private GETs,
  include group-only budgets, compare membership/configuration and verify any
  members already read through the generic walk. Native identities restrict the
  supplemental members to the exact local resource group.
- Omitted indexed members can follow their verified group controller; unindexed
  members become unresolved ownership requirements that must be scanned before
  planning. A member appearing after review blocks controller DELETE. Failed
  native collections and changed observations cannot authorize empty membership.
  Newly discovered members use existing private configuration, receiver,
  retention/protection and individual residual GET checks after group absence.
- All ten kinds are tested with generic-list omissions, unindexed/late members,
  403/404 collections, changes between native passes and disagreement with the
  generic walk. Group-only budget indexes are explicit. Actual SQLite component
  inventory/graph/plans include an omitted Action Group, component-linked Web Test
  and group-only Cost Management budget; native controller deletion and recovered
  member absence remain separately verified.
- Initial regression failures identified missing resource-group budget responses
  in the shared DNS test fixture. The fixture now uses the existing exact native
  collection responder after explicit resource/list/error overrides. Production
  still rejects failed dependency reads; no 404-as-empty exception was added.
- The final full Go run passed (Azure 171.116s; GCP cached), and repository-wide
  vet passed. Native membership/managed configuration tests passed (2.567s),
  additional SQLite/group discrepancy scenarios passed (4.897s), and the affected
  AKS/Monitor workspace/container/Stream Analytics regressions passed (7.740s).
  Required-deletion/managed Monitor race checks passed (Azure 18.296s), the shared
  cascade regression race run passed (126.073s), and the final managed-controller
  regression race run passed (24.414s).
- Bilingual permission/capability docs and retained evidence READMEs now describe
  native supplemental discovery and individual absence checks. Existing native
  documents, catalog operations and registered specifications are unchanged:
  1,224 operations, 383 types/specs and 357 cleanup bindings. No independent
  emulator or real-cloud verification was performed. The parity matrix still
  names 29 unmapped Azure types; global Runbook mapping, remaining lifecycle/
  service integration and all eight overall acceptance criteria remain open.

### Diagnostic settings native registration and application recovery

- Added resource/subscription diagnostic-setting GET/LIST/DELETE and resource
  category GET/LIST from the immutable Microsoft 2021-05-01-preview sources.
  The catalog now retains 1,232 operations, 217 source/reference documents,
  384 resource types/specifications and 358 cleanup bindings. Twelve unchanged
  native examples retain source digests; schema tests exercise fourteen
  responses and explicitly preserve the documented malformed-ID/null-type
  discrepancies instead of repairing examples to make them pass.
- The registered global source discovers exact source scopes from the ARM
  resource index, expands existing native child adapters and all four Storage
  service scopes, and re-reads saved setting IDs after source/group deletion.
  Resource LIST paging and non-pageable subscription LIST have separate
  contracts. Two observations bind complete private configuration, source
  incarnation, inherited protection, locks and public reference proofs; cursor
  replay rejects changed scope, metadata, membership and configuration.
- Every native source/destination reference and its valid ancestors require
  independently reviewed setting deletion before the referenced resource.
  Native incoming discovery covers unindexed and late settings. Target drivers
  validate saved prerequisite proofs and the setting's own native absence,
  including after restart. AKS/Monitor/Application Insights group membership
  cannot grant diagnostic settings controller-cascade authority. A regression
  exposed and removed prefix-based managed-group ownership re-inference after
  native discovery had excluded an independent setting.
- The server now supplies the Azure service/reference contributor for independent
  ARM assets as well as controller kinds. Actual SQLite scan, graph, plan and
  execution workers exercise registration, global shard selection, saved orphan
  discovery, private-property changes, native DELETE, durable pending state,
  recreated application services and final own-resource GET. Shared targets and
  other settings survive. Source/group disappearance is never a deletion receipt.
- Native requests preserve Cosmos source names and selected response-ID aliases.
  A provider-authenticated selector is bound to its private configuration and
  context before reads, writes or absence checks. The shared inventory worker
  supplies a fixed, deep-copied normalized metadata baseline for known IDs across
  pages. Composed selector tests are not evidence that Monitor supports logs on
  every Cosmos child. Cross-connection/kind/partition data, changed selectors,
  missing proofs and forged prerequisite/receipt state are rejected.
- The final provider namespace controls extension paging API validation. Native
  Monitor lists under API Management, Batch and Stream Analytics parents retain
  their own API version, while each parent's native lists still reject a wrong
  version. Source metadata accepts documented native differences such as budget
  `eTag` spelling and private-workbook label arrays without weakening setting
  identity or protection checks.
- Focused diagnostic/shared-worker tests passed (inventory 0.287s, Azure 3.854s),
  affected race tests passed (inventory 4.662s, Azure 43.539s, server 2.452s),
  repository-wide vet and catalog-source Python tests passed. The registration
  baseline had also passed full Go tests (Azure 186.518s) and race tests (Azure
  38.576s, server 2.477s). The final full Go run covering native-selector changes
  passed as well (Azure 183.249s, GCP 161.195s).
- This closes one mapped type gap; 28 unmapped Azure types and all eight overall
  acceptance criteria remain open. Source discovery still depends on the broad
  ARM index plus implemented child adapters and saved IDs; it cannot enumerate
  every never-seen orphan or replace all omitted native product-root indexes.
  Unknown source kinds remain protected until a native incarnation reader exists.
  Explicit own-GET absence still needs separate scan reconciliation, because
  the custom source has no blanket absence authority. No independent Monitor
  emulator or live Azure run is claimed. Native DELETE has no conditional version
  guard, leaving an external-edit window after final preflight.

### Explicit native absence for bounded inventory sources

- Added final-batch `AbsentNativeIDs` for sources that reconcile known IDs.
  The scan worker validates each claim against its fixed connection/provider/
  kind/partition baseline and all observed pages. Unknown, duplicate, ambiguous,
  observed and partial-page claims fail the shard. This grants no authority over
  other omitted resources and does not change the sources' non-authoritative
  defaults.
- Successful shard completion closes only the confirmed, unchanged assets in
  the same database transaction. Identity/scope changes and newer observations
  survive. Failed/cancelled scans cannot close records; a closure does not create
  a cleanup deletion tombstone. The normal and network scan paths share this
  contract and retain coverage checks.
- Diagnostic settings, shared/private workbooks and historical annotations now
  report saved identities only after their own native GET confirms absence in
  both observations. Parent disappearance and bounded list omissions are still
  insufficient. Workbook/annotation absence sets are compared before region
  filtering and bound into cursors, including empty projected result sets.
- Actual SQLite scan/graph/cleanup/recovery scenarios verify individual external
  deletion reconciliation for all three sources. Shared worker tests cover 22
  identity, paging, cancellation, failure, network and concurrent-update cases;
  transaction tests verify rollback of both asset and shard changes. Source
  tests include permission denial and disappearance/reappearance hidden by a
  region filter.
- Final full Go tests passed (Azure 187.581s, GCP 164.711s), affected race tests
  passed (inventory 6.512s, Azure 37.914s), and repository-wide vet passed.
  Bilingual Azure documentation and native fixture evidence describe the new
  behavior. Catalogs and API sources are unchanged. This closes the explicit
  native-absence follow-up above; remaining discovery/lifecycle/type gaps and
  all eight overall acceptance criteria remain open. These are native-protocol
  and application tests, not new independent-emulator or real-cloud evidence.

### RBAC native contracts and scope boundaries

- Added 11 native Authorization operations: role-definition and assignment
  GET/LIST/DELETE, plus both PIM schedule GET/LIST pairs. Four immutable Swagger
  roots and two common-type dependencies retain source fingerprints. The catalog
  now has 1,243 operations and 223 documents (169 roots, 54 references). Existing
  resource registrations are unchanged at 384 types/specifications.
- Eleven unchanged examples cover all selected operations; their 13 responses
  include 11 schema-validated bodies. Native placeholder identifiers and role
  types remain unchanged in evidence. Fourteen Microsoft CLI recorded responses
  additionally exercise 645 returned roles and 167 assignments, including 109
  inherited assignments. Role definitions use a preview version in recordings;
  those checks establish response compatibility only. Assignment recordings use
  the exact selected version and retain a native DELETE 200 body.
- Native scope/read/index primitives use subscription-local request endpoints,
  recognize inherited tenant/management-group rows without out-of-bound reads,
  and bind role GUID aliases. Private configuration and unknown fields remain
  part of comparison. A recorded built-in role exposed legitimate duplicate
  permission actions; preserving the native values fixed the initial overly
  strict validator. Assignable-scope duplicates remain invalid.
- Protocol tests cover all four read families, native paging/filter/version
  binding, 403/404/partial responses, malformed identities and properties, native
  LIST/GET disagreement, repeated rows/pages and foreign scopes. RBAC/catalog
  binding checks passed (Azure 1.316s), RBAC race tests passed (5.063s), Azure vet
  passed and all five offline source-import tests passed. The previous full
  repository verification belongs to the completed native-absence step above.
- This is a foundation for the mapped RAM policy/role gap, not completed support:
  registered inventory, reviewed role-assignment dependencies, native action
  execution and application recovery are next. No new independent-emulator or
  real-cloud run is claimed. All 28 mapped Azure type gaps and the eight overall
  acceptance criteria remain open.


### Registered RBAC inventory and independent scope cleanup

- Registered global role-definition and role-assignment inventory and native
  independent deletion. The catalog now has 386 types/specifications and 360
  cleanup bindings; the existing 1,243 operations and 223 source documents are
  unchanged. One mapped type gap closes, leaving 27 absent mapped Azure types.
- Native collection/detail, scope, protection and PIM observations participate in
  stable paging and action revalidation. Built-in roles, shared assignable scopes
  outside the connection, unverified scopes, matching PIM schedules, locks and
  protected tags cannot authorize independent deletion. Native Cosmos scope names
  and aliases retain their exact selector through scans and JSON recovery.
- Forward references and complete native reverse indexes require explicit prior
  deletion of assignments before roles, and of referring assignments/custom roles
  before ARM scopes and ancestors. Delegated identity ARM references are included.
  Sources omitted from inventory remain blockers; saved sources omitted from a
  list receive their own GET. Failed reads and late references block final
  readback even when the target has disappeared. Existing Monitor and general
  ARM action families share these checks without replacing their native drivers.
- RBAC extensions never acquire managed-group cascade ownership. An AKS scenario
  with an assignment in the native group index verifies explicit selection,
  assignment deletion, retained controller until prerequisite absence and native
  controller recovery. Scope ownership metadata is authenticated; it does not
  prevent an explicitly reviewed independent authorization deletion.
- DELETE 200 bodies and 204 acknowledgements retain native request IDs and require
  subsequent own-resource GET absence. Private/unknown fields participate in
  configuration comparison but do not leave invocation, log, normalized or Raw
  projections. Wrong identities/scopes, forged prerequisites/receipts, native
  mutation errors and post-delete recreation fail without invented completion.
- Actual SQLite registered scan, scheduled graph, plan and execution workers
  verify role-only blocking, two assignments ordered before the custom role,
  independent jobs, durable pending state, recreated clients/application services,
  JSON recovery and final native readback. Built-in roles and independent scopes
  remain; failed discovery does not close prior observations.
- Focused RBAC, Monitor and diagnostic checks passed. Full Go tests passed
  (Azure 188.584s; final internal guard run 21.843s; unchanged GCP tests cached).
  Affected race checks passed (Azure 101.584s, server 6.429s, architecture guards
  304.411s), repository-wide vet passed and all five offline source checks passed.
- The first full run exposed the architecture checker's 64 KiB line limit in a
  retained native response. Both source checks now read complete lines without
  dropping fixtures or changing forbidden patterns. A 2 MiB response and a
  forbidden query literal at the end of a long source line verify that full
  content is checked. The final full run and guard race test include this fix.
- Bilingual Azure docs and retained native evidence describe supported permissions
  and boundaries. Principal-GUID matching to managed identities/system-assigned
  resource identities, PIM mutation and tenant/management-group administration
  remain unfinished; no independent emulator or real-cloud RBAC run is claimed.
  The native DELETE edit window and all eight full-provider acceptance criteria
  remain open, alongside the remaining mapped families and lifecycle gaps.

### Azure managed identity principal dependencies

- Subscription role-assignment `principalId` values now join to native
  user-assigned managed identities and root system-assigned resource identities.
  Inventory binds the principal/tenant tuple and ARM selector with the connection
  credential; empty identities are bound too. Native target reads reject a
  changed principal, tenant or client identity, and saved evidence remains usable
  after own-resource absence and JSON/client recovery. Location enrichment does
  not change a resource's principal identity.
- Graphs and ordinary/Monitor actions require explicit prior assignment deletion
  even when its ARM scope is in another group. Unindexed or omitted assignments,
  late arrivals, unreadable native identities and missing/altered evidence block
  cleanup. Attached user-assigned identities, application `clientId` values,
  User/Group principal types and other tenants cannot establish ownership of a
  principal. External service principals remain unresolved Uses references;
  there are no Microsoft Graph calls or Entra principal mutations.
- AKS composition covers a system identity on an owned VM. The role assignment
  remains an independent prerequisite before the controller's native cascade;
  ownership does not authorize implicit RBAC deletion. The existing scope
  extension case remains separately covered.
- Actual SQLite registered scan and graph jobs now also include a regional
  user-assigned identity alongside global role definitions and assignments. An
  identity-only plan is blocked. The assignment precedes both the custom role
  and identity in three independent durable jobs. Each job survives application
  and client recreation, pending native deletion and final readback without
  repeating DELETE; built-in roles and independent scopes remain.
- Retained native 2023-01-31 IdentityGet and IdentityListBySubscription examples
  are pinned to Microsoft commit `5da82d5c3687ac3cc845330aaf0f13d3a40ce47e`.
  Source tests verify their original hashes, request bindings and response
  schemas against the selected native document. APIM and Monitor receiver test
  compositions substitute fixture GUIDs for upstream redacted/non-UUID values;
  their retained originals remain unchanged. Native UserAssigned/None responses
  with root principal fields do not acquire system-identity ownership.
- Focused RBAC tests passed (4.965s), affected product regression tests passed
  (21.057s), full Go tests passed (Azure 196.821s; architecture guards 19.490s;
  shared contracts 1.893s; server 3.517s; unchanged GCP tests cached), affected
  race tests passed (26.684s), and repository-wide vet passed. Bilingual Azure
  permissions/capabilities and retained evidence documentation were updated.
- Existing ARM assets require a rescan for principal evidence. Unknown native
  readers cannot prove identity. This pass covers the connected subscription;
  tenant/management-group administration, cross-subscription identity management
  and PIM mutation remain unfinished. No independent identity/RBAC emulator or
  real-cloud run is claimed. Catalog/spec counts and the 27 mapped Azure type
  gaps are unchanged; all eight full-provider acceptance criteria remain open.

### Azure Kubernetes Fleet native contracts and scope boundaries

- Added 22 native Fleet operations from immutable Microsoft stable
  `2026-06-01`: root GET/two LISTs/DELETE; member, namespace, strategy, profile
  and run GET/LIST/DELETE; run Stop; and read-only Gate GET/LIST. The catalog now
  retains 1,265 operations and 225 documents (170 roots and 55 dependencies).
  Resource registrations remain at 386 types/specifications and 360 cleanup
  bindings. This is the foundation for the mapped ACK One/Fleet gap.
- The 22 unchanged original examples retain hashes and operation provenance.
  All requests bind to their selected native operations; 33 responses include
  16 schema-checked bodies. Tests explicitly preserve two upstream null-nextLink
  discrepancies. Placeholder subscriptions, malformed illustrative subnet/
  identity IDs and mismatched request/response names remain in the evidence;
  only composed protocol scenarios replace these with actual-format identities.
- Seven native resource readers and indexes preserve full private authored
  configuration, independently GET listed resources, allow additional GET detail
  and reject changed listed fields. Pagination binds subscription, collection,
  stable version and opaque continuation without filters. Repeated resources/
  pages, malformed rows, incomplete responses and dependency 403/404s fail the
  observation. The shared query guard uses the final native provider/collection,
  so an AKS resource or resource group named `fleets` is unaffected.
- References distinguish existing member AKS clusters, supplied Hub subnets and
  user identities from same-Fleet run/strategy/profile/Gate relationships.
  Namespace Keep/Delete and full placement configuration stay privately bound.
  Fixed member names and dynamic scheduling are distinct; a dynamic selection
  cannot be silently projected as an empty fixed set. Gates expose no mutation.
- Also retained 44 actual Microsoft CLI GET/DELETE responses at immutable commit
  `a20385bffcbb7403846af8a65dbfcaf96c717e4d`. Compatibility checks validate 44
  returned resources across all seven families, nine native DELETE responses,
  five fixed namespace placements and a real 503 HTML failure. Three gates use
  ScheduledStart; the native modelAsString contract permits preserving this
  opaque read-only subtype. Additional placement rollout/scheduling configuration
  remains in the private digest. Recordings use `2026-06-02-preview`, so this is
  response compatibility evidence, not a stable-version live-cloud run.
- The full Go source-baseline run passed (Azure 197.110s; GCP cached), broader
  Fleet/RBAC/prerequisite race checks passed (23.540s), repository-wide vet passed
  and all five offline source-import tests passed. Subsequent query-scope and
  recorded-response changes are covered by final Fleet tests and race checks;
  the final Fleet race run passed (5.210s) and Azure vet passed again.
- Fleet inventory/action registration, reviewed deletion graphs, managed Hub/
  node resource-group reconciliation and actual application recovery remain next.
  This checkpoint closes no mapped family gap: 27 Azure type gaps and all eight
  overall acceptance criteria remain open. No independent Fleet emulator or new
  real-cloud run is claimed.

### Kubernetes Fleet native inventory registration

- Registered all seven pinned Fleet resource kinds and explicit specifications.
  Azure now has 393 registered types/specs, 360 cleanup rules and the same 1,265
  native operations from 225 source documents. Fleet deletion remains disabled
  while the lifecycle driver, managed Hub proof and execution tests are built;
  this inventory checkpoint does not satisfy the Fleet cleanup requirement.
- Added a dedicated non-authoritative, known-ID-reconciling source. Complete
  subscription Fleet indexes and child indexes use native detail reads and two
  private snapshots. Saved child IDs recover omitted parents through their own
  GET; a missing parent cannot close a surviving child. Only two consistent
  native reads of that specific known resource can report its absence. Failed
  scopes, incomplete collections and permission failures preserve prior assets.
- Bound parent configuration, resource-group configuration, protection and
  current references to the full private native configuration and cursor.
  Proxy children inherit the Fleet region; tracked namespaces keep their native
  location. Generic ARM observations use the same native validation and private
  metadata. Authored namespace annotations, placement expressions and future
  settings are omitted from public inventory, Invoke responses and API logs.
- Specs record shared AKS/subnet/identity references, profile-to-strategy links
  and Gate-to-run links without inventing ownership. Native update runs copy
  their strategy; the original strategy and creating profile are provenance,
  not current dependencies. Dynamic namespace placement stays explicit rather
  than becoming an empty fixed target set. The pinned stable member schema
  permits AKS; the broader Arc membership described by current Microsoft docs
  remains unsupported by this selected contract and needs separate work.
- Retained protocol tests for all seven kinds, source/identity restrictions,
  missing and forbidden parent/child reads, private drift, credential rotation,
  cursor resumption and safe logs. A SQLite test exercises registered scan
  creation, both regions, worker reconciliation, persisted spec relationships,
  source-authority narrowing and exact known-resource absence. This is protocol
  and application-worker evidence, not a live Azure run or independent emulator.
- Verification: repository-wide `go test ./...` passed (Azure 198.574s), all
  Fleet tests including the SQLite worker passed under `-race` (11.879s),
  repository-wide `go vet ./...` and all five offline Azure catalog-sync tests
  passed. Shared catalog tests now bind the native Gate UUID selector. Bilingual
  coverage docs explicitly label these seven kinds as inventory-only. All eight
  acceptance criteria and the Fleet lifecycle gap remain open.

### Fleet native asynchronous operation protocol

- Retained 13 representative polling responses from the same two pinned Azure
  CLI recordings, with a deterministic source-hash-verifying reproduction
  script. They belong to five native asynchronous Fleet deletions and preserve
  pending/succeeded status envelopes and final 204 Location responses.
- Bound native signed ContainerService regional operations (2016-03-30) and the
  stable Swagger's unsigned group operationResults route (2022-02-01) to the
  selected subscription, resource group, region, operation UUID and credential.
  Conflicting headers, foreign endpoints, altered versions/signing parameters,
  malformed states and substituted operation IDs are rejected. Refreshed signed
  successors are authenticated and survive serialized execution results.
- Added redaction for the ContainerService `operations` route as well as its
  `operationresults` route. Request signing material and unknown response fields
  stay out of API logs. Polling success or an expired operation's 404 authorizes
  only subsequent native resource readback; neither alone proves deletion.
- Native recorded polling, generated failure cases, serialization and resource
  readback tests passed. All Fleet/AKS/managed-resource/Invoke tests passed under
  `-race` (20.615s); Azure `go vet` and diff checks passed. The full repository
  run from the inventory checkpoint preceded this transport addition. This is
  transport groundwork for the Fleet cleanup driver: the seven registered
  kinds remain inventory-only and the acceptance criteria remain open.

### Fleet native lifecycle graph and incoming dependencies

- Registered native Fleet lifecycle discovery: members, namespaces, runs,
  strategies and profiles require their own cleanup before their Fleet. Gates
  belong exclusively to the run in `target.id`, not the enclosing Fleet ARM
  path. Gate cleanup has no independent DELETE. Full private configuration,
  resource-group/Fleet context, location and recorded references are checked
  in native graph construction and cascade impact authentication.
- Reconciled known children with their own GET when LIST omits them. A native
  Gate can also reveal an unindexed run. Missing runs, unreadable collections,
  private drift, late children and retargeted Gates fail closed. Both native
  observations must agree before publishing ownership or prerequisite edges.
- Added connected-subscription reverse Fleet indexes to the shared target
  deletion boundary. Unindexed members block AKS deletion; native hub subnet
  and user-assigned identity references block their targets. Profiles depend
  on their current strategy, while a run's copied strategy and creating
  profile stay provenance. Source deletion requires the source's own GET;
  neither a missing parent nor LIST omission establishes child absence.
- Dynamic namespace placement is authenticated separately with the saved
  reference proof. It conservatively requires explicit namespace cleanup
  before removing any member of the same Fleet. These are dependency edges,
  not ownership of member clusters or a claimed exact placement set. Shared
  dependencies are not automatically selected. Only a reviewed native Gate
  can be accepted by the target guard as a run-owned incoming source.
- Added failure/omission/change tests, a registered subnet action test with
  native Fleet prerequisite absence and serialized receipt/readback, and a
  SQLite scan/graph-worker test that persists all six native lifecycle
  bindings. The existing transport tests now use the inventory projection's
  authentic Fleet proof for cascade readback. Bilingual docs describe the
  additional read permissions for affected target deletions.
- This checkpoint does not enable Fleet DELETEs. The six independent action
  drivers, durable Stop/DELETE progression, managed Hub ownership/residue and
  namespace-policy execution remain unfinished. Counts stay 393 resource
  types/specs and 360 cleanup rules; the 27 mapped Azure gaps and all eight
  overall acceptance criteria remain open. Evidence is protocol fixtures and
  real application workers, not a new Azure execution or independent emulator.
- Verification after the final changes: repository-wide `go test ./...`
  passed (Azure 196.688s); Fleet, AKS and native target-deletion tests passed
  under `-race` (26.365s); repository-wide `go vet ./...`, all five offline
  Azure catalog-sync tests and diff checks passed.

### Fleet independent child cleanup and persistent Stop/Delete phases

- Enabled native cleanup for Fleet members, managed namespaces, update runs,
  update strategies and auto-upgrade profiles. All requests bind the retained
  stable `2026-06-01` operations and current native `If-Match` ETags. Catalog
  selection, retained resource bindings and all five specs agree; Azure now has
  393 types/specs and 365 native cleanup rules, with unchanged operation count.
- Update runs in Running/Pending/Skipped issue native Stop once. Existing Stopping runs
  are observed, and no run is deleted before native terminal status. The Stop
  operation can finish while the run is still Stopping. Each cleanup phase has
  a signed, serializable receipt bound to its reviewed request, original and
  current operation, polling mode and successor. DELETE receives a fresh phase
  receipt; poll completion or mutation 404 still requires native own-resource
  and reviewed Gate readback. Request/receipt changes fail before API access.
- Kept the complete private configuration, resource-group/Fleet context,
  protection, lock and incoming-dependency checks before mutation. Gate
  approval/skip progress is excluded from authored configuration, while its
  target and subtype settings stay bound. Gate inventory uses the existing
  controller-only semantics so a reviewed Run can remove it; it still has no
  DELETE operation. Known omitted Gates are reconciled by individual GETs.
- Managed namespace Keep/Delete policies are preserved, without policy updates
  or Kubernetes calls. The stable namespace GET example omits its optional
  provisioningState; that exact response remains supported with configuration
  and ETag verification. Documented the different workload consequences and
  native child deletion/Stop permission requirements in both languages.
- Added registered-driver protocol tests for native terminal states, delayed
  Stop, async polling, phase recovery, conditional conflicts, changed private
  configuration/ownership, protected resources, locks, missing ETags, source
  omissions/403/404, mutation-versus-resource absence and residual Gates.
  The real SQLite scan/graph/plan/execution test creates five ordered child
  steps and one Run-owned Gate impact. Restarted workers preserve phases,
  send each mutation once, and only close individually verified absences.
- Fleet root cleanup, managed Hub reconciliation and Arc-enabled membership
  remain unfinished. This checkpoint does not close the mapped Fleet gap, the
  other 26 mapped Azure gaps, or any of the eight overall acceptance criteria.
- Final verification: repository-wide `go test ./...` passed (Azure 198.116s),
  Fleet/AKS/native-target race tests passed (44.358s), repository-wide `go vet
  ./...` passed, and all five Azure catalog synchronization tests passed. The
  native lifecycle state check includes Skipped -> Stop -> Stopped, following
  the published state-transition table rather than treating Skipped as terminal.

### Fleet managed Hub inventory anchors

- Fleet root inventory now joins the native group's `managedBy` with the
  Fleet/AKS public or private API endpoint and the AKS `nodeResourceGroup`
  with its reciprocal native group owner. Exactly one owned hub group and
  one AKS cluster must agree with the Fleet's location. FL_/MC_FL_ naming
  conventions never prove ownership. Hubless resources with unexpected owned
  groups and incomplete/ambiguous joins remain unverified and protected.
- Retained the three anchors and full private configuration digests in a
  request-bound, authenticated inventory projection. Saved anchors recover
  groups and the AKS cluster omitted from a subsequent native LIST; a named
  anchor's own 404 invalidates that ownership join without closing the Fleet.
  Invalid saved receipts fail before API access. Anchors and the Fleet are
  read again after discovery, and the existing two-pass scan and pagination
  fingerprint include the entire authenticated Hub state.
- Full native bodies remain available to ownership validation. Hub-read
  response logs use the Fleet public projection, while inventory stores only
  IDs and private digests. Added public/private/hubless scenarios, ownership
  conflicts, malformed responses, 403/404/202 failures, filtered continuations,
  known omissions, JSON receipt recovery, tampering, private drift and changes
  during or between scan pages. The real SQLite workers also verify persistence
  of an authenticated unverified Hub when its native anchors are unavailable.
- The native AKS schema supplies public/private FQDN and node-group fields;
  these ownership-join test responses are composed protocol fixtures. The
  retained Microsoft Fleet CLI recordings include no Hub AKS or managed-group
  GETs, so this checkpoint does not claim live-cloud compatibility. Complete
  managed descendant inventory, delegation, residue checks and Fleet root
  actions remain unfinished, as does Arc membership. Counts stay 393 types
  and 365 cleanup rules. All 27 mapped Azure gaps and all eight overall
  acceptance criteria remain open.
- The current stable Fleet source has no cluster-mesh profile routes. Native
  preview documentation also requires removing mesh members before profile
  and Fleet-member cleanup. That preview family remains to be modeled before
  claiming complete Fleet coverage; it is not silently covered by these Hub
  anchors or the five stable child drivers.
- Final verification after the Hub log projection: repository-wide
  `go test ./...` passed (Azure 201.081s), Fleet/AKS/managed-resource race tests
  passed (42.653s), repository-wide `go vet ./...` and diff checks passed.
  The focused Fleet/AKS tests also passed, including private Hub log checks.

### Fleet managed Hub descendant inventory

- Verified Hub scans now capture both managed groups and their native resource
  trees, including documented external descendants and Monitor resources absent
  from the generic ARM index. Reused the existing Application Insights managed
  workspace walker, including native product reads, recursive child discovery
  and unfiltered pagination. Only the independently verified Hub AKS can
  introduce its separately inventoried node group; other nested managed
  controllers fail closed until their ownership is reconciled.
- Captured IDs, owning group and complete private configuration digests inside
  the authenticated Hub state. Re-read every known member and the controller
  after discovery; the two-pass scan and page fingerprint cover the member set.
  Unknown contained types remain represented without inventing a product GET.
  RBAC assignments and diagnostic settings remain independent prerequisites.
- Serialized known members recover native LIST omissions with individual GETs.
  Only the named member's own 404 removes it from the captured membership; it
  never closes the Fleet. External recovery requires native ownership evidence,
  including the reciprocal VNet reference on a DNS link. Missing automatic DNS
  records cannot regain ownership from their zone alone. Unknown omitted kinds,
  incomplete native ownership, forbidden reads and asynchronous responses prevent
  scan completion. Full native configurations stay out of Fleet inventory/logs.
- Added a composed 17-member Hub scenario with Uniform scale-set instances,
  extensions, nested network resources, an external disk, VNet/DNS descendants,
  unknown membership and an omitted Monitor action group. Tests preserve shared
  zones/manual records and cover recovery, private changes, typed metadata,
  pagination, incomplete indexes and separate extensions. The actual registered
  scan worker reopens SQLite and recreates clients between scans, preserving
  existing membership after 403 and reconciling only individual native 404s.
- Broader managed-workspace tests caught the native budget `eTag` spelling;
  the shared walker now retains both native ETag spellings in the complete
  digest and validates metadata types without imposing one product's spelling.
- Lifecycle delegation, residual checks, Fleet root cleanup, Arc membership
  and preview Cluster Mesh remain unfinished. This checkpoint does not close
  any of the 27 mapped Azure gaps or eight overall acceptance criteria. Catalog
  counts remain 393 resource types, 365 cleanup rules and 1,265 native operations.
- Final verification after the native ETag compatibility fix and SQLite worker
  coverage: repository-wide `go test ./...` passed (Azure 198.865s), Fleet/AKS/
  managed-group race tests passed (87.352s), and repository-wide `go vet ./...`
  passed. The focused native/managed-workspace tests passed (14.989s), the
  dedicated restarted Hub scan-worker test passed (0.942s), and diff checks passed.

### Fleet Hub lifecycle delegation and graph recovery

- The service lifecycle contributor now authenticates the saved Fleet Hub state,
  verifies the Fleet configuration/context, repeats both native managed-group
  observations and binds the verified Hub AKS, both groups and every observed
  member directly to Fleet. It reuses the strict inventory walker and existing
  native managed-group binding checks; full resource bodies remain transient.
- Fleet Hub ownership takes precedence over the ordinary AKS, service-child
  and VM/NIC attachment contributions. Static membership hints suppress only
  duplicate bindings; the mandatory service contributor authenticates their
  proof before any graph can be persisted, and the AKS contributor authenticates
  before suppressing its Hub walk. A Hub's unsigned node-group hint cannot
  expand Fleet membership. Other AKS clusters and shared DNS resources retain
  their independent relationships.
- Missing inventory members and foreign connection/partition matches remain
  unresolved. Duplicate identities, mismatched normalized/native configuration,
  changed ownership, new or omitted unknown members and incomplete native reads
  prevent graph rebuild. Known omitted members are recovered by their own GET;
  a known member's own 404 requires a refreshed inventory before rebuilding the
  previous graph. RBAC assignments and diagnostic settings remain independent
  prerequisites.
- Added combined-contributor tests of all 17 native/composed Hub members and
  21 boundary cases. A real SQLite graph-worker test persists product-normalized
  assets, reopens storage and recreates the provider for each reconciliation.
  It preserves unique Fleet ownership after native list omission; altered
  receipts and native 403s fail the scan reconciliation without replacing the
  last accepted graph revision or its 17 Hub bindings.
- Root cleanup remains non-actionable. These bindings deliberately do not
  advertise controller verification of member absence until the root driver
  implements residual readback. Fleet root actions, residuals, Arc membership
  and Cluster Mesh remain unfinished; all eight acceptance criteria and all
  27 mapped gaps remain open. Catalog counts stay at 393 resource types/specs,
  365 cleanup drivers, 1,265 native operations and 225 native source documents.
- Verification: `go test ./...` passed (Azure 202.077s); Fleet/AKS/attachment/
  managed-group race tests passed (94.905s); repository-wide `go vet ./...`
  passed. Focused compatibility tests passed (8.752s), the combined graph and
  restarted SQLite worker tests passed (2.809s), and diff checks passed.
  The Hub responses remain composed protocol evidence, not live-cloud proof.


### Fleet Arc member references and unregister recovery

- Fleet members now accept the documented AKS and Arc Kubernetes cluster ARM
  identities, including foreign subscriptions. Strict root-resource/type and
  casing checks reject nested extensions, malformed paths and other resource
  families. Full private configuration and reference proofs bind the selected
  cluster before graph contribution or cleanup.
- Arc enrollment uses the existing registered member inventory, explicit
  namespace prerequisites and conditional DELETE driver. It owns neither the
  cluster nor its Kubernetes extensions. Graph contributions retain missing or
  foreign cluster references and explicitly order member removal before a
  referenced cluster; reverse native discovery also recognizes unregistered Arc
  type casing and requires each known member's own absence.
- Retained tests cover registered inventory/graph/DELETE, serialization and
  driver recreation, delayed member disappearance, idempotent recovery,
  namespace ordering, foreign/duplicate targets, cluster retargeting and altered
  proofs. The twelve reverse-source scenarios now run for both AKS and Arc.
- The Swagger's ARM-ID annotation still names only AKS, including in the
  inspected preview document; Microsoft's current quickstart explicitly names
  Arc connectedClusters. The original source remains unchanged. This is
  documented protocol compatibility with composed Arc responses, not retained
  Arc cloud recordings or emulator evidence.
- Fleet root actions, Hub residual checks and Cluster Mesh remain unfinished.
  All eight acceptance criteria and all 27 mapped Azure gaps remain open.
  Catalog counts stay at 393 resource types/specs, 365 cleanup drivers,
  1,265 native operations and 225 native source documents.
- Verification: repository-wide `go test ./...` passed (Azure 203.686s),
  Fleet/AKS/attachment race tests passed (73.358s), and `go vet ./...` passed.
  Focused Arc/reference tests passed (1.051s), and diff checks passed.

### Fleet Cluster Mesh inventory, disconnection and cleanup

- Registered Cluster Mesh profiles with native inventory, applied-member
  references and conditional cleanup. Five unchanged native Mesh operations
  and member GET/LIST use the pinned `2026-06-02-preview` document; other Fleet
  operations retain `2026-06-01`. The catalog now has 394 resource types/specs,
  366 cleanup drivers, 1,270 native operations and 226 source documents.
  All 27 retained Fleet examples, 41 responses and 21 bodies are source-bound
  and schema-checked; the catalog and recording generation are reproducible.
- Inventory joins actual member mesh associations using unfiltered native
  lists and individual GETs. Labels alone cannot establish attachment. Known
  omitted profiles/members are recovered through native reads, and surviving
  orphan associations cannot become absence. Configuration, Cilium association
  and last-applied selection are privately authenticated. Mesh cleanup is an
  explicit prerequisite for member removal, without owning members or clusters.
- Cleanup waits for an active Apply, sets an empty selector while preserving
  other authored configuration, applies disconnection, verifies zero attached
  members, and deletes the profile. Each conditional phase and configuration
  receipt survives serialization and provider/worker recreation. HTTP 200
  Applying, LRO success and DELETE acknowledgment never substitute for native
  resource and member readback. Protected members, missing permissions, changed
  configuration, new attachments and forged receipts block progression.
- Retained 19 native responses from Microsoft's pinned CLI Mesh recording,
  including PUT 200/201, Apply 200/Applying, DELETE 204, a real HTML 503 and the
  failed ConnectivityTimeout connection attempt. Native disconnection updates
  the selector, replaces createdAt, applies and reaches NotConnected. This is
  response evidence, not a recorded Connected network. Individual post-disconnect
  member reads and final profile 404s are composed protocol evidence.
- A real SQLite scan/graph/plan/execution test requires both namespace and Mesh
  cleanup before member deletion. A fresh provider and worker resumes every
  phase, performs exactly one PUT/Apply/DELETE for Mesh, verifies independent
  resource absence and redaction, then rescans the retained Fleet and unrelated
  resources. Other tests cover association drift, list omission, profile/parent
  absence with surviving members, synchronous/asynchronous operation envelopes,
  delayed disappearance and conditional failures.
- Verification: `go test ./...` passed (Azure 205.624s), Fleet/AKS/attachment
  race tests passed (94.262s), and `go vet ./...` passed. All Fleet tests passed
  (9.923s), the native recording test passed (0.402s), and the restarted Mesh
  execution test passed (1.736s). Five catalog importer tests, deterministic
  catalog generation, native recording reproduction and diff checks passed.
- Fleet root deletion and managed Hub residual verification remain unfinished.
  All eight acceptance criteria and all 27 mapped Azure gaps remain open;
  these protocol and native-response tests do not establish emulator or
  live-cloud acceptance.

### Fleet root cleanup and managed Hub residual recovery

- Enabled the native Fleet root DELETE for verified Hub ownership and verified
  hubless Fleets. Unverified ownership remains protected. The existing stable
  operation now appears in the root spec and generated resource binding;
  Azure has 394 types/specs and 367 cleanup rules, with 1,270 operations and
  226 retained source documents. Catalog regeneration and selected metadata
  remain deterministic without modifying the native operation schemas.
- Root inventory also authenticates the native child/Gate identity manifest.
  Named GETs recover omitted known children and gate-referenced runs. Before
  root deletion, two complete observations require all independent children
  absent and exactly the reviewed Hub members/ownership/configuration. Full
  authored settings and creation identity remain privately bound while ETags,
  deletion progress and modification audit fields may change.
- The driver sends only Fleet DELETE for the Hub cascade, with the current
  native If-Match condition. Root absence still requires every known Fleet
  child, Gate, managed group and typed Hub descendant's own GET/404, including
  external managed disks and DNS resources. Unknown contained types rely only
  on their owning group's absence. Existing Deleting state and persisted native
  operation receipts resume without issuing another DELETE.
- Root review rejects missing, extra, foreign, duplicate, retained and forged
  impacts before API access. Native membership changes, locks, protected tags,
  unreadable resources and conditional conflicts block cleanup. Managed Hub
  Monitor references retain their verified internal ownership; new/external
  rules and diagnostic extensions remain independent blockers even after the
  root disappears. Shared managed-group checks now additionally require a
  synchronous, valid external group read and honor its protection tags.
- The registered SQLite test scans eight Fleet kinds across 16 region shards,
  normalizes the composed Hub resources and runs the native graph worker.
  Selecting Fleet creates seven separate deletion steps and 18 impacts: six
  independent child steps before the root, 17 Fleet-owned Hub members and a
  Run-owned Gate. Retaining the Hub blocks the plan. Every retry reopens SQLite
  and recreates the provider/registry/worker. Exactly seven conditional DELETEs,
  delayed Gate/Hub disappearance, both groups gone with an external disk still
  present, final impact closure and four retained shared resources are verified.
- Fleet root proof records, operation journals and API logs exclude full Hub
  configuration. Individual assets retain their existing product inventory
  projections in the reviewed plan. Bilingual docs describe root eligibility,
  native deletion/read permissions, reviewed prerequisites, retention and
  residual behavior. The Hub responses remain composed evidence; retained CLI
  recordings establish native Fleet DELETE/poll compatibility, not complete
  live Hub ownership or post-delete absence observations.
- Verification: `go test ./...` passed (Azure 209.165s), Fleet/AKS/attachment
  race tests passed (165.800s), and `go vet ./...` passed. All Fleet tests passed
  (20.735s); the final Root/AKS focused check passed (8.629s). Five catalog
  importer tests, deterministic regeneration and diff checks also passed.
- A fresh matrix comparison finds 26 mapped Azure types still without rules.
  Fleet now has its root cleanup path, but all eight overall acceptance
  criteria remain open, including other lifecycle work, independent emulator
  and full application/publication verification. No live Azure deletion is
  claimed by this checkpoint.

### DomainRegistration discovery and delayed cleanup

- Added native global registered-domain and ownership-identifier rules using
  Microsoft's stable `2024-11-01` DomainRegistration contract. The catalog now
  has 396 types/specs, 369 cleanup rules and 1,277 operations from 172 root and
  56 reference documents. Seven unchanged upstream examples retain immutable
  source URLs and hashes; all seven requests and nine responses (five bodies)
  are checked against the retained schemas. The upstream GET's extra example
  parameter and DELETE's hard-delete example remain documented and unchanged.
- Inventory privately binds creation, renewal, contacts, authorization, DNS
  configuration and ownership identifiers. Two native dependency observations
  enumerate independent app/slot bindings and identifiers; known omitted entries
  receive their own reads. Private parent changes invalidate child cursors even
  without an ETag. App/slot hostname bindings establish deletion order without
  owning the application. DNS-zone deletion requires explicit registration
  selection or repointing; registration cleanup preserves its DNS zone.
- The registered driver deletes reviewed prerequisites first and preserves
  `forceHardDeleteDomain=false` and the native 24-hour delay. Saved hostname
  and deletion phases survive provider and worker recreation. The former waits
  only for reviewed, independently absent app bindings to leave the native
  index; unknown assignments block deletion. The 48-hour waiter requires root
  and known-child GET absence, including children surviving an absent root.
  Configuration, protection, permission and plan/receipt changes fail closed.
  A saved delete receipt or native Deleting state prevents ordinary repeat
  deletion. The API has no If-Match condition; external-write atomicity and
  exactly-once mutation across the write/receipt crash window are not claimed.
- A real SQLite test scans 21 global/regional shards into 14 native assets,
  runs graph reconciliation and creates four deletion steps with three
  independent prerequisites and no cascade impacts. Each retry reopens SQLite
  and recreates the registry/provider/worker. It verifies durable write intent,
  delayed hostname cleanup, native deletion after the hostname phase, a wait
  clock beyond 25 hours, surviving children, final closure of four assets and
  ten retained assets. Private domain settings do not enter plans, journals or
  API logs. A shared App Service fix permits sibling binding deletion after
  Azure updates the parent's read-only hostname indexes while preserving checks
  on parent authored settings and each binding's own configuration.
- Verification: `go test ./...` passed (Azure 214.454s), the targeted
  Domain/AppService/DNS/network/attachment/AKS race check passed (67.720s), and
  `go vet ./...` passed. The final focused domain/AppService/DNS suite passed
  (6.156s). Five offline importer tests, deterministic regeneration and diff
  checks passed. Bilingual capability/permission docs and fixture provenance
  distinguish native schema examples, composed protocol tests and real SQLite
  worker execution. Inspected Azure CLI domain recordings contain availability
  and agreement calls rather than registration CRUD; no independent domain
  emulator or live deletion is claimed.
- A fresh matrix comparison finds 25 mapped Azure types still without rules.
  All eight overall acceptance criteria remain open, including remaining
  lifecycle work, independent emulator/full application checks and publication.


### Communication and Email native inventory, lifecycle and cleanup

- Added ten native kinds: Communication accounts, SMTP usernames, purchased
  phone numbers, number reservations, rooms, Email resources, domains, sender
  usernames, suppression lists and addresses. Azure now has 406 types/specs,
  379 cleanup rules and 1,311 operations from 175 root and 61 reference
  documents. ARM uses `2026-03-18`, phone APIs `2025-06-01`, and Rooms
  `2025-03-13`. All 34 selected operations, 56 example responses and 36 bodies
  retain immutable upstream provenance and native contract checks. Catalog
  generation remains deterministic and offline.
- Registered inventory uses separate ARM and Communication Entra audiences and
  establishes each data endpoint through its owned ARM account. Two complete
  native observations cover descendants and room participants. Omitted known
  resources require named GETs; parent/list absence never establishes child
  absence. Private configuration, SMTP, recipient, verification and participant
  fields are authenticated without exposing them in inventory or API logs.
- Account deletion releases its reviewed phone numbers. Other modeled children
  are direct deletion prerequisites, ordered before their parents. Linked email
  domains require explicit account selection or prior unlink and rescan. Reverse
  checks cover the connected subscription and known omitted accounts, not all
  subscriptions. Linked Notification Hubs and assigned identities remain
  independent references; malformed or changed links fail review.
- Native DELETE, signed ARM polling, signature rotation, relative phone release
  receipts and the final absence phase survive serialization and provider/worker
  recreation. Operation success or an expired operation cannot replace the
  selected resource and every recorded descendant's own GET/404. Participant
  collection failures cannot become room absence. Account/phone verification
  allows 40 days, with hourly absence checks after operation completion; this
  bounds verification and makes no claim about when charges stop.
- The real SQLite scan/graph/plan/execution test scans ten global shards and
  executes nine native DELETE steps with one account-owned phone impact. Every
  retry reopens SQLite and recreates runtime/registry/worker. It verifies durable
  intent, retained assets while an account is gone but its phone remains, a
  simulated delay beyond 32 days, final closure and no repeated persisted
  DELETE. Unit tests cover graph and action drift, missing permissions, locks,
  protection, forged identities/receipts, list omissions and surviving orphans.
- Retained 134 responses from Microsoft's pinned CLI recordings, including
  unchanged ARM/phone DELETE receipts, data responses and signed polling. The
  polling replay checks three native DELETE headers and 45 operation responses
  (29 nonterminal); these include creation/update polls as well as deletion.
  Current-version worker responses are composed protocol evidence. No complete
  live cleanup, independent ACS emulator, atomic external-write exclusion or
  exactly-once write across a crash before receipt persistence is claimed.
- Account deletion also irreversibly removes associated application data.
  Chat/Identity data and Event Grid filters are not individually inventoried
  lifecycle impacts here. Notification Hubs are references, not registered
  cleanup kinds. These rules do not establish full ACS or SMS-template parity.
  A fresh matrix comparison leaves 24 mapped Azure types without rules. All
  eight acceptance criteria remain open, including remaining families,
  lifecycle work, independent emulator and full application/publication checks.
- Verification: repository-wide `go test ./...`, Communication race tests
  (74.940s), and `go vet ./...` passed. Focused Communication/spec tests passed
  (8.985s / 0.541s), all five offline importer tests passed, catalog regeneration
  was byte-identical, and diff checks passed. These results do not close the
  remaining acceptance scope above.

### Data Factory native inventory, lifecycle and cleanup

- Added fourteen native rules and 54 operations at `2018-06-01`: factories,
  pipelines, datasets, dataflows, linked services, credentials, global parameters,
  triggers, CDC, integration runtimes, node registrations, managed virtual
  networks, managed private endpoints and inbound private connections. Thirteen
  rules have native DELETE; managed virtual networks require factory cleanup.
  Azure now has 420 rules, 392 cleanup actions and 1,365 operations from 176 root
  and 61 reference documents. Previously retained source documents are unchanged.
- Complete native family observations preserve the factory's region and the
  node's original spelling. Node discovery uses native POST GetStatus. Known
  identities and descendants receive their own reads despite list omission;
  failed or inconsistent families cannot reconcile absence. Private authored
  configuration and work content stay out of public assets, plans and logs.
- Native discriminator-aware reference shapes cover 506 schema structures.
  Typed artifact references order consumers without treating arbitrary JSON,
  scripts or parameters as dependencies. Factory cascades can contain artifact
  cycles; independently selected cycles, retained children and missing inventory
  block the plan. Triggers, CDC, SSIS and linked runtimes are direct prerequisites;
  SSIS consumers precede runtime Stop and deletion.
- Pipeline-run queries preserve the full service-visible window and all pages,
  and known run IDs receive independent GETs. Debug sessions have their native
  paged query. Factory cleanup cancels reviewed runs individually and deletes
  reviewed debug sessions; pipeline-only cleanup cancels only that pipeline's
  reviewed runs. Other standalone artifacts wait for active factory work. New
  work, changed identities or private configuration require a fresh review.
- Key/RBAC shared runtimes use native host links, factory indexes and own reads.
  Independent consumers require explicit selection. RemoveLinks applies only to
  the reviewed consumer factory after every required runtime is absent. A stale
  link to a deleted factory is resolved only through its signed recorded identity
  and independent factory/runtime 404s. Foreign or ambiguous links block cleanup.
- Trigger/CDC Stop, event unsubscription, SSIS status and final-result polling,
  run cancellation, debug deletion, link removal and DELETE retain signed durable
  receipts. Accepted operations are not replayed after persistence. Final reads
  include every recorded descendant and required external consumer, even after
  the host disappears. A regression test exposed and fixed omitted external
  prerequisite readback after host deletion. Verification has a 24-hour bound;
  it makes no billing, atomic-concurrency or crash-before-persistence guarantee.
- The real SQLite scan, graph, plan and execution workers use generated asset
  IDs. Each resumed job reopens the database and rebuilds runtime/registry/worker.
  Seven-step SSIS and five-step shared-work plans each execute eight mutations,
  retain assets until native absence, and verify durable receipt transitions.
  Separate tests cover scan authority, protections, forged proofs, new consumers,
  incomplete work queries, late reappearing prerequisites and surviving orphans.
- The [Data Factory evidence](providers/azure/fixtures/datafactory/README.md)
  retains 54 unchanged examples with 76 responses/35 schema-validated bodies and
  64 original CLI responses from two checksum-pinned sources. Recorded Cancel
  and CDC string bodies and the empty final Stop result are narrowly supported;
  original credential/private-connection identity defects remain explicit.
  Floci-AZ's current documented services do not establish Data Factory support.
  Protocol/recording verification is distinct from independent emulator and
  live-cloud acceptance. External data stores and self-hosted machines remain
  independent; masked secrets and missing creation fields limit change detection.
- A fresh parity comparison leaves 22 mapped Azure types without rules; GCP has
  no missing mapped type, which does not establish behavior-level equivalence.
  All eight acceptance criteria remain open, including remaining lifecycle work,
  independent emulator/full application verification and publication.
- Verification: final repository-wide `go test ./...` passed; all Data Factory
  race tests passed (401.244s), and the final deterministic worker/reappearing
  prerequisite race tests passed (70.914s). `go vet ./...`, all five offline
  importer tests, byte-identical catalog regeneration, the 506-shape reference
  check, 64-response original recording reproduction and documentation checks
  (40 chapters / 10 screenshots) passed. These checks do not close the broader
  acceptance criteria above.

### Data Migration native inventory, lifecycle and cleanup

- Eight native rules cover classic services, projects, tasks, files and service
  tasks, SQL/Mongo services, and database migrations across five target routes:
  SQL server, SQL managed instance, SQL virtual machine, Cosmos Mongo RU account
  and Mongo vCore cluster. Forty-five DMS operations use `2025-06-30`; two
  supporting SQL target GETs add 47 operations in total. Azure now has 428 rules,
  400 cleanup actions and 1,412 operations from 180 root and 101 reference
  documents. All previous 1,365 operation definitions remain unchanged.
- Native catalog selection retains task discriminator subtypes with reverse-only
  references in separate files. The importer identifies the innermost operation
  provider for target-scoped extensions and preserves SQL VM name constraints.
  Deterministic generation and offline schema checks retain the pinned source
  definitions, without using mock schemas as the contract.
- Inventory rereads complete classic trees, modern service indexes, independent
  Mongo target indexes and every known own-resource identity. SQL has no native
  target-scoped migration list in the selected contract. Missing list entries or
  parents cannot prove known resource absence; forbidden or incomplete context
  fails the scan. Service execution regions remain separate from target/group
  regions. Private signatures bind authored inputs, targets, groups, ancestors,
  typed references, nodes and protection across pagination, graph and actions.
- Native subnet/NIC, target, storage, identity and schema-file fields contribute
  dependencies. Supporting SQL MI target reads carry their subnet into network
  scope; SQL VM target reads carry the underlying compute VM. Neither those
  fields nor migration service links assign ownership of independent targets.
  Arbitrary scripts, SQL text, connection strings and opaque input objects are
  excluded from dependency discovery and public logs.
- Classic services/projects require reviewed immediate-child deletion. Active
  tasks cancel first; subsequent DELETE uses `deleteRunningTasks=false`. Shared
  schema-file consumers and modern service-linked migrations require explicit
  prior deletion, with reviewed classic ancestor expansion handled consistently.
  SQL migrations cancel using the recorded migration operation ID. Active Mongo
  migrations use native force deletion because there is no separate Cancel API.
  SQL services wait for zero running node jobs, remove the reviewed runtime
  registrations, verify their absence and then delete the service.
- Accepted cancellation, node-removal and deletion phases remain durable across
  retries. SQL Cancel HTTP 200 can include an async receipt. Polling preserves
  exact returned URLs, uses ARM header precedence and verifies subscription,
  native operation family, phase and UUID. Old `2021-06-30` polling is accepted
  only for a fully signed classic service-delete receipt. Generic and typed
  operation paths have separate checks; no operation 404 proves resource absence.
  Every recorded descendant and required consumer still needs its own readback
  after the parent disappears. Changed private inputs, protection, targets,
  groups, locks or nodes stop further mutations. Verification is bounded to
  24 hours and does not guarantee atomic exclusion of external changes.
- The real SQLite inventory/graph/plan/execution workers scan 16 regional shards
  and execute a 12-step plan through 54 fresh worker invocations. They verify
  durable intent, generated asset IDs, five cancellations, two node removals,
  12 deletions, final absence before closure and no repeated persisted mutation.
  Tests also cover known-list omissions, failed scans, forged receipts, expired
  polls, retained or protected children, new migrations, surviving orphans,
  passive running work and unchanged independent targets.
- The [Data Migration evidence](providers/azure/fixtures/datamigration/README.md)
  retains 56 unchanged official examples, 76 responses/43 schema-validated bodies
  and 107 original CLI response bodies from four checksum-pinned sources.
  Native example identity, scope, parameter and enum inconsistencies remain
  explicit. Signed URL values are replaced with deterministic replay values;
  response bodies are unchanged. These are protocol, recorded and SQLite worker
  checks. Floci-AZ's documented service list does not establish DMS emulation;
  independent DMS emulator and live-cloud acceptance remain open. Secret masking
  and absent conditional DELETE versions limit change detection; no source-data,
  backup-erasure, billing-stop or crash-before-receipt guarantee is made.
- The DTS mapping now includes classic tasks and modern services/migrations as
  functional candidates, with its behavior-level verification still pending.
  A fresh comparison leaves 21 mapped Azure types without rules; GCP has no
  missing mapped type. All eight acceptance criteria remain open, including
  lifecycle gaps in existing families, independent emulator/full application
  verification and publication.
- Verification: repository-wide `go test ./...` passed (Azure 293.286s); all
  Data Migration race tests and the shared Data Factory worker regressions passed
  (206.621s). `go vet ./...`, six offline importer tests, byte-identical catalog
  regeneration, the 506-shape Data Factory reference generation, 107-response
  original recording reproduction and documentation checks (40 chapters /
  10 screenshots) passed. No acceptance criterion is closed by these checks.
  The four added root documents and 43 dependency documents were also reproduced
  from their checksum-pinned official sources. The independent-target gap found
  in this foundation is addressed by the follow-up below.

### Data Migration incoming references on independent targets

- Independent SQL/Cosmos, network, compute, storage, identity and containing
  resources now use native incoming DMS discovery through the shared target
  action guard. Complete classic and modern walks supplement the original
  forward edges, including known source/ancestor GETs when lists omit them.
  Native SQL DB/MI `targetDbName` also identifies the specific target database.
- Indexed migrations contribute authoritative, explicitly selected deletion
  prerequisites. Unindexed sources block target cleanup; migration references
  never grant target ownership. Source context stays bound to its own signed
  connection, while foreign connection/partition graph targets remain unresolved.
  Private input/reference changes, unreadable native context or changes during
  the repeated observations fail closed.
- Full registered lifecycle/planner tests verify target-only blockers, migration
  ordering and persisted prerequisite assets. The registered SQL target driver
  requires each reviewed source's own 404 before deletion, restores a serialized
  request and deletes once. Resumed verification rejects both a reappearing
  source and a new migration after the target itself disappears. Additional
  cases cover all five target routes, exact SQL database references, classic
  references to containing resources and opaque script exclusion.
- Existing isolated product fixtures explicitly compose empty native DMS indexes
  with validated versions/scopes. Real DMS fixtures retain their native responses
  before that fallback. Incoming discovery adds subscription-wide migration
  read requirements to independent target cleanup; the bilingual Azure guide
  and retained fixture documentation explain these permissions and behavior.
- Resource/catalog counts and the 21 missing mapped Azure types are unchanged.
  This follow-up remains protocol and registered-runtime evidence. Independent
  emulator/live-cloud verification and all eight overall acceptance criteria
  remain open.
- Verification: repository-wide `go test ./...` passed (Azure 297.583s);
  all DMS, shared native-target and Data Factory registered-worker race tests
  passed (215.651s). `go vet ./...`, documentation checks (40 chapters /
  10 original screenshots) and `git diff --check` passed. The 20 existing
  product scenarios affected by overly strict external connection/partition
  checks also passed after the native source context was bound independently;
  no existing blocker or mutation expectation was relaxed.

### Defender for Cloud native service-state inventory

- `Microsoft.Security/pricings` now models subscription protection plans and
  resource-level plan state. The native fields preserve Free/Standard tier,
  sub-plan, trial duration, enablement time, extension status, deprecated and
  replacement plans, enforcement, inheritance and actual resource coverage.
  Subscription Standard and partial coverage can coexist with a resource's Free
  override. Trial countdown does not invalidate an otherwise stable scan.
- This is the read-only service-state equivalent of Alibaba Cloud Security
  Center's `DescribeVersionConfig`. No protection-tier write or resource override
  removal is exposed as cleanup. The catalog retains all four native Pricing
  operations plus supporting Arc machine GET/List for reproducibility. Arc
  machine inventory/lifecycle remains an unfinished mapped family.
- Native VM, VMSS, Arc, AKS and ACR indexes provide resource scopes; each source
  gets its own native read. Pricing Lists cover subscriptions and the machine
  scopes, while AKS/ACR use a named Containers GET. ACR read support is an
  inference from its native configuration example and generic GET route, pending
  original recording/live verification. Complete index discovery does not depend
  on the broad ARM resource list. Parent continuation rejects filtering, version
  changes, foreign hosts and duplicate members.
- Known plans and omitted parents are reread individually. A typed inheritance
  reference can recover an omitted subscription plan. Neither parent nor list
  absence proves a known plan absent. Repeated observations and pagination bind
  native configuration, parent creation context and the scan request. Signed
  references contribute resource/inheritance `uses` edges without ownership or
  cleanup prerequisites. Read-only plan graph construction does not require
  unrelated incoming-deletion API permissions.
- Private extension parameters, future authored settings and operation messages
  stay out of public inventory and API logs; extension operation codes remain
  visible. The [Defender evidence](providers/azure/fixtures/security/README.md)
  retains 18 unchanged official examples with 26 responses/22 bodies. It records
  the native wrong-case filter and eight non-nullable-schema/null-response
  discrepancies explicitly, without modifying the retained examples.
- Real SQLite global scan and graph workers produce seven generated plan assets
  and five inheritance edges. Persisted read-only capabilities, native request
  provenance, rejected forged references, repaired known-list omissions, failed
  permission scans and own-404 reconciliation are covered. These remain protocol
  and worker checks; Floci-AZ does not document Defender inheritance/coverage
  emulation. Independent emulator and live-cloud verification stay open.
- Azure now has 429 rules, 400 cleanup actions and 1,418 operations from 182 root
  and 101 reference documents. All previous 1,412 operations and source fragments
  are unchanged. Twenty mapped Azure types remain without rules; GCP still has
  behavior gaps, including security-center service state. All eight overall
  acceptance criteria remain open.
- Verification: repository-wide `go test ./...` passed (Azure 303.743s).
  Defender and catalog race tests passed (16.136s); the subsequently added
  application network-closure check also passed under the race detector
  (3.649s), preserving the selected resource's plan without pulling unrelated
  subscription plans into its network. Final `go vet ./...`, six importer tests,
  byte-identical catalog/506-shape Data Factory regeneration, all 18 online
  pinned example reproductions, documentation checks (40 chapters / 10 original
  screenshots) and whitespace validation passed.

### Arc HybridCompute native contract foundation

- Added 16 native operations to the two machine reads already used by Defender:
  machine deletion/resource-group List, extension/Run Command/license-profile
  GET/List/DELETE, supporting license reads, network-profile GET and hybrid
  identity metadata GET/List. All 1,418 prior operations and source fragments
  remain unchanged; no resource mapping, inventory rule or cleanup action was
  added. Azure remains at 429 rules and 400 actions, with 1,434 operations from
  182 root and 101 reference documents. All 20 missing mapped Azure types and
  all eight acceptance criteria remain open.
- The [Arc evidence](providers/azure/fixtures/hybridcompute/README.md) retains
  19 unchanged, pinned 2025-01-13 REST examples with 24 responses and 15 bodies.
  Contract tests assert two undeclared List parameters, two extension-level enum
  mismatches and exactly 138 non-nullable-schema/null-value discrepancies across
  eight bodies before adapting only those known values in memory. Native source
  metadata, raw examples and their hashes are preserved.
- Twenty-five unmodified interaction extracts from three official CLI recordings
  exercise shared transport, 429 classification, signed URL preservation and
  log redaction. Five accepted deletes use distinct `operationstatus` and
  `operationresults` URLs with intermediate signature changes; terminal result
  reads are empty HTTP 200 and need explicit transport opt-in. These recordings
  retain API version 2026-07-15 and contain no final resource-own 404. They do not
  establish 2025-01-13 lifecycle parity or completed resource deletion.
- Official guidance requires extensions to be removed before agent disconnect,
  distinguishes cloud deletion from local agent cleanup, and identifies Azure
  Local VM deletion consequences. Run Command deletion terminates running
  scripts. Shared ESU licenses are referenced by machine profiles, not owned by
  a machine. These behaviors must govern the unfinished native inventory,
  planning, cleanup and restored worker implementation. The Arc ENS mapping
  remains pending; cloud-record absence is not physical-server release.
- Floci-AZ's service and ARM documentation was rechecked on 2026-09-13. It does
  not document Arc agent/extension removal or signed HybridCompute polling;
  generic ARM fallthrough does not close independent-emulator acceptance.
- Verification: repository-wide `go test ./...` passed (Azure 305.753s), including
  the native contract test. Arc/Defender/catalog race checks passed (18.092s);
  the subsequently added recorded transport test and native contract test
  passed together under the race detector (5.428s). Native inventory, cleanup,
  planning, final own-resource readback and resumed execution remain unfinished.
  `go vet ./...`, six offline importer tests, byte-identical regeneration of both
  generated Azure files, online reproduction of all 19 examples and all 25
  extracts from three complete CLI recordings, documentation checks (40 chapters
  / 10 original screenshots) and whitespace validation also passed.

### Arc HybridCompute native inventory and reconciliation

- Five regional rules now inventory machines, extensions, Run Commands,
  machine license profiles and shared ESU licenses through native product
  Lists and individual GETs. Child discovery enumerates and reads native
  machines first. Known children recover omitted parents; known resources are
  reread even when absent from lists. Only their own 404 supports closure.
- Native continuation checks reject filtering, foreign pages, version changes,
  duplicate members and wrong parents. Two observations bind private resource
  configuration and every parent, including parents with empty collections.
  Scan continuation also binds the connection, scope, kind, known identities
  and bundle revision. Null connection status remains absent rather than
  becoming Connected. Read failures and partial responses fail the scan.
- Public fields retain operational machine, extension, command and license
  state while scripts, extension settings, protected parameters, agent proxy
  settings and future authored fields remain private. API diagnostics preserve
  method, path, API version and request IDs without signed query material.
  Authenticated references contribute four native `uses` relationships in the
  worker fixture; shared licenses and external cluster/private-link references
  do not become owned resources. Missing references remain unresolved.
- Real SQLite scan and graph workers cover five assets across ten regional
  shards, reject a saved authoritative flag, preserve all five assets when all
  indexes omit them, preserve assets after permission failure, and close only
  the command whose own GET becomes 404. Tests also cover parent pagination,
  malformed identities/properties, missing parents, forged graph proofs,
  filtered pages, and private configuration changes across scan continuations.
- Cleanup remains explicitly unavailable for these five types until the native
  lifecycle driver is implemented. This is an incremental inventory milestone,
  not fulfillment of the lifecycle requirement: agent/extension cleanup,
  running-command impact, ownership/locks, license handling, restored execution
  and final native readback remain unfinished. The ENS parity row and all eight
  overall acceptance criteria remain pending. Nineteen mapped Azure types still
  lack rules; Arc now has rules but remains incomplete behaviorally.
- Azure has 434 rules, 400 cleanup actions and the same 1,434 operations from
  182 root and 101 reference documents. All prior operations are unchanged.
  Bilingual capability and permission documentation distinguishes available
  inventory from unfinished cleanup.
- Verification: repository-wide `go test ./...` passed (Azure 303.798s).
  Final Arc/Defender/catalog race checks passed (24.256s), including the later
  request-log projection and additional boundary cases. Final `go vet ./...`,
  six offline importer checks, byte-identical regeneration of both generated
  files, documentation checks (40 chapters / 10 original screenshots) and
  whitespace validation passed. Independent emulator and live-cloud evidence
  remain open.

### Arc HybridCompute deletion receipts and resumable native polling

- The four catalog DELETE operations now validate native response statuses,
  callback roles and operation identity; accepted Run Command deletion requires
  its final Location callback. Synchronous and asynchronous receipts bind the
  exact resource and credential configuration before they can be restored.
- Native polling preserves complete signed URLs and accepts signature rotation
  only for the same subscription, provider, region, UUID, API version and saved
  URL role. Status success advances to the saved result URL. Empty final 200/204
  completes the operation only; operation 404, permission failure, redirects,
  malformed responses and failed/canceled states cannot prove resource absence.
  Returned progress is authenticated for persistence and leaves the previous
  receipt unchanged.
- A clearly labeled cross-version adaptation replays five deletions and 19
  polls from the pinned Microsoft CLI recordings, serializing the receipt and
  creating a fresh runtime at each step. Only the subscription and API version
  are adapted in memory, and requests follow newly returned signatures. The
  unmodified transport/provenance tests remain separate. Boundary tests cover
  forged phases, changed owners/credentials, URL scope and query violations,
  mismatched callbacks, final-result handling and runtime DELETE dispatch.
- This is a polling foundation milestone. Arc's five resource mappings remain
  read-only, with cleanup drivers, plan impact/ordering, ownership/locks,
  restored cleanup workers and final own-resource absence still unfinished.
  Counts remain 434 rules, 400 actions and 1,434 operations; all eight overall
  acceptance criteria and independent-emulator/live-cloud evidence remain open.
- Verification used an isolated checkout of `d6584fc` plus exactly these five
  changed files because concurrent, unrelated identity changes were being
  edited in the shared workspace. Repository-wide `go test ./...` passed there
  (Azure 362.347s; GCP 218.185s), as did Arc/Defender/catalog race checks
  (18.600s), `go vet ./...`, documentation checks (30 committed chapters and
  10 original screenshots) and whitespace validation. The five files were
  byte-compared with the shared workspace before adding this verification note.

### Arc HybridCompute native child cleanup and restored workers

- Three native actions now delete machine extensions, Run Commands and license
  profiles. They retain the pinned 2025-01-13 operations and the signed native
  polling protocol. Machine registrations and shared ESU licenses remain
  read-only pending their distinct lifecycle work. Counts are 434 rules, 403
  cleanup actions and the same 1,434 operations; all prior operation definitions
  are unchanged. Generation is byte-identical after the source checksum refresh.
- Child cleanup binds private authored/unknown configuration and machine
  registration identity to the reviewed inventory, including its signed graph
  references. Native metadata types, protected tags, managed resources/groups,
  management locks and current incoming dependencies are verified before
  mutation. Two observations repeat these checks. ETags are bound before DELETE;
  provisioning/output and the machine's child projections may change during
  deletion without pretending that a new registration is the original one.
- Every completion requires the child's own GET. A parent 404, DELETE 404,
  synchronous response or operation success alone cannot close a surviving
  child. Missing parents and already-deleting children cause verification only.
  Accepted receipts bind the reviewed action and persist signed polling progress;
  restoring them neither repeats DELETE nor resets the verification deadline.
  Native DELETE lacks If-Match, so repeated reads do not eliminate the final
  check/mutation race or identify an identical recreation without native
  creation metadata. Agent-side removal is separate from cloud child absence.
- The cleanup plan displays English and Chinese warnings for extension removal,
  running-script termination and changed machine license configuration. Shared
  licenses remain untouched, and profile absence does not establish billing
  termination. Permission documentation includes native child DELETE, parent
  and resource-group reads, locks and the shared incoming-dependency checks.
- Composed native tests cover the three child actions, running commands,
  private/registration changes, malformed metadata, tags/ownership/locks,
  permission failures, synchronous/asynchronous responses, missing parents,
  retained children, tampered requests/receipts and native incoming alerts.
  A real SQLite scan/graph/plan/execution test reviews three child steps, restores
  the database, job and runtime between phases, closes exactly those children
  after own absence and leaves the machine and shared license active. Job logs
  exclude signed queries, script content and private configuration.
- This completes the child-driver milestone, not overall Arc or provider
  acceptance. Machine cleanup and ordering, local-agent verification, shared
  license lifecycle policy, independent emulation/live-cloud evidence and the
  nineteen mapped Azure types without rules remain open. All eight overall
  acceptance criteria remain pending.
- Verification: an isolated checkout of `5349859` plus this milestone passed
  `go test ./...` (Azure 322.306s), Arc/Defender/catalog/planning race checks
  (Azure 33.202s; cleanup 1.556s), `go vet ./...`, frontend type checking,
  39 frontend contract tests, six offline importer tests and documentation
  checks (30 committed chapters, 10 original screenshots). Both generated
  artifacts reproduce byte-for-byte. The shared workspace also passed Arc,
  catalog and cleanup-warning integration tests and frontend type checking.
  Its pre-existing translation edits were retained outside this commit.


### Arc HybridCompute machine registration cleanup and ordered prerequisites

- Ordinary machine registrations (empty kind, AWS or GCP) now have a native
  cleanup action. HCI, VMware, SCVMM, AVS, EPS, unknown kinds and registrations
  linked to parent clusters remain protected for their controller lifecycle.
  Azure now has 434 rules, 404 cleanup actions and the same 1,434 operations.
  Both generated catalogs reproduce byte-for-byte; existing operations are unchanged.
- Machine inventory reads all three native child indexes and each child itself,
  binding membership, private configuration and stable registration identity.
  Saved children recover index omissions. Graph ownership uses explicit direct
  child deletion steps; unscanned children remain unresolved, and retaining or
  protecting a child blocks registration deletion. Selecting only a machine
  produces four steps when its extension, command and license profile exist.
- Machine DELETE rechecks registration/configuration, child absence, group and
  lock protection and native incoming dependencies. Reviewed child deletion may
  update the machine ETag and embedded projections. Each completion reads the
  root and every recorded/reviewed child independently, even after parent 404.
  Signed receipts bind the reviewed prerequisites across worker/runtime restarts.
  Operation completion, DELETE 404 and parent 404 alone never prove subtree absence.
- English/Chinese plan warnings and permission documentation distinguish cloud
  registration removal from external host and local-agent removal. Native DELETE
  still lacks If-Match, so repeated reads cannot eliminate the last check/mutation
  race or identify an identical recreation lacking native creation metadata.
  Shared licenses are retained. Controller variants, local-agent verification,
  hybrid identity metadata/network-profile lifecycle, shared-license policy,
  independent emulation/live-cloud evidence and nineteen missing mapped Azure
  types remain open. All eight overall acceptance criteria remain pending.
- Composed tests cover eligibility, retained/protected children, known omissions,
  missing child assets, changing configuration/identity, permission failures,
  late children, locks, forged requests/receipts, synchronous DELETE and surviving
  children after parent 404. SQLite execution tests cover both child-only selection
  and machine selection, restore database/jobs/runtime between phases, preserve
  original operation/deadline, and close only individually verified resources.
- Verification: isolated `07a7cec` plus this milestone passed `go test ./...`
  (Azure 309.427s; GCP 163.099s), Arc/Defender/catalog/planning race checks
  (Azure 54.427s; cleanup 8.077s), `go vet ./...`, frontend type checking,
  39 frontend contract tests, six offline importer tests and documentation checks
  (30 committed chapters, 10 original screenshots). Shared-workspace Arc,
  catalog and cleanup-warning tests passed (Azure 7.430s; cleanup 3.328s), as did
  frontend type checking. Pre-existing translation changes were byte-checked
  and preserved outside the milestone commit.


### Arc shared ESU license cleanup and independent consumer ordering

- Shared licenses now bind native `Licenses_Delete` from the pinned 2025-01-13
  contract. Its original example returns empty 200/204 despite retaining the
  LRO annotation. A separate exact extraction of Microsoft's CLI interaction 5
  records DELETE 200 with no callbacks at API 2026-07-15. Tests preserve that
  original transport version; no same-version live-cloud claim follows.
- License inventory signs tenant/immutable identity, private authored configuration
  and known local profile assignments. Native machine/profile indexes and each
  profile GET recover omitted known associations. Public fields include assigned
  license count and processors; private licensing metadata remains hidden.
- Native graph references require explicit profile selection without claiming
  license ownership over machines/profiles. Profile/machine-only cleanup retains
  shared licenses. Deletion requires no live local assignments and a present,
  nonnegative native assignment count of zero. This guards assignments beyond
  the current subscription; documented cross-subscription consumers must be
  cleared through their own connection workflow. Group, lock, protection,
  credential identity and incoming dependencies remain checked before DELETE.
- Signed synchronous receipts bind reviewed prerequisites across restarts.
  Completion reads the license itself and saved/reviewed assignment references;
  an operation response or license 404 cannot conceal a remaining association.
  Repeated reads cannot eliminate concurrent reassignment because native DELETE
  lacks conditional mutation. Billing termination is not inferred from absence.
- Bilingual plan warnings and permission guidance describe entitlement removal
  and up to five further calendar days of billing. Composed tests cover explicit
  selection, foreign counts, missing/malformed metadata, opaque immutable IDs,
  configuration changes, protection/locks, permissions, late/omitted profiles,
  forged history/receipts and surviving associations. SQLite scan/graph/plan and
  restored execution now also cover profile plus license deletion while retaining
  the machine and its other children.
- Counts are 434 rules, 405 actions and 1,435 operations, with 182 root and 101
  reference documents. Existing operations remain unchanged. Controller variants,
  agent-side verification, cross-subscription orchestration, identity/network
  profile lifecycle, independent emulation/live-cloud/billing evidence and
  nineteen missing mapped Azure types remain unfinished. All eight acceptance
  criteria remain open.
- Verification: isolated `d60fa6a` plus this milestone passed `go test ./...`
  (Azure 313.148s; GCP 165.175s), Arc/Defender/catalog/planning race checks
  (Azure 55.913s; cleanup 1.318s), `go vet ./...`, frontend type checking,
  39 frontend contract tests, six offline importer tests and documentation checks
  (30 committed chapters, 10 original screenshots). Both generated artifacts
  reproduce byte-for-byte. Shared-workspace integration passed (Azure 5.627s;
  cleanup 0.269s), along with frontend type checking. Existing translation edits
  were verified separately and preserved outside this commit.


### Azure Local controller contracts and corrected ENS scope

- ENS releases edge VMs; ordinary Arc registration removal is not equivalent.
  The parity matrix now includes the Azure Local VM instance, guest agent and
  identity metadata, plus NIC, disk, logical network, storage path and image
  resource families. These nine additions expose 28 missing mapped Azure types;
  they do not count as implemented rules or cleanup actions.
- Seven pinned native StackHCIVM 2024-01-01 documents add 33 operations with
  unchanged official examples: 24 GETs, eight DELETEs and VM Stop. Identity
  metadata has no separate DELETE. Counts are 434 rules, 405 actions and 1,468
  operations, with 189 root and 102 reference documents. All 1,435 prior
  operations remain unchanged.
- Offline checks cover 42 declared responses and schema-validate 25 bodies.
  Original examples retain nine malformed extension parents, the Stop suffix
  mistake, two NIC list parameter mistakes and 18 unsafe callback placeholders.
  Tests distinguish these source defects from canonical runtime requests.
  Extension binding requires a direct HybridCompute machine parent in the
  connection subscription; public invocation results and logs exclude private
  OS, SSH, proxy and unknown nested configuration.
- A checksum-verified function extracted from Microsoft's published stack-hci-vm
  1.15.1 wheel independently establishes VM-instance DELETE plus poll completion
  before Arc machine DELETE. Stubbed execution verifies ordering and failure
  boundaries without importing the CLI package. The source manifest, original
  fragment, license and reproduction instructions are retained in
  `providers/azure/fixtures/azure-local/`. The management guide confirms NICs
  and data disks remain independent resources.
- Native Azure Local inventory, reference reconciliation, controller ownership,
  cleanup execution/recovery and final readback remain unfinished. No emulator,
  live controller, physical VM removal or billing outcome is claimed. Bilingual
  capability notes describe this boundary. All eight acceptance criteria remain
  open.
- Verification: isolated `b58e73f` plus this milestone passed `go test ./...`
  (Azure 311.124s; GCP 163.579s), Azure Local/Arc/catalog race checks
  (Azure 52.366s), `go vet ./...`, seven offline native-CLI/importer tests
  and documentation checks (30 chapters, 10 original screenshots). Both
  generated artifacts reproduce byte-for-byte; the downloaded CLI archive,
  source member and exact function extraction match their retained hashes.
  Shared-workspace Azure Local/Arc/catalog integration also passed (4.999s).
  The 49 milestone files do not overlap existing work, whose file hashes were
  verified unchanged before commit.


### Azure Local native inventory, singleton recovery and reference graphs

- Nine explicit native resource specifications now scan VM instances, guest
  agents, identity metadata, NICs, disks, logical networks, storage paths and
  gallery/marketplace images. Counts are 443 rules, 405 actions and 1,468
  operations, with the same 189 root/102 reference documents. Nineteen mapped
  Azure types still lack specifications; cleanup for these new types remains
  unfinished and no generic DELETE is exposed as a reviewed lifecycle.
- Six independent families use subscription indexes and individual reads. VM
  and guest discovery starts from native HybridCompute machines; singleton GETs
  recover unknown omissions even when a child list is empty or missing. Known
  identities recover omitted roots/parents. A surviving known child whose parent
  disappeared fails reconciliation; only its own absence closes it. Regions
  inherit from verified Arc machines, with conflicting locations rejected.
- Arc's existing unfiltered ARM paging loop is reused. Two observations bind
  configuration, registrations, membership and scoped continuation tokens.
  Partial responses, permission errors, changed pages/versions/parents and
  malformed identities/references fail without publishing a partial snapshot.
- Public fields include VM power/capacity, network addresses, disk/image state
  and storage capacity. Credentials, keys, proxy settings, local paths, error
  text and unknown nested metadata remain private. Signed reference evidence
  distinguishes VM/guest parents, NICs, disks, logical networks, storage paths,
  images and custom locations. Foreign or absent targets stay unresolved; these
  references grant no controller or deletion ownership.
- Native contract and composed protocol tests now also cover SQLite scans,
  exact graph edges, known omissions, failed-read preservation, individual
  absence, public/private projection and logical-network closure through NIC,
  VM and guests. No emulator or live controller backend was run. Network-picker
  integration, controller cleanup/recovery, independent final readback and real
  VM/billing outcomes remain pending. All eight acceptance criteria remain open.
- Verification: isolated `170dcd6` plus this milestone passed `go test ./...`
  (Azure 309.349s; GCP 159.705s), Azure Local/Arc/catalog race checks
  (56.597s), `go vet ./...`, seven offline CLI/importer tests and documentation
  checks (30 chapters, 10 original screenshots). Additional cursor/identity
  boundary checks passed (0.605s). Both generators reproduce byte-for-byte,
  and all 1,468 existing operation definitions remain unchanged.
  Shared-workspace Azure Local/Arc/catalog integration passed (8.701s). The
  29 milestone files do not overlap existing work; pre-existing file hashes
  were verified unchanged before commit.


### Azure Local network selection and VM-attached disk scope

- Live network search now pages from Azure VNets into Local logical networks,
  supports name/ARM-ID queries and binds continuations to connection, region,
  parent, query and source. Exact Local IDs use native individual reads and
  recover index omissions without querying VNet first. Empty filtered pages
  advance; exhausted sources terminate. Permission failures remain failures.
  Local embedded subnet configuration is not fabricated as an ARM subnet.
- Network scans include Local virtual disks through native VM attachments.
  Signed saved VM identities recover known parent-index omissions and are
  reread on subsequent scans; detach removes the VM network reference. These
  references do not add reverse graph dependencies or deletion ownership.
- The actual creator/SQLite worker/graph path is covered with the nine Local
  rules and broad ARM source enabled: live target validation, ten successful
  shards, six matching assets and rejection before persistence after the
  selected network disappears. This scoped fixture does not prove all service
  families' network behavior or pruning of old scope memberships.
- The existing styled frontend picker now uses Azure network/subnet labels.
  Permission/page errors expose retry while preserving chosen networks. Twenty
  scan UI tests, 39 frontend contracts and TypeScript checks passed. Actual
  dialog/browser QA with a temporary response fixture verified light/dark
  dropdowns, pagination, permission retry and preserved selection; no native
  HTML select was present. This is UI evidence, not cloud backend evidence.
- Counts remain 443 rules, 405 actions and 1,468 operations, with 189 root/102
  reference documents. Nineteen mapped types still lack specifications. Local
  controller ownership, cleanup/recovery, independent final readback and live
  VM/billing outcomes remain pending. All eight acceptance criteria stay open.
- Verification: isolated `c74a909` plus this milestone passed `go test ./...`
  (Azure 311.702s; GCP 161.771s), Azure Local/Arc/network/catalog race checks
  (58.050s), `go vet ./...`, seven offline CLI/importer tests and documentation
  checks (30 chapters, 10 original screenshots). Shared-workspace integration
  passed (8.792s), as did TypeScript and all 20 scan UI tests. Exactly 12 owned
  files are included; all 65 pre-existing file contents were preserved,
  including unrelated edits in the shared translation file.


### Azure Local native guest-agent cleanup and restart recovery

- Guest agents now have an explicit native DELETE action. Counts are 443 rules,
  406 actions and 1,468 operations, with 189 root/102 reference documents. All
  existing native operation definitions remain unchanged; source mappings,
  generated checksums and the explicit guest specification are synchronized.
- Inventory signs guest and VM configuration, HCI registration identity/location,
  ETag and protection evidence. Preflight rereads the same context and verifies
  resource-group protection and inherited locks before mutation. Authored and
  unknown private fields remain bound; changing operational status during
  deletion does not invalidate the original resource configuration.
- Native 202/204 deletion persists a resource/request-bound ARM receipt, honors
  async-header precedence and retry delays, restores signed URL rotations and
  avoids mutation replay after restart. Poll failure, malformed/foreign URLs,
  signature changes and operation 404 cannot establish resource absence.
  A missing parent prevents a new DELETE; the guest's own GET decides completion.
- Original 2024 SDK methods extracted from the pinned official CLI wheel are
  retained with hashes, extraction coordinates and the Microsoft MIT license.
  Offline execution checks native response handling and continuation without
  another initial mutation. Callback shapes use composed ARM protocol fixtures;
  no independently recorded Local backend or emulator was available in the
  inspected Python/.NET/Go/Java SDK and CLI trees. Real-cloud compatibility of
  the bounded callback policy and guest-side effects remain unverified.
- The registered SQLite workflow covers scan, graph, warning-bearing plan and
  restartable execution. Operation success leaves a surviving guest active;
  eventual own absence closes that guest only. VM/registration/identity are
  retained. The bilingual warning explains possible guest-management disruption.
- Nineteen mapped types still lack specifications. VM controller ownership,
  controller/independent-resource cleanup, final backend verification and real
  VM/billing outcomes remain unfinished. All eight acceptance criteria stay open.
- Verification: isolated `77b247c` plus this milestone passed `go test ./...`
  (Azure 311.021s; GCP 159.966s), Azure Local/Arc/catalog and cleanup race checks
  (63.153s / 1.394s), `go vet ./...`, nine offline SDK/CLI/importer checks and
  documentation validation (30 chapters, 10 original screenshots). Catalog
  reproduction passed after synchronizing the source checksum. Both retained
  SDK fragments match the pinned wheel, full member hashes and AST line ranges.
  Shared-workspace Azure/cleanup integration passed (9.586s / 3.359s), plus
  TypeScript, 39 frontend contracts and 92 cleanup/i18n UI tests. The 23-file
  milestone preserves all 65 pre-existing file contents, including unrelated
  shared-translation edits.


### Azure Local VM deletion transport preparation

- Retained and executed the pinned official VM SDK's request builder and deletion
  methods with offline transport/poller stubs. Native VM deletion uses the direct
  HybridCompute machine parent, API 2024-01-01 and only 202/204 initial responses.
  Its explicit `final-state-via: azure-async-operation` setting is tested separately
  from the guest SDK's default polling options. Continuation skips the initial
  mutation. Original archive/member/AST-fragment hashes and license are retained.
- Shared Local receipt validation now accepts canonical VM and guest owners and
  runs the same callback, failure, rotation and restart checks for both. Signed
  receipts cannot cross kind, machine or subscription; unreviewed roots, read-only
  identity metadata and malformed/noncanonical IDs fail even without a callback.
- This prepares the native VM transport; VM cleanup remains unavailable until
  native child review, dependency ordering, action recovery, identity readback and
  subsequent Arc registration deletion are connected. No real ARMPolling engine,
  independent emulator or live Local backend was run. Composed callback shapes
  still require live compatibility checks. Counts remain 443 rules, 406 actions
  and 1,468 operations; all eight acceptance criteria remain open.
- Verification: isolated `6249ecc` plus this milestone passed the full Azure and
  cleanup package suites (308.649s / 2.817s), Azure Local/Arc/catalog race checks
  (62.212s), Azure/cleanup `go vet`, twelve offline SDK/CLI/importer checks and
  documentation validation (30 chapters, 10 original screenshots). All three
  retained VM fragments match the verified wheel member and AST ranges.
  Shared-workspace Azure/cleanup integration passed (6.333s / 1.320s). The nine
  milestone files preserve all 65 pre-existing file contents.


### Azure Local VM lifecycle, system-disk impact and independent readback

- VM instances now have a native DELETE action. Counts are 443 rules, 407 actions
  and 1,468 operations; source selection and generated metadata preserve the
  existing native operation definitions. Nineteen mapped types still lack specs.
- VM inventory records native guest/identity singletons, Arc extensions/commands/
  license profiles, the registered OS disk and known VM scopes. Signed history
  recovers omitted Arc children. Graph contributions order the four direct
  prerequisites before VM deletion and review identity/OS-disk impacts separately.
  Arc child ownership stays with its registration; the Local action does not
  delete that registration. Retention and protection block incompatible cleanup.
- Research corrected the initial composed fixture's disk-retention assumption:
  the later Microsoft support response reports engineering confirmation that the
  OS disk is deleted with the VM, whereas data disks remain. The source URL,
  response timestamp and correction are retained in `lifecycle-sources.json`.
  This is support/documentation evidence, not a Swagger cascade guarantee or a
  live backend test. The native GET contract supplies the registered OS-disk ID.
- Native machine/VM reads verify that another VM does not use the OS disk, with
  known VM identities recovering parent-index omissions. The driver checks each
  managed resource's configuration/ETag, protection and inherited locks, including
  the OS disk's own resource group. Data disks, NICs, images and storage paths
  remain independent; the action does not issue separate OS-disk DELETE calls.
- The same native receipt supports VM recovery. Operational instance/installation
  observations can change without discarding authored configuration or SMBIOS
  identity; unchanged legacy guest snapshots remain readable. VM, identity and
  registered OS-disk own absence are all required before completing the controller
  action. Missing parents, DELETE 404, synchronous responses and successful polls
  cannot substitute for those reads. If the native response has no registered
  OS-disk ID, no separate disk asset can be verified; the warning still describes
  OS-disk removal with the VM.
- Registered SQLite tests cover both inventory sources, graph, five direct steps,
  two managed impacts and repeated repository/runtime restart. VM absence followed
  by identity absence leaves the task pending while the OS disk survives. Final
  disk absence closes only the reviewed resources and leaves the data disk and
  Arc registration active. Additional checks cover stale/forged manifests, denied
  reads, new/shared consumers, cross-group protection and retention choices.
- Bilingual capability/permission documentation and cleanup warnings describe the
  VM/system-disk effect and retained data disks/NICs/registration. The final native
  Arc-registration controller step, standalone Local root cleanup, real callback
  compatibility, physical removal and billing outcomes remain unfinished. All
  eight acceptance criteria remain open.
- Validation: full isolated `go test ./...` passed (Azure 315.436s, GCP
  163.693s); focused race checks passed (Azure 96.760s), and `go vet ./...`
  passed. The 12 pinned CLI/catalog Python checks and documentation validation
  (30 chapters, 10 original screenshots) passed. After applying the milestone to
  the shared workspace, Azure/cleanup/plan integration passed (12.448s / 3.456s /
  3.889s), TypeScript checking passed, and all 39 frontend contract tests plus
  92 cleanup/localization UI tests passed. The 24 milestone files preserve all
  65 pre-existing file contents, including unrelated edits in the translation file.


### Azure Local registration controller completion

- The native Arc registration action now accepts a verified Local VM context.
  Inventory binds the native VM configuration, two Local guest singletons and
  registered OS disk to the exact HCI registration. Signed context survives the
  VM's own 404 only while that registration is unchanged. Bare HCI hosts and
  unsupported controller variants remain protected. Existing ordinary Arc records
  and durable receipts keep their previous representation.
- Selecting the registration produces the original CLI order: guest/Arc
  prerequisites, VM deletion and identity/OS-disk own absence, then registration
  DELETE and own readback. The Local VM is a direct lifecycle child of the
  registration; Arc extensions/commands/license profiles retain their existing
  ownership and required-before-VM order. VM-only selection retains registration.
- The Arc driver rereads the VM, guest, identity metadata and registered OS disk
  before mutation and after parent disappearance. Surviving or changed resources,
  denied reads, retention/protection, registration replacement and inherited
  locks prevent an unsafe transition. Synchronous responses and DELETE 404 cannot
  stand in for own absence. The existing receipt avoids replay across restarts.
- SQLite tests now exercise both the five-step VM-only and six-step complete
  registration plans, each with two managed impacts and restarted runtimes/
  repositories. Standalone history tests cover a later scan after VM deletion.
  Bilingual warnings and permission/capability documentation describe the full
  sequence and separate retained NIC/data-disk cleanup.
- Counts remain 443 rules, 407 actions and 1,468 operations. Native operation
  definitions and pinned original SDK/CLI extractions are unchanged. Independent
  Local root cleanup, real controller callback compatibility, physical removal
  and billing outcomes remain unfinished. All eight acceptance criteria remain
  open; composed tests do not establish live-cloud parity.
- Validation: full isolated `go test ./...` passed (Azure 318.592s, GCP
  164.019s); focused race tests passed (Azure 112.674s), and `go vet ./...`
  passed. The 12 retained CLI/catalog Python checks and documentation validation
  (30 chapters, 10 original screenshots) passed. Shared-workspace integration
  passed for Azure/cleanup/plan (12.045s / 1.475s / 1.794s), along with TypeScript
  checking, 39 frontend contract tests and 92 cleanup/localization UI tests.
  The 15 milestone files preserve all 65 pre-existing file contents, including
  unrelated translation edits. No live controller/backend was used.


### Azure Local independent disks and network interfaces

- Native disk/NIC DELETE actions bring the catalog to 443 rules and 409 actions;
  all 1,468 operation definitions are preserved. Original request builders and
  delete/poller methods from the pinned Microsoft CLI 1.15.1 wheel are retained
  with hashes. Offline SDK/CLI/catalog checks cover both new root shapes.
- Root inventory now reads native VM scopes and binds durable consumer history.
  Known parent-index omissions and vanished VM IDs remain recoverable. A new
  configuration prefix prevents dropping the signed history to bypass checks.
  Legacy disk metadata remains valid for existing VM impacts; independent
  deletion requires a fresh root scan.
- Native VM references must be cleared before disk/NIC deletion. A dependent VM
  must be explicitly selected or detached externally; the graph never silently
  selects a VM from a root-resource selection. OS disks remain delegated VM
  impacts. Independent roots check configuration/ETag, inherited locks, resource
  group protection and fresh/known consumers before mutation and own readback.
- ARM 204 operation headers are diagnostic and discarded without network access,
  matching the native terminal response and original examples. Validated 202
  callbacks retain the existing bounded receipt. Synchronous results, DELETE 404
  and restart cannot replace resource absence or replay deletion.
- New tests exposed a shared network-filter problem: private recovery metadata
  could expand a network scan through unrelated known IDs. The common scalar
  reference fallback now excludes internal underscore fields; explicit verified
  network references still participate. The retained regression checks both.
- Registered SQLite tests exercise VM-first disk/NIC cleanup, six direct steps,
  two managed impacts and repeated runtime/repository recovery. Focused cases
  cover live references, explicit selection, retention, protected resources,
  omitted indexes, malformed history, permission errors, legacy rescan and native
  202/204/404 responses. Bilingual docs and disk data-loss warnings are updated.
- Local networks, storage paths and images still need independent lifecycles.
  Real backend callback compatibility, physical removal, billing and the wider
  provider-parity acceptance remain unverified. All eight acceptance items stay
  open; these tests are protocol evidence rather than live-cloud validation.
- Validation: full isolated `go test ./...` passed (Azure 327.620s, GCP
  169.291s); focused race checks passed (Azure 177.879s), and `go vet ./...`
  passed. All 18 retained SDK/CLI/catalog Python checks and documentation checks
  (30 chapters, 10 original screenshots) passed. Shared-workspace integration
  passed for Azure/cleanup/plan/inventory (21.224s / 3.623s / 3.680s / 3.378s),
  together with TypeScript checking, 39 frontend contract tests and 92
  cleanup/localization UI tests. The 36 milestone files preserve all 65
  pre-existing file contents, including unrelated translation edits. No live
  controller/backend was used.


### Azure Local gallery and marketplace image cleanup

- Both image families now use their native 2024-01-01 DELETE operations, bringing
  the catalog to 443 rules and 411 actions; all 1,468 operation definitions remain
  unchanged. The original SDK builders, response handlers and resumable methods
  from the pinned Microsoft CLI wheel are retained with hashes and line ranges.
- The official Azure Local FAQ establishes a critical lifecycle distinction:
  deleting a source image does not affect VMs created from its copy. Image-only
  inventory/actions therefore require no VM/Arc reads, VM deletions or managed
  impacts. Existing references order VM removal first when both are selected.
- Images reuse the signed root receipt, private configuration/ETag comparison,
  resource-group protection, inherited locks, bounded callbacks and own readback.
  Original records can be rescanned; known index omissions recover through own
  GET. Removing signed context or injecting VM history/prerequisites is rejected.
- Retained tests exercise both image kinds with surviving referencing VMs,
  native 202/204/404, denied reads, changed/protected resources, receipt transfer,
  SDK continuation and SQLite restarts. Deployed VM configuration remains intact
  in image-only execution; complete VM+image plans keep their reviewed ordering.
- Bilingual capability/permission docs and source provenance are updated. Local
  network/storage cleanup, live controller behavior, physical removal and the
  broader provider-parity audit remain unfinished; all eight acceptance items
  remain open. Original SDK functions and composed transports do not establish
  independent emulator or real-cloud parity.
- Validation: full isolated `go test ./...` passed (Azure 328.152s, GCP
  164.905s), focused race tests passed (Azure 227.793s), and `go vet ./...`
  passed. The focused Azure/Arc/catalog suite passed (21.413s); shared-workspace
  integration passed for Azure/cleanup/plan/inventory (24.231s / 0.769s / 1.183s /
  2.370s). All 24 original SDK/CLI/catalog Python checks and documentation checks
  (30 chapters, 10 original screenshots) passed. Generation preserved every
  native operation, and the six SDK excerpts match the verified original wheel.
  The 28 milestone files preserve all 65 pre-existing file contents. No frontend
  code changed; no emulator or live backend was used.


### Azure Local storage paths and managed deletion prerequisites

- Storage paths now use their native 2024-01-01 DELETE contract, bringing the
  catalog to 443 rules and 412 actions with all 1,468 native operations unchanged.
  The pinned Microsoft CLI storage SDK builder, initial handler and resumable
  delete methods are retained with archive/member/fragment hashes.
- Native disk/image lists and own reads, plus Arc machine/VM reads, identify
  placement consumers. Signed context preserves known root IDs and VM scopes
  across omitted indexes, own 404 and later scans. Optional/missing placement is
  treated as a possible consumer, not evidence that a path is unused. Workloads
  require explicit selection or native removal; no ownership cascade is inferred.
- Storage tests exposed a shared prerequisite gap: OS disks are deleted and
  verified by their VM, so requiring an independent disk action is incorrect.
  Provider-declared exclusive managed deletion can now satisfy a prerequisite
  through an already planned controller that verifies own absence. The worker
  restores the frozen disk impact after closure/restart. Unselected controllers
  are not promoted, and missing authority, retention, protection, foreign/faulty
  snapshots and duplicate impacts remain blocked.
- Standalone storage cleanup verifies configuration/ETags, inherited locks,
  resource-group protection and live/known references. A path's own 404 cannot
  close surviving consumers; synchronous responses and persisted polls still
  require independent readback. Tests cover each native placement kind, unknown
  placement, new roots, omitted/invalid indexes, denied reads and legacy rescan.
- The registered SQLite chain reviews nine direct steps and two VM-managed
  impacts, restarts between polls, never issues OS-disk DELETE and removes the
  path only after VM/disks/images. Bilingual permission/capability docs and
  source provenance are updated. Logical-network cleanup, physical behavior,
  production callbacks and the broader parity audit remain incomplete; all
  eight acceptance items remain open. No live backend/emulator was used.
- Validation: full isolated `go test ./...` passed (Azure 329.150s, GCP
  161.127s). Focused race checks passed (Azure 259.174s, cleanup 3.273s, plan
  1.929s, inventory 2.338s), and `go vet ./...` passed. The focused Local/Arc/
  prerequisite suite passed (Azure 28.028s); additional index/legacy checks
  passed (0.712s). Shared-workspace integration passed for Azure/cleanup/plan/
  inventory (29.359s / 4.378s / 3.840s / 3.767s). All 27 original SDK/CLI/catalog
  Python checks, 39 frontend contracts, 92 cleanup/localization UI tests,
  TypeScript checks and documentation checks (30 chapters, 10 original
  screenshots) passed. The 28 milestone files preserve all 65 pre-existing file
  contents, including unrelated edits to the shared worker. No live backend or
  independent emulator was used.

### Azure Local logical-network type prerequisite

- Logical-network GET/list/native-delete catalog contracts now use the pinned
  Microsoft `2025-06-01-preview` source. Its read-only `networkType` distinguishes
  `Workload` and `Infrastructure`; the original 2024 contract has no such field.
  This is an inventory prerequisite, not completion of network cleanup. The
  catalog remains at 443 rules, 412 actions and 1,468 native operations; the four
  network operations changed (including the native resource-group list rename),
  with other operations and all resource-type mappings unchanged.
- Discovery uses the catalog version consistently for indexes and own reads.
  Only exact native enum values establish the type; missing, future, differently
  cased or whitespace-padded values remain `Unknown`. Non-string values fail
  discovery. Names, tags, switch names and empty NIC lists cannot establish
  workload status. Unknown types remain visible without deletion capability;
  denied/unsupported preview responses cannot close existing assets.
- Four new original examples retain their native bytes and source hashes. The
  four stable examples and provenance are preserved separately. Three unchanged
  SDK model/enum declarations were checked against the verified 1.15.1 wheel;
  AST tests establish the read-only field and enum, not live SDK execution.
  The preview GET example also omits the type, so no workload default is inferred.
- Tests cover strict types, permission/API-version failures, conflicting native
  indexes, identity and cursor boundaries, changed-type pagination, own absence,
  JSON/omitted-index recovery and private-field projection. Existing network
  selection, SQLite inventory, Local cleanup and Arc recovery tests still pass.
  Bilingual docs describe the preview API requirement and infrastructure-network
  prerequisites. Workload/AKS consumers, infrastructure-wide deletion ordering,
  network cleanup recovery and real-cloud validation remain unfinished. All eight
  acceptance items remain open; no independent emulator or live backend was used.
- Validation: full isolated Azure package tests passed (323.087s). Focused race checks passed (Azure
  262.893s, shared inventory 2.394s), and `go vet` passed for both packages.
  Focused Local/Arc/catalog checks passed (27.751s); shared-workspace integration
  passed (Azure 26.816s, inventory 0.791s). All 29 Python SDK/CLI/catalog checks
  passed. Documentation checks passed in the isolated checkout (30 chapters) and
  shared workspace (42 chapters), each with 10 original screenshots. Generated
  catalog reproducibility and unchanged unrelated operations were verified.
  All 65 pre-existing file contents are preserved. No frontend code changed.

### Azure Local logical-network cleanup and native AKS consumers

- Workload and infrastructure logical networks now support independent native
  deletion through the preview contract. The catalog contains 443 rules, 413
  actions and 1,472 operations. Four native connected-cluster/provisioned-instance
  GET/list operations establish AKS consumers; no AKS deletion family is added.
  The previous 1,468 operations remain unchanged.
- Workload networks check NIC and AKS logical-network references. Infrastructure
  networks require VMs, NICs, other networks and AKS instances at their verified
  custom location to be cleared first. Missing references/placement remain
  possible consumers; unknown network type or custom location cannot authorize
  deletion. Graph prerequisites require explicit workload selection and never
  grant cascade ownership. Native AKS dependencies require external removal.
- Signed history preserves observed NIC/network/AKS identities and VM scopes
  across omitted indexes and missing Arc registrations. Native reverse NIC IDs
  are checked independently; stale references continue to block deletion. Own
  404 still requires consumer checks. Malformed/foreign indexes, denied reads,
  changed configuration/ETags, protection and inherited locks fail safely.
- Four unchanged native AKS/Arc examples and three exact preview SDK functions
  retain source hashes and provenance. Unlike the stable root SDK, network
  `begin_delete` declares `final-state-via: location`; persisted Location polling
  and fresh own-resource readback survive runtime/repository restart without
  another DELETE. Offline original-function checks use stubs and do not execute
  Azure Core ARMPolling against a service.
- Registered SQLite tests cover seven workload-network steps or eight
  infrastructure-network steps, with two existing VM-managed impacts. Tests also
  cover native index/response boundaries, surviving consumers after network 404,
  reference/scope changes, private-field isolation, explicit selection and legacy
  rescan. The cleanup warning and bilingual docs explain that infrastructure
  deletion removes the cloud projection while its on-premises network remains.
- This milestone completes the implemented Local network cleanup path, not the
  broader provider-parity audit. All eight acceptance items remain open. Native
  AKS deletion, physical behavior and real callback compatibility remain outside
  this milestone. No independent emulator or live backend was used.
- Validation: full isolated `go test ./...` passed (Azure
  340.781s, GCP 161.111s), and `go vet ./...` passed. Shared-workspace Local/Arc/
  catalog/prerequisite integration passed (Azure 53.785s, cleanup 0.793s, plan
  1.121s); the full shared inventory package passed (1.328s). All 32 original
  SDK/CLI/catalog Python checks, 39 frontend contracts, 92 cleanup/localization
  UI tests, TypeScript checks and documentation checks (30 isolated / 42 shared
  chapters, each with 10 original screenshots) passed. Catalog reproducibility,
  all unchanged operations, original wheel/fragment/example hashes and the
  preservation of 65 existing file contents were verified.
- The attempted full Azure race run reached its 10-minute default timeout while
  executing an existing API Management notification test; it did not report a
  data race. Shared cleanup/inventory race packages passed (13.706s / 7.313s);
  the plan package was rerun at its correct `internal/core/plan` path and passed
  (4.337s). The affected Local/Arc/catalog/prerequisite race run passed (Azure
  461.076s, cleanup 5.449s, plan 5.307s); the full Azure race run is not a pass.
  The committed milestone contains 41 files and preserves all 65 pre-existing
  file contents, including unrelated changes in the shared translation file.

### Elastic SAN native contracts and retained-resource boundaries

- The missing dedicated block-storage equivalent now has a pinned native
  contract foundation: 17 Elastic SAN GET/list/DELETE operations for SANs, volume
  groups, volumes, snapshots, private endpoint connections and private-link
  capabilities. The catalog contains 1,489 operations; all prior 1,472 operations
  and 443 resource mappings remain unchanged. Rules/actions remain 443/413.
  Inventory, lifecycle ownership and registered cleanup are not yet implemented.
- The selected `2026-04-01-preview` schema exposes volume-group retention policy
  and separate active/soft-deleted indexes, which the stable 2025-09-01 contract
  omits. True selects only retained records, not both populations. The original
  maximum GET example includes an undeclared retained-resource header; it is
  rejected, not silently added to the contract. Both DELETE examples explicitly
  request permanent deletion, so neither establishes a default normal-delete
  request. Original source bytes are preserved while request copies isolate
  these differences. Discovery must not interpret these gaps as absence.
- All 34 native examples preserve 54 response variants and 24 schema-validated
  bodies. Ten Location callbacks use an external placeholder and are rejected.
  Exact lifecycle switches are enforced before transport; typed booleans,
  case-changed values, whitespace and undeclared options cannot broaden deletion.
  Public invocation/logging retains operational capacity and retention state
  while excluding key, identity, target/client and unknown private configuration.
- Four exact classes from a checksum-verified Microsoft preview CLI wheel are
  executed with transport/serialization stubs. They independently establish
  list selection, GET's missing retained selector, explicit deletion options
  and 200/202/204 Location poller setup. Their API version is 2024-07-01-preview;
  this is not a same-version service test or execution of the full AAZ poller.
- Remaining work includes authoritative active/retained inventory, known-ID and
  parent-omission recovery, graph/network references, managed volume ownership,
  client-session and snapshot prerequisites, private endpoint effects, retained
  deletion semantics and restored SQLite cleanup with own absence verification.
  All eight acceptance items and the broader parity audit remain open. No live
  Elastic SAN backend or independent ARM emulator was used.
- Verification: full isolated Azure package tests passed (340.010s); focused
  native-contract/catalog/invocation race checks passed (13.174s), and
  `go vet ./providers/azure` passed. Shared-workspace integration passed
  (1.329s). All 36 Python native CLI/SDK/catalog checks passed. Documentation
  checks passed for 30 isolated and 42 shared chapters, each with 10 original
  screenshots. Catalog regeneration reproduces exactly; all original source
  fragments, operations, mappings and CLI extraction hashes were verified.
  The 53-file milestone preserves all 65 pre-existing file contents. No frontend
  code changed, and full lifecycle acceptance is still open.

### Elastic SAN native reads and retained pagination

- Five resource kinds now have native GET and index helpers with selected-
  subscription, exact-depth ARM identity checks and typed operational metadata.
  Group and volume indexes bind explicit active/retained headers and preserve
  them across every page. Changed collection, subscription, API version,
  filters, malformed cursors, canonical cursor cycles, duplicate IDs, malformed
  records and incomplete responses return an error without partial rows.
- GET does not send an undeclared retained selector. Its 404 remains an error;
  parent-index 404 is also not translated into an empty collection. Registration,
  repeated complete inventory snapshots, known-ID reconciliation and retained
  list authority still need to compose these helpers before scanning is enabled.
- Ten unchanged response bodies from the official CLI soft-delete recording
  preserve provenance and SHA-256 hashes. They show separate group/volume
  populations, restore and permanent-removal transitions, and a retained volume
  gaining a timestamp suffix in its native ID while keeping its `volumeId`.
  The recording uses 2024-07-01-preview and has no retained-resource GET. Replay
  against the current bindings is offline response-shape evidence, not live
  2026 service behavior, an independent emulator, or proof of retained GET.
- Public payloads now include typed SKU name/tier and availability zones while
  keeping unknown SKU and target configuration private. No new inventory kinds
  or cleanup actions are registered; all eight acceptance criteria remain open.
- Verification: full isolated Azure tests passed (339.990s), Elastic SAN race
  tests passed (5.878s), and `go vet ./providers/azure` passed. Shared-workspace
  Elastic SAN integration passed (3.845s). Documentation checks passed for 30
  isolated and 42 shared chapters, each with 10 original screenshots. All ten
  response extractions were compared byte-for-byte with the checksum-verified
  original recording. This 18-file milestone preserves all 65 pre-existing file
  contents; catalog mappings, operations, rules and actions remain unchanged.

### Elastic SAN registered active/retained inventory

- Registered five native resource kinds with the `elastic-san` source: SANs,
  volume groups, volumes, snapshots and private endpoint connections. The Azure
  catalog still contains 1,489 operations; mappings and rules increase from
  443 to 448, while registered actions remain 413. The SAN uses the dedicated
  storage-cluster class from the AliCloud parity baseline. Cleanup stays pending.
- Two complete subscription-native snapshots cover the selected kind and its
  parent context. Active/retained lists remain separate on every page. GET
  validates active entries and recovers known IDs omitted by indexes; selected
  retained-list entries survive GET 404. Duplicate populations, inconsistent
  GET/list state, changed snapshots and incomplete collections fail atomically.
  Own 404 and both completed population indexes are required for known absence.
- Child region and network context derive from verified SAN/group records.
  Signed prior observations can recover known children after parent disappearance
  when child collections remain readable. Unverified orphans and parent-index
  404 fail closed. Regional filtering does not close a live ID from another
  region; the worker intentionally supplies known IDs across regional shards.
- A signed inventory record binds identity, connection, configuration, region,
  native ancestry, retention and graph/network evidence. Continuations also bind
  the request, complete snapshot and bundle revision. Forged, stripped, unrelated
  or changed evidence is rejected. Restored active IDs and timestamp-suffixed
  retained IDs remain distinct even when the service preserves `volumeId`.
- Ordinary graph references cover parents, subnets, source volumes/resources,
  private endpoints, assigned identities and managed controllers. They do not
  authorize ownership or cascade deletion. Key Vault hosts do not become guessed
  ARM IDs. Public capacity, SKU, zones, retention and private-link connection
  status are retained; key, target and connection-description configuration
  remains private. Resource-specific fields are declared in YAML.
- SQLite worker tests exercise all five regional sources, eight fixture assets,
  ordinary graph edges, database reopen with a fresh runtime, index omission
  recovery, retained absence and denied-read preservation. Other scenarios cover
  network closure, historical parents, native restoration IDs, proof tampering,
  scope/source changes, incomplete indexes and a change between complete passes.
  The original REST and older CLI evidence remains unchanged; no live cloud or
  independent ARM emulator was used. Cleanup and all eight acceptance criteria
  remain open.
- Verification: full isolated Azure tests passed (340.246s); Elastic SAN and
  catalog race checks passed (18.069s); `go vet ./providers/azure` passed.
  Shared-workspace Elastic SAN/catalog integration passed (1.944s). All six
  catalog synchronization tests passed. Documentation checks passed for 30
  isolated and 42 shared chapters, each with 10 original screenshots. Catalog
  regeneration is reproducible; all 1,489 operations, original native documents
  and 443 prior mappings remain unchanged. The five new mappings compile to
  448 specs/rules and 413 actions. This 23-file milestone preserves all 65
  pre-existing file contents.

### Elastic SAN snapshot deletion and persisted Location operations

- Registered direct snapshot deletion with native VolumeSnapshots_Delete. Azure
  still has 1,489 operations and 448 kinds/rules; actions increase from 413 to
  414. Volume, group, SAN and private-endpoint cleanup remain pending. Snapshot
  cleanup removes only the selected restore point; it does not delete its source
  volume, siblings or parents and submits no volume force/permanent options.
- A signed cleanup record binds creation/configuration, ETag observations and
  the signed inventory identity. Missing creation timestamps, managed resources
  and unsupported states remain protected. Preflight rereads snapshot identity,
  protection, parent regions/states, resource-group protection and inherited
  locks. Existing deletion and missing parents are observed without mutation.
- Location receipts bind the selected resource and region. Reviewed regional
  asyncoperations URLs require the selected subscription/provider/version,
  operation UUID and monitor=true. Signature rotation may not change the native
  operation. Malformed or ambiguous headers, unsafe redirects, bad response
  identity/state and forged saved phases are rejected. Terminal 204 diagnostic
  callbacks are neither saved nor followed; opaque operation IDs avoid exposing
  signing values through execution provenance.
- Poll success is followed by independent snapshot readback. An expired callback
  cannot erase a live snapshot or use parent absence as evidence. Independent
  own GET absence can finish that expired operation. Same-name recreation and
  changed immutable configuration fail closed. SQLite execution tests reopen
  the database with a fresh runtime, preserve operation identity and deletion
  deadline, send one DELETE, and keep source volumes/parents open.
- Six unchanged response bodies from the pinned official snapshot CLI recording
  add stable 2025-09-01 evidence alongside the existing preview soft-delete
  recording. Native deletion preserves creation identity while provisioning and
  modification state change; Location returns empty 202 then empty 200, followed
  by an empty snapshot list. Source/body/URL hashes and safe callback shapes are
  retained; original signing values are not copied. Current-version replay is
  composed offline evidence, not a 2026 cloud execution or independent emulator.
- The remaining four Elastic SAN cleanup kinds, retained-volume outcome rules,
  connection/session effects and broader provider parity remain open. All eight
  acceptance criteria are still unproven.

- Validation: isolated Azure full suite passed (341.211s); Elastic SAN/catalog
  race checks passed (24.716s); Azure go vet passed; all six catalog sync tests
  passed. Main-workspace Elastic SAN/catalog tests passed (5.610s). Documentation
  checks passed for 30 isolated and 42 main chapters plus 10 original screenshots;
  diff whitespace checks passed. Pinned source and six body/callback hashes were
  verified, all 1,489 existing operations stayed unchanged, and all 65 preexisting
  worktree file hashes remained unchanged. No live-cloud execution was performed.

### Elastic SAN private endpoint connection cleanup

- Registered native PrivateEndpointConnections_Delete: Azure remains at 1,489
  operations and 448 kinds/rules, with 415 delete actions. Volume, volume-group
  and SAN lifecycle cleanup remain pending; this does not close family parity.
- Reused the snapshot child driver and signed Location poller, preserving prior
  snapshot serialized keys, versions and configuration/ETag semantics. Connection
  cleanup binds creation, target, mapped native group IDs and authored config;
  preflight separately verifies approval state, rereads SAN/mapped group regions,
  protection and management locks, and waits without mutation on disappearing
  ancestry. Only the selected connection DELETE is submitted.
- Reviewed the pinned original GET maximum example: groupIds contain volume-group
  ARM IDs and systemData supplies creation time. Tests compose valid requests by
  replacing only documented placeholders in memory. Native mappings now add
  ordinary group dependency edges and group subnet closure; opaque mappings are
  retained as inventory but cannot authorize cleanup. No ownership is inferred.
- Operation success or Disconnected status cannot close a live connection. Own
  readback, creation/configuration verification, expired-callback behavior and
  persisted signed phases apply after restart. SQLite tests perform the actual
  scan/graph/plan/confirmed execution workflow with fresh runtimes and reopened
  databases, one DELETE, stable operation/deletion deadlines and independent
  storage assets remaining open. Bilingual docs describe access disruption and
  exact permission scope, including no consumer Network endpoint deletion.
- Evidence is native REST fixtures plus composed protocol and SQLite tests. No
  independent Elastic SAN ARM emulator or live connection deletion was verified.
  Client/session management, retained volume deletion, parent cascades and broader
  provider parity remain unfinished; all eight acceptance criteria stay open.

- Validation: isolated Azure full tests passed (342.192s); Elastic SAN/catalog
  race checks passed (31.698s); Azure go vet passed; all six catalog sync tests
  passed. Main-workspace Elastic SAN/catalog tests passed (6.272s). Docs checks
  passed for 30 isolated and 42 main chapters plus 10 original screenshots;
  staged diff checks passed. All 1,489 operations and original fixture bytes
  remain unchanged; 448 specs and 415 actions were independently counted.
  The 65 preexisting worktree file hashes were unchanged.

### Elastic SAN volume cleanup, snapshot ordering and retention outcomes

- Registered native Volumes_Delete without changing the 1,489 operations or 448
  mappings/specs; delete actions increase to 416. SAN and volume-group cleanup
  remain pending, including their cascades and retained-group outcomes.
- Inventory signs volume GUID/creation/configuration, retention observation and
  snapshot membership. Snapshot collection reads are shared per group; known
  snapshot own reads recover list omissions, and forged prior cleanup metadata
  is rejected before these reads. Native source IDs establish authoritative
  direct snapshot lifecycle bindings. Plans review/order separate snapshot
  removal before volume DELETE and reject retained snapshot expectations.
- Ordinary DELETE preserves native retention settings; an omitted policy stays
  unspecified, never guessed disabled. A separately selected retained volume
  binds deleteType=permanent. No implicit purge follows soft deletion. Snapshot
  force removal is disabled; active iSCSI force is false unless a reviewed boolean
  force_delete option explicitly enables it. Bilingual plan warnings explain
  retention, permanent data removal and forced-session interruption.
- Both active/retained populations, selected own GET, retained own GET where
  addressable and immutable GUID/creation/configuration determine readback. A
  retained-index 404 fallback remains specific to retained resources. Signed
  persisted outcomes distinguish soft_deleted with the retained native ID from
  absent. Callback success/expiry cannot erase live or recreated resources,
  unknown populations, restored identities or remaining known snapshots.
- Original pinned CLI soft-delete response bodies prove ID/name transition with
  unchanged GUID/creation projection. SQLite integration verifies real plan and
  confirmed two-step snapshot/volume execution, reopened runtimes/databases,
  one mutation per step, retained outcome persistence and separate retained-copy
  rediscovery. Targeted protocols cover explicit permanent purge/force, changed
  retention/configuration, management protection, permission failures, omitted
  and new snapshots, ambiguous identities and forged state.
- Tests remain offline protocol/application evidence, not live preview cloud or
  independent Elastic SAN ARM emulator evidence. No host-side iSCSI command runs.
  Group/SAN lifecycle completion and the broader eight acceptance criteria remain
  open; provider parity is not established by this milestone.

- Validation: full Azure (338.609s), cleanup service (3.535s) and plan (1.202s)
  tests passed. A final older-volume-record graph guard then added recovery of
  independently known signed snapshot assets; final Elastic SAN/catalog tests
  passed in isolation (7.285s) and the main workspace (4.154s), with final race
  checks (41.969s) and vet passing. All six catalog-sync tests passed. Frontend
  locale tests passed all 16 cases in both workspaces; main type checking and
  all 39 frontend contract tests passed, and the isolated production build
  succeeded. Docs passed 30 isolated/42 main chapters and 10 original screenshots.
  All 1,489 operations and original fixtures remain unchanged. The 64 unrelated
  WIP hashes stayed unchanged; the existing localization edits were preserved
  exactly around the six new translated entries and excluded from the commit.

### Elastic SAN volume-group membership verification

- Group inventory now captures signed active/retained volume, snapshot and
  incoming private endpoint connection context, including retention policy and
  child incarnation/configuration hashes. Known signed child IDs are recovered
  through own reads when indexes omit them. Context changes invalidate cursors;
  forged prior context is rejected before native reads.
- Verified groups expose member counts. Unresolved connection mappings remain
  explicit possible dependencies, without consumer endpoint ownership. Retained
  groups retain historical membership without claiming current counts; creating
  volumes with an absent optional GUID leave membership unverified and visible.
- When a retained group's snapshot collection is unavailable (404), discoverable
  volumes remain visible but cleanup-protected. Permission failures still fail
  the scan. This does not reinterpret a missing collection as verified absence.
- Tests reopen real SQLite observations with a fresh runtime and cover omission
  recovery, cursor drift, forged history, unresolved mappings and retained
  history. Original CLI interactions 23/28 preserve the same group ARM ID and
  creation/configuration identity across soft deletion. That empty-group recording
  does not establish populated cascades or retained-group purge behavior.
- This milestone enables no new parent DELETE: 1,489 operations, 448 kinds/specs
  and 416 deletion actions remain unchanged. Group/SAN cleanup and all eight
  parity acceptance criteria remain open. Evidence is offline; no live cloud or
  independent Elastic SAN ARM emulator has been verified.

- Validation: full isolated Azure tests passed (343.764s), followed by a final
  optional-GUID visibility fix verified by Elastic SAN/catalog tests (4.557s),
  focused group race tests (6.182s) and Azure go vet. Earlier Elastic SAN/catalog
  race tests also passed (44.285s). Main-workspace Elastic SAN/catalog tests
  passed (4.517s); docs checks passed for 30 isolated and 42 main chapters and
  10 original screenshots. Staged diff checks passed. All 65 preexisting
  worktree hashes and all original fixture bytes remained unchanged.

### Elastic SAN active volume-group cleanup

- Registered native VolumeGroups_Delete and signed cleanup state against the
  existing membership context. Catalog operations remain 1,489 and kinds/specs
  remain 448; deletion actions increase to 417. Active volume cleanup orders its
  own snapshots, while orphan group snapshots remain direct group prerequisites.
  Incoming private endpoint connections require separate selection rather than
  automatic expansion. Existing retained volumes are explicit retained impacts.
- Native preflight verifies both group populations, own creation/configuration,
  complete child indexes, signed known own reads, retention, protection and locks.
  New or live active members block deletion; retained members must match reviewed
  GUID/configuration. No group force/permanent flag or host-side command is added.
  Groups with retained members require Enabled retention; retained-group purge
  and SAN cleanup remain unfinished, without asserting that the cloud lacks them.
- Signed Location receipts and outcomes distinguish same-ID group soft deletion
  from absence, including an addressable retained GET. A missing snapshot index
  after terminal group verification requires every known snapshot's own absence;
  missing volume populations remain incomplete. Existing retained members must
  remain present. Callback success/expiry cannot hide live or recreated groups.
- A child-only rescan can leave frozen parent membership stale. Graph contribution
  records a group-specific unresolved refresh requirement without failing unrelated
  reconciliation; stale context still cannot authorize group deletion.
- SQLite tests review four ordered delete steps plus a retained impact, require
  independent PEC selection, persist translated retention warnings, restart fresh
  runtimes/databases, verify one mutation per step and rediscover the retained group
  under its original asset ID. Protocol tests cover denied/incomplete reads, changed
  membership, locks/protection, changed policy and native same-ID outcomes. Original
  pinned CLI group identities remain evidence; no populated-group cloud execution
  or independent Elastic SAN ARM emulator was verified. All eight criteria stay open.

- Remaining shared-plan gap discovered during regression: governance returns
  unresolved diagnostics, but ReplaceGraph persists only relationships/bindings
  and cleanup planning does not consume unresolved references. A stale group
  can therefore still have a Ready draft. Native signed request/preflight checks
  reject its changed boundary before group DELETE; a fresh full family scan
  restores reviewable membership. Persisting executable unresolved constraints
  and exposing them in plan blockers remains part of the broader parity work.

- Validation: final full Azure tests passed (348.462s), cleanup service tests
  passed (5.214s), and plan tests passed (3.259s). Elastic SAN/catalog race
  tests passed (62.364s); the final stale-membership regression also passed
  under race (7.747s). Azure/cleanup/plan vet passed in both workspaces.
  Main Elastic SAN/catalog tests passed (6.468s). Six catalog-sync tests,
  17 locale UI cases in each workspace, 39 frontend contracts, TypeScript
  checking and the isolated production build passed. Documentation checks
  passed 30 isolated/42 main chapters and 10 original screenshots. The final
  catalog exactly preserves all 1,489 operation definitions and original
  fixture bytes. Earlier generated-checksum failures were corrected by
  regenerating from the minimally edited source before the final full run.
  All 64 unrelated WIP hashes remained unchanged; original localization edits
  were preserved exactly around two new translated entries and not staged.


## Persisted unresolved cleanup dependencies

- Closed the shared-plan gap recorded in the active Elastic SAN group milestone:
  governance now atomically persists unresolved references with graph revisions,
  and cleanup planning loads them with the reviewed lifecycle component. Native
  Azure and GCP lifecycle contributors explicitly mark incomplete destructive
  boundaries. Ordinary display references remain informational. No cloud API
  permissions or generated operation/spec/action counts changed.
- Selected and delegated deletion boundaries with these diagnostics produce
  `unresolved_cleanup_dependency` blockers before execution creation. Retained
  impacts and unrelated resources do not acquire this blocker. Diagnostic content
  participates in the deterministic plan hash; even unchanged graph revision and
  inventory cannot keep an old Ready task valid when a dependency finding changes.
- SQLite and PostgreSQL share migration 00008, storing diagnostics in the graph
  revision row. Legacy rows start with no diagnostics; rescan relevant resources
  after upgrade to populate them. Successful graph replacement clears resolved
  findings; failed serialization rolls back the graph transaction. Reads exclude
  closed controllers, superseded scopes and foreign-connection diagnostics.
- The Elastic SAN stale-retained-member regression now verifies a blocked draft
  and refused execution creation, then a Ready plan after a full family refresh.
  Native signed preflight checks remain an independent guard. Planner tests cover
  direct/delegated/retained/unrelated boundaries, provider and connection isolation,
  and diagnostic ordering. Shared repository contracts cover persistence, clearing,
  failed replacement and closed controllers on SQLite and real PostgreSQL. A
  relational test verifies superseded-scope exclusion. User cleanup guidance and
  both locale messages explain scanning the resource and its dependencies again.
- Full repository Go tests passed, including Azure (342.919s) and GCP (159.604s).
  Shared governance/plan/cleanup/SQLite/relational race tests and repository-wide
  vet passed. After adding the final regressions, cleanup, SQLite, PostgreSQL and
  relational tests passed again. PostgreSQL used an isolated local
  `postgres:17-alpine` container; this is database integration evidence, not a cloud
  emulator or live-cloud result. All eight overall acceptance criteria remain open.
- Final shared cleanup/relational race checks passed (14.932s/4.829s). Main
  governance, plan, cleanup, SQLite and relational tests passed; main Elastic SAN
  tests passed (5.872s). Both workspaces passed all 18 locale UI cases; 39 frontend
  contracts, TypeScript checks and the isolated production build passed (existing
  large-chunk advisory only). Documentation checks passed 30 isolated/42 main
  chapters and all 10 original screenshots. The owned PostgreSQL container was
  removed and Docker Desktop restored to its initially stopped state. All 64
  unrelated WIP hashes and the exact original localization edits were preserved.


## Elastic SAN parent inventory boundary

- SAN inventory now enumerates groups, volumes, snapshots and provider-side
  private endpoint connections, including both native active/retained group and
  volume populations. It saves a signed boundary per SAN: canonical member IDs,
  native kinds, retention classification and private full-configuration hashes.
  The signature binds the connection, SAN identity, verified region and inventory
  proof. Child contents and collection availability participate in the two-read
  consistency check and pagination fingerprint.
- Signed prior SAN membership recovers omitted groups before enumerating their
  children and recovers independently addressable descendants. A retained member
  in Deleting/Restoring keeps its signed prior population when its own read lacks
  an unambiguous population marker. Legacy SAN observations without a boundary
  can be refreshed from the native collections. Different SANs retain separate
  member sets; private configuration is never copied into public inventory fields.
- A retained group's snapshot-index 404 records incomplete membership and still
  reads known snapshots individually. It does not assert that unknown snapshots
  are absent. Active snapshot-index 404, denied reads and missing volume indexes
  fail the scan. Completeness here describes the observed inventory boundary;
  it neither proves deletion eligibility nor retained-resource purge semantics.
- This closes the missing SAN-wide member observation prerequisite. SAN cleanup
  still requires lifecycle impact planning, native preflight, asynchronous deletion
  and independent child/parent outcome verification. Retained-group purge and the
  previously documented full-family retained snapshot-index reconciliation edge
  remain unfinished. Existing operation/spec/action counts stay 1489/448/417.
  All eight overall acceptance criteria remain open.
- New tests exercise seven native children across four types and both populations,
  SQLite/runtime restart, legacy refresh, omitted groups/descendants, individual
  snapshot absence, retained transitions, native collection/own-read failures,
  signed foreign-member rejection, connection isolation, separate SANs and cursor
  invalidation from child changes, new connections and collection availability.
  Evidence is composed native-protocol fixtures and actual SQLite persistence;
  no live Azure or independent Elastic SAN ARM emulator was used.
- Validation: final full Azure suite passed (341.318s), Elastic SAN/catalog race
  tests passed (68.763s), final focused tests passed (10.034s), and main focused
  tests passed (7.236s). The final separate-SAN regression also passed before the
  full run. Azure vet passed in both checkouts. Documentation checks passed
  30 isolated/42 main chapters and all 10 original screenshots. Existing native
  operation definitions, source catalogs and original protocol fixture bytes were
  unchanged. All 65 preexisting WIP hashes were preserved.


## Elastic SAN ordered parent cleanup

- Registered native `ElasticSans_Delete` with `2026-04-01-preview` and the existing
  signed Location protocol. Catalog operations/specs stay 1489/448; delete actions
  become 418. Existing snapshot/volume/group/connection persisted keys and receipts
  remain compatible. SAN cleanup binds creation identity, writable configuration,
  signed member boundary and reviewed child fingerprints.
- Root lifecycle contributions require refreshed member assets, schedule groups
  before the SAN and keep groups' volume/snapshot ordering. Private connections
  remain independently selected required deletions. New/stale/unscanned members
  produce persisted plan blockers. Protected roots do not prevent independently
  reviewed child cleanup. Root requests verify the exact frozen group/connection
  prerequisites and reject additional options or lifecycle impacts.
- SAN DELETE runs only after active/retained group and volume collections, snapshot
  and connection collections, and every known own-resource read establish that
  children are absent. A missing collection can use known own reads only after
  its parent's own absence; denied reads remain errors. Final root readback repeats
  child verification, including after an expired operation callback. Successful
  callbacks and missing parents cannot independently close assets.
- Native read-only aggregate fields (the six fields marked readOnly in the pinned
  ElasticSanProperties schema) may change as children are removed. Stable root
  comparison preserves creation identity, tags, writable capacity, SKU, network
  access and autoscale configuration; ETag changes alone do not reject completed
  child steps. Resource-group protection and subscription management locks remain
  checked before mutation. No client-side iSCSI command, force flag or implicit
  retained-resource purge was added.
- Existing retained children and Enabled group-retention policies protect root
  cleanup before execution. If an unspecified native policy unexpectedly retains
  a group/volume, the root's independent member reads refuse SAN DELETE. This is
  supported ordered cleanup of non-retained boundaries, not completion of SAN
  retained-boundary cleanup or retained-group purge. Those behaviors and the
  retained snapshot-index reconciliation edge remain unfinished. All eight
  overall acceptance criteria remain open.
- Tests cover five-step SQLite planning and fresh database/runtime recovery with
  one native DELETE per snapshot, volume, connection, group and SAN; explicit PEC
  selection; stale-boundary plan blocking and refresh; exact prerequisite proofs;
  native read-only changes; recreated/configured/protected/locked resources;
  new/retained children; denied collection/own reads; disappearing collections,
  surviving known children and expired callbacks. These are composed native
  protocol fixtures plus real SQLite persistence, not live-cloud or independent
  Elastic SAN ARM emulator evidence.
- A shared planner guard now blocks direct child deletions when their selected
  controller has no actionable capability. Independent child selections keep
  their direct-cleanup fallback; retention-only read-only scopes keep their
  existing skip behavior. This prevents an unsupported/protected SAN from
  releasing child mutations while its own step is silently omitted. The native
  retention regression verifies a blocked draft and refused execution creation.
- Validation: final full repository tests passed, including Azure (373.104s) and
  GCP (184.761s). Elastic SAN/catalog race tests passed (86.995s); final controller,
  cleanup and SAN-root race tests passed (1.501s/5.501s/27.028s). Main focused
  plan/cleanup/Azure tests passed (3.334s/2.592s/10.706s). Full repository vet,
  all 10 Python catalog/CLI evidence tests, and documentation checks passed
  (30 isolated/42 main chapters plus all 10 original screenshots). The SQLite
  execution test also rescans all five resource kinds after cleanup and finds
  no remaining assets. All 1489 original operation definitions and original
  protocol fixtures remain unchanged. All 65 preexisting WIP hashes were preserved.


## Elastic SAN retained snapshot-index reconciliation

- Fixed full-family native inventory and graph reconciliation when a retained
  group's snapshot index returns 404. The shared inventory collector records
  unavailable membership consistently for SAN, volume and snapshot shards; every
  known snapshot still receives its own read. Active-group index 404 and denied
  parent/index/own reads remain failures. Unknown snapshots are not declared absent.
- Snapshot collection availability now participates in two-pass consistency and
  pagination fingerprints even when the collection contains no known resources.
  Incomplete volume cleanup records carry a signed `snapshots_complete: false`
  marker and remain protected. Existing complete cleanup records and execution
  receipts retain their schema. Lifecycle reconstruction persists a volume
  constraint instead of failing the whole scope or creating destructive bindings
  from incomplete membership. A fresh complete scan removes the constraint.
- Actual SQLite inventory/graph/plan tests reopen the database with a fresh
  runtime, preserve a live known snapshot, preserve assets after permission
  failure, reconcile an independently confirmed absent snapshot, and restore
  volume cleanup eligibility after collection recovery. Independent known snapshot
  selection remains available. Additional tests cover unknown membership, native
  read failures, empty collection availability changes, cursors and signed-state
  tampering. These are composed protocol fixtures and real SQLite persistence; no
  live Azure or independent Elastic SAN emulator was used.
- This resolves the previously documented full-family retained snapshot-index404
  inventory/lifecycle edge. Retained-group purge, SAN retained-boundary cleanup
  and externally deleted SAN reconciliation remain unfinished. All eight overall
  acceptance criteria remain open. Catalogs, original API definitions and original
  fixtures are unchanged.
- Validation: final `go test ./... -count=1` passed, including Azure (352.438s)
  and GCP (165.123s). Elastic SAN/catalog race tests passed (104.228s), isolated
  focused tests passed (9.819s), and main focused tests passed (13.721s). Azure
  vet and documentation checks passed (30 isolated/42 main chapters and all
  10 original screenshots). All 65 preexisting WIP hashes were preserved.


## Elastic SAN external deletion reconciliation

- Known SAN identities now remain enumeration roots after their own disappearance.
  Every relevant active/retained child population is still attempted. A collection
  404 can fall back to known own reads only after its parent is independently
  absent and has no live retained-index observation. Denied reads remain errors.
  Collection availability is included in two-pass and cursor bindings.
- Signed group history now preserves retained child classification during known
  recovery, matching SAN history. An own404 for a previously retained resource
  does not become absence when its retained index is unavailable; the scan stays
  incomplete. Independently addressable retained survivors remain visible.
- Native root own absence/configuration changes and missing group/volume member
  reads produce persisted refresh constraints during lifecycle reconciliation.
  They do not fail unrelated scope graph work or release controller child steps.
  Full scans close only resources with independent disappearance evidence;
  child-only scans do not close unscanned parents. Cleanup execution/readback
  protocols and complete inventory/cleanup record schemas are unchanged.
- SQLite full-family tests reopen the database and runtime after external
  deletion, then verify complete disappearance and each of four surviving child
  kinds, preserved region, blocked orphan controller plans, and final own absence.
  Additional tests cover child-only graph refresh, changed root writable state,
  permission errors, unavailable retained-index-only identities, surviving
  retained own reads, live-parent collection404, and collection availability
  changing between the two reads. Evidence is composed native-protocol fixtures
  and actual SQLite persistence, not live Azure or an independent ARM emulator.
- This resolves external deletion reconciliation for independently verifiable
  known resources. Unavailable retained-index-only populations still require
  native evidence. Retained-group purge and SAN retained-boundary cleanup remain
  unfinished; all eight overall parity acceptance criteria remain open. Catalogs,
  pinned API definitions and original fixtures remain unchanged.
- Validation: final `go test ./... -count=1` passed, including Azure (359.057s)
  and GCP (166.495s). Elastic SAN/catalog race tests passed (131.100s), isolated
  focused tests passed (12.866s), and main focused tests passed (13.484s). Azure
  vet and documentation checks passed (30 isolated/42 main chapters and all
  10 original screenshots). All 65 preexisting WIP hashes were preserved.


## GCP Hyperdisk Storage Pool inventory and references

- Added `compute.googleapis.com/StoragePool` as the native pooled-capacity
  candidate for the EBS dedicated-storage-cluster row, replacing generic Disk.
  The Alibaba baseline inventories cluster capacity/zone/properties without a
  delete action. Physical-resource exclusivity remains a platform difference to
  assess; pooled capacity alone does not establish identical isolation.
- Imported five original Compute v1 storagePools methods and 26 transitive
  schemas from revision `20260908`, full-response SHA-256
  `30f29098aad84c7c4fb51eb657deebaec233799f13877e1cd0bb25738a02b4fd`.
  The new fragment preserves every prior source document and all 768 existing
  generated operations. The catalog now has 194 rules and 773 methods.
- Native aggregated inventory covers project and regional scans with zonal
  identity checks, empty-page continuation, partial-result failure and bound
  cursors. Pool state is separate from structured usage; both native usage field
  names are supported. Capacity, IOPS, throughput, provisioning types, disk
  counts, Exapool capacity and sharing settings remain available, with int64
  values retained as strings. Disk pool references are ordinary dependencies.
- Real SQLite worker tests reopen the database/runtime, preserve observations
  after denied/partial scans and reconcile a missing pool only after a complete
  successful native list. Native wire tests cover identity/scope conflicts,
  precision, paging, references and method bindings. These are composed protocol
  fixtures, not live-cloud recordings. The pinned Google mockgcp Compute service
  does not register StoragePools; no emulator was started for this milestone.
- This milestone implements inventory and references. Complete listDisks member
  discovery and reviewed pool cleanup remain unfinished; imported DELETE metadata
  does not register a cleanup action. Google requires disks removed before Storage
  Pool deletion and retains snapshots; Exapool deletion requires its account team.
  Cross-project sharing and physical-isolation equivalence remain to verify.
  All eight overall acceptance criteria remain open.
- Validation: final `go test ./... -count=1` passed, including Azure (352.652s)
  and GCP (164.193s). GCP Storage Pool/Compute/product/catalog race tests passed
  (20.971s), isolated focused tests passed (6.492s), and main focused tests passed
  (10.530s). GCP vet, seven Python catalog tests and documentation checks passed
  (30 isolated/42 main chapters and all 10 original screenshots). All 65
  preexisting WIP hashes were preserved; every prior source fragment, resource
  rule and generated operation remains unchanged.


## GCP Storage Pool native disk members

- Pool product inventory now enumerates every native `storagePools.listDisks`
  page, retaining disk capacities, usage, performance, attachments and snapshot
  policies. Empty-page continuation is supported; missing empty `items` is valid
  only with the native response discriminator. Explicit malformed arrays/tokens,
  partial results, token cycles and failed pages fail the scan.
- Member identities must be unique across pages and valid same-zone zonal disks.
  Google's native error catalog documents same-organization cross-project sharing
  despite the overview's same-project limitation. Shared foreign-project members
  remain summaries without foreign API reads or actionable assets; only local
  disk identities enter this connection's explicit network references. A final native pool read verifies selfLink, kind, zone, id and
  creation timestamp against the enumerated pool; mutable usage may change.
- Validated disk identities participate in network selection. Member summaries
  remain pool metadata and never become reverse pool-to-disk delete dependencies;
  the separate Disk shard remains authoritative for disk existence and its own
  ordinary pool dependency. No pool cleanup action is enabled by this milestone.
- Actual SQLite worker tests reopen database/runtime, preserve old members after
  denied/partial member scans, reconcile a missing pool, then replace the surviving
  pool's members with a complete empty native list. Protocol tests cover paging,
  large integer precision, malformed responses, scope/identity conflicts, network
  closure and parent replacement/absence. No independent emulator or live cloud
  was started. Existing catalog source documents and generated operations remain
  unchanged. List pagination is not a transactional membership snapshot; supported
  pool cleanup still requires lifecycle and action validation. All eight overall
  acceptance criteria remain open.
- Validation: final `go test ./... -count=1` passed, including Azure (352.239s)
  and GCP (167.678s). Final GCP Storage Pool/Compute/product/catalog/managed race
  tests passed (26.792s), isolated focused tests passed (9.871s), and main focused
  tests passed (9.577s). GCP vet and documentation checks passed (30 isolated/42
  main chapters and all 10 original screenshots). All final checks include the
  shared-project member implementation. All 65 preexisting WIP hashes and all
  source/generated catalog files were preserved.


## GCP reviewed Hyperdisk Storage Pool cleanup

- Enabled native `compute.storagePools.delete` for reviewed Hyperdisk Balanced
  and Throughput pools. All 773 operation objects and 194 resource rules remain
  unchanged; resource metadata enables the existing native operation. Exapools,
  unknown types and foreign-project members require external management.
- Native configuration and canonical disk creation manifests bind inventory,
  graph review, preflight and operation receipts. Complete repeated membership
  reads detect changes; missing or changed disk inventory blocks pool cleanup.
  Required-deletion edges require explicit disk selection and preserve independent
  VM ownership. No disk cascade, snapshot deletion or reservation cancellation is
  inferred. Active future reservations remain native API blockers.
- Before DELETE, two complete empty lists and every known disk's own 404 are
  required, together with unchanged READY pool configuration and protection labels.
  Native operation identity/target/status, scoped polling and receipt binding are
  checked; both pool and known disks must be independently absent for completion,
  including after parent 404, operation expiry and database/runtime restart.
- Real SQLite tests run registered scan, graph, plan and execution workers, reopen
  between execution rounds, verify disk-before-pool ordering without replay and
  reconcile final absence. Protocol tests cover omitted live members, permission
  errors, replacement/configuration drift, foreign/shared resources, protected
  types, altered requests/receipts and malformed/foreign native operations.
- Official type, delete and management contracts are linked in the pool fixture
  evidence. The pinned mockgcp still has no StoragePools service; no live cloud or
  independent emulator was used. Native DELETE has no atomic resource-ID/etag
  condition. Physical-isolation equivalence and all eight acceptance criteria
  remain open; the parity matrix remains pending verification.
- Validation passed: full isolated `go test ./... -count=1`, including Azure
  (350.347s) and GCP (167.354s); focused GCP/server race tests (31.051s / 2.134s);
  affected-package `go vet`; native pool/catalog/Compute/product/managed and server
  resolver regression tests in both isolated and main workspaces. Documentation
  checks passed for 30 isolated and 42 main chapters plus 10 original screenshots.
  All 773 generated native operation objects compare equal to the previous commit.
  The 23 owned files were isolated from all 65 preexisting working changes, whose
  bytes were verified unchanged. No frontend code or live-cloud environment changed.


## Storage Pool cleanup composed with native compute controllers

- Pool disk prerequisites now accept separately selected controllers with an
  authoritative exclusive native lifecycle chain and verified managed absence.
  This preserves VM/MIG/GKE ownership and does not select controllers, force
  deletion or bypass retained/protected disks. Pool readback still independently
  verifies every reviewed disk before confirming pool cleanup.
- VM readback verifies reviewed disk outcomes after its own 404, including missing
  impacts and retained disks. MIG readback verifies all reviewed members after
  manager and complementary-group absence. Controller completion cannot close a
  surviving known member. GKE's workload finalizers and complete readback remain.
- Shared planner/executor prerequisite validation follows the reviewed intermediate
  impacts to the selected deletion action. An effective-controller label alone is
  insufficient; changed, missing, unverified or duplicate intermediate ownership
  cannot supply a prerequisite. Frozen nested member snapshots survive restarts.
- Native protocol tests cover combined MIG, node-pool and cluster/pool deletion;
  the cluster case uses the existing real local HTTPS/TLS Kubernetes fixture with
  finalizers. SQLite runs real scan/graph/plan/execution/reconciliation for VM,
  managed disk and pool with delayed member absence and database/runtime reopening.
  These remain scripted protocol evidence, not live cloud or independent emulator
  evidence. API definitions, specifications and catalogs are unchanged. Broader
  parity and all eight acceptance criteria remain open.
- Final validation passed: full isolated `go test ./... -count=1`, including
  Azure (354.043s) and GCP (172.852s); targeted GCP/plan/cleanup/server race tests
  (52.414s / 7.297s / 11.597s / 9.182s); affected-package vet; main integration
  tests for native pool/controllers, nested prerequisite restoration, shared
  lifecycle contracts and server dispatch. Documentation checks passed for 30
  isolated and 42 main chapters plus 10 original screenshots. Twenty owned files
  were staged; 64 unrelated working-file hashes remain unchanged and the existing
  worker modification remains an identical separate unstaged diff. Its only owned
  change calls the shared prerequisite validator. Catalog/API/spec bytes are unchanged.

## Future Reservation native inventory and procurement state

- Corrected procurement state to `status.procurementStatus` in the resource rule
  and shared inventory/action readback parser. Planning status and amendment
  history remain separate; unknown native state strings remain observable.
- Materialized declared FutureReservation properties in normalized inventory,
  including requested/fulfilled counts, matching usage, lock/amendment details,
  generated-reservation URLs, commitment/time settings and storage capacities.
  Native int64 strings retain precision. Native payloads remain available.
- Creation history does not create deletion dependencies, child API access or
  inferred pool ownership. The existing native deletion and own-resource absence
  workflow remains; scheduled auto-deletion is not interpreted as a DELETE cascade.
- Tests reuse pinned official schemas without changing the native catalog and
  verify public field paths/types, every known procurement state plus a future
  state, native pagination/scope, exact quantities and own-resource lifecycle.
  SQLite scans survive database/runtime reopening and preserve existing assets
  on denied, partial or malformed lists before complete reconciliation.
- Evidence and commands are recorded in the future-reservation fixture README.
  Google's pinned mockgcp has a candidate native implementation, inspected but
  not executed in this milestone. No live cloud was used. The 194 rules and 773
  operations remain unchanged; all eight overall acceptance criteria remain open.
- Final validation passed: full isolated `go test ./... -count=1`, including
  Azure (348.929s) and GCP (169.603s); focused GCP/inventory race tests
  (26.351s / 1.682s); affected-package vet; main integrated native inventory,
  lifecycle, catalog, compiler and projection regressions. Documentation checks
  passed for 30 isolated and 42 main chapters plus 10 original screenshots.
  Exactly eight owned files were integrated, with all 65 original working-file
  hashes unchanged. No frontend changes or live-cloud tests were involved.

## Future Reservation independent native server verification

- Executed the retained opt-in test against Google's unmodified mockgcp at pinned
  commit `673a61419de1b8e4f7d26070ce20dde2daa61da8`, using the existing Compute
  harness. Repository-local dependencies were checked out and compiled without
  changing upstream service source or module definitions.
- Two native INSERTs and a fixture-side CANCEL exercise generated state, numeric
  identity, matching usage and duration-derived timestamps. Steward's 18 forwarded
  Compute calls include native aggregated inventory, regional selection, own
  readback, DELETE, zonal operation polling and final absence. There are no
  Compute response substitutions; only OAuth and CRM use connection fixtures.
- Serialized requests/receipts resume in fresh runtimes without replaying either
  delete. Deleting the first reservation preserves the other zone's reservation;
  final own GET and aggregate absence agree. Unique fixture names and exact
  resource cleanup allow isolated repeat runs against the same loopback server.
- Reproduction and source fingerprints are retained in the fixture README.
  Upstream does not model real procurement, fulfillment, IAM or list pagination.
  Independent-server evidence does not establish cloud equivalence or close the
  eight overall criteria. This milestone changes only tests and evidence docs.
- Validation passed with the independent test enabled: isolated GCP suite
  (168.400s), focused race suite (8.501s) and main integrated native/protocol suite
  (6.957s). The standalone independent run passed (0.762s). GCP vet and docs checks
  passed for 30 isolated and 42 main chapters plus 10 original screenshots.
  The exact temporary server exited normally; its upstream checkout and binary
  were removed. Three owned files were integrated, preserving all 65 preexisting
  working-file hashes. Production code, API metadata and frontend remain unchanged.

## GCP declared inventory property projection

- Every successful public inventory result now resolves declared aliases after
  native discovery, service enrichment, proof construction and sanitization.
  This covers product APIs and specialized sources without changing their
  internal parent discovery data. Existing native keys are preserved; alias
  values are resolved together so map iteration cannot create alias chains.
- Corrected ten specifications against their retained native GET schemas:
  SQL/Subnetwork/Pub/Sub state fields, IAM/Dataproc project IDs, native Dataproc
  policy/template names plus short resource IDs, firewall numeric/display names,
  and TPU queued state objects plus their scalar lifecycle state. The native
  catalog and operation definitions are unchanged.
- Firewall configuration comparison ignores derived project/resource/date aliases
  while preserving native ID, creation timestamp, fingerprint and rule checks.
  The existing native-change/tampered-plan tests remain the regression boundary.
  Organization policies acquire no project ownership from alias projection.
  Infra Manager physical-resource comparison removes only declared aliases that
  still equal their native source values; native fields and inconsistent aliases
  remain checked. Registered deployment/deployment-group lifecycle tests cover
  this distinction across review, execution, retention and restart.
- Native Dataproc, TPU and firewall fixtures exercise validated property queries
  alongside action identity/configuration checks. StoragePool's SQLite scan worker
  now verifies enriched member aliases, exact capacity strings and real SQL query
  results across failures, database/runtime reopening and absence reconciliation.
  Existing native keys and sanitized raw observations remain intact; unknown kinds
  receive no invented properties. Bilingual Google Cloud docs explain queries,
  exact integer strings and the next successful scan needed for older inventory.
- All eight overall criteria remain open. These changes improve property parity;
  they do not establish new native APIs, permissions or live-cloud equivalence.
- Final validation passed: full `go test ./... -count=1` (Azure 353.800s,
  GCP 172.515s); affected native lifecycle/property/query race suites (GCP 39.134s,
  inventory 1.560s, resourcequery 1.948s); vet; main integration (GCP 12.426s,
  inventory 0.576s, resourcequery 0.704s). The earlier full GCP race attempt on
  superseded source reached its 10-minute suite timeout; final race verification
  targeted the affected paths. Documentation checks passed for 30 isolated and
  42 main chapters plus 10 original screenshots.
- Both firewall and FutureReservation independent Google mockgcp tests passed
  again in isolated and main workspaces. Existing explicit firewall list/CRM
  substitutions remain documented; FutureReservation Compute responses are
  unmodified. The exact server exited normally, and its temporary source checkout
  and binary were removed. Nineteen owned files preserve all 65 original working
  file hashes. No catalog/API generation or frontend changes were required.

## GCP native nested state, labels and invocation timing

- Corrected Cloud Run v2 Service's declared state to `terminalCondition.state`.
  The shared native state reader now uses that nested condition when no existing
  status/state is present, keeping inventory and action readback consistent.
  An absent condition remains unknown, without inferring readiness from presence
  or the `reconciling` flag. See the [Service API](https://docs.cloud.google.com/run/docs/reference/rest/v2/projects.locations.services).
- Cloud SQL labels now resolve from `settings.userLabels`. Newly projected labels
  also populate string-valued inventory tags, preserving existing tag entries.
  Raw observations and native settings remain unchanged. See the
  [Cloud SQL settings API](https://docs.cloud.google.com/sql/docs/mysql/admin-api/rest/v1/instances#Settings).
- Removed WorkflowInvocation's unsupported `createTime` declaration; the native
  `invocationTiming` object remains available. Execution timing is not represented
  as resource creation time. See the [WorkflowInvocation API](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.repositories.workflowInvocations).
- Extended the pinned Discovery schema checks and native HTTP inventory/query
  regressions for all five Cloud Run condition states and missing conditions,
  Cloud SQL tags, Dataform timing/configuration proofs and preservation of native
  values. SQL physical-proof checks distinguish derived aliases from native
  settings changes. Bilingual query examples cover Run failures and SQL tags.
- This is a bounded correction of three confirmed native field mismatches; other
  static audit candidates still need adapter-by-adapter review. All eight overall
  acceptance criteria remain open. No new live-cloud or independent-server
  validation is claimed for this milestone.
- Validation passed: full `go test ./... -count=1` (GCP 173.508s,
  Azure 352.356s); affected GCP race suite (204.812s); provider/inventory/query
  vet; main integration (GCP 2.904s, inventory 4.489s, resourcequery 3.270s).
  The race filter selected GCP tests only; inventory/query packages had no
  matching race tests and were tested without a filter in main integration.
  Docs checks passed for 30 isolated and 42 main chapters plus 10 original
  screenshots. Eight owned files preserve all 65 original working-file hashes.

## GCP Compute field contracts and BigQuery detail inventory

- Audited all 52 Compute specifications against retained native detail schemas.
  Removed 47 unsupported state/label declarations, mapped Route state to native
  `routeStatus`, and exposed specific managed-certificate, PSC-connection and
  load-balancer migration status fields without inventing general readiness.
  The shared state reader uses supplied route status for inventory and readback;
  missing route status remains unknown. Source evidence: the retained Compute
  Discovery document and [Route](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routes),
  [SSL certificate](https://docs.cloud.google.com/compute/docs/reference/rest/v1/sslCertificates),
  [Network](https://docs.cloud.google.com/compute/docs/reference/rest/v1/networks)
  and [Firewall](https://docs.cloud.google.com/compute/docs/reference/rest/v1/firewalls) API fields.
- BigQuery dataset/table names resolve from native reference IDs. Inventory now
  reads each listed resource's native GET response, including parent datasets,
  before normalization. This supplies GET-only properties that list summaries
  omit, such as dataset encryption/default expiration and table row/byte counts.
  Native reference identities must match the project/dataset/table that was
  listed; missing references cannot fall back to synthetic names. Failed or
  mismatched details fail the batch instead of creating authoritative absence.
  See the [dataset](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/datasets/get)
  and [table](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/tables/get) detail APIs.
- The retained schema guard now checks every field in all Compute and BigQuery
  specs, plus TPU Reservation and Data Fusion namespace/DNS specs. It unwraps
  list-detail arrays and resolves native references. Explicit adapter-derived
  scope fields and StoragePool members/usage are handled separately; integer
  schemas map to numeric properties without converting native int64 strings.
  Previously corrected field contracts remain covered. Four old static audit
  candidates were valid native fields hidden inside list response wrappers.
- Native HTTP tests cover Compute status queries and unavailable fields, BigQuery
  parent/table pagination, same-named tables in different datasets, exact large
  integers, numeric project aliases, forbidden/missing/mismatched detail reads,
  and list/delete/wait/readback with original native identity. View and materialized
  view SQL is redacted before logs, inventory and public responses, while native
  metadata and same-named ordinary labels remain intact. Bilingual docs
  describe property availability and the additional BigQuery detail permissions.
- These tests are protocol and retained-schema evidence, not independent emulator
  or live-cloud evidence. The native catalog/API metadata is unchanged. All eight
  overall acceptance criteria remain open; the remaining native field candidates
  and broader lifecycle/end-to-end gaps still require verification.
- Validation passed: full `go test ./... -count=1` (Azure 357.013s,
  GCP 173.920s), followed after the SQL-redaction addition by the full GCP
  suite (176.887s), inventory/query/server/HTTP suites and shared contracts.
  Final affected GCP race tests passed (98.301s); vet and docs checks passed
  (30 isolated / 42 main chapters and 10 original screenshots). Main integration
  passed (GCP 11.476s, inventory 1.577s, resourcequery 0.391s, contracts 0.522s).
  Forty-three owned files preserve all 65 original working-file hashes. No
  temporary external server or cloud resources were created for this milestone.
