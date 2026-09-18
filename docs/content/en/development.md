---
title: "Developer guide"
description: "Build Steward from source and develop the frontend and API locally."
navTitle: "Source and local development"
---

## Prerequisites

Requires Git, Go 1.26+, Node.js 22+, npm, make, and a C compiler. Use Xcode Command Line Tools on macOS, GCC on Linux, or MinGW-w64 and Git Bash on Windows.

<span id="source"></span>

## Build from source

```sh
git clone https://github.com/loomx-ai/steward.git
cd steward
make install
make build
./bin/steward --version
```

`make build` embeds the web console in the executable. Plain `go install` does not include the web console.

<span id="development"></span>

## Local development

```sh
make dev
```

Open [http://127.0.0.1:5858](http://127.0.0.1:5858). The frontend supports hot reload and its proxy generates an API token automatically. Do not run `make run` at the same time: both use the same API port.

## Run checks

```sh
make test
make lint
```

## Verify against a mock server

`make test` never contacts a cloud or a mock server. Provider behavior that
needs a server is covered by additional tests that skip unless you point an
environment variable at one you started yourself. They are not part of CI: run
them locally when you change that provider's behavior.

| Server | Purpose | Variable |
| --- | --- | --- |
| [Moto](https://docs.getmoto.org/) | AWS EC2, OpenSearch, FSx, Route 53 domains, Organizations, KMS, Backup, S3 and the full cleanup pipeline | `STEWARD_AWS_MOTO_URL` |
| [Config Connector mockgcp](https://github.com/GoogleCloudPlatform/k8s-config-connector/tree/master/mockgcp) | Google Compute, Monitoring, Logging, Billing, Cloud Identity and Infrastructure Manager | `STEWARD_*_MOCKGCP_URL` |
| [fake-gcs-server](https://github.com/fsouza/fake-gcs-server) | Cloud Storage buckets | `STEWARD_GCS_EMULATOR_URL` |
| [bttest](https://pkg.go.dev/cloud.google.com/go/bigtable/bttest) | Bigtable tables | `STEWARD_BIGTABLE_EMULATOR_URL` |
| [Azurite](https://learn.microsoft.com/en-us/azure/storage/common/storage-use-azurite) | Azure Blob containers | `STEWARD_AZURITE_BLOB_URL` |
| [azure-apim-emulator](https://github.com/calvinchengx/azure-apim-emulator) | Azure API Management | `STEWARD_APIM_EMULATOR_URL` |
| [Azure SDK Test Proxy](https://github.com/Azure/azure-sdk-tools/tree/main/tools/test-proxy) | Azure recording playback | `STEWARD_AZURE_TEST_PROXY` |

Each fixture README under `providers/*/fixtures/` pins the exact upstream
version and documents how to start that server and which tests to run, for
example:

```sh
STEWARD_GCS_EMULATOR_URL=http://127.0.0.1:4443 \
  go test ./providers/gcp -run Emulator -count=1 -v
```

Run every server on loopback, without cloud credentials. Mock servers do not
reproduce a cloud exactly: treat a passing run as evidence for that native path,
not as cloud acceptance.
