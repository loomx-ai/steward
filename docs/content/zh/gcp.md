---
title: "Google Cloud（GCP）"
description: "连接项目、扫描 Google Cloud 资源，并审查支持的清理操作。"
navTitle: "Google Cloud"
---

## 连接项目

每个 GCP 连接通过服务账号 JSON 密钥访问一个项目。服务账号可以来自另一个项目，但必须有权访问目标项目。

1. 在目标项目启用 **Cloud Asset Inventory**、**Cloud Resource Manager** 和 **Compute Engine API**。使用清理功能前，还需启用对应产品的 API。
2. 为服务账号授予目标项目上的 `cloudasset.assets.listResource`、`resourcemanager.projects.get` 和 `compute.regions.list` 权限，用于盘点和地域发现。原生产品盘点还需要相应产品的列表和读取权限；仅对准备清理的资源补充删除及操作状态查询权限。清理存储桶还需要 `storage.objects.list`。
3. 打开 **设置 → 云连接 → 添加连接**，选择 **Google Cloud**，填写 **项目 ID** 和服务账号的 **JSON 密钥**。
4. 验证连接并刷新地域，按下面的首次扫描步骤核对结果。

Steward 会验证项目访问权限和资源盘点权限。连接验证成功不代表拥有所有清理 API 的权限。密钥使用部署环境的凭据加密密钥加密保存；轮换时使用「替换凭证」，目标项目必须保持一致。

