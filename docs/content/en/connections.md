---
title: "Cloud connections"
description: "Choose a credential type for AWS, Alibaba Cloud, Google Cloud, or Azure, add a connection, and keep its credentials current."
navTitle: "Cloud connections"
---

A cloud connection gives Steward access to one cloud identity: an AWS account, an Alibaba Cloud account, a Google Cloud project, or an Azure subscription. Each connection has its own inventory, scans, and cleanup tasks. Add one connection per account you want to inventory.

<span id="credentials"></span>

## Choose a credential type

Use a dedicated identity for Steward. Grant read permissions for inventory first; add deletion permissions later, only for the resource types you plan to clean up.

| Cloud | Key-based credentials | Browser sign-in |
| --- | --- | --- |
| [AWS](./aws.md) | Access Key ID + Secret Access Key. For temporary credentials, also the Session Token and expiration time. | Through IAM Identity Center |
| [Alibaba Cloud](./alicloud.md) | AccessKey ID + AccessKey Secret. For STS, also the Security Token and expiration time. | Yes |
| [Google Cloud (GCP)](./gcp.md) | Project ID + service account JSON key | Yes |
| [Microsoft Azure](./azure.md) | Subscription ID + Tenant ID + Application (client) ID + Client secret | Yes |

Which to choose:

- **Key-based credentials** suit a stable, unattended setup and a server deployment. They are the option available in Steward Cloud.
- **Browser sign-in** is the quickest way to try Steward on your own computer. The connection reads what your personal account can read.
- **[OIDC workload identity](./oidc.md)** avoids storing long-lived cloud keys. It is available for all four clouds once the server operator has configured it.

Steward stores credentials encrypted. A successful validation confirms the identity, not permission to read every resource type — each scan reports the permissions it is missing. The cloud guides linked above list the permissions for inventory and cleanup.

<span id="browser"></span>

### Sign in with your browser

Instead of pasting a key, you can sign in on the cloud's own sign-in page. Steward receives the authorization on a loopback address and stores only the resulting tokens.

What each cloud asks for:

- **Alibaba Cloud** — choose the China or International site to match your account. Nothing else.
- **Google Cloud** — after signing in, pick one of the projects your account can access.
- **Microsoft Azure** — after signing in, pick one of the subscriptions your account can access. The tenant comes with it.
- **AWS** — before signing in, enter your IAM Identity Center start URL and its region; they decide which directory you sign in to. Afterwards, pick one of your assigned accounts and roles. AWS has no account-wide browser sign-in, so this option requires IAM Identity Center; otherwise use an access key or OIDC.

Two limits to know before choosing this option:

- **Your browser and the Steward server must run on the same machine.** The cloud redirects the authorization to `127.0.0.1` — the machine running your browser — so a Steward server on another host never receives it. For remote deployments, use an access key or [OIDC](./oidc.md). Steward Cloud does not offer browser sign-in for the same reason.
- **The connection acts as you, not as a service identity.** It reads exactly what your own account can read. Resource types you can't see are missing from the inventory, and a directory or data-plane permission you lack shows up as a failed scan item. For an unattended, stable scope, use a service account, service principal, or access key.

Browser sign-ins renew automatically while they remain valid. If the authorization is revoked, or the AWS client registration reaches its 90-day expiry, the connection reports that authorization is required. Use **Replace credential** and sign in again.

<span id="add"></span>

## Add a connection

1. Open the user menu → **Settings** → **Cloud connections** → **Add connection**.
2. Enter a name you will recognize, such as "Production", and choose the cloud and credential type.
3. Enter the credentials, or sign in, and create the connection. Steward validates the cloud identity before saving.
4. Check the region list. If region discovery is still running, refresh after a moment. If a region you use is missing, check the region-listing permission in the cloud guide, or add the region manually.

Next, select the connection and [run a scan](./scans.md).

<span id="maintain"></span>

## Maintain a connection

- **Rotate or renew credentials** with **Replace credential**. The new credentials must belong to the same cloud identity; to switch accounts, add a new connection.
- **If validation fails**, open the error details for the error code and request ID, then check the permissions, the expiration time, and that the key ID and secret belong together.
- **Deleting a connection** permanently destroys its stored credential; its historical records are kept. Nothing in your cloud is changed. To confirm, type the connection's name.
