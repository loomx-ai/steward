<p align="center">
  <img src="web/public/brand/steward-symbol.svg" alt="Steward logo" width="96" height="96">
</p>

<h1 align="center">steward</h1>

<p align="center">
  <em>发现、理解并安全清理云资源。</em>
</p>

<p align="center">
  <a href="https://github.com/loomx-ai/steward/actions/workflows/test.yml"><img src="https://github.com/loomx-ai/steward/actions/workflows/test.yml/badge.svg" alt="测试"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-blue.svg" alt="Apache-2.0 许可证"></a>
</p>

<p align="center">
  <strong>语言：</strong> <a href="README.md">English</a> | 简体中文
</p>

Steward 是一个开源云治理服务，提供资产盘点、拓扑分析、治理发现和受控资源清理。目前支持阿里云、AWS、Google Cloud（GCP）和 Microsoft Azure。

[使用文档](https://loomx.ai/steward/docs) · [快速开始](https://loomx.ai/steward/docs/quick-start) · [清理指南](https://loomx.ai/steward/docs/cleanup)

## 功能

- 使用云厂商原生 API 建立可搜索的资产清单。
- 查看账号、地域、网络和资源关系。
- 基于标准化资源数据生成治理发现。
- 通过依赖排序、安全阻断、幂等调用、结果回读和审计执行清理。

## 快速开始

macOS 或 Linux 使用 Homebrew 安装：

```bash
brew install loomx-ai/tap/steward
steward --version
steward server start
```

也支持[安装脚本、Scoop、deb/rpm 和独立二进制](https://loomx.ai/steward/docs/installation)。[GitHub Releases](https://github.com/loomx-ai/steward/releases) 提供 macOS/Linux amd64、arm64 和 Windows amd64 产物及 SHA-256 校验文件。发布版内含 Web 控制台和数据库迁移脚本，无需 Go 或 Node.js。

打开 <http://127.0.0.1:8585>，直接使用，无需登录。SQLite 数据默认保存在 `.steward/steward.db`，凭证加密密钥首次启动时自动保存在 `.steward/credential-master-key`。备份时请同时保存数据库和密钥；已有数据库必须继续使用原来的密钥。

始终从同一工作目录启动。升级前先停止服务并备份数据，再执行 `brew upgrade steward` 或安装新版本软件包。

### 网络部署

本机模式只允许监听回环地址，并拒绝跨来源请求。需要通过网络访问时，设置 Token 认证，并在服务前配置 HTTPS：

```bash
export STEWARD_AUTH_MODE=token
export STEWARD_AUTH_TOKEN="$(openssl rand -hex 32)"
export STEWARD_CREDENTIAL_MASTER_KEY="$(openssl rand -base64 32)"
steward server start --addr 0.0.0.0:8585
```

使用配置的 Token 登录。未指定认证模式但已设置 Token 时，也会使用 Token 模式，以兼容已有部署。

Steward Cloud 使用独立的账户和工作区网关。内部实例以 `STEWARD_AUTH_MODE=cloud` 运行：每个实例使用独立的服务令牌，并接收网关验证的用户身份，用于权限判断和操作审计。服务令牌不得发送到浏览器。

## 开发

需要 Git、Go 1.26+、Node.js 22+、npm、make，以及供 SQLite 使用的 C 编译器。

```bash
git clone https://github.com/loomx-ai/steward.git
cd steward
make install
make dev
```

打开 <http://127.0.0.1:5858>，开发代理通过自动生成的令牌保护 API。使用 `make run` 构建并在 8585 端口运行完整应用，或用 `make build` 生成内嵌 Web 控制台的 `bin/steward`。单独执行 `go install` 不包含控制台。

```bash
make test
make lint
make build
```

PostgreSQL 仓储契约测试使用 `STEWARD_TEST_POSTGRES_DSN`；未设置时自动跳过。

发布自动化与 Homebrew/Scoop 仓库配置见 [RELEASING.md](RELEASING.md)。

欢迎贡献。较大的改动请先创建 issue，并为行为变更补充测试。

## 许可证

Copyright 2026 LoomX

基于 [Apache License 2.0](LICENSE) 授权。
