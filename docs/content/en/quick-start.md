---
title: "Quick start"
description: "Run Steward locally, then connect a cloud account."
navTitle: "Quick start"
---

<span id="requirements"></span>

## Prerequisites

Install Git, Go 1.26+, Node.js 22+, npm, and make. The build downloads Go and npm dependencies.

<span id="run"></span>

## Install and start

```
git clone https://github.com/loomx-ai/steward.git
cd steward
make install
make run
```

Open [http://127.0.0.1:8585](http://127.0.0.1:8585). No account or login is required. The server accepts local connections by default.

Press Ctrl+C to stop. On subsequent starts, use the built binary:

```
./bin/steward server start
```

<span id="first-scan"></span>

## Connect and scan

With the server running, follow [First resource inventory](./tutorials/first-inventory.md) through the steps below, including expected results and troubleshooting.

1.  Open Settings from the user menu and add a cloud connection.
2.  Choose a provider, enter credentials, and check the region list after validation.
3.  Select the connection, open Scans, and start with one region you use.
4.  Once the scan finishes, open Resources or Resource panorama.

[Credential types and connection setup →](./connections.md)

<span id="development"></span>

## Development mode

```
make dev
```

Open [http://127.0.0.1:5858](http://127.0.0.1:5858). The frontend supports hot reload, and the proxy uses a generated token for API access. Do not run it alongside make run: both use the same API port.

<span id="data"></span>

## Where data lives

The default database is .steward/steward.db; the credential key is .steward/credential-master-key. Paths are relative to the directory where you start Steward. Use the same directory on restart.

<aside class="docs-note">Back up the database and its original key together. Replacing the key makes existing cloud credentials unreadable.</aside>

[Network access, backups, and upgrades →](./deployment.md)
