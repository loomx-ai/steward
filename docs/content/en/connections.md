---
title: "Cloud connections"
description: "Each connection represents a cloud identity, with its own inventory, scans, and cleanup tasks."
navTitle: "Cloud connections"
---

<span id="credentials"></span>

## Prepare credentials

Use a dedicated RAM or IAM identity. Start with read permissions for inventory; add deletion and related action permissions for the resource types you intend to clean up.

| Provider | Credential options |
| --- | --- |
| Alibaba Cloud | AccessKey ID + AccessKey Secret<br>STS: also supply a Security Token and expiration time. |
| AWS | Access Key ID + Secret Access Key<br>Session credentials: also supply a Session Token and expiration time. |

Alibaba Cloud also supports browser authorization when the login option is available. Choose the China or International site to match your account.

Successful validation confirms the identity, not permission to call every resource API. Resolve permission errors reported by the scan.

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
