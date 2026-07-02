# Cloud Steward MVP Scan Loop Design

## Scope

This design started as the first Cloud Steward MVP delivery slice from `docs/design.md`: local server lifecycle, Web API, SQLite-backed local testing, optional MySQL persistence, Alibaba Cloud resource scan orchestration, and a React Scan Console for account configuration, scan status, resource list, and resource detail.

The implementation has since been extended with resource graph derivation, cleanup candidates, CleanupPlan generation, approval, guarded execution audit, and savings reports. Execution re-reads live resource state before each item and runs through a replaceable executor interface. The default executor is dry-run; the optional `alicloud-tag` executor only performs tag actions and skips unsupported or destructive actions.

## Architecture

Cloud Steward runs as a single Go binary named `steward`. The CLI owns only server lifecycle commands: `server start`, `server stop`, and `server status`. `server start` launches an HTTP server, initializes the configured local database, starts a lightweight scan worker, and serves both `/api/*` and embedded React static assets.

The backend is split into small packages:

- `internal/domain`: stable resource, scan job, account, and error types.
- `internal/store`: repository interfaces plus GORM implementations for SQLite local testing and MySQL deployment.
- `internal/scanner`: connector interface, fake connector for tests/demo, Alibaba Cloud connector adapter, and scan service.
- `internal/api`: HTTP handlers and response/error envelopes.
- `internal/server`: router, static asset serving, worker startup, and shutdown.
- `internal/cli`: cobra command wiring and server status file management.

The React app is a single operational console, not a marketing page. It calls scan, resource, graph, candidate, plan, audit, and report APIs, shows scan state, renders selected resource details, and drives the local guarded cleanup workflow.

## Data Flow

1. The user runs `steward server start`; local testing defaults to SQLite at `.cloud-steward/cloud-steward.db`. MySQL can be selected explicitly with `--db-driver mysql --db-dsn ...`.
2. The user opens the Web UI, enters an account label, region list, and either demo mode or Alibaba Cloud AccessKey credentials.
3. `POST /api/scans` creates or updates the account, creates one pending scan job, and returns the job.
4. The worker claims pending jobs, calls the selected connector for each requested region, normalizes resources, upserts resources, writes scan snapshot rows, and marks the job succeeded or failed.
5. `GET /api/scans` and `GET /api/resources` power the resource console. `GET /api/resources/{id}` returns a full resource record with raw provider fields.
6. `POST /api/scans/{id}/reconcile` derives resource graph edges and cleanup candidates.
7. The user reviews candidates, creates a plan, approves it, executes it through the configured executor, then reviews audit events and savings estimates. Each plan item rechecks the current resource before executor dispatch so late protection-tag changes block the item.

Credentials are never logged and are omitted from API responses. MVP stores AccessKey credentials in the configured local database for the self-hosted scan loop; this is documented as local-only behavior and can be replaced by encrypted secret storage later.

## API Contract

- `POST /api/scans`
  - Request: `accountName`, `provider`, `regions`, `mode`, optional `accessKeyId`, optional `accessKeySecret`.
  - Response: scan job JSON.
- `GET /api/scans`
  - Response: ordered scan job list.
- `GET /api/scans/{id}`
  - Response: one scan job or structured 404 error.
- `GET /api/resources`
  - Query filters: `scan_id`, `provider`, `account_id`, `region`, `type`, `q`.
  - Response: resource list.
- `GET /api/resources/{id}`
  - Response: one resource including tags and raw provider fields.
- `POST /api/scans/{id}/reconcile`
  - Response: graph edge and cleanup candidate counts.
- `GET /api/graph`
  - Query filters: `scan_id`, `resource_id`, `vpc_id`.
  - Response: resource relationship edges.
- `GET /api/candidates`
  - Query filters: `scan_id`, `rule_id`, `team`, `region`, `type`, `risk`, `status`.
  - Response: cleanup candidate list.
- `PATCH /api/candidates/{id}`
  - Request: `status`.
  - Response: updated candidate.
- `POST /api/plans`, `GET /api/plans`, `GET /api/plans/{id}`
  - Create and inspect cleanup plans. Plans are dry-run with the default executor and live only when a live executor is explicitly configured. Plan creation accepts `max_resource_count`, `max_region_count`, and `max_high_risk_count` guardrails.
- `GET /api/plans/{id}/export`
  - Query parameter `format=json|markdown`; exports plan details and item evidence for review.
- `POST /api/plans/{id}/approve`, `POST /api/plans/{id}/execute`
  - Approve and execute a plan through the configured executor.
- `GET /api/audits`, `GET /api/audits/export`, `GET /api/reports/savings`
  - Response: audit events, CSV/JSON audit export, and savings aggregates.

All API errors use `{ "error": { "code": "...", "message": "...", "request_id": "..." } }`.

## Resource Model

Each resource stores:

- `provider`: `alicloud` for live scans and `demo` for local demo scans.
- `account_id`, `region`, `type`, `native_id`.
- `name`, `state`, `created_at`, `last_seen_at`.
- `tags` as JSON.
- ownership fields derived from tags: `owner`, `team`, `application`, `environment`, `cost_center`.
- `protected` when tags indicate production or explicit protection.
- `raw` as JSON for original provider fields.

The first implementation covers ECS instances, disks, EIPs, security groups, snapshots, VPCs, and vSwitches. Demo mode produces representative resources of those types so the UI and API are useful without cloud credentials.

## Error Handling

Validation errors return HTTP 400 with field-specific messages in the text. Missing records return 404. Connector failures mark the scan job `failed` with `failure_reason` and preserve prior resources. Worker panics are recovered at job boundaries and converted into failed jobs.

## Testing

Backend behavior is implemented test-first:

- Domain normalization and protection tag extraction.
- In-memory repository scan-job transitions.
- Scan service success and connector-failure paths.
- API handler validation and resource response shapes.
- CLI status file read/write behavior.

The final MVP verification gates are:

- `go test ./...`
- `npm --prefix web run build`
- server smoke: start API with SQLite and the default dry-run executor, create demo scan, poll succeeded job, reconcile graph/candidates, create/approve/execute a dry-run plan, and query audits/reports.
