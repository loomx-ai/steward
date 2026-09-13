# Azure Elastic SAN native contract evidence

This directory supports registered Elastic SAN inventory for SANs, volume groups,
volumes, snapshots, private endpoint connections and retained resources.
Verified snapshot deletion is registered. Volume, group, SAN and private-endpoint
cleanup remain pending; this family does not yet close the parity gap.

## Pinned REST source

`sources.json` records 34 unchanged maximum/minimum examples for 17 native
GET/list/DELETE operations from Azure REST API specifications commit
`c20bf553ad64f20c6d5e3f56080380c086cb1fde`:

`specification/elasticsan/resource-manager/Microsoft.ElasticSan/ElasticSan/preview/2026-04-01-preview/elasticsan.json`

The catalog retains selected operation fragments, native schemas and transitive
references. Tests bind each example, preserve all 54 responses, and validate all
24 response bodies using an offline Draft 4 schema compiler. This validates
schema shape; randomly generated example IDs, types and names are not usable
ARM inventory identities or live-cloud evidence.

Two discrepancies matter for implementation:

- `Volumes_Get_MaximumSet_Gen.json` includes
  `x-ms-access-soft-deleted-resources`, but the GET operation does not declare
  that header. The test rejects the original binding and removes the field only
  from an in-memory request copy to test the declared contract. The original
  bytes are unchanged. The retained native CLI GET also has no such header.
- Both volume DELETE examples explicitly pass `deleteType: permanent`, including
  the example called `MinimumSet`. This is not a default. The contract tests
  separately omit it and verify that binding adds no permanent-delete query or
  force/snapshot-delete headers.

All ten original DELETE callback examples use the external placeholder
`https://contoso.com/operationstatus`. Transport validation rejects it. None is
used as a real Azure operation endpoint. Every selected native DELETE declares
`final-state-via: location` and 200/202/204 responses. Cleanup still needs persisted
receipts, scoped callback validation, restart recovery and independent absence
checks; schema examples do not implement or verify those behaviors.

## Active and retained resources

The preview volume-group schema includes `deleteRetentionPolicy.policyState` and
`retentionPeriodDays`. Volume-group and volume LIST operations accept
`x-ms-access-soft-deleted-resources`: true returns only retained resources; false
or omission returns only active resources. This is not an include-all flag.
Complete discovery must handle both collections and cannot infer permanent
absence from an empty active collection or an unsupported GET header.

`cli-soft-delete-sources.json` pins ten original response bodies from Microsoft's
CLI extension recording at commit `2aa1d8fc6417d0d5055acd88e9491e29734ad62b`.
It records the source file SHA-256, interaction index, request method/path,
API version, selector, status and each extracted body's SHA-256. Body bytes
are unchanged. No signed polling URLs or query signing values are extracted.
The recording uses `2024-07-01-preview`; replay through current catalog bindings
tests compatibility with these response shapes, not the live 2026 API.

The recorded transitions establish separate active and retained populations,
restoration, and removal from the retained index after permanent deletion.
Interaction 39 returns a deleted volume with a `-1751081600` native name/ID
suffix while retaining its original `volumeId`. IDs must be preserved exactly;
the active ID is not an alias for that retained ID. There is no retained GET
in this recording, so it does not establish GET behavior for retained resources.

The native read/index helpers now cover five resource kinds. They validate
subscription-bound identities and typed operational metadata, preserve the
active/retained selector on every page, and reject changed collection/version,
filters, repeated cursors, duplicate records and incomplete responses. A missing
parent collection propagates as an error. GET has no retained selector and
preserves 404 as an error. The registered `elastic-san` source composes two
complete native snapshots, recovers known IDs using GET, retains valid selected
soft-delete index records when GET returns 404, and detects duplicate populations.
Known absence requires both completed population scans and the known ID's own
404. A missing parent collection is incomplete, not an empty retained index.

Inventory records bind native identity, connection, private configuration hash,
region, ancestry, retention and reference/network evidence. Signed history can
preserve known child region/network context when a parent has disappeared but
its child indexes remain readable. Unverified orphans fail closed. A cursor binds
the request, bundle revision and full native snapshot, including parent context.

Parent, subnet, source volume, managed controller, assigned identity and private
endpoint references contribute ordinary graph edges. Key Vault data-plane hosts
are not converted into guessed ARM IDs. Private key/target configuration stays
out of public inventory and API logs. SQLite tests reopen observations with a
fresh runtime, preserve live resources omitted from indexes, reconcile retained
absence, and preserve observations after denied reads. Network closure, restored
volume IDs, forged history and changing snapshots are also exercised. Snapshots
with verified creation identity support deletion; the other four kinds remain
read-only while their cleanup lifecycle is implemented and verified.

