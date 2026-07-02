# MVP Scan Loop Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Build the first Cloud Steward MVP slice: local server lifecycle, API, SQLite-backed local testing, optional MySQL-backed scan jobs/resources, demo and Alibaba Cloud scan connectors, and a React Scan Console.

**Architecture:** One Go binary serves API and embedded Web UI. A repository interface isolates GORM-backed SQLite/MySQL persistence from core scan orchestration, and the scanner package exposes connector adapters so tests use a fake connector while runtime can use demo or Alibaba Cloud mode. The React app is a dense operational console for scan creation, scan status, resource list, and resource detail.

**Tech Stack:** Go 1.26, chi, cobra, GORM, SQLite for local testing, goose-style SQL migrations for MySQL, React, Vite, TypeScript, Tailwind CSS, lucide-react.

---

### Task 1: Backend Module And Domain Tests

**Files:**
- Create: `go.mod`
- Create: `cmd/steward/main.go`
- Create: `internal/domain/models.go`
- Create: `internal/domain/models_test.go`

- [ ] Initialize the Go module as `github.com/prodesire/cloud-steward`.
- [ ] Write `internal/domain/models_test.go` first with tests for `ExtractOwnership`, `IsProtectedResource`, and resource validation.
- [ ] Run `go test ./internal/domain` and confirm it fails because the package does not exist.
- [ ] Implement `internal/domain/models.go` with account, scan job, resource, error envelope, ownership extraction, and protection helpers.
- [ ] Run `go test ./internal/domain` and confirm it passes.

### Task 2: Repository Contract And In-Memory Store

**Files:**
- Create: `internal/store/store.go`
- Create: `internal/store/memory.go`
- Create: `internal/store/memory_test.go`

- [ ] Write tests for creating scan jobs, claiming pending jobs, marking success/failure, upserting resources, filtering resources, and fetching details.
- [ ] Run `go test ./internal/store` and confirm it fails on missing store code.
- [ ] Implement repository interfaces and an in-memory store used by tests and demo smoke.
- [ ] Run `go test ./internal/store` and confirm it passes.

### Task 3: Scan Service And Demo Connector

**Files:**
- Create: `internal/scanner/scanner.go`
- Create: `internal/scanner/demo.go`
- Create: `internal/scanner/service.go`
- Create: `internal/scanner/service_test.go`

- [ ] Write tests proving a pending job is marked running, demo resources are persisted, success stores counts, and connector errors mark the job failed without deleting previous resources.
- [ ] Run `go test ./internal/scanner` and confirm it fails on missing scanner code.
- [ ] Implement connector interfaces, demo connector resources for ECS/disk/EIP/security group/snapshot/VPC/vSwitch, and scan service orchestration.
- [ ] Run `go test ./internal/scanner` and confirm it passes.

### Task 4: HTTP API

**Files:**
- Create: `internal/api/router.go`
- Create: `internal/api/handlers.go`
- Create: `internal/api/handlers_test.go`

- [ ] Write handler tests for `POST /api/scans`, `GET /api/scans`, `GET /api/scans/{id}`, `GET /api/resources`, `GET /api/resources/{id}`, validation failures, and JSON error envelopes.
- [ ] Run `go test ./internal/api` and confirm it fails on missing handlers.
- [ ] Implement chi router and handlers against the store and scanner service.
- [ ] Run `go test ./internal/api` and confirm it passes.

### Task 5: SQL Stores And Migrations

**Files:**
- Create: `migrations/00001_init.sql`
- Create: `internal/store/mysql.go`
- Create: `internal/store/migrate.go`
- Create: `internal/store/sqlite_test.go`

- [ ] Add explicit SQL schema for accounts, scan_jobs, resources, and resource_snapshots.
- [ ] Implement GORM models that map to the domain model and preserve JSON fields for SQLite and MySQL.
- [ ] Implement migration execution through goose-compatible SQL files.
- [ ] Run `go test ./internal/store` after adding SQL store code and confirm existing tests still pass.

### Task 6: CLI And Server Runtime

**Files:**
- Create: `internal/cli/root.go`
- Create: `internal/cli/server.go`
- Create: `internal/cli/status_test.go`
- Create: `internal/server/server.go`
- Create: `internal/server/static.go`
- Create: `internal/webui/dist/index.html`

- [ ] Write CLI status-file tests before implementation.
- [ ] Implement `steward server start`, `steward server stop`, and `steward server status`.
- [ ] Implement HTTP server startup with graceful shutdown, worker polling, API router, and static fallback.
- [ ] Run `go test ./...` and fix failures.

### Task 7: Alibaba Cloud Connector Adapter

**Files:**
- Create: `internal/scanner/alicloud.go`
- Modify: `internal/scanner/service.go`

- [ ] Implement the connector selection path: `mode=demo` uses demo connector; `mode=alicloud` uses Alibaba Cloud SDK credentials and requested regions.
- [ ] Normalize ECS instances, disks, EIPs, security groups, snapshots, VPCs, and vSwitches into domain resources.
- [ ] Return clear connector errors when credentials or region are missing.
- [ ] Run `go test ./internal/scanner ./internal/api`.

### Task 8: React Scan Console

**Files:**
- Create: `web/package.json`
- Create: `web/index.html`
- Create: `web/tsconfig.json`
- Create: `web/vite.config.ts`
- Create: `web/src/main.tsx`
- Create: `web/src/App.tsx`
- Create: `web/src/api.ts`
- Create: `web/src/styles.css`

- [ ] Create a Vite React TypeScript app with a single Scan Console screen.
- [ ] Implement scan form, scan table, resource filters/list, and resource detail panel.
- [ ] Use icon buttons from `lucide-react` for refresh and detail actions.
- [ ] Run `npm --prefix web run build` and confirm it emits into `internal/webui/dist`.

### Task 9: Local Ops Docs And Verification

**Files:**
- Create: `README.md`
- Create: `.env.example`
- Create: `.gitignore`
- Modify: `docs/design.md` only if needed for consistency.

- [ ] Document SQLite local startup, optional MySQL DSN startup, demo scan, live Alibaba Cloud credential variables, and verification commands.
- [ ] Run `go test ./...`.
- [ ] Run `npm --prefix web run build`.
- [ ] Start the server, create a demo scan through the API, poll scan completion, list resources, and fetch one resource detail.
