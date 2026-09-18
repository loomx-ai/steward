---
title: "配置参考"
description: "配置监听地址、数据库、认证方式和扫描并发。"
navTitle: "配置参考"
---

## 常用配置

| 变量 | 用途 / 默认值 |
| --- | --- |
| `STEWARD_AUTH_MODE` | `local` / `token`；默认本机模式，设置 Token 后默认使用 Token 模式 |
| `STEWARD_AUTH_TOKEN` | Token 模式的登录令牌 |
| `STEWARD_ADDR` | 监听地址；127.0.0.1:8585 |
| `STEWARD_HOME` | SQLite 数据库、凭证密钥和服务状态的目录；`~/.steward` |
| `STEWARD_DB_DRIVER` | sqlite / postgres；默认 sqlite |
| `STEWARD_DB_DSN` | SQLite 路径或 PostgreSQL 连接串 |
| `STEWARD_CREDENTIAL_MASTER_KEY` | 32 字节密钥的 Base64 编码；必须持久保存 |
| `STEWARD_SCAN_CONCURRENCY` | 扫描并发；默认 4，必须为正整数 |
| `STEWARD_AUTH_ROLE` | viewer / operator / admin；Token 对应角色，默认 admin |

当 `LC_ALL`、`LC_MESSAGES` 或 `LANG` 为中文语言环境时，命令行显示中文，否则显示英文。



[网络访问与认证配置 →](./deployment.md#network)
