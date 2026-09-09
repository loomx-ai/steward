---
title: "Troubleshooting"
description: "Resolve inventory, credential, and cleanup permission issues."
navTitle: "Troubleshooting"
---

## The inventory is empty

Check the selected connection, scan scope, and failed targets. Creating a connection does not complete an inventory scan.

## Credentials cannot be decrypted after restart

Restore the original key and check the database and working directory. Do not overwrite the old key with a new one.

## Cleanup reports a permission error

Read the resource’s logs. Successful connection validation or scanning does not imply deletion permission.

## Reporting an issue

Include the version, steps, error code, and a redacted request ID. Do not attach tokens, access keys, databases, or encryption keys. [GitHub Issues ↗](https://github.com/loomx-ai/steward/issues)
