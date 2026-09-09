# CDN and Front Door protocol evidence

The 42 original GET, LIST and DELETE examples come from Microsoft's immutable
`e45039baa985c442877529906e705982a6e0099d` REST specification commit, under
`specification/cdn/resource-manager/Microsoft.Cdn/Cdn/stable/2025-04-15`.
`sources.json` records each original URL and SHA-256; example bytes are unchanged.
The selected 42 operations retain the native `cdn.json` and `afdx.json` definitions
and their common-types v6 references in the catalog source snapshot. The three
RuleSets operations now use the same pinned commit's stable `2025-12-01/openapi.json`
to read opt-in batch mode. `batch-sources.json` retains three additional unchanged
examples from that version; the earlier examples remain checksum-verified.

`TestCDNNativeExamplesAndSchemas` independently validates the 28 selected GET/LIST
example bodies against their respective native schemas with network loading disabled. Eighteen pass without
changes. Ten contain documented source inconsistencies: optional typed fields
set to null in Origins, AFDOrigins, Routes and Endpoints, and two undeclared
provisioning-state enum values in classic CustomDomains. The test first requires
the original failure, then omits only the named optional fields in an in-memory
copy and validates the remainder. The examples and schema remain unchanged.
The OriginGroups list's example label is also misspelled `OriginsGroups`; the
actual `$ref` determines the file. Example ARM IDs use the non-UUID `subid`, which
must be bound to the fixture subscription for runtime tests.

The fixture scenarios bind the otherwise independent examples into one classic
CDN profile and one Front Door profile. Profile names, subscription/group IDs,
child references and classic deep-created member names are deliberately reconciled
in memory. Rule header/condition values and extra fault scenarios are synthetic.
The original files are not internally consistent multi-resource deployments.

## Recorded Microsoft CLI responses

`cli-recordings.json` retains 17 selected responses from three recordings in
`Azure/azure-cli-extensions` commit
`5813689875f128709e8db10903e893138a064236`:

- `test_afd_profile_crud`: GET 28, LIST 7, DELETE 35, pending poll 36, successful
  poll 69, and operation-result Location 404 at 70.
- `test_rule_set_crud`: parent GET 6, GET 12, LIST 11, DELETE 13, pending poll 14,
  successful poll 16, and resource GET 404 at 17.
- `test_afd_secret_latest_version_crud`: parent GET 5, GET 8, HTTP 200 DELETE 9,
  and empty LIST 10.

Response bodies and status codes are unchanged. Only selected response headers
are retained. Signed `t`, `c`, `s`, `h` URL values become explicit `replay-*`
placeholders; original paths and API versions remain. No signed credential from
the recordings is committed. Replay binds the zero subscription ID to the test
connection. Recorded resource requests use `2026-04-01-preview`; tests issue the
selected stable `2025-04-15` resource API (`2025-12-01` for RuleSets) while honoring the version returned in
operation URLs. Resource-group profile LIST bodies are replayed through native
subscription LIST; parent and empty child/protection collections are synthetic.
The secret's positive inventory list is synthesized from its recorded GET.

The recorded rule set has `batchMode: true`. Its native LIST omits `rules`, and
its detail GET contains the complete named rule array. The replay verifies that
inventory follows the detail response and does not invent independent rule IDs,
rule collection reads or individual DELETEs for those embedded properties.
Native deletion and final 404 cover the batch rule set. Additional origin-group
references, retention, malformed arrays, configuration/secret drift and a stale
classic rule whose parent was recreated in batch mode are synthetic tests.

Microsoft's pending and successful CDN polls both contain
`error: {code: "None", message: null}`. Runtime accepts exactly that placeholder
only for CDN `InProgress`/`Succeeded` responses. Failed status, non-null messages,
additional error details and incomplete HTTP responses remain errors.
Signed operation receipts bind the target resource and survive JSON persistence.
A successful/expired operation still requires the native target and every reviewed
cascade impact to be absent. Rule-set recording includes native resource 404;
profile and secret target absence, altered failures, header fallback modes and
surviving descendants are injected protocol scenarios, not recorded cloud results.

To reproduce with the downloaded, SHA-verified original YAMLs (requires PyYAML):

```sh
python3 providers/azure/fixtures/cdn/reproduce_recordings.py /path/to/recordings
```

## Lifecycle and verification boundaries

The [native profile DELETE contract](https://learn.microsoft.com/en-us/rest/api/cdn/profiles/delete?view=rest-cdn-2025-04-15)
removes all its subresources. Classic endpoints contain origins, origin groups
and domains. Front Door endpoints contain routes; origin groups contain origins;
classic-mode rule sets contain independently managed rules. Batch-mode rule sets
hold their named rules as one atomic configuration, as documented in Microsoft's
[batch rule guide](https://learn.microsoft.com/en-us/azure/frontdoor/rule-set-batch).
Embedded rules are retained or deleted with the entire rule set. Their origin-group
overrides require deleting the referring batch rule set first, including any route
prerequisites. A missing batch detail array fails discovery; an explicit empty
array is valid. Classic individual rules additionally bind the live parent rule-set
configuration; prior inventories without that binding require a rescan.
Full native child lists, detail reads, repeat reads,
profile SKU/configuration and immutable creation fields where returned bind each
reviewed cascade. Classic endpoint embedded collections must agree with lists.
Retention, missing inventory, changing relationships/configuration, management
locks and protected/managed resources block cleanup. Profile migration states
also block deletion. Nested cursors include the profile configuration, even when
the immediate endpoint does not change.

Routes, rule overrides, domain certificate references and security-policy
associations establish shared native deletion prerequisites. These do not claim
ownership of their targets. The planner can satisfy a dependency inside an
explicitly named common native cascade only when both resources are already
reviewed deletes of that same controller. Otherwise each prerequisite has its
own ordered action and frozen readback. See Microsoft's
[rule-set disassociation requirements](https://learn.microsoft.com/en-us/azure/frontdoor/standard-premium/how-to-configure-rule-set).
A classic endpoint's active default/override origin-group reference blocks
independent group deletion; endpoint/profile cleanup reviews its owned group.
Steward does not rewrite surviving endpoint routing to force deletion.

External origins, DNS zones, Key Vault secrets/certificates, identities and WAF
policies remain independent. WAF policy resource lifecycles and external DNS
record unlinking are not implemented by this resource family. Native service
rejections, including external associations, remain errors. Rule values and
validation/signing secrets are removed from inventory and logs; keyed configuration
digests still detect returned sensitive changes. Credential rotation requires a
rescan. APIs without immutable creation fields cannot distinguish an identical
recreation; the native DELETE APIs have no atomic If-Match contract.

These are retained protocol, native-schema and recorded-response tests. No
independent CDN/Front Door ARM lifecycle emulator was established and no live
Azure resources were created or deleted. Microsoft's
[Dev Proxy mocks](https://learn.microsoft.com/en-us/microsoft-cloud/dev/dev-proxy/how-to/simulate-mock-responses-dev-tunnel)
and [API Management mock-response tutorial](https://learn.microsoft.com/en-us/azure/api-management/mock-api-responses)
simulate configured responses, not CDN control-plane lifecycle semantics.
