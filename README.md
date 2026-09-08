<p align="center">
  <img src="web/public/brand/steward-symbol.svg" alt="Steward logo" width="96" height="96">
</p>

<h1 align="center">steward</h1>

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
Alibaba Cloud, AWS, Google Cloud (GCP), and Microsoft Azure.

[Documentation](https://loomx.ai/steward/docs) · [Quick start](https://loomx.ai/steward/docs/quick-start) · [Cleanup guide](https://loomx.ai/steward/docs/cleanup)

## Features

- Build a searchable inventory from provider-native APIs.
- Explore account, region, network, and resource relationships.
- Detect governance findings from normalized resource data.
- Plan and execute cleanup with dependency ordering, safety blockers,
  idempotency, readback, and audit trails.

## Quick start

Requirements: Go 1.26+, Node.js 22+, and npm.

```bash
git clone https://github.com/loomx-ai/steward.git
cd steward
make install
make dev
```

Open <http://127.0.0.1:5858>. No account or interactive login is required.
The development proxy protects the API with an automatically generated token.

To build and run the production server locally:

```bash
make run
```

Open <http://127.0.0.1:8585> and start using Steward without logging in.
SQLite data is stored in `.steward/steward.db`. A credential-encryption key is
generated once in `.steward/credential-master-key`; keep it with your database
backups. Existing databases must retain their original key.

### Network deployment

Local mode only listens on loopback and rejects cross-origin requests. To serve
Steward over a network, configure token authentication and put it behind HTTPS:

```bash
export STEWARD_AUTH_MODE=token
export STEWARD_AUTH_TOKEN="$(openssl rand -hex 32)"
export STEWARD_CREDENTIAL_MASTER_KEY="$(openssl rand -base64 32)"
./bin/steward server start --addr 0.0.0.0:8585
```

Sign in using the configured token. Setting a token without an explicit mode
also selects token authentication, preserving existing deployments.

Steward Cloud uses a separate account and workspace gateway. Its private
instances run with `STEWARD_AUTH_MODE=cloud`: each instance requires its own
service token and accepts the gateway's verified user identity for authorization
and auditing. These service tokens must never be distributed to browsers.

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

Copyright 2026 LoomX

Licensed under the [Apache License 2.0](LICENSE).
