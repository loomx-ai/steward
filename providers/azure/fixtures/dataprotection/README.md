# Data Protection native inventory and policy deletion evidence

The unmodified Microsoft API examples and their pinned URLs/SHA-256 values
are listed in sources.json. They are API examples, not cloud recordings.

DeletedBackupVaults has a native request path ending in locations/{location}/
deletedVaults/{deletedVaultName}; the official response examples instead use
locations/{location}/deletedBackupVaults/{name} and the response type
Microsoft.DataProtection/deletedBackupVaults. The reader accepts precisely that
response alias while issuing requests only on the declared operation path. It
preserves the subscription, region and deletion name. The original active vault
ID is descriptive retention metadata, not ownership of a currently active vault
with the same name. Multiple deleted identities remain separate.

Protocol tests exercise the registered inventory source, authenticated transport,
pagination, direct read reconciliation, configuration-bound continuation, scope
and parent checks, and payload/log redaction. Same-name recreation does not
merge deleted and active vaults.

The unmodified DeleteBackupPolicy example pins synchronous 200/204 deletion.
Policy tests reject asynchronous or nonempty acknowledgements, persist signed
receipts before follow-up reads, require actual own-read absence and refuse
changed/protected parents or active/retained consumers. A real SQLite inventory,
graph, plan and cleanup worker test recreates the runtime and repository between
retries, preserves the receipt, and verifies that other backups remain untouched.
Permission failures are refused; transient service failures exercise retry recovery.

Coordinated workload dependencies, retained recovery and
independent emulator/live-cloud acceptance remain unfinished. The API has no
If-Match guard or universally available incarnation ID; repeated configuration
checks do not provide atomic protection against concurrent same-ID replacement.


Backup-instance deletion additionally uses the unmodified DeleteBackupInstance,
GetOperationStatusVaultContext and GetOperationResult examples. The DELETE Location
sample refers to a different instance and omits the vault path. The result's 202
sample mixes a different subscription, token and 2021 API version. They are retained
as source evidence, not treated as permissible callbacks for the sample request.

Native callback tests cover six scoped routes, opaque case-sensitive operation IDs,
phase receipts, serialization/restart, permission failures, expired operation URLs,
failed/unknown states and malformed results. Registered action tests cover sync and
async deletion, changed reviews and retained-data reconciliation. A real SQLite
scan/graph/plan/worker test resumes after read failure and keeps other backups active.
Completion means active-instance absence; it never claims permanent backup purge.


Vault operations use 2026-06-01. GetResourceGuardProxy.json retains Microsoft's
`Microsoft.DataProtection/vaults/backupResourceGuardProxies` response type alias;
its resource ID still uses `backupVaults`. Only the exact type alias is accepted.
The ID, subscription, vault and native child name must match. External Resource
Guard IDs are descriptive dependencies and do not authorize following/deleting them.

vault-delete-recording.json extracts interactions 5-8 without rewriting any URL,
response body or selected callback header from the pinned Azure CLI recording.
Its source URL, original YAML SHA-256 and interaction indices are embedded.
The replay verifies exact signed requests, phase persistence across runtime restart,
callback scope/version restrictions and signing-material redaction. The all-zero
subscription is the upstream recording's sanitization, not an application account.
The recording stops at the operation result; own absence and retention behavior
are covered by separate protocol/SQLite tests rather than claimed as recorded facts.
