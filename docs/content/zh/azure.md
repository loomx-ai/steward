---
title: "Microsoft Azure"
description: "连接 Azure 订阅，配置 Steward 所需的 RBAC 与数据面权限，完成第一次盘点，并按产品查看清理会做什么、受哪些保护。"
navTitle: "Microsoft Azure"
---

本页帮助你把一个 Azure 订阅接入 Steward、配置所需权限，并按产品查看 Steward 能发现哪些资源、清理时会做什么。[常见问题](#常见问题)在页面末尾。

**连接覆盖的范围。** 每个 Azure 连接读取 Azure 公有云中的一个订阅，不支持主权云和 Azure Stack 端点。地域资源显示在所在的 Azure 地域；资源组及订阅级资源（例如 RBAC 角色定义与分配、诊断设置、通信与邮件资源、注册域名和 Cosmos DB 账号）显示在全局范围。

**Steward 如何读取 Azure。**

- **Azure Resource Manager**（ARM）提供订阅的整体盘点。对已支持的资源类型，Steward 还会调用各产品自身的列表和详情 API。没有原生支持的 ARM 资源类型作为只读清单展示。
- **产品数据面**通过各自的端点读取，每个数据面使用独立的 Microsoft Entra 令牌：Storage、Batch、Communication Services、Key Vault、Microsoft Graph 和 Synapse。参见[数据面与目录权限](#数据面与目录权限)。
- **子资源单独发现。** VNet 子网、Blob 容器、SQL 数据库、伸缩集实例、DNS 记录、Service Bus 实体、Event Hubs 消费者组等子资源由 Steward 自行列举，而不是依赖 ARM 资源列表。
- **已知资源不会被静默删除记录。** 列表遗漏了此前发现过的资源时，Steward 会按 ID 单独读取。权限或分页失败不会被当作资源已消失：只有资源自身的读取确认不存在，旧记录才会关闭。
- **私有配置保持私密。** 密钥、脚本、连接详情以及消息和通知内容不会进入公开清单和 API 日志；各产品小节会列出具体排除的内容。

## 连接订阅

打开用户菜单 → **设置** → **云连接** → **添加云连接**，选择 **Microsoft Azure**，再选择凭证类型：

| 凭证类型 | 需要填写 | 适用场景 |
| --- | --- | --- |
| **Azure 服务主体** | **订阅 ID**、**租户 ID**、**应用（客户端）ID**、**客户端密钥** | 专用身份，权限范围稳定，适合无人值守运行。 |
| **OAuth** | 在浏览器中登录，再选择一个订阅 | 以你本人的账号[用浏览器登录](./connections.md#browser)。 |
| **OIDC 工作负载身份** | 订阅 ID、租户 ID、客户端 ID；可选的写入客户端 ID | 服务端已配置 [OIDC](./oidc.md) 时使用，无需保存密钥即可换取临时凭证。 |

Steward 只使用你填写的凭证，不会使用机器上已有的 Azure CLI 凭据、托管身份、存储账户密钥或 Communication Services 账户密钥。

### 使用服务主体

1. 在订阅所属租户中创建应用注册和服务主体，再创建客户端密钥。记录**密钥值**，不是密钥 ID。
2. 按[权限](#权限)一节在订阅上分配角色。起步授予 **Reader** 即可。
3. 打开 **设置** → **云连接** → **添加云连接**，选择 **Microsoft Azure** → **Azure 服务主体**，填写**订阅 ID**、**租户 ID**、**应用（客户端）ID**和**客户端密钥**。
4. 验证连接，然后刷新地域。

结果检查：验证通过只说明身份有效，不代表每个产品 API 都已授权。执行[第一次盘点](#第一次盘点)，按扫描报告补齐权限。

参阅微软的[服务主体认证说明](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-client-creds-grant-flow)。

### 用浏览器登录

选择 **OAuth**，即可通过[浏览器登录](./connections.md#browser)接入，不必创建服务主体。Steward 引导你在浏览器中登录，并列出账号可访问的订阅；选择其中一个，租户随之确定。

这样的连接以你本人的身份运行，并为每个 audience（ARM、Microsoft Graph、Storage、Batch、Communication Services 和 Key Vault）分别申请令牌。账号无法访问的数据平面（例如 Key Vault 或 Microsoft Graph）只会表现为对应数据源失败，其余盘点照常进行。需要长期稳定、无人值守的权限范围时，服务主体更合适。

### 使用 OIDC 工作负载身份

Steward 服务端已配置工作负载身份时，可选择 **OIDC 工作负载身份**。Steward 用短期工作负载令牌换取 Azure 凭证，不保存客户端密钥。在 Azure 中为应用添加联合身份凭据，使其与连接的 **OIDC 信任配置**中显示的颁发者、主体和 audience 一致，并为该服务主体授予与上文相同的 RBAC 角色。读取身份用于验证和扫描，可选的写入身份用于清理。完整配置见 [OIDC 连接](./oidc.md)。

### 轮换凭证

凭证使用部署的凭证加密密钥加密保存。轮换客户端密钥时使用**替换凭证**；订阅、租户和应用必须保持一致。

## 权限

按层次授权：盘点所需的基础读取权限；仅为计划清理的资源授予删除权限；需要数据面或目录权限的产品再单独授权。

### 盘点所需的基础权限

在订阅范围授予 **Reader**，用于盘点、资源详情、地域发现和管理锁检查。

自定义角色需要等效的读取权限，包括：

- `Microsoft.Resources/subscriptions/read`
- 订阅资源与资源组、地域以及各资源提供程序读取操作的权限
- `Microsoft.Authorization/locks/read`

Reader 只包含 ARM 读取操作。部分盘点调用使用其他操作，例如 Azure NetApp Files 网络同级集合需要 `Microsoft.NetApp/locations/queryNetworkSiblingSet/action`。[清理保护](#清理保护)下的各产品小节会列出这类额外权限，以及自定义角色必须包含的读取权限。

管理组以只读方式盘点，需要对要盘点的管理组具有 `Microsoft.Management/managementGroups/read`。

### 清理所需的权限

仅为计划清理的资源类型授予删除权限。对每个选中的资源，清理需要：

- 该资源的原生 `delete` 操作；
- 读取其异步操作状态的权限；
- 删除前检查依赖所需的读取权限：资源本身、父资源、资源组和管理锁。

部分检查会读取与所选资源不同类型的资源，也会超出所选资源组：

- 所有 ARM 资源的清理都会读取订阅的角色分配和角色定义索引（见 [Azure RBAC](#azure-rbac)），以及六类原生告警规则集合（见 [Monitor 告警与预算](#monitor-告警与预算)）。
- 删除 AKS 集群、子网或用户分配托管身份时，会读取订阅中的 Kubernetes Fleet 集合（见 [Kubernetes Fleet Manager](#kubernetes-fleet-manager)）。
- 删除 Application Insights 组件、Log Analytics 工作区或数据收集终结点时，会读取订阅中的 Azure Monitor 专用链接范围（见 [Azure Monitor 专用链接范围](#azure-monitor-专用链接范围)）。
- 删除可能作为 Data Migration 目标的资源时，会读取订阅的迁移索引（见 [Data Migration](#data-migration)）。
- 删除诊断设置的源、目标或其祖先时，会读取各源范围内的诊断设置（见[诊断设置](#诊断设置)）。

部分产品除 `delete` 外还需要其他操作，例如先停止、取消、解除绑定或解除配对：

| 产品 | 清理所需的额外操作 |
| --- | --- |
| [Azure Batch](#azure-batch) | Batch 数据面权限，例如 **Azure Batch Data Contributor** |
| [Kubernetes Fleet Manager](#kubernetes-fleet-manager) | 停止更新运行；写入和 Apply Cluster Mesh 配置 |
| [Data Factory](#data-factory) | 准备操作，例如停止触发器、CDC 和运行时，取消管道运行 |
| [Data Migration](#data-migration) | 任务与迁移的 Cancel、SQL `deleteNode` |
| [Azure NetApp Files](#azure-netapp-files) | 清理策略和备份库时需要卷更新及最新备份状态读取 |
| [Communication Services](#communication-services) | 电话号码、预留号码和房间的数据面删除权限 |

Steward 永远不会删除授予连接自身访问权的角色分配，见[通用保护](#通用保护)。

### 数据面与目录权限

以下产品通过各自端点、使用独立令牌读取，Reader 不覆盖这些权限。

| 产品 | 令牌 audience | 需要授予 |
| --- | --- | --- |
| Blob 容器 | `https://storage.azure.com/.default` | 数据平面读取权限，例如 **Storage Blob Data Reader**，并能通过网络访问账户的公有 Blob 端点。Blob 清理目前要求使用标准 `ACCOUNT.blob.core.windows.net` 端点。 |
| Azure Batch 作业、计划、任务和节点 | `https://batch.core.windows.net//.default` | Batch 数据权限，清理可使用 **Azure Batch Data Contributor**；另需所选账户资源的 ARM 权限。参见 [Batch 身份验证](https://learn.microsoft.com/en-us/azure/batch/batch-aad-auth)与 [Batch 角色](https://learn.microsoft.com/en-us/azure/batch/batch-role-based-access-control)。 |
| Communication Services 电话号码、预留号码和房间 | `https://communication.azure.com/.default` | 原生数据读取权限（包括房间参与者列表），清理时还需相应的删除权限。Steward 从已验证归属的 ARM 账户获取数据端点。参见 [Communication Services 身份验证](https://learn.microsoft.com/en-us/rest/api/communication/authentication)。 |
| Key Vault 证书 | `https://vault.azure.net/.default` | 证书 list/get 数据面权限（**Key Vault 读者**角色，或包含证书“列出”和“获取”的访问策略），以及到保管库的网络访问。无法读取的保管库会使扫描失败，而不会显示为空。 |
| Microsoft Entra 用户与组 | `https://graph.microsoft.com/.default` | 经管理员同意的 Microsoft Graph 应用权限 `User.Read.All` 与 `GroupMember.Read.All`（或 `Directory.Read.All`）。 |
| Synapse Spark 任务与会话、Notebook、Spark 作业定义和 Pipeline | `https://dev.azuresynapse.net/.default` | Synapse 数据面列表与读取权限，见 [Azure Synapse Analytics](#azure-synapse-analytics)。 |

### 各产品的专属权限

[清理保护](#清理保护)下的各产品小节列出盘点所需的读取权限，以及每种清理操作额外需要的权限。产品的删除权限只需为选中清理的资源授予。

## 第一次盘点

1. 切换到新连接并核对订阅。
2. 打开 **扫描** → **开始扫描**，选择 **全部启用地域 + 全局**。Steward 会对已支持的资源执行原生产品列表和详情读取，同时保留 ARM 的整体盘点。
3. 检查扫描覆盖与错误。权限或分页失败不代表原有资源已经消失；补齐权限后重新扫描。
4. 在 **资源全景** 中打开地域内的 VNet，查看子网、网卡、VM 和关联资源。VM 的网络位置通过网卡解析。

需要了解：

- 资源组显示在全局清单中；Azure 为资源组记录的地域用于存放资源组元数据。完整 ARM ID 区分订阅和资源组。
- 扫描全局告警规则和预算时须包含全局范围。
- Cosmos DB 请求保留数据资源名称的大小写；仅大小写不同的名称若在清单身份中冲突，会使扫描失败。

**扫描指定网络。** 选择 **指定网络** 可将扫描限制在 Azure 虚拟网络或 Azure Local 逻辑网络。**虚拟网络 / 逻辑网络** 列表支持按名称或 ARM ID 搜索，并提供分页。列出 Local 网络需要 `Microsoft.AzureStackHCI/logicalNetworks/read`。逻辑网络内部配置的子网不作为独立 ARM 资源供选择。创建扫描任务时会重新读取所选网络；网络已删除或不可访问时，不会保存任务。权限错误会显示在界面中，**重试** 会保留已选网络。

## 盘点与清理范围

Steward 识别 522 类 Azure 资源，其中 470 类具有原生清理操作（包括 Batch 节点移除），执行时受下文保护约束。ARM 返回的其他资源类型作为只读清单展示。覆盖范围仍在扩展，尚未完整覆盖 Azure 的所有产品。

| 产品 | 资源 | 清理能力 |
| --- | --- | --- |
| Compute | VM 与扩展、托管磁盘、快照、托管镜像、可用性集、专用宿主机、容量预留 | 支持，受[挂载与归属保护](#通用保护)限制；宿主机组和容量预留组须先删除成员 |
| VM Scale Set | Uniform、Flexible 伸缩集及实例、扩展 | Uniform 成员纳入伸缩集的已审查删除影响；Flexible 伸缩集须先删除其 VM |
| [Azure Batch](#azure-batch) | 账户、池、节点、作业、计划、任务、应用、包版本、专用终结点连接及网络边界视图 | 已审查的前置删除与级联清理；精确移除节点并将其运行中任务重新排队；边界视图随账户清理 |
| [API Management](#api-management) | 服务、工作区、API 及修订、策略、产品、订阅、门户内容与配置、凭据、通知、关联、自托管网关注册及独立工作区网关 | 已审查的级联清理与有序解绑；固定配置随所属控制资源清理；服务删除遵循软删除保留期 |
| 虚拟网络 | VNet、子网、网卡、网络安全组、路由表、公网 IP、公网 IP 前缀、NAT Gateway | 支持，受[网络占用检查](#通用保护)限制 |
| 负载均衡 | Load Balancer 及其后端池、Application Gateway | 支持；仍被负载均衡、NAT 或出站规则或网卡使用的后端池受保护，后端池随其负载均衡器一起删除 |
| Storage | 存储账户、Blob 容器 | [仅空资源](#通用保护) |
| SQL | 逻辑服务器、数据库、弹性池 | 服务器清理包含已审查的数据库与弹性池；禁止单独删除 `master` |
| PostgreSQL / MySQL | Flexible Server | 支持 |
| [Cosmos DB](#cosmos-db) | NoSQL、MongoDB、Cassandra、Gremlin、Table 账号及数据库/容器，角色、服务、笔记本与私有连接；托管 Cassandra 和 Fleet | 先删除已审查的子资源及共享依赖；客户端加密密钥和内置角色随控制资源清理；Fleet 解绑保留账号 |
| [Azure DocumentDB](#azure-documentdb) | MongoDB 兼容集群及副本、防火墙规则、专用终结点连接与 Microsoft Entra 用户 | 先删除已审查的副本及子资源，再删除源集群或父资源；单独删除副本会保留源集群 |
| [Azure Data Explorer](#azure-data-explorer) | Kusto 集群、数据库、跟随挂接、数据连接、主体、脚本与私有连接；自定义沙箱映像 | 先删除已审查的子资源及跟随挂接；只读数据库与活动映像随控制资源清理 |
| [Azure Synapse](#azure-synapse-analytics) | 工作区、Spark 池、SQL 池、Spark 任务与会话、Notebook、Spark 作业定义、Pipeline、可恢复的已删除 SQL 池与还原点 | 工作区清理包含池、代码资产和 Spark 任务；SQL 池、Spark 池和用户还原点可独立清理；代码资产和 Spark 任务不能单独清理 |
| [Data Factory](#data-factory) | 工厂、管道、数据集、数据流、链接服务、凭据、触发器、CDC、全局参数、集成运行时及节点、私有连接 | 审查工厂级联；先处理运行时及运行中的任务；托管虚拟网络随工厂清理 |
| [Data Migration](#data-migration) | 经典服务、项目、任务/文件和服务任务；SQL/Mongo 迁移服务及面向 SQL、Cosmos DB 的迁移 | 审查子资源及迁移前置删除；先取消任务、处理运行时节点，再删除 |
| [Defender for Cloud](#defender-for-cloud) | 订阅防护计划及支持的资源级计划状态 | 只读展示服务状态、覆盖率、扩展及继承关系 |
| [Azure Arc](#azure-arc) | 机器、扩展、运行命令、许可证配置和共享 ESU 许可证 | 先清理已审查的子资源，再移除普通机器注册；共享 ESU 许可证需先解除分配；控制器托管的机器随控制器处理 |
| [Azure Local](#azure-local) | VM 实例、访客代理、身份元数据、网卡、磁盘、网络、存储路径与镜像 | 访客、VM 与 Arc 注册清理；核验 VM 引用后清理磁盘和网卡；镜像清理不删除已部署 VM；明确审查工作负载后清理存储路径与逻辑网络 |
| [Stream Analytics](#stream-analytics) | 作业、输入、输出、函数、转换、集群和集群私有终结点 | 作业定义随作业删除；集群中的作业须明确选中或先移出集群 |
| [Foundry / Cognitive Services](#foundry-与-cognitive-services) | 账号、部署、项目、代理、连接、能力主机、托管网络、内容过滤与承诺计划 | 先删除部署及已审查的依赖，再软删除账号；不执行永久清除 |
| [Azure AI Search](#azure-ai-search) | 服务、专用终结点连接、共享私有链接与网络边界配置视图 | 先删除已审查的链接；边界配置视图随服务清理 |
| [Redis](#redis) | 经典缓存、访问策略/分配、防火墙规则、复制链接、维护计划和专用终结点连接；Enterprise / Managed Redis 集群、数据库、访问分配和专用终结点连接 | 先删除已审查的子资源，再删除父资源；经典复制先解除链接；健康的主动复制组检查全部成员 |
| [App Service](#app-service) | Web App / Function App、部署槽、函数、证书、主机名绑定与服务计划 | 应用和部署槽审查所属子资源；服务计划保持独立；证书须无 TLS 绑定 |
| [域名注册](#app-service-域名) | 注册域名与所有权标识 | 先删除已审查的标识和应用/部署槽主机名绑定，再按原生延迟删除域名；DNS 托管保持独立 |
| [Communication Services](#communication-services) | 通信账户、SMTP 用户名、电话号码、预留号码、房间、邮件资源、域、发件人用户名、抑制列表及地址 | 先清理已审查的子资源；账户删除释放已审查的电话号码；邮件域共享连接须明确选择前置清理 |
| [CDN 与 Front Door](#cdn-与-front-door) | 配置、经典终结点/源站/源站组/域名；Front Door 终结点/路由/源站组/源站/域名/规则集/规则/安全关联/证书引用 | 配置及所属子资源树审查级联影响；共享引用按依赖顺序清理 |
| [WAF](#waf-策略) | CDN 与 Front Door 策略 | 先删除引用的终结点和安全关联；经典 Front Door 引用仍阻止清理 |
| 容器 | 容器注册表、Container App、[Container Instances](#container-instances) 容器组、AKS | AKS 清理审查节点资源组及已知的嵌套、外部托管资源 |
| [Kubernetes Fleet Manager](#kubernetes-fleet-manager) | Fleet、AKS 和 Arc 成员、托管命名空间、更新运行、策略、自动升级配置、Gate 与跨集群网络 | 先删除已审查的原生子资源，再删除 Fleet；跨集群网络先断开，更新运行负责所属 Gate，经核验的 Hub 资源随 Fleet 清理 |
| DNS 与私有终结点 | 公有/私有 DNS 区域及记录、私有 DNS 链接、Private Endpoint 与 DNS 区域组 | 级联审查包含已验证的托管网卡和外部 DNS 记录；系统 DNS 记录不可单独删除 |
| Virtual WAN 网关 | VPN/ExpressRoute 网关、连接、VPN NAT 规则及链路 | 显式编排连接和 NAT 的前置删除；VPN 链路由连接管理 |
| [Service Bus](#service-bus-与-event-hubs) | 命名空间、队列、主题、订阅、规则、授权规则、灾难恢复别名、迁移配置、私有终结点连接 | 原生操作及已审查的命名空间/实体级联；迁移清理先中止复制再删除；配对别名先解除配对再删除 |
| [Event Hubs](#service-bus-与-event-hubs) | 专用集群、命名空间、事件中心、消费者组、授权规则、灾难恢复别名、架构组/应用组、私有终结点连接 | 集群清理先删除已审查的成员命名空间；支持命名空间/事件中心级联及配对别名解除配对 |
| 运维与身份 | Log Analytics 工作区、用户分配的托管身份 | 支持 |
| Log Analytics 表 | 各工作区的自定义表、还原表与搜索结果表 | 支持；内置 Azure Monitor 表属于工作区本身，不列出 |
| 事件网格 | 自定义主题、系统主题、命名空间与主题事件订阅 | 删除主题会同时删除其事件订阅；事件订阅也可以单独删除 |
| Azure 虚拟桌面 | 主机池、会话主机、应用程序组与工作区 | 先删除主机池的会话主机和应用程序组，再删除主机池；移除会话主机会保留其虚拟机 |
| HDInsight | 群集 | 支持；存储账户和虚拟网络作为独立资源保留 |
| 逻辑应用 | 消耗型工作流 | 支持；运行历史和触发器随工作流一起删除 |
| 自动化 | 自动化帐户 | 支持；Runbook、计划和作业随帐户一起删除。如仍有功能使用该帐户与 Log Analytics 工作区的链接，请先解除链接 |
| IoT 中心 | IoT 中心 | 支持；存储账户、事件中心等路由终结点作为独立资源保留 |
| SignalR 与 Web PubSub | SignalR 服务与 Web PubSub 服务 | 支持 |
| 应用配置 | 配置存储 | 支持；删除后的存储在保留期内可恢复，不出现在清单中；开启清除保护时，名称在保留期内保持占用 |
| 静态 Web 应用 | 静态 Web 应用 | 支持 |
| DNS 专用解析器 | 解析器、入站与出站终结点、转发规则集、转发规则及规则集虚拟网络链接 | 先删除终结点再删除解析器，先删除虚拟网络链接再删除规则集；转发规则随规则集删除，规则集先于其使用的出站终结点删除 |
| Azure 中继 | 命名空间、混合连接与 WCF 中继 | 删除命名空间会同时删除其中继；中继也可以单独删除 |
| 通知中心 | 命名空间与通知中心 | 删除命名空间会同时删除其通知中心；通知中心也可以单独删除 |
| Azure Databricks | 工作区 | 只读：删除工作区会同时删除其托管资源组，该级联尚未审查 |
| [托管 Grafana](#托管-grafana) | 工作区、托管专用终结点、专用终结点连接、集成配置 | 先删除已审查的子资源，再删除工作区；各资源也支持独立原生操作 |
| [Monitor 工作区](#azure-monitor-工作区) | 工作区及默认摄取托管资源组 | 审查组内全部资源，先解绑外部关联，再确认资源组和已知资源均已消失 |
| [Monitor 数据采集](#monitor-数据采集) | 规则、终结点及被监控资源上的关联 | 先删除已审查的关联，再删除规则或终结点；共享关联只删除一次 |
| [Monitor 专用链接](#azure-monitor-专用链接范围) | 全局专用链接范围、范围资源关联与专用终结点连接 | 先删除已审查的子资源，再删除范围；关联的工作区和终结点须先解除关联 |
| [Application Insights](#application-insights) | 组件、分析项、持续导出、收藏、工作项配置、API Key 和 Profiler 存储关联 | 先独立删除子资源并解除 AMPLS 关联，再删除已审查的当前托管工作区资源组 |
| [Azure Monitor 工作簿](#工作簿) | 共享工作簿、私有工作簿及工作簿模板 | 独立清理并核对完整内容和历史版本；引用的存储和身份保持独立 |
| [Azure Monitor 告警](#monitor-告警与预算) | 指标、活动日志、计划查询、智能检测、Prometheus 和处理规则；动作组与 Web 测试 | 独立清理；引用规则须经审查并先于共享 Monitor 目标删除 |
| [Azure RBAC](#azure-rbac) | 自定义和内置角色定义；订阅、资源组及资源范围的角色分配 | 符合条件的自定义角色与分配可独立删除；内置或跨范围共享角色、PIM 管理的分配及连接自身的分配保持受保护 |
| [诊断设置](#诊断设置) | 资源和订阅级设置，包括 Blob、File、Queue、Table 各自的服务范围 | 在其引用的源、目标或祖先之前删除；目标资源保持独立 |
| [预算](#monitor-告警与预算) | 订阅及资源组范围的 Consumption、Cost Management 预算 | 独立清理，通知动作组保持独立 |
| 管理组 | 租户中可见的管理组目录及其父级 | 只读；需要对要盘点的管理组具有 `Microsoft.Management/managementGroups/read` |
| Service Fabric 与存储同步 | Service Fabric 群集与存储同步服务 | 支持 |
| 机器学习 | 工作区与托管联机终结点 | 支持终结点清理；工作区作为只读父资源 |
| Purview 与托管应用程序 | Microsoft Purview 账户与托管应用程序 | 只读：删除它们会同时删除托管资源组，该级联尚未纳入审查 |
| Key Vault 密钥 | 通过 ARM 密钥 API 列出的密钥 | 只读：密钥删除是 ARM 未提供的数据面软删除 |
| Microsoft Entra 用户与组 | 通过 Microsoft Graph 读取的用户和组，含组的直接成员 | 只读：目录对象影响整个租户。需要 [Graph 权限](#数据面与目录权限)；仅保存显示名称、用户主体名称、账户状态、用户类型、组类型标记与创建时间 |
| Key Vault 证书 | 从各保管库数据面读取的证书，含保管库与托管密钥引用 | 只读：删除会同时软删除托管密钥和机密。需要[证书数据面权限](#数据面与目录权限) |
| Site Recovery | 恢复服务保管库中的复制保护项目，含复制结构、保护容器与保管库引用 | 只读：禁用复制会删除恢复端副本，尚未纳入审查；需要 `Microsoft.RecoveryServices/vaults/replicationFabrics/replicationProtectionContainers/replicationProtectedItems/read` |
| 尚待实现生命周期的集合资源 | 资源组、Key Vault、Container Apps 环境 | 只读：资源组、Key Vault 和 Container Apps 环境的清理尚未实现 |

**没有独立删除操作的资源。** Service Bus/Event Hubs 网络规则集、Event Hubs 网络边界配置、灾难恢复别名的授权视图、Uniform 伸缩集网络资源和 VPN 连接链路不能单独删除。默认命名空间授权规则 `RootManageSharedAccessKey` 也只能随命名空间删除。这些资源会纳入所属控制资源的已审查删除影响；保留其中任何一个，都会阻止删除所属控制资源。

## 清理保护

本节说明 Steward 删除 Azure 资源前会检查什么，以及删除会带来什么影响。先阅读适用于所有产品的[通用保护](#通用保护)，再查找对应产品的小节。

执行任务前，请审查[清理选择与结果](./cleanup.md)。删除数据库、容器注册表或存储资源可能删除其中的数据。Azure 权限、保留策略、依赖关系和并发变更仍可能阻止操作。参阅微软的[管理锁](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/lock-resources)、[VM 删除设置](https://learn.microsoft.com/en-us/azure/virtual-machines/delete)和[异步操作说明](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/async-operations)。

| 领域 | 产品 |
| --- | --- |
| 计算与容器 | [Azure Batch](#azure-batch)、[Container Instances](#container-instances)、[Kubernetes Fleet Manager](#kubernetes-fleet-manager)、[App Service](#app-service)、[App Service 域名](#app-service-域名) |
| 混合云 | [Azure Arc](#azure-arc)、[Azure Local](#azure-local) |
| 网络与分发 | [CDN 与 Front Door](#cdn-与-front-door)、[WAF 策略](#waf-策略) |
| 存储 | [Elastic SAN](#elastic-san)、[Azure NetApp Files](#azure-netapp-files) |
| 数据库与分析 | [Cosmos DB](#cosmos-db)、[Azure DocumentDB](#azure-documentdb)、[Azure Data Explorer](#azure-data-explorer)、[Redis](#redis)、[Azure Synapse Analytics](#azure-synapse-analytics)、[Data Factory](#data-factory)、[Data Migration](#data-migration)、[Stream Analytics](#stream-analytics) |
| AI | [Foundry 与 Cognitive Services](#foundry-与-cognitive-services)、[Azure AI Search](#azure-ai-search) |
| 消息与通信 | [Service Bus 与 Event Hubs](#service-bus-与-event-hubs)、[API Management](#api-management)、[Communication Services](#communication-services) |
| 监控 | [Monitor 告警与预算](#monitor-告警与预算)、[Azure Monitor 工作区](#azure-monitor-工作区)、[Monitor 数据采集](#monitor-数据采集)、[Azure Monitor 专用链接范围](#azure-monitor-专用链接范围)、[Application Insights](#application-insights)、[工作簿](#工作簿)、[托管 Grafana](#托管-grafana)、[诊断设置](#诊断设置) |
| 安全与治理 | [Azure RBAC](#azure-rbac)、[Defender for Cloud](#defender-for-cloud) |

### 通用保护

**删除前**

- **管理锁**：订阅、资源组、资源本身及相关子资源的锁会阻止删除。Steward 在盘点时和实际删除前都会检查锁，从不移除锁。
- **保护标签**：带有 `steward/protected` 或 `steward:protected` 标签、且值为 `true`、`1`、`yes`、`on` 或 `protected` 的资源不会被清理。各产品小节中的“保护标签”即指此标签。
- **连接自身的访问权**：授予连接所用主体的角色分配始终受保护，因为删除它们会在清理途中撤销 Steward 自身的访问权。若 Steward 无法从令牌中读出该主体的对象 ID，则所有角色分配都受保护。
- **托管资源**：云服务拥有的资源只能通过受支持的控制资源清理，具有明确独立删除能力的成员除外。AKS 删除包含经过审查的节点资源组；仍禁止任意删除托管资源组中的资源。
- **网络占用**：删除前立即检查：
  - 子网不能有 IP 配置、专用终结点、服务关联链接或资源导航链接、应用程序网关 IP 配置或 IP 配置文件；
  - 网络安全组、路由表或 NAT 网关不能关联子网或网络接口；
  - 公共 IP 地址不能附加到 IP 配置或 NAT 网关。

  同一任务中先删除的占用方可满足该检查；服务关联链接须由其所属服务移除。仅有委派不会阻止子网删除。
- **承载工作负载的网卡**：`hostedWorkloads` 非空的网卡不能直接清理，也不能被会删除或改写它的 VM 级联清理处理。格式异常的工作负载信息同样受保护；字段缺失、为 null 或为空数组本身不构成工作负载关联。清理时会重新读取网卡，扫描后新增的工作负载也会阻止执行。这些信息不会授予任何控制资源删除网卡的权限；NetApp 卷组的网卡归属及自动删除仍需独立的生命周期审查（见 [Azure NetApp Files](#azure-netapp-files)）。
- **VNet 与 DNS 区域**：必须先移除必要的子网和私有 DNS 链接。选择 VNet 不会隐式删除未选择的子网或链接。
- **VM 挂载资源**：计划展示会随 VM 由 Azure 自动删除的磁盘、网卡和公网 IP。支持的保留操作在删除前通过带条件的原生更新完成，并支持工作进程重启恢复。VM 扩展属于 VM 的删除影响。Uniform 伸缩集的非托管 VHD 清理和磁盘解除挂载尚未实现。
- **存储账户与 Blob 容器**：存储账户对应的服务必须没有 Blob 容器、文件共享、队列或表。Blob 容器不能含有 Blob、快照、版本、软删除条目或未提交上传。法律保留与不可变策略会阻止清理。Steward 不会清空或永久清除数据来满足删除条件。Blob 清理目前要求使用标准 `ACCOUNT.blob.core.windows.net` 端点，并需要[数据面权限](#数据面与目录权限)。
- **App Service Plan**：删除应用时保留其 App Service Plan；需要删除计划时，应单独选择。

**其他资源的引用**

仍有其他资源引用目标、且引用方不在任务中时，删除会被阻止。把引用方加入任务（它会先被删除），或在 Azure 中移除引用后重新扫描。以下检查覆盖整个订阅：

| 引用方 | 阻止删除的对象 | 详见 |
| --- | --- | --- |
| 角色分配与自定义角色 | 作用域资源及其祖先，以及分配中出现的托管身份和具有系统分配身份的资源 | [Azure RBAC](#azure-rbac) |
| 诊断设置 | 其源、目标或祖先 | [诊断设置](#诊断设置) |
| 告警规则与预算 | 其引用的动作组或 Monitor 资源 | [Monitor 告警与预算](#monitor-告警与预算) |
| AMPLS 关联 | Application Insights 组件、Log Analytics 工作区和数据收集终结点 | [Azure Monitor 专用链接范围](#azure-monitor-专用链接范围) |
| Kubernetes Fleet 成员与配置 | AKS 集群、子网和用户分配身份 | [Kubernetes Fleet Manager](#kubernetes-fleet-manager) |
| Data Migration 迁移 | 其 SQL 或 Cosmos DB 目标，或包含目标的资源 | [Data Migration](#data-migration) |

**删除中与删除后**

- **并发变更**：删除前重新核对原生创建标识；已审查的服务资源树还会核对版本、成员清单、锁与保护设置。资源被重建、出现未审查的子资源，或已审查的配置、保护、锁发生变化时，需要重新扫描并生成计划。许多 Azure DELETE 接口没有条件（If-Match）版本保护，包括 Azure Arc、Data Migration、Data Factory、Communication Services、Azure RBAC、诊断设置、托管 Grafana、AMPLS、工作簿和注册域名，因此重复校验也无法排除最后一次检查与删除之间发生的外部修改。
- **异步操作**：Steward 跟踪 ARM 返回的操作状态，再重新读取资源确认其不存在。失败或取消的操作仍记为失败，不启用强制删除或永久清除选项。
- **完成判定**：只有资源自身的读取报告不存在（例如 HTTP 404），步骤才算完成。DELETE 被接受、操作回调成功或过期、父资源消失、列表中找不到都不够。父资源消失后，每个已记录的后代及必要的使用方同样需要各自确认。
- **工作进程重启**：已接受的操作及其进度会保存，工作进程重启后 Steward 继续轮询和核验。各产品小节会注明哪些重启恢复不会重复发送 DELETE。
- **带着未完成任务升级**：清理恢复会同时核对完整的已审查请求和已保存的操作回执。从未包含此检查的版本升级前，请先完成进行中的清理任务：旧任务的回执无法通过新的恢复校验。发起新的清理需要重新扫描并生成计划。

**验证状态**

Azure 覆盖范围使用原生 HTTP 协议测试和未修改的微软官方响应样例进行离线测试。托管 Grafana 和 Monitor 数据采集还重放了微软 CLI 录制的删除响应，包括签名操作 URL 和延迟完成；较早的录制 API 版本及补充的最终不存在响应均在测试证据中说明。除非产品小节另有说明，这不代表 Steward 完成了独立模拟器或真实云验证。

### Azure Batch

**盘点。** 账户、池、节点、作业、计划、任务、应用、包版本、专用终结点连接及网络边界视图。作业、计划、任务和节点通过账户的 Batch 端点、使用独立的 Batch 令牌读取。

**权限。**

- 所选账户资源的 ARM 权限。
- Batch 数据权限，清理可使用 **Azure Batch Data Contributor**（见[数据面与目录权限](#数据面与目录权限)）。
- 存储及密钥 URL 引用：订阅级 Storage/Key Vault 列表权限，以及匹配资源的读取权限。
- 用户订阅模式的节点：读取其 VM、磁盘和网络资源的 Compute/Network 权限。

**清理。**

- 清理会审查完整账户层级。池、应用、专用终结点连接、作业及计划按依赖顺序删除；包版本先于所属应用删除；网络边界视图随账户清理。
- 删除作业或计划包含已审查的任务，删除池包含已审查的节点。
- 自动池只有在实际生命周期设置与成员关系共同证明归属时，才随作业或计划清理。
- 共享池、包及任务依赖需要明确选择其使用方的清理范围。
- **单独移除节点**：使用池的最新 ETag，并将节点上运行中的任务重新排队。任务曾在该节点运行过，不会要求删除你要保留的任务记录。
- **用户订阅模式的节点**：审查文档指向的 Uniform VMSS 实例及其磁盘、扩展和网络资源。VM 身份缺失、磁盘共享或已分离、子资源被保留或受保护，以及配置漂移都会阻止清理。所属伸缩集保留。
- **多实例任务**：先终止任务并等待全部子任务，再在删除后核验其工作目录；仅主任务记录消失不足以完成清理。

**限制。**

- Batch 的原生删除会忽略任务数据保留期。
- 外部存储、Key Vault 和身份保持独立引用；解析引用时从不读取文件内容、存储密钥或保管库内的机密与密钥内容。
- 验证包括原生 Schema、组合 HTTP 场景、官方 CLI 响应回放、应用关系图检查及重启测试，不代表独立 Batch 模拟器或真实 Azure 部署验证。

参见[任务删除](https://learn.microsoft.com/en-us/rest/api/batchservice/tasks/delete-task?view=rest-batchservice-2025-06-01)、[节点移除](https://learn.microsoft.com/en-us/rest/api/batchservice/pools/remove-nodes?view=rest-batchservice-2025-06-01)与[应用包](https://learn.microsoft.com/en-us/azure/batch/batch-application-packages)。

### Container Instances

**盘点。** 容器组，支持原生盘点与删除。子网、托管身份和返回的 Log Analytics 资源 ID 作为引用记录。命令、配置值和凭据不会写入清单或日志。

**清理。**

- 普通容器和初始化容器共享容器组的生命周期，随容器组一起删除。外部 Azure Files 文件共享保持独立。
- Azure 返回的配置必须与审查时一致，敏感值通过与连接绑定的摘要比较；轮换凭据后需要重新扫描。
- 删除返回 HTTP 200 后，仍须原生读取确认容器组已消失。

参见[容器组删除约定](https://learn.microsoft.com/en-us/rest/api/container-instances/container-groups/delete?view=rest-container-instances-2025-09-01)。

### Kubernetes Fleet Manager

**盘点。**

- Fleet、AKS 和 Arc Kubernetes 成员、托管命名空间、更新运行、更新策略、自动升级配置、Gate 与跨集群网络（Cluster Mesh 配置）。
- 代理子资源使用 Fleet 所在地域，托管命名空间保留原生地域。
- 更新运行保留策略副本，Gate 指向其所属运行。
- 动态命名空间放置会明确标记，不声称已验证实际成员集合。命名空间注释和放置表达式不进入清单与日志。
- 多数 Fleet 操作使用稳定版 API `2026-06-01`；成员读取和 Cluster Mesh 操作使用 `2026-06-02-preview`，以核验实际网络成员关系。
- Fleet 被列表遗漏或已不存在，都不能证明子资源已删除；已知子资源会逐个核验。
- **Hub 集群**：Fleet 根资源扫描通过托管资源组的 `managedBy`、Fleet 与 AKS 的 API 地址，以及 AKS 的 `nodeResourceGroup` 和该组反向指向 AKS 的归属信息，核验 Hub 归属。随后遍历两个资源组，展开原生子资源和有文档支持的外部后代，并补查通用 ARM 列表遗漏的 Monitor 资源。归属缺失或有歧义时保留为未核实状态，命名规则不能证明归属。已知资源被列表遗漏时通过逐项读取找回；未知类型的资源被遗漏，或外部归属证据不完整时，扫描不会完成。Fleet 记录仅保存 Hub 和成员配置的私有摘要；各资源保留其产品的正常清单字段。

**权限。**

| 任务 | 权限 |
| --- | --- |
| 盘点 | `Microsoft.ContainerService/fleets/read`、七类子资源的读取权限，以及资源组和管理锁读取权限 |
| Hub 扫描 | 未过滤的资源组和组内资源列表、成员原生读取及子资源列表，以及相关的订阅 Monitor 和 DNS 索引权限 |
| 删除 AKS 集群、子网或用户分配身份 | Fleet 读取权限：这些检查会读取订阅中的 Fleet 集合，包括尚未进入清单的 Fleet |
| 删除成员、命名空间、更新运行、策略或自动升级配置 | 对应子资源的删除权限；状态为 `Running`、`Pending` 或 `Skipped` 的更新运行还需要停止权限 |
| 删除 Cluster Mesh 配置 | 配置的读取、写入、Apply、删除权限，以及成员读取权限 |
| 删除 Fleet | `Microsoft.ContainerService/fleets/delete` |

**清理。**

- **成员、托管命名空间、更新运行、更新策略和自动升级配置**使用原生条件删除。
- **更新运行**：运行中、待处理或已跳过的运行会先停止；已经处于停止过程中的运行不会重复发送 Stop。系统等待运行进入终止状态后再删除，并确认运行及已审查的 Gate 均已消失。Gate 随所属更新运行删除。轮询和执行阶段可在工作进程重启后恢复。
- **托管命名空间**沿用已审查的 `deletePolicy`：`Keep` 移除 ARM 管理并保留 Kubernetes 命名空间；`Delete` 删除 Hub 和成员集群上的命名空间及其内容。两种策略都会删除关联的 Azure RBAC 分配。策略或放置配置变化后，需要重新扫描和审查。
- **成员**：移除成员只解除成员关系，不会删除其引用的 AKS 或 Arc Kubernetes 集群。Arc 集群及其 Kubernetes 扩展不归 Fleet 所有。存在动态命名空间放置时，会保守地要求先清理命名空间，再移除该 Fleet 中的任何成员。已连接 Cluster Mesh 配置的成员，须先清理该配置。跨订阅或清单中缺失的集群保留为待解析引用。
- **Cluster Mesh（跨集群网络）**：先断开已审查的跨集群网络，再删除配置，因此会中断跨集群连接和服务发现。连接关系取自成员实际的 `meshProperties`，通过未过滤的原生列表及逐项读取获得；标签匹配不能证明实际连接。清理会等待正在进行的 Apply 完成，在不改动其他配置的前提下将成员选择器设为空，应用断开操作，确认没有成员仍然连接。成员受保护或加锁、新增连接、配置变化及无法读取的残留连接都会阻止推进。最终以配置自身的 404 和已知连接的逐项核验确认完成。成员及其集群保留。
- **Fleet**：已核验 Hub 归属的 Fleet，或已确认不含 Hub 的 Fleet，支持根资源清理。选择 Fleet 后，计划会为其原生子配置生成独立的已审查删除步骤；逐项确认它们不存在后，才发送 Fleet DELETE。Steward 会核验完整的 Hub 影响范围、归属、配置、保护设置和管理锁。Hub 集群、两个托管资源组及其所属后代统一归属 Fleet（不再归属 AKS 或挂载资源控制器），只通过 Fleet DELETE 删除。Fleet 消失后，两个资源组及每个已知类型的后代仍须各自确认 404，包括外部托管磁盘和 DNS 资源；未知类型的组内资源依赖所属组确认不存在。要求保留资源、Hub 归属未核实或残留资源无法读取时，清理不能完成。共享资源及加入 Fleet 的成员集群保持独立。
- **Fleet 的引用**：仍有 Fleet 引用时，会阻止删除 AKS 集群、子网或用户分配身份。这些检查不会让 Fleet 取得成员集群或共享网络的所有权。
- RBAC 分配和诊断设置仍需各自独立清理。

**限制。** 清单中缺失的资源保留为待解析引用。归属证明或原生状态变化后，需要重新扫描才能清理。

参阅 [Fleet 常见问题](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/faq)、[Hub 集群说明](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/concepts-lifecycle)、[跨集群网络删除说明](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/howto-configure-use-cross-cluster-networking#delete-a-cross-cluster-network)、[支持的成员类型](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/quickstart-create-fleet-and-members)、[命名空间删除说明](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/howto-managed-namespaces#delete-a-managed-fleet-namespace)和[更新运行状态](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/concepts-update-orchestration#update-run-states)。

### App Service

**盘点。** Web App 与 Function App、部署槽、函数、证书、主机名绑定和服务计划。

**权限。** 子资源的原生读取、列举权限，以及所选操作的删除权限。删除证书还需要读取所有应用及部署槽的 TLS 状态和主机名绑定。

**清理。**

- 删除应用或部署槽会包含已审查的部署槽、函数、应用证书和主机名绑定。保留子资源会阻止应用或部署槽删除；默认主机名只能随所属应用或部署槽清理。
- App Service Plan 保持独立；需要删除时请单独选择。
- 只要有绑定匹配证书 ID 或指纹，证书就不能删除。请先移除相关绑定并重新扫描，再删除证书。
- 应用通过部署包运行时，单个函数可能无法删除；Steward 会原样展示 Azure 返回的错误。

参见[应用删除契约](https://learn.microsoft.com/en-us/rest/api/appservice/web-apps/delete?view=rest-appservice-2025-05-01)与[部署包行为](https://learn.microsoft.com/en-us/azure/azure-functions/run-functions-from-deployment-package)。

### App Service 域名

**盘点。** 注册域名及其所有权标识，显示在全局清单。联系人信息、转移授权及所有权令牌值不进入清单与日志。

**权限。** 域名和标识的原生列举、读取权限，订阅范围的 App Service 列表、应用/部署槽详情与主机名绑定读取权限，以及资源组和管理锁读取权限。域名、标识和绑定的删除权限仅为已审查的步骤授予。

**清理。**

- 选择域名后，计划先删除其所有权标识及关联的应用/部署槽主机名绑定；应用、证书、服务计划和 DNS 区域保持独立。
- 删除仍被注册域名引用的 DNS 区域时，须同时选择该域名，或先更改其 DNS 托管并重新扫描。
- 删除域名会释放注册，其他人随后可能购买该名称。Steward 保留 Azure 的购买锁，并使用 `forceHardDeleteDomain=false`，因此适用 Azure 原生的 24 小时删除延迟。Steward 最长等待 48 小时，工作进程重启后继续等待。
- 已删除、已审查的应用绑定若仍出现在主机名索引中，系统会继续等待；未知分配仍会阻止删除。域名及每个已知依赖都须各自读取确认不存在。
- 配置变化后须重新扫描和生成计划。域名 DELETE 不提供 If-Match 条件。

参阅微软的[域名管理与取消说明](https://learn.microsoft.com/en-us/azure/app-service/manage-custom-dns-buy-domain)及已固定版本的 [DomainRegistration API 契约](https://github.com/Azure/azure-rest-api-specs/blob/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/domainregistration/resource-manager/Microsoft.DomainRegistration/DomainRegistration/stable/2024-11-01/openapi.json)。

### Azure Arc

**盘点。** 机器和共享 ESU 许可证，以及每台机器下的扩展、运行命令和许可证配置。已知资源及列表遗漏的父机器会逐项补读；不完整响应或权限不足会使扫描失败。脚本、扩展设置、受保护参数和代理设置不会进入公开清单及 API 日志。

**权限。**

| 任务 | 权限 |
| --- | --- |
| 盘点 | 按所选类型授予 `Microsoft.HybridCompute/machines/read`、`Microsoft.HybridCompute/machines/extensions/read`、`Microsoft.HybridCompute/machines/runCommands/read`、`Microsoft.HybridCompute/machines/licenseProfiles/read` 和 `Microsoft.HybridCompute/licenses/read`。子资源扫描需要订阅范围的机器列表和读取权限；机器扫描需要三类子集合的读取权限，仅扫描机器时也不例外。 |
| 删除扩展、运行命令或许可证配置 | `Microsoft.HybridCompute/machines/extensions/delete`、`Microsoft.HybridCompute/machines/runCommands/delete` 或 `Microsoft.HybridCompute/machines/licenseProfiles/delete`；子资源和机器的原生读取；资源组列表与读取；管理锁读取；以及适用的 Monitor、诊断设置、RBAC、Fleet 和 Data Migration 依赖读取权限 |
| 删除机器注册 | `Microsoft.HybridCompute/machines/delete`，以及三类子集合的读取权限 |
| 删除共享 ESU 许可证 | `Microsoft.HybridCompute/licenses/delete`、许可证读取、订阅范围的机器列表和读取、机器许可证配置列表和读取，以及上述资源组、锁和引用依赖读取权限 |

**清理。**

- **扩展、运行命令和许可证配置**：引用它们的规则需要先审查并移除。删除前会核验机器注册、子资源私有配置、保护标签和归属关系。只有子资源自身的 GET 返回 404 才算完成，机器消失或异步操作成功都不够。计划会提示：
  - 删除运行中的 Run Command 会终止其脚本；
  - 扩展移除需要另行核实代理端结果；
  - 删除许可证配置会改变机器的许可配置，共享许可证仍保留；许可证配置消失不能证明计费已终止。
- **机器注册**：普通 Arc 机器注册（类型为空、AWS 或 GCP）可在已审查的扩展、运行命令和许可证配置删除后清理。子资源清单缺失时，需先重新扫描再生成计划；保留子资源会阻止机器删除。即使机器返回 404，已知和已审查的子资源仍会逐项读取。计划会提示：移除云端注册后，外部主机和本地代理仍需单独移除。
- **受保护的注册**：无 VM 归属证据的 HCI 主机、VMware、SCVMM、AVS、EPS、未知类型及其他关联父集群的注册仍受保护；控制器托管的机器只能随控制器处理。Azure Local VM 的 HCI 注册需要已验证的 VM 上下文，并遵循 [Azure Local](#azure-local) 的清理顺序。
- **共享 ESU 许可证**：使用该许可证的配置必须选中清理，或先单独解除关联；仅删除配置或机器会保留共享许可证。删除前，许可证的原生分配计数必须存在且为零。许可证可覆盖同一租户的其他订阅，因此本地配置列表为空不足以证明无关联；外部分配需在对应订阅中处理。计划会提示删除将移除许可权益，计费可能继续最多五个日历日。许可证返回 404 后，仍会检查已保存和已审查的配置引用，残留分配会阻止完成。

**限制。**

- 原生 DELETE 不支持 If-Match 条件。
- 测试覆盖原生协议回放，以及扫描、关系图、计划和恢复执行链；真实代理移除尚未验证。
- ESU 许可证测试包含微软 CLI 原始 DELETE 响应和恢复执行，不能据此确认计费已实际终止。

参见微软的[代理移除指南](https://learn.microsoft.com/en-us/azure/azure-arc/servers/uninstall-agent)、[断开连接与 Azure Local 删除说明](https://learn.microsoft.com/en-us/azure/azure-arc/servers/azcmagent-disconnect)、[ESU 许可范围](https://learn.microsoft.com/en-us/azure/azure-arc/servers/license-extended-security-updates)及 [ESU 计费行为](https://learn.microsoft.com/en-us/azure/azure-arc/servers/billing-extended-security-updates)。

### Azure Local

**盘点。**

- 通过原生接口盘点 VM 实例、访客代理、访客身份元数据、网卡、磁盘、逻辑网络、存储路径与镜像。已知资源逐项补读；集合为空或缺失时，也会检查固定的 `default` 单例资源。访客资源继承经过核验的 Arc 机器地域。
- 权限不足、扫描期间配置变化或引用格式错误会使扫描失败，不会关闭已有记录。
- 公开字段包含 VM 容量与电源状态、网络地址、磁盘和镜像信息、存储容量；凭据、SSH 密钥、代理配置及本地路径保持私密。
- **逻辑网络**使用 API 版本 `2025-06-01-preview` 读取原生只读字段 `networkType`，区分工作负载网络（`Workload`）和基础设施网络（`Infrastructure`）；字段缺失或无法识别时显示 `Unknown`。名称、标签或网卡列表为空都不能证明网络类型。权限不足或不支持该 API 版本时，扫描失败且不会关闭已有记录。其他 Azure Local 资源仍使用 `2024-01-01`。
- **网络扫描**沿网卡与 VM 引用纳入访客资源和挂接的虚拟磁盘。系统会重新读取已保存的挂载证据，补回列表遗漏的已知 VM；磁盘解除挂载后，网络引用中会移除该 VM。存储路径与镜像仍是独立引用。网络归属不构成反向依赖，也不授予删除所有权。
- **关系**区分机器、VM 实例、网卡、磁盘、逻辑网络、存储路径、镜像和自定义位置的引用。缺失或跨订阅目标保留为未解析引用，普通引用关系不授予删除所有权。

**权限。** 以下每种清理还需要资源组和管理锁读取权限。

| 任务 | 权限 |
| --- | --- |
| 盘点 | VM 和访客资源发现需要 `Microsoft.HybridCompute/machines/read`，并按所选类型授予对应的 `Microsoft.AzureStackHCI/<资源类型>/read`，包括 `virtualMachineInstances/guestAgents/read` 和 `virtualMachineInstances/hybridIdentityMetadata/read`。在扫描对话框中列出 Local 网络需要 `Microsoft.AzureStackHCI/logicalNetworks/read`。 |
| 网络扫描中的磁盘归属 | `Microsoft.AzureStackHCI/virtualMachineInstances/read` 和 `Microsoft.HybridCompute/machines/read` |
| 删除 VM | `Microsoft.AzureStackHCI/virtualMachineInstances/delete`。即使只扫描 VM，发现和审查阶段也会读取两类 Local 访客单例、引用的系统盘，以及 Arc 扩展、运行命令和许可证配置集合。访客/Arc 前置步骤需要各自的删除权限。 |
| 删除访客代理 | `Microsoft.AzureStackHCI/virtualMachineInstances/guestAgents/delete`；访客资源、VM 实例和 Arc 机器的读取权限 |
| 删除 Arc 注册 | `Microsoft.HybridCompute/machines/delete`。HCI 机器扫描还会读取原生 VM 实例、两类 Local 访客单例、已登记的系统盘和三类 Arc 子集合。 |
| 删除磁盘或网卡 | `Microsoft.AzureStackHCI/virtualHardDisks/delete` 或 `Microsoft.AzureStackHCI/networkInterfaces/delete`；对应资源读取；订阅范围的 Arc 机器和 Local VM 列表/读取 |
| 删除镜像 | `Microsoft.AzureStackHCI/galleryImages/delete` 或 `Microsoft.AzureStackHCI/marketplaceGalleryImages/delete`；对应镜像读取。仅扫描或清理镜像不需要 VM 或 Arc 注册读取权限。 |
| 删除存储路径 | `Microsoft.AzureStackHCI/storageContainers/delete`；存储路径读取；订阅范围的磁盘、库镜像和市场镜像列表/读取；Arc 机器和 Local VM 列表/读取 |
| 删除逻辑网络 | `Microsoft.AzureStackHCI/logicalNetworks/delete`（使用 `2025-06-01-preview`）；订阅范围的逻辑网络和网卡列表/读取；`Microsoft.Kubernetes/connectedClusters/read`；`Microsoft.HybridContainerService/provisionedClusterInstances/read`。基础设施网络还需要 Arc 机器和 Local VM 列表/读取权限。 |

系统会检查 VM 及所有受影响资源的资源组保护和管理锁，包括位于其他资源组的磁盘。

**清理。**

- **VM**：先移除已审查的访客与 Arc 前置资源，再调用原生 VM DELETE。系统盘纳入 VM 删除影响：[微软更正后的支持答复](https://learn.microsoft.com/en-us/answers/questions/5758576/what-happen-with-associated-data-disk-with-azure-l)称，工程团队确认系统盘会随 VM 删除，而数据盘保留。保留或保护系统盘、访客资源或 Arc 前置资源会阻止 VM 删除。清理前会通过原生机器/VM 读取检查系统盘是否被其他 VM 使用，也会补读此前观察到但本次父级列表遗漏的 VM。只有 VM、系统盘与身份元数据各自确认不存在，动作才会完成；身份元数据和系统盘在 VM 删除后通过各自的 GET 核验。若原生 VM 响应没有已登记系统盘的 ID，就没有可单独核验的磁盘记录，VM 删除提示仍会说明系统盘将被移除。仅选中 VM 时保留其 Arc 注册；关联网卡和数据盘保留，需要单独清理。清理提示会区分系统盘删除、保留的数据盘/网卡以及单独执行的 Arc 注册清理。
- **Local VM 的 Arc 注册**：选中注册时，系统会按官方 CLI 的顺序先完成 VM 清理，再原生删除注册。注册删除在 VM 自身清理完成后执行。VM 消失后，已核验的 VM 上下文仍绑定原注册，后续扫描可以继续完成注册清理；从未获得已验证 VM 上下文的 HCI 主机仍受保护。注册被替换、读取不可用、VM 被保留或保护，以及 VM、访客、身份元数据或系统盘仍存在，都会阻止删除或完成。网卡和数据盘仍是独立资源。
- **访客代理**：已扫描并核验 HCI 注册和 VM 配置后，可单独清理。清理会重新核验已审查配置、保护状态与继承锁。计划会提示访客管理可能中断，VM、Arc 注册和身份元数据仍保留。删除 ARM 资源并不证明访客内的代理已移除。
- **磁盘和网卡**：全部原生 VM 引用解除后可单独清理。仍在使用该资源的 VM 必须选中并先删除，或通过原生管理工具解除挂接后重新扫描；仅选择磁盘或网卡不会自动选择 VM 删除。系统盘仍作为 VM 的受管删除影响。扫描保留已验证的 VM 身份，用于补回列表遗漏的 VM，但不会因此把它们加入网络成员关系。保护、配置变化、读取不可用或新增使用方会阻止清理。完成要求资源自身不存在且 VM 引用已解除，重启恢复、同步 204 或 DELETE 404 后也是如此。删除磁盘可能永久移除数据。
- **库镜像和市场镜像**：根据 [Azure Local 官方 FAQ](https://learn.microsoft.com/en-us/azure/azure-local/manage/azure-arc-vms-faq)，删除源镜像后，已部署 VM 持有的副本不受影响。仅选中镜像时生成一个清理步骤，没有 VM 前置删除或受管影响；同时选中使用它的 VM 时，VM 会先清理。删除前检查已审查的镜像配置、ETag、保护标签和锁。完成需要镜像自身不存在的证据，DELETE 返回 404 时也一样。
- **存储路径**：系统根据原生 `containerId` 和 `vmConfigStoragePathId` 引用识别路径上的工作负载。这些位置字段可省略，因此没有返回存储位置的资源会按可能的使用方处理；请补齐其位置证据，或明确审查并先清理。已知磁盘、镜像 ID 和 VM 范围会在列表遗漏及后续扫描中保留。使用或可能使用该路径的工作负载必须选中并先删除，或在 Steward 之外移除；仅选中路径不会自动选中它们。只有原生生命周期声明并核验系统盘随 VM 删除时，已纳入计划的 VM 才能满足系统盘的前置要求；这项系统盘影响在清单变化和工作进程重启后仍保留，不生成单独的系统盘 DELETE。删除路径前和完成时都会逐项确认工作负载已消失，路径已返回 404 时也不例外。该操作不会请求删除卷。
- **逻辑网络**：清理前必须核验网络类型与自定义位置；类型为 `Unknown` 时网络保持受保护。
  - 工作负载网络检查网卡引用和原生 AKS `vnetSubnetIds`。
  - 基础设施网络检查同一已核验自定义位置上的 VM、网卡、其他逻辑网络及 AKS 实例；须先移除实例上的 VM、网卡和工作负载网络。删除仅移除云端投影，本地网络仍保留。
  - 位置或引用字段缺失时按可能占用处理：补齐证据，或先移除使用方。使用方必须选中并先清理，或在 Steward 之外移除；仅选中网络不会自动选中其工作负载。
  - AKS 预配实例作为未解析的原生依赖跟踪，需先通过原生工具移除，网络才能清理；此流程不删除 AKS。
  - 已知使用方 ID 在列表遗漏和 Arc 父注册消失后仍保留。原生子网的 `ipConfigurationReferences[].ID` 独立于网卡列表核验，残留引用仍会阻止清理。
  - DELETE 前及网络自身返回 404 后，都会重新读取保护状态、配置、锁和存活依赖。预览 SDK 的 `Location` 轮询状态会保存，恢复时不会重复发送 DELETE。

**限制。** 这些流程均为离线测试：原生 SDK 契约（必要时使用桩）、组合 VM/Arc/网络协议、网络筛选、保护、保留与引用变化，以及带进程重启恢复的扫描、关系图、计划和执行链。尚未进行真实验证的包括：真实控制器及 VM、磁盘、镜像、存储和网络的物理移除，与真实操作回调的兼容性，以及计费终止。

参见微软的 [Azure Local 虚拟机管理](https://learn.microsoft.com/en-us/azure/azure-local/manage/manage-arc-virtual-machines?view=azloc-2607)、[逻辑网络管理说明](https://learn.microsoft.com/en-us/azure/azure-local/manage/manage-logical-networks?view=azloc-2604)与 [API 变更记录](https://learn.microsoft.com/en-us/azure/templates/microsoft.azurestackhci/change-log/logicalnetworks)、[存储路径移除顺序](https://learn.microsoft.com/en-us/azure/azure-local/manage/create-storage-path?view=azloc-2606)，以及原生[磁盘删除](https://learn.microsoft.com/en-us/rest/api/stackhci/virtual-hard-disks/delete?view=rest-stackhci-2024-01-01)、[网卡删除](https://learn.microsoft.com/en-us/rest/api/stackhci/network-interfaces/delete?view=rest-stackhci-2024-01-01)和[存储路径删除](https://learn.microsoft.com/en-us/rest/api/stackhci/storage-containers/delete?view=rest-stackhci-2024-01-01)契约。

### CDN 与 Front Door

**盘点。** CDN 与 Front Door 配置，按 SKU 读取各自的原生子资源集合：经典终结点、源站、源站组和域名；Front Door 终结点、路由、源站组、源站、域名、规则集、规则、安全关联和证书引用。Front Door 批量模式的规则显示在所属规则集内。

**权限。** 配置及相关子集合的原生读取、列举权限（包括引用方的路由和安全关联），以及所选资源的删除权限。

**清理。**

- **配置**：清理前审查全部所属资源，保留级联成员会阻止删除。删除整个配置时，同一次已审查的级联可以一并移除其内部引用。
- **单独删除域名、源站组、规则集或证书引用**时，计划会加入必须先删除的路由、规则或关联；多个目标共用的前置删除只执行一次。
- 经典终结点仍在引用源站组时，须先更新路由或选择清理该终结点，才能删除源站组。
- **批量模式规则**随所属规则集保留或删除：要保留这些规则，请保留整个规则集。批量规则中的源站组覆盖配置会将引用它的规则集加入必要删除项，使用该规则集的路由也必须先删除。经典模式规则仍可单条删除，执行前会重新核对父规则集。
- 外部源站、Key Vault 数据、DNS 区域和 WAF 策略保持独立。
- 签名异步操作以及资源和子资源的最终不存在核验支持重启恢复。

参见原生[配置删除契约](https://learn.microsoft.com/en-us/rest/api/cdn/profiles/delete?view=rest-cdn-2025-04-15)、[规则集清理说明](https://learn.microsoft.com/en-us/azure/frontdoor/standard-premium/how-to-configure-rule-set)和微软的[批量规则管理指南](https://learn.microsoft.com/en-us/azure/frontdoor/rule-set-batch)。

### WAF 策略

**盘点。** CDN 与 Front Door 的 WAF 策略，以及引用它们的终结点或安全关联。

**权限。** 盘点需要策略与引用方的读取权限；清理还需要各自的删除权限及操作状态查询权限。

**清理。**

- 删除策略也会删除其内嵌规则。
- 引用该策略的 CDN 终结点或 Front Door 安全关联必须先经审查并删除；保留引用方会阻止策略删除。
- 经典 Front Door 前端或路由引用仍会阻止清理，需在 Steward 之外解除。
- 删除前会重新核对策略配置、锁和全部剩余关联。

参见 [Front Door 策略删除契约](https://learn.microsoft.com/en-us/rest/api/frontdoorservice/webapplicationfirewall/policies/delete?view=rest-frontdoorservice-webapplicationfirewall-2025-11-01)。

### Elastic SAN

**盘点。**

- SAN、卷组、卷、快照与私有终结点连接，使用 API 版本 `2026-04-01-preview`。Steward 分别读取活动和软删除（保留）的卷及卷组列表。保留中的资源仍以原生 ID 显示；恢复卷时 ID 可能改变，但 `volumeId` 保持不变。活动列表为空不能证明已永久移除。
- 响应会校验资源身份、记录格式和分页；外部资源、格式错误的页面、非终态响应及不安全的续页链接会导致调用失败。
- SAN 扫描还会读取活动及保留卷组下的全部卷、快照和私有连接集合，并通过已知子资源的自身读取补回列表遗漏。卷组扫描读取活动/保留卷、快照以及 SAN 的私有终结点连接。
- 子资源继承已验证的 SAN 地域。父资源、子网、源卷、私有终结点和控制器引用显示为依赖；控制器引用不授予删除所有权。
- 核验通过的卷组显示成员数量；无法解析卷组映射的私有连接仍作为可能的依赖显示。保留卷组只保留历史成员信息，不将其显示为当前数量；创建中的卷尚无 GUID 时，成员信息保持未核验。
- **列表不可用时**：保留卷组的快照列表返回 404 时，成员信息记为不完整；扫描仍会完成，可发现的卷继续显示但受保护，已知快照逐个读取（已知快照自身返回 404 可以关闭其记录，但列表不可用不能证明未知快照不存在）。卷成员不完整会保存为清理约束，直到重新扫描读到该集合；已知快照仍可单独选中。权限失败、卷集合缺失或活动卷组的快照列表缺失会使扫描失败。
- **SAN 或卷组在 Steward 之外被删除后**，盘点仍会读取活动与保留两类列表，并逐个读取已知资源。只有确认父资源自身不存在后，才接受子集合缺失；仍存活的子资源保留已记录的地域和网络关联。此前只能通过保留列表访问的资源，在该列表不可用时不能判为不存在。父资源缺失时，只有此前已核验的记录可以提供历史地域和网络信息，且子资源集合必须仍可读取。
- **只扫描子资源时**，未扫描的父记录保持打开；原生成员信息过期或缺失会保存为清理约束。请完整重新扫描该 SAN 的所有资源以对账父记录。SAN 的创建身份或可写配置变化，也需要重新审查后才能清理子资源。

**权限。** 即使只扫描 SAN 或卷组，也需要子资源的列表/读取权限。每种清理还需要 SAN 读取、资源组读取和管理锁列表权限。

| 任务 | 权限 |
| --- | --- |
| 删除私有终结点连接 | `Microsoft.ElasticSan/elasticSans/privateEndpointConnections/delete`；连接和卷组读取。不需要 Network 提供方的删除权限。 |
| 删除卷 | `Microsoft.ElasticSan/elasticSans/volumegroups/volumes/delete`；卷与快照列表/读取；已审查快照的删除权限；卷组读取 |
| 删除卷组 | `Microsoft.ElasticSan/elasticSans/volumegroups/delete`；卷、快照和私有连接列表/读取；已审查前置步骤的删除权限 |
| 删除 SAN | `Microsoft.ElasticSan/elasticSans/delete`；卷组、卷、快照和私有连接列表/读取；已审查子步骤的删除权限 |

**清理。**

- **快照**：具有可验证创建身份的快照可以删除。删除会移除所选恢复点，保留源卷和父资源。保护标签、管理锁、配置变化及不完整读取会阻止删除。操作回执支持重启恢复，不会重复发送 DELETE。
- **私有终结点连接**可直接删除。删除已批准的连接可能中断其映射卷组的访问。使用方的 Network 私有终结点、网卡、DNS 记录以及 SAN、卷组、卷和快照均保留。清理会核验连接的创建身份、目标、原生卷组 ID 和配置，再重新读取 SAN 与映射卷组的保护状态、状态、地域和管理锁。卷组映射不完整的连接仍会显示，但不能删除。断开状态不代表已删除。
- **卷**：关联快照作为独立的已审查前置删除步骤；保留任一关联快照会阻止卷清理。卷 DELETE 始终将快照删除设为 false，快照由各自的步骤清理。
  - 普通删除遵循卷组的保留策略（重新读取并与审查绑定），不会隐式清除保留副本。API 未返回策略时适用云端默认行为，实际结果通过活动列表、保留列表及卷自身 GET 确认。
  - 保留副本以原生 ID 和 `volumeId` 报告，继续可被发现，需要单独选中才能永久删除。选中已保留的卷时使用 `deleteType=permanent`。
  - 恢复或同名重建、列表不完整、已知快照仍存在，都会阻止确认完成。
  - 默认不强制删除存在活动 iSCSI 会话的卷：请先断开客户端。API 调用方可设置卷清理选项 `force_delete: true`，计划会提示可能中断工作负载；永久清除保留卷时不接受强制选项，也不会在主机上执行客户端命令。
  - 回执会记录软删除或不存在的结果，重启后不会重复发送 DELETE。
- **活动卷组**：计划先删除活动卷（含其快照）和卷组中其余快照，再删除卷组。私有终结点连接需要单独选中并先确认不存在；其使用方 Network 终结点保持独立。已有保留卷按保留资源审查，清理完成时必须仍然存在。含有保留卷的卷组需要核验为 Enabled 的保留策略。卷组 DELETE 不提供强制或永久删除选项，此流程也不会断开主机客户端。子资源身份或配置变化、新成员、受保护资源、锁及保留策略变化都会阻止删除。活动与保留两类列表加上自身读取，可区分永久删除与同 ID 软删除；保留卷组会以原 ID 被重新发现。卷组进入终态后，快照集合缺失时改用每个已知快照的自身读取核验；卷的列表必须仍可读取，以核验保留身份。卷组成员记录过期时，需要先重新扫描卷组。
- **SAN**：完成成员审查后可以删除。卷组作为前置步骤，其卷和快照保持各自顺序；私有终结点连接必须单独选中。Steward 确认这些资源均已消失后才调用 `Microsoft.ElasticSan/elasticSans/delete`，SAN 消失后还会再次核对原生集合及每个已知子资源的自身读取；仅父资源或集合返回 404 不能完成任务。Location 回执支持重启恢复，不会重复发送 DELETE。同名重建、可写配置变化、保护标签、锁和新发现的子资源会阻止删除。随子资源删除而变化的只读容量计数不会使审查失效。SAN 成员记录过期时，计划会被阻止，需要完整重新扫描。

**限制。**

- SAN 存在保留子资源，或其卷组启用了 Enabled 保留策略时，SAN 仍受保护：此流程无法保证永久移除这些资源。子步骤意外产生保留结果时，也会停止 SAN DELETE。
- 父级不支持强制或永久删除选项。永久清除保留卷组及跨保留资源的 SAN 清理尚未完成。
- 原始 REST 示例、预览版软删除 CLI 录制、稳定版 `2025-09-01` 快照录制，以及盘点和清理恢复流程已离线测试。尚未验证真实 Elastic SAN 或独立 ARM 模拟器。

参见[微软 Elastic SAN 删除顺序说明](https://learn.microsoft.com/en-us/azure/storage/elastic-san/elastic-san-delete)。

### Azure NetApp Files

**盘点。**

- 帐户、容量池、卷、快照、子卷、配额规则、卷组、快照和备份策略、备份库及备份。Steward 沿原生父资源 API 发现资源并逐项读取，包括已知但未出现在列表中的资源。父资源读取失败时保留已有记录。
- 备份和子卷的地域继承自已验证的父资源。卷的子网与 VNet 关联用于网络选择；备份与源卷的关联不授予级联删除权限。AD 凭据和未识别的私有字段不会显示。
- **策略绑定**：扫描备份策略、快照策略和备份库时，还会读取卷的当前绑定关系。策略暂停或执行已禁用仍算作绑定；备份中保存的历史策略 ID 不代表当前绑定。原生策略索引不完整时保持未解析状态。这些依赖不授权删除卷。
- **备份库**：扫描独立于卷的当前绑定，记录库内完整的原生备份清单，包括源卷已删除的备份。已知备份未出现在列表中时仍会按 ID 读取，只有备份自身不存在才会移除。成员变化会使扫描失败；备份记录缺失或过期时需要刷新。
- **卷组**：扫描读取卷组详情中的卷 ID 及数量，再读取这些卷以及帐户内完整的容量池和卷索引，核对卷的当前组名；卷组提供文件系统 UUID 时，还必须与卷自身读取结果一致。列表遗漏的已知成员仍会按 ID 读取，只有确认关联改变或卷自身不存在后才移除成员关系。卷组扫描不会删除或关闭卷记录本身。图记录缺失时需要刷新。
- **网络同级集合**：卷带有网络同级集合 ID 时，Steward 执行只读的原生查询，标识共享主挂载 IP 的卷；通过重复读取核对返回的子网与集合标识、当前卷 ID、UUID 和配置，卷提供挂载目标时，报告的 IP 也必须一致。可选挂载目标缺失时，仍以原生查询确认成员关系。
- **卷组网卡关联**：卷组扫描通过订阅范围内完整的网卡列表及网卡自身读取进行匹配，将同级集合主 IP 与卷提供的全部挂载地址结合，分两轮核对匹配的子网/IP、网卡原生 GUID 和关联工作负载 ID。列表遗漏的已知网卡会单独读取。挂载信息或网卡匹配缺失、原生 GUID 缺失、未知或共享工作负载及网卡过渡状态都会使关联不完整。只保存规范的关联卷 ID，不保存任意工作负载字段或网卡私密配置。关联不等于卷组独占所有权，也不授权删除。
- 读取不可用或扫描期间发生变化时，这些扫描会失败并保留先前记录。

**权限。** 每种清理还需要资源组和管理锁读取权限。

| 任务 | 权限 |
| --- | --- |
| 策略和备份库扫描 | 帐户内容量池和卷的列表/读取；快照策略还需要关联卷列表权限 |
| 备份库扫描 | 各备份库内备份的列表/读取 |
| 卷组扫描 | 卷组读取，帐户内容量池和卷的列表/读取，以及连接订阅范围内的 `Microsoft.Network/networkInterfaces/read` |
| 带网络同级集合的卷 | `Microsoft.NetApp/locations/queryNetworkSiblingSet/action`，以及返回的每个卷的读取权限 |
| 删除快照或备份 | 快照或备份删除；资源及父级读取。审查备份还需要帐户内备份库和备份列表及源卷读取。 |
| 删除子卷或配额规则 | 子卷或配额规则的列表、读取和删除；卷、容量池和帐户读取；活动复制列表权限 |
| 删除容量池 | 容量池删除及卷清理权限；容量池和卷的列表/读取；帐户读取 |
| 删除快照策略 | 卷更新和快照策略删除，以及绑定发现所需的读取权限 |
| 删除备份策略 | 卷更新、最新备份状态读取和备份策略删除，以及绑定发现所需的读取权限 |
| 删除备份库 | 卷更新、最新备份状态读取、备份策略读取、备份删除和备份库删除，以及完整的发现和保护检查读取权限 |

**清理。**

- **卷**：符合条件的卷可在审查后删除，范围包含卷内快照、子卷和配额规则。请先停止应用，并从所有主机卸载此卷。备份库内的备份、容量池及帐户保留。存在活动复制、还原、克隆、保护标签或锁时不允许清理；卷或子资源发生变化后需要重新生成计划。执行中断后从已保存的回执继续，并确认卷及子资源已经消失。
  - 挂载目标是卷的只读属性，不能以删除整个卷代替移除挂载。
  - 属于网络同级集合的卷：网络迁移中或原生 UUID 缺失时，卷仍会显示，但不允许清理。读取不可用、新增同级卷、状态变化或查询遗漏了仍存活的同级卷时，需要重新审查。清理途中，只有被移除的已审查同级卷各自返回 404，才接受集合缩小，这样先前的卷前置步骤可以完成，又不会悄悄漏掉存活的同级卷。已有卷需要重新扫描才能记录这项网络审查。网络 IP 不是网卡资源 ID：网卡归属、保护和删除影响的核验尚未完成。
- **快照和备份库内的备份**可单独删除。计划会提示所选恢复点将永久丢失，源卷、其他恢复点及父资源保留。删除最后一个备份还会移除后续增量备份的参考点。源卷删除后，备份仍可审查和清理。
  - 源卷仍存在且分配了备份策略时，已知的最新备份（包括快照时间相同的并列最新备份）受保护，即使策略执行已禁用也不例外。只有成功完成的更新备份才能证明所选备份较旧。可选的时间字段缺失时，由 Azure 原生 DELETE 执行最终限制。
  - Steward 不会强制删除，也不会更改备份策略。快照的还原、克隆和复制限制同样由 Azure 原生检查处理。读取不可用时不能清理。
- **子卷和配额规则**可在无活动复制的卷上单独删除。
  - 删除子卷会移除其数据，并可能中断使用它的应用；父卷、其他子卷和恢复点保留。
  - 删除配额规则会改变相关用户或组的存储限制，其他配额规则仍可能适用，文件不会被删除。支持默认及单独的用户/组配额规则，也支持删除需要移除的失败规则。复制源上的配额变更会同步到目标卷。
  - 已审查的路径、配额配置或父资源变化后需要重新生成计划。子卷功能禁用、还原、活动克隆、保护设置或读取不完整时，不允许删除。
  - Azure 已[弃用子卷 CLI 命令](https://learn.microsoft.com/en-us/cli/azure/netappfiles/subvolume)，Steward 使用的 `2025-12-01` REST 版本仍提供其删除接口。
- **容量池**：先逐一删除已审查的卷，再删除空容量池。请在计划中检查这些卷及其快照、子卷和配额规则，执行前停止应用并卸载这些卷。保留任一卷会阻止容量池删除；NetApp 帐户和备份库内的备份保留。权限缺失、新增卷、可写配置或原生标识变化均需重新审查。只有父资源仍可读取且容量池自身已不存在时才确认完成。
- **快照策略**可在保留关联卷的情况下删除。计划会将这些卷及其已有快照、子卷和配额规则列为保留项。执行时逐一解除卷上已审查的快照策略绑定，确认绑定消失后再删除策略。由此策略安排的后续快照停止；卷内数据、已有快照及备份库内的备份保留。单独选择卷仍走卷清理流程，不会同时选择快照策略。新增关联卷、绑定改为其他策略、卷配置或标识变化、保护设置及父资源不可读取都会阻止修改。回调完成后，若卷仍显示旧绑定或策略仍存在，不会确认完成。
- **备份策略**可在保留卷和已有备份的情况下删除。执行会等待备份传输空闲，暂停已审查卷上的策略执行，确认暂停后再次等待传输完成，然后解除卷的备份策略绑定。所有已审查绑定消失后才删除策略。后续策略备份停止；备份库、已有备份、快照、子卷、配额规则和卷内数据均保留。重新扫描必须取得原生策略 UUID 及完整的使用方清单。传输状态未知、读取不可用、策略执行被重新启用、新增使用方或标识/配置变化都会阻止继续修改。
- **备份库**：清理会停止已审查卷的计划备份，解除其备份策略绑定，并永久删除库内所有已审查备份。确认这些备份已不存在后，才解除备份库绑定并删除空备份库。卷及其数据、快照、子卷、配额规则和全局备份策略保留。删除最后一个备份还会移除后续增量备份的参考点。保留库内任一备份都会阻止备份库清理。新增备份或使用方、标识/配置变化、保护状态、未知传输状态及读取失败都会阻止继续修改；清理途中出现的新备份需要重新审查，即使计划备份已经停止。
- 每次被接受的更新和删除都会保存以支持重启恢复，回调完成从不代替对资源本身的读取。
- **卷组**暂不支持清理。Azure 要求先删除全部成员卷，并在删除卷组时自动删除相关网卡；这些网卡影响仍需审查。在网卡生命周期影响实现之前，卷组保持受保护。

**限制。**

- 复制中子规则的清理、复制终止、克隆管理及导出策略编辑仍在实现中。
- 策略和备份库流程已有离线契约、故障注入及重启测试；通过卷 PATCH 解除绑定尚未在真实 Azure 环境验收。
- 快照策略和备份库没有原生 UUID：缺少创建元数据时，无法区分同名且配置完全相同的重建资源。
- 早期版本记录的卷组审查（四字段格式）需要重新扫描。

参见[删除快照](https://learn.microsoft.com/en-us/azure/azure-netapp-files/snapshots-delete)、[删除备份](https://learn.microsoft.com/en-us/azure/azure-netapp-files/backup-delete)、[配额规则语义](https://learn.microsoft.com/en-us/azure/azure-netapp-files/manage-default-individual-user-group-quotas)、[删除子卷](https://learn.microsoft.com/en-us/rest/api/netapp/subvolumes/delete?view=rest-netapp-2025-12-01)、[NetApp 访问权限](https://learn.microsoft.com/en-us/azure/azure-netapp-files/network-attached-storage-permissions)、[删除卷](https://learn.microsoft.com/en-us/azure/azure-netapp-files/volume-delete)、[存储层级](https://learn.microsoft.com/en-us/azure/azure-netapp-files/azure-netapp-files-understand-storage-hierarchy)、[快照策略删除要求](https://learn.microsoft.com/en-us/azure/azure-netapp-files/snapshots-manage-policy#delete-a-snapshot-policy)、[备份策略管理](https://learn.microsoft.com/en-us/azure/azure-netapp-files/backup-manage-policies)、[备份库管理](https://learn.microsoft.com/en-us/azure/azure-netapp-files/backup-vault-manage)和[应用程序卷组删除](https://learn.microsoft.com/en-us/azure/azure-netapp-files/application-volume-group-delete)。

### Cosmos DB

**盘点。** NoSQL、MongoDB、Cassandra、Gremlin、Table 账号及数据库/容器；角色、服务、笔记本与私有连接；托管 Cassandra 和 Fleet。账号显示在全局范围，托管 Cassandra 数据中心使用实际部署地域。数据资源名称区分大小写（见[第一次盘点](#第一次盘点)）。

**权限。** 读取权限须覆盖适用 API 的子集合、吞吐量配置、祖先资源，以及订阅内引用该账号的 Fleet 关联。

**清理。**

- 删除账号、数据库、容器或表会删除其中的数据。
- 计划先审查必要子资源、角色依赖和 Fleet 关联。保留内置角色或客户端加密密钥，需要同时保留其账号或数据库。
- 删除 Fleet 会解除账号关联，账号本身保留；账号受保护或加锁会阻止解绑。
- 删除前检查吞吐量、备份迁移、配置与子资源成员关系。
- 不提供恢复或永久清除操作。

参见[资源模型](https://learn.microsoft.com/en-us/azure/cosmos-db/resource-model)及 [MongoDB 角色](https://learn.microsoft.com/en-us/azure/cosmos-db/mongodb/role-based-access-control)。

### Azure DocumentDB

Azure DocumentDB 原名 MongoDB vCore。

**盘点。** MongoDB 兼容集群及副本、防火墙规则、专用终结点连接与 Microsoft Entra 用户注册。

**权限。** 读取权限须覆盖集群、完整的子资源与副本清单，以及各引用副本。

**清理。**

- 删除集群会删除其中的数据。
- 副本是独立集群。计划先删除已审查的副本，再删除源集群；单独删除副本会保留源集群。
- 防火墙规则、专用终结点连接与 Microsoft Entra 用户注册各有独立删除步骤。保留或保护必要资源会阻止父资源删除。
- 删除用户注册不会删除 Entra 身份，也不会清理数据库角色。
- 配置变化或正在进行的拓扑变更需要重新扫描或稍后重试。
- 不提供备份恢复或永久清除操作。

参见[副本删除规则](https://learn.microsoft.com/en-us/azure/documentdb/troubleshoot-replication)和[身份验证](https://learn.microsoft.com/en-us/azure/documentdb/how-to-connect-role-based-access-control)。

### Azure Data Explorer

**盘点。** Kusto 集群、数据库、跟随挂接、数据连接、主体、脚本与私有连接，以及自定义沙箱映像。

**权限。** 读取权限须覆盖全部子集合、祖先资源、跟随索引和链接目标。

**清理。**

- 计划先删除已审查的数据库资源及其他必要子资源，再删除集群。
- 删除仍被跟随的源数据库或集群时，先删除跟随集群上已审查的挂接；跟随集群本身保留。挂接控制其本地只读数据库视图，保留视图会阻止解除挂接。
- 活动自定义映像只能随集群清理。
- 托管私有终结点须校验目标配置、管理锁与保护状态；外部数据源保持独立。
- 删除脚本不会撤销其已经执行的命令。
- Azure 可能将集群软删除并保留 14 天，但恢复集群不能撤销之前执行的数据库 DELETE 步骤。Steward 不提供恢复，也不提供跳过软删除的选项。

参见[跟随行为](https://learn.microsoft.com/en-us/azure/data-explorer/follower)、[脚本说明](https://learn.microsoft.com/en-us/azure/data-explorer/database-script)与[集群删除](https://learn.microsoft.com/en-us/azure/data-explorer/delete-cluster)。

### Redis

**盘点。** 经典缓存及其访问策略与分配、防火墙规则、复制链接、维护计划和专用终结点连接；Enterprise / Managed Redis 集群、数据库、访问分配和专用终结点连接。

**权限。** 读取权限须覆盖订阅内的经典缓存及复制对端，包括其他资源组中的对端。

**清理。**

- 删除缓存或数据库会删除其中的数据。
- 独立子资源必须先删除；经典内置策略只能随缓存清理。
- **经典复制**：选择任一副本，会将主侧共享的解除链接操作及可能存在的反向视图纳入审查；保留链接视图会阻止解除链接。
- **Enterprise 主动复制**：会检查全部成员。后续删除仅在离组成员均返回 404 后接受更小的复制组。退化的复制组需先单独恢复，Steward 不会强制解除链接。
- 新增或矛盾的成员关系、不健康的链接、配置变化、锁和受保护的对端都会阻止清理。
- 完成时还会检查存活副本不再引用被删除的目标。

参见[经典复制](https://learn.microsoft.com/en-us/azure/azure-cache-for-redis/cache-how-to-geo-replication)与[主动复制](https://learn.microsoft.com/en-us/azure/redis/how-to-active-geo-replication)。

### Azure Synapse Analytics

**盘点。**

- 工作区、Spark 池和专用 SQL 池，使用原生列表和详情读取。列表遗漏不会移除已知资源，只有资源自身的 GET 确认不存在后才会关闭记录。默认 Data Lake 存储作为独立依赖保留。
- **数据面对象**：Spark 任务与会话、Notebook 和 Spark 作业定义从工作区数据面读取，并校验工作区归属、使用[独立令牌](#数据面与目录权限)。它们作为清单记录显示，关联工作区和池。扫描校验原生分页，保留列表遗漏的已知对象。返回数据和诊断日志不包含代码、任务配置和运行日志。
- **Pipeline**：Pipeline 对 Notebook、Spark 作业定义、其他 Pipeline 和 Spark 池的静态引用会显示为依赖，包括嵌套控制活动中的引用。动态表达式保持未解析；权限不足或配置在扫描期间变化时，扫描不会成功。
- Notebook 与 Spark 作业定义的盘点还会核验工作区内的 Spark 任务和 Pipeline 引用，供后续清理审查使用。
- **备份**：可恢复的已删除 SQL 池和 SQL 池还原点。Azure 提供相应字段时，会记录创建/删除时间、最早恢复时间、还原点类型与标签以及服务规格。这些记录与活动池分开保存；关联表示查询它们所用的工作区或池，从不授权级联删除。已知记录漏列时会单独读取。父资源缺失或集合不可用会使扫描失败并保留原有记录：已删除的工作区可能需要重建后才能查询其保留备份。只有在父资源可读时，备份自身不存在的结果才能关闭记录。

**权限。** 清理和取消操作还需要资源组读取和管理锁列表权限。

| 任务 | 权限 |
| --- | --- |
| Pipeline | 工作区及被引用资源的读取权限 |
| Notebook 与 Spark 作业定义 | 工作区内 Spark 池、任务、会话及 Pipeline 的列表与读取权限 |
| 备份 | 工作区和 SQL 池列表/读取，以及所选备份类型的 `Microsoft.Synapse/workspaces/restorableDroppedSqlPools/read` 和 `Microsoft.Synapse/workspaces/sqlPools/restorePoints/read` |
| 删除工作区 | 工作区删除；工作区读取；SQL/Spark 池列表和读取；SQL 复制链接列表和读取；Notebook、作业定义、Pipeline、批任务和会话的 Synapse 数据面列表和读取 |
| 删除专用 SQL 池 | `Microsoft.Synapse/workspaces/sqlPools/delete`；池和工作区读取；复制链接列表和读取 |
| 取消 Spark 任务和会话 | Synapse 数据面取消权限，工作区和池的 ARM 读取，以及用于异步状态和结果读取的工作区操作端点访问权限 |

**清理。**

- **工作区**：完整扫描其 SQL/Spark 池、代码资产和 Spark 任务记录后可以清理。请核对完整影响列表：删除将移除 SQL 池、计算引擎、Notebook、作业定义、Pipeline 和工作区元数据，并中断工作区内的任务；关联的 Data Lake 存储保留。保留任一工作区成员会阻止此操作；仅选择子资源不会自动选择工作区。新增成员、配置变化、保护设置或读取不完整时，需要重新审查。已保存的操作可跨重启恢复，清理会确认工作区和池均已不存在。
- **工作区删除与备份**：清理核验的是活动工作区和池已移除，不会清除 SQL 备份，也不代表所有 SQL 数据副本均已消失：工作区删除后，Azure 仍可能保留可恢复的 SQL 备份。恢复取决于可用还原点及保留期，Steward 不保证恢复成功。
- **专用 SQL 池**：Online 或 Paused 状态的池可以单独清理。单独选择符合条件的 SQL 或 Spark 池时，即使已完整扫描工作区，也会保留该池自己的清理步骤，不会选中工作区。计划会提示数据库将被移除，查询和使用方将失去访问；工作区、其他池和保留的 SQL 备份不会被删除。这是原生池删除，不代表所有使用方已停止，也不代表备份已清除。删除前会重新核验创建身份、配置、父级上下文和保护设置。
- **复制链接**会同时阻止池级和工作区级清理；已知链接即使漏列，也会单独读取。请先单独处理复制关系，再重新扫描。
- **Spark 池**仍被 Spark 任务或会话使用时，只能随工作区删除。
- **用户还原点**：Azure 返回 DISCRETE 类型、用户请求标签和有效创建时间时，可单独删除。受保护或信息不完整的记录不能清理。删除会移除该恢复选项，SQL 池、工作区及其他备份保留。Steward 保存删除回执，并在父资源未变更且可读取时确认该还原点已不存在。自动还原点不能由用户删除；仅 `DISCRETE` 类型不能证明该点由用户创建或可以删除。
- 代码资产（Pipeline、Notebook、Spark 作业定义）和 Spark 任务在活动 Pipeline 覆盖完成前不能单独清理，只能随工作区删除。

**限制。**

- Steward 的 Synapse 原生支持包括取消 Spark 任务和会话，并检查保护状态、读取详情确认结果。取消后可能保留已停止的历史记录，不能据此判断资源已删除。操作状态和结果读取会校验操作范围，并区分操作完成与资源不存在。
- 备份恢复和完整的保留备份处理尚未完成。

参见微软的[工作区删除范围说明](https://learn.microsoft.com/en-us/azure/synapse-analytics/quickstart-create-workspace-cli)、[从已删除工作区恢复](https://learn.microsoft.com/en-us/azure/synapse-analytics/backuprestore/restore-sql-pool-from-deleted-workspace)和[备份保留说明](https://learn.microsoft.com/en-us/azure/synapse-analytics/sql-data-warehouse/backup-and-restore)。

### Data Factory

**盘点。** 工厂所在地域的 14 类原生资源（见[覆盖范围表](#盘点与清理范围)），包括运行时节点注册和托管虚拟网络。管道内容、连接值、运行参数及调试详情不会进入公开清单和日志。

**权限。** 盘点与清理需要完整的工厂及子资源列表、资源自身读取、运行时状态、触发器事件订阅状态、管道运行查询及读取、调试会话查询，以及资源组和管理锁读取权限。清理还需要所选 DELETE 和准备操作的权限。

**清理。**

- **工厂**：清理会审查全部自有对象，保留任何自有子资源都会阻止删除工厂。托管虚拟网络没有独立 DELETE，随工厂清理。
- **触发器和 CDC** 先停止；事件触发器还需等待事件订阅取消。
- **SSIS 运行时**及引用它的对象是独立前置步骤。异步 Stop 完成后，还要等运行时自身报告已停止才会删除。
- **运行中的任务**：工厂清理逐个取消已审查的活动管道运行，并删除已审查的调试会话。单独清理管道只取消该管道已审查的运行；其他独立对象会等待工厂任务结束。新发现的任务需要重新审查。
- **共享自托管运行时**：请明确选择引用它的运行时资源或其工厂。只有在各自读取确认不存在后，才会解除已审查工厂的链接；无法解析或来自其他订阅的链接会阻止清理宿主。删除节点仅移除其注册。
- 源数据、外部计算、身份、网络和自托管机器保持独立。
- 准备和删除核验可在工作进程重启后继续，核验时限为 24 小时。配置、创建身份、保护、管理锁发生变化，或原生上下文不可读取时，会阻止继续执行。

**限制。**

- 查询覆盖服务可见的任务，并重读已知运行，无法证明不可访问的历史状态。
- Azure 返回的脱敏凭据和缺少创建标识的对象会限制变更检测。
- 验证包含协议测试、官方录制和工作进程测试；真实云及独立 Data Factory 模拟器验收仍待完成。

参见微软的 [SSIS 删除顺序](https://learn.microsoft.com/en-us/azure/data-factory/manage-azure-ssis-integration-runtime)、[取消事件订阅接口](https://learn.microsoft.com/en-us/rest/api/datafactory/triggers/unsubscribe-from-events?view=rest-datafactory-2018-06-01)与[共享运行时管理](https://learn.microsoft.com/en-us/azure/data-factory/create-shared-self-hosted-integration-runtime-powershell)。

### Data Migration

**盘点。** 迁移服务所在地域的八类原生资源：经典服务、项目、任务、文件和服务任务；SQL/Mongo 迁移服务及其面向 SQL、Cosmos DB 目标的迁移。Mongo 同时使用目标范围的独立列表和服务索引；SQL 在服务索引之外补读已知迁移。迁移输入和连接详情不会进入公开清单及日志。

**权限。**

- 盘点与清理：完整的原生服务、子资源和迁移索引；资源自身读取；SQL 运行时监控；关联 SQL/Cosmos DB 目标的读取；资源组和管理锁读取。
- 执行还需要所选 DELETE、任务/迁移 Cancel、SQL `deleteNode` 及区域操作状态读取权限。
- 删除可能作为迁移目标的资源时，也需要订阅范围内同样的迁移发现权限。

**清理。**

- **经典服务和项目**：子资源分别作为已审查的删除步骤，运行中的任务先取消。架构文件须先清理使用它的任务。
- **SQL/Mongo 服务**：须明确选择其目标范围内的迁移并先删除。SQL 迁移先取消再删除；运行中的 Mongo 迁移使用原生强制删除。SQL 服务清理会等待节点任务结束，移除已审查的运行时注册并确认其不存在。
- **迁移目标**：删除被引用的资源或包含它的资源时，会检查引用它的迁移，包括原生迁移路径指向的 SQL 数据库。相关迁移必须选中并先删除，尚未入库的迁移会阻止清理。索引不可读或已记录的迁移上下文改变也会阻止清理；仅凭目标自身的读取或列表，不能判断没有迁移引用它。执行和恢复核验时会再次检查，目标已经消失后也不例外。
- 源/目标数据库、备份存储、身份、网络和运行时机器保持独立。
- 配置、目标身份、资源组、保护、管理锁发生变化，出现新迁移或运行时节点改变时，可能需要重新审查。已接受的操作和核验可在工作进程重启后继续，核验时限为 24 小时。

**限制。**

- 所有可用索引均遗漏的未知迁移无法补回。
- 脱敏字段限制了变更检测。
- 目前证据包含官方 API 示例、CLI 录制和工作进程测试；独立 DMS 模拟器及真实云验收仍待完成。

参见 [ARM 异步操作跟踪](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/async-operations)。

### Stream Analytics

**盘点。** 作业、输入、输出、函数、转换、集群和集群私有终结点。

**权限。** 读取权限须覆盖全部子集合、集群作业成员、父资源和链接目标。

**清理。**

- 删除作业会永久移除其输入输出定义、函数和查询，外部数据存储保留。
- 转换须通过选择其所属作业来清理。
- 单独删除输入、输出或函数要求作业处于 Created、Stopped 或 Failed 状态。
- 删除集群前先删除已审查的私有终结点；私有终结点须检查目标配置、管理锁和保护状态。
- 集群中的作业保持独立：须明确选中它们一同删除，或者先停止要保留的作业、在 Azure 中将其移出集群，再重新扫描。Steward 不会自动停止、移出或删除未选中的作业。

参见[作业清理](https://learn.microsoft.com/en-us/azure/stream-analytics/stream-analytics-clean-up-your-job)与[移出集群](https://learn.microsoft.com/en-us/azure/stream-analytics/manage-jobs-cluster)。

### Foundry 与 Cognitive Services

**盘点。** 账号、模型部署、项目、代理、连接（包括数据存储连接）、能力主机、托管网络、内容过滤与承诺计划。

**清理。**

- 账号清理先删除模型部署及已审查的依赖，再软删除账号，不提供永久清除。
- 删除能力主机会使依赖的代理状态无法访问；线程、文件与遗留存储数据不会逐项清理。
- Key Vault 连接需等待账号及项目中的其他连接全部删除。
- 声明需要或已启用托管专用终结点的连接，在其终结点影响建模完成前保持受保护。
- 保留必要子资源会阻止控制资源删除；共享承诺计划与引用的存储资源仍独立保留。
- 托管网络清理包含其规则，并核验专用终结点目标的保护状态；部分派生规则只能随网络清理。

**限制。** 旧账号类型的适用范围、托管连接的专用终结点影响及外部网络边界关联的生命周期仍未完成。

参见[恢复与计费行为](https://learn.microsoft.com/en-us/azure/ai-services/recover-purge-resources)。

### Azure AI Search

**盘点。** 服务、专用终结点连接、共享私有链接与网络边界配置视图。

**权限。** 读取权限须覆盖全部子集合及各链接目标。

**清理。**

- 删除服务会删除其搜索内容。
- 专用终结点连接与共享私有链接须先审查并删除；保留其中任一资源或边界配置视图都会阻止服务删除。
- 删除共享私有链接还会修改目标资源的连接元数据，因此须通过目标原生读取、继承锁和保护状态检查。目标数据资源独立保留。Cosmos DB 账号已有原生目标检查。

**限制。** 尚未建模的目标、跨订阅链接以及外部边界关联的生命周期仍未完成。

参见[共享私有链接删除行为](https://learn.microsoft.com/en-us/azure/search/troubleshoot-shared-private-link-resources)。

### Service Bus 与 Event Hubs

**盘点。**

- Service Bus 命名空间、队列、主题、订阅、规则、授权规则、灾难恢复别名、迁移配置和私有终结点连接。
- Event Hubs 专用集群、命名空间、事件中心、消费者组、授权规则、灾难恢复别名、架构组/应用组和私有终结点连接。
- Service Bus 自动转发依赖解析为同一命名空间内的队列或主题。Event Hubs Capture 引用其目标存储账户和 Blob 容器。
- 网络规则集、Event Hubs 网络边界配置、灾难恢复别名的授权视图及默认 `RootManageSharedAccessKey` 规则没有独立删除操作，随命名空间删除。

**权限。**

- 盘点和清理权限必须包含所有已审查子资源的原生读取操作；子资源列表失败不会被当作命名空间为空。
- 专用集群：集群成员清单和配额设置权限，以及各命名空间的生命周期权限。
- 配对的灾难恢复别名：命名空间列表权限，以及配对命名空间和别名的读取权限，所选地域之外的也需要，因为只有名称的配对目标会在整个订阅内解析。
- Service Bus 迁移：订阅内所有 Service Bus 命名空间及迁移配置的读取权限，包括所选地域之外的命名空间。清单缺失或原生列表不可读会阻止计划或执行。

**清理。**

- **命名空间与实体**：删除命名空间、主题和订阅可能删除其中的消息与配置。所有已建模的后代资源都会经过审查，并在执行后逐一确认不存在。删除命名空间不会自动选择 Capture 存储、用户分配的身份或独立的 Private Endpoint。
- **带复制的实体**：单独删除实体时也会检查当前复制配置，因为删除可能传播到配对命名空间。复制活动期间仍禁止单独删除实体；命名空间清理会先处理已审查的灾难恢复或迁移配置。
- **Event Hubs 专用集群**：原生成员清单必须与每个命名空间的 `clusterArmId` 一致，包括位于其他资源组的成员。选择集群会纳入这些命名空间及其已审查的后代资源；保留成员会阻止删除集群。执行前重新校验集群配额设置、成员关系、命名空间创建身份、锁与保护设置。删除集群前，以及重启后的完成检查中，都要求所有前置命名空间经原生读取确认不存在。Azure 要求集群至少存在四小时：已知未满该时长的集群会暂时标记为受保护。
- **异地灾难恢复（配对别名）**：清理已配对的主别名时，先等待待完成的复制，调用原生 BreakPairing，确认状态为 `PrimaryNotReplicating` 且配对方已清空，再删除别名。两侧 ARM 别名视图及其授权视图都会经过审查并确认不存在。选择任一命名空间都会纳入这一共享前置步骤，且只执行一次；未选择的另一命名空间及其中实体保留。单独选择副别名时，计划会指出其主别名是必需的控制资源。保留任一别名视图会阻止配对清理。在已保存的准备阶段中及重启后，都会重新核对两侧命名空间、资源组、锁与保护状态。Steward 不执行故障转移。
- **Service Bus 迁移**：清理会等待迁移同步完成，通过原生 Revert 操作中止复制，确认目标关联已清空，再删除配置。清理源或目标命名空间时都会纳入这一前置步骤；同时选择两端时只删除一次。仅清理源时保留目标及其中实体，仅清理目标时保留源及其中实体。执行中会重新核对源和目标的创建身份、迁移配置、权限、锁与保护设置；正在提交迁移或状态未知时阻止清理。已保存的准备阶段支持工作进程重启恢复。Steward 不会提交迁移。

参阅微软的[自动转发](https://learn.microsoft.com/en-us/azure/service-bus-messaging/service-bus-auto-forwarding)、[Capture](https://learn.microsoft.com/en-us/azure/event-hubs/event-hubs-capture-overview)、原生[命名空间清单](https://learn.microsoft.com/en-us/rest/api/eventhub/clusters/list-namespaces?view=rest-eventhub-2024-01-01)、[专用集群删除说明](https://learn.microsoft.com/zh-cn/azure/event-hubs/event-hubs-dedicated-cluster-create-portal#delete-a-dedicated-cluster)、[配对与解除配对说明](https://learn.microsoft.com/en-us/azure/event-hubs/configure-geo-disaster-recovery)和[迁移行为说明](https://learn.microsoft.com/zh-cn/azure/service-bus-messaging/service-bus-migrate-standard-premium)。

### API Management

**盘点。** 服务、工作区、API 及修订、策略、产品、订阅、门户内容与配置、凭据、通知、关联、自托管网关注册及独立工作区网关。不会读取 Key Vault 秘密内容，也从不下载外部策略 URL 或执行策略表达式。

**权限。** 读取权限须覆盖订阅内完整的网关索引、服务工作区链接、适用子集合、GET/HEAD 存在性检查、祖先及引用目标，包括其他资源组中的目标。凭据引用还需要原生订阅清单，以及匹配 Key Vault 与托管身份的读取权限。

**清理。**

- 清理会审查所选服务或工作区的 API 定义、策略、内容与配置。共享订阅、API 修订和引用方资源使用独立的已审查删除步骤。
- 删除 API、产品、组、标签关联或通知关联，只会解除该关联；被引用的成员保留，除非也选中它。
- 内置组、管理员用户、主订阅、邮件模板及固定的门户、通知、租户配置只能随所属控制资源清理。保留必要资源会阻止控制资源删除。
- 门户版本正在发布或配置发生变化时会阻止清理。
- **工作区**：清理先删除其已审查的独立网关配置连接；共享网关及其与其他工作区的连接保留。
- **服务**：Azure 将已删除的 API Management 服务保留 48 小时。Steward 不提供恢复、永久清除或邮件模板重置；恢复服务不能撤销先前单独执行的 DELETE 步骤。异步操作完成后会确认资源及子资源不存在。

**限制。** 验证包含官方 Schema、CLI 响应回放及固定版本独立模拟器中的部分 API Management 路径，不代表真实 Azure 部署测试。

参见[工作区网关](https://learn.microsoft.com/en-us/azure/api-management/workspaces-overview)与[软删除行为](https://learn.microsoft.com/en-us/azure/api-management/soft-delete)。

### Communication Services

**盘点。** 通信账户、SMTP 用户名、电话号码、预留号码、房间、邮件资源、域、发件人用户名、抑制列表及地址，均在全局范围盘点。电话号码、预留号码和房间保留原生账户 URL，房间 ID 区分大小写。扫描会两次核对完整的账户层级（包括房间参与者），已知资源从列表遗漏时会单独读取。账户或域不存在，不能证明已记录的子孙资源已删除。私有 SMTP、收件人、验证和参与者详情不会进入公开清单及 API 日志。

**权限。**

- 电话号码、预留号码和房间使用 Communication 数据面：原生数据读取权限（包括房间参与者列表），清理时还需相应的删除权限（见[数据面与目录权限](#数据面与目录权限)）。
- 通信与邮件资源、子资源、资源组和管理锁的 ARM 列表及读取权限。
- 清理还需要各所选资源的原生 DELETE 与操作状态读取权限。

**清理。**

- **账户**：先删除已审查的 SMTP 用户名、预留号码和房间，再通过账户的原生 DELETE 释放已审查的电话号码。单独选择电话号码时调用号码释放 API。
- **邮件**：地址、抑制列表、发件人用户名和域先于父资源删除。邮件域仍连接通信账户时，需要同时选择该账户，或先解除连接再重新扫描。反向连接检查覆盖当前连接的订阅，并通过 GET 补读已知但遗漏的账户；不能据此排除其他订阅中的连接。
- 关联的 Notification Hub 保持独立引用，不会随通信账户一起选中；该关联变化后，已审查的账户配置失效。
- 通信资源删除不可恢复，也会删除其关联的应用数据。这十类资源不会单独列出或审查聊天、通信身份数据及 Event Grid 筛选器等清理影响。
- 微软区分号码释放与计费周期内继续可见的状态。Steward 最多等待 40 天核验账户和号码删除；操作完成后仍未消失的账户或号码按小时复查，并要求资源及每个已记录的子孙资源自身读取返回 404。这不说明费用何时停止。
- 正在进行的购买、保护标签、管理锁、配置变化或资源不可读都会阻止清理。

**限制。** 这些资源规则不包含短信发送或模板管理。

参见[连接邮件域](https://learn.microsoft.com/en-us/azure/communication-services/quickstarts/email/connect-email-communication-resource)、[资源删除](https://learn.microsoft.com/en-us/azure/communication-services/quickstarts/create-communication-resource)与[号码释放](https://learn.microsoft.com/en-us/azure/communication-services/quickstarts/telephony/get-phone-number)。

### Monitor 告警与预算

**盘点。** 指标、活动日志、计划查询、智能检测、Prometheus 和告警处理规则；动作组；Web 测试；以及订阅和资源组范围的 Consumption、Cost Management 预算。扫描全局规则及预算时须包含全局范围。私密查询、接收器及通知内容不会进入清单和日志。

**权限。**

- 可能引用目标的规则的原生列举、读取权限。动作组检查还会遍历订阅和各资源组下的两类预算 API。
- 所有 ARM 资源的清理都会读取订阅内六类原生告警规则集合，包括源资源未入库的规则。相关接收目标还需要动作组读取权限；Application Insights 组件需要 Web 测试读取权限；关联的引用方需要资源组读取权限。
- Event Hub 接收器需要订阅范围的 Event Hubs 命名空间列举、读取权限；ITSM 接收器需要 Log Analytics 工作区列举、读取权限。
- 托管资源组：八类 Monitor 集合及两类预算 API 的读取权限，包括订阅和资源组预算列表。
- 仅为选中清理的资源授予原生删除权限。

**清理。**

- 告警规则、动作组、Web 测试和预算都可单独删除。预算的通知动作组保持独立。
- 保留告警规则或预算会阻止清理其引用的动作组。同时选择两者时，会通过分别审查的有序步骤先删除引用方。
- 依赖读取失败或前后不一致时会阻止清理，目标已不存在也不例外。
- **接收器**：目标消失或任务重启后，仍会通过已审查的 ARM 身份或工作区经过认证的客户 GUID 匹配引用；来自订阅之外的标识不能占用本地资源。已有 Log Analytics 工作区需要重新扫描以记录该身份。Function 和非全局 Runbook 的引用会与父资源分别记录；全局 Runbook 动作名称仍需要原生 Webhook 映射。
- **托管资源组中的 Monitor 资源**：Steward 通过两次完整的原生 Monitor 与预算读取补充通用 ARM 列表。未入库的成员需要先扫描，新增成员、配置不一致或集合不可读都会阻止删除。清理会检查每个将被删除的已审查成员的传入引用。经核验的同组 Monitor 成员可以随控制资源级联删除，包括直接引用控制资源的告警或 Web 测试；来自组外的引用仍会阻止删除。检查会重新核对私有配置、资源组身份和接收器解析结果，控制资源和资源组消失后仍会读取已知残留成员。每个成员都要单独确认最终不存在。
- 配置或权限变化后需要重新成功扫描并生成计划。

参阅微软的[动作组接收器契约](https://learn.microsoft.com/en-us/rest/api/monitor/action-groups/get?view=rest-monitor-2023-01-01)。

### Azure Monitor 工作区

**盘点。** 工作区及其默认摄取托管资源组。

**清理。**

- 删除工作区还会删除默认摄取托管资源组及组内全部资源。计划会审查完整影响，包括 Steward 无法识别的组内资源类型；保留任何组成员都会阻止删除。
- 两个原生默认摄取资源 ID 及任何 `managedBy` 值必须一致。
- 工作区数据不支持软删除恢复。
- 私有连接属于冻结的工作区配置，引用的外部网络终结点不会仅因被引用而纳入托管组。
- 外部关联会先解除，清理会确认资源组及每个已知资源均已消失。托管组中的数据收集终结点执行相同的 [AMPLS 检查](#azure-monitor-专用链接范围)。

参阅[微软的工作区管理说明](https://learn.microsoft.com/en-us/azure/azure-monitor/metrics/azure-monitor-workspace-manage)。

### Monitor 数据采集

**盘点。** 数据收集规则（DCR）、数据收集终结点（DCE）及其在被监控资源上的关联。盘点通过限定当前订阅的 Resource Graph 查询补充发现孤立关联，再以原生 GET/ListByResource 核实每个结果。该查询存在延迟，且只返回有读取权限的资源，因此完整盘点需要覆盖整个订阅的读取权限。无位置信息的孤立关联归入全局范围。Blob 引用 URL 的凭据不会进入清单和日志。

**权限。** 订阅范围的规则和终结点列举、读取权限，两个方向的关联反查权限，以及被监控资源范围内的关联读取权限。清理还需要所选规则或终结点的删除权限，以及关联所在范围的关联删除权限。

**清理。**

- 移除关联会停止对应的数据采集。
- 计划会纳入每个必要的解除关联步骤；同一关联涉及两个目标时只删除一次；保留必要关联会阻止删除。
- DCR 删除显式使用 `deleteAssociations=false`，并先通过原生读取确认所有前置关联已不存在。

**限制。** 租户全局 monitoredObjects 关联仍未支持。

参阅原生[关联操作](https://learn.microsoft.com/en-us/rest/api/monitor/data-collection-rule-associations?view=rest-monitor-2024-03-11)和[规则删除](https://learn.microsoft.com/en-us/rest/api/monitor/data-collection-rules/delete?view=rest-monitor-2024-03-11)。

### Azure Monitor 专用链接范围

**盘点。** 全局的 Azure Monitor 专用链接范围（AMPLS）、其范围资源关联与专用终结点连接，以及专用链接能力描述。

**权限。** 读取订阅内完整的 AMPLS 清单、原生关联列表与详情，以及目标资源反向引用的权限，包括所选资源组之外的范围。

**清理。**

- **范围**：范围资源关联与专用终结点连接都有独立删除步骤；保留其中任何一个都会阻止范围删除。关联的 Log Analytics 工作区、Application Insights 组件和使用方网络终结点保持独立。配置核对覆盖访问模式及连接级例外。
- **关联的资源**：删除 Application Insights 组件、Log Analytics 工作区或 DCE 前，Steward 会核对订阅内完整的 AMPLS 清单、关联的原生列表与详情，以及目标资源的反向引用。缺失盘点、读取失败及来自其他订阅的反向引用都会阻止删除；请在所属订阅中解除这些关联后重新扫描。Monitor 工作区托管组中的 DCE 也执行相同检查。
- 相对操作地址经校验后绑定到所选连接和资源，并保存。

**限制。** 验证使用固定版本的官方样例和组合协议测试，尚未进行 AMPLS 模拟器或真实云验证。

参阅 [AMPLS 关联要求](https://learn.microsoft.com/en-us/azure/azure-monitor/fundamentals/private-link-configure#connect-resources-to-the-ampls)。

### Application Insights

**盘点。**

- 组件、共享及个人分析项、持续导出、收藏、工作项配置、API Key、Profiler 关联存储及注释。
- 组件设置：当前计费功能、每日上限、定价计划、配额状态及旧版主动检测设置。这些设置随组件清理，没有独立删除操作。私密通知收件人不会进入清单和日志。
- **注释**使用 Azure 滚动 90 天限制内的固定窗口发现。每次扫描还会按 ID 重读已保存的注释，包括窗口之外的记录，因此窗口不会关闭旧记录。只有注释自身的原生 GET 确认不存在，已保存的注释才会关闭，组件消失后也是如此。Steward 无法列出原生窗口之外从未发现过的历史记录，删除组件可能一并移除这些历史。
- **托管工作区**：组件扫描还会读取完整资源组索引，以及当前托管工作区的组成员和 AMPLS 关联。归属须由组件的工作区引用与资源组 `managedBy` 共同确认，不能仅凭名称推断。共享工作区和切换后留下的旧托管组分别记录。读取失败或不一致会使扫描失败，跨订阅引用不会授权跨订阅读取。

**权限。**

- 计费、每日上限、定价、配额和主动检测设置等组件接口的读取权限。
- 注释 LIST/GET 权限；选中的注释还需要 DELETE 权限。
- 资源组、资源列表、成员产品详情和 AMPLS 读取权限，包括组件资源组以外的托管组。

**清理。**

- **子资源**：八类子资源可单独删除，并核对组件、资源组、锁及最终原生 GET 不存在状态。共享存储保持独立。
- **注释**：组件清理将近期和已保存的注释列为独立前置步骤，保留注释会阻止删除。读取失败或含义不明确的 GET 数组会阻止清理，空数组不被视为不存在的证明。
- **组件**：先删除已审查的子资源及 AMPLS 关联，包括指向当前托管工作区的关联。删除范围包含已审查的当前托管组及其已知后代。只有组件和资源组都已消失，且每个已知成员的原生 GET 均确认不存在，才会完成。锁或 Azure Policy 可能留下资源组；Steward 会继续等待，不会自行删除工作区。切换后留下的旧组和共享工作区会保留。组内嵌套的托管控制资源若还有未建模的外部托管组，会阻止清理。
- 删除组件前会重新核对可编辑设置，包括私密通知收件人；设置变化后需要重新扫描并生成计划。配额等只读数据的变化不会使计划失效。
- 迁移后的智能检测告警规则及其动作组与其他 [Monitor 告警](#monitor-告警与预算)一样独立盘点和删除。

参阅[原生注释接口](https://learn.microsoft.com/en-us/python/api/azure-mgmt-applicationinsights/azure.mgmt.applicationinsights.v2015_05_01.operations.annotationsoperations?view=azure-python)、[托管工作区行为](https://learn.microsoft.com/en-us/azure/azure-monitor/app/managed-workspaces)和[智能检测迁移说明](https://learn.microsoft.com/en-us/azure/azure-monitor/alerts/alerts-smart-detections-migration)。

### 工作簿

**盘点。** 共享工作簿、私有工作簿和工作簿模板。工作簿发现会读取四个官方类别，以及从 ARM 或已保存 ID 中发现的自定义类别；类别清单遗漏不会关闭已保存的工作簿，成功扫描只关闭自身原生 GET 已确认不存在的记录。模板使用完整的资源组枚举。完整内容与历史版本保持私密。私有工作簿 API 不可用等原生错误会使扫描失败。

**权限。** 订阅资源及资源组列表、锁和原生工作簿 LIST/GET 权限，共享工作簿还需要历史版本 LIST/GET 权限。清理需要所选资源的原生 DELETE 权限。

**清理。**

- 删除前会重新核对内容和历史版本；变化后需重新扫描并生成计划。
- 删除工作簿只移除活动资源，不代表永久擦除。Azure 通常会保留已删除工作簿约 90 天。
- 自带存储（BYOS）工作簿没有服务托管的历史版本或回收站恢复能力，能否恢复可能取决于存储的软删除配置。
- 引用的来源资源、存储账户或容器及分配的身份是独立依赖，不会被工作簿清理自动选中。
- 若原生证据确认工作簿属于托管资源组，也可以随其控制资源一起删除，仍会核对内容和历史版本，并单独确认最终 GET 不存在。

参阅[工作簿管理](https://learn.microsoft.com/en-us/azure/azure-monitor/visualize/workbooks-manage)和 [BYOS 行为](https://learn.microsoft.com/en-us/azure/azure-monitor/visualize/workbooks-bring-your-own-storage)。

### 托管 Grafana

**盘点。** 工作区、托管专用终结点、专用终结点连接和集成配置。SMTP 密码不进入清单和日志。

**权限。** 删除工作区需要三类子资源的原生读取、列举和删除权限。

**清理。**

- 每个子资源作为已审查的前置步骤删除，保留子资源会阻止工作区删除。各资源也支持独立的原生操作。
- Steward 核对原生配置与父资源身份，再确认子资源和工作区均已不存在。
- 连接的数据源、AKS 集群和使用方专用终结点仍作为独立资源保留。

参阅原生[工作区](https://learn.microsoft.com/en-us/rest/api/managed-grafana/grafana/delete?view=rest-managed-grafana-2025-08-01)、[托管专用终结点](https://learn.microsoft.com/en-us/rest/api/managed-grafana/managed-private-endpoints/delete?view=rest-managed-grafana-2025-08-01)和[集成配置](https://learn.microsoft.com/en-us/rest/api/managed-grafana/integration-fabrics/delete?view=rest-managed-grafana-2025-08-01)操作。

### 诊断设置

**盘点。** 资源和订阅级诊断设置，包括 Blob、File、Queue、Table 各自的服务范围，显示在全局清单。订阅级设置列表并不包含每个资源上的设置，因此 Steward 还会使用原生子资源 API 和已保存的设置 ID；源资源被删除后，已知设置仍可保留在清单中。未知源类型上的设置会先标为受保护，直到支持其原生源校验。从未入库、且不在已发现源范围内的孤立设置，没有可用的订阅级索引。只有设置自身的原生 GET 确认不存在，已保存的设置才会关闭；源资源或资源组消失不能证明设置已删除。

**权限。** 订阅资源列表、已发现源资源的原生读取及子资源列表、资源组和管理锁读取，以及每个确切源范围内的诊断设置列举、读取权限。清理还需要所选设置的 `Microsoft.Insights/diagnosticSettings/delete` 权限。

**清理。**

- 删除源、目标或其祖先前，须先选中引用它们的诊断设置，包括托管资源组中的设置。
- 删除设置会停止相应的导出。共享存储、Event Hubs 和工作区仍是独立资源。
- 列表或详情读取失败会阻止清理。

参阅微软的[诊断设置说明](https://learn.microsoft.com/zh-cn/azure/azure-monitor/platform/diagnostic-settings)。

### Azure RBAC

**盘点。** 自定义和内置角色定义，以及订阅、资源组和资源范围的角色分配，显示在全局清单。发现使用当前订阅的原生 Authorization API，也覆盖更小的资源范围；继承自租户或管理组的分配不由当前连接管理。权限表达式和编写的配置不会进入清单及日志。

**权限。**

- `Microsoft.Authorization/roleDefinitions/read`、`Microsoft.Authorization/roleAssignments/read`、`Microsoft.Authorization/roleEligibilitySchedules/read`、`Microsoft.Authorization/roleAssignmentSchedules/read`，以及相关作用域资源、资源组和管理锁的原生读取权限。
- 删除自定义角色：对每个可分配范围拥有 `Microsoft.Authorization/roleDefinitions/delete`。
- 删除角色分配：在其确切范围拥有 `Microsoft.Authorization/roleAssignments/delete`。
- 读取用户分配身份：`Microsoft.ManagedIdentity/userAssignedIdentities/read`；系统分配身份使用其资源的原生读取权限。该匹配使用 ARM API，无需 Microsoft Graph 权限。

**清理。**

- **自定义角色**：删除前须先选中引用它的角色分配。
- **作用域**：删除作用域资源或其祖先前，须先删除引用它们的分配和自定义角色定义，包括托管资源组中的扩展资源。所有 ARM 依赖与清理检查都会读取订阅的角色分配和角色定义索引；存在关联时还需要作用域和 PIM 读取权限。新出现、未入库或不可读的引用会阻止清理，目标已被删除也不例外。
- **托管身份**：在当前连接的订阅内，角色分配的 `principalId` 还会关联用户分配的托管身份，以及具有系统分配身份的资源，即使分配位于其他资源组。须先删除这些分配；由控制资源连同身份资源一起删除时也遵循这个顺序。微软说明删除托管身份后仍会保留其角色分配。
- 使用这些主体依赖前，请重新扫描已有 ARM 资源。Steward 通过原生资源读取核对已保存的主体和租户 GUID，资源消失后仍保留经过核验的身份。身份信息变更、缺失或不可读会阻止依赖检查。应用的 `clientId` 和挂载的共享身份不会建立所有权：删除使用共享身份的资源会保留该身份及其分配。
- **不能单独删除**：内置角色；可分配范围超出连接的角色；存在匹配 PIM 计划的分配；授予连接自身主体的分配（见[通用保护](#通用保护)）；以及带有保护标签、管理锁，或原生配置、作用域身份已变化的资源。
- 原生 DELETE 返回 200/204 仅表示请求已接受，仍需资源自身的 GET 确认不存在才能完成。
- 共享目标和外部 Entra 主体保持独立。

**限制。** 尚未实现 PIM 计划删除，也不支持租户或管理组级的 RBAC 管理。

参阅[自定义角色删除要求](https://learn.microsoft.com/en-us/azure/role-based-access-control/custom-roles-rest#delete-a-custom-role)和[托管身份维护](https://learn.microsoft.com/en-us/entra/identity/managed-identities-azure-resources/managed-identity-best-practice-recommendations#maintenance)。

### Defender for Cloud

**盘点。** 订阅防护计划、VM/VMSS/Arc 机器范围，以及 AKS、ACR 上的 Containers 计划。计划展示原生 Free/Standard 等级、子计划、试用剩余时间、启用时间、扩展状态、继承关系及资源覆盖率。订阅计划为 Standard 并不代表所有资源均受保护，资源级覆盖配置可能不同。已知计划会逐项补读，父资源或列表中消失不能证明计划不存在。扩展参数和操作消息不会进入公开清单及日志。

**权限。** 相应范围内的订阅身份、原生父资源列表与读取，以及 `Microsoft.Security/pricings/read` 权限。

**清理。** 不支持。这些是只读的服务状态记录，与阿里云安全中心的只读基线一致。Steward 不修改防护等级，也不移除资源级覆盖配置。

**限制。** 目前证据包含官方示例及协议、工作进程测试；独立 Defender 模拟器和真实云验证仍待完成。

参见[原生计划状态与继承说明](https://learn.microsoft.com/en-us/rest/api/defenderforcloud/pricings/list?view=rest-defenderforcloud-2024-01-01)。

## 常见问题

| 现象 | 检查什么 |
| --- | --- |
| 服务主体验证失败 | 确认填写的是客户端密钥的**值**而不是 ID，且服务主体创建在订阅所属租户中。不支持主权云和 Azure Stack。轮换密钥后使用**替换凭证**，订阅、租户和应用须保持不变。 |
| 验证通过，但扫描项报权限错误 | 验证只确认身份有效。在订阅上授予 [Reader](#盘点所需的基础权限)，并补齐[清理保护](#清理保护)下对应产品小节列出的读取权限，然后重新扫描。 |
| 浏览器登录的连接报告 Key Vault、Microsoft Graph 等数据面失败 | 你本人的账号无法访问该数据面，其余盘点照常进行。为账号授予相应权限，或改用具备[数据面权限](#数据面与目录权限)的服务主体。 |
| Blob 容器缺失或 Blob 清理失败 | 授予 **Storage Blob Data Reader** 等数据面角色，并确认 Steward 能访问账户的公有 Blob 端点。Blob 清理要求使用标准 `ACCOUNT.blob.core.windows.net` 端点。 |
| Batch 作业、任务或节点缺失，或 Batch 清理失败 | 除 ARM 权限外，还需授予 **Azure Batch Data Contributor** 等 Batch 数据权限。见 [Azure Batch](#azure-batch)。 |
| 缺少 Microsoft Entra 用户和组 | 授予 Graph 应用权限 `User.Read.All` 与 `GroupMember.Read.All`（或 `Directory.Read.All`），并完成管理员同意。 |
| 扫描在某个 Key Vault 上失败 | Steward 无法读取该保管库的证书。授予 **Key Vault 读者**角色或包含证书“列出”和“获取”的访问策略，并检查到保管库的网络访问。 |
| 缺少管理组 | 对要盘点的管理组授予 `Microsoft.Management/managementGroups/read`。 |
| 已在 Azure 中删除的资源仍然显示 | 列表遗漏资源或读取失败时，Steward 会保留旧记录，直到资源自身的读取确认不存在。修复扫描中的权限错误后重新扫描。 |
| Cosmos DB 扫描失败 | 有两个仅大小写不同的数据资源名称在清单身份中冲突。 |
| 扫描对话框中无法选择 Azure Local 逻辑网络 | 授予 `Microsoft.AzureStackHCI/logicalNetworks/read`。修复权限后点击**重试**，已选网络会保留。 |
| Azure Local 逻辑网络类型显示为 `Unknown` 且受保护 | Steward 未能通过 API 版本 `2025-06-01-preview` 读到可识别的 `networkType`。在网络类型和自定义位置核验通过前，网络保持受保护。 |
| 属于网络同级集合的 NetApp 卷扫描失败 | 授予 `Microsoft.NetApp/locations/queryNetworkSiblingSet/action` 及返回的每个卷的读取权限。 |
| 清理被管理锁阻止 | Steward 从不移除锁。确认要删除时，在 Azure 中移除锁后重新扫描。 |
| 资源显示为受保护 | 检查是否有 `steward/protected` 或 `steward:protected` 标签、管理锁、托管资源组，或对应产品小节中的专属保护。 |
| 角色分配无法清理 | 它授予了连接自身的访问权、属于内置角色、可分配范围超出连接，或存在匹配的 PIM 计划。见 [Azure RBAC](#azure-rbac)。 |
| 子网、NSG、路由表、NAT 网关或公网 IP 无法删除 | 仍有资源占用它，见[网络占用](#通用保护)。把占用方加入任务，或在 Azure 中移除后重新扫描。服务关联链接须由其所属服务移除。 |
| 存储账户或 Blob 容器无法删除 | 资源不为空，或存在法律保留、不可变策略。Steward 不会清空或永久清除数据。 |
| 任务被引用目标的角色分配、诊断设置、告警规则、AMPLS 关联、Fleet 或迁移阻止 | 把引用方加入任务，使其先被删除；或在 Azure 中移除引用后重新扫描。见[其他资源的引用](#通用保护)。 |
| 计划要求重新扫描 | 已审查的配置、保护、锁或成员关系发生变化，或资源被重建。重新扫描并审查新任务。 |
| 升级后进行中的清理未通过恢复校验 | 旧版本缺少完整请求校验，其回执无法恢复。重新扫描并创建新的清理任务；下次升级前请先完成进行中的任务。 |
| 删除长时间停留在核验阶段 | 部分产品确认删除较慢：通信账户和电话号码最长 40 天，域名最长 48 小时，Data Factory 和 Data Migration 的核验最长 24 小时。托管资源组也可能被锁或 Azure Policy 留下。 |
| Event Hubs 专用集群暂时受保护 | Azure 要求集群至少存在四小时才能删除，请稍后再试。 |
| Elastic SAN 卷因活动 iSCSI 会话删除失败 | 先断开客户端。API 调用方可在卷清理请求中设置 `force_delete: true`。 |
| 单个函数删除失败 | 应用通过部署包运行，可能不支持删除单个函数。界面会原样展示 Azure 返回的错误。 |
| 轮换凭据后 Container Instances 清理要求重新扫描 | 敏感值会与审查时的值比较；轮换凭据后请重新扫描。 |
| 某个产品显示为只读 | 该产品的清理尚未实现，例如资源组、Key Vault、Container Apps 环境、Purview 账户和托管应用程序。见[覆盖范围表](#盘点与清理范围)。 |

## 下一步

- [扫描资源](./scans.md)
- [查询资源](./resources.md)
- [清理资源](./cleanup.md)
- [OIDC 连接](./oidc.md)
