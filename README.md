# Cloud Steward

Cloud Steward is a topology-aware cloud resource governance tool. This repository implements the local MVP loop: server lifecycle, Web API, SQLite/MySQL persistence, Alibaba Cloud/demo scan orchestration, resource graph derivation, cleanup candidates, cleanup plans, manual approval, guarded execution audit, savings report, and a React console.

Execution is guarded and dry-run by default in this MVP. The governance service re-reads each resource before execution, records per-item request IDs/results, and uses a replaceable executor interface. The optional `alicloud-tag` executor only performs Alibaba Cloud tag actions and skips unsupported or destructive actions.

## Local Development

Start the server with local SQLite:

```bash
go run ./cmd/steward server start
```

The default local database is `.cloud-steward/cloud-steward.db`. For an ephemeral in-memory run:

```bash
go run ./cmd/steward server start --db-driver memory
```

To use MySQL, start a local or managed MySQL 8.x service, create the `cloud_steward` database and user, then provide a DSN:

```bash
export CLOUD_STEWARD_DB_DSN='steward:steward@tcp(127.0.0.1:3306)/cloud_steward?parseTime=true&multiStatements=true'
go run ./cmd/steward server start --db-driver mysql
```

Open the console at:

```text
http://127.0.0.1:8585
```

Run a demo scan from the API:

```bash
curl -sS -X POST http://127.0.0.1:8585/api/scans \
  -H 'content-type: application/json' \
  -d '{"accountName":"local-demo","provider":"demo","mode":"demo","regions":["cn-hangzhou"]}'
```

Analyze the scan, inspect candidates, create a dry-run plan, approve it, and execute it through the default dry-run executor:

```bash
SCAN_ID="$(curl -sS http://127.0.0.1:8585/api/scans | jq -r '.[0].id')"
curl -sS -X POST "http://127.0.0.1:8585/api/scans/${SCAN_ID}/reconcile" \
  -H 'content-type: application/json' \
  -d '{"actor":"local-user"}'

curl -sS "http://127.0.0.1:8585/api/graph?scan_id=${SCAN_ID}"
curl -sS "http://127.0.0.1:8585/api/candidates?scan_id=${SCAN_ID}"

CANDIDATE_ID="$(curl -sS "http://127.0.0.1:8585/api/candidates?scan_id=${SCAN_ID}" | jq -r '.[0].id')"
PLAN_ID="$(curl -sS -X POST http://127.0.0.1:8585/api/plans \
  -H 'content-type: application/json' \
  -d "{\"candidate_ids\":[\"${CANDIDATE_ID}\"],\"actor\":\"local-user\",\"max_resource_count\":10,\"max_region_count\":2,\"max_high_risk_count\":0}" | jq -r '.id')"

curl -sS -X POST "http://127.0.0.1:8585/api/plans/${PLAN_ID}/approve" \
  -H 'content-type: application/json' \
  -d '{"actor":"local-user","comment":"approved locally"}'
curl -sS -X POST "http://127.0.0.1:8585/api/plans/${PLAN_ID}/execute" \
  -H 'content-type: application/json' \
  -d '{"actor":"local-user"}'

curl -sS "http://127.0.0.1:8585/api/plans/${PLAN_ID}/export?format=markdown"
curl -sS http://127.0.0.1:8585/api/audits
curl -sS "http://127.0.0.1:8585/api/audits/export?format=csv"
curl -sS http://127.0.0.1:8585/api/reports/savings
```

## CLI

The CLI only manages the local server:

```bash
steward server start
steward server status
steward server stop
```

It intentionally does not expose scan, candidate, plan, apply, or report commands. Those operations belong in the Web UI/API.

Use `--executor dry-run` for the default local path. To enable the live low-risk Alibaba Cloud tag subset:

```bash
go run ./cmd/steward server start --executor alicloud-tag
```

`alicloud-tag` adds `cloud-steward:*` metadata tags to resources whose plan action is `tag`. It requires Alibaba Cloud tag write permissions. It does not delete, release, stop, or detach resources.

## Alibaba Cloud Scans

Use `mode=alicloud` in the Web UI or API and provide AccessKey credentials. Credentials are stored in the configured local database for this MVP self-hosted workflow and are omitted from logs and API responses.

The live connector reads ECS instances, disks, security groups, snapshots, EIPs, VPCs, and vSwitches in the requested regions. Use a RAM user with read-only permissions for those services.

If you enable `--executor alicloud-tag`, the RAM user also needs tag permissions for the scanned ECS/VPC resource types. Keep the default `dry-run` executor for local SQLite smoke tests.

## Verification

```bash
go test ./...
npm --prefix web run build
```

## License

Copyright 2026 Prodesire

Licensed under the Apache License, Version 2.0. See the [LICENSE](LICENSE) file for the full text, or read it at http://www.apache.org/licenses/LICENSE-2.0.
