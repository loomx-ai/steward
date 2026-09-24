---
title: "在服务器上部署 Steward"
description: "把 Steward 作为常驻服务运行，通过登录令牌和 HTTPS 为团队提供安全访问，并备份必要的数据。"
navTitle: "部署服务"
---

默认情况下，`steward server start` 以本机模式运行：无需登录，只能从同一台机器访问。如果希望 Steward 在服务器上长期运行、并供其他人访问，请参考本页。

<span id="build"></span>

## 作为服务运行

在服务器上[安装 Steward](./installation.md)，然后交给进程管理器（例如 systemd）运行：

```sh
steward server start
```

- 数据保存在运行用户的 `~/.steward`。使用进程管理器时，请把 `STEWARD_HOME` 设为服务用户可写的持久目录。
- `steward server status` 显示服务是否在运行，以及监听地址和版本；`steward server stop` 停止服务。
- [升级](./installation.md#update)之后，重启服务才会运行新版本。

<span id="network"></span>

## 为团队开放访问

本机模式拒绝监听回环地址以外的地址。要接受其他机器的访问，请切换到 Token 模式，在前面放置 HTTPS 反向代理，并且只允许代理访问 Steward 的端口。

1. **只生成一次**登录令牌和凭证加密密钥，并保存到你的密钥管理系统：

   ```sh
   openssl rand -hex 32     # 登录令牌
   openssl rand -base64 32  # 凭证加密密钥
   ```

2. 通过进程管理器把它们传给服务，将占位符替换为已保存的值：

   ```
   STEWARD_AUTH_MODE=token
   STEWARD_AUTH_TOKEN=<saved-token>
   STEWARD_CREDENTIAL_MASTER_KEY=<saved-base64-key>
   STEWARD_ADDR=0.0.0.0:8585
   ```

3. 在反向代理上配置 HTTPS，转发到 8585 端口。
4. 打开 HTTPS 地址，用令牌登录。

重启时继续使用同一令牌和密钥，不要每次重新生成。如果要把已有的本机安装切换到 Token 模式，请把 `STEWARD_CREDENTIAL_MASTER_KEY` 设为原 `~/.steward/credential-master-key` 中的内容，否则已保存的云凭证将无法解密。

令牌登录后的角色由 `STEWARD_AUTH_ROLE` 决定（默认 `admin`），详见[角色](./configuration.md#roles)。浏览器和服务不在同一台机器上时，浏览器登录方式的云连接无法使用，请改用 AccessKey 或 [OIDC](./oidc.md)。

如需用 PostgreSQL 代替内置的 SQLite，设置 `STEWARD_DB_DRIVER=postgres` 和 `STEWARD_DB_DSN`，详见[配置参考](./configuration.md)。

<span id="backup"></span>

## 备份与恢复

Steward 需要数据库和凭证加密密钥同时存在，才能读取已保存的云凭证，因此两者必须一起备份。

- **SQLite（默认）**：停止服务后，复制整个数据目录（`~/.steward` 或 `STEWARD_HOME`），其中包含 `steward.db` 和 `credential-master-key`。
- **PostgreSQL**：使用数据库自己的备份工具，并把 `STEWARD_CREDENTIAL_MASTER_KEY` 的值与备份一起保存。
- 如果数据库或密钥存放在其他位置，也要一并备份。

恢复时，把原数据库和原密钥放回原处。换用其他密钥无法解密已保存的凭证。

[配置参考 →](./configuration.md) · [故障排查 →](./troubleshooting.md)
