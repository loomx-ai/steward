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
