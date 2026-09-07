---
title: "部署与维护"
description: "固定数据目录和加密密钥，通过 HTTPS 提供网络访问。"
navTitle: "部署与维护"
---

<span id="build"></span>

## 构建服务

在源码目录执行以下命令。产物包含 Web 界面，无需另起前端服务。

```
make install
make build
./bin/steward server start
```

本机访问使用默认的 127.0.0.1:8585。使用进程管理器部署时，将工作目录固定到 Steward 目录，并为 .steward 目录保留写权限。

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

<span id="config"></span>

## 常用配置

| 变量 | 用途 / 默认值 |
| --- | --- |
| `STEWARD_ADDR` | 监听地址；127.0.0.1:8585 |
| `STEWARD_DB_DRIVER` | sqlite / postgres；默认 sqlite |
| `STEWARD_DB_DSN` | SQLite 路径或 PostgreSQL 连接串 |
| `STEWARD_CREDENTIAL_MASTER_KEY` | 32 字节密钥的 Base64 编码；必须持久保存 |
| `STEWARD_SCAN_CONCURRENCY` | 扫描并发；默认 4，必须为正整数 |
| `STEWARD_AUTH_ROLE` | viewer / operator / admin；Token 对应角色，默认 admin |

<span id="backup"></span>

## 备份与升级

1.  先停止服务，再备份整个 .steward 目录；若数据库或密钥在其他位置，也一并备份。PostgreSQL 使用数据库自己的备份工具。
2.  更新到选定版本，重新执行 make install 和 make build。
3.  使用原工作目录、数据库和加密密钥启动。检查云连接并运行一次小范围扫描。

<span id="troubleshooting"></span>

## 常见问题

### 界面为空，没有资源

检查当前连接、扫描范围和失败目标；新建连接不会自动完成资源扫描。

### 重启后凭证无法解密

恢复原密钥，核对数据库和工作目录。不要用新密钥覆盖旧密钥。

### 清理提示没有权限

查看对应资源日志。连接验证通过或扫描成功，不代表有删除权限。

### 需要提交问题

附上版本、操作步骤、错误码和脱敏后的请求 ID。不要提交 Token、AccessKey、数据库或加密密钥。 [GitHub Issues ↗](https://github.com/loomx-ai/steward/issues)
