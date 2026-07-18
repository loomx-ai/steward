# Steward

<p align="center">
  <em>Discover, understand, and safely clean up cloud resources.</em>
</p>

<p align="center">
  <a href="https://github.com/loomx-ai/steward/actions/workflows/test.yml"><img src="https://github.com/loomx-ai/steward/actions/workflows/test.yml/badge.svg" alt="Tests"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue.svg" alt="Apache-2.0 license"></a>
</p>

<p align="center">
  <strong>Language:</strong> English | <a href="README.zh-CN.md">简体中文</a>
</p>

Steward is an open-source cloud governance service for inventory,
topology, findings, and controlled resource cleanup. It currently supports
Alibaba Cloud and AWS.

## Features

- Build a searchable inventory from provider-native APIs.
- Explore account, region, network, and resource relationships.
- Detect governance findings from normalized resource data.
- Plan and execute cleanup with dependency ordering, safety blockers,
  idempotency, readback, and audit trails.

## Quick start

Requirements: Go 1.26+, Node.js 22+, and npm.

```bash
git clone git@github.com:loomx-ai/steward.git
cd steward
make install
make dev
```

Open <http://127.0.0.1:5858>. The development command creates a local login
token and credential-encryption key automatically.

To build and run the production server locally:

```bash
export STEWARD_AUTH_TOKEN="$(openssl rand -hex 32)"
export STEWARD_CREDENTIAL_MASTER_KEY="$(openssl rand -base64 32)"
make run
```

Open <http://127.0.0.1:8585> and sign in with the bearer token. SQLite data is
stored in `.steward/steward.db` by default.

## Development

```bash
make test
make lint
make build
```

PostgreSQL contract tests use `STEWARD_TEST_POSTGRES_DSN` and are skipped
when it is not set.

Contributions are welcome. Please open an issue before proposing a substantial
change, and include tests for changed behavior.

## License

Copyright 2026 Prodesire

Licensed under the [Apache License 2.0](LICENSE).
