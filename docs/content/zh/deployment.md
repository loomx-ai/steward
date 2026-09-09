---
title: "部署 Steward"
description: "运行服务、配置网络访问并备份数据。"
navTitle: "部署服务"
---

<span id="build"></span>

## 运行服务

按[安装指南](./installation.md)安装 Steward，并在固定目录启动：

```sh
mkdir -p "$HOME/steward-data"
cd "$HOME/steward-data"
steward server start
```

使用进程管理器时，将工作目录设为该目录，并为 `.steward/` 保留写权限。

<span id="network"></span>

## 网络访问

本机模式拒绝非回环监听。网络部署使用 Token 模式，在反向代理上配置 HTTPS，并限制后端端口仅供代理访问。

首次部署生成并妥善保存认证令牌和加密密钥，再通过进程管理器注入以下变量。不要每次启动都重新生成。

```
# Generate once; store the outputs securely
openssl rand -hex 32
openssl rand -base64 32
```

```
STEWARD_AUTH_MODE=token
STEWARD_AUTH_TOKEN=<saved-token>
STEWARD_CREDENTIAL_MASTER_KEY=<saved-base64-key>
STEWARD_ADDR=0.0.0.0:8585
```

以上是配置模板，需替换占位值。用户通过 HTTPS 打开界面后，使用保存的 Token 登录。从本机模式迁移时，加密密钥必须使用原 .steward/credential-master-key 中的值。

<span id="backup"></span>

## 备份数据

停止服务后，备份整个 `.steward/` 目录，包括数据库和原始凭证密钥。数据库或密钥配置在其他位置时也需一并保存；PostgreSQL 使用数据库自己的备份工具。

恢复时使用原数据库、原密钥和相同的工作目录。更换密钥会导致已有凭证无法解密。

[配置参考 →](./configuration.md) · [故障排查 →](./troubleshooting.md)
