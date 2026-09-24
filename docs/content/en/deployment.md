---
title: "Run Steward on a server"
description: "Run Steward as a long-lived service, give your team secure access with a sign-in token and HTTPS, and back up the data it needs."
navTitle: "Deploy the server"
---

By default, `steward server start` runs in local mode: no sign-in, reachable only from the same machine. Use this page when you want Steward to keep running on a server and be reachable by other people.

<span id="build"></span>

## Run Steward as a service

[Install Steward](./installation.md) on the server, then run it under your process manager (for example systemd):

```sh
steward server start
```

- Steward stores its data in `~/.steward` of the user running it. Under a process manager, set `STEWARD_HOME` to a persistent directory that the service user can write.
- `steward server status` shows whether the server is running, with its address and version. `steward server stop` stops it.
- After [updating](./installation.md#update), restart the service to run the new version.

<span id="network"></span>

## Give your team access

Local mode refuses to listen on anything other than a loopback address. To accept connections from other machines, switch to token mode, put an HTTPS reverse proxy in front, and allow only the proxy to reach Steward's port.

1. Generate a sign-in token and a credential encryption key **once**, and store both in your secret manager:

   ```sh
   openssl rand -hex 32     # sign-in token
   openssl rand -base64 32  # credential encryption key
   ```

2. Pass them to the service through your process manager. Replace the placeholders with the saved values:

   ```
   STEWARD_AUTH_MODE=token
   STEWARD_AUTH_TOKEN=<saved-token>
   STEWARD_CREDENTIAL_MASTER_KEY=<saved-base64-key>
   STEWARD_ADDR=0.0.0.0:8585
   ```

3. Configure HTTPS on the reverse proxy and point it at port 8585.
4. Open the HTTPS address and sign in with the token.

Keep the same token and key across restarts; do not generate new ones each time. If you are moving an existing local installation to token mode, set `STEWARD_CREDENTIAL_MASTER_KEY` to the contents of its `~/.steward/credential-master-key`, or its stored cloud credentials will be unreadable.

The token signs in with the role set in `STEWARD_AUTH_ROLE` (default `admin`). See [roles](./configuration.md#roles). Browser sign-in connections do not work when your browser is on a different machine from the server; use access keys or [OIDC](./oidc.md) instead.

To use PostgreSQL instead of the built-in SQLite database, set `STEWARD_DB_DRIVER=postgres` and `STEWARD_DB_DSN`. See the [configuration reference](./configuration.md).

<span id="backup"></span>

## Back up and restore

Steward needs both the database and the credential encryption key to read stored cloud credentials. Back them up together.

- **SQLite (default):** stop the server, then copy the whole data directory (`~/.steward` or `STEWARD_HOME`). It contains `steward.db` and `credential-master-key`.
- **PostgreSQL:** use your database's own backup tools, and keep the `STEWARD_CREDENTIAL_MASTER_KEY` value with the backup.
- If you keep the database or key somewhere else, include those locations as well.

To restore, put the original database and the original key back in place. A different key cannot decrypt the stored credentials.

[Configuration reference →](./configuration.md) · [Troubleshooting →](./troubleshooting.md)
