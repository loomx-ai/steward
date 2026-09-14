# Security Center Management project billing

The native singleton method is
[projects.locations.getBillingMetadata](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/projects.locations/getBillingMetadata):
`GET /v1/{name=projects/*/locations/*/billingMetadata}` with no body or query.
It returns the billing tier explicitly set on the named project/location. This
is not effective inherited entitlement, onboarding state, trial status, or
subscription history; those values cannot be derived from this response.

The unchanged official Cloud SDK client/message files and archive/member SHA-256
values are retained in [security-services provenance](../security-services/provenance.json).
The existing static AST converter imports the selected GET and BillingMetadata
schema from that source without executing the SDK. Offline Python checks validate
the exact path/parameter/enum/response declaration and reproduce the stored source.
No new dependency or source archive is needed. Anonymous service Discovery was
unavailable in that source audit; the catalog marks SDK provenance explicitly.

`security_billing_test.go` exercises paginated native locations (global and EU),
project/global/regional scans, exactly one native GET per location, project-number
canonicalization, distinct location identities, preserved request IDs, and native
or future tier strings. The response is already the complete singleton: no fake
LIST or second detail request is made. Malformed fields, changed/foreign identities,
403/404, bad location lists, cursor changes and unexpected response pagination
fail inventory. Invoke validates the same response and selected-project boundary.
There is no cleanup action for this GET-only metadata resource.

`security_billing_inventory_worker_test.go` uses the real inventory worker and
SQLite database. A successful scan produces queryable billingTier properties;
permission failure, missing response and identity mismatch leave the previous
complete tier searchable and record failed coverage. The dedicated
security-billing source is kind-specific and non-authoritative: even a successful
location list that no longer exposes the previously observed location cannot
prove that billing settings were deleted. The SQLite test retains the tier after
that visibility loss as well as failed reads. Authoritative product-source routing
and network scans are rejected for this singleton.

The native metadata only contains name and billingTier. STANDARD, PREMIUM and
ENTERPRISE are preserved separately from organization Subscription records. No
trial dates or effective service state are invented. SDK omission of the tier
remains omission; a future enum is preserved as a string.

The pinned Google mockgcp tree at
`673a61419de1b8e4f7d26070ce20dde2daa61da8` has no SecurityCenterManagement server.
This is protocol/application integration evidence, not independent emulator or
real-cloud verification. No temporary test service was started. Organization
regional billing metadata, project trial history and region-specific real-cloud
behavior remain separate coverage.
