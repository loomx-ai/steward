# Official Cloud SDK metadata

These four generated Python files are unchanged source members from Google Cloud
CLI 583.0.0 core, build 20260831161632. `LICENSE` is copied from the same archive.

- Official archive: https://dl.google.com/dl/cloudsdk/channels/rapid/components/google-cloud-sdk-core-20260831161632.tar.gz
- SHA-256: `cecc5d244c10e3bc8ef7449937569c40339b90686b902fd48b4afc6f47ebfeb3`
- Network Services member prefix: `lib/googlecloudsdk/generated_clients/apis/networkservices/v1/`
- Security Center Management member prefix: `lib/googlecloudsdk/generated_clients/apis/securitycentermanagement/v1/`
- Official release manifest: https://dl.google.com/dl/cloudsdk/channels/rapid/components-2.json

Media CDN and Cloud Multicast have published REST methods omitted from anonymous
Discovery responses. `scripts/google_sdk_metadata.py` statically reads the SDK's
`ApiMethodInfo` and message declarations without executing SDK code. It preserves
method IDs, relative/flat paths, query/body fields, enums, repeated fields, maps
and transitive messages in Discovery's JSON vocabulary. Expanded name patterns
are derived from the SDK's flat path. The resulting operations carry
`source_format: google-cloud-sdk` and the pinned archive source URI. They are
SDK-derived metadata, not unchanged Discovery fragments.

`python3 scripts/sync-google-catalog.py` verifies the pinned archive checksum,
refreshes these source members and regenerates the selected metadata. Upgrading
the SDK requires reviewing the archive URI/checksum and resulting changes in
`../selection.json`. Offline Python tests exercise the conversion and compare it
to the checked-in catalog source; Go protocol tests independently assert published
Media CDN and Multicast wire paths and deletion readback.

Published REST references:
- https://docs.cloud.google.com/media-cdn/docs/reference/rest/v1/projects.locations.edgeCacheServices
- https://docs.cloud.google.com/vpc/docs/multicast/reference/rest/v1/projects.locations.multicastDomains
- https://docs.cloud.google.com/vpc/docs/multicast/delete-resources

Security Center Management v1 GET/LIST metadata uses the same verified archive
and static converter. Its service Discovery endpoint returned 403 during this
review. [Service inventory evidence](../../../fixtures/security-services/README.md)
records the native read-only scope and independent verification limits.

Project `getBillingMetadata` also uses these unchanged Security Center Management
sources. [Project billing evidence](../../../fixtures/security-billing/README.md)
covers the native singleton GET, explicit tier semantics and location paging.
