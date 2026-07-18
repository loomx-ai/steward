# Steward

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

Steward 是一个开源云治理服务，提供资产盘点、拓扑分析、治理发现和受控资源清理。目前支持 Alibaba Cloud 和 AWS。

## 功能

- 使用云厂商原生 API 建立可搜索的资产清单。
- 查看账号、地域、网络和资源关系。
- 基于标准化资源数据生成治理发现。
- 通过依赖排序、安全阻断、幂等调用、结果回读和审计执行清理。

## 快速开始

需要 Go 1.26+、Node.js 22+ 和 npm。

```bash
git clone git@github.com:loomx-ai/steward.git
cd steward
make install
make dev
```

打开 <http://127.0.0.1:5858>。开发命令会自动生成本地登录令牌和凭证加密密钥。

在本地构建并运行生产服务：

```bash
export STEWARD_AUTH_TOKEN="$(openssl rand -hex 32)"
export STEWARD_CREDENTIAL_MASTER_KEY="$(openssl rand -base64 32)"
make run
```

打开 <http://127.0.0.1:8585>，使用 bearer token 登录。SQLite 数据默认保存在 `.steward/steward.db`。

## 开发

```bash
make test
make lint
make build
```

PostgreSQL 仓储契约测试使用 `STEWARD_TEST_POSTGRES_DSN`；未设置时自动跳过。

欢迎贡献。较大的改动请先创建 issue，并为行为变更补充测试。

## 许可证

Copyright 2026 Prodesire

基于 [Apache License 2.0](LICENSE) 授权。
