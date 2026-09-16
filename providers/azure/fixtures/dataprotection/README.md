# Data Protection native inventory evidence

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
merge deleted and active vaults. This milestone exposes inventory only. Actions,
backup workload dependency semantics, retained recovery and full application or
independent emulator/live-cloud acceptance remain unfinished.