如需管理 Cloud Identity 身份组，请填写可选的**组目录**，例如 `customers/C01234567` 或 `identitysources/source-1`，并启用 Cloud Identity API。服务账号还需要对应目录的组管理权限；项目权限本身不足以授权。Google 支持[无需全域委派的服务账号 Groups Admin 配置](https://docs.cloud.google.com/identity/docs/how-to/setup)。扫描时包含全局范围；更换目录或失去可见性不会把旧观测误标为已删除。

Steward 支持标准 Google Cloud 端点的服务账号 JSON 密钥，不会使用机器上已有的 `gcloud` 凭据或元数据服务凭据。参阅 Google 的[服务账号密钥管理建议](https://docs.cloud.google.com/iam/docs/best-practices-for-managing-service-account-keys)和[Cloud Asset Inventory 列表权限](https://docs.cloud.google.com/asset-inventory/docs/reference/rest/v1/assets/list)。

管理层级防火墙策略时，还需在连接中填写可选的**防火墙范围**，例如 `organizations/123` 或 `folders/456`。该设置明确纳入对应组织或文件夹及其下级文件夹中的防火墙策略；项目访问权限不会自动启用此范围。服务账号需具备该层级的 Resource Manager 读取权限和原生防火墙策略权限，扫描时需包含全局范围。移除或修改此设置不会把历史策略记录判为已删除，旧清理计划也需重新审查。

## 完成第一次扫描

1. 切换到新建的 Google Cloud 连接，核对目标项目 ID。
2. 选择一个包含已知资源的地域，运行扫描；例如在 `us-central1` 中查找一个已知 VM。
3. 查看扫描详情，处理权限或 API 未启用的失败项，再核对资源名称、项目、地域与可用区。
4. 检查完成后，选择 **全部启用地域 + 全局** 进行完整盘点。全局 VPC、全局地址等资源需要包含全局范围。

**完成标志：** 能找到预期资源，所属项目和位置正确，扫描没有尚未处理的失败项。Cloud Asset Inventory 存在收集延迟，新建资源可能需要稍后重扫。

## 按资源属性搜索

选择资源类型后，搜索补全会显示它支持的属性字段。已有库存会在下一次成功扫描后获得新增属性。例如：

```text
type = "compute.googleapis.com/StoragePool" AND properties.provisionedCapacityGiB = "20480"
type = "compute.googleapis.com/FirewallPolicy" AND properties.shortName = "hierarchical-policy"
type = "tpu.googleapis.com/QueuedResource" AND properties.lifecycleState = "ACTIVE"
type = "run.googleapis.com/Service" AND state = "CONDITION_FAILED"
type = "sqladmin.googleapis.com/Instance" AND tags.team = "analytics"
type = "bigquery.googleapis.com/Table" AND properties.name = "events" AND properties.numRows = "9007199254740993"
type = "compute.googleapis.com/SslCertificate" AND properties.managedStatus = "PROVISIONING_FAILED"
type = "discoveryengine.googleapis.com/Document" AND properties.indexedAt = "2026-08-01T13:00:00Z"
type = "cloudidentity.googleapis.com/Membership" AND properties.memberId = "member@example.test"
```

容量、数量等原生 64 位整数字符串需要使用引号包裹查询值。防火墙策略的 `properties.name` 是原生数字名称，`properties.shortName` 是显示名称。TPU 排队资源保留结构化的 `properties.state`，可用 `properties.lifecycleState` 查询其中的状态值。各资源类型仍可使用顶层 `state` 字段搜索状态；API 不提供资源状态时，该值为空。例如，网络和防火墙规则没有原生状态或标签属性。路由返回 `routeStatus` 时会映射为可搜索状态。托管证书状态、PSC 连接状态和负载均衡迁移状态使用各自的属性名，不代表资源的整体健康状态。参见[路由](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routes)和[证书](https://docs.cloud.google.com/compute/docs/reference/rest/v1/sslCertificates)的 API 字段。

BigQuery 数据集与表的 `properties.name` 是短 ID；不同数据集中的同名表通过完整资源 ID 区分。扫描会在列举后读取原生详情，以获得加密配置、行数和字节数等属性。除列举权限外，连接还需要 `bigquery.datasets.get` 和 `bigquery.tables.get`。详情访问被拒绝、资源消失或身份不一致时，该扫描分片失败并保留已有库存。参见[数据集](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/datasets/get)和[表](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/tables/get)的详情方法。

Cloud Identity 身份组、成员关系及 Resource Manager 组织的 `properties.name` 保留原生资源名。身份组与组织的显示名称使用 `properties.displayName`，成员标识使用 `properties.memberId`。GKE 标签来自原生集群或节点池配置。

Discovery Engine 文档的 `properties.indexedAt` 是索引状态中的时间戳；索引错误文本和文档内容仍被遮蔽。目标网站提供 `properties.indexingStatus`，会话提供 `startTime` 与 `endTime`，而非创建时间。Infrastructure Manager 资源变更提供 `properties.intent`。KMS 的 `properties.primaryState` 仅表示 CryptoKey 主版本的状态；密钥版本和导入任务不会继承密钥标签。参见[文档索引字段](https://docs.cloud.google.com/generative-ai-app-builder/docs/reference/rest/v1/projects.locations.collections.dataStores.branches.documents)和[资源变更意图](https://docs.cloud.google.com/infrastructure-manager/docs/reference/rest/v1/projects.locations.previews.resourceChanges)。

Bigtable 表扫描会在列举后读取完整原生元数据，包括列族、复制状态、备份策略和删除保护。因此连接除了 `bigtable.tables.list`，还需要 `bigtable.tables.get`。详情无权限、资源消失或身份不一致时，扫描分片失败并保留之前的观测。单表删除与实例级联清理也会读取完整元数据并遵守表的删除保护。参见原生[列表](https://docs.cloud.google.com/bigtable/docs/reference/admin/rest/v2/projects.instances.tables/list)与[详情](https://docs.cloud.google.com/bigtable/docs/reference/admin/rest/v2/projects.instances.tables/get)方法。

## 盘点与清理范围

Steward 通过产品原生 API 盘点下表中的资源，Cloud Asset Inventory 用于补充发现其他类型，作为只读资源展示。产品扫描分片失败时会明确报告，也不会据此认定资源已不存在。

| 产品 | 识别的资源 | 清理能力 |
| --- | --- | --- |
| Compute Engine | VM 实例、可用区与地域级持久磁盘、快照、镜像、实例模板、托管实例组、实例组和自动扩缩器 | 支持 |
| Hyperdisk 存储池 | 原生池、容量与性能用量、预配模式和磁盘成员 | 支持盘点和经审查的清理 |
| VPC | 网络、子网、防火墙规则、路由、Cloud Router | 支持 |
| Cloud Router | 原生路由器配置、NAT 影响和策略／集合前置步骤 | 支持审查后的父资源删除及 NAT 级联 |
| Cloud NAT | 各路由器的公共/私有 NAT 配置、规则及子网/IP 引用 | 支持独立删除，保留其他 NAT 和所属路由器 |
| Cloud Router 命名集合 | 各路由器的前缀/社区集合、CEL 元素和指纹 | 审查引用后删除，先清理引用它的策略 |
| Cloud Router BGP 策略 | 各路由器的导入/导出策略、CEL 条款和指纹 | 支持解除 BGP 引用后原生独立删除 |
| Cloud Identity | 配置目录中的身份组及成员关系 | 审查后删除身份组；普通成员关系也可独立删除 |
| Resource Manager | 沿文件夹父级链发现当前项目所属的组织 | 只读；公开的 v3 API 没有组织删除方法 |
| 防火墙策略 | 配置范围内的层级策略、全局和地域级网络策略及原生关联 | 先解除审查过的关联，再删除策略；也支持独立解除关联 |
| 负载均衡与地址 | 地域和全局 IP 地址、转发规则、地域和全局后端服务、健康检查（含旧版 HTTP/HTTPS）、目标池、网络端点组、URL Map、HTTP/HTTPS 代理、SSL 证书 | 支持 |
| Cloud Storage | 存储桶 | 仅空桶 |
| Pub/Sub | 主题、订阅 | 支持 |
| Cloud SQL | 实例 | 关闭删除保护后支持 |
| Cloud Run | 服务 | 支持 |
| Artifact Registry | 仓库 | 支持 |
| Cloud Monitoring 指标范围 | 当前连接项目的指标范围、被监控项目关联及其他范围的反向引用 | 支持解除选中的项目关联；保留范围及其自身项目关联 |
| Cloud TPU | 节点、排队资源和原生预留容量 | 节点与排队资源支持审查后清理；数据盘先解绑并保留；预留容量只读 |
| Data Fusion | 实例、DNS 对等连接和命名空间 | 实例清理纳入审查过的命名空间与 DNS 对等连接；DNS 对等连接也支持独立删除 |
| Infrastructure Manager | 部署组、部署、修订、资源记录、预览及变更/漂移记录 | 支持审查后的部署组、部署与预览清理；子级元数据没有独立删除操作 |
| Batch | 作业、任务记录 | 删除作业时取消运行中的工作，并核实任务、VM 和磁盘影响；任务没有独立删除接口 |
| Discovery Engine | 集合、数据存储、应用、架构、控制规则、服务配置、会话、对话、助手、文档分支与文档、目标网站 | 审查影响后执行原生清理；分支和网站搜索配置随数据存储删除 |
| Dataproc | 集群、作业、辅助节点组、自动伸缩策略和工作流模板 | 审查后清理集群；默认保留作业历史；辅助节点组没有独立删除接口 |
| Dataform | 文件夹、团队文件夹、仓库、工作区、发布与工作流配置、工作流执行、编译结果 | 按审查后的计划清理文件夹和仓库；先取消运行中的执行；编译结果随仓库删除 |
| Secret Manager | 全局和地域级密钥 | 支持 |
| Google Kubernetes Engine | 集群、节点池 | 审查成员影响后，由原生控制器执行清理 |
| Cloud KMS | 密钥环、密钥、版本、导入任务 | 删除满足条件的资源记录；导入任务只读 |

Google 对部分地域和全局资源使用不同的类型名，例如 `RegionDisk`、`GlobalAddress` 和 `GlobalForwardingRule`。资源完整名称保留项目、地域和可用区信息，不同可用区的同名 VM 不会合并。

资源盘点具有最终一致性。新建或删除的资源可能需要一段时间才会反映到 Cloud Asset Inventory 中，立即重扫也可能看到旧数据。执行清理时，Steward 直接查询产品 API、等待异步操作结束并确认资源不存在，不会把旧盘点结果当作删除成功的依据。参阅 Google 的[资源类型与数据时效说明](https://docs.cloud.google.com/asset-inventory/docs/asset-types)。

组织盘点需要对每一级父文件夹和组织的 Resource Manager 读取权限。读取失败或项目移动时，历史组织记录会保留；项目的组织归属不会授权组织级清理。参阅 Google 的[组织 API](https://docs.cloud.google.com/resource-manager/reference/rest/v3/organizations)和[独立组织生命周期指南](https://docs.cloud.google.com/resource-manager/docs/delete-standalone-org)。

## 全局 VPC 与地域子网

Google VPC 可以跨地域。Steward 在各地域的网络视图中展示同一个 VPC 边界，以及该地域的子网和关联资源；全局资源也可通过全局视图查看。

清理 **某个地域内的 VPC 分组** 时，只选择该地域中的资源，并保留共享的全局 VPC。删除 VPC 本身时，应将它作为独立全局资源，与剩余依赖一起审查。网络扫描可包含所选地域的资源及其引用的全局网络资源。其他项目中的 Shared VPC 资源需要单独连接，不会自动合并为跨项目清理。

## 删除保护

- **Cloud Identity 身份组：** 删除组会清理计划中的成员关系，保留成员用户、服务账号和嵌套组对象。锁定组受保护；动态组成员由 Google 管理，Steward 不会逐项删除。组配置或成员关系变化会阻止旧计划。组删除不可恢复，并会改变访问权限；外部 IAM 绑定及其他产品中的引用需另行审查。单独出现权限错误不能证明删除成功。参阅 Google 的[组删除契约](https://docs.cloud.google.com/identity/docs/reference/rest/v1/groups/delete)和[身份模型](https://docs.cloud.google.com/architecture/identity/overview-google-authentication)。
- **防火墙策略：** 清理时先逐项调用原生 API 解除审查过的关联，再删除策略及其规则。解除关联会改变目标的防火墙规则应用情况，网络、组织或文件夹本身仍保留。目标必须仍在配置范围内；策略、关联或目标身份变化会阻止旧计划执行。层级策略还会查询目标侧关联列表。API 无法把这些读取与删除绑定为原子操作，同名关联也没有创建标识；清理期间应避免并发修改。参阅 Google 的[层级策略指南](https://docs.cloud.google.com/firewall/docs/manage-hierarchical-firewall-policies)和[全局网络策略指南](https://docs.cloud.google.com/firewall/docs/use-network-firewall-policies)。
- **VM 磁盘和托管实例组**：磁盘与 IP 的原生删除策略会进入影响计划。对支持保留的资源，Steward 先通过原生操作修改策略并验证生效，再删除控制器。挂载关系或资源身份发生变化时会停止执行。
- **删除保护**：获准清理 VM 后，Steward 会通过明确的原生准备阶段解除其删除保护；保护标签和 Cloud SQL 的删除保护仍会阻止删除。
- **存储桶**：非空桶会被拒绝删除。Steward 不会先清空对象或对象版本来满足删除条件。
- **指标范围**：解除项目关联会改变范围可以查询的指标，但会保留被监控项目、时序数据、仪表板和告警配置。范围及其自身项目关联只读；其他项目中的反向关联需要在各自连接中清理。参阅 Google 的[指标范围配置说明](https://docs.cloud.google.com/monitoring/settings/multiple-projects)。
- **Infrastructure Manager**：部署清理会审查当前修订创建的资源及其嵌套原生影响。原生策略可以删除这些资源，或全部保留；两种情况下都会删除部署及修订元数据。预览清理仅删除预览元数据。不支持部分保留；无法完整识别或映射的 Terraform 资源需要显式指定 `retain_all_resources=true`。执行服务账号和源文件存储桶仍作为依赖保留。参阅 Google 的[部署删除说明](https://docs.cloud.google.com/infrastructure-manager/docs/delete-deployments)。
- **部署组**：清理会审查当前引用的部署，以及上次成功组修订中存在、此后已从配置移除的部署和实际资源。先解除资源配置，再删除部署组及修订元数据。`retain_all_resources=true` 会保留实际资源并删除部署元数据；显式保留全部被引用的 Deployment 则也会保留这些部署。不支持部分保留。参阅 Google 的[部署组说明](https://docs.cloud.google.com/infrastructure-manager/docs/deployment-groups)。
- **Cloud TPU**：清理排队资源时，先删除计划中的节点。节点清理会先解绑已有数据盘，确认磁盘仍然存在，再删除节点及其启动盘；所有节点消失后再删除排队请求。网络资源和预留容量作为独立资源保留。参阅 Google 的[排队资源删除契约](https://docs.cloud.google.com/tpu/docs/reference/rest/v2/projects.locations.queuedResources/delete)。
- **Data Fusion**：实例清理包含计划中已审查的命名空间和 DNS 对等连接，并等待原生操作完成及资源消失。策略不可读、新增子资源或配置变化会阻止清理。用户数据及引用的存储、网络、服务账号、密钥和主题作为独立资源保留。参阅 Google 的[实例删除说明](https://docs.cloud.google.com/data-fusion/docs/how-to/delete-instance)。
- **Batch**：选择作业后，一起审查其任务、VM 和磁盘。清理通过原生作业删除接口执行，并等待相关资源消失。已挂载且 `autoDelete=false` 的已有磁盘会保留；要求保留 Batch 创建的磁盘，或外部磁盘的删除设置不安全时，会停止清理。实例模板、存储桶、NFS 数据、密钥、Pub/Sub 主题、日志和输出数据仍作为独立资源保留。参阅 Google 的[作业删除行为](https://docs.cloud.google.com/batch/docs/delete-job)。
- **Discovery Engine**：删除应用会保留其关联的数据存储。删除数据存储前，必须先删除或解除所有关联应用；Steward 不会解除未选中应用的关联。计划会包含已发现的子配置、会话和文档。数据存储删除可能持续数天，任务会等待原生操作及已审查资源消失，重启后继续核实。清理集合时先删除其中的应用和数据存储。参阅 Google 的[数据存储删除要求](https://docs.cloud.google.com/generative-ai-app-builder/docs/delete-a-data-store)。
- **Dataproc**：集群清理会审查托管 VM、实例组、生成的实例模板和磁盘。默认保留作业历史；同时选中作业记录时，会先取消活动作业并删除记录，再删除集群。已挂载且 `autoDelete=false` 的已有磁盘、存储桶、自动伸缩策略及外部服务仍作为独立资源保留。虚拟集群清理会保留其 GKE 集群和节点池。参阅 Google 的[集群删除契约](https://docs.cloud.google.com/managed-spark/docs/reference/rest/v1/projects.regions.clusters/delete)和 [GKE 清理行为](https://docs.cloud.google.com/managed-spark/docs/guides/dpgke/quickstarts/gke-quickstart-create-cluster)。
- **Dataform**：清理仓库时，先删除其中的工作区、发布与工作流配置、执行记录，再删除仓库及已审查的编译结果。运行中的执行会先取消，并等待其进入终态。新增成员、配置变化或依赖读取失败会阻止清理。Git 远程仓库、引用的密钥和 BigQuery 输出表作为独立资源保留；取消执行不会回滚已完成的 BigQuery 操作。参阅 Google 的[仓库删除约束](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.repositories/delete)和[取消执行行为](https://docs.cloud.google.com/dataform/docs/reference/mcp/tools_list/cancel_workflow_invocation)。

Dataform 文件夹清理会纳入其中的嵌套文件夹和仓库，逐一删除成员后再删除父文件夹。任一祖先文件夹被移动或重建，也会阻止子资源操作和取消流程恢复。文件夹搜索结果受连接权限限制；因共享权限变化而不可见的资源会保留记录，直到删除得到核实。详情参阅 Google 的[团队文件夹搜索约束](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.teamFolders/search)。

- **GKE**：计划包含已核实的节点与网络资源影响。删除集群前，会等待 Kubernetes Service、Ingress 和 Gateway 的 finalizer 完成。持久卷、已有 IP 和证书按已验证的策略保留；不支持的保留要求或无法确认的归属会阻止执行。控制平面端点必须可达；盘点需要对 `kube-system` Namespace 的 `get` 权限，以及对 Service、Ingress、已安装 Gateway 资源的 `list` 权限。清理还需要这些工作负载的 `delete` 权限、Container/Compute 的原生读取、删除和操作查询权限，以及节点组成员查询权限。不需要读取 Kubernetes Secret。

执行前检查实际选择范围和[清理结果](./cleanup.md)。产品权限、保留策略、资源依赖和云端状态变化仍可能导致操作无法完成。

指标范围盘点需要当前连接项目的 `resourcemanager.projects.get` 权限。解除被监控项目关联时，范围项目和被监控项目都需要 `monitoring.metricsScopes.link`，并需能够读取返回的 Monitoring 操作。盘点读取完整范围，范围不可读不能证明关联消失。执行前及等待期间会再次核对范围和关联的创建时间；API 不支持原子的创建时间或 etag 条件，清理期间应避免并发重新关联。参阅原生[读取](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/locations.global.metricsScopes/get)与[解除关联](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/locations.global.metricsScopes.projects/delete)契约。

Infrastructure Manager 需要 `config.locations.list`，以及 deployments、revisions、resources、previews、resourcechanges 和 resourcedrifts 对应的原生 `get` / `list` 权限。清理还需要 `config.deployments.delete` 或 `config.previews.delete`、`config.operations.get`，以及实际资源的读取和列举权限。Terraform 使用部署的服务账号和源配置，两者必须保持有效。参阅 [Config 权限索引](https://docs.cloud.google.com/iam/docs/roles-permissions/config)。

部署组还需要 `config.deploymentgroups` 和 `config.deploymentgrouprevisions` 对应的原生读取、列举权限，以及清理所需的 `config.deploymentgroups.deprovision` 和 `config.deploymentgroups.delete`。如果修订结果未知或无法唯一确定成功历史，清理会停止，直到能够确认资源影响。

如果子资源仍需修改 VM/MIG 保留策略或删除保护、处理 GKE 工作负载与网络 finalizer，或解绑 TPU 数据盘，部署与部署组清理会停止。目前尚未把这些准备步骤与 Terraform 销毁组合执行；可以先保留全部已创建资源，在部署移除后另行审查清理。Terraform 自身的保护和删除策略也可能阻止销毁。最终检查会核实元数据与实际资源的结果，重启后继续执行；这些 API 没有原子的配置条件，清理期间应避免并发修改。

Dataform 盘点需要 `dataform.locations.list`，以及仓库、工作区、发布配置、工作流配置、工作流执行和编译结果各自的 `list` / `get` 权限。清理还需对可独立删除的类型授予 `delete`，取消运行中的执行另需 `dataform.workflowInvocations.cancel`。检查只读取密钥引用，不读取密钥内容。完整权限名称参阅 [Dataform 权限索引](https://docs.cloud.google.com/iam/docs/roles-permissions/dataform)。

文件夹发现和清理还需要对可访问的目录树授予 `dataform.folders.get`、`dataform.teamFolders.get` 和 `dataform.folders.queryContents`；删除所选文件夹另需相应的 `dataform.folders.delete` 或 `dataform.teamFolders.delete`。

Batch 盘点需要 `batch.locations.list`、`batch.jobs.list`、`batch.jobs.get`、`batch.tasks.list` 和 `batch.tasks.get`。清理另需 `batch.jobs.delete`、`batch.operations.get`，以及用于核实影响的 `compute.instances.list`、`compute.instances.get`、`compute.disks.list` 和 `compute.disks.get`；使用实例模板的作业还需要 `compute.instanceTemplates.get`。检查不会读取所引用的密钥内容。参阅 [Batch 权限索引](https://docs.cloud.google.com/iam/docs/roles-permissions/batch)。

Dataproc 盘点需要 `compute.regions.list`、`dataproc.clusters.list` / `dataproc.clusters.get`、`dataproc.jobs.list` / `dataproc.jobs.get`、`dataproc.nodeGroups.get`，以及策略和模板对应的 `list/get` 权限。集群清理另需 `dataproc.clusters.delete`、`dataproc.operations.get`，以及用于核实 VM、磁盘、实例组和模板的 Compute 读取与列举权限。所选作业需要 `dataproc.jobs.cancel` / `dataproc.jobs.delete`；所选策略和模板需要各自的删除权限。参阅 [Dataproc 权限索引](https://docs.cloud.google.com/iam/docs/roles-permissions/dataproc)。

Dataproc 检查会绑定集群 UUID 和已审查的配置，但其 API 无法同时锁定作业和 Compute 成员。清理期间请保留服务写入的身份元数据和标签，并避免并发修改集群。额外的 Compute 自动伸缩器或有状态实例组策略会使清理停止，等待重新审查。删除工作流模板不会取消已经由该模板启动的工作流。

Discovery Engine 使用 `global`、`us` 和 `eu` 位置；即使 Cloud Asset Inventory 尚未发现资源，地域选择器也会提供 US/EU。连接需要所选类型的原生 `list` / `get` 权限、集合与祖先资源的读取权限；有连接器的集合另需 `discoveryengine.dataConnectors.get`，网站数据存储还需网站配置与 sitemap 的读取权限。清理另需所选类型的 `delete` 和 `discoveryengine.operations.get`。参阅[权限索引](https://docs.cloud.google.com/iam/docs/roles-permissions/discoveryengine)。文档正文、会话内容、提示词、架构和连接器配置会在存储盘点结果或日志前脱敏，配置一致性检查使用脱敏前的数据。

Data Fusion 盘点需要 `datafusion.locations.list`、`datafusion.instances.list`、`datafusion.instances.get`、`datafusion.namespaces.list` 和 `datafusion.namespaces.getIamPolicy`。实例清理需要 `datafusion.instances.delete` 和 `datafusion.operations.get`；独立删除 DNS 对等连接使用 `datafusion.instances.update`。参阅[原生方法权限](https://docs.cloud.google.com/data-fusion/docs/how-to/audit-logging)和[权限索引](https://docs.cloud.google.com/iam/docs/roles-permissions/datafusion)。私有选项和命名空间策略会在配置一致性检查后脱敏。

Data Fusion 的支持范围使用管理 API，单个 CDAP 流水线、数据集、安全存储条目及其运行时计算资源尚未纳入此流程。命名空间记录随实例清理；Steward 不会为独立删除命名空间开启不可恢复重置。这些删除接口没有原子的配置条件，清理期间应避免并发修改。参阅 [CDAP API 文档](https://docs.cloud.google.com/data-fusion/docs/reference/cdap-reference)。

这些 Discovery Engine 删除接口不支持以配置或创建 ID 作为原子删除条件，清理期间应避免并发修改。分支和网站搜索配置没有独立删除接口。上述范围不会单独盘点预览版 agent/runtime 资源或每一种数据记录；删除所属应用或数据存储时，也会删除其中的服务数据。

## 常见问题

| 现象 | 处理方法 |
| --- | --- |
| JSON 密钥无法验证 | 确认使用服务账号 JSON 密钥，内容完整，且服务账号未停用、密钥未撤销。 |
| 提示 API 未启用或 `SERVICE_DISABLED` | 在目标项目启用报错中指出的 API，然后重试。 |
| 连接验证或扫描返回 `403` | 核对目标项目上的服务账号授权；跨项目服务账号也必须获得目标项目权限，并检查组织策略。 |
| 没有找到刚创建的资源 | 检查项目和扫描地域，查看失败项，并等待 Cloud Asset Inventory 更新后重扫。 |
| 相同名称出现多个 VM | 核对项目和可用区；不同位置的同名资源有不同完整资源名称。 |
| 清理因保护设置被阻止 | 按任务中的原因检查 VM、Cloud SQL、磁盘 `autoDelete` 或存储桶内容；不要只修改本地记录。 |

下一步：[扫描资源](./scans.md) · [查看资源关系](./topology.md) · [清理资源](./cleanup.md)

Cloud TPU 盘点需要 `tpu.locations.list`、`tpu.nodes.list`、`tpu.nodes.get`，以及读取已挂载数据盘的 `compute.disks.get`。排队资源 API 复用节点权限，[预留容量列表](https://docs.cloud.google.com/tpu/docs/reference/rest/v2alpha1/projects.locations.reservations/list)也需要 `tpu.nodes.get`。清理另需 `tpu.nodes.update`、`tpu.nodes.delete` 和 `tpu.operations.get`。参阅 [TPU 权限索引](https://docs.cloud.google.com/iam/docs/roles-permissions/tpu)。节点和模板的私有元数据在配置一致性检查后脱敏。

这些 Cloud TPU 写入接口不支持以配置或资源创建身份作为原子条件，清理期间应避免并发修改。数据盘保留用于确保磁盘资源仍在，不会创建备份。上述范围使用 Cloud TPU API；包括 TPU7x 及后续版本在内的 Compute Engine/GKE TPU 使用[独立管理接口](https://docs.cloud.google.com/tpu/docs/tpus-in-compute-engine)。


## Hyperdisk 存储池

盘点使用 Compute 原生聚合列表，将各可用区映射到扫描区域，保留预配容量、IOPS、吞吐量、写入／使用容量、磁盘数量、预配模式、Exapool 容量及共享设置。大整数保持原生精度。磁盘的存储池引用只是普通依赖，不代表级联删除关系。

盘点需要 `compute.storagePools.list` 和 `compute.storagePools.get`（用于成员列表和池复查）。权限失败或部分列表会使扫描失败，并保留已有记录。成员发现遍历 `storagePools.listDisks` 的所有分页，保留磁盘容量、已用字节、IOPS、吞吐量、挂载实例及快照策略。成员磁盘须属于池所在可用区，分页结束后会复查池的创建身份。共享到其他项目的磁盘会保留成员摘要，当前连接不会因此读取或管理外部项目。成员读取失败时保留已有记录；磁盘资源自身仍由磁盘扫描确认存续。

Hyperdisk Balanced 和 Throughput 存储池支持经审查的删除。可选择池中的本地磁盘，或选择负责删除这些磁盘的 VM、MIG、GKE 控制器；计划不会自动选择控制器。清理会先确认每块磁盘已不存在，再删除池；若计划决定保留成员磁盘，则禁止删除其存储池。配置或成员变化后需重新扫描。快照独立保留。Exapool 以及仍有跨项目成员的池需要在外部完成相应清理，之后重新扫描。参见 [Google 存储池管理指南](https://docs.cloud.google.com/compute/docs/disks/manage-storage-pools)。

删除还需要 `compute.storagePools.delete`、`compute.zoneOperations.get`，成员磁盘清理需要 `compute.disks.get` / `compute.disks.delete`。有效的未来预留可能阻止原生删除，Steward 不会自动取消预留。清理期间应避免并发修改池：原生删除 API 不支持按资源 ID 或 etag 进行条件删除。容量和性能池化不能据此认定与阿里云的物理资源独享完全等价。

安全指挥中心服务盘点会分别显示预期启用状态、实际生效状态，以及模块设置和更新时间。
继承配置的实际状态可能因开通状态或计费资格而不同；`INGEST_ONLY` 表示仅接收发现结果，
服务本身未启用。Steward 对这些设置提供只读盘点，不据此推断订阅套餐。
请启用 **Security Center Management API**，并授予
`securitycentermanagement.locations.list`、
`securitycentermanagement.securityCenterServices.list` 和
`securitycentermanagement.securityCenterServices.get`。项目扫描使用服务自身的地域列表；
全局设置需要包含全局范围。详情读取失败会保留上次观测。地域或服务从列表中暂时消失时，
保留旧记录及其原有观测时间；重新可见后继续更新状态。若更新前创建的待执行扫描失败，请重新启动扫描。参阅
[服务设置契约](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/organizations.locations.securityCenterServices)和
[读取权限](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/projects.locations.securityCenterServices/get)。

服务盘点还会在项目可见的相同地域中读取其祖先文件夹和组织的设置。
`configurationParent` 标明配置所属层级，与项目实际生效的设置分别保留。
需要在这些祖先上授予服务 LIST/GET 权限，并授予 `resourcemanager.projects.get`、
`resourcemanager.folders.get` 和 `resourcemanager.organizations.get` 来核验祖先链。
每页读取绑定已核验的父级关系；权限失败或项目迁移会使该页失败，保留原有观测。
不再可见的祖先设置保留原观测时间。这不涵盖其他项目或组织中的所有私有地域，
也不会修改任何祖先设置。

已知 `projects/.../locations/.../clusters/.../securityCenterServices/...` 名称时，
可调用原生 `securitycentermanagement.projects.locations.clusters.securityCenterServices.get`。
读取会核验连接项目、返回身份和设置，并继续脱敏；使用相同的
`securitycentermanagement.securityCenterServices.get` 权限。自动集群服务枚举及 GKE
标识映射仍待核实，此能力不代表已验证所有集群的威胁检测覆盖。

组织订阅盘点展示 Security Command Center 当前套餐，以及最近一次订阅的类型、开始时间和结束时间。
最近一次订阅可能已经结束；这些时间不代表当前仍在使用付费套餐，Steward 会单独保留原生套餐字段。
盘点沿当前项目的祖先关系读取所属组织，并在读取后复核祖先关系。项目迁移或权限丢失会保留之前的记录。
订阅记录只读，不代表项目自身的独立计费权益。

请启用 **Security Command Center API**，在组织上授予 `securitycenter.subscription.get`，
并授予祖先发现所需的 `resourcemanager.projects.get`、中间文件夹的
`resourcemanager.folders.get` 和 `resourcemanager.organizations.get`。扫描需包含全局范围。
权限失败或订阅返回不存在都会使扫描失败。参见
[订阅接口契约](https://docs.cloud.google.com/security-command-center/docs/reference/rest/v1beta2/organizations/getSubscription)
和 [Security Command Center 权限](https://docs.cloud.google.com/iam/docs/roles-permissions/securitycenter#securitycenter.subscription.get)。

项目计费盘点展示各项目／地域上显式设置的 Security Command Center 套餐，单独保留该值，
不据此推断组织继承套餐、试用期或到期时间。全局和区域记录使用不同身份。项目扫描使用服务原生地域列表，
读取失败、返回身份变化或原地域从可见列表中消失时保留已有记录。请启用 **Security Center Management API**，授予
`securitycentermanagement.locations.list` 和 `securitycentermanagement.billingMetadata.get`，
并在扫描中包含全局范围以读取全局设置。这些记录只读。参见
[项目计费接口契约](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/projects.locations/getBillingMetadata)。

组织计费通过连接项目的已核验祖先链单独读取，使用相同的项目可见地域。
`configurationParent` 区分项目和组织的值。需要在组织上授予
`securitycentermanagement.billingMetadata.get`，并具备 Resource Manager 的项目、
文件夹和组织 GET 权限。祖先或地域不再可见时保留原记录及观测时间；权限失败、
资源缺失或身份变化会使该页失败。原生接口没有文件夹计费单例；组织计费值也不用于
推断项目实际权益或试用历史。


## Cloud Router 路由策略

扫描路由策略需要目标项目的 `compute.routers.list`、
`compute.routers.listRoutePolicies` 和 `compute.routers.getRoutePolicy` 权限。
Steward 在各路由器所属地域分页列举策略，并逐条读取详情；不同路由器下的同名策略
分别保存。详情读取失败时保留上次成功观测；原生列表成功返回为空时，将旧策略标为已不存在。

可用 `type = "compute.googleapis.com/RoutePolicy"` 和
`properties.type = "ROUTE_POLICY_TYPE_IMPORT"` 筛选策略。
参阅 Google 的[策略列表 API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/listRoutePolicies)
和[策略详情 API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/getRoutePolicy)。

独立删除策略还需要 `compute.routers.get`、`compute.routers.deleteRoutePolicy`
和 `compute.regionOperations.get` 权限。创建清理任务前先重新扫描，记录策略指纹及
父路由器 ID 和 BGP 对等体配置；其他配置变化后需重新审查，已完成的同级策略移除
按下述规则处理。`bgpReferences` 列出受影响的
对等体及导入/导出方向。Steward 调用[原生策略删除 API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/deleteRoutePolicy)，
在重启后继续等待地域级操作，并在同一父路由器仍可读取的前提下确认策略已不存在。
异步操作结束本身不能证明删除结果已可见；父路由器不可读取时会报告依赖读取失败。

对等体引用所选策略时，Steward 会先从相应导入/导出列表中移除该策略名，保留其他
策略及原有顺序。这一步使用 [Router PATCH](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/patch)，
还需要 `compute.routers.update`；Google 的[审计权限列表](https://docs.cloud.google.com/compute/docs/logging/audit-logging)
还列出了路由器所在网络的 `compute.networks.updatePolicy`。请求保留其他对等体配置，
不包含无关路由器字段。地域级操作结束且配置变更可见后，才开始删除策略。
两个阶段均可通过持久化操作记录在重启后继续。

顺序删除多条策略时可以使用同一次扫描结果。更新对等体前，Steward 读取当前配置，
并仅在原生 API 确认先前移除的同级策略已不存在后保留这些移除。新增引用、策略顺序
变化、对等体或其他配置变化，以及同级策略不可读取，都会阻止执行。

同一路由器中选定的 Router、NAT、策略和命名集合删除会按依赖顺序执行，执行并发度
较高时也是如此；不同路由器仍可独立进行。该路由器存在未确认完成的修改时，其他
清理任务不能启动或继续相关修改。

其他配置变化、原生依赖冲突及权限失败会明确报告，供重新审查。这些原生修改没有指纹
前置条件，清理期间应避免并发修改策略或 BGP 对等体。
本操作不会将命名集合或其他策略纳入删除范围。

旧计划在启动或继续前会补齐缺失的顺序依赖，并保留步骤身份及已审查的资源快照。
如果新增依赖涉及尚未结束的工作作业，或已发起但未确认完成的云端操作，会阻止继续。
策略解除引用的操作结束本身不能解除路由器占用，因为后续原生删除阶段的结果也需确认。

对于已失败或取消、所有作业均已结束的执行，Steward 可通过只读核验所有可能发起的
修改阶段已结束，解除路由器占用。已关联策略需要同时确认原 BGP 解绑与策略删除操作，
包括请求已发出但新回执尚未保存的删除；未关联策略或命名集合只需确认单个删除操作。
回执丢失或过期时，除 `compute.regionOperations.get` 外还需
`compute.regionOperations.list` 权限。操作历史缺失、有歧义、不完整或不可读取时，
仍保持阻塞。恢复保留原执行状态，不会重发删除或将资源标为已删除。参阅
[操作查询 API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/regionOperations/list)。

## Cloud Router 命名集合

扫描命名集合需要目标项目的 `compute.routers.list`、`compute.routers.listNamedSets`
和 `compute.routers.getNamedSet` 权限。Steward 独立发现各路由器的集合，包括类型、
描述、CEL 表达式元素和指纹。集合名只在所属路由器内唯一，因此资产标识包含地域及
路由器。读取失败或结果不完整时保留此前观测记录。

可用 `type = "compute.googleapis.com/NamedSet"` 和
`properties.type = "NAMED_SET_TYPE_PREFIX"`（或 `NAMED_SET_TYPE_COMMUNITY`）筛选。
详见[原生集合详情 API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/getNamedSet)。
Google
[禁止删除仍被同一路由器任意策略引用的集合](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/bgp-route-policies/update-named-sets)。

策略扫描会解析原生 CEL 中的 `prefixSets('name')` 和 `communitySets('name')` 调用，
建立对同一路由器内命名集合的依赖。将策略和集合一同扫描，可在关系图中查看这些
依赖；删除策略会保留其引用的集合。字符串、注释不视为调用，表达式不会被执行。
CEL 语法错误或无法解析的计算所得集合名会使该策略扫描分片失败，并保留此前观测。
清理命名集合时会检查所属路由器的所有策略，包括未纳入本地扫描的策略。如果已知
引用策略不在清理范围内，计划会显示阻塞；同时选择策略和集合时，先删除策略。
只删除策略会保留集合。清理需要 `compute.routers.get`、
`compute.routers.listRoutePolicies`、`compute.routers.getRoutePolicy`、
`compute.routers.getNamedSet`、`compute.routers.deleteNamedSet` 和
`compute.regionOperations.get` 权限。Steward 校验扫描时的集合版本和路由器身份，
等待地域操作完成，并确认集合已不存在。策略读取不完整、引用无法解析、资源发生
变化或云端冲突都会停止清理。详见[原生删除 API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/deleteNamedSet)。
选择父 Router 清理时，其策略和命名集合会纳入前置删除步骤。

## Cloud Router 盘点与清理

Router 扫描需要 `compute.routers.list` 和 `compute.routers.get` 权限。
Steward 会逐一读取路由器的当前详情，核实身份后记录 NAT、BGP 和接口配置。
详情读取被拒绝、资源消失、结果不完整或身份不匹配时，该扫描源会失败，并保留
已有观测记录。MD5 认证材料会从库存和 API 日志中脱敏。

选择 Router 清理前，应一同扫描路由器、NAT 配置、策略和命名集合。Steward 会完整
列举并读取原生子资源，核对扫描时的配置；未纳入盘点的子资源会阻止清理。策略和命名
集合成为独立的前置删除步骤，已审查的 NAT 纳入父资源删除影响，不能在删除 Router
的同时保留。单独清理 NAT 的能力仍然保留。

生命周期审查还需要 `compute.routers.listRoutePolicies`、
`compute.routers.getRoutePolicy`、`compute.routers.listNamedSets` 和
`compute.routers.getNamedSet`。父资源删除需要 `compute.routers.delete`、
`compute.regionOperations.get`，以及各前置步骤所需权限。Steward 调用原生
[Router DELETE](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/delete)，
在重启后继续等待已绑定的地域操作，确认父资源和前置资源不存在后记录 NAT 级联结果。
该级联流程不会另外发送 NAT 或地址资源删除请求。

关联的 VPN 隧道和 VLAN attachment 会阻止此清理；应先移除关联资源、重新扫描，
再创建 Router 任务。其他配置变化也需要重新审查。读取与原生删除不是原子操作，
清理期间应避免外部修改。缺少配置审查的旧 Router 任务需要重新扫描后再执行新的删除。参阅 [Router GET 契约](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/get)
和[路由器删除指南](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/managing-routers)。

对于已失败或取消、所有作业均已结束的 Router 执行，确认原生删除操作结束后，可解除
修改占用。新回执恢复会重建已冻结的 NAT 影响和策略／集合前置步骤；子资源审查缺失或
变化时会阻止恢复。可识别的旧格式操作回执则必须由原生操作返回匹配的原请求 UUID、
目标和 Router 数字 ID。这一只读过程不会继续旧删除，也不会将 Router／NAT 清理
标为成功。需要 `compute.regionOperations.get`；新回执丢失或过期、旧回执过期时，
还需 `compute.regionOperations.list`。操作历史缺失、旧回执无法识别或缺少资源 ID
证据时，仍保持阻塞。

## Cloud NAT 网关

扫描 Cloud NAT 需要目标项目的 `compute.routers.list` 和 `compute.routers.get`
权限。Steward 从各路由器的原生 `nats` 数组独立发现网关，包括公共/私有类型、
IP 分配与排空、子网和 NAT64 选择、规则、端口分配及日志设置。可用
`type = "compute.googleapis.com/RouterNat"` 和 `properties.type = "PRIVATE"`
筛选。资源身份包含项目、地域、路由器和 NAT 名称；该盘点身份不代表独立的
REST 接口或 CAI 资产类型。参阅[原生 Router 架构](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers)。

同时扫描相关资源后，可查看路由器、VPC、子网、地址和 NCC Hub 的引用关系。
扫描从原生 CEL 等式提取 `nexthop.hub` 的字符串字面量目标，不把注释或普通字符串
当作引用。对于未指定 Hub 或使用计算表达式的选择器，扫描会完整列举源项目的 NCC
spoke，逐项读取匹配的 VPC spoke，重复成员查询并重新核验 Router。除 Router 读取
权限外，还需要 `networkconnectivity.spokes.list` 和 `networkconnectivity.spokes.get`。
所有匹配 Hub 都保留为可能依赖，不执行数据包匹配表达式；完整空成员清单不添加 Hub。
重复读取可发现已观测到的变化，但不提供原子快照。参阅[Private NAT 配置指南](https://docs.cloud.google.com/nat/docs/set-up-private-nat)。

地域 NAT 按完整云资源身份连接同一连接、同一分区中的全局 Hub；缺失或跨项目 Hub
保留为待解析引用。保留引用 Hub 的 NAT 会阻止删除 Hub；同时选择两者时先清理 NAT，
只选 NAT 则保留 Hub。旧 NAT 记录中的未绑定 Hub 表达式需要重新扫描后才能重建关系。
CEL 语法错误、Router/spoke 响应不完整、权限不足、缺失或状态变化时，扫描分片失败
并保留历史记录；只有完整且身份匹配的 Router 响应才能确认 NAT 已不存在。

清理 NAT 通过原生 Router PATCH 移除已审查的配置，等待地域操作完成，再从完整的
路由器响应确认删除。需要 `compute.routers.get`、`compute.routers.update` 和
`compute.regionOperations.get` 权限。请求保留其他 NAT 的最新配置，并保留 BGP
对等连接、接口、密钥和路由器；该动作不会显式删除手动分配的地址资源。

NAT 删除与父 Router、路由策略及命名集合共用顺序依赖和跨任务协调。其他清理任务
存在未确认完成的更新时，会阻止新任务更新该路由器。对于已失败或取消且没有可运行
作业的 NAT 动作，Steward 在确认
尚未发起更新，或原云端操作已结束后，会解除阻塞。此过程不会将原任务或删除
标记为成功。暂停的任务和无法确认状态的更新仍保持阻塞；失败任务也可在原任务中继续。

操作回执丢失或过期时，恢复还需要 `compute.regionOperations.list` 权限，以查找
原请求对应的操作。记录缺失、结果不完整或读取被拒绝时，仍会保持阻塞。参阅
[原生操作查询契约](https://docs.cloud.google.com/compute/docs/reference/rest/v1/regionOperations/list)。
原生 PATCH 没有配置版本条件，最后一次读取与更新之间仍可能受到外部并发写入
影响；清理期间应避免在外部修改该路由器的 NAT 配置。参阅
[原生 PATCH 与请求 ID 契约](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/patch)。
选择父 Router 清理时，已审查的 NAT 会纳入其删除影响。
Google 明确说明，[删除路由器也会删除其中的 Cloud NAT 网关](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/managing-routers)。

## Cloud Monitoring 正常运行时间检查

Uptime Check 扫描读取项目级原生配置，并逐项核验 LIST 与 GET。保留 HTTP、TCP、
被监控资源或资源组、合成监控、周期、检查地域及用户标签。原生名称作为身份，
显示名称不必唯一。读取失败或配置变化会保留历史观测；只有完整空清单才能确认不存在。

清理需要重新扫描得到的配置审查凭证，并在删除前再次比较可观测配置，删除后通过
GET 确认不存在，工作进程重启后同样复核。原生 DELETE 是同步操作，空响应本身不能
证明资源已消失。请求认证、请求头和正文会从资产及 API 输出中脱敏。原生接口遮蔽
的敏感值无法按明文比较；接口也不提供 etag 条件来防止最后一次读取后的外部修改。

需要 `monitoring.uptimeCheckConfigs.list`、`monitoring.uptimeCheckConfigs.get`
及 `monitoring.uptimeCheckConfigs.delete` 权限。需先删除相关告警策略，否则原生
接口拒绝删除仍被引用的检查。删除合成检查会保留其 Cloud Run 函数。

同时选择检查与目标时，计划先删除检查。目标关系覆盖 GCE 实例数字 ID、合成监控的
Cloud Run 函数、Cloud Run 服务、Service Directory 服务，以及 Kubernetes Service
所在集群；显式内部检查器关联其所在项目的 VPC。网络扫描依据这些身份建立关联，
标签或响应匹配内容中的任意 URL 不会成为网络成员依据。缺失目标保留为未解析引用，
仅选择检查不会自动选择或删除目标。

Monitoring 资源组清单及成员关系、App Engine 和 AWS 目标解析、独立 Kubernetes
Service 关系、旧版隐式内部检查器网络，以及告警条件引用与删除顺序仍待补齐。参阅
[Monitoring 删除接口](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.uptimeCheckConfigs/delete)。


## Cloud Monitoring 告警策略

AlertPolicy 扫描使用项目级原生 LIST 和 GET，也包含禁用或无效策略。保留启用状态、
严重程度、用户标签、条件类型、通知渠道名称及创建/变更记录。说明文档和条件表达式
先参与配置审查，再从持久化资产与 API 输出中脱敏。读取失败、不完整或配置变化时
保留历史观测。

删除需要重新扫描并匹配配置审查凭证。原生删除同步完成，重启后仍通过独立 GET
确认不存在；通知渠道与监控目标不会随之删除。同一连接和项目内的策略写操作按序
执行，未确认结束的请求继续占用写入范围；响应丢失时可继续原任务核验。重复配置的
其他连接及外部客户端不在该协调范围内；接口也没有防止外部并发修改的原子版本条件。

需要 `monitoring.alertPolicies.list`、`monitoring.alertPolicies.get` 和
`monitoring.alertPolicies.delete` 权限。条件引用解析及告警先于 Uptime 的自动顺序
仍待补齐；目前请先选择并完成关联告警策略的删除，再删除其 Uptime 检查。参阅
[原生删除接口](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.alertPolicies/delete)。


## Cloud Billing 预算

预算扫描覆盖连接凭据有权查看的账单账号，包括已关闭账号。资产展示实际账单账号、
预算金额、阈值和所有权范围，并归入连接的全局清单；连接所选项目并非预算的所有者。
通知投递配置和费用筛选条件不会写入资产配置，API 输出中的相关字段也会脱敏。

预算从可见清单中消失不会被视为已删除。Steward 会逐项读取历史预算，并确认其账单
账号仍可正常读取。只有该预算自身返回 404 才能确认不存在；权限错误会保留历史记录。
新扫描且配置匹配的预算可解析通知渠道依赖。邮件渠道清理仍会继续阻断，直到消费方
范围得到完整确认。

此路径需要账单账号可见权限以及 `billing.accounts.get`、`billing.budgets.list`
和 `billing.budgets.get`。扫描也会读取所选项目的账单关联和单项目预算；此项目路径
需要 `resourcemanager.projects.get` 与 `billing.resourcebudgets.read`，在账号清单为空
或拒绝访问时仍可使用。
参阅[原生预算访问控制要求](https://docs.cloud.google.com/billing/docs/how-to/budget-api-access-control)。


预算清理需要重新扫描预算及其账单账号。删除前核对两者的可观测配置，工作进程重启后
仍通过预算自身的 404 确认不存在；账号失权、账号消失或读取被拒绝不会成为删除证据。
通知渠道和费用所属项目会被保留。还需要 `billing.budgets.delete` 权限，不使用关闭
账单账号或删除项目的权限。通过项目权限发现的预算，则复核单项目费用范围和项目的
账单关联，并额外需要 `billing.resourcebudgets.write`。变更账单关联或项目失权会阻断
清理及历史预算的不存在确认；其他账单配置变更需要重新扫描后复核。已可见账号内部的读取
失败仍会使扫描失败并保留历史记录。项目范围的发现不代表邮件渠道消费方范围已完整。
参阅[原生预算删除契约](https://docs.cloud.google.com/billing/docs/reference/budget/rest/v1/billingAccounts.budgets/delete)。

同一连接内对同一账号的预算写入按序执行。响应丢失时请继续原任务核验；无法确认的
失败或取消请求仍保留写入范围。重复配置的连接与外部客户端不在此协调范围内。
原生接口不支持带版本条件的 DELETE，部分 Console 设置也不会返回；外部修改仍可能
发生在最后一次读取之后。


## Cloud Monitoring 资源组

资源组清单包含原生组层级，以及固定过去一分钟内的受监控成员。以组为目标的 Uptime
检查可通过已支持的成员引用接入网络扫描。Compute 实例按不可变数字 ID 匹配，同名
重建的 VM 不会继承旧引用。组筛选条件和成员的描述符标签会脱敏；尚未映射的资源类型
（包括 AWS 成员）保留为未解析的观察。

动态成员关系不代表所有权、保留策略或级联删除。成员读取失败、配置不一致或分页不完整
时，会保留上次观察。资源组清理会绑定复核过的原生配置，并使用非递归删除。读取需要
范围项目上的 `monitoring.groups.list` 与 `monitoring.groups.get`；成员枚举同样使用
`monitoring.groups.get`。参阅[原生组契约](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.groups)
及[成员时间窗口契约](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.groups.members/list)。

依赖刷新还会完整读取本项目的资源组、Uptime 检查、告警策略和仪表板，并比较原生
LIST、GET、再次 LIST 的配置。失权、分页不完整或配置变化会使刷新失败，并保留已有
关系图。已刷新且存在引用的子组、Uptime 检查和策略需要显式选择，不会自动选择成员
或父组。此步骤还使用对应的 `monitoring.uptimeCheckConfigs.list/get`、
`monitoring.alertPolicies.list/get` 和 `monitoring.dashboards.list/get` 权限。

仪表板查询按原生结构检查，包括时间序列筛选和比值分母；文本和日志内容不会当作组
引用。动态 `GROUP` 筛选、模板变量、未知结构及尚未解析的 MQL、PromQL、SQL 查询，
在无法确定引用时会阻断清理。已刷新且确认引用的仪表板可以显式选择，经配置复核后
先于资源组删除。资源组清理会在发送 DELETE 前重新检查消费方。参阅
[Monitoring 组选择器](https://docs.cloud.google.com/monitoring/api/v3/filters)和
[仪表板契约](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards)。

资源组删除还需要 `monitoring.groups.delete`。动作固定使用 `recursive=false`，拒绝
调用方覆盖参数，并通过所选子组、Uptime 检查、策略和仪表板各自的原生 GET 确认其已不存在。
消费方仍存在、配置变化或读取结果不确定时，不会发送 DELETE。写入前完成两轮消费方
复核及最后一次资源组读取，组成员会保留。重启后的回执仍绑定原始资源、配置、前置
删除项和请求键，只通过该资源组自身的 404 确认不存在。

同一连接、同一项目中的组写入按序执行。失败或取消请求的结果无法确认时，仍保留写入
范围；请继续原任务核验。响应为空或丢失不能独立释放该范围。原生接口不支持带版本
条件的 DELETE，外部配置变更可能发生在最后一次 GET 之后；重复连接和外部客户端
不在此协调范围内。参阅[非递归删除契约](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.groups/delete)。


自定义仪表板清单要求原生 LIST 与 GET 配置一致，复核涵盖 `etag`、布局及未知字段。
查询、文本、注释和组件内容会在持久化与 Invoke 返回前脱敏，保留显示元数据和配置
摘要。系统仪表板不纳入项目清理。

仪表板删除需要 `monitoring.dashboards.delete` 权限和已冻结的配置复核。两次自身 GET
通过后才发送无请求体、无调用方参数的 DELETE；配置变化或保护标签会阻止动作。
原生 DELETE 不支持版本条件：将 `etag` 纳入复核仍无法阻止最后一次 GET 与 DELETE
之间的外部写入。回执在重启后仍绑定原始资源和请求键，通过自身 GET 的 404 确认完成。
仪表板与同连接、同项目内其他已复核的 Monitoring 配置写入共享写入范围；失败或取消
后的结果不确定时仍保留该范围。参阅[仪表板删除契约](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards/delete)。


告警策略清理还会检查已配置项目中的仪表板。原生 `AlertChart.name` 使用完整的项目与
策略路径，`IncidentList.policyNames` 使用 `alertPolicies/ID` 短路径。没有策略筛选的
事件列表按可能引用本项目所有策略处理。文本、日志和指标查询中的策略名称不会仅因
字面匹配而成为策略对象引用；未知组件或无效策略名称不能证明没有引用。

已刷新且确认引用的仪表板必须显式选择，并先于策略删除。仪表板 LIST、GET、再次
LIST 以及目标配置复核会拒绝读取不完整或配置变化；策略清单过期时，需要重新扫描后
才能刷新关系图。删除前重复检查消费方，并通过所选仪表板自身的 GET 确认其已不存在。
重启后的策略回执和项目写入范围仍绑定已冻结的仪表板前置项。仅扫描策略时也会执行
此依赖发现，额外需要 `monitoring.dashboards.list/get` 权限。当前检查已配置项目的
仪表板；其他项目中的任意策略对象引用仍需扩大覆盖。参阅[AlertChart 与 IncidentList 契约](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards#AlertChart)。


Uptime 依赖复核现在包含发现到的各指标范围项目及相关项目日志路由目标中的仪表板。
原生时间序列筛选和比值分母复用 Uptime 指标、检查 ID 解析。默认使用所在项目的日志
组件也会复核日志路由和筛选器，并识别显式指定的被监控项目日志来源。查询模板、未支持
的语言、未知结构，以及尚未复核的其他项目或日志视图来源，都不能证明没有引用。
跨项目消费方保留为需要其自身连接处理的阻塞，不会通过被监控项目的连接删除。

已刷新的本项目仪表板需要显式选择，并先于 Uptime 检查删除。完整 LIST、GET、再次
LIST 前后会复核指标范围和日志路由；权限失败或配置变化会阻止清理。已冻结的仪表板、
策略前置项和同步回执支持重启，通过资源自身 GET 的 404 确认完成。五类已复核的
Monitoring 资源现在统一使用同连接、同项目的写入范围，并兼容旧任务按资源类型保存
的范围。失败或取消后的 Uptime 动作结果不确定时，仍保留范围，直到独立核验完成。
发现范围以外的任意仪表板、详细日志视图权限和未支持的查询语言仍待补齐。
