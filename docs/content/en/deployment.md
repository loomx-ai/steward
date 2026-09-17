---
title: "Deploy Steward"
description: "Run the server, configure network access, and back up its data."
navTitle: "Deploy the server"
---

<span id="build"></span>

## Run the server

Follow [Installation](./installation.md), then start Steward from a stable directory:

```sh
mkdir -p "$HOME/steward-data"
cd "$HOME/steward-data"
steward server start
```

When using a process manager, set its working directory to this directory and allow writes to `.steward/`.

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

<span id="backup"></span>

## Back up data

Stop the server and back up the entire `.steward/` directory, including the database and its original credential key. Include databases or keys stored elsewhere. Use database-native backup tools for PostgreSQL.

Restore the original database and key, and use the same working directory. Replacing the key makes stored credentials unreadable.

[Configuration reference →](./configuration.md) · [Troubleshooting →](./troubleshooting.md)
