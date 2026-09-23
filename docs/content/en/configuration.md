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
| `STEWARD_DB_MAX_CONNS` | PostgreSQL connection pool limit; unlimited by default, positive integer. Keep it below the database role's connection limit |
| `STEWARD_CREDENTIAL_MASTER_KEY` | Base64-encoded 32-byte key; retain permanently |
| `STEWARD_SCAN_CONCURRENCY` | Scan concurrency; default 4, positive integer |
| `STEWARD_AUTH_ROLE` | viewer / operator / admin; token role, default admin |

The command line uses Chinese when `LC_ALL`, `LC_MESSAGES`, or `LANG` selects a Chinese locale, and English otherwise.

## Release checks

Steward asks `checkpoint.loomx.ai` whether a newer release exists and whether a
security advisory applies to the running build. `steward version` reports the
answer, any command reports an advisory, and a running server checks again once
a day.

The request carries four values and nothing else:

| Value | Example |
| --- | --- |
| Running version | `0.4.1` |
| Operating system | `linux` |
| Architecture | `amd64` |
| Signature | `4f0b…` a random UUID kept in `~/.steward/checkpoint_signature` |

The signature counts one installation once and keeps an advisory from repeating.
It is random: nothing about you, the machine, or the network goes into it.
Delete the file to be issued a new one. Your inventory, connections, credentials,
scan results, and the commands you run are never sent.

The answer is cached in `~/.steward/checkpoint_cache` for a day, so a machine
reaches the service at most once a day however many commands it runs. A check
times out after three seconds and a failure is silent — no command depends on it.

| Variable | Purpose / default |
| --- | --- |
| `STEWARD_CHECKPOINT_DISABLE` | Any value but `0` stops the check entirely |
| `DO_NOT_TRACK` | Honoured the same way |
| `STEWARD_CHECKPOINT_SIGNATURE_DISABLE` | Keep checking, but send no signature |
| `STEWARD_CHECKPOINT_URL` | Endpoint to ask; `https://checkpoint.loomx.ai` |
| `STEWARD_CHECKPOINT_TIMEOUT` | Request timeout as a Go duration; `3s` |

Checks are also off in continuous integration: when `CI` is set (GitHub Actions,
GitLab, CircleCI, Travis, Buildkite, Bitbucket) or when `TF_BUILD`, `JENKINS_URL`,
`TEAMCITY_VERSION` or `CODEBUILD_BUILD_ID` is. A CI job starts from a fresh home
directory, so a check there would count a new installation on every run. A value
of `0` or `false` does not count as CI.

`steward update` downloads from GitHub releases directly, so upgrading keeps
working with checks switched off.



[Network access and authentication →](./deployment.md#network)
