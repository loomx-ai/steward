# App Service protocol evidence

The 15 unchanged examples in `sources.json` come from the immutable Microsoft
REST specification commit `e45039baa985c442877529906e705982a6e0099d`, under
`web/resource-manager/Microsoft.Web/AppService/stable/2025-05-01/openapi.json`.
Its SHA-256 is `92a4b53f4d25cb178439eb8c5f59ccf7dd659c52a05bac50a0bc4e31657da03f`.
The catalog selects 29 operations: five existing WebApps operations and 24
GET/LIST/DELETE operations for slots, production/slot functions, certificates,
production/slot certificate resources and production/slot hostname bindings.
App Service plan APIs retain their existing version.

The examples cover site, slot and all three certificate scopes. Ten original
GET/LIST bodies pass independent offline Draft 4 validation without modification.
The selected function and hostname APIs have no official examples. Four synthetic
detail responses are separately validated against their original native schemas.
Function source/configuration, linked scenario IDs and failure injections are
synthetic, not cloud recordings. Native slot certificate examples use
`Microsoft.Web/sites/certificates` as their type while retaining the full slot ID;
the catalog accepts this explicit response alias.

## Microsoft CLI recordings

`cli-recordings.json` retains 18 response bodies and statuses from Azure CLI
commit `dc50d475a00ded4a1a1980d4a10a9fbd9a750a81`. Each source has its original
URL, SHA-256 and interaction indices. Only selected diagnostic/operation headers
are retained; request bodies are excluded.

- `test_webapp_ssl`: site GET 16/20, certificate LIST 17/83, hostname LIST 18/31,
  certificate DELETE 42/84, slot GET 50/62, slot hostname LIST 60/73 and site
  DELETE 89. Both bound and unbound TLS states are retained. The later
  certificate's native name contains a space; runtime verifies URL encoding.
- `test_functionapp_retain_plan`: GET 16 and DELETE 18 with the native
  `deleteEmptyServerFarm=false` query.
- `test_functionapp_update_slot`: parent GET 20 and slot GET 23/27. The latter
  two differ only in `lastModifiedTimeUtc`, which must not invalidate cleanup.

All these selected requests use `2025-05-01`. Replay binds the zero subscription
to the test connection. Certificate RG LIST bodies replay through subscription
LIST with the same schema. Certificate and hostname detail responses are
synthesized from recorded LIST rows. Empty child/protection collections, a
combined site/slot scenario and all final GET 404 responses are synthetic.
There is no recorded individual function DELETE or final target GET 404.

Native unbound hostname responses omit both `sslState` and `thumbprint`. Site
`hostNameSslStates` still reports explicit Disabled states. Runtime accepts this
specific omission for hostname resources; malformed and contradictory TLS
information cannot establish an unused certificate. Slot hostname responses
also use the application-level type alias with their complete slot IDs.

Reproduce with the downloaded, fingerprint-verified YAMLs and PyYAML:

```sh
python3 providers/azure/fixtures/appservice/reproduce_recordings.py /path/to/recordings
```

## Lifecycle and boundaries

Application and slot cleanup reviews the native function, certificate and
hostname-binding collections, including nested slots. Every retained member
blocks the controller's deletion. Default application/SCM hostnames require
their owning controller; standalone deletion is blocked. Non-Function Apps skip
the function API according to native `kind`, while missing kind cannot prove an
empty collection. Standalone members bind their parent configuration, and slot
members also bind the root application. Native parent-set/configuration changes
invalidate cursors and reviewed deletion. All cascade members must be absent
after the controller's DELETE, even if its own GET already returns 404.

Certificates are also independent native resources. TLS state may supply an
ARM certificate ID, but the recorded active bindings supply only a thumbprint.
Any matching ID or thumbprint blocks certificate deletion. Runtime reads all
native applications, slots and hostname bindings twice before declaring a
certificate unused, then re-reads the certificate. It does not infer ownership
or authorize hostname deletion from a potentially ambiguous thumbprint.
Missing/invalid thumbprints require a new complete observation before cleanup.
Key Vault data, external domains and App Service plans remain separate resources.

Private certificate blobs, function files/configuration/test data, application
commands and domain-verification values are removed from inventory/logs. Link
credentials and query strings are removed from stored function URLs. Keyed
digests preserve sensitive drift detection; credential rotation requires a
rescan. Retained tests cover review/retention, missing or new children, parent
recreation, private drift, locks, pagination, incomplete responses, native delete
errors, JSON restart and operation/final-absence checks. Async polls and errors
are injected protocol cases; the retained native DELETEs return HTTP 200.

The SiteCertificates API incorrectly restricts the parent `name` parameter to
`^[A-z][A-z0-9]*$`. This rejects observed native app names with hyphens and
contradicts Microsoft's [App Service naming rules](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/resource-name-rules#microsoftweb).
The original schema is unchanged. A narrow binding correction applies only to
this exact pattern in the selected 2025-05-01 SiteCertificates operations,
accepting 2–60 character ASCII/Punycode names with internal hyphens and
alphanumeric ends. Regression tests verify the valid/invalid boundary and
ensure the native catalog is not mutated. This is an inference from naming
rules and observed parent names, not a recorded SiteCertificates call.

Function deployment packages can make individual code deletion unavailable;
Azure's rejection is preserved. Steward does not modify deployment packages to
force removal. See [run-from-package behavior](https://learn.microsoft.com/en-us/azure/azure-functions/run-functions-from-deployment-package)
and the native [function DELETE contract](https://learn.microsoft.com/en-us/rest/api/appservice/web-apps/delete-function?view=rest-appservice-2025-05-01).
Identical recreation without returned immutable creation fields remains
indistinguishable, and these DELETE APIs provide no atomic If-Match contract.
This evidence consists of native schemas, recordings and HTTP protocol tests;
no independent App Service ARM lifecycle emulator or live cloud deletion is
claimed. The local Azure Functions host is not an ARM resource-management emulator.
