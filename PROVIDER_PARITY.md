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
