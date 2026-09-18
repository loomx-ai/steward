---
title: "Configuration reference"
description: "Configure the listen address, database, authentication, and scan concurrency."
navTitle: "Configuration"
---

## Common settings

| Variable | Purpose / default |
| --- | --- |
| `STEWARD_AUTH_MODE` | `local` / `token`; defaults to local, or token mode when a token is set |
| `STEWARD_AUTH_TOKEN` | Sign-in token for token mode |
| `STEWARD_ADDR` | Listen address; 127.0.0.1:8585 |
| `STEWARD_HOME` | Data directory for the SQLite database, credential key, and server status; `~/.steward` |
| `STEWARD_DB_DRIVER` | sqlite / postgres; default sqlite |
| `STEWARD_DB_DSN` | SQLite path or PostgreSQL connection string |
| `STEWARD_CREDENTIAL_MASTER_KEY` | Base64-encoded 32-byte key; retain permanently |
| `STEWARD_SCAN_CONCURRENCY` | Scan concurrency; default 4, positive integer |
| `STEWARD_AUTH_ROLE` | viewer / operator / admin; token role, default admin |

The command line uses Chinese when `LC_ALL`, `LC_MESSAGES`, or `LANG` selects a Chinese locale, and English otherwise.



[Network access and authentication →](./deployment.md#network)
