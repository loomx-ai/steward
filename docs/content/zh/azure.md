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
2. 执行**全部启用地域与全局资源**扫描。Steward 使用各产品原生列表和详情 API 发现支持的资源，同时保留 Azure Resource Manager 的广泛盘点结果。
3. 检查扫描覆盖与错误。权限或分页失败不会被视为原有资源已经消失。
4. 打开地域中的 VNet，查看子网、网卡、VM 和关联资源。VM 的网络位置通过网卡解析。

原生发现覆盖 VNet 子网、Blob 容器、SQL 数据库、伸缩集实例、DNS 记录、Service Bus 实体和 Event Hubs 消费者组等子资源。资源组显示在全局清单中；Azure 为资源组记录的地域用于存放资源组元数据。完整 ARM ID 保留订阅与资源组边界，匹配时不区分大小写，不同资源组中的同名资源不会混淆。

## 盘点与清理范围

Steward 识别 155 类资源，其中 143 类具有原生删除操作，执行时受下列条件约束。ARM 返回的其他资源类型作为只读清单展示。覆盖范围仍在扩展，尚未完整覆盖 Azure 的所有产品。

| 产品 | 资源 | 清理能力 |
| --- | --- | --- |
| Compute | VM 与扩展、托管磁盘、快照、托管镜像、可用性集、专用宿主机、容量预留 | 支持，受挂载与归属保护限制；宿主机组和容量预留组先删除成员 |
| VM Scale Set | Uniform、Flexible 伸缩集及实例、扩展 | Uniform 成员纳入级联影响；Flexible VM 作为前置删除步骤 |
| 虚拟网络 | VNet、子网、网卡、网络安全组、路由表、公网 IP、公网 IP 前缀、NAT Gateway | 支持 |
| 负载均衡 | Load Balancer、Application Gateway | 支持 |
| Storage | 存储账户、Blob 容器 | 仅空资源 |
| SQL | 逻辑服务器、数据库、弹性池 | 服务器清理包含已审查的数据库与弹性池；禁止独立删除 `master` |
| PostgreSQL / MySQL | Flexible Server | 支持 |
| App Service | Web App / Function App、部署槽、函数、证书、主机名绑定与服务计划 | 应用/部署槽审查所属子资源的级联影响；服务计划保持独立；证书须无 TLS 绑定 |
| CDN 与 Front Door | 配置、经典终结点/源站/源站组/域名；Front Door 终结点/路由/源站组/源站/域名/规则集/规则/安全关联/证书引用 | 配置及所属子资源审查级联影响；共享引用按依赖顺序清理 |
| WAF | CDN 与 Front Door 策略 | 先删除已审查的引用终结点或安全关联，再删除策略；经典 Front Door 引用仍阻止清理 |
| 容器 | 容器注册表、Container App、Container Instances 容器组、AKS | AKS 清理审查节点资源组及已知的嵌套、外部托管资源 |
| DNS 与私有终结点 | 公有/私有 DNS 区域及记录、私有 DNS 链接、Private Endpoint 与 DNS 区域组 | 级联审查包含已验证的托管网卡和外部 DNS 记录；系统 DNS 记录不可独立删除 |
| Virtual WAN 网关 | VPN/ExpressRoute 网关、连接、VPN NAT 规则及链路 | 显式编排连接和 NAT 的前置删除；VPN 链路由连接管理 |
| Service Bus | 命名空间、队列、主题、订阅、规则、授权规则、灾难恢复别名、迁移配置、私有终结点连接 | 支持原生操作及经过审查的命名空间/实体级联；迁移清理先中止复制再删除；配对别名经审查后先解除配对再删除 |
| Event Hubs | 专用集群、命名空间、事件中心、消费者组、授权规则、灾难恢复别名、架构组/应用组、私有终结点连接 | 集群清理先删除经审查的成员命名空间；支持命名空间/事件中心级联及配对别名的解配对流程 |
| 运维与身份 | Log Analytics 工作区、用户分配的托管身份 | 支持 |
| 托管 Grafana | 工作区、托管专用终结点、专用终结点连接、集成配置 | 先删除经过审查的子资源，再删除工作区；各资源也支持独立原生操作 |
| Monitor 工作区 | 工作区及默认摄取托管资源组 | 审查组内全部资源，先解绑外部关联，再验证组和已知资源均已消失 |
| Monitor 数据采集 | 规则、终结点及被监控资源上的关联 | 先删除经过审查的关联，再删除规则或终结点；共享关联只执行一次 |
| 尚待实现生命周期的集合资源 | 资源组、Key Vault、Container Apps 环境 | 只读 |

Service Bus/Event Hubs 网络规则集、Event Hubs 网络边界配置、灾难恢复别名的授权视图、Uniform 伸缩集网络资源和 VPN 连接链路没有独立原生删除操作。默认命名空间授权规则 `RootManageSharedAccessKey` 也必须随命名空间删除。这些资源会纳入所属控制资源的删除影响；保留这类内置子资源会阻止删除所属控制资源。资源组、Key Vault 和 Container Apps 环境的清理仍未实现。

Service Bus 自动转发目标通过原生 API 解析为同一命名空间内的队列或主题。Event Hubs Capture 记录目标存储账户和 Blob 容器依赖。删除命名空间不会自动选择这些存储资源、用户分配的身份或独立的 Private Endpoint。盘点和执行权限必须包含所有已审查子资源的原生读取权限；子资源列表失败不代表命名空间为空。参阅微软的[自动转发](https://learn.microsoft.com/en-us/azure/service-bus-messaging/service-bus-auto-forwarding)和 [Capture](https://learn.microsoft.com/en-us/azure/event-hubs/event-hubs-capture-overview) 文档。

## 清理保护

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
