# Data Protection native inventory and policy deletion evidence

The unmodified Microsoft 2026-03-01 examples and their pinned URLs/SHA-256 values
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

Vault actions, coordinated workload dependencies, retained recovery and
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
