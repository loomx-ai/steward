# Discovery Engine protocol fixtures

`resources.json` is synthetic. It contains 21 resources across `global`, `us`
and `eu`: collections, same-name data stores/apps in different locations,
schemas, controls, serving configurations, sessions, conversations, assistants,
actual document branches/documents and website configuration/targets. Names,
timestamps, content, prompts, connector parameters and errors are fake.

The two complete official Discovery responses were retrieved on 2026-09-09:

| Source | Revision | Complete-source SHA-256 |
| --- | --- | --- |
| [v1](https://discoveryengine.googleapis.com/$discovery/rest?version=v1) | `20260831` | `a687d0b856610311ed767a4a27de4d3e36b4e99fd9b74a68432b1185564acc2e` |
| [v1alpha](https://discoveryengine.googleapis.com/$discovery/rest?version=v1alpha) | `20260831` | `ad4d7efae56b71bae0604809b37bd666472a2491cc6bdbe71f31bddad79c22fb` |

The source catalog retains 57 unmodified methods and their transitive schemas.
Collection list/get/delete/operation reads and Branch list/get use v1alpha;
the other selected operations use v1. Thirteen resource rules share those
native methods. Branch and SiteSearchEngine have no independent DELETE.
Literal HTTP fixtures decide request paths and response shapes without reading
the generated catalog.

Official contracts checked:

- [Locations](https://docs.cloud.google.com/generative-ai-app-builder/docs/locations)
  require the US/EU API origins for those locations. Native collection discovery
  and branch discovery supply actual parent IDs; no default collection/branch
  is invented when the API returns no resources.
- [Data-store deletion](https://docs.cloud.google.com/generative-ai-app-builder/docs/delete-a-data-store)
  requires all linked apps to be deleted or unlinked and can take days.
  [App deletion](https://docs.cloud.google.com/generative-ai-app-builder/docs/delete-engine)
  preserves its data stores. Collection plans delete reviewed apps and data
  stores first. They never unlink an unselected app.
- [Document schema](https://docs.cloud.google.com/generative-ai-app-builder/docs/reference/rest/v1/projects.locations.collections.dataStores.branches.documents)
  places `schemaId` in the same data store; the resulting dependency participates
  in the shared plan alongside app/data-store and serving-config/control links.
- [Connector reads](https://docs.cloud.google.com/generative-ai-app-builder/docs/reference/rest/v1/projects.locations.collections/getDataConnector)
  supply full connection/entity configuration beyond the collection summary.
  [Sitemap fetch](https://docs.cloud.google.com/generative-ai-app-builder/docs/reference/rest/v1/projects.locations.collections.dataStores.siteSearchEngine.sitemaps/fetch)
  without a matcher returns all sitemap metadata. Both enter ancestor proofs
  before their private content is redacted; sitemaps are not separate assets.
- [Target-site operation reads](https://docs.cloud.google.com/generative-ai-app-builder/docs/reference/rest/v1/projects.locations.collections.dataStores.siteSearchEngine.targetSites.operations/get)
  use `siteSearchEngine/targetSites/operations/{operation}`, without a target-site
  ID before `operations`.
- [Permission index](https://docs.cloud.google.com/iam/docs/roles-permissions/discoveryengine).

Run `go test ./providers/gcp -run '^TestDiscoveryEngine' -count=1`.
Tests use real credential resolution, provider inventory/normalization, lifecycle
contribution and the shared solver. They cover paginated multi-parent inventory,
regional origins, legacy aliases, actual branch IDs, app/data-store ordering,
standalone child deletion, retained linked data stores, native operation errors,
long deletion deadlines, serialized restart, expired operations, delayed members
after parent absence, changed ancestry/configuration, protected or unreviewed
effects, failed/partial reads, corrupt cursors/phases and secret-safe logs.
The SQLite inventory-worker test preserves document observations and ancestor
proofs when collection discovery loses permissions. Regional-picker tests also
verify that US/EU do not produce invalid Compute-style regional requests.

These are protocol and application-component tests. Google's
[emulator command catalog](https://docs.cloud.google.com/sdk/gcloud/reference/beta/emulators)
and the [floci-gcp service list](https://github.com/floci-io/floci-gcp/blob/main/README.md)
were checked on 2026-09-09; neither lists a Discovery Engine emulator. The fixture
does not run a search index, independently emulate the service, or demonstrate
real-cloud acceptance. These APIs offer no atomic configuration/incarnation
condition on deletion. Leaf resources generally lack immutable incarnation IDs;
configuration and ancestor proofs cannot distinguish identical replacements
without a changed creation token. Preview agent/runtime families and every
data-plane record are not separately inventoried. Parent deletion also removes
contained service data beyond the individual resources represented in the plan.
