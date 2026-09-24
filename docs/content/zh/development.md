---
title: "开发者指南"
description: "从源码构建 Steward，并在本地开发前端和 API。"
navTitle: "源码与本地开发"
---

本指南面向需要从源码构建或修改 Steward 代码的开发者。如果只是使用 Steward，请直接[安装发行版](./installation.md)。欢迎在 [GitHub](https://github.com/loomx-ai/steward) 上参与贡献。

## 准备环境

需要 Git、Go 1.26+、Node.js 22+、npm、make 和 C 编译器。macOS 使用 Xcode Command Line Tools，Linux 使用 GCC，Windows 使用 MinGW-w64 和 Git Bash。

<span id="source"></span>

## 从源码构建

```sh
git clone https://github.com/loomx-ai/steward.git
cd steward
make install
make build
./bin/steward --version
```

`make build` 将 Web 控制台打包进可执行文件。单独执行 `go install` 不包含 Web 控制台。

<span id="development"></span>

## 本地开发

```sh
make dev
```

打开 [http://127.0.0.1:5858](http://127.0.0.1:5858)。前端支持热更新，开发代理会自动生成 API 令牌，无需手动登录。不要同时运行 `make run`，两者会使用同一个 API 端口。

## 运行检查

```sh
make test
make lint
```

## 使用 mock server 验证

`make test` 不会访问云，也不依赖任何 mock server。需要服务端才能验证的行为由额外的测试覆盖：只有把环境变量指向你自己启动的服务时才会运行，否则自动跳过。它们不在 CI 中，修改对应云厂商行为时请在本地运行。

| 服务 | 覆盖范围 | 环境变量 |
| --- | --- | --- |
| [Moto](https://docs.getmoto.org/) | AWS EC2、OpenSearch、FSx、Route 53 域名、Organizations、KMS、Backup、S3 及完整清理流程 | `STEWARD_AWS_MOTO_URL` |
| [Config Connector mockgcp](https://github.com/GoogleCloudPlatform/k8s-config-connector/tree/master/mockgcp) | Google Compute、Monitoring、Logging、Billing、Cloud Identity 与 Infrastructure Manager | `STEWARD_*_MOCKGCP_URL` |
| [fake-gcs-server](https://github.com/fsouza/fake-gcs-server) | Cloud Storage 存储桶 | `STEWARD_GCS_EMULATOR_URL` |
| [bttest](https://pkg.go.dev/cloud.google.com/go/bigtable/bttest) | Bigtable 表 | `STEWARD_BIGTABLE_EMULATOR_URL` |
| [Azurite](https://learn.microsoft.com/zh-cn/azure/storage/common/storage-use-azurite) | Azure Blob 容器 | `STEWARD_AZURITE_BLOB_URL` |
| [azure-apim-emulator](https://github.com/calvinchengx/azure-apim-emulator) | Azure API 管理 | `STEWARD_APIM_EMULATOR_URL` |
| [Azure SDK Test Proxy](https://github.com/Azure/azure-sdk-tools/tree/main/tools/test-proxy) | Azure 录制回放 | `STEWARD_AZURE_TEST_PROXY` |

`providers/*/fixtures/` 下的每个 README 都固定了上游版本，并说明如何启动该服务、运行哪些测试，例如：

```sh
STEWARD_GCS_EMULATOR_URL=http://127.0.0.1:4443 \
  go test ./providers/gcp -run Emulator -count=1 -v
```

所有服务都在回环地址启动，不使用任何云凭证。mock server 并不能完全复现云上行为：通过只能作为该原生路径的证据，不等于云上验收。
