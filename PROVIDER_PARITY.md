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
