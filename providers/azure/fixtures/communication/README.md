# Azure Communication Services evidence

`sources.json` identifies 34 unchanged Microsoft Swagger examples and records
each complete-file SHA-256, immutable URL, source document, method and operation.
The source revision is `c20bf553ad64f20c6d5e3f56080380c086cb1fde`:

- ARM Communication/Email resources: `2026-03-18`.
- Data-plane phone numbers and reservations: `2025-06-01`.
- Data-plane rooms and participants: `2025-03-13`.

`TestCommunicationOfficialContracts` binds all 34 requests and checks 56 example
responses, including 36 bodies, against the retained schemas. Five examples
contain extra parameters absent from their native operations: the phone-number
list's `phoneNumber` and four empty `parameters` objects on SMTP/suppression
reads or deletes. Tests assert those exact discrepancies before binding the
remaining parameters. The suppression-list example uses `nextLink:null` where
the schema says string; the test first asserts that failure, then validates the
remaining response. The checked-in examples remain unchanged.

The room list's illustrative `nextLink:"string"` is not a usable continuation.
The ARM communication example uses a legacy `.comms.azure.net` hostname and
omits creation metadata; it does not establish authority for a public data-plane
request. SMTP examples use the singular `SmtpUsername` ID/type alias; sender
examples can report the domain name in the child's `name` field. Full request
identities and native response differences require explicit adapter validation.

`cli-recordings.json` retains 134 GET/DELETE responses from 12 Microsoft CLI
recordings at revision `d2f60986756c939c3d6d7f85e89798cca158d935`. Every entry
records its original file URL/hash and interaction index, original request URL,
response status, selected native headers and unchanged response-body string.
To reproduce, parse the original YAML `interactions`, select GET/DELETE calls
whose path contains `/providers/Microsoft.Communication/` ignoring case or whose
Communication data-plane URL begins with `/rooms` or `/phoneNumbers`, and copy
those fields in source-file/interaction order. Retained headers cover content
type, ETag, request/correlation IDs, errors, polling locations, operation/release
IDs and Retry-After. Source request credential headers are excluded.

These recordings include six DELETE receipts: Communication/Email/domain 202,
sender 200, SMTP 204 and room 204. ARM polling uses signed subscription-scoped
ProviderHub `locations/WESTUS2/operationStatuses/{uuid}*{digest}` URLs with
`api-version,t,c,s,h` parameters, including 202 progress and 200 completion.
The polling region differs from the resources' `Global` location. Recorded SMTP
and sender `type` fields omit the `Microsoft.Communication/` prefix. Native
data-plane hostnames include the resource and geography before
`.communication.azure.com`. Upstream sanitizers replace some ARM origins with
`sanitized.com`, phone/room identifiers with `sanitized`, and account values;
these placeholders never establish production endpoint or identity authority.
The ARM response test replaces only literal `sanitized` request path segments
with the corresponding response segments before checking the five recorded
resource shapes. All other path segments must still agree. Production identity
validation performs no sanitizer substitution.

Recorded APIs are ARM `2023-04-01`, SMTP `2024-09-01-preview`, Rooms
`2025-03-13` and PhoneNumbers `2025-06-01`. The phone-operation recordings cover
search/purchase, not phone release. They provide response evidence, not proof
of current ARM deletion, native phone-release readback, an independent emulator,
or a live cleanup performed by Steward.

Microsoft documents that [resource deletion releases associated phone numbers
and irreversibly deletes associated data](https://learn.microsoft.com/en-us/azure/communication-services/quickstarts/create-communication-resource).
The [phone-number guidance](https://learn.microsoft.com/en-us/azure/communication-services/quickstarts/telephony/get-phone-number)
also distinguishes release from billing-cycle visibility and re-purchase.

The composed protocol tests exercise separate ARM/Communication OAuth audiences,
owned endpoint resolution, native data URLs, full reservation reads, room roster
pagination, relative release receipts, private snapshots and known-ID inventory
reconciliation. Each inventory scan makes two complete observations; a known
parent's absence never substitutes for its recorded children's own GETs.

The registered lifecycle tests cover ten native resource kinds. SMTP usernames,
reservations, rooms and Email children are direct deletion prerequisites;
Communication account deletion owns the reviewed phone releases. A connected
email domain requires explicit selection of its referring account or a prior
unlink and rescan. Reverse discovery reads this subscription's accounts and
known omitted accounts; it does not prove the absence of cross-subscription
connections.

The native `notificationHubId` is an independent Notification Hubs reference.
Inventory and graph review preserve it, including omitted and foreign targets;
it grants no ownership or automatic deletion. Retargeting invalidates the
account's reviewed configuration. The [native link contract](https://learn.microsoft.com/en-us/rest/api/communication/resourcemanager/communication-services/link-notification-hub?view=rest-communication-resourcemanager-2026-03-18)
describes this association. Notification Hubs are not registered cleanup kinds
in this checkpoint.

`TestCommunicationRecordedARMLongRunningOperations` validates three unchanged
CLI DELETE receipt headers and 45 operation-status responses (29 nonterminal).
The latter include creation and update operations as well as deletion. For the
recorded request URL shape check only, the upstream sanitized origin becomes
`management.azure.com`; native header URLs and response bodies stay unchanged.
The replay validates the recorded `2023-04-01` version explicitly, while current
runtime requests use the pinned `2026-03-18` ARM version. Composed transport
tests cover global receipt version completion, signature rotation, phone release
polling, forged endpoints/receipts, status failure and final named readback.

The real SQLite worker test scans ten global shards, builds the lifecycle graph,
and executes nine native DELETE steps with one account-owned phone impact.
Every retry reopens SQLite and recreates the provider, registry and worker. It
checks durable intent before mutation, signed polling and terminal absence
phases, root disappearance with a remaining phone, a verification clock beyond
32 days, and final closure without repeated DELETEs. A room participant-list
404 cannot establish room absence; surviving grandchildren require their own
reads even after an Email parent disappears. Operation signing queries, SMTP,
recipient, participant and future native settings are excluded from API logs.

The account/phone verification deadline is 40 days, with hourly absence checks
after operation completion. Microsoft documents portal visibility through the
billing cycle, not a guaranteed native GET disappearance deadline. No billing
cessation claim, live phone-release observation, independent Communication
emulator, atomic protection against concurrent external edits, or exactly-once
write across a crash before receipt persistence is claimed.

These ten kinds do not enumerate all account-associated application data, such
as Chat/Identity data or Event Grid filters, which Microsoft's resource-deletion
guidance also covers. Those are not individually reviewed lifecycle impacts in
this checkpoint. Account deletion remains an irreversible deletion of that
associated data; this implementation is not full ACS or SMS-template parity.
