---
title: "Quick start"
description: "Run Steward locally, then connect a cloud account."
navTitle: "Quick start"
---

<span id="requirements"></span>

## Prerequisites

Choose an [installation method](./installation.md). Steward includes the web console and database; no additional runtime is required.

<span id="run"></span>

## Start Steward

```sh
steward server start
```

Open [http://127.0.0.1:8585](http://127.0.0.1:8585). No account or login is required. The server accepts local connections by default.

Press Ctrl+C to stop.

<span id="first-scan"></span>

## Connect and scan

With the server running, follow [First resource inventory](./tutorials/first-inventory.md) through the steps below, including expected results and troubleshooting.

1.  Open Settings from the user menu and add a cloud connection.
2.  Choose a provider, enter credentials, and check the region list after validation.
3.  Select the connection, open Scans, and start with one region you use.
4.  Once the scan finishes, open Resources or Resource panorama.

[Credential types and connection setup →](./connections.md)

<span id="data"></span>

## Where data lives

Data is stored in `~/.steward` (on Windows, `%USERPROFILE%\.steward`): the database `steward.db` and the credential key `credential-master-key`. Set `STEWARD_HOME` to use another directory.

<aside class="docs-note">Back up the database and its original key together. Replacing the key makes existing cloud credentials unreadable.</aside>

[Network access and backups →](./deployment.md)
