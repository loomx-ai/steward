---
title: "Cloud connections"
description: "Each connection represents a cloud identity, with its own inventory, scans, and cleanup tasks."
navTitle: "Cloud connections"
---

<span id="credentials"></span>

## Prepare credentials

Use a dedicated cloud identity. Start with read permissions for inventory; add deletion and related action permissions for the resource types you intend to clean up.

| Provider | Credential options |
| --- | --- |
| [AWS](./aws.md) | Access Key ID + Secret Access Key<br>Session credentials: also supply a Session Token and expiration time.<br>Browser sign-in through IAM Identity Center. |
| [Alibaba Cloud](./alicloud.md) | AccessKey ID + AccessKey Secret<br>STS: also supply a Security Token and expiration time.<br>Browser sign-in. |
| [Google Cloud (GCP)](./gcp.md) | Project ID + service account JSON key.<br>Browser sign-in. |
| [Microsoft Azure](./azure.md) | Subscription ID + Tenant ID + Application (client) ID + Client secret.<br>Browser sign-in. |

When the server is configured for workload identity, all four clouds offer [OIDC connections](./oidc.md), exchanging trusted workload identities for temporary credentials without uploading long-lived cloud keys.

<span id="browser"></span>

### Sign in with your browser

Every cloud can be connected by signing in through the browser instead of uploading a key. Steward opens the cloud's own sign-in page, receives the authorization on a loopback address, and stores only the resulting tokens.

What each cloud asks for differs:

-   **Alibaba Cloud** — choose the China or International site to match your account. Nothing else is asked for.
-   **Google Cloud** — after signing in, pick one of the projects your account can reach.
-   **Microsoft Azure** — after signing in, pick one of the subscriptions your account can reach. The tenant comes with it.
-   **AWS** — supply your IAM Identity Center start URL and its region before signing in, because they decide which directory to sign in to. Afterwards, pick one of the accounts and roles you are assigned. AWS has no account-wide browser sign-in, so this path needs IAM Identity Center; use an access key or OIDC otherwise.

Two limits are worth knowing before you choose this path:

-   **The browser and the Steward server must run on the same machine.** The cloud redirects the authorization to `127.0.0.1`, which is the machine running your browser. A Steward server on a remote host cannot receive it. Use an access key or [OIDC](./oidc.md) for remote deployments. Browser sign-in is not offered by Steward Cloud for the same reason.
-   **The connection acts as you, not as a service identity.** It reads exactly what your own account can read. A resource type your account cannot see will be missing from the inventory, and a directory or data-plane permission you lack will be reported as that source failing. A service account, service principal or access key is the better choice for an unattended, stable scope.

A browser sign-in is renewed automatically while it stays valid. If it is revoked, or if the AWS client registration reaches its ninety-day expiry, the connection reports that authorization is required again — use **Replace credential** and sign in once more.

Successful validation confirms the identity, not permission to call every resource API. Resolve permission errors reported by the scan.

Each cloud guide covers permissions, regional and global resources, the first scan, cleanup limits, and troubleshooting. Complete the relevant connection checks before expanding the scope.

<span id="add"></span>

## Add a connection

1.  Open the user menu → Settings → Cloud connections → Add connection.
2.  Give it a recognizable name, such as Production, and choose the provider and credential type.
3.  Enter the credentials and create the connection. Steward validates the cloud identity before saving it.
4.  Check the region list. Refresh if discovery is still running. If a region is missing, check permissions or add it manually.

<span id="maintain"></span>

## Maintain a connection

-   Use Replace credential after rotation or expiration. The replacement must belong to the original cloud identity.
-   If validation fails, open the error details for the code and request ID. Check permissions, expiration, and whether the key pair matches.
-   Deleting a connection removes its local records, not its cloud resources. Read the confirmation dialog for the affected data.
