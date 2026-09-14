# Security Command Center service inventory

The native `securitycentermanagement.googleapis.com/SecurityCenterService` rule
reads project-visible service locations, paginated project/ancestor service lists and each listed
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

The connection authorizes one project's context. The rule follows that project's
verified Resource Manager parent chain and reads ancestor folder/organization
settings separately, retaining `configurationParent` and omitting project ownership
fields on ancestor records. Four native SDK GET/LIST methods implement these reads.
No folder/organization locations LIST exists in the pinned SDK, so ancestor reads
use project-visible locations (or the explicitly selected global scope). This does
not enumerate every private location or other projects. Cluster-specific settings
remain outside this rule. The service API has
GET/LIST/PATCH and no resource DELETE. This inventory implements the read-only
security-service-state baseline; it neither disables protection nor changes a
subscription. [Organization subscriptions](../security-subscription/README.md) and
[explicit project billing](../security-billing/README.md) are separate inventory
records. Unstructured serviceConfig is redacted before inventory, Invoke and logs.

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

## Visibility and scan authority

The project locations API describes locations visible to the project, including
private locations. A later successful list may stop exposing a prior location;
this is not evidence that its security settings were deleted. Service settings
have GET/LIST/PATCH, with no independent DELETE operation. Steward therefore uses
the dedicated `security-services` source, which is kind-specific and
non-authoritative. Empty location or service lists retain the previous observation;
a later native GET updates it. An observation that was not read keeps its old
last-seen time and is not presented as newly observed.

The real SQLite regression first failed against the earlier authoritative source:
a successful empty location list removed the previously searchable service. It
now covers hidden locations, empty service lists, later effective-state updates,
403/404, identity changes, and legacy authoritative jobs. A separate real scan
creator test checks persisted global/regional shards use the new source without
closure authority. Old `product-api` service jobs fail safely and require a fresh
scan; they cannot close earlier observations. Network routing and other resource
kinds are rejected at this source boundary. Cloud Asset Inventory still cannot
overwrite the native service records.

These tests exercise preservation of historical observations; they do not establish
that every temporarily invisible service still exists. No synthetic deletion or
protection-disable call is introduced. The catalog's 778 native operations and all
source metadata remain unchanged by the authority correction.

## Ancestor read boundary

The complete project/folder/organization identity chain is included in page cursors
and re-read after every page, including empty results. A move, replacement or denied
ancestor read rejects the page; no foreign ancestor can be selected through Invoke.
All ancestors use the existing non-authoritative source, so lost ancestry or location
visibility cannot close historical observations. Native GET/LIST do not grant PATCH
or DELETE authority. Settings and private configuration retain the same redaction.

Retained tests cover both ancestor kinds, global/EU fanout, service pagination,
identity and location mismatches, partial/malformed lists, 403/404, movement during
and between pages, Invoke scope/response validation and original SDK conversion.
The real SQLite scan-worker regression now runs for project, folder and organization
records, including inaccessible and no-longer-visible ancestors and later updates.
These remain protocol/application evidence; the pinned mockgcp has no such service.

- [Folder service list](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/folders.locations.securityCenterServices/list)
- [Organization service list](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/organizations.locations.securityCenterServices/list)
