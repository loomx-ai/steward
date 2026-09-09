# WAF policy protocol evidence

The six unchanged examples in `sources.json` come from Microsoft's immutable
REST specification commit `e45039baa985c442877529906e705982a6e0099d`:

- `Microsoft.Cdn/cdnWebApplicationFirewallPolicies`: stable `2025-12-01`
  `Policies_Get`, resource-group `Policies_List` and `Policies_Delete`.
- `Microsoft.Network/frontDoorWebApplicationFirewallPolicies`: stable
  `2025-11-01` `Policies_Get`, `Policies_ListBySubscription` and `Policies_Delete`.

Native definitions and common-type references are retained in the catalog.
`TestWAFNativeExamplesAndSchemas` checks source fingerprints and independently
validates the four original GET/LIST bodies with offline Draft 4 JSON Schema.
All four originals fail because optional selectors are null; Front Door also
returns null `logScrubbing`. The test asserts these source inconsistencies,
then omits only those optional fields in memory and validates the remainder.
Neither the saved examples nor their schemas are modified.

## Microsoft CLI recording

`cli-recording.json` retains four responses from `test_waf_policy_basic.yaml`
in Azure CLI extensions commit `5813689875f128709e8db10903e893138a064236`.
The source URL and SHA-256 are embedded in the file. Interaction 26 is policy
GET, 27 is the six-policy resource-group LIST, 28 is DELETE 204, and 29 is the
five-policy LIST after deletion. Bodies and status codes are unchanged;
selected request-ID and operation headers are retained. The recording and
implementation both use `2025-11-01`.

Replay binds the all-zero subscription to the test connection. The native RG
LIST body is served by the subscription LIST, which has the same response
schema. Detail responses for the five other policies come from LIST rows;
protection collections and the target's final GET 404 are synthetic. The
recording proves removal from LIST, but does not contain a final resource GET.
Runtime requires that separate native absence read even after DELETE 204.
`x-ms-original-request-ids` is retained when canonical request-ID headers are absent.

Reproduce from the downloaded original YAML with PyYAML installed:

```sh
python3 providers/azure/fixtures/waf/reproduce_recording.py /path/to/test_waf_policy_basic.yaml
```

## Reviewed cleanup

Policy custom rules, managed rules and rate limits are embedded configuration;
they share the policy lifetime. Native policy GET supplies reverse association
indexes. Required indexes must be explicit arrays; Front Door's
`routingRuleLinks` alone is optional in both the schema and recordings.
Malformed or duplicate references cannot establish safe deletion.

Known CDN endpoint and Front Door security-policy references are read natively
and must reciprocally name the policy. They become reviewed prerequisites,
never owned children. A retained prerequisite blocks cleanup. Unsupported
classic Front Door frontend/routing references remain unresolved blockers;
Steward does not rewrite a surviving routing configuration to remove WAF.
Each policy is read again after association discovery. Execution checks that
all live associations are gone and that reviewed prerequisites are absent.

Removing associations changes ETag and reverse indexes. All other returned
policy configuration, location and available creation identity remain bound
to the reviewed inventory. Rule match values and custom response bodies are
removed from inventory and logs but covered by a keyed configuration digest.
Credential rotation requires rescanning. Identical recreation without a returned
immutable identity remains indistinguishable; native policy DELETE has no
atomic If-Match contract.

The [native Front Door DELETE contract](https://learn.microsoft.com/en-us/rest/api/frontdoorservice/webapplicationfirewall/policies/delete?view=rest-frontdoorservice-webapplicationfirewall-2025-11-01)
allows HTTP 200/202/204. Its example's async URL uses a `frontdoors/.../operationResults`
path and API `2022-10-01`; a test preserves that path/version while binding the
example subscription to the connection. Poll bodies, Location completion,
expired operations, failures and altered receipts are injected protocol cases.
Persisted operation receipts bind the target and operation URL; final GET must
prove absence. Additional tests cover both native collection scopes, pagination,
changed RG sets, foreign identities/continuations, partial responses, retention,
locks, configuration/private-value drift and JSON restart.

The scenario joins otherwise independent native examples into synthetic
reciprocal associations, including service-side reverse-index updates after
referrer deletion. These tests are native-schema, recorded-response and HTTP
protocol evidence. No independent WAF ARM lifecycle emulator was established,
and no live Azure resources were created or deleted. See the
[CDN evidence boundaries](../cdn/README.md) for the investigated mock tools.
