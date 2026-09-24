---
title: "Troubleshooting"
description: "Fix common problems: an empty inventory, missing or outdated resources, connection and permission errors, blocked cleanup, and credentials that can't be decrypted."
navTitle: "Troubleshooting"
---

Start with the symptom closest to yours. Each cloud guide also has a troubleshooting section for provider-specific errors: [AWS](./aws.md) · [Alibaba Cloud](./alicloud.md) · [Google Cloud](./gcp.md) · [Azure](./azure.md).

## Inventory and scans

| Symptom | What to check |
| --- | --- |
| The inventory is empty | Adding a connection does not scan it. Check that the right connection is selected, then [start a scan](./scans.md#scope) and open its details for failed items. |
| A resource you know exists is missing | Search by its native resource ID. Check the connection, region, and resource type, and whether the scan included that region — and **Global** for global resources. Open the scan and look for failed items. |
| A new resource or change hasn't appeared yet | Some sources update with a delay, such as Alibaba Cloud Resource Center and Google Cloud Asset Inventory. Scan again later. |
| Properties show an old value | Compare the **last seen** time with your change. Reloading the page doesn't rescan; scan the scope again. |
| Some scan items failed with permission errors | The connection is missing read permission for those resource types. A successful connection validation checks only the identity. Grant the permission named in the error (see the cloud guide), then use **Retry** on the scan. |
| The same name appears more than once | Compare the connection, region, resource type, and native resource ID. |
| An expected relationship line is missing | Check that both resources were scanned and that Steward supports that relationship. A missing line does not mean there is no dependency. See [Resource relationships](./topology.md). |

## Connections

| Symptom | What to check |
| --- | --- |
| Validation fails | Open the error details for the error code and request ID. Check permissions, the expiration time, and that the key ID and secret belong together. |
| A connection says authorization is required | Temporary credentials expired, or a browser sign-in was revoked or reached its expiry. Use **Replace credential**. See [Maintain a connection](./connections.md#maintain). |
| Browser sign-in never completes | The browser and the Steward server must be on the same machine. For a remote server, use an access key or [OIDC](./oidc.md). |
| A region is missing from the connection | Refresh the region list, check the region-listing permission in the cloud guide, and make sure the region is enabled in your cloud account. |

## Cleanup

| Symptom | What to check |
| --- | --- |
| A task shows blockers | A resource outside the task still depends on a target, or a dependency couldn't be verified. See [Review blockers and retained resources](./cleanup.md#review). |
| Deletion fails with a permission error | Read the resource's entry in **Cleanup logs**. Reading and scanning don't require deletion permissions, so these are often missing. Grant them, then continue the task. |
| A resource is still billed after cleanup | Check retained and skipped resources in the task results, then scan again. |

## Self-hosted server

| Symptom | What to check |
| --- | --- |
| The server refuses to listen on a network address | Local mode accepts only loopback addresses. Switch to token mode as described in [Give your team access](./deployment.md#network). |
| Stored credentials can't be decrypted after a restart, restore, or move | Steward is using a different credential key. Restore the original `credential-master-key` in the data directory (`~/.steward` or `STEWARD_HOME`), or set `STEWARD_CREDENTIAL_MASTER_KEY` to the original value. Never overwrite the old key with a new one. |
| After an upgrade or a move, the inventory looks empty | Steward is reading a different data directory. Check that `STEWARD_HOME` points to the directory used before. Earlier releases kept data in `./.steward` under the working directory; if the server prints “Using data in …” at startup, move that directory to `~/.steward` or set `STEWARD_HOME` to it. |

## Report a problem

Open a [GitHub issue](https://github.com/loomx-ai/steward/issues) with the Steward version (`steward version`), the steps to reproduce, the error code, and a redacted request ID. Never attach tokens, access keys, databases, or encryption keys.
