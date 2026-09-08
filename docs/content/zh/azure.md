---
title: "Microsoft Azure"
description: "连接 Azure 订阅、发现资源，并审查支持的清理操作。"
navTitle: "Microsoft Azure"
---

## 连接订阅

每个 Azure 连接通过 Microsoft Entra 服务主体访问一个订阅。当前支持 Azure 公有云，不支持主权云和 Azure Stack 端点。

1. 在订阅所属租户创建应用注册和服务主体，再创建客户端密钥。记录**密钥值**，不是密钥 ID。
2. 在订阅范围授予 **Reader**，用于盘点、资源详情、地域发现和管理锁检查。自定义角色需要等效读取权限，包括 `Microsoft.Resources/subscriptions/read`、订阅资源与资源组、地域、各产品的读取操作及 `Microsoft.Authorization/locks/read`。
3. 打开 **设置 → 云连接 → 添加连接**，选择 **Microsoft Azure**，填写**订阅 ID**、**租户 ID**、**应用（客户端）ID**和**客户端密钥**。
4. 验证连接并刷新地域。仅为计划清理的资源补充产品删除和异步操作状态读取权限。连接验证通过不代表每个产品 API 都已授权。

清理 Blob 容器还需要数据平面读取权限，例如 **Storage Blob Data Reader**，并能通过网络访问账户的公有 Blob 端点。Steward 分别申请 ARM 和 Storage 访问令牌，不会使用机器已有的 Azure CLI 凭据、托管身份或存储账户密钥。

凭证使用部署的凭证加密密钥加密保存。轮换密钥时使用**替换凭证**；订阅、租户和应用必须保持一致。参阅微软的[服务主体认证说明](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-client-creds-grant-flow)。

## 第一次盘点

1. 切换到新连接并核对订阅。
2. 执行**全部启用地域与全局资源**扫描。Azure Resource Manager 提供资源列表，Steward 再调用各产品 API 获取支持的资源详情。
3. 检查扫描覆盖与错误。权限或分页失败不会被视为原有资源已经消失。
4. 打开地域中的 VNet，查看子网、网卡、VM 和关联资源。VM 的网络位置通过网卡解析。

订阅级列表可能不包含子资源，因此 Steward 会显式枚举 VNet 子网、Blob 容器、SQL 数据库和弹性池。资源组显示在全局清单中；Azure 为资源组记录的地域用于存放资源组元数据。完整 ARM ID 保留订阅与资源组边界，匹配时不区分大小写，不同资源组中的同名资源不会混淆。

## 盘点与清理范围

Steward 识别 34 类资源，其中 27 类支持删除。ARM 返回的其他资源类型作为只读清单展示。

| 产品 | 资源 | 清理能力 |
| --- | --- | --- |
| Compute | VM、托管磁盘、快照、托管镜像、可用性集 | 支持，受挂载与归属保护限制 |
| 虚拟网络 | VNet、子网、网卡、网络安全组、路由表、公网 IP、公网 IP 前缀、NAT Gateway | 支持 |
| 负载均衡 | Load Balancer、Application Gateway | 支持 |
| Storage | 存储账户、Blob 容器 | 仅空资源 |
| SQL | 数据库、弹性池 | 支持；`master` 数据库受保护 |
| PostgreSQL / MySQL | Flexible Server | 支持 |
| App Service | Web App / Function App、App Service Plan | 分别支持清理 |
| 容器 | 容器注册表、Container App | 支持 |
| 运维与身份 | Log Analytics 工作区、用户分配的托管身份 | 支持 |
| 托管或集合资源 | 资源组、VM Scale Set、Private Endpoint、SQL 逻辑服务器、AKS 集群、Key Vault、Container Apps 环境 | 只读 |

只读类型可能包含数据或管理其他资源，这些连带删除影响尚未完整纳入清理计划。展示这些资源不代表可以删除它们管理的资源。

## 清理保护

- **管理锁**：订阅、资源组、资源本身及相关子资源的锁会阻止删除。Steward 在盘点和实际删除前检查锁，不会移除锁。
- **托管资源**：由云服务管理的资源组中的资源、伸缩集成员 VM、Private Endpoint 网卡及其他云服务拥有的资源受到保护。
- **VM 挂载资源**：VM 或网卡配置了 `deleteOption: Delete` 时会被阻止清理。请先在 Azure 审查设置，按需改为 `Detach`，再重新扫描。已挂载的托管磁盘按依赖排在 VM 之后删除。
- **存储**：存储账户对应的服务必须没有 Blob 容器、文件共享、队列或表。Blob 容器不能含有 Blob、快照、版本、软删除条目或未提交上传。法律保留与不可变策略会阻止清理。Steward 不会清空或永久清除数据来满足删除条件。Blob 清理目前要求使用标准 `ACCOUNT.blob.core.windows.net` 端点。
- **VNet**：必须先删除子网。选择 VNet 不会隐式删除未选择的子网。
- **App Service**：删除应用时显式保留 App Service Plan；需要删除计划时，应单独选择。
- **异步操作**：Steward 跟踪 ARM 返回的操作状态，再重新读取资源确认其不存在。失败或取消的操作仍记为失败，不启用强制删除或永久清除选项。

执行前审查[清理选择与结果](./cleanup.md)。删除数据库或容器注册表可能删除其内部数据。Azure 权限、保留策略、依赖关系和并发变更仍可能阻止操作。参阅微软的[管理锁](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/lock-resources)、[VM 删除设置](https://learn.microsoft.com/en-us/azure/virtual-machines/delete)和[异步操作说明](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/async-operations)。
