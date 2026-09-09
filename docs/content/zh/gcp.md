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

## 盘点与清理范围

Steward 通过产品原生 API 盘点下表中的资源，Cloud Asset Inventory 用于补充发现其他类型，作为只读资源展示。产品扫描分片失败时会明确报告，也不会据此认定资源已不存在。

| 产品 | 识别的资源 | 清理能力 |
| --- | --- | --- |
| Compute Engine | VM 实例、可用区与地域级持久磁盘、快照、镜像、实例模板、托管实例组、实例组和自动扩缩器 | 支持 |
| VPC | 网络、子网、防火墙规则、路由、Cloud Router | 支持 |
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
