# Recovery Services native evidence

`sources.json` pins the unchanged Microsoft REST examples and their SHA-256
values. The selected schema versions are Recovery Services 2026-07-01 and Backup
2026-08-01. These are separate APIs from Microsoft.DataProtection backup vaults.

`hana-read-recording.json` preserves three complete responses and their request
URLs from the official Azure CLI recordings at commit
`79fa7556f80f3edce83676e10a2d0952776b7022`. Each interaction carries its original
source URL, YAML digest and zero-based interaction index. The recording uses
2023-04-01; it is identity evidence, not a current-version replay or live-cloud
acceptance. No URL, response body or API version has been rewritten.

The schema's BackupProtectedItems_List example omits the backupFabrics segment
from the item ID. The own-GET example and HANA CLI list return complete fabric,
container and item IDs. Readers reject the incomplete example instead of
inventing a fabric. Compound names retain their semicolons.

HANA protection containers and protected databases are distinct resources. The
recorded container is a VMAppContainer; the protected database is an
AzureVmWorkloadSAPHanaDatabase. Matching Alibaba's backup-source lifecycle also
requires container registration/unregistration and consumer checks; protected
item registration alone does not establish equivalent support.

Deleted Recovery Services vaults expose vaultId, vaultDeletionTime and purgeAt.
Their regional deletion identity remains separate from the original vault ID;
these fields must not be interpreted using Data Protection's retention schema.

Current reader tests cover native identity, immutable upstream examples and
recorded identities, full subscription/vault boundaries, pagination filtering,
foreign origins, duplicate members, cycles and asynchronous partial responses.
Registered inventory additionally tests region scoping, persisted SQLite scan and
graph processing, known-ID omission/readback, failed-shard preservation and private
configuration-bound continuations. Containers and items reference their native
parents without authorizing parent deletion. All four resource types currently
remain non-actionable; cleanup and end-to-end acceptance are unfinished.

Recorded item vaultId may be an absolute HTTPS ARM URL. Its host, subscription
and vault path are checked without following it; userinfo, ports, query strings,
fragments and foreign origins are rejected.

`deleted-vault-read-recording.json` preserves the official CLI regional list and
own GET from 2026-05-01 (including original response strings and source hashes).
Some returned purgeAt timestamps precede vaultDeletionTime by fractions of a
second. Both values must be valid, nonzero RFC 3339 timestamps, but their order
is not an API invariant. A past purgeAt is not proof of resource absence; returned
retention objects remain visible until their own GET confirms absence.
