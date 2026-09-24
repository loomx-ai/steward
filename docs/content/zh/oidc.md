---
title: "OIDC 云连接"
description: "通过工作负载身份联合获取短期凭证，接入阿里云、AWS、Google Cloud 或 Azure，Steward 中不保存任何长期云密钥。"
navTitle: "OIDC 云连接"
---

OIDC 连接让 Steward 在不保存长期 AccessKey 的情况下访问你的云账号。云平台把 Steward 服务视为可信的身份提供方，每次扫描或清理时，Steward 用一个短期令牌换取临时凭证。支持阿里云、AWS、Google Cloud 和 Azure。

工作过程：

1. Steward 服务签发一个 JWT，标明是哪个连接、处于读取还是删除阶段，有效期 5 分钟。
2. 云平台按你配置的信任关系（issuer、audience 和精确的 subject）校验令牌。
3. 云平台返回你所指定角色或服务身份的临时凭证。

这是服务之间的工作负载认证，与[浏览器登录](./connections.md#browser)不同。

只有服务运维者完成配置后，连接表单中才会出现 **OIDC 工作负载身份** 选项。在 Steward Cloud 中，只有 LoomX 为你的工作空间开启后才会出现。

## 在服务端启用 OIDC

本节面向运行 Steward 服务的人员。为每个独立部署配置稳定的 issuer URL 和独立的 RSA 签名密钥。issuer 和密钥只能由服务端配置决定，连接表单无法指定令牌来源、可执行程序或交换地址。

```sh
umask 077
openssl genpkey -algorithm RSA -pkeyopt rsa_keygen_bits:3072 -out oidc-signing.pem
export STEWARD_OIDC_ISSUER_URL=https://steward.example.com
export STEWARD_OIDC_WORKSPACE_ID=production
export STEWARD_OIDC_SIGNING_KEY_FILE=/absolute/path/oidc-signing.pem
```

- 签名密钥文件必须是私有文件（权限 `0600`），且服务进程可读。不要把它当作云凭证上传。
- issuer 必须使用 HTTPS，且不能以 `/` 结尾。可以带路径前缀，但反向代理必须保留该路径。
- 未配置这些变量时不显示 OIDC；配置不完整或无效时，服务无法启动。
- 测试环境与生产环境使用不同的 issuer 和密钥。

云平台的身份服务必须能在不登录 Steward 的情况下访问以下两个文档：

```text
<issuer>/.well-known/openid-configuration
<issuer>/.well-known/jwks
```

它们只包含 discovery 元数据和公钥，Steward 没有对外签发令牌的接口。Steward 的其余 API 仍需保持认证访问。

## 创建连接

1. 打开用户菜单 → **设置** → **云连接** → **添加云连接**，选择云厂商和 **OIDC 工作负载身份**。
2. 填写 Steward 要使用的角色或服务身份（见下表）。角色 ARN 可以先确定、稍后再到云端创建。
3. 保存连接，展开该连接的 **OIDC 信任配置**，其中列出需要信任的 issuer、audience，以及完整的 read subject 和 write subject。
4. 在云端用这些值配置信任关系，并为该身份授予资源权限。
5. 回到 Steward 验证连接，然后开始扫描。

subject 由服务端的工作空间 ID 和 Steward 生成的连接 ID 组成：

```text
workspace:<workspace-id>:connection:<connection-id>:run_phase:read
workspace:<workspace-id>:connection:<connection-id>:run_phase:write
```

- **`read`** 用于验证、地域发现、扫描和依赖分析；**`write`** 只用于执行清理。
- **一个身份还是两个。** 不填写可选的写入身份时，默认身份同时负责读写，需要信任两个 subject；填写写入身份后，默认身份信任 read subject，写入身份信任 write subject。
- **重命名**连接不会改变 subject；**删除后重建**会生成新的 subject，需要同步更新云端信任。
- subject 必须精确匹配，不要使用会同时匹配其他工作空间或连接的通配符。

连接验证只检查读取身份，不会测试清理权限。第一次正式清理之前，先用测试资源执行一次，确认写入身份的信任和权限都已生效。

## 在云端配置信任

| 云 | 连接字段 | 默认 audience | 云端需要配置的内容 |
| --- | --- | --- | --- |
| 阿里云 | 默认 Role ARN、OIDC Provider ARN；可选 Write Role ARN | `sts.aliyuncs.com` | 创建 RAM OIDC 身份提供商；角色信任策略允许 `sts:AssumeRoleWithOIDC`，并约束 issuer、audience 和 subject。 |
| AWS | 默认 Role ARN；可选 Write Role ARN | `sts.amazonaws.com` | 创建 IAM OIDC Provider，以该 audience 作为 Client ID；角色信任策略允许 `sts:AssumeRoleWithWebIdentity`，并精确匹配 `aud` 和 `sub`。 |
| Google Cloud | 资源项目 ID、Workload Provider 资源名、默认服务账号邮箱；可选写入服务账号 | `steward.workload.identity` | 创建 Workload Identity Pool 和 Provider，将 `google.subject` 映射到 `assertion.sub`，并允许该 audience；在服务账号上为对应 principal 授予 `roles/iam.workloadIdentityUser`，再给该服务账号授予资源权限。 |
| Azure | Subscription ID、Tenant ID、默认 Client ID；可选 Write Client ID | `api://AzureADTokenExchange` | 创建应用或服务主体，在其联合身份凭据中精确配置 issuer、subject 和 audience，并在订阅上分配 RBAC 角色。无需 Client Secret。 |

账号与项目的约束：

- **阿里云**：各角色与 OIDC Provider 必须属于同一账号。
- **AWS**：读取角色和写入角色必须属于同一账号。
- **Google Cloud**：身份池所在项目可以与资源项目不同，写入服务账号也可以来自其他项目，但资源项目由连接固定。Workload Provider 填写资源名，不加 URL 前缀：

  ```text
  projects/123456789/locations/global/workloadIdentityPools/steward/providers/production
  ```

  Steward 先换取联合令牌，再通过 IAM Credentials 模拟服务账号。需要启用 STS、IAM Credentials 和 Cloud Asset API。不接受任意凭证 JSON、可执行程序或外部令牌 URL。
- **Azure**：两个客户端都在配置的租户中认证，访问同一个订阅。

支持的云环境：AWS 商业分区、Azure 公有云（Entra ID 与 ARM）以及 Google 的 `googleapis.com`。不支持 AWS 中国区、AWS GovCloud、Azure 主权云和其他 Google universe。

### AWS 信任策略示例

将占位符替换为 IAM OIDC Provider ARN、去掉 `https://` 的 issuer，以及连接上显示的 subject。本例为读取角色；写入角色使用 write subject，一个角色同时负责读写时，可以把两个完整 subject 写成数组。

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

信任策略只允许 Steward 扮演该角色。资源读取、删除及相关操作的权限，需要另外附加权限策略。

## 凭证有效期与密钥轮换

- **令牌**：JWT 使用 RS256 签名，包含 `kid`、`iss`、`aud`、`sub`、`iat`、`nbf`、`exp`、随机 `jti`，以及工作空间、连接、云厂商、读写阶段和任务等 claims，有效期 5 分钟。
- **临时凭证**：阿里云和 AWS 的 STS 以及 Google Cloud 模拟凭证请求 1 小时有效期；Azure 使用云端返回的有效期。Steward 按配置版本、读写阶段、任务和 audience 缓存，到期前自动刷新。
- **存储**：令牌和临时凭证只保存在内存中，数据库只保存加密后的连接配置，不需要任何长期云密钥。
- **撤销**：停用连接或替换其配置后，Steward 不再使用缓存的凭证。但云端已签发的凭证仍可能在到期前有效，紧急撤销时还需修改云端信任或权限。
- **修改 issuer 或工作空间 ID** 会改变信任关系，不属于常规的密钥轮换。
- **轮换签名密钥**：PEM 文件可以包含**一个当前 RSA 私钥**和若干旧的 `PUBLIC KEY` 块。发布新私钥并保留旧公钥后重启服务：新令牌使用新的 `kid`，JWKS 仍保留旧公钥用于校验。云平台通常在遇到新 `kid` 时刷新公钥，正式切换前先用测试连接确认。至少等旧令牌有效期和云端公钥缓存窗口都过去之后，再移除旧公钥。

## 参考

- [阿里云 OIDC 单点登录](https://www.alibabacloud.com/help/zh/ram/user-guide/overview-of-oidc-based-sso)
- [AWS OIDC 身份提供商](https://docs.aws.amazon.com/IAM/latest/UserGuide/id_roles_providers_create_oidc.html)
- [Microsoft Entra 工作负载身份联合](https://learn.microsoft.com/zh-cn/entra/workload-id/workload-identity-federation)
- [Google Cloud 工作负载身份联合](https://docs.cloud.google.com/iam/docs/workload-identity-federation)
