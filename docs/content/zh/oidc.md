---
title: "OIDC 云连接"
description: "通过工作负载身份联邦获取临时云凭证，无需上传长期云密钥。"
navTitle: "OIDC 云连接"
---

Steward 使用 OIDC 工作负载身份联合：为工作负载签发短期 JWT，云厂商验证客户配置的信任关系，再返回临时凭证。支持 AWS、阿里云、GCP 和 Azure。OIDC 是后台工作负载授权，不是用户浏览器登录。

## 管理员准备

为每个独立工作区配置稳定的 issuer 和独立 RSA 签名密钥。配置只从服务器环境读取，连接表单不能设置签发者、私钥、令牌来源或交换地址。

```sh
umask 077
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out oidc-signing.pem
export STEWARD_OIDC_ISSUER_URL=https://steward.example.com
export STEWARD_OIDC_WORKSPACE_ID=production
export STEWARD_OIDC_SIGNING_KEY_FILE=/absolute/path/oidc-signing.pem
```

签名文件必须为服务进程可读的私有文件（权限 `0600`）。issuer 必须使用 HTTPS，不能以 `/` 结尾。可以包含路径，但代理必须保留对应路径。环境变量未配置时不开放 OIDC；配置不完整或密钥无效时服务器启动失败。不要将签名密钥作为云连接凭证上传。

云端必须能够在不登录 Steward 的情况下访问以下端点：

```text
<issuer>/.well-known/openid-configuration
<issuer>/.well-known/jwks
```

这些端点只返回 discovery 和公钥；Steward 没有对外开放签发 JWT 的 API。不要将整个工作区 API 暴露为匿名访问。

### Steward Cloud

Cloud 网关按工作区提供以下公开路径：

```text
https://<console-host>/oidc/workspaces/<workspace-id>/.well-known/openid-configuration
https://<console-host>/oidc/workspaces/<workspace-id>/.well-known/jwks
```

对应工作区进程配置：

```text
STEWARD_OIDC_ISSUER_URL=https://<console-host>/oidc/workspaces/<workspace-id>
STEWARD_OIDC_WORKSPACE_ID=<workspace-id>
STEWARD_OIDC_SIGNING_KEY_FILE=<该工作区独立签名文件的绝对路径>
```

网关仅转发这两个公开文档，不转发会话或服务令牌。每个工作区独立签名，不共享私钥。现有工作区 provisioner 不会自动生成签名密钥或修改上述配置；由运维显式启用。使用 systemd DynamicUser 时，可通过工作区专用的 `LoadCredential` drop-in 提供密钥，并将签名文件变量指向服务的 credentials directory。测试与生产使用不同的 issuer 和密钥。

## 创建和授权

1. 设置 → 云连接 → 添加连接，选择云厂商和「OIDC 工作负载身份」。
2. 填写目标角色或服务身份。可以先确定将创建的角色 ARN，再到云端创建角色。
3. 保存连接，展开该连接的「OIDC 信任配置」。
4. 在云端使用显示的 **issuer、audience、完整 read subject 和 write subject** 配置信任及权限。
5. 返回 Steward 验证连接；通过后开始扫描。

subject 绑定服务器配置的工作区 ID 和服务器生成的连接 ID，重命名连接不改变 subject。删除并重新创建连接会生成新的 subject，需要更新云端信任。不要使用覆盖其他工作区或连接的通配符。

```text
workspace:<workspace-id>:connection:<connection-id>:run_phase:read
workspace:<workspace-id>:connection:<connection-id>:run_phase:write
```

验证、地域发现、扫描和依赖分析使用 `read`；持久化清理执行任务使用 `write`。只配置默认身份时，两种阶段使用同一云身份，因此需要信任两个 subject。可选写入身份仅供清理执行使用；填写后，分别给默认身份信任 read subject、给写入身份信任 write subject。

连接验证检查读取身份，不会提前假扮清理任务来验证写入权限。首次清理应在测试资源上验证写入身份的信任和所需权限。

## 各云配置