## Snapshot deletion and Location polling

`cli-snapshot-sources.json` pins six original response bodies from Microsoft's
snapshot scenario recording at commit `2aa1d8fc6417d0d5055acd88e9491e29734ad62b`.
That recording uses the stable `2025-09-01` API, independently of the earlier
soft-delete preview recording. It shows native GET/list, a DELETE 202 containing
the same snapshot identity and creation timestamp, a pending empty 202 callback,
an empty 200 callback, and a subsequent empty snapshot list. Empty `.body` files
preserve the original zero-byte responses. Callback metadata retains paths,
query key names, version, monitor mode and hashes of the original signed URLs;
signature values are not copied into fixtures. Replay changes only the composed
request version/signature values, not the original bodies or source metadata.

DELETE accepts reviewed 200/202/204 responses. Optional resource bodies must name
the selected resource. A 202 requires Location; regional asyncoperations URLs
must match subscription, provider, planned region, UUID, API version and
`monitor=true`. Signed query values may rotate within the same operation.
Unreviewed async headers, redirects, malformed responses, forged receipts and
scope changes fail closed. Terminal 204 diagnostic URLs are neither persisted
nor followed. Transport completion alone never establishes resource absence.

Snapshot cleanup binds native creation/configuration and the inventory proof,
checks protected tags, resource-group protection, locks and parent regions, and
never submits volume force/snapshot-delete/permanent flags. Existing deletion or
missing parents cause observation without a new DELETE. SQLite restart tests
preserve the original opaque operation ID and deletion-check deadline, avoid a
second DELETE, and keep source volumes/parents open. An expired callback can be
superseded only by the snapshot's independent own GET absence; a live or
same-name replacement snapshot cannot be closed. No live service or independent
ARM emulator was run.

The stable `2025-09-01` contract omits these preview options. This is why the
selected lifecycle work uses the preview contract. Preview features are not
assumed to be newly introduced in 2026: the older preview CLI below already
supports them. No feature fallback may silently hide retained resources.

Native volume DELETE has two separate optional string headers:
`x-ms-force-delete` permits deletion with active sessions, and
`x-ms-delete-snapshots` includes snapshots. They default to false. The optional
`deleteType=permanent` query addresses a soft-deleted volume. Runtime binding
requires exact string switch values and rejects booleans, mixed case, whitespace,
unknown values and attempts to inject headers. Generic case-insensitive enum
matching is not used for these lifecycle switches.

[Microsoft's deletion guide](https://learn.microsoft.com/en-us/azure/storage/elastic-san/elastic-san-delete)
requires clients to disconnect before deletion and explains that deleting a SAN
or volume group removes corresponding children. The
[snapshot guide](https://learn.microsoft.com/en-us/azure/storage/elastic-san/elastic-san-snapshots)
explains that snapshots belong to the volume's lifetime; exported managed disk
snapshots are separate resources. Private endpoint effects, client disconnection,
retention and managed volume ownership require explicit implementation and tests.

## Original CLI operations with stubs

`cli-source.json` pins Microsoft's `elastic-san` 1.3.1b1 wheel through a
checksum-pinned CLI extension index. Four exact nested classes are retained,
with wheel/member/fragment hashes and source line ranges. `MICROSOFT-LICENSE.txt`
is the license from the same pinned repository revision; the wheel itself does
not include a license file.

The classes use `2024-07-01-preview`, not the selected REST catalog version. The
checks execute their original request properties and DELETE response dispatch
using small AAZ transport/serialization stubs. They establish:

- Separate active and soft-deleted LIST headers.
- No soft-deleted selector on native GET.
- Explicit force, snapshot-delete and permanent-delete options.
- Native 200/202/204 acceptance and `Location` poller configuration.

The GET response schema builders remain intact in the fragments but are not
executed. These checks do not execute the complete Azure CLI, AAZ poller,
controller, iSCSI client or real cloud. No independent Elastic SAN ARM emulator
was used. The Azure-native request/response examples are protocol evidence.

Run the offline checks from the repository root:

```sh
go test ./providers/azure -run '^TestElasticSan'
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p 'test_elastic_san_cli.py'
```

To reproduce a class, download the wheel URL from `cli-source.json`, verify its
SHA-256 against the pinned index, extract the recorded ZIP member and select its
inclusive `start_line`–`end_line` range without changing whitespace. Verify both
the member and fragment hashes. Never execute a newly downloaded package as a
substitute for checking its provenance.
