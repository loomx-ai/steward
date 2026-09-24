---
title: "Configuration reference"
description: "Environment variables for the listen address, database, sign-in, roles, scan concurrency, OIDC, and release checks, with their defaults."
navTitle: "Configuration"
---

Steward is configured with environment variables. `steward server start` also accepts the most common ones as flags; run `steward server start --help` to list them.

## Server settings

| Variable | Purpose | Default |
| --- | --- | --- |
| `STEWARD_ADDR` | Listen address | `127.0.0.1:8585` |
| `STEWARD_HOME` | Data directory for the SQLite database, credential key, and server status file | `~/.steward` |
| `STEWARD_AUTH_MODE` | `local` (no sign-in, loopback only) or `token` | `local`, or `token` when a token is set |
| `STEWARD_AUTH_TOKEN` | Sign-in token for token mode | — |
| `STEWARD_AUTH_ROLE` | Role granted to the token: `viewer`, `operator`, or `admin` | `admin` |
| `STEWARD_AUTH_SUBJECT` | Name recorded in audit events for actions taken with the token | `local-admin` |
| `STEWARD_CREDENTIAL_MASTER_KEY` | Base64-encoded 32-byte key that encrypts stored cloud credentials. Local mode with SQLite creates `credential-master-key` in the data directory automatically; token mode and PostgreSQL need this variable. Keep it for as long as you keep the data. | — |
| `STEWARD_DB_DRIVER` | `sqlite` or `postgres` | `sqlite` |
| `STEWARD_DB_DSN` | SQLite file path or PostgreSQL connection string | `steward.db` in the data directory |
| `STEWARD_DB_MAX_CONNS` | PostgreSQL connection pool limit, a positive integer. Keep it below the database role's connection limit. | Unlimited |
| `STEWARD_SCAN_CONCURRENCY` | Number of scan items run at the same time, a positive integer | `4` |

[OIDC connections](./oidc.md) use `STEWARD_OIDC_ISSUER_URL`, `STEWARD_OIDC_WORKSPACE_ID`, and `STEWARD_OIDC_SIGNING_KEY_FILE`.

The command line uses Chinese when `LC_ALL`, `LC_MESSAGES`, or `LANG` selects a Chinese locale, and English otherwise.

<span id="roles"></span>

## Roles

| Role | Can do |
| --- | --- |
| `viewer` | Read connections, scans, inventory, relationships, cleanup tasks, and audit events |
| `operator` | Everything a viewer can, plus start and control scans, mark resources, and create, execute, pause, and resume cleanup tasks |
| `admin` | Everything an operator can, plus add, rename, validate, replace credentials for, and delete cloud connections |

In local mode you are always an admin.

## Release checks

Steward asks `checkpoint.loomx.ai` whether a newer release exists and whether a security advisory applies to the version you run. `steward version` shows the answer, every command shows an applicable advisory, and a running server checks again once a day.

The request carries four values and nothing else:

| Value | Example |
| --- | --- |
| Running version | `0.3.1` |
| Operating system | `linux` |
| Architecture | `amd64` |
| Signature | `4f0b…`, a random UUID kept in `~/.steward/checkpoint_signature` |

The signature lets LoomX count one installation once and avoid repeating an advisory. It is random: nothing about you, the machine, or the network goes into it. Delete the file to get a new one. Your inventory, connections, credentials, scan results, and the commands you run are never sent.

The answer is cached in `~/.steward/checkpoint_cache` for a day, so a machine contacts the service at most once a day however many commands it runs. A check times out after three seconds and fails silently; no command depends on it.

| Variable | Purpose | Default |
| --- | --- | --- |
| `STEWARD_CHECKPOINT_DISABLE` | Any value other than `0` turns the check off | — |
| `DO_NOT_TRACK` | Same as above | — |
| `STEWARD_CHECKPOINT_SIGNATURE_DISABLE` | Keep checking, but send no signature | — |
| `STEWARD_CHECKPOINT_URL` | Endpoint to ask | `https://checkpoint.loomx.ai` |
| `STEWARD_CHECKPOINT_TIMEOUT` | Request timeout as a Go duration | `3s` |

Checks are also off in continuous integration: when `CI` is set (GitHub Actions, GitLab, CircleCI, Travis, Buildkite, Bitbucket), or when `TF_BUILD`, `JENKINS_URL`, `TEAMCITY_VERSION`, or `CODEBUILD_BUILD_ID` is set. A value of `0` or `false` does not count as CI. CI jobs start from a fresh home directory, so a check there would count a new installation on every run.

`steward update` downloads from GitHub Releases directly, so updating still works with checks turned off.

[Network access and sign-in →](./deployment.md#network)
