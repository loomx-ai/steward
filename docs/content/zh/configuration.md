---
title: "配置参考"
description: "监听地址、数据库、登录方式、角色、扫描并发、OIDC 和版本检查相关的环境变量及默认值。"
navTitle: "配置参考"
---

Steward 通过环境变量配置。`steward server start` 也以命令行参数的形式支持其中最常用的几项，运行 `steward server start --help` 可查看完整列表。

## 服务配置

| 变量 | 用途 | 默认值 |
| --- | --- | --- |
| `STEWARD_ADDR` | 监听地址 | `127.0.0.1:8585` |
| `STEWARD_HOME` | 数据目录，存放 SQLite 数据库、凭证密钥和服务状态文件 | `~/.steward` |
| `STEWARD_AUTH_MODE` | `local`（无需登录，仅限本机访问）或 `token` | `local`；设置了令牌时为 `token` |
| `STEWARD_AUTH_TOKEN` | Token 模式的登录令牌 | — |
| `STEWARD_AUTH_ROLE` | 令牌对应的角色：`viewer`、`operator` 或 `admin` | `admin` |
| `STEWARD_AUTH_SUBJECT` | 使用令牌执行操作时，审计记录中显示的操作者名称 | `local-admin` |
| `STEWARD_CREDENTIAL_MASTER_KEY` | 32 字节密钥的 Base64 编码，用于加密已保存的云凭证。本机模式配合 SQLite 时会在数据目录自动生成 `credential-master-key`；Token 模式和 PostgreSQL 必须设置此变量。只要数据还在，就必须一直保留这个密钥。 | — |
| `STEWARD_DB_DRIVER` | `sqlite` 或 `postgres` | `sqlite` |
| `STEWARD_DB_DSN` | SQLite 文件路径或 PostgreSQL 连接串 | 数据目录中的 `steward.db` |
| `STEWARD_DB_MAX_CONNS` | PostgreSQL 连接池上限，正整数。应低于数据库角色的连接数限制。 | 不限制 |
| `STEWARD_SCAN_CONCURRENCY` | 同时执行的扫描项数量，正整数 | `4` |

[OIDC 云连接](./oidc.md)使用 `STEWARD_OIDC_ISSUER_URL`、`STEWARD_OIDC_WORKSPACE_ID` 和 `STEWARD_OIDC_SIGNING_KEY_FILE`。

当 `LC_ALL`、`LC_MESSAGES` 或 `LANG` 为中文语言环境时，命令行显示中文，否则显示英文。

<span id="roles"></span>

## 角色

| 角色 | 权限 |
| --- | --- |
| `viewer` | 查看云连接、扫描、资源清单、资源关系、清理任务和审计记录 |
| `operator` | 在 viewer 基础上，可以发起和控制扫描、标记资源，以及创建、执行、暂停和恢复清理任务 |
| `admin` | 在 operator 基础上，可以添加、重命名、验证、替换凭证和删除云连接 |

本机模式下始终是 admin。

## 版本检查

Steward 会向 `checkpoint.loomx.ai` 询问是否有新版本，以及当前版本是否受安全公告影响。`steward version` 会显示结果，任何命令都会显示适用的安全公告，常驻服务每天再检查一次。

请求只携带以下四项内容：

| 内容 | 示例 |
| --- | --- |
| 当前版本 | `0.3.1` |
| 操作系统 | `linux` |
| 架构 | `amd64` |
| 签名 | `4f0b…`，保存在 `~/.steward/checkpoint_signature` 中的随机 UUID |

签名用于让 LoomX 把同一个安装只统计一次，并避免重复推送同一条公告。它是随机生成的，不包含任何与你、这台机器或网络相关的信息；删除该文件即可换一个新的。资源清单、云连接、凭证、扫描结果以及你执行的命令都不会上报。

结果缓存在 `~/.steward/checkpoint_cache` 中，有效期一天，因此无论执行多少命令，一台机器每天最多访问该服务一次。请求 3 秒超时，失败时静默忽略，没有任何命令依赖它。

| 变量 | 用途 | 默认值 |
| --- | --- | --- |
| `STEWARD_CHECKPOINT_DISABLE` | 设为 `0` 以外的任意值即关闭检查 | — |
| `DO_NOT_TRACK` | 同上 | — |
| `STEWARD_CHECKPOINT_SIGNATURE_DISABLE` | 继续检查，但不发送签名 | — |
| `STEWARD_CHECKPOINT_URL` | 检查服务地址 | `https://checkpoint.loomx.ai` |
| `STEWARD_CHECKPOINT_TIMEOUT` | 请求超时，Go duration 格式 | `3s` |

在持续集成环境中同样不检查：设置了 `CI`（GitHub Actions、GitLab、CircleCI、Travis、Buildkite、Bitbucket），或设置了 `TF_BUILD`、`JENKINS_URL`、`TEAMCITY_VERSION`、`CODEBUILD_BUILD_ID` 之一时即视为 CI。值为 `0` 或 `false` 不算 CI。CI 任务每次都从全新的主目录启动，在这里检查会让每次运行都被算作一个新安装。

`steward update` 直接从 GitHub Releases 下载，关闭检查后仍可正常升级。

[网络访问与登录 →](./deployment.md#network)
