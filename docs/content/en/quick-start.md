---
title: "Quick start"
description: "Open a Steward Cloud workspace or start Steward on your own machine, connect a cloud account, and run your first scan."
navTitle: "Quick start"
---

Choose where Steward runs. Both options use the same interface and the same steps once you're in.

| | Steward Cloud | Self-hosted |
| --- | --- | --- |
| Setup | Sign up in the browser | Install one executable |
| Who runs it | LoomX | You, on your computer or server |
| Where data is stored | LoomX-hosted workspace | Your own data directory |
| Ways to connect a cloud | Keys and secrets (including temporary credentials) | Keys and secrets, plus [browser sign-in](./connections.md#browser) and, once configured, [OIDC](./oidc.md) |

<span id="cloud"></span>

## Use Steward Cloud

1. Open [Steward Cloud](https://steward.console.loomx.ai/auth/signup) and sign up with your email, or continue with Google or GitHub.
2. If you signed up with email, open the verification link within 30 minutes.
3. Wait while your personal workspace is prepared, then select **Open workspace**. If capacity is full, your workspace is queued; use **Refresh status** to check again.

Continue with [Connect and scan](#first-scan).

<span id="requirements"></span>

## Run Steward yourself

[Install Steward](./installation.md) for your operating system. The executable includes the web console and database; nothing else is required.

<span id="run"></span>

Start the server:

```sh
steward server start
```

Open [http://127.0.0.1:8585](http://127.0.0.1:8585). In this local mode there is no sign-in, and the server accepts connections only from the same machine. Press Ctrl+C to stop it. To make Steward available to other people, see [Run Steward on a server](./deployment.md).

<span id="first-scan"></span>

## Connect and scan

1. Open the user menu → **Settings** → **Cloud connections** → **Add connection**. Choose your cloud and credential type, enter the credentials, and create the connection. Steward checks which cloud identity they belong to before saving. See [Cloud connections](./connections.md) for credential options and permissions.
2. Check the region list on the new connection. It should include the regions you use.
3. Select the connection, open **Scans** → **Start scan**, and scan one region where you know a resource exists.
4. When the scan finishes, open **Resources** and search for that resource, or open **Resource Panorama** to see it in its network.

[First resource inventory](./tutorials/first-inventory.md) walks through the same steps with expected results and troubleshooting at each stage.

<span id="data"></span>

## Where self-hosted data lives

Steward keeps its data in `~/.steward` (`%USERPROFILE%\.steward` on Windows): the database `steward.db` and the credential encryption key `credential-master-key`. Set `STEWARD_HOME` to use another directory.

<aside class="docs-note">Back up the database and its key together. Without the original key, Steward cannot decrypt the cloud credentials stored in the database.</aside>

[Network access and backups →](./deployment.md)
