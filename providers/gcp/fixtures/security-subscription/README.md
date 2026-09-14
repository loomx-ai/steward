# Security Command Center organization subscription

The fixture uses the native `securitycenter.organizations.getSubscription` GET
singleton from [Google Discovery v1beta2](https://securitycenter.googleapis.com/$discovery/rest?version=v1beta2).
`provenance.json` preserves its unchanged method, transitive Subscription/Details
schemas, complete response SHA-256 and revision `20260828`. The catalog refresh
selection uses the same URL. Normal tests and generation are offline.

The [native contract](https://docs.cloud.google.com/security-command-center/docs/reference/rest/v1beta2/organizations/getSubscription)
distinguishes current feature tier from the latest subscription details. Missing
details mean no subscription history; retained dates may refer to an ended
subscription. Neither a trial type nor an end date independently determines
current entitlement. The dated Discovery also includes SUBSCRIPTION, SUB_FIXED
and SUB_BASE_OVERAGE beyond the public reference's listed billing types. Runtime
strings remain forward compatible, while invalid field shapes fail the read.

`security_subscription_test.go` validates success payloads against the native
schema and exercises current/historical trial, missing history, pay-as-you-go,
Enterprise and future enums. Requests must use the exact native host/path, GET,
empty query and selected credentials. Resource IDs and subscription field shapes
are validated; request IDs survive. Foreign or malformed IDs, unknown parameters,
403/404, changing identity, failed ancestor visibility and project moves are
covered through inventory and/or Invoke. Both paths recheck ancestry after GET.
There is no subscription deletion method or cleanup action.

`security_subscription_inventory_worker_test.go` runs the real scan creator,
worker and SQLite store. It checks global kind-specific routing, native fields and
search aliases, failed coverage, and preservation of the previous searchable
subscription after 403/404, changed identity and a successful scan after the
project loses its organization. The ancestry source is non-authoritative:
visibility or project membership changes cannot prove subscription deletion.

The pinned Google mockgcp tree at
`673a61419de1b8e4f7d26070ce20dde2daa61da8` has a Security Center v1 mute-config
implementation and no v1beta2 subscription service. The complete, non-truncated
mockgcp subtree was audited for the preceding security-services milestone. These
are protocol and application integration tests, not independent emulator or real
cloud acceptance. No temporary server or new dependency is required.

The older Go Settings v1beta1 client exposes billing fields at
`/settings/v1beta1/{name}` but is not the source of this implementation. The native
v1beta2 subscription contract is directly available through Discovery and covers
additional current tier/type values. Project-level billing and regional endpoint
behavior remain separate work; organization billing is never copied onto projects.
