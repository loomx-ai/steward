# AWS API metadata

`source/selection.json` is the reviewed selection: official Smithy model files
(pinned `aws/api-models-aws` commit) with the operations Steward calls, the
CloudFormation resource schema archive, and every resource type with its class,
display name and scope.

`source/smithy.json` holds the selected service, operation and top-level
input/output shapes plus each model's URL and SHA-256. The importer derives call
metadata (SDK ID, API version, protocol, endpoint prefix, HTTP binding,
idempotency token and pagination) from the service and operation traits.
`source/cloudformation.json` holds, for each Cloud Control type, the schema
member digest, primary identifier, handler permissions and list handler inputs,
tagging and top-level properties, together with the archive SHA-256.

Refresh from the repository root:

```sh
python3 scripts/sync-aws-catalog.py
go generate ./providers/aws
go test ./providers/aws ./internal/provider/catalog ./internal/provider/spec
```

Builds, generation and tests are offline. Review source and generated diffs
together after a refresh.
