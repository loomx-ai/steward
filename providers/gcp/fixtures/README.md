# Google Cloud protocol fixtures

These are synthetic resources using the native [Asset](https://docs.cloud.google.com/asset-inventory/docs/reference/rest/v1/Asset)
and [assets.list](https://docs.cloud.google.com/asset-inventory/docs/reference/rest/v1/assets/list)
response schemas, including project-number full names, Compute API selfLinks,
global/zonal locations, snapshot readTime, and pagination. They contain no cloud
credentials or real tenant data. They test the actual OAuth/HTTP/provider boundary
with a controlled transport; they are not evidence of an independent emulator or
real Google Cloud integration run.
