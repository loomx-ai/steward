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
parents without authorizing parent deletion. Empty, registered, unprotected containers now support reviewed unregistration.
Active protected items now support reviewed deletion. Active and deleted vaults
remain non-actionable; overall end-to-end acceptance is unfinished.

Recorded item vaultId may be an absolute HTTPS ARM URL. Its host, subscription
and vault path are checked without following it; userinfo, ports, query strings,
fragments and foreign origins are rejected.

`deleted-vault-read-recording.json` preserves the official CLI regional list and
own GET from 2026-05-01 (including original response strings and source hashes).
Some returned purgeAt timestamps precede vaultDeletionTime by fractions of a
second. Both values must be valid, nonzero RFC 3339 timestamps, but their order
is not an API invariant. A past purgeAt is not proof of resource absence; returned
retention objects remain visible until their own GET confirms absence.

`container-unregister-recording.json` preserves CLI interactions 133–150 from
`test_backup_wl_hana_container.yaml` at the same pinned commit. Original methods,
URLs, response strings and headers are unchanged and were compared with the YAML.
The sequence is DELETE 202, sixteen GET 202 responses with `{}`, then GET 204.
Its Location includes the historical `fabricName=Azure?api-version=2023-04-01`
form, and status headers advertise 2019-05-13-preview. The official CLI's
`track_register_operation` extracts the operation ID and calls the container's
operation-results client. Our adapter first validates origin, subscription,
vault/container/fabric scope and matching case-sensitive operation IDs, then binds
an unsigned legacy result to the selected current API. Only the explicitly
recorded/schema versions (2017-07-01, 2019-05-13-preview, 2023-04-01) are accepted
as legacy metadata. Signed callbacks require the selected version and retain all
signature fields unchanged. Historical signatures are not rewritten.

The protocol test consumes all seventeen unchanged poll responses through that
adapter. Its outgoing URL uses the selected catalog version, so this is historical
protocol adaptation coverage, not independent current-version wire replay or
live-cloud validation. The recorded operation's final 204 alone is not evidence
of own-resource absence. Separate synthetic registered-runtime and SQLite worker
tests require own GET 404, preserve receipts across restart and transient 503
reads, and prevent late active or retained consumers from being hidden.

The two additional unchanged REST examples pin unregister and container result
contracts. The unregister example's request uses resource group `testRg` while
its callback uses `test-rg`; tests reject that scope mismatch. They do not repair
the example. Native DELETE acknowledgements must be bodyless 200, 202 or 204.
A pending result may have an empty decoded payload, including `{}` or null, but
never establishes completion. A native result resource is likewise not an own
read and cannot prove disappearance. No test here authorizes purging backup data,
disabling protection, unregistering an occupied source, or deleting a vault.


Protected-item deletion uses native bodyless DELETE and validates scoped status
and result callbacks. Operation success can carry one or several backup job IDs;
each job must complete before own-item readback can establish the outcome.
CompletedWithWarnings remains subject to own readback. Failure, cancellation,
malformed jobs, foreign identities and unavailable dependencies cannot establish
success. Signed receipts preserve the status-to-job checkpoint across restart.

`hana-item-delete-recording.json` preserves CLI interactions 77–85 from
`test_backup_wl_hana_item.yaml` (2023-04-01). Its unchanged responses exercise the
historical protocol adapter through operation status and backup job completion.
`vm-item-delete-recording.json` preserves interactions 118–127 from
`test_backup_item.yaml` (2025-02-01). The VM list before and after
delete returns the same native ID, with protection stopped, policy links cleared,
and deferred deletion enabled. Tests use this observed transition without
assuming a fixed retention duration or claiming permanent purge. Historical
signed callback URLs are preserved as evidence but are not rewritten or followed
by the current-version client. Current signed callback tests are synthetic.

The unchanged policy and Resource Guard examples establish reviewed dependency
contracts. A documented internal guard proxy ID is bound to its known vault and
proxy name; it is never followed as a URL. List and own guard examples are separate
observations, not a synchronized fixture. Operation-specific delete authorization
remains enforced by Azure. The implementation neither disables guards nor changes
immutability settings, and does not add cross-tenant auxiliary authorization.

HANA database cleanup requires explicit selection and prior cessation of related
instance snapshot protection. Synthetic native-schema fixtures exercise relation
fields, list omissions, ambiguity, graph ordering, and actual SQLite worker
execution with out-of-order job delivery and provider restart. They are not a
recording of a real HANA instance-snapshot deletion. Separate registered-runtime
tests reject policy, source, protection and registration changes before DELETE and
after acknowledgement. SQLite rescan tests rediscover the same retained native ID
as non-actionable after cleanup completes. English and Chinese plan warnings make
the backup-data deletion and retention consequences visible.

Vault deletion, full source-workload graph coverage, policy cleanup, retention
recovery/purge, Site Recovery, independent current-version replay and live-cloud
acceptance remain unfinished. No parity baseline or acceptance gate is closed.


Source graph coverage now reads explicit `sourceResourceId` and `virtualMachineId`
fields from active protected items and protection containers. Fully qualified ARM
URLs are accepted only at the Azure management origin without credentials, ports,
queries, fragments or encoded paths, and are parsed as identities rather than
followed. The two VM identity fields must agree when both are present. Workload
labels never substitute for resource IDs. VM and HANA unchanged recordings cover
these fields; container/storage and malformed-input cases use synthetic fixtures.

Source references establish independent `uses` relationships. Selecting a backup
alone does not select its source; selecting both orders backup deletion before
source deletion. Unscanned and cross-subscription sources remain unresolved without
being fetched. Retained backups omit live-source dependencies only after an own
read matches the reviewed configuration and retention flag. Container/source
changes, duplicate targets and forged retention are rejected.

The SQLite worker test persists an already-discovered synthetic VM, runs native
backup inventory and graph contribution, then invokes both registered deletion
drivers through restart and out-of-order job delivery. Both native DELETEs occur
once, backup first. It validates joint execution, not VM discovery or live Azure.
Non-ARM source coverage and the previously listed family/acceptance gaps remain
open.


`SoftDeletedContainers_List.json` is the unchanged 2026-08-01 REST example for
`DeletedProtectionContainers_List`. It returns the original protection-container
ID with `registrationStatus: SoftDeleted`, rather than a protected-item retention
flag. Source graph tests consume the original example and separately scope-adapt
it for registered inventory; such containers preserve historical provenance
without acquiring live source dependencies. The deleted-container collection is
not yet registered as a separate inventory path in this milestone.
