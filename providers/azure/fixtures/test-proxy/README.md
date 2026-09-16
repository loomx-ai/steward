# Independent Azure SDK Test Proxy playback

`TestDataProtectionVaultIndependentProxy` starts Microsoft's standalone Test
Proxy as a separate loopback process. It exercises the registered, guarded Azure
action through the production client, native REST catalog and OAuth transport.
Only the final test transport redirects playback requests; credentials and
precondition responses are synthetic. No live cloud credentials are needed.

The public CLI DELETE and three polling responses come unchanged from
[`../dataprotection/vault-delete-recording.json`](../dataprotection/vault-delete-recording.json).
The test pins its SHA-256 before converting it to Test Proxy's recording format.
Inventory, dependency indexes, subscription validation and final vault reads
are explicitly synthetic: the upstream recording does not contain that evidence.
This is independent protocol playback, not a cloud emulator or live-cloud
acceptance, and does not close the provider parity gates.

## Reproduce

Download the appropriate standalone archive from the pinned official release:
[Azure.Sdk.Tools.TestProxy_1.0.0-dev.20260521.2](https://github.com/Azure/azure-sdk-tools/releases/tag/Azure.Sdk.Tools.TestProxy_1.0.0-dev.20260521.2).
Verify its SHA-256 before extracting it outside the repository:

| Archive | SHA-256 |
| --- | --- |
| `test-proxy-standalone-osx-arm64.zip` | `ec33686e46b9ac19c289c36a84150be5e6ac16f539018e5ad8a55367987ca8cd` |
| `test-proxy-standalone-osx-x64.zip` | `ed5c4bdffccf38726e1eedaba14bc513cae1e5e1c9525a6d25086a37921572f9` |
| `test-proxy-standalone-linux-arm64.tar.gz` | `edaf248684072b7d84c426ea5b6d20a3df96aa649280fc31452c1c2bb7effb3b` |
| `test-proxy-standalone-linux-x64.tar.gz` | `ea0aa71e50fc279e511ba245d3457a9e9c8b5cce60bd58080b3e6ff48bb4cbcd` |

From the Steward repository root:

```sh
STEWARD_AZURE_TEST_PROXY=/absolute/path/to/Azure.Sdk.Tools.TestProxy \
  go test ./providers/azure -run '^TestDataProtectionVaultIndependentProxy$' -count=1 -v

STEWARD_AZURE_TEST_PROXY=/absolute/path/to/Azure.Sdk.Tools.TestProxy \
  go test -race ./providers/azure -run 'TestDataProtection' -count=1
```

The executable must report version `20260521.2`. Without the environment
variable the integration test skips; a normal green Go test run alone is not
proof that independent playback ran. The helper owns its process, temporary
recording and dynamic loopback port, and stops the playback session before
terminating the process. It neither installs a global tool nor records traffic.

## What this checks

- The independent matcher rejects a changed host, API version, subscription or
  removed signing parameters without consuming the correct recorded response.
- Native DELETE occurs once, with the expected request ID and no invented ETag.
- JSON-persisted receipts survive a newly constructed runtime before every poll;
  resumed Execute calls do not repeat the deletion.
- Recorded operation completion cannot close a still-existing synthetic vault.
  Subsequent absence does not claim that retained backup data was purged.
- Production origin validation still rejects a foreign URL before transport,
  and application diagnostics do not expose the recorded signing values.

Default Test Proxy sanitizers are disabled **only for this already-public,
sanitized fixture** so host and subscription changes cannot be hidden by
substitution. Do not reuse this configuration for live recording. The adapter
removes even the fake OAuth token before contacting loopback. Server mismatch
logs are discarded because they may include signed queries; method, URL and
body matching remain enabled. Only standard client headers absent from the
extracted upstream recording are excluded from matching.

Official protocol and process documentation:
[Azure SDK Test Proxy README](https://github.com/Azure/azure-sdk-tools/blob/aec3143097825ccc8c054bc4a287471f94b63d26/tools/test-proxy/Azure.Sdk.Tools.TestProxy/README.md).
