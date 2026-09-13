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

Batch 作业、计划、任务和节点使用账户的 Batch 端点及独立访问令牌。除了所选账户资源的 ARM 权限，还需要相应的 Batch 数据权限；清理可使用 **Azure Batch Data Contributor** 角色。存储及密钥 URL 引用需要订阅级 Storage/Key Vault 列表权限与匹配资源的读取权限。用户订阅模式的节点还需要读取其 VM、磁盘和网络资源的 Compute/Network 权限。参见 [Batch 身份验证](https://learn.microsoft.com/en-us/azure/batch/batch-aad-auth)与 [Batch 角色](https://learn.microsoft.com/en-us/azure/batch/batch-role-based-access-control)。

Communication Services 的电话号码、预留号码和房间使用独立的 Microsoft Entra 令牌，作用域为 `https://communication.azure.com/.default`。服务主体需要原生数据读取权限（包括房间参与者列表），清理时还需相应的删除权限。ARM 权限须覆盖通信与邮件资源、子资源、资源组和管理锁的列表及读取；清理还需要各所选资源的原生 DELETE 与操作状态读取权限。Steward 从已验证归属的 ARM 账户获取数据端点，不使用账户密钥。参见 [Communication Services 身份验证](https://learn.microsoft.com/en-us/rest/api/communication/authentication)。

凭证使用部署的凭证加密密钥加密保存。轮换密钥时使用**替换凭证**；订阅、租户和应用必须保持一致。参阅微软的[服务主体认证说明](https://learn.microsoft.com/en-us/entra/identity-platform/v2-oauth2-client-creds-grant-flow)。

## 第一次盘点

1. 切换到新连接并核对订阅。
2. 执行**全部启用地域与全局资源**扫描。Steward 使用各产品原生列表和详情 API 发现支持的资源，同时保留 Azure Resource Manager 的广泛盘点结果。
3. 检查扫描覆盖与错误。权限或分页失败不会被视为原有资源已经消失。
4. 打开地域中的 VNet，查看子网、网卡、VM 和关联资源。VM 的网络位置通过网卡解析。

原生发现覆盖 VNet 子网、Blob 容器、SQL 数据库、伸缩集实例、DNS 记录、Service Bus 实体和 Event Hubs 消费者组等子资源。资源组显示在全局清单中；Azure 为资源组记录的地域用于存放资源组元数据。完整 ARM ID 区分订阅和资源组。Cosmos DB 请求保留数据资源名称的大小写；仅大小写不同的名称若在清单身份中冲突，会使扫描失败。

## 盘点与清理范围

Steward 识别 434 类资源，其中 405 类具有原生清理操作（包括 Batch 节点移除），执行时受下列条件约束。ARM 返回的其他资源类型作为只读清单展示。覆盖范围仍在扩展，尚未完整覆盖 Azure 的所有产品。

| 产品 | 资源 | 清理能力 |
| --- | --- | --- |
| Compute | VM 与扩展、托管磁盘、快照、托管镜像、可用性集、专用宿主机、容量预留 | 支持，受挂载与归属保护限制；宿主机组和容量预留组先删除成员 |
| VM Scale Set | Uniform、Flexible 伸缩集及实例、扩展 | Uniform 成员纳入级联影响；Flexible VM 作为前置删除步骤 |
| Azure Batch | 账户、池、节点、作业、计划、任务、应用、包版本、专用终结点连接及网络边界视图 | 已审查的前置删除与级联清理；精确移除节点并将运行任务重新排队；边界视图随账户清理 |
| API Management | 服务、工作区、API 及修订、策略、产品、订阅、门户内容与配置、凭据、通知、关联、自托管网关注册及独立工作区网关 | 已审查的级联清理与有序解绑；固定配置需通过所属控制资源清理；服务删除遵循软删除保留期 |
| 虚拟网络 | VNet、子网、网卡、网络安全组、路由表、公网 IP、公网 IP 前缀、NAT Gateway | 支持 |
| 负载均衡 | Load Balancer、Application Gateway | 支持 |
| Storage | 存储账户、Blob 容器 | 仅空资源 |
| SQL | 逻辑服务器、数据库、弹性池 | 服务器清理包含已审查的数据库与弹性池；禁止独立删除 `master` |
| PostgreSQL / MySQL | Flexible Server | 支持 |
| Cosmos DB | NoSQL、MongoDB、Cassandra、Gremlin、Table 账号及数据库/容器，角色、服务、笔记本与私有连接；托管 Cassandra 和 Fleet | 先删除已审查的子资源及共享依赖；客户端加密密钥和内置角色随控制资源清理；Fleet 解绑保留账号 |
| Azure DocumentDB | MongoDB 兼容集群及副本、防火墙规则、专用终结点连接与 Microsoft Entra 用户 | 先删除已审查的副本及子资源，再删除源集群或父资源；单独删除副本会保留源集群 |
| Azure Data Explorer | Kusto 集群、数据库、跟随挂接、数据连接、主体、脚本与私有连接；自定义沙箱映像 | 先删除已审查的子资源及跟随挂接；只读数据库与活动映像随控制资源清理 |
| Data Factory | 工厂、管道、数据集、数据流、链接服务、凭据、触发器、CDC、全局参数、集成运行时及节点、私有连接 | 审查工厂级联；先处理运行时及运行任务；托管虚拟网络随工厂清理 |
| Data Migration | 经典服务、项目、任务/文件和服务任务；SQL/Mongo 迁移服务及面向 SQL、Cosmos DB 的迁移 | 审查子资源及迁移前置删除；先取消任务、处理运行时节点，再删除服务 |
| Defender for Cloud | 订阅防护计划及支持的资源级计划状态 | 只读展示服务状态、覆盖率、扩展及继承关系 |
| Azure Arc | 机器、扩展、运行命令、许可证配置和共享 ESU 许可证 | 先清理已审查的子资源，再移除普通机器注册；共享 ESU 许可证需先解除分配，控制器托管机器需通过控制器处理 |
| Azure Local | VM 实例、访客代理、身份元数据、网卡、磁盘、网络、存储路径与镜像 | 原生盘点、VM/访客清理及系统盘/身份读回，再按审查结果清理 Arc 注册 |
| Stream Analytics | 作业、输入、输出、函数、转换、集群和集群私有终结点 | 作业定义随作业删除；集群关联作业须明确选中或先移出集群 |
| Foundry / Cognitive Services | 账号、部署、项目、代理、连接、能力主机、托管网络、内容过滤与承诺计划 | 先删除部署及已审查的依赖，再软删除账号；不执行永久清除 |
| Azure AI Search | 服务、专用终结点连接、共享私有链接与网络边界配置视图 | 先删除已审查的连接；边界配置视图随服务清理 |
| Redis | 经典缓存、访问策略/分配、防火墙规则、复制连接、维护计划和专用终结点连接；Enterprise / Managed Redis 集群、数据库、访问分配和专用终结点连接 | 先删除已审查的子资源，再删除父资源；经典复制先解除链接；健康的主动复制组检查全部成员 |
| App Service | Web App / Function App、部署槽、函数、证书、主机名绑定与服务计划 | 应用/部署槽审查所属子资源的级联影响；服务计划保持独立；证书须无 TLS 绑定 |
| 域名注册 | 注册域名与所有权标识 | 先删除已审查的标识和应用/部署槽主机名绑定，再按原生延迟删除域名；DNS 托管保持独立 |
| Communication Services | 通信账户、SMTP 用户名、电话号码、预留号码、房间、邮件资源、域、发件人用户名、抑制列表及地址 | 先清理已审查的子资源；账户删除负责释放已审查的电话号码；邮件域共享连接要求明确选择前置清理 |
| CDN 与 Front Door | 配置、经典终结点/源站/源站组/域名；Front Door 终结点/路由/源站组/源站/域名/规则集/规则/安全关联/证书引用 | 配置及所属子资源审查级联影响；共享引用按依赖顺序清理 |
| WAF | CDN 与 Front Door 策略 | 先删除已审查的引用终结点或安全关联，再删除策略；经典 Front Door 引用仍阻止清理 |
| 容器 | 容器注册表、Container App、Container Instances 容器组、AKS | AKS 清理审查节点资源组及已知的嵌套、外部托管资源 |
| Kubernetes Fleet Manager | Fleet、AKS 和 Arc 成员、托管命名空间、更新运行、策略、自动升级配置、Gate 与跨集群网络 | 先删除已审查的原生子资源，再删除 Fleet；跨集群网络先断开，更新运行负责所属 Gate，经核验的 Hub 资源随 Fleet 清理 |
| DNS 与私有终结点 | 公有/私有 DNS 区域及记录、私有 DNS 链接、Private Endpoint 与 DNS 区域组 | 级联审查包含已验证的托管网卡和外部 DNS 记录；系统 DNS 记录不可独立删除 |
| Virtual WAN 网关 | VPN/ExpressRoute 网关、连接、VPN NAT 规则及链路 | 显式编排连接和 NAT 的前置删除；VPN 链路由连接管理 |
| Service Bus | 命名空间、队列、主题、订阅、规则、授权规则、灾难恢复别名、迁移配置、私有终结点连接 | 支持原生操作及经过审查的命名空间/实体级联；迁移清理先中止复制再删除；配对别名经审查后先解除配对再删除 |
| Event Hubs | 专用集群、命名空间、事件中心、消费者组、授权规则、灾难恢复别名、架构组/应用组、私有终结点连接 | 集群清理先删除经审查的成员命名空间；支持命名空间/事件中心级联及配对别名的解配对流程 |
| 运维与身份 | Log Analytics 工作区、用户分配的托管身份 | 支持 |
| 托管 Grafana | 工作区、托管专用终结点、专用终结点连接、集成配置 | 先删除经过审查的子资源，再删除工作区；各资源也支持独立原生操作 |
| Monitor 工作区 | 工作区及默认摄取托管资源组 | 审查组内全部资源，先解绑外部关联，再验证组和已知资源均已消失 |
| Monitor 数据采集 | 规则、终结点及被监控资源上的关联 | 先删除经过审查的关联，再删除规则或终结点；共享关联只执行一次 |
| Monitor 专用链接 | 全局专用链接范围、范围资源关联与专用终结点连接 | 先删除已审查的子资源，再删除范围；关联的工作区和数据收集终结点需先解绑 |
| Application Insights | 组件、分析项、持续导出、收藏、工作项配置、API Key 和 Profiler 存储关联 | 先独立删除子资源并解除 AMPLS 关联，再审查当前托管工作区资源组的删除影响 |
| Azure Monitor 工作簿 | 共享工作簿、私有工作簿及工作簿模板 | 独立清理并核对完整内容和历史版本，引用的存储和身份保持独立 |
| Azure Monitor 告警 | 指标、活动日志、计划查询、智能检测、Prometheus 和处理规则；动作组与 Web 测试 | 独立清理；引用规则须经审查并先于共享 Monitor 目标删除 |
| Azure RBAC | 自定义和内置角色定义；订阅、资源组及资源范围的角色分配 | 符合条件的自定义角色与分配独立删除；内置或跨范围共享角色、PIM 管理的分配保持受保护 |
| 诊断设置 | 资源和订阅级设置，包括 Blob、File、Queue、Table 各自的服务范围 | 先独立删除设置，再删除其引用的源、目标或祖先；共享目标资源保持独立 |
| 预算 | 订阅及资源组范围的 Consumption、Cost Management 预算 | 独立清理，通知动作组保持独立 |
| 尚待实现生命周期的集合资源 | 资源组、Key Vault、Container Apps 环境 | 只读 |

Service Bus/Event Hubs 网络规则集、Event Hubs 网络边界配置、灾难恢复别名的授权视图、Uniform 伸缩集网络资源和 VPN 连接链路没有独立原生删除操作。默认命名空间授权规则 `RootManageSharedAccessKey` 也必须随命名空间删除。这些资源会纳入所属控制资源的删除影响；保留这类内置子资源会阻止删除所属控制资源。资源组、Key Vault 和 Container Apps 环境的清理仍未实现。

Service Bus 自动转发目标通过原生 API 解析为同一命名空间内的队列或主题。Event Hubs Capture 记录目标存储账户和 Blob 容器依赖。删除命名空间不会自动选择这些存储资源、用户分配的身份或独立的 Private Endpoint。盘点和执行权限必须包含所有已审查子资源的原生读取权限；子资源列表失败不代表命名空间为空。参阅微软的[自动转发](https://learn.microsoft.com/en-us/azure/service-bus-messaging/service-bus-auto-forwarding)和 [Capture](https://learn.microsoft.com/en-us/azure/event-hubs/event-hubs-capture-overview) 文档。

## 清理保护

Azure Arc 通过原生接口盘点机器和许可证，并在每台机器下枚举扩展、运行命令和许可证配置。请按所选类型授予 `Microsoft.HybridCompute/machines/read`、`Microsoft.HybridCompute/machines/extensions/read`、`Microsoft.HybridCompute/machines/runCommands/read`、`Microsoft.HybridCompute/machines/licenseProfiles/read` 和 `Microsoft.HybridCompute/licenses/read` 权限。子资源扫描需要订阅范围内的机器列表和读取权限。已知资源及列表遗漏的父机器会逐项补读；不完整响应或权限不足会使扫描失败。脚本、扩展设置、受保护参数和代理设置不会进入公开清单及 API 日志。

清理 Arc 扩展、运行命令或许可证配置，需要相应的 `Microsoft.HybridCompute/machines/extensions/delete`、`Microsoft.HybridCompute/machines/runCommands/delete` 或 `Microsoft.HybridCompute/machines/licenseProfiles/delete` 权限，以及子资源和机器读取、资源组列表与读取、管理锁读取和适用的 Monitor、诊断设置、RBAC、Fleet、DMS 依赖读取权限。引用这些资源的规则需要先审查并移除。驱动在删除前核验机器注册身份、子资源私有配置、保护标签和托管关系；重启后沿用已保存的签名异步进度，并要求子资源自身 GET 返回 404。父机器消失或异步操作成功不能单独完成清理。

清理计划会提示：删除运行中的 Run Command 会终止脚本；扩展移除需要单独核实代理端结果；删除许可证配置会改变机器许可配置，共享许可证仍保留。许可证配置消失不能证明计费已终止。原生 DELETE 不支持 If-Match 条件，重复校验也不能消除最后一次检查与删除之间的变化窗口。参见[微软代理移除指南](https://learn.microsoft.com/en-us/azure/azure-arc/servers/uninstall-agent)。测试覆盖原生协议回放及 SQLite 扫描、图谱、计划和恢复执行；真实代理移除仍待验证。

普通 Arc 机器注册（类型为空、AWS 或 GCP）支持在已审查的扩展、运行命令和许可证配置删除后清理。需要 `Microsoft.HybridCompute/machines/delete` 及三类子集合读取权限；仅扫描机器时也须读取这些子集合。子资源清单缺失时，需先补齐再生成计划；保留子资源会阻止机器删除。即使机器返回 404，系统仍会逐项读取已知和已审查的子资源。计划会提示：移除云端注册后，外部主机和本地代理仍需单独移除。HCI 注册需要已验证的 Local VM 上下文，并遵循下文的有序清理流程。无 VM 归属证据的 HCI 主机、VMware、SCVMM、AVS、EPS、未知类型及其他关联父集群的注册仍受保护。参见[断开连接与 Azure Local 删除说明](https://learn.microsoft.com/en-us/azure/azure-arc/servers/azcmagent-disconnect)。

共享 Arc ESU 许可证清理需要 `Microsoft.HybridCompute/licenses/delete`、许可证读取、订阅范围的机器列表和读取、机器许可证配置列表和读取，以及上述资源组、锁和引用依赖读取权限。关联配置必须显式选中清理，或先单独解除关联；仅删除配置或机器会保留共享许可证。删除前，许可证原生分配计数必须存在且为零。许可证可覆盖同一租户的其他订阅，因此本地配置列表为空不足以证明无关联；外部分配需在对应订阅中处理。计划会提示删除将移除许可权益，计费可能继续最多五个日历日。许可证返回 404 后仍会检查已知和已审查配置的关联，残留关联会阻止完成。参见[许可范围](https://learn.microsoft.com/en-us/azure/azure-arc/servers/license-extended-security-updates)及[计费行为](https://learn.microsoft.com/en-us/azure/azure-arc/servers/billing-extended-security-updates)。测试覆盖微软 CLI 原始删除响应及 SQLite 恢复执行，不能据此确认真实计费已经终止。

Azure Local 通过原生接口盘点 VM 实例、访客代理、访客身份元数据、网卡、磁盘、逻辑网络、存储路径与镜像。VM 和访客资源发现需要 `Microsoft.HybridCompute/machines/read`，并按所选类型授予对应的 `Microsoft.AzureStackHCI/<资源类型>/read` 权限，包括 `virtualMachineInstances/guestAgents/read` 和 `virtualMachineInstances/hybridIdentityMetadata/read`。已知资源逐项补读；集合为空或缺失时，也会检查固定的 `default` 单例资源。访客资源继承经过核验的 Arc 机器区域。权限不足、扫描期间配置变化或引用格式错误会使扫描失败，不会关闭已有资产。公开字段包含 VM 容量与电源状态、网络地址、磁盘和镜像信息、存储容量；凭据、SSH 密钥、代理配置及本地路径保持私密。

扫描对话框支持选择 Azure 虚拟网络和 Azure Local 逻辑网络，提供名称或 ARM ID 搜索及分页。创建任务时会重新读取所选网络；网络已删除或不可访问时，不会保存任务。列出 Local 网络需要 `Microsoft.AzureStackHCI/logicalNetworks/read` 权限。逻辑网络内部配置的子网不作为独立 ARM 资源供选择。权限错误会显示在界面中，重试会保留已选网络。

逻辑网络扫描沿网卡与 VM 引用纳入访客资源和关联虚拟磁盘。磁盘的网络归属还需读取原生 VM 实例和 Arc 机器，权限为 `Microsoft.AzureStackHCI/virtualMachineInstances/read` 和 `Microsoft.HybridCompute/machines/read`。系统会重新读取已保存的挂载证据，补查集合遗漏的已知 VM；磁盘解除挂载后，其网络引用中会移除该 VM。存储路径与镜像仍是独立引用。网络归属不授予反向依赖或删除所有权。

Azure Local 访客代理支持单独清理，前提是已扫描并核验 HCI 注册和 VM 配置。需要 `Microsoft.AzureStackHCI/virtualMachineInstances/guestAgents/delete`，以及访客资源、VM 实例、Arc 机器、资源组和管理锁的读取权限。清理会重新核验已审查配置、保护标记与继承锁，并持久化 ARM 操作以支持重试和重启恢复。只有访客资源自身的 GET 确认不存在，才会完成清理；操作成功或父资源消失都不足以证明完成。计划会提示访客管理可能中断，VM、Arc 注册和身份元数据仍保留。ARM 资源删除并不证明访客端代理已移除。

Azure Local 图谱区分机器、VM 实例、网卡、磁盘、逻辑网络、存储路径、镜像和自定义位置的引用。缺失或跨订阅目标保留为未解析引用，普通引用关系不授予删除所有权。VM 清理会先移除已审查的访客与 Arc 前置资源，再调用原生 VM DELETE。选中对应 Arc 注册时，系统会按官方 CLI 的顺序先完成 VM 清理，再原生删除注册；仅选中 VM 时会保留注册。关联网卡和数据盘仍保留，需要单独清理。参见 [Azure Local 虚拟机管理](https://learn.microsoft.com/en-us/azure/azure-local/manage/manage-arc-virtual-machines?view=azloc-2607)。测试覆盖原生契约、组合协议、网络关联筛选及 SQLite 对账；真实控制器与物理虚拟机移除尚未验证。

Defender for Cloud 计划展示原生 Free/Standard 等级、子计划、试用剩余时间、启用时间、扩展状态、继承关系及资源覆盖率。订阅计划为 Standard 并不代表所有资源均受保护，资源级覆盖配置可能不同。盘点读取订阅计划、VM/VMSS/Arc 机器范围，以及 AKS、ACR 上的 Containers 计划；需要相应范围内的订阅身份、原生父资源列表与读取、`Microsoft.Security/pricings/read` 权限。已知计划会逐项补读，父资源或列表中消失不能单独证明计划不存在。

这些记录用于展示服务状态，与阿里云安全中心的只读基线一致。Steward 不修改防护等级，也不移除资源级覆盖配置。扩展参数和操作消息不会进入公开清单及日志。目前证据包含官方示例及协议、SQLite worker 测试；独立 Defender 模拟器和真实云验证仍待完成。参见[原生计划状态与继承说明](https://learn.microsoft.com/en-us/rest/api/defenderforcloud/pricings/list?view=rest-defenderforcloud-2024-01-01)。

Data Migration 按迁移服务所在区域清点八类原生资源。经典服务和项目的子资源分别列为经过审查的删除步骤，运行中的任务先取消。架构文件须先清理使用它的任务。SQL/Mongo 服务要求明确选择其目标范围内的迁移，并先删除这些迁移。SQL 迁移先取消再删除；运行中的 Mongo 迁移使用原生强制删除。SQL 服务会等待节点任务结束，移除已审查的运行时注册并确认不存在后再删除。源/目标数据库、备份存储、身份、网络和运行时机器保持独立。

单独删除被引用资源或包含它的资源时，也会检查引用它的迁移，包括原生迁移路径指向的 SQL 数据库。相关迁移必须明确选择并先删除；尚未盘点的迁移会阻止清理。执行和恢复核验时会再次检查，目标已经消失后仍会检查。

盘点与清理需要完整的原生服务、子资源和迁移列表，资源自身读取、SQL 运行时监控、关联 SQL/Cosmos 目标读取，以及资源组和管理锁权限。执行还需要所选 DELETE、任务/迁移 Cancel、SQL `deleteNode` 及区域操作状态读取权限。Mongo 同时使用目标范围的独立列表和服务索引；SQL 在服务索引之外补读已知迁移。所有可用索引均遗漏的未知迁移无法被补回。

单独清理目标资源也需要上述订阅范围内的迁移发现权限。索引不可读或已记录的迁移上下文改变时会阻止清理，不能仅凭目标自身读取或列表判断没有迁移引用。

配置、目标身份、资源组、保护、管理锁发生变化，出现新迁移或节点注册改变时，可能需要重新审查。已接受的操作和核验可在 worker 重启后继续，核验时限为 24 小时。父资源消失后，仍须逐项确认已记录的后代及必要消费者不存在。迁移输入和连接详情不会进入公开清单及日志。原生 DELETE 没有条件版本保护，脱敏字段也限制变更检测。目前证据包含官方 API 示例、CLI 录制和 SQLite worker 测试；独立 DMS 模拟器及真实云验收仍待完成。参见 [ARM 异步操作跟踪](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/async-operations)。

Data Factory 按工厂所在区域清点 14 类原生资源，包括运行时节点注册和托管虚拟网络。盘点与清理需要完整的工厂及子资源列表、资源自身读取、运行时状态、触发器事件订阅状态、管道运行查询及读取、调试会话查询，以及资源组和管理锁读取权限。清理还需要所选 DELETE 和准备操作的权限。管道内容、连接值、运行参数及调试详情不会进入公开清单和日志。

工厂清理会审查全部自有对象。触发器和 CDC 先停止，事件触发器还需等待取消事件订阅。SSIS 运行时及其引用对象是独立前置步骤，异步 Stop 完成后仍须读取运行时自身，确认已停止才能删除。托管虚拟网络没有独立 DELETE，随工厂清理；保留任何自有子资源都会阻止删除工厂。参见微软的 [SSIS 删除顺序](https://learn.microsoft.com/en-us/azure/data-factory/manage-azure-ssis-integration-runtime)与[取消事件订阅接口](https://learn.microsoft.com/en-us/rest/api/datafactory/triggers/unsubscribe-from-events?view=rest-datafactory-2018-06-01)。

工厂清理逐个取消已审查的活动管道运行，并删除已审查的调试会话。单独清理管道仅取消该管道已审查的运行；其他独立对象会等待工厂任务结束。新发现的任务需要重新审查。共享自托管运行时要求明确选择引用它的运行时资源或其工厂；只有各自读取确认不存在后，才能解除已审查消费者工厂的链接。无法解析或来自其他订阅的链接会阻止清理宿主。源数据、外部计算、身份、网络和自托管机器保持独立；删除节点仅移除注册。参见[共享运行时管理](https://learn.microsoft.com/en-us/azure/data-factory/create-shared-self-hosted-integration-runtime-powershell)。

准备和删除核验可在 worker 重启后继续，核验时限为 24 小时。收到回执或父资源消失均不能证明已记录的后代或必要消费者消失，仍须逐项读取确认。配置、创建身份、保护、管理锁发生变化，或原生上下文不可读取时，会阻止继续执行。查询覆盖服务可见的任务，并重读已知运行，无法证明不可访问的历史状态。脱敏凭据和缺少创建标识的对象会限制变更检测，原生 DELETE 也没有条件版本参数来阻止并发外部修改。目前验证包含协议测试、官方录制和 SQLite worker 测试，真实云及独立 Data Factory 模拟器验收仍待完成。

通信与邮件资源在全局范围盘点。电话号码、预留号码和房间保留原生账户 URL，房间 ID 区分大小写。扫描会两次核对完整账户层级和房间参与者；已知资源从列表遗漏时，会通过该资源自己的 GET 补读。账户或域不存在不能证明已记录的子孙资源已删除。私有 SMTP、收件人、验证和参与者详情不会进入公开清单及 API 日志。

通信账户清理先删除已审查的 SMTP 用户名、预留号码和房间，再通过账户原生 DELETE 释放已审查的电话号码；单独选择电话号码则调用号码释放 API。邮件清理按地址、抑制列表、发件人用户名、域与父资源的依赖顺序执行。邮件域仍有通信账户连接时，需要明确选择该账户，或先解除连接再重新扫描。反向连接检查覆盖当前连接的订阅，并补读已知但遗漏的账户；不能据此断言其他订阅没有连接。参见[连接邮件域](https://learn.microsoft.com/en-us/azure/communication-services/quickstarts/email/connect-email-communication-resource)。

关联的 Notification Hub 保持独立，不会随通信账户自动选中清理；关联变更后需要重新扫描和审查账户配置。

通信资源删除不可恢复，也会删除其关联应用数据。这十类资源规则不单独列举聊天、通信身份数据或 Event Grid 筛选器，也不将其逐项列为清理影响。微软区分号码释放与计费周期内继续可见的状态。Steward 将异步轮询和后续不存在检查持久化，重启后可继续；账户及号码的核验最多等待 40 天，只有资源及每个已记录子孙资源自己的 GET 均返回 404 才完成。操作结束后仍未消失的账户或号码按小时复查；这不表示费用何时停止。正在购买、保护标签、管理锁、配置变化或资源不可读都会阻止清理。原生 DELETE 没有版本条件，无法消除检查后的外部并发修改。参见[资源删除](https://learn.microsoft.com/en-us/azure/communication-services/quickstarts/create-communication-resource)与[号码释放](https://learn.microsoft.com/en-us/azure/communication-services/quickstarts/telephony/get-phone-number)。这些资源规则不包含短信发送或模板管理。

Azure RBAC 角色定义和角色分配显示在全局清单。发现使用当前订阅的原生 Authorization API，也覆盖更小的资源范围；继承自租户或管理组的分配不由当前连接管理。需要 `Microsoft.Authorization/roleDefinitions/read`、`Microsoft.Authorization/roleAssignments/read`、`Microsoft.Authorization/roleEligibilitySchedules/read`、`Microsoft.Authorization/roleAssignmentSchedules/read`，以及相关作用域资源、资源组和管理锁的原生读取权限。删除自定义角色需要对每个可分配范围拥有 `Microsoft.Authorization/roleDefinitions/delete`；删除分配需要在其确切范围拥有 `Microsoft.Authorization/roleAssignments/delete`。参阅[自定义角色删除要求](https://learn.microsoft.com/en-us/azure/role-based-access-control/custom-roles-rest#delete-a-custom-role)。

删除自定义角色前，须明确选择并先删除引用它的角色分配。删除作用域资源或其祖先前，同样需要先独立删除引用它们的分配和自定义角色定义，包括托管资源组中的扩展资源。所有 ARM 依赖图与清理检查都会读取订阅的角色分配和角色定义索引；存在关联时还需要作用域和 PIM 读取权限。新出现、未入库或不可读的引用会阻止清理，目标已被删除也不例外。保护标签、管理锁、原生配置或作用域身份变化、内置角色、连接范围之外的可分配范围，以及匹配的 PIM 计划都会阻止独立删除。原生 DELETE 返回 200/204 仅表示接受请求，仍需所选资源自身的 GET 确认不存在才能完成。

在当前连接的订阅内，角色分配的 `principalId` 还会关联用户分配的托管身份，以及具有系统分配身份的资源，即使角色分配位于其他资源组。删除这些身份资源前，须明确选择并先删除相关分配；由控制资源连同成员一起删除时也遵循这个顺序。微软说明删除托管身份后仍会保留角色分配，参阅[托管身份维护](https://learn.microsoft.com/en-us/entra/identity/managed-identities-azure-resources/managed-identity-best-practice-recommendations#maintenance)。

使用身份依赖前，请重新扫描已有 ARM 资产。Steward 通过原生资源读取核对已保存的主体和租户 GUID，资源消失后仍保留经过认证的身份信息。身份信息变更、缺失或不可读会阻止依赖检查。读取用户分配身份需要 `Microsoft.ManagedIdentity/userAssignedIdentities/read`；系统身份使用其资源的原生读取权限。该匹配使用 ARM API，无需 Microsoft Graph 权限。应用的 `clientId` 和挂载的共享身份不会建立所有权；删除使用共享身份的资源会保留共享身份及其分配。

RBAC 权限表达式和编写的配置不会进入清单及日志；共享目标和外部 Entra 主体保持独立。尚未实现 PIM 计划删除或租户、管理组级管理。RBAC DELETE 没有版本条件保护，预检后仍可能发生外部并发修改。

诊断设置显示在全局清单。发现过程需要订阅资源列表、已发现源资源的原生读取及子资源列表、资源组和管理锁读取，以及每个确切源范围内的诊断设置列举、读取权限。清理还需要所选设置的 `Microsoft.Insights/diagnosticSettings/delete` 权限。清理源、目标或其祖先时，必须先明确选择并删除相关诊断设置，包括托管资源组中的设置。删除设置会停止相应的导出；共享存储、Event Hubs 和工作区仍是独立资源。参阅微软的[诊断设置说明](https://learn.microsoft.com/zh-cn/azure/azure-monitor/platform/diagnostic-settings)。

订阅级设置列表并不包含每个资源上的设置。Steward 结合原生子资源 API 和已保存的设置 ID 补充发现，因此源资源被删除后，已知设置仍能保留在盘点中。未知源类型的设置会先标为受保护，等待支持其原生身份校验；从未入库且不在已发现源范围内的孤立设置，没有可用的订阅级完整索引。列表或详情读取失败会阻止收敛或清理；源资源或资源组消失不能证明设置已删除。成功扫描仅在设置自身的原生 GET 确认不存在后关闭其已保存记录；清理也要求逐项确认不存在。原生 DELETE 不支持版本条件参数，删除前的检查无法完全排除同时发生的外部修改。

Monitor 告警、动作组、Web 测试与预算支持原生盘点和独立删除。扫描全局规则及预算时须包含全局范围。保留告警规则或预算会阻止其引用的动作组清理；同时选择两者时，会先删除引用方。清理需要可能引用目标的规则的原生列举、读取权限；动作组检查还会遍历订阅和各资源组下的两类预算 API。仅为选定的清理资源授予原生删除权限。私密查询、接收器及通知内容不会进入盘点和日志；配置或权限变化后需重新成功扫描并生成计划。

所有 ARM 资源的关系图与清理检查现都会读取订阅内六类原生告警规则集合，包括未入库的引用方。相关接收目标还需要动作组读取权限，Application Insights 组件还需要 Web 测试读取权限；关联的引用方需要资源组读取权限。依赖读取失败或前后不一致时会阻止清理，目标已不存在也不会跳过检查。同时选择引用方 Monitor 资源和目标时，计划会生成经过分别审查的顺序删除步骤。

解析 Event Hub 接收器需要订阅范围的 Event Hubs 命名空间列举、读取权限；ITSM 接收器同样需要 Log Analytics 工作区列举、读取权限。目标消失或任务重启后，仍会通过已审查的 ARM 身份或经过认证的工作区客户 GUID 匹配引用；跨范围标识不会获得本地资源。已有工作区需重新扫描以保存身份凭证。Function 和非全局 Runbook 的子资源引用会与父资源分别记录，全局 Runbook 动作名称还需要原生 Webhook 映射。参阅微软的[动作组接收器契约](https://learn.microsoft.com/en-us/rest/api/monitor/action-groups/get?view=rest-monitor-2023-01-01)。

托管组清理会检查每个已审查且将被删除的 ARM 成员的传入引用。经验证的同组 Monitor 成员可以随控制器级联删除，包括直接引用控制器的告警或 Web 测试；外部引用仍会阻止清理。检查会重新核对私有配置、资源组身份和接收目标解析结果；控制器与资源组消失后，仍会读取已知残留成员。

托管组发现会通过两次完整的原生 Monitor/预算清单补充通用 ARM 列表，因此需要八类 Monitor 集合及两类预算 API 的读取权限，包括订阅和资源组预算列表。未入库成员需要先扫描；新增成员、配置不一致或集合不可读时会阻止删除。通过原生清单补充发现的 Monitor 成员同样需要逐一确认最终不存在。

ARM 清理恢复现会同时认证完整的已审查请求和原生操作凭证。从未包含此检查的版本升级前，请先完成进行中的清理任务；旧任务凭证无法通过新的恢复校验。发起新的清理尝试需要重新扫描并生成计划。

API Management 清理会审查所选服务或工作区的 API 定义、策略、内容与配置。共享订阅、API 修订和引用方资源通过独立步骤先行删除。删除 API、产品、组、标签或通知关联仅解除该关联；被引用成员保持独立，除非另行选中。内置组、管理员用户、主订阅、邮件模板及固定门户、通知、租户配置需通过所属控制资源清理。保留必要资源会阻止控制资源删除；门户版本正在发布或配置发生变化时也会阻止清理。

清理工作区前，先删除其已审查的独立网关配置连接；共享网关及其他工作区的连接保留。读取权限须覆盖订阅内全部网关、服务工作区链接、适用子集合、GET/HEAD 存在性检查、祖先及引用目标，包括其他资源组。凭据引用还需通过原生订阅清单和详情读取匹配 Key Vault 与托管身份。不会读取 Key Vault 秘密内容、下载外部策略 URL 或执行策略表达式。参见[工作区网关](https://learn.microsoft.com/en-us/azure/api-management/workspaces-overview)。

Azure 将已删除的 API Management 服务保留 48 小时。Steward 不提供恢复、永久清除或邮件模板重置；恢复服务不能撤销先前独立执行的 DELETE 步骤。异步操作完成后仍须确认资源及子资源不存在。验证包含官方模式、CLI 响应重放及固定版本独立模拟器中的部分 APIM 路径，不代表已完成真实 Azure 部署测试。参见[软删除行为](https://learn.microsoft.com/en-us/azure/api-management/soft-delete)。

Azure Batch 清理会审查完整账户层级。池、应用、专用终结点连接、作业及计划按依赖顺序删除；包版本先于所属应用删除。作业或计划的原生删除包含已审查的任务，池的原生删除包含已审查的节点。自动池只有在实际生命周期设置与成员索引共同证明归属时才随作业或计划清理。共享池、包及任务依赖要求明确选择相应使用者的清理范围。

单独移除节点使用池的最新 ETag，并将运行任务重新排队；任务的历史运行位置不会强制删除待保留的任务记录。用户订阅模式会审查原生指向的 Uniform VMSS 实例及其磁盘、扩展和网络资源。VM 身份缺失、磁盘共享或分离、子资源保留或受保护，以及配置变化都会阻止清理。所属伸缩集保持独立。多实例任务清理先终止任务并等待全部子任务，再在删除后检查其工作目录；仅主任务记录消失不足以完成清理。Batch 的原生删除会忽略任务数据保留期。外部存储、Key Vault 和身份保持独立引用；解析引用时不会读取文件内容、存储密钥或保管库内的密钥及机密内容。参见[任务删除](https://learn.microsoft.com/en-us/rest/api/batchservice/tasks/delete-task?view=rest-batchservice-2025-06-01)、[节点移除](https://learn.microsoft.com/en-us/rest/api/batchservice/pools/remove-nodes?view=rest-batchservice-2025-06-01)与[应用包](https://learn.microsoft.com/en-us/azure/batch/batch-application-packages)。

Batch 验证包括原生 Schema、组合 HTTP 场景、官方 CLI 响应回放、应用关系图及重启恢复测试，不代表独立 Batch 模拟器或真实 Azure 部署验证。

- **Cosmos DB**：删除账号、数据库、容器或表会删除其中的数据。计划先审查必要子资源、角色依赖和 Fleet 关联。保留内置角色或客户端加密密钥，需要保留其账号或数据库。删除 Fleet 会解除账号关联，账号本身保留；账号的保护设置或管理锁会阻止解绑。删除前检查吞吐量、备份迁移、配置与子资源成员关系。读取权限须覆盖适用 API 的子集合、吞吐量配置、祖先资源和订阅内引用该账号的 Fleet 关联。账号显示在全局清单，托管 Cassandra 数据中心使用实际部署地域。不提供恢复或永久清除操作。参见[资源模型](https://learn.microsoft.com/en-us/azure/cosmos-db/resource-model)及 [MongoDB 角色](https://learn.microsoft.com/en-us/azure/cosmos-db/mongodb/role-based-access-control)。

- **Azure DocumentDB（原 MongoDB vCore）**：删除集群会删除其中的数据。副本是独立集群，计划先删除已审查的副本，再删除源集群；单独删除副本会保留源集群。防火墙规则、专用终结点连接与 Microsoft Entra 用户注册各有独立删除步骤。保留或保护必要资源会阻止父资源删除。删除集群用户注册不会删除 Entra 身份，也不会执行额外的数据库角色清理。读取权限须覆盖集群、完整子资源与副本清单，以及各引用副本。配置变化或正在进行的复制拓扑变更需要重新扫描或稍后重试。不提供备份恢复或永久清除操作。参见[副本删除规则](https://learn.microsoft.com/en-us/azure/documentdb/troubleshoot-replication)和[身份验证](https://learn.microsoft.com/en-us/azure/documentdb/how-to-connect-role-based-access-control)。

- **Foundry / Cognitive Services**：账号清理先删除模型部署及已审查的依赖，再软删除账号，不提供永久清除。删除能力主机会使依赖的代理状态无法访问；线程、文件与遗留存储数据尚未逐项清理。连接盘点包含数据存储连接；Key Vault 连接需等待账号及项目中的其他连接全部删除。对于声明需要或已启用托管专用终结点的连接，在其影响范围建模完成前会阻止清理。保留必要子资源会阻止控制资源删除；共享承诺计划与引用的存储资源仍独立保留。托管网络清理包含其规则，并验证专用终结点目标的保护状态；部分派生规则需随网络清理。旧账号类型的接口适用范围、托管连接的专用终结点影响及外部边界关联生命周期仍未完成。参见[恢复与计费行为](https://learn.microsoft.com/en-us/azure/ai-services/recover-purge-resources)。
- **Azure Data Explorer（Kusto）**：计划先删除已审查的数据库资源及其他必要子资源，再删除集群。源数据库或集群仍被跟随时，须先删除跟随集群中已审查的挂接；跟随集群本身保留。挂接控制其本地只读数据库视图，保留视图会阻止解绑。活动自定义映像随集群清理。托管私有终结点须校验目标配置、管理锁与保护状态；外部数据源保持独立。读取权限须覆盖全部子集合、祖先资源、跟随索引和链接目标。删除脚本不会撤销其已经执行的命令。Azure 可能将集群软删除并保留 14 天，但恢复集群不能撤销之前单独执行的数据库删除步骤。Steward 不提供恢复或跳过软删除操作。参见[跟随行为](https://learn.microsoft.com/en-us/azure/data-explorer/follower)、[脚本说明](https://learn.microsoft.com/en-us/azure/data-explorer/database-script)与[集群删除](https://learn.microsoft.com/en-us/azure/data-explorer/delete-cluster)。
- **Stream Analytics**：删除作业会永久移除其输入输出定义、函数和查询，外部数据存储保留。转换须通过其所属作业清理；单独删除输入、输出或函数要求作业处于 Created、Stopped 或 Failed 状态。删除集群前先清理已审查的私有终结点。关联作业保持独立：须明确选中它们一同删除，或者先在 Azure 中停止并将待保留作业移出集群，再重新扫描。Steward 不会自动停止、解绑或删除未选中的作业。读取权限须覆盖全部子集合、集群作业成员、父资源和链接目标；私有终结点须检查目标配置、管理锁和保护状态。参见[作业清理](https://learn.microsoft.com/en-us/azure/stream-analytics/stream-analytics-clean-up-your-job)与[移出集群](https://learn.microsoft.com/en-us/azure/stream-analytics/manage-jobs-cluster)。

- **Azure AI Search**：删除服务会删除其搜索内容。专用终结点连接与共享私有链接须先审查并删除；保留其中任一资源或边界配置视图都会阻止服务删除。共享私有链接删除还会修改目标资源的连接元数据，因此须检查目标原生读取、继承锁和保护状态。目标数据资源独立保留。Cosmos DB 账号已有原生目标检查；尚未建模的目标、跨订阅链接以及外部边界关联生命周期仍未完成。读取权限须覆盖全部子集合及各链接目标。参见[共享私有链接删除行为](https://learn.microsoft.com/en-us/azure/search/troubleshoot-shared-private-link-resources)。
- **Redis**：删除缓存或数据库会删除其中的数据。独立子资源必须先删除，经典内置策略随缓存清理。选择经典复制的任一缓存，会将主侧共享解链接操作及可能存在的反向视图纳入审查；保留链接视图会阻止解链接。读取权限须覆盖订阅内的经典缓存及复制对端，包括其他资源组。Enterprise 主动复制会检查全部成员；后续删除仅在离组成员均返回 404 后接受更小的复制组。新增或矛盾的成员关系、不健康的链接、配置变化、锁和受保护对端都会阻止清理。退化复制组需先单独恢复，Steward 不会强制解链接。完成时还会检查存活副本不再引用删除目标。参见[经典复制](https://learn.microsoft.com/en-us/azure/azure-cache-for-redis/cache-how-to-geo-replication)与[主动复制](https://learn.microsoft.com/en-us/azure/redis/how-to-active-geo-replication)。
- **管理锁**：订阅、资源组、资源本身及相关子资源的锁会阻止删除。Steward 在盘点和实际删除前检查锁，不会移除锁。
- **托管资源**：云服务拥有的资源需要通过受支持的控制资源清理，具有明确独立删除能力的成员除外。AKS 删除包含经过审查的节点资源组；仍禁止任意删除托管资源组中的资源。
- **VM 挂载资源**：计划展示原生自动删除的磁盘、网卡和公网 IP。支持的保留操作在删除前执行带条件的原生更新，并支持工作进程重启恢复。VM 扩展仍属于 VM 的删除影响。Uniform 伸缩集的非托管 VHD 清理和磁盘解除挂载尚未实现。
- **存储**：存储账户对应的服务必须没有 Blob 容器、文件共享、队列或表。Blob 容器不能含有 Blob、快照、版本、软删除条目或未提交上传。法律保留与不可变策略会阻止清理。Steward 不会清空或永久清除数据来满足删除条件。Blob 清理目前要求使用标准 `ACCOUNT.blob.core.windows.net` 端点。
- **VNet 与 DNS 区域**：必须先移除必要的子网和私有 DNS 链接。选择 VNet 不会隐式删除未选择的子网或链接。
- **消息服务**：删除命名空间、主题和订阅可能删除其中的消息与配置。所有已建模子资源均须经过审查，并在执行后逐一确认不存在。单独删除实体时也会检查当前复制配置，因为删除可能传播至配对命名空间。复制活动期间仍禁止单独删除实体；命名空间清理会先处理已审查的灾难恢复或迁移配置。
- **Event Hubs 专用集群**：原生成员清单必须与每个命名空间的 `clusterArmId` 一致，包括位于其他资源组的成员。选择集群会纳入成员命名空间及经审查的后代资源；保留成员会阻止删除集群。执行前重新校验集群配额、成员关系、命名空间创建身份、锁与保护设置；删除集群前及重启后的完成检查都要求所有前置命名空间经原生读取确认不存在。权限需覆盖集群成员清单、配额设置和各命名空间的生命周期读取。Azure 要求集群至少存在四小时；已知未满足该时长的集群会暂时标记为受保护。参见[原生命名空间清单](https://learn.microsoft.com/en-us/rest/api/eventhub/clusters/list-namespaces?view=rest-eventhub-2024-01-01)和[集群删除说明](https://learn.microsoft.com/zh-cn/azure/event-hubs/event-hubs-dedicated-cluster-create-portal#delete-a-dedicated-cluster)。
- **异地灾难恢复**：清理已配对的主别名时，先等待复制完成，调用原生 BreakPairing，确认角色为 `PrimaryNotReplicating` 且配对关联已清空，再删除别名。计划审查两侧 ARM 别名及授权视图，并逐一确认不存在。选择任一命名空间都会纳入这一共享前置步骤，同时选择两侧也只执行一次；未选择的命名空间及其中实体会保留。单独选择副别名时，计划提示选择其主别名作为控制资源。保留任一别名视图会阻止配对清理。持久化准备阶段及重启后会重新核对两侧命名空间、资源组、锁与保护状态。只有名称的配对目标通过订阅级原生清单解析，因此需要命名空间列表权限，以及所选区域之外配对命名空间和别名的读取权限。Steward 不执行故障转移。参见微软的[配对与解除配对说明](https://learn.microsoft.com/en-us/azure/event-hubs/configure-geo-disaster-recovery)。
- **Service Bus 迁移**：清理会等待迁移同步完成，通过原生 Revert 操作中止复制，确认目标关联已清空，再删除配置。清理源或目标命名空间时，计划都会纳入这一前置步骤；同时选择两端时，迁移配置只删除一次。仅清理源时保留目标及其中实体，仅清理目标时保留源及其中实体。执行中会重新核对源和目标的创建身份、迁移配置、权限、锁与保护设置；正在提交迁移或状态未知时阻止清理。准备阶段会持久化，支持工作进程重启恢复。Steward 不会提交迁移。检测迁入配置需要订阅内所有 Service Bus 命名空间及迁移配置的读取权限，包括所选区域之外的命名空间；清单缺失或原生列表不可读会阻止计划或执行。参见[微软的迁移行为说明](https://learn.microsoft.com/zh-cn/azure/service-bus-messaging/service-bus-migrate-standard-premium)。
- **并发变更**：删除前重新核对原生创建标识；服务资源树还会核对版本、成员清单、锁与保护设置。资源被重建或出现未审查的子资源时，需要重新扫描和生成计划。
- **App Service**：删除应用时显式保留 App Service Plan；需要删除计划时，应单独选择。
- **异步操作**：Steward 跟踪 ARM 返回的操作状态，再重新读取资源确认其不存在。失败或取消的操作仍记为失败，不启用强制删除或永久清除选项。
- **托管 Grafana**：删除工作区需要三类子资源的原生读取、列举和删除权限；保留子资源会阻止工作区删除。Steward 核对原生配置与父资源身份，先执行已审查的子资源删除，再核验子资源和工作区均不存在。连接的数据源、AKS 集群和用户侧私有终结点仍作为独立资源保留。SMTP 密码不进入盘点和日志。原生 API 没有条件删除请求头，配置检查无法防止同时发生的外部修改。参阅[工作区](https://learn.microsoft.com/en-us/rest/api/managed-grafana/grafana/delete?view=rest-managed-grafana-2025-08-01)、[托管专用终结点](https://learn.microsoft.com/en-us/rest/api/managed-grafana/managed-private-endpoints/delete?view=rest-managed-grafana-2025-08-01)和[集成配置](https://learn.microsoft.com/en-us/rest/api/managed-grafana/integration-fabrics/delete?view=rest-managed-grafana-2025-08-01)原生操作。

执行前审查[清理选择与结果](./cleanup.md)。删除数据库或容器注册表可能删除其内部数据。Azure 权限、保留策略、依赖关系和并发变更仍可能阻止操作。参阅微软的[管理锁](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/lock-resources)、[VM 删除设置](https://learn.microsoft.com/en-us/azure/virtual-machines/delete)和[异步操作说明](https://learn.microsoft.com/en-us/azure/azure-resource-manager/management/async-operations)。

Monitor 数据采集需要订阅范围的规则、终结点列举与读取权限、两个方向的关联反查权限，以及被监控资源范围内的关联读取权限。盘点通过限定当前订阅的 Resource Graph 查询补充发现孤立关联，再以原生 GET/ListByResource 核实。索引存在延迟，且仅返回有读取权限的资源；完整订阅盘点需要覆盖整个订阅的读取权限。无位置信息的孤立关联归入全局范围。清理还需要所选规则或终结点的删除权限，以及关联所在范围的关联删除权限。移除关联会停止对应的数据采集。计划会纳入每个必要的解绑步骤，在两个目标间共用一次关联删除；保留必要关联会阻止目标删除。规则删除显式使用 deleteAssociations=false，并通过原生读取确认前置关联已不存在。Blob 引用 URL 的凭据不会进入盘点和日志。租户全局 monitoredObjects 关联仍未支持。参阅[关联操作](https://learn.microsoft.com/en-us/rest/api/monitor/data-collection-rule-associations?view=rest-monitor-2024-03-11)和[规则删除](https://learn.microsoft.com/en-us/rest/api/monitor/data-collection-rules/delete?view=rest-monitor-2024-03-11)。

这些扩展能力已保留原生 HTTP 协议测试及未修改的官方响应样例。Grafana 和 Monitor 数据采集还重放了微软 CLI 的删除录制响应；较早的录制 API 版本及补充的最终不存在响应均在证据文档中说明。这不代表 Steward 完成了独立模拟器或真实云验证。

删除 Azure Monitor 工作区还会删除默认摄取托管资源组及组内全部资源。计划会审查完整影响，包括尚未识别的组内资源类型；保留任何组成员都会阻止删除。两个原生默认摄取资源 ID 及任何 managedBy 值必须相互一致。工作区数据不支持软删除恢复。私有连接属于冻结的工作区配置，引用的外部网络终结点不会仅因被引用而纳入托管组。参阅[微软的工作区管理说明](https://learn.microsoft.com/en-us/azure/azure-monitor/metrics/azure-monitor-workspace-manage)。

Container Instances 容器组支持原生清单与删除。普通容器和初始化容器共享组的生命周期，外部 Azure Files 文件共享保持独立。子网、托管身份和返回的 Log Analytics 资源 ID 作为依赖引用。执行时核对已审阅的配置，敏感值通过与连接绑定的摘要比较；轮换凭据后需要重新扫描。命令、配置值和凭据不会以明文写入清单或日志。删除返回 HTTP 200 后仍须原生读取确认资源消失。参见[容器组删除约定](https://learn.microsoft.com/en-us/rest/api/container-instances/container-groups/delete?view=rest-container-instances-2025-09-01)。

CDN 与 Front Door 根据 SKU 使用各自的原生子资源集合。清理前审查全部所属资源，保留级联成员会阻止删除。单独删除域名、源站组、规则集或证书引用时，计划会加入必须先删除的路由、规则或关联；多个目标共用一项前置删除。删除整个配置时，已审查的同一次级联可以同时移除其内部引用。外部源站、Key Vault 数据、DNS 区域和 WAF 策略保持独立。经典终结点仍在引用源站组时，必须先更新路由配置或选择清理终结点，才能删除源站组。清点和清理需要配置及相关子集合的原生读取、列举权限，包括引用方的路由和安全关联。签名异步操作及资源、子资源最终缺失回读支持重启恢复。参见原生[配置删除契约](https://learn.microsoft.com/en-us/rest/api/cdn/profiles/delete?view=rest-cdn-2025-04-15)和[规则集清理说明](https://learn.microsoft.com/en-us/azure/frontdoor/standard-premium/how-to-configure-rule-set)。

Front Door 批量模式的规则显示在规则集内，并随整个规则集保留或删除。要保留这些规则，请保留整个规则集。批量规则中的源站组覆盖配置会将引用它的规则集加入前置删除步骤；使用该规则集的路由也必须先删除。经典模式仍支持单条规则删除，并在执行前重新核对父规则集。参见微软的[批量规则管理指南](https://learn.microsoft.com/en-us/azure/frontdoor/rule-set-batch)。

删除 WAF 策略也会删除其内嵌规则。引用该策略的 CDN 终结点或 Front Door 安全关联必须先经审查并删除；保留引用方会阻止策略删除。经典 Front Door 前端或路由引用仍需在 Steward 外解除。清点需要策略与引用方的读取权限，清理还需要各自的删除及操作状态查询权限。执行前重新核对策略配置、锁和全部剩余关联；成功响应后仍须确认资源最终不存在。参见 [Front Door 策略删除契约](https://learn.microsoft.com/en-us/rest/api/frontdoorservice/webapplicationfirewall/policies/delete?view=rest-frontdoorservice-webapplicationfirewall-2025-11-01)。

App Service 清理会审查部署槽、函数、应用证书和主机名绑定。保留子资源会阻止应用或部署槽删除；默认主机名随所属应用或部署槽清理，服务计划保持独立。独立删除证书需要读取所有应用及部署槽的 TLS 状态和主机名绑定；任何匹配的证书 ID 或指纹都会阻止删除。请先移除相关绑定并重新扫描。通过部署包运行的函数可能不支持单个删除，Steward 会保留 Azure 返回的错误。清点和清理需要子资源的原生读取、列举权限，以及所选操作的删除权限。参见[应用删除契约](https://learn.microsoft.com/en-us/rest/api/appservice/web-apps/delete?view=rest-appservice-2025-05-01)与[部署包行为](https://learn.microsoft.com/en-us/azure/azure-functions/run-functions-from-deployment-package)。

注册域名及其所有权标识显示在全局清单。盘点与清理需要域名和标识的原生列举、读取权限，订阅范围的 App Service 列表、应用/部署槽详情与主机名绑定读取权限，以及资源组和管理锁读取权限。仅为已审查的步骤授予域名、标识和绑定的删除权限。选择域名后，计划先删除其所有权标识及关联的应用/部署槽主机名绑定；应用、证书、服务计划和 DNS 区域保持独立。DNS 区域仍被注册域名引用时，须显式同时选择该域名，或先更改 DNS 托管并重新扫描。

删除域名会释放注册，其他人可能重新购买该名称。Steward 保留 Azure 的购买锁，并使用 `forceHardDeleteDomain=false`，遵循原生 24 小时删除延迟。已保存的等待阶段最长允许 48 小时，可在 worker 重启后恢复。已删除、已审查的应用绑定若仍出现在主机名索引中，系统会继续等待；未知分配仍会阻止删除。DELETE 回执或列表遗漏不能证明完成，域名及每个已知依赖都须分别读取确认不存在。联系人信息、转移授权及所有权令牌值不进入清单与日志；配置变化后须重新扫描和生成计划。域名 DELETE 契约不提供 If-Match 条件，无法阻止预检之后同时发生的外部修改。参阅微软的[域名管理与取消说明](https://learn.microsoft.com/en-us/azure/app-service/manage-custom-dns-buy-domain)及已固定版本的 [DomainRegistration API 契约](https://github.com/Azure/azure-rest-api-specs/blob/c20bf553ad64f20c6d5e3f56080380c086cb1fde/specification/domainregistration/resource-manager/Microsoft.DomainRegistration/DomainRegistration/stable/2024-11-01/openapi.json)。

Azure Monitor 专用链接范围（AMPLS）清理会审查两类原生子资源清单和专用链接能力描述。范围资源关联与专用终结点连接都有独立删除步骤；保留其中任何一个都会阻止范围删除。关联的 Log Analytics 工作区、Application Insights 组件和使用方网络终结点保持独立。配置核对覆盖访问模式及连接级例外。原生 DELETE 接口没有条件删除头，读取与删除之间仍存在并发修改窗口。

删除 Application Insights 组件、Log Analytics 工作区或 DCE 前，Steward 会核对订阅内完整的 AMPLS 清单、关联的原生列表与详情，以及目标资源的反向引用。因此需要读取这些范围的权限，包括所选资源组之外的范围。缺失盘点、读取失败及跨订阅反向引用都会阻止删除；跨订阅关联需在所属订阅中解除后重新扫描。Monitor 工作区托管组中的 DCE 也执行相同检查。相对操作地址经校验后绑定到所选连接和资源，并持久化保存；异步成功后仍须确认资源及前置关联均已不存在。验证使用固定版本的官方样例和组合协议测试，尚未进行 AMPLS 独立模拟器或真实云验证。参阅[AMPLS 关联要求](https://learn.microsoft.com/en-us/azure/azure-monitor/fundamentals/private-link-configure#connect-resources-to-the-ampls)。

Application Insights 盘点覆盖组件、共享及个人分析项、持续导出、收藏、工作项配置、API Key、Profiler 关联存储及注释。八类子资源支持独立清理，并核对组件、资源组、锁及最终原生 GET 不存在状态；共享存储保持独立。组件删除会先移除已审查的子资源及 AMPLS 关联，包括指向当前托管工作区的关联。

共享工作簿、私有工作簿和工作簿模板支持独立盘点与清理。工作簿发现会读取四个官方类别，以及从 ARM 或已保存 ID 中发现的自定义类别；类别清单遗漏不会把已保存工作簿标记为不存在；成功扫描只会关闭自身原生 GET 已确认不存在的记录。模板使用完整资源组枚举。扫描需要订阅资源及资源组列表、锁和原生工作簿 LIST/GET 权限，共享工作簿还需要历史版本 LIST/GET 权限。完整内容与历史版本保持私密，删除前会重新核对；内容变更后需重新扫描和生成计划。私有工作簿 API 不可用等原生错误会使扫描失败。

工作簿删除仅确认活动资源不存在，不代表永久擦除。Azure 通常会保留已删除工作簿约 90 天。自带存储（BYOS）工作簿没有服务托管的历史版本或回收站恢复能力，能否恢复可能取决于存储的软删除配置。引用的来源资源、存储账户或容器及托管身份是独立依赖，不会被工作簿清理自动选中。若原生证据确认工作簿属于托管资源组，则可委托给控制资源一同清理，仍核对内容和历史版本，并单独确认最终 GET 不存在。清理需要所选资源的原生 DELETE 权限；接口不提供条件删除头，读取与删除之间仍有并发修改窗口。参阅[工作簿管理](https://learn.microsoft.com/en-us/azure/azure-monitor/visualize/workbooks-manage)和 [BYOS 行为](https://learn.microsoft.com/en-us/azure/azure-monitor/visualize/workbooks-bring-your-own-storage)。

注释发现使用 Azure 滚动 90 天限制内的固定窗口，每次扫描还会按 ID 重读已保存的注释，包括窗口之外的记录。窗口查询遗漏不会把旧记录标记为不存在。成功扫描仅在注释自身的原生 GET 确认不存在后关闭已保存记录，组件消失后也须逐项确认。组件清理将近期和已保存注释列为独立前置步骤；保留注释会阻止删除。读取失败或含义不明确的 GET 数组会阻止清理，空数组不会被视为不存在的证明。Steward 无法枚举原生窗口之外从未发现的历史记录，组件删除可能一并移除这些历史记录。盘点和清理需要注释 LIST/GET 权限，选中的注释还需要 DELETE 权限。参阅[原生注释接口](https://learn.microsoft.com/en-us/python/api/azure-mgmt-applicationinsights/azure.mgmt.applicationinsights.v2015_05_01.operations.annotationsoperations?view=azure-python)。

组件扫描还会读取完整资源组索引，以及当前托管工作区的组成员和 AMPLS 关联。因此需要资源组、资源列表、成员产品详情和 AMPLS 读取权限，包括组件资源组以外的托管组。归属须由组件的工作区引用与资源组 `managedBy` 共同确认，不能仅凭名称推断；共享工作区和切换后留下的旧托管组会分别记录。读取失败或索引不一致会使扫描失败，跨订阅引用不会授权跨订阅读取。组件删除包含已审查的当前托管组及已知后代。只有组件和资源组消失，且每个已知成员的原生 GET 均确认不存在，才会完成。锁或 Azure 策略可能留下资源组；Steward 会继续等待，不会独立删除托管工作区。切换后留下的旧组和共享工作区会保留；若组内嵌套控制资源还有未建模的外部托管组，则阻止清理。参阅[托管工作区行为](https://learn.microsoft.com/en-us/azure/azure-monitor/app/managed-workspaces)。

组件盘点还会读取当前计费功能、每日上限、定价计划、配额状态及旧版主动检测设置，需要授予这些组件接口的读取权限。删除前会重新核对可编辑设置，包括私密通知收件人；设置变化后需要重新扫描和生成计划。配额等只读运行数据的变化不会使计划失效，私密收件人不会进入盘点和日志。这些设置随组件清理，没有独立删除操作。迁移后的智能检测告警规则及其动作组独立盘点和删除，参阅[迁移说明](https://learn.microsoft.com/en-us/azure/azure-monitor/alerts/alerts-smart-detections-migration)。

Kubernetes Fleet 清单需要原生 `Microsoft.ContainerService/fleets/read`、七类子资源读取权限，以及资源组和管理锁读取权限。系统逐个核验已知资源；Fleet 被列表遗漏或已不存在，都不能证明子资源已删除。代理子资源使用 Fleet 所在地域，托管命名空间保留原生地域。命名空间注释和放置表达式不进入清单与日志；动态放置会明确标记，不声称已验证实际成员集合。多数 Fleet 操作使用稳定版 API `2026-06-01`；成员读取和 Cluster Mesh 操作使用 `2026-06-02-preview`，以核验实际网络成员关系。成员引用支持 AKS 和 Arc Kubernetes 集群。更新运行保留策略副本，Gate 指向其所属运行。参阅 [Fleet 常见问题](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/faq)。

原生生命周期图已记录 Fleet 各类配置的独立清理关系，以及 Gate 对关联更新运行的归属。删除 AKS 集群、子网和用户分配托管身份时，也会读取当前连接订阅中相关的 Fleet 集合，检查尚未进入清单的引用来源；仍有引用时阻止删除。动态命名空间放置会保守地要求先清理命名空间，再移除该 Fleet 中的任何成员。即使选择的是其他资源类型，这些检查也需要 Fleet 读取权限；不会因此赋予 Fleet 删除成员集群或共享网络的权限。

Fleet 成员、托管命名空间、更新运行、更新策略和自动升级配置已支持原生条件删除，需要相应子资源的删除权限。状态为 `Running`、`Pending` 或 `Skipped` 的更新运行还需要停止权限；已经处于停止过程中的运行不会重复发送 Stop。系统确认运行进入终止状态后才删除，并逐个核验运行及已审查 Gate 的消失。轮询和执行阶段可在 worker 重启后恢复。

Cluster Mesh 清理会先断开已审查的跨集群网络，再删除其配置，因此会中断跨集群连接和服务发现。清单通过未过滤的原生成员列表及逐项读取，核验成员的 `meshProperties`；标签匹配不能证明实际连接。系统等待正在进行的 Apply 完成，保留其他配置并将成员选择器设为空，应用断开操作，确认没有成员仍然连接后才删除。这需要配置的读取、写入、Apply、删除权限及成员读取权限。成员保护或管理锁、新增连接、配置变化及无法读取的残留会阻止推进。最终通过配置自身的 404 和已知连接的逐项核验确认完成。成员及其集群保留；清理已连接的成员时，必须显式先清理其 Mesh 配置。参阅[跨集群网络删除说明](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/howto-configure-use-cross-cluster-networking#delete-a-cross-cluster-network)。

Fleet 根资源扫描通过托管资源组的 `managedBy`、Fleet 与 AKS 的 API 地址，以及 AKS 的 `nodeResourceGroup` 和该组反向指向 AKS 的归属信息，核验 Hub 归属。随后遍历两个资源组，展开原生子资源和有文档支持的外部后代，并补查通用 ARM 列表遗漏的 Monitor 资源。这需要未过滤的资源组和组内资源列表、成员原生读取及子资源列表，以及相关的订阅 Monitor 和 DNS 索引权限。归属缺失或有歧义时保留为未核实状态，命名规则不能证明归属。已认证的历史标识通过逐项读取找回列表遗漏；未知类型的资源被遗漏或外部归属证据不完整时，扫描不会完成。RBAC 分配和诊断设置仍需独立清理。Fleet 根资源记录仅保存 Hub 和成员配置的私有摘要；独立资源保留各自产品的正常清单字段。生命周期图已将经核验的 Hub 集群、两个托管资源组及其后代统一归属 Fleet，避免再归属 AKS 或挂载资源控制器。清单中缺失的资源保留为待解析引用；证明或原生状态变化会阻止关系图重建。已核验 Hub 归属或确认不含 Hub 的 Fleet 支持根资源清理。选择 Fleet 后，计划会列出原生子配置的独立删除步骤；逐项读取确认它们不存在后，才发送根资源 DELETE。根资源还需要 `Microsoft.ContainerService/fleets/delete` 权限，并重新核验完整 Hub 影响范围、归属、配置、保护设置和管理锁。Hub 级联仅通过 Fleet DELETE 触发。Fleet 消失后，两个托管组及每个已知类型的后代仍须逐项读取确认 404，包括外部托管磁盘和 DNS 资源；未知类型的组内资源依赖所属组确认不存在。要求保留资源、Hub 归属未核实或残留读取失败时，清理不能完成。共享资源及加入 Fleet 的成员集群仍保持独立。参阅 [Hub 集群说明](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/concepts-lifecycle)。

托管命名空间清理沿用已审查的 `deletePolicy`：`Keep` 移除 ARM 管理并保留 Kubernetes 命名空间；`Delete` 删除 Hub 和成员集群上的命名空间及其内容。两种策略都会删除关联的 Azure RBAC 分配。策略或放置配置变化后，需要重新扫描和审查。移除 Fleet 成员只解除成员关系，不会删除其引用的 AKS 或 Arc Kubernetes 集群。Arc 集群和其 Kubernetes 扩展不归 Fleet 所有；跨订阅或清单中缺失的集群保留为待解析引用。参阅[支持的成员类型](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/quickstart-create-fleet-and-members)。参阅[命名空间删除说明](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/howto-managed-namespaces#delete-a-managed-fleet-namespace)和[更新运行状态](https://learn.microsoft.com/en-us/azure/kubernetes-fleet/concepts-update-orchestration#update-run-states)。

Azure Local VM 清理需要 `Microsoft.AzureStackHCI/virtualMachineInstances/delete`。即使只扫描 VM，发现和审查阶段也需要读取两类 Local 访客单例、引用的系统盘，以及 Arc 扩展、运行命令和许可证配置集合，请授予对应的原生读取权限。访客/Arc 前置资源需各自的删除权限；身份元数据和系统盘在 VM 删除后通过自身 GET 验证。系统会检查 VM 及所有受影响资源的资源组保护和管理锁，包括位于其他资源组的系统盘。

系统盘纳入 VM 删除影响。[微软更正后的支持答复](https://learn.microsoft.com/en-us/answers/questions/5758576/what-happen-with-associated-data-disk-with-azure-l)称，工程团队确认系统盘会随 VM 删除，而数据盘保留。保留或保护系统盘、访客资源或 Arc 前置资源会阻止 VM 删除。清理前会通过原生机器/VM 读取检查系统盘是否被其他 VM 使用，也会补读此前已观察到但本次父级索引遗漏的 VM。只有 VM、系统盘与身份元数据各自确认不存在，动作才会完成；操作回调成功不足以关闭受影响资产。若原生 VM 响应没有已注册系统盘的 ID，则没有可单独验证的磁盘资产，VM 删除提示仍会说明系统盘将被移除。

清理提示区分系统盘删除、数据盘/网卡保留以及单独执行的 Arc 注册清理。测试覆盖原生 SDK 合约、组合 VM/Arc 协议、保护与保留、引用变化，以及带重启恢复的 SQLite 扫描、图谱、计划和执行链。物理 VM/磁盘移除、真实回调兼容性和计费终止仍需云端验证。Local 网络、网卡、数据盘、存储路径和镜像的独立清理尚未开放。

Azure Local 注册清理需要 `Microsoft.HybridCompute/machines/delete`，并在 VM 自身清理完成后执行。HCI 机器扫描还会读取原生 VM 实例、两类 Local 访客单例、已登记系统盘和三类 Arc 子集合。签名后的 VM 上下文在 VM 消失后仍绑定原注册身份，使后续扫描可以继续清理注册；从未获得已验证 VM 上下文的 HCI 主机仍受保护。注册被替换、读取不可用、VM 被保留或保护，以及 VM、访客、身份元数据或系统盘仍存在，都会阻止删除或完成。父资源消失或操作返回成功均不能代替子资源自身的不存在证据。网卡和数据盘仍是独立资源。
