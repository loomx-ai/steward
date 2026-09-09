---
title: "Deployment"
description: "Keep a stable data directory and encryption key. Use HTTPS for network access."
navTitle: "Deployment"
---

<span id="build"></span>

## Build the server

For a prebuilt release, follow [Installation](./installation.md) and run `steward server start` from a stable data directory. The binary includes the web interface and database migrations; no separate frontend server or source checkout is needed.

To build yourself, run these commands from the source directory:

```
make install
make build
./bin/steward server start
```

Local access defaults to 127.0.0.1:8585. With a process manager, keep the working directory set to the Steward directory and allow writes to .steward.

<span id="network"></span>

## Network access

Local mode rejects non-loopback listeners. For network deployment, use token mode with HTTPS at a reverse proxy, and restrict the backend port to the proxy.

Generate and securely store the token and encryption key once, then inject these variables through your process manager. Do not regenerate them at every start.

```
# Generate once; store the outputs securely
openssl rand -hex 32
openssl rand -base64 32
```

```
STEWARD_AUTH_MODE=token
STEWARD_AUTH_TOKEN=<saved-token>
STEWARD_CREDENTIAL_MASTER_KEY=<saved-base64-key>
STEWARD_ADDR=0.0.0.0:8585
```

This is a configuration template: replace the placeholders. Users open the HTTPS URL and sign in with the saved token. When migrating from local mode, reuse the value in the original .steward/credential-master-key.

<span id="config"></span>

## Common settings

| Variable | Purpose / default |
| --- | --- |
| `STEWARD_ADDR` | Listen address; 127.0.0.1:8585 |
| `STEWARD_DB_DRIVER` | sqlite / postgres; default sqlite |
| `STEWARD_DB_DSN` | SQLite path or PostgreSQL connection string |
| `STEWARD_CREDENTIAL_MASTER_KEY` | Base64-encoded 32-byte key; retain permanently |
| `STEWARD_SCAN_CONCURRENCY` | Scan concurrency; default 4, positive integer |
| `STEWARD_AUTH_ROLE` | viewer / operator / admin; token role, default admin |

<span id="backup"></span>

## Back up and upgrade

1.  Stop the server, then back up the entire .steward directory. Include databases or keys stored elsewhere. Use database-native backup tools for PostgreSQL.
2.  Update using the original installation method: `brew update && brew upgrade steward`, `scoop update steward`, the installer, or a newer release package. For a source build, update to the intended version and run `make install` and `make build`.
3.  Start with the original working directory, database, and key. Check connections and run a small scan.

<span id="troubleshooting"></span>

## Troubleshooting

### The inventory is empty

Check the selected connection, scan scope, and failed targets. Creating a connection does not complete an inventory scan.

### Credentials cannot be decrypted after restart

Restore the original key and check the database and working directory. Do not overwrite the old key with a new one.

### Cleanup reports a permission error

Read the resource’s logs. Successful connection validation or scanning does not imply deletion permission.

### Reporting an issue

Include the version, steps, error code, and a redacted request ID. Do not attach tokens, access keys, databases, or encryption keys. [GitHub Issues ↗](https://github.com/loomx-ai/steward/issues)