| 云 | 连接字段 | 默认 audience | 云端操作 |
| --- | --- | --- | --- |
| AWS | 默认 Role ARN；可选 Write Role ARN | `sts.amazonaws.com` | 创建 IAM OIDC Provider，配置 Client ID 为 audience；角色允许该 Provider 执行 `sts:AssumeRoleWithWebIdentity`，精确匹配 `aud` 和 `sub`。 |
| 阿里云 | 默认 Role ARN、OIDC Provider ARN；可选 Write Role ARN | `sts.aliyuncs.com` | 创建 RAM OIDC 身份提供商；角色允许 `sts:AssumeRoleWithOIDC`，约束 issuer、audience、subject。 |
| GCP | 资源项目 ID、Workload Provider 资源名、默认服务账号邮箱；可选写入服务账号邮箱 | `steward.workload.identity` | 创建 Workload Identity Pool/Provider，将 `google.subject` 映射到 `assertion.sub`，将 audience 配置为允许值；允许对应 subject 以 `roles/iam.workloadIdentityUser` 模拟目标服务账号，再给服务账号授予目标项目权限。 |
| Azure | Subscription ID、Tenant ID、默认 Client ID；可选 Write Client ID | `api://AzureADTokenExchange` | 创建应用/服务主体，在其 Federated Identity Credentials 中精确配置 issuer、subject 和 audience，再在目标订阅授予 RBAC 权限。无需 Client Secret。 |

GCP 的 Workload Provider 字段使用以下格式，不加 URL 前缀：

```text
projects/123456789/locations/global/workloadIdentityPools/steward/providers/production
```

GCP 会先交换联邦访问令牌，再调用 IAM Credentials 模拟配置的服务账号；不接受任意凭证 JSON、可执行程序或外部令牌 URL。启用所需的 STS、IAM Credentials、Cloud Asset 等 API。身份池所在项目可以与资源项目不同。

AWS 默认角色和写入角色必须属于同一账号；阿里云的默认角色、写入角色与 OIDC Provider 必须属于同一账号。Azure 写入客户端仍在连接指定的 tenant 中认证，访问同一 subscription。GCP 写入服务账号可以来自另一个项目，但资源项目由连接的 project ID 固定。

AWS 当前使用商业分区 STS，Azure 使用公共云 Entra/ARM，GCP 使用 `googleapis.com`。本功能不新增 AWS 中国/GovCloud、Azure 主权云或其他 GCP universe 支持。

### AWS 信任策略示例

将下面占位符替换为 IAM Provider ARN、issuer 去掉 `https://` 后的值，以及连接页面显示的 subject。此示例是读取角色；写入角色使用 write subject。共享一个角色时可将 `sub` 条件设为包含两个完整 subject 的数组。

```json
{
  "Version": "2012-10-17",
  "Statement": [{
    "Effect": "Allow",
    "Principal": {"Federated": "<IAM_OIDC_PROVIDER_ARN>"},
    "Action": "sts:AssumeRoleWithWebIdentity",
    "Condition": {
      "StringEquals": {
        "<ISSUER_WITHOUT_HTTPS>:aud": "sts.amazonaws.com",
        "<ISSUER_WITHOUT_HTTPS>:sub": "<READ_SUBJECT>"
      }
    }
  }]
}
```

这只是信任策略。资源 API 的读取、删除及相关权限需要作为角色权限策略另行配置。

## 生命周期与轮换

- JWT 使用 RS256，包含 `kid`、`iss`、`aud`、`sub`、`iat`、`nbf`、`exp`、随机 `jti`，有效期 5 分钟；额外 claims 包含工作区、连接、云厂商、读写阶段和任务 ID。
- AWS/阿里云 STS 与 GCP 模拟凭证请求 1 小时有效期；Azure 使用云端返回的有效期。临时云凭证按配置版本、读写阶段、任务和访问范围缓存，到期前重新交换。
- JWT 和临时云凭证只保存在运行内存中。数据库只保存加密后的连接配置，不存客户长期云私钥。
- 连接被撤销或配置被替换后，运行时停止再次取用对应旧缓存。已经由云签发的凭证仍可能有效至云端过期；紧急撤销需同时在云端调整信任或权限。
- 修改 issuer 或工作区 ID 会改变信任，不能作为普通密钥轮换操作。
- 签名 PEM bundle 可包含 **一个当前 RSA 私钥** 和旧密钥的 `PUBLIC KEY` PEM。发布新私钥与旧公钥并重启后，新的 JWT 使用新 `kid`，JWKS 同时保留旧公钥。云端通常会在遇到新 `kid` 后刷新公钥；正式切换前在测试连接验证该云的缓存行为。至少等待旧 JWT 的有效期和公钥缓存窗口后，才能移除旧公钥。

## 参考

- [AWS OIDC 身份提供商](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_providers_create_oidc.html)
- [Microsoft Entra 工作负载身份联合](https://learn.microsoft.com/zh-cn/entra/workload-id/workload-identity-federation)
- [Google Cloud 工作负载身份联合](https://docs.cloud.google.com/iam/docs/workload-identity-federation)
- [阿里云 OIDC 单点登录](https://www.alibabacloud.com/help/zh/ram/user-guide/overview-of-oidc-based-sso)
