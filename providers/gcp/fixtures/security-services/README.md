# Security Command Center service inventory

The native `securitycentermanagement.googleapis.com/SecurityCenterService` rule
reads project-visible service locations, paginated service lists and each listed
service's own GET. It preserves intended/effective enablement, module settings and
update time. Module omission denotes inherited configuration, not a disabled
module. The state shown in inventory is the effective service state, including
`INGEST_ONLY`; no subscription tier is inferred from it.

The source is Google Cloud SDK 583.0.0 core build 20260831161632, shared with the
already pinned Network Services source. [Provenance](provenance.json) records the
archive and both unchanged member checksums. The existing AST converter reads
`ApiMethodInfo` and message declarations without executing SDK code. Anonymous
service Discovery returned 403 and the central Discovery endpoint returned 404
when checked; the retained metadata explicitly uses `google-cloud-sdk` provenance.
All older catalog operations/documents remain unchanged.

The connection still authorizes one project. Its effective settings include
inherited organization/folder policy, but their separate ancestor resources and
cluster-specific settings are not registered by this rule. The service API has
GET/LIST/PATCH and no resource DELETE. This inventory implements the read-only
security-service-state baseline; it neither disables protection nor changes a
subscription. Billing tier, trial and expiry information remain separate parity
work. Unstructured serviceConfig is redacted before inventory, Invoke and logs.

`security_services_test.go` covers two service pages in both global/EU locations,
project-number canonicalization, complete detail state, no eligible-module filter,
no deletion, invalid identities/settings, denied/missing reads, partial results,
page cycles and a changed location set. The SQLite scan-worker test retains the
last complete service and searchable state after detail failures. All service
responses are synthetic. The official Google mockgcp tree at
`673a61419de1b8e4f7d26070ce20dde2daa61da8` contains no Security Center Management
implementation; no independent emulator or live-cloud execution is claimed.

Reproduce from the repository root:

```sh
go generate ./providers/gcp
PYTHONDONTWRITEBYTECODE=1 python3 -m unittest discover -s scripts -p 'test_google_sdk_metadata.py'
go test ./providers/gcp -run 'SecurityServices|CatalogReproducibleAndSpecsExecutable' -count=1
go test -race ./providers/gcp -run SecurityServices -count=1
```

Official contracts:
- [Service settings](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/organizations.locations.securityCenterServices)
- [Project service list](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/projects.locations.securityCenterServices/list)
- [Project service GET](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/projects.locations.securityCenterServices/get)
- [Project-visible locations](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/projects.locations/list)
