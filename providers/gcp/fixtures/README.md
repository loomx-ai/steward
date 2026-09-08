# Google Cloud protocol fixtures

These are synthetic resources using the native [Asset](https://docs.cloud.google.com/asset-inventory/docs/reference/rest/v1/Asset)
and [assets.list](https://docs.cloud.google.com/asset-inventory/docs/reference/rest/v1/assets/list)
response schemas, including project-number full names, Compute API selfLinks,
global/zonal locations, snapshot readTime, and pagination. They contain no cloud
credentials or real tenant data. They test the actual OAuth/HTTP/provider boundary
with a controlled transport; they are not evidence of an independent emulator or
real Google Cloud integration run.

`kubernetes-api.json` contains unmodified selected operations and deletion
precondition schemas from Kubernetes v1.35.0's published OpenAPI document. Its
source URL and complete-document SHA-256 are retained in the fixture. The
Kubernetes tests validate the wire deletion body against that schema, use a real
local TLS server and OAuth exchange, and test CA/hostname failures, redirects,
endpoint binding, paging, cancellation, API errors, and secret-safe diagnostics.

The GKE network tests combine that actual HTTPS client with controlled native
Container/Compute responses. They cover Service, Ingress and Gateway finalizers,
NEG ownership, native frontend chains, generated and pre-shared TLS certificates,
IPv6 addresses, zero-node template tags, firewall/route ownership, reserved IPs,
immutable plans, resumed cleanup, failed operations, late protection and final
readback. These tests do not emulate a running GKE controller. The ownership
rules are cross-checked against the pinned ingress-gce source linked from the
catalog README and official GKE documentation.
