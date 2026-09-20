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
| `STEWARD_DB_MAX_CONNS` | PostgreSQL 连接池上限；默认不限制，必须为正整数。应低于数据库角色的连接数限制 |
| `STEWARD_CREDENTIAL_MASTER_KEY` | 32 字节密钥的 Base64 编码；必须持久保存 |
| `STEWARD_SCAN_CONCURRENCY` | 扫描并发；默认 4，必须为正整数 |
| `STEWARD_AUTH_ROLE` | viewer / operator / admin；Token 对应角色，默认 admin |

当 `LC_ALL`、`LC_MESSAGES` 或 `LANG` 为中文语言环境时，命令行显示中文，否则显示英文。

## 版本检查

Steward 会向 `checkpoint.loomx.ai` 询问是否有新版本、以及当前版本是否涉及安全公告。
`steward version` 会展示结果，任何命令都会展示安全公告，常驻服务每天再问一次。

请求只携带四项内容，没有其它信息：

| 内容 | 示例 |
| --- | --- |
| 当前版本 | `0.4.1` |
| 操作系统 | `linux` |
| 架构 | `amd64` |
| 签名 | `4f0b…`，保存在 `~/.steward/checkpoint_signature` 的随机 UUID |

签名用于把同一个安装只统计一次、并避免重复推送同一条公告。它是随机生成的，不包含
任何与你、这台机器或网络相关的信息；删除该文件即可换一个新的。资源清单、云账号连接、
凭证、扫描结果以及你执行了哪些命令，都不会上报。

结果缓存在 `~/.steward/checkpoint_cache`，有效期一天——无论执行多少命令，一台机器
每天最多访问该服务一次。请求 3 秒超时，失败时静默忽略，没有任何命令依赖它。

| 变量 | 用途 / 默认值 |
| --- | --- |
| `STEWARD_CHECKPOINT_DISABLE` | 设为 `0` 以外的任意值即完全关闭检查 |
| `DO_NOT_TRACK` | 同样生效 |
| `STEWARD_CHECKPOINT_SIGNATURE_DISABLE` | 继续检查，但不发送签名 |
| `STEWARD_CHECKPOINT_URL` | 服务地址；默认 `https://checkpoint.loomx.ai` |
| `STEWARD_CHECKPOINT_TIMEOUT` | 请求超时，Go duration 格式；默认 `3s` |

`steward update` 直接从 GitHub releases 下载，关闭检查后升级依然可用。



[网络访问与认证配置 →](./deployment.md#network)
