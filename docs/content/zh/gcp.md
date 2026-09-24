---
title: "Google Cloud（GCP）"
description: "连接 Google Cloud 项目，授予扫描与清理权限，并查阅 Steward 对各服务的盘点范围和清理行为。"
navTitle: "Google Cloud"
---

本页说明如何连接 Google Cloud 项目、授予 Steward 所需权限、完成第一次扫描，以及各服务如何盘点和清理。

每个连接对应一个项目。两个可选设置可以扩展范围：**防火墙范围**用于管理组织或文件夹中的层级防火墙策略，**组目录**用于管理 Cloud Identity 身份组。Steward 还会盘点一些不属于该项目的记录：凭证可见的账单账号中的预算、项目所属的组织，以及连接服务账号自身的 OS Login SSH 公钥。

Steward 通过两种方式读取 Google Cloud：

- **Cloud Asset Inventory** 提供广泛的资源发现。没有原生支持的资源类型作为只读资源展示。
- **产品原生 API** 更完整地盘点[盘点与清理范围](#盘点与清理范围)中列出的服务，清理也始终通过这些 API 执行。

## 连接项目

### 选择凭证

| 凭证 | 适用场景 |
| --- | --- |
| **Google 服务账号** JSON 密钥 | 需要长期稳定、无人值守的权限范围。服务账号可以来自另一个项目，只要它有权访问目标项目。支持标准 Google Cloud 端点的密钥。 |
| [浏览器登录](./connections.md#browser)（**用浏览器登录**） | 授权你本人的 Google 账号，再从它能访问的项目中选择一个。连接只能读到你账号能读的内容。 |

浏览器登录由 Steward 自己发起授权。Steward 不会使用机器上已有的 `gcloud` 凭证，也不会使用元数据服务提供的凭证。

服务账号密钥使用部署环境的凭证加密密钥加密保存。轮换密钥时使用**替换凭证**，目标项目必须保持不变。参阅 Google 的[服务账号密钥管理建议](https://docs.cloud.google.com/iam/docs/best-practices-for-managing-service-account-keys)。

### 添加连接

1. 在目标项目启用 **Cloud Asset Inventory**、**Cloud Resource Manager** 和 **Compute Engine API**。使用某个产品的清理功能前，先启用该产品的 API。
2. 在目标项目上为服务账号授予[基础盘点权限](#基础盘点权限)，以及准备扫描或清理的服务所需的[产品权限](#产品权限)。
3. 打开用户菜单 → **设置** → **云连接** → **添加云连接**，选择 **Google Cloud**，填写**项目 ID** 和**服务账号 JSON 密钥**；也可以用浏览器登录后选择项目。
4. 按需填写**防火墙范围**或**组目录**（见下文）。
5. 验证连接并刷新地域。

**结果检查：** 验证会确认项目访问权限和资源盘点权限，但不代表所有清理 API 都已获授权；第一次扫描和清理审查会暴露缺失的权限。

### 可选连接设置

- **防火墙范围**（`organizations/123` 或 `folders/456`）：把该组织或文件夹及其下级文件夹纳入连接的防火墙策略范围，以便管理其中的层级防火墙策略。仅有项目访问权限不会启用此范围。权限与行为见[防火墙策略](#防火墙策略)。
- **组目录**（`customers/C01234567` 或 `identitysources/source-1`）：允许 Steward 管理该目录中的 Cloud Identity 身份组。仅有项目权限不足以授权。详见 [Cloud Identity 身份组](#cloud-identity-身份组)。

两者都需要在扫描时包含全局范围。

## 权限

### 基础盘点权限

在目标项目上授予以下权限，用于 Cloud Asset Inventory 盘点和地域发现。

| 权限 | 用途 |
| --- | --- |
| `cloudasset.assets.listResource` | 通过 Cloud Asset Inventory 列举资源（[列表权限](https://docs.cloud.google.com/asset-inventory/docs/reference/rest/v1/assets/list)） |
| `resourcemanager.projects.get` | 读取目标项目 |
| `compute.regions.list` | 发现地域 |

### 产品权限

原生产品盘点还需要各产品的列表和读取权限。只对准备清理的资源补充删除权限，以及查询所返回操作状态的权限。清理存储桶还需要 `storage.objects.list`。

[各服务说明](#各服务说明)中的每一节先列出扫描所需权限，再列出清理另需的权限，并附上该产品的 Google 权限索引链接。部分服务还需要额外启用 API：

| 服务 | 需要启用的 API |
| --- | --- |
| [Cloud Identity 身份组](#cloud-identity-身份组) | Cloud Identity API |
| [OS Login SSH 公钥](#os-login-ssh-公钥) | OS Login API |
| [Security Command Center](#security-command-center) 服务设置与计费元数据 | **Security Center Management API** |
| Security Command Center 组织订阅 | **Security Command Center API** |

## 完成第一次扫描

1. 切换到新建的 Google Cloud 连接，核对目标项目 ID。
2. 选择一个包含已知资源的地域运行扫描，例如在 `us-central1` 中查找一个已知 VM。
3. 在扫描详情中处理权限不足或 API 未启用的失败项，再核对资源名称、项目、地域与可用区。
4. 选择 **全部启用地域 + 全局** 完成完整盘点。全局 VPC、全局地址等全局资源需要包含全局范围。

**结果检查：** 能找到预期资源，所属项目和位置正确，扫描没有未处理的失败项。Cloud Asset Inventory 存在收集延迟，新建资源可能要在之后的扫描中才出现。

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
type = "compute.googleapis.com/RoutePolicy" AND properties.type = "ROUTE_POLICY_TYPE_IMPORT"
type = "compute.googleapis.com/NamedSet" AND properties.type = "NAMED_SET_TYPE_PREFIX"
type = "compute.googleapis.com/RouterNat" AND properties.type = "PRIVATE"
```

通用规则：

- Google 以 64 位整数字符串返回的容量、数量等字段，查询值需要加引号。
- 顶层 `state` 字段适用于所有资源类型；API 不提供资源状态时该值为空。例如，网络和防火墙规则没有原生状态或标签属性。路由返回 `routeStatus` 时，会作为其可搜索状态。
- 托管证书状态、PSC 连接状态和负载均衡迁移状态使用各自的属性名，不代表资源的整体健康状态。参见[路由](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routes)和[证书](https://docs.cloud.google.com/compute/docs/reference/rest/v1/sslCertificates)的 API 字段。

各服务的专用字段：

- **防火墙策略：** `properties.name` 是原生数字名称，`properties.shortName` 是显示名称。
- **Cloud TPU 排队资源：** 保留结构化的原生 `properties.state`，按 `properties.lifecycleState` 查询状态。
- **BigQuery：** `properties.name` 是数据集或表的短 ID；不同数据集中的同名表通过完整资源 ID 区分。
- **Cloud Identity 身份组与成员关系、Resource Manager 组织：** `properties.name` 是原生资源名。身份组与组织的显示名称用 `properties.displayName`，成员标识用 `properties.memberId`。
- **GKE：** 标签来自原生集群或节点池配置。
- **Discovery Engine：** 文档的 `properties.indexedAt` 是索引状态中的时间戳，索引错误文本和文档内容仍被遮蔽。目标网站提供 `properties.indexingStatus`；会话和对话提供 `startTime` 与 `endTime`，而不是创建时间。参见[文档索引字段](https://docs.cloud.google.com/generative-ai-app-builder/docs/reference/rest/v1/projects.locations.collections.dataStores.branches.documents)。
- **Infrastructure Manager：** 资源变更提供 `properties.intent`。参见[资源变更意图](https://docs.cloud.google.com/infrastructure-manager/docs/reference/rest/v1/projects.locations.previews.resourceChanges)。
- **Cloud KMS：** `properties.primaryState` 仅表示 CryptoKey 主版本的状态；密钥版本和导入任务不会继承密钥标签。
- **Cloud Router 命名集合：** 社区集合使用 `NAMED_SET_TYPE_COMMUNITY`。

## 盘点与清理范围

Steward 通过产品原生 API 盘点下表中的资源，Cloud Asset Inventory 补充发现的其他类型作为只读资源展示。某个产品的扫描项失败时，Steward 会报告失败，不会据此认为该产品的资源已删除。

| 产品 | 识别的资源 | 清理能力 |
| --- | --- | --- |
| [Compute Engine](#compute-engine) | VM 实例、可用区与地域级持久磁盘、快照、镜像、实例模板、托管实例组、实例组和自动扩缩器 | 支持 |
| [Hyperdisk 存储池](#hyperdisk-存储池) | 存储池、容量与性能用量、预配模式和成员磁盘 | 审查后清理 |
| Cloud Storage | 存储桶 | 仅空桶 |
| [Cloud TPU](#cloud-tpu) | 节点、排队资源和原生预留容量 | 节点与排队资源支持审查后清理；数据盘先解绑并保留；预留容量只读 |
| [Batch](#batch) | 作业、任务记录 | 删除作业时取消运行中的工作，并核实已审查的任务、VM 和磁盘影响；任务不能单独删除 |
| [Google Kubernetes Engine](#google-kubernetes-engine) | 集群、节点池 | 审查成员影响后，由原生控制器执行清理 |
| Cloud Run | 服务 | 支持 |
| Artifact Registry | 仓库 | 支持 |
| VPC | 网络、子网、防火墙规则、路由、Cloud Router | 支持 |
| [Cloud Router](#cloud-router) | 路由器配置、NAT 影响和策略／命名集合前置步骤 | 审查后删除路由器，其中的 NAT 随之删除 |
| [Cloud Router BGP 策略](#路由策略) | 各路由器的导入／导出策略、CEL 条款和指纹 | 支持独立删除，先解除 BGP 对等体引用 |
| [Cloud Router 命名集合](#命名集合) | 各路由器的前缀／社区集合、CEL 元素和指纹 | 先删除引用它的策略，再审查删除 |
| [Cloud NAT](#cloud-nat) | 各路由器的公共／私有 NAT 配置、规则及子网与地址引用 | 支持独立删除，保留其他 NAT 和所属路由器 |
| [防火墙策略](#防火墙策略) | 配置范围内的层级策略、全局和地域级网络策略及原生关联 | 先解除审查过的关联，再删除策略；关联也可单独解除 |
| 负载均衡与地址 | 地域和全局 IP 地址、转发规则、地域和全局后端服务、健康检查（含旧版 HTTP/HTTPS）、目标池、网络端点组、URL Map、HTTP/HTTPS 代理、SSL 证书 | 支持 |
| [BigQuery](#bigquery) | 数据集、表 | 支持 |
| [Bigtable](#bigtable) | 实例、集群、表 | 支持；遵守表的删除保护 |
| Cloud SQL | 实例 | 关闭删除保护后支持 |
| Pub/Sub | 主题、订阅 | 支持 |
| [Data Fusion](#data-fusion) | 实例、DNS 对等连接和命名空间 | 实例清理包含审查过的命名空间与 DNS 对等连接；DNS 对等连接也可单独删除 |
| [Dataform](#dataform) | 文件夹、团队文件夹、仓库、工作区、发布与工作流配置、工作流执行、编译结果 | 审查后清理文件夹和仓库；先取消运行中的执行；编译结果随仓库删除 |
| [Dataproc](#dataproc) | 集群、作业、辅助节点组、自动伸缩策略和工作流模板 | 审查后清理集群；默认保留作业历史；辅助节点组不能单独删除 |
| [Discovery Engine](#discovery-engine) | 集合、数据存储、应用、架构、控制规则、服务配置、会话、对话、助手、文档分支与文档、目标网站 | 审查影响后执行原生清理；分支和网站搜索配置随数据存储删除 |
| [Cloud Monitoring](#cloud-monitoring) | 告警策略、自定义仪表板、资源组、正常运行时间检查 | 审查后删除；选中的引用方会先删除 |
| [Cloud Monitoring 指标范围](#指标范围) | 当前连接项目的指标范围、被监控项目关联及其他范围的反向引用 | 可解除选中的项目关联；保留范围及其自身项目关联 |
| [Cloud Billing 预算](#cloud-billing-预算) | 可见账单账号中的预算，以及连接项目的预算 | 审查后删除 |
| [Cloud Identity](#cloud-identity-身份组) | 配置目录中的身份组及成员关系 | 审查后删除身份组；普通成员关系也可单独删除 |
| Cloud KMS | 密钥环、密钥、版本、导入任务 | 删除满足条件的资源记录；导入任务只读 |
| [Infrastructure Manager](#infrastructure-manager) | 部署组、部署、修订、资源记录、预览及变更／漂移记录 | 审查后清理部署组、部署与预览；子级元数据不能单独删除 |
| [OS Login](#os-login-ssh-公钥) | 连接服务账号 OS Login 资料中的 SSH 公钥 | 支持 |
| [Resource Manager](#resource-manager-组织) | 连接项目所属的组织 | 只读；公开的 v3 API 没有组织删除方法 |
| Secret Manager | 全局和地域级密钥 | 支持 |
| [Security Command Center](#security-command-center) | 服务设置、组织订阅、项目与组织计费元数据 | 只读 |

资源盘点具有最终一致性。新建或删除的资源可能需要一段时间才会反映到 Cloud Asset Inventory 中，立即重扫也可能看到旧数据。清理不依赖盘点结果：Steward 直接查询产品 API、等待异步操作结束并确认资源已不存在。参阅 Google 的[资源类型与数据时效说明](https://docs.cloud.google.com/asset-inventory/docs/asset-types)。

创建清理任务前请核对实际选择范围，执行后查看[清理结果](./cleanup.md)。产品权限、保留策略、资源依赖和 Google Cloud 中的变化仍可能导致操作无法完成。

## 全局 VPC 与地域子网

Google VPC 可以跨地域。Steward 在各地域的网络视图中展示同一个 VPC 边界，以及该地域的子网和关联资源；全局资源也可通过全局视图查看。

清理**某个地域内的 VPC 分组**时，只选择该地域中的资源，并保留共享的全局 VPC。要删除 VPC 本身，应将它作为独立的全局资源，与剩余依赖一起审查。网络扫描可以包含所选地域的资源及其引用的全局网络资源。其他项目中的 Shared VPC 资源需要单独连接，Steward 不会自动合并为跨项目清理。

## 删除保护

遇到保护时，清理会停止，而不是绕过它。下面列出主要保护，完整行为见各链接小节。

- **保护标签：** 带有 `steward-protected` 或 `steward_protected` 标签、且值为 `true`、`1`、`yes`、`on` 或 `protected` 的资源不会被删除。
- **VM 删除保护：** 获准清理后，Steward 会在删除 VM 前通过明确的原生准备步骤解除删除保护。见 [Compute Engine](#compute-engine)。
- **VM 磁盘和托管实例组：** 磁盘与 IP 的删除策略会进入影响计划，保留设置会在删除控制器前应用并核实。见 [Compute Engine](#compute-engine)。
- **Cloud SQL：** 实例的删除保护会阻止删除。
- **Cloud Storage 存储桶：** 非空桶会被拒绝删除。Steward 不会先清空对象或对象版本来满足删除条件。
- **Bigtable：** 遵守表的删除保护。见 [Bigtable](#bigtable)。
- **Cloud Identity 身份组：** 锁定组受保护，动态成员关系不能逐项删除。见 [Cloud Identity 身份组](#cloud-identity-身份组)。
- **防火墙策略：** 先解除审查过的关联；目标超出配置范围，或策略、关联、目标发生变化时，计划会被阻止。见[防火墙策略](#防火墙策略)。
- **Hyperdisk 存储池：** 计划保留成员磁盘时，不能删除其存储池。见 [Hyperdisk 存储池](#hyperdisk-存储池)。
- **Cloud TPU、Batch、Dataproc：** 已有数据盘，以及已挂载且 `autoDelete=false` 的已有磁盘会保留。见 [Cloud TPU](#cloud-tpu)、[Batch](#batch) 和 [Dataproc](#dataproc)。
- **GKE：** 持久卷、已有 IP 和证书按已核实的策略保留。见 [Google Kubernetes Engine](#google-kubernetes-engine)。
- **Cloud Router：** 关联的 VPN 隧道和 VLAN attachment 会阻止删除路由器。见 [Cloud Router](#cloud-router)。
- **Cloud Monitoring：** 仍引用目标的资源会阻止删除，除非一并选中它们。见 [Cloud Monitoring](#cloud-monitoring)。
- **指标范围：** 范围及其自身项目关联只读。见[指标范围](#指标范围)。
- **Infrastructure Manager：** 不支持部分保留。见 [Infrastructure Manager](#infrastructure-manager)。
- **Discovery Engine：** 数据存储仍有关联应用时，必须先删除或解除所有关联应用。见 [Discovery Engine](#discovery-engine)。
- **连接自身的身份：** 连接使用的服务账号及其密钥始终受保护。见[清理资源](./cleanup.md)。

## 各服务说明

每一节按相同顺序说明：盘点哪些内容、扫描和清理所需权限、清理会做什么，以及已知限制。

| 领域 | 服务 |
| --- | --- |
| 计算、容器与存储 | [Batch](#batch) · [Cloud TPU](#cloud-tpu) · [Compute Engine](#compute-engine) · [Google Kubernetes Engine](#google-kubernetes-engine) · [Hyperdisk 存储池](#hyperdisk-存储池) |
| 网络 | [Cloud NAT](#cloud-nat) · [Cloud Router](#cloud-router) · [防火墙策略](#防火墙策略) |
| 数据与 AI | [BigQuery](#bigquery) · [Bigtable](#bigtable) · [Data Fusion](#data-fusion) · [Dataform](#dataform) · [Dataproc](#dataproc) · [Discovery Engine](#discovery-engine) |
| 监控 | [Cloud Monitoring](#cloud-monitoring) |
| 管理、身份、安全与计费 | [Cloud Billing 预算](#cloud-billing-预算) · [Cloud Identity 身份组](#cloud-identity-身份组) · [Infrastructure Manager](#infrastructure-manager) · [OS Login SSH 公钥](#os-login-ssh-公钥) · [Resource Manager 组织](#resource-manager-组织) · [Security Command Center](#security-command-center) |

### Batch

**盘点：** 作业及其任务记录。

**权限：**

- 扫描：`batch.locations.list`、`batch.jobs.list`、`batch.jobs.get`、`batch.tasks.list` 和 `batch.tasks.get`。
- 清理另需：`batch.jobs.delete`、`batch.operations.get`，以及用于核实影响的 `compute.instances.list`、`compute.instances.get`、`compute.disks.list` 和 `compute.disks.get`。使用实例模板的作业还需要 `compute.instanceTemplates.get`。

Steward 不会读取所引用的密钥内容。参阅 [Batch 权限索引](https://docs.cloud.google.com/iam/docs/roles-permissions/batch)。

**清理：** 选择作业后，一起审查其任务、VM 和磁盘；任务不能单独删除。清理会取消运行中的工作，通过原生作业删除接口执行，并等待相关资源消失。

- 已挂载且 `autoDelete=false` 的已有磁盘会保留。
- 计划要求保留 Batch 创建的磁盘，或外部磁盘的删除设置不安全时，清理会停止。
- 实例模板、存储桶、NFS 数据、密钥、Pub/Sub 主题、日志和输出数据仍作为独立资源保留。

参阅 Google 的[作业删除行为](https://docs.cloud.google.com/batch/docs/delete-job)。

### Cloud TPU

**盘点：** 通过 Cloud TPU API 盘点节点、排队资源和原生预留容量。包括 TPU7x 及后续版本在内的 Compute Engine／GKE TPU 使用[独立管理接口](https://docs.cloud.google.com/tpu/docs/tpus-in-compute-engine)，不在此范围内。节点和模板的私有元数据会脱敏，但 Steward 仍会用它检查配置是否变化。

**权限：**

- 扫描：`tpu.locations.list`、`tpu.nodes.list`、`tpu.nodes.get`，以及读取已挂载数据盘的 `compute.disks.get`。排队资源 API 复用节点权限，[预留容量列表](https://docs.cloud.google.com/tpu/docs/reference/rest/v2alpha1/projects.locations.reservations/list)也需要 `tpu.nodes.get`。
- 清理另需：`tpu.nodes.update`、`tpu.nodes.delete` 和 `tpu.operations.get`。

参阅 [TPU 权限索引](https://docs.cloud.google.com/iam/docs/roles-permissions/tpu)。

**清理：** 节点和排队资源可以清理，预留容量只读。

- 删除排队资源时，先删除计划中的节点，所有节点消失后再删除排队请求。
- 节点清理先解绑已有数据盘，确认磁盘仍然存在，再删除节点及其启动盘。保留数据盘只是保留磁盘资源，不会创建备份。
- 网络资源和预留容量作为独立资源保留。

参阅 Google 的[排队资源删除契约](https://docs.cloud.google.com/tpu/docs/reference/rest/v2/projects.locations.queuedResources/delete)。

**限制：** Cloud TPU 写入接口不支持以配置或资源身份作为条件，清理期间应避免修改 TPU 资源。

### Compute Engine

**盘点：** VM 实例、可用区与地域级持久磁盘、快照、镜像、实例模板、托管实例组、实例组和自动扩缩器。Google 对部分地域和全局资源使用不同的类型名，例如 `RegionDisk`、`GlobalAddress` 和 `GlobalForwardingRule`。完整资源名称包含项目和地域或可用区，不同可用区的同名 VM 不会合并。

**清理：**

- **磁盘与 IP：** 影响计划会显示每块磁盘和每个 IP 的原生删除策略。对支持保留的资源，Steward 先通过原生操作应用保留设置并核实生效，再删除 VM 或托管实例组。
- **删除保护：** 获准清理后，Steward 会通过明确的原生准备步骤解除 VM 删除保护。
- 挂载关系或资源身份发生变化时会停止执行。

### Google Kubernetes Engine

**盘点：** 集群和节点池。集群的控制平面端点必须可达。

**权限：**

- 扫描：对 `kube-system` Namespace 的 Kubernetes `get` 权限，以及对 Service、Ingress 和已安装 Gateway 资源的 `list` 权限。
- 清理另需：这些工作负载资源的 `delete` 权限、Container 与 Compute 的原生读取、删除和操作查询权限，以及节点组成员查询权限。

不需要读取 Kubernetes Secret 的权限。

**清理：** 清理由原生控制器执行，计划包含已核实的节点与网络影响。删除集群前，Steward 会等待 Kubernetes Service、Ingress 和 Gateway 的 finalizer 完成。持久卷、已有 IP 和证书按已核实的策略保留。不支持的保留要求或无法确认的归属会阻止清理。

### Hyperdisk 存储池

**盘点：** Steward 使用 Compute 原生聚合列表读取存储池，并把各可用区映射到扫描地域。记录预配容量、IOPS 和吞吐量、写入与已用容量、磁盘数量、预配模式、Exapool 容量及共享设置。大整数保持原生精度。

对于成员，Steward 遍历 `storagePools.listDisks` 的所有分页，记录每块磁盘的容量、已用字节、IOPS、吞吐量、挂载实例和快照策略。成员磁盘必须位于池所在可用区；列完成员后，Steward 会重新读取存储池，确认它仍是同一个池。共享自其他项目的磁盘只显示成员摘要，当前连接不会因此读取或管理那些项目。磁盘记录本身由磁盘扫描更新。

磁盘对存储池的引用只是普通依赖，不代表删除其中一方会连带删除另一方。容量和性能池化也不能据此认定与阿里云的物理资源独享等价。

**权限：**

- 扫描：`compute.storagePools.list` 和 `compute.storagePools.get`（用于成员列表和池复查）。
- 清理另需：`compute.storagePools.delete`、`compute.zoneOperations.get`，成员磁盘清理需要 `compute.disks.get` / `compute.disks.delete`。

**清理：** Hyperdisk Balanced 和 Throughput 存储池支持审查后删除。

1. 选择池中的本地成员磁盘，或选择会删除这些磁盘的受支持 VM、MIG 或 GKE 控制器。计划不会自动替你选择控制器。
2. 清理会等每块成员磁盘都不存在后，再删除存储池。计划保留成员磁盘时，不能删除其存储池。

快照独立保留。Exapool 以及仍有其他项目成员的池需要在 Steward 之外完成清理，之后刷新盘点。有效的未来预留可能阻止删除，Steward 不会自动取消预留。参见 [Google 存储池管理指南](https://docs.cloud.google.com/compute/docs/disks/manage-storage-pools)。

**限制：** 列表权限被拒绝或只返回部分结果时，扫描失败；成员读取失败时同样保留已有记录。配置或成员变化后需要重新扫描。原生删除 API 不支持按资源 ID 或 etag 条件删除，清理期间应避免修改存储池。

### Cloud NAT

**盘点：** 每个路由器的原生 `nats` 数组中，每个 NAT 网关生成一条记录，包括公共／私有类型、IP 分配与排空、子网和 NAT64 选择、规则、端口分配及日志设置。NAT 的身份由项目、地域、路由器和 NAT 名称组成，不对应独立的 REST 接口或 Cloud Asset Inventory 资产类型。参阅[原生 Router 架构](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers)。

同时扫描相关资源后，可以看到指向路由器、VPC、子网、地址和 NCC Hub 的关系连线：

- Steward 从原生 CEL 等式中提取 `nexthop.hub` 的字面量目标。注释和普通字符串不算引用，数据包匹配表达式不会被执行。
- 对于未指定 Hub 或使用计算表达式的选择器，Steward 会列举项目中的 NCC spoke，读取匹配的 VPC spoke，再次查询成员并重新核验路由器。所有匹配的 Hub 都保留为可能依赖；完整的空成员清单不会添加 Hub。重复读取能发现观测到的变化，但不是原子快照。
- 地域 NAT 只在 Hub 的完整身份属于同一连接、同一分区时才关联该全局 Hub。缺失的 Hub 或其他项目中的 Hub 保留为待解析引用。
- 旧版本扫描的 NAT 记录中有未绑定 Hub 表达式时，需要重新扫描后才能重建关系。

参阅 [Private NAT 配置指南](https://docs.cloud.google.com/nat/docs/set-up-private-nat)。

**权限：**

- 扫描：`compute.routers.list` 和 `compute.routers.get`。未指定 Hub 或使用计算表达式的选择器还需要 `networkconnectivity.spokes.list` 和 `networkconnectivity.spokes.get`。
- 清理另需：`compute.routers.get`、`compute.routers.update` 和 `compute.regionOperations.get`。操作回执丢失或过期后恢复，还需要 `compute.regionOperations.list`。

**清理：** Steward 通过原生 Router PATCH 只移除已审查的 NAT 配置，等待地域操作完成，再从完整的路由器响应确认 NAT 已不存在。请求保留其他 NAT 的最新配置，并保留 BGP 对等体、接口、密钥和路由器。手动预留的地址资源不会被删除。

- 保留引用 Hub 的 NAT 会阻止删除 Hub。同时选择两者时先删除 NAT；只选 NAT 则保留 Hub。
- 删除父路由器也会删除其中的 NAT，见 [Cloud Router](#cloud-router)。
- 同一路由器上的 NAT 修改与其他修改串行执行。NAT 动作已失败或取消且没有可运行作业时，只要 Steward 确认尚未发送更新，或原云端操作已经结束，就会解除对路由器的占用。这不会把原任务或删除标记为成功。暂停的任务和结果无法确认的更新仍保持阻塞；失败的任务仍可在原任务中继续。操作记录缺失、结果不完整或读取被拒绝时，阻塞保持不变。参阅[串行修改与恢复](#串行修改与恢复)和[原生操作查询契约](https://docs.cloud.google.com/compute/docs/reference/rest/v1/regionOperations/list)。

**限制：** CEL 语法错误，或路由器／spoke 响应不完整、被拒绝、缺失或发生变化时，该扫描项失败并保留之前的记录。只有完整且身份匹配的路由器响应才能确认 NAT 已不存在。原生 PATCH 没有配置版本条件，最后一次读取与更新之间仍可能被外部写入抢先；清理期间应避免在外部修改该路由器的 NAT 配置。参阅[原生 PATCH 与请求 ID 契约](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/patch)。

### Cloud Router

本节介绍路由器、BGP 路由策略、命名集合，以及 Steward 如何安排同一路由器上的修改顺序。清理路由器前，应把路由器与其 NAT、路由策略和命名集合一同扫描。

#### 路由器

**盘点：** Steward 逐一读取路由器的当前详情，核实身份后记录 NAT、BGP 和接口配置。MD5 认证材料会从库存和 API 日志中脱敏。参阅 [Router GET 契约](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/get)。

**权限：**

- 扫描：`compute.routers.list` 和 `compute.routers.get`。
- 清理审查另需：`compute.routers.listRoutePolicies`、`compute.routers.getRoutePolicy`、`compute.routers.listNamedSets` 和 `compute.routers.getNamedSet`。
- 删除路由器另需：`compute.routers.delete`、`compute.regionOperations.get`，以及前置策略和命名集合删除所需的权限。

**清理：** Steward 会列举并读取路由器的所有原生子资源，与扫描结果核对；未纳入盘点的子资源会显示为阻断项。

- 路由策略和命名集合成为前置删除步骤。
- 已审查的 NAT 属于删除影响，删除路由器时不能保留：Google 明确说明[删除路由器也会删除其中的 Cloud NAT 网关](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/managing-routers)。只想移除某个 NAT 时，请使用 [Cloud NAT](#cloud-nat) 清理。
- Steward 调用原生 [Router DELETE](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/delete)，重启后继续等待地域操作，确认路由器及前置资源都不存在后，再把 NAT 记为随之删除。整个过程不会另外发送 NAT 或地址删除请求。
- 关联的 VPN 隧道和 VLAN attachment 会阻止清理。先移除它们并重新扫描，再创建任务。
- 其他配置变化需要重新审查。在引入配置审查之前创建的旧路由器任务，需要重新扫描后才能执行删除。

**限制：** 详情读取被拒绝、资源缺失、结果不完整或身份不匹配时，该扫描项失败并保留之前的记录。读取与原生删除不是原子操作，清理期间应避免修改路由器。

#### 路由策略

**盘点：** Steward 在路由器所属地域读取其导入／导出策略，并逐条读取详情，包括 CEL 条款和指纹。不同路由器下的同名策略分别保存。`bgpReferences` 列出使用该策略的对等体及导入／导出方向。

Steward 还会解析原生 CEL 中的 `prefixSets('name')` 和 `communitySets('name')` 调用，建立对同一路由器内命名集合的依赖。把策略和命名集合一同扫描，即可看到这些关系连线。字符串和注释不视为调用，表达式也不会被执行。

参阅 Google 的[策略列表 API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/listRoutePolicies)和[策略详情 API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/getRoutePolicy)。

**权限：**

- 扫描：`compute.routers.list`、`compute.routers.listRoutePolicies` 和 `compute.routers.getRoutePolicy`。
- 清理另需：`compute.routers.get`、`compute.routers.deleteRoutePolicy` 和 `compute.regionOperations.get`。
- BGP 对等体引用该策略时，还需要 `compute.routers.update`；Google 的[审计权限列表](https://docs.cloud.google.com/compute/docs/logging/audit-logging)还列出了路由器所在网络的 `compute.networks.updatePolicy`。

**清理：** 创建任务前先重新扫描，记录策略指纹、所属路由器 ID 和 BGP 对等体配置。

1. 对等体引用该策略时，Steward 先通过 [Router PATCH](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/patch) 从它们的导入／导出列表中移除该策略名。其他策略保持原有顺序，其他对等体配置不变，请求也不包含无关的路由器字段。
2. 该地域操作结束、更新后的对等体配置可见后，Steward 调用[原生策略删除 API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/deleteRoutePolicy)删除策略。
3. Steward 在同一路由器仍可读取的前提下确认策略已不存在；操作结束本身不能证明已删除。路由器不可读取时，会报告依赖读取失败。

两个阶段都能在重启后继续。删除策略会保留其引用的命名集合，也不会选中其他策略。

可以基于同一次扫描依次删除多条策略。更新对等体前，Steward 读取当前配置，仅在原生 API 确认先前移除的同级策略已不存在后，才接受这些移除。新增引用、策略顺序变化、对等体或其他配置变化，以及同级策略不可读取，都会阻止执行。其他配置变化、原生依赖冲突和权限失败会报告出来，供重新审查。

**限制：** 详情读取失败时保留上次成功的记录；策略列表成功返回为空时，旧策略会被标为已删除。CEL 语法错误或无法解析的计算所得集合名，会使该策略扫描项失败并保留之前的记录。这些修改没有指纹前置条件，清理期间应避免修改策略或 BGP 对等体。

#### 命名集合

**盘点：** 各路由器的前缀集合和社区集合，包括类型、描述、CEL 表达式元素和指纹。集合名只在所属路由器内唯一，因此身份包含地域和路由器。参阅[原生集合详情 API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/getNamedSet)。

**权限：**

- 扫描：`compute.routers.list`、`compute.routers.listNamedSets` 和 `compute.routers.getNamedSet`。
- 清理另需：`compute.routers.get`、`compute.routers.listRoutePolicies`、`compute.routers.getRoutePolicy`、`compute.routers.getNamedSet`、`compute.routers.deleteNamedSet` 和 `compute.regionOperations.get`。

**清理：** Google [禁止删除仍被同一路由器任意策略引用的集合](https://docs.cloud.google.com/network-connectivity/docs/router/how-to/bgp-route-policies/update-named-sets)。Steward 会检查该路由器上的所有策略，包括你没有扫描的策略。

- 已知的引用策略不在清理范围内时，计划会被阻止。同时选择策略和集合时，先删除策略；只删除策略则保留集合。
- Steward 核对扫描时的集合版本和路由器身份，等待地域操作完成，并确认集合已不存在。参阅[原生删除 API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/routers/deleteNamedSet)。
- 策略读取不完整、引用无法解析、资源发生变化或云端冲突都会停止清理。

**限制：** 读取失败或结果不完整时，保留之前的记录。

#### 串行修改与恢复

同一路由器上选中的 Router、NAT、路由策略和命名集合删除会逐个执行，即使执行并发度更高也是如此；不同路由器之间互不影响。该路由器存在尚未确认完成的修改时，与之重叠的清理任务不能启动或继续。

旧版本创建的任务在启动或继续时会自动补上这一顺序，步骤和已审查的资源快照保持不变。如果补上的顺序会影响仍在运行的作业，或已发出但结果尚未确认的云端请求，任务不能继续。

任务已失败或取消、且所有作业都已结束时，Steward 可以通过只读检查确认所有可能发出的修改都已结束，再解除对路由器的占用：

- **已被对等体引用的策略：** 需要同时确认原 BGP 解绑操作和策略删除操作，包括请求已发出但回执尚未保存的删除。仅解绑操作结束不会解除占用。
- **未被引用的策略或命名集合：** 只需确认其单个删除操作。
- **路由器：** 需要确认原删除操作已结束。恢复还需要原任务中的 NAT、策略和命名集合审查记录，缺失或变化时路由器仍保持阻塞。对于只记录了操作的旧版本任务，Google 的操作记录必须返回匹配的原请求 UUID、目标和路由器数字 ID。

恢复需要 `compute.regionOperations.get`；回执丢失或过期时，还需要 `compute.regionOperations.list`。操作历史缺失、有歧义、不完整或无法读取，旧回执无法识别，或缺少路由器身份证据时，路由器仍保持阻塞。恢复会保留原执行状态：不会继续或重发删除，也不会把任何资源标为已删除或把清理标为成功。参阅[操作查询 API](https://docs.cloud.google.com/compute/docs/reference/rest/v1/regionOperations/list)。

### 防火墙策略

**盘点：** 连接**防火墙范围**内的层级策略、全局和地域级网络策略，以及它们的原生关联。该范围明确纳入对应组织或文件夹及其下级文件夹；仅有项目访问权限不会启用它。扫描时需包含全局范围。

移除或修改**防火墙范围**不会把之前发现的策略标为已删除，其旧清理计划也需要重新审查。

**权限：** 该组织或文件夹层级的 Resource Manager 读取权限，以及原生防火墙策略权限。

**清理：** Steward 先逐项调用原生 API 解除审查过的关联，再删除策略及其规则。关联也可以单独解除。

- 解除关联会改变目标上的防火墙规则应用情况，网络、组织或文件夹本身仍保留。
- 目标必须仍在配置范围内；策略、关联或目标身份变化会阻止计划执行。层级策略还会查询目标侧的关联列表。

参阅 Google 的[层级策略指南](https://docs.cloud.google.com/firewall/docs/manage-hierarchical-firewall-policies)和[全局网络策略指南](https://docs.cloud.google.com/firewall/docs/use-network-firewall-policies)。

**限制：** API 无法把这些读取与删除绑定为原子操作，同名关联也没有创建标识。清理期间应避免修改策略。

### BigQuery

**盘点：** 数据集和表。列举后，Steward 会读取各自的原生详情，以获得加密配置、行数和字节数等属性。

**权限：** 扫描需要列举权限，以及 `bigquery.datasets.get` 和 `bigquery.tables.get`。参见[数据集](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/datasets/get)和[表](https://docs.cloud.google.com/bigquery/docs/reference/rest/v2/tables/get)的详情方法。

**限制：** 详情访问被拒绝、资源消失或身份不一致时，该扫描项失败并保留已有库存。

### Bigtable

**盘点：** 列举表之后，Steward 会读取完整的原生元数据，包括列族、复制状态、备份策略和删除保护。

**权限：** 扫描需要 `bigtable.tables.list` 和 `bigtable.tables.get`。参见原生[列表](https://docs.cloud.google.com/bigtable/docs/reference/admin/rest/v2/projects.instances.tables/list)与[详情](https://docs.cloud.google.com/bigtable/docs/reference/admin/rest/v2/projects.instances.tables/get)方法。

**清理：** 删除单个表和删除实例时，都会读取完整的表元数据并遵守表的删除保护。

**限制：** 详情访问被拒绝、资源消失或身份不一致时，该扫描项失败并保留之前的记录。

### Data Fusion

**盘点：** 通过 Data Fusion 管理 API 盘点实例、DNS 对等连接和命名空间。单个 CDAP 流水线、数据集、安全存储条目及其运行时计算资源不在范围内，参阅 [CDAP API 文档](https://docs.cloud.google.com/data-fusion/docs/reference/cdap-reference)。私有选项和命名空间策略会在配置一致性检查后脱敏。

**权限：**

- 扫描：`datafusion.locations.list`、`datafusion.instances.list`、`datafusion.instances.get`、`datafusion.namespaces.list` 和 `datafusion.namespaces.getIamPolicy`。
- 实例清理另需：`datafusion.instances.delete` 和 `datafusion.operations.get`。
- 单独删除 DNS 对等连接使用 `datafusion.instances.update`。

参阅[原生方法权限](https://docs.cloud.google.com/data-fusion/docs/how-to/audit-logging)和[权限索引](https://docs.cloud.google.com/iam/docs/roles-permissions/datafusion)。

**清理：** 实例清理包含已审查的命名空间和 DNS 对等连接，并等待原生操作完成、资源消失。DNS 对等连接也可以单独删除。命名空间只随实例一起删除；单独删除命名空间需要开启不可恢复的重置，Steward 不会开启。

- 策略不可读、新增子资源或配置变化会阻止清理。
- 用户数据及引用的存储、网络、服务账号、密钥和主题作为独立资源保留。

参阅 Google 的[实例删除说明](https://docs.cloud.google.com/data-fusion/docs/how-to/delete-instance)。

**限制：** 这些删除接口没有原子的配置条件，清理期间应避免修改实例。

### Dataform

**盘点：** 文件夹、团队文件夹、仓库、工作区、发布与工作流配置、工作流执行和编译结果。文件夹搜索结果受连接权限限制：因共享权限变化而不可见的资源会保留记录，直到删除得到核实。Google 的[团队文件夹搜索约束](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.teamFolders/search)说明了这一限制。检查只读取密钥引用，不读取密钥内容。

**权限：**

- 扫描：`dataform.locations.list`，以及仓库、工作区、发布配置、工作流配置、工作流执行和编译结果各自的 `list` / `get` 权限。
- 文件夹发现和清理：对可访问的目录树授予 `dataform.folders.get`、`dataform.teamFolders.get` 和 `dataform.folders.queryContents`。
- 清理另需：可单独删除的各类型的 `delete` 权限、取消运行中执行所需的 `dataform.workflowInvocations.cancel`，以及删除所选文件夹所需的 `dataform.folders.delete` 或 `dataform.teamFolders.delete`。

参阅 [Dataform 权限索引](https://docs.cloud.google.com/iam/docs/roles-permissions/dataform)。

**清理：**

- **仓库：** 先删除其中的工作区、发布与工作流配置和执行记录，再删除仓库及已审查的编译结果。运行中的执行会先取消，并等待其进入终态。
- **文件夹：** 包含其中的嵌套文件夹和仓库，逐一删除成员后再删除父文件夹。任一祖先文件夹被移动或重建，都会阻止子资源操作和取消流程恢复。
- 新增成员、配置变化或依赖读取失败会阻止清理。
- Git 远程仓库、引用的密钥和 BigQuery 输出表作为独立资源保留；取消执行不会回滚已完成的 BigQuery 操作。

参阅 Google 的[仓库删除约束](https://docs.cloud.google.com/dataform/reference/rest/v1/projects.locations.repositories/delete)和[取消执行行为](https://docs.cloud.google.com/dataform/docs/reference/mcp/tools_list/cancel_workflow_invocation)。

### Dataproc

**盘点：** 集群、作业、辅助节点组、自动伸缩策略和工作流模板。

**权限：**

- 扫描：`compute.regions.list`、`dataproc.clusters.list` / `dataproc.clusters.get`、`dataproc.jobs.list` / `dataproc.jobs.get`、`dataproc.nodeGroups.get`，以及策略和模板对应的 `list/get` 权限。
- 集群清理另需：`dataproc.clusters.delete`、`dataproc.operations.get`，以及用于核实 VM、磁盘、实例组和模板的 Compute 读取与列举权限。
- 所选作业需要 `dataproc.jobs.cancel` / `dataproc.jobs.delete`；所选策略和模板需要各自的删除权限。

参阅 [Dataproc 权限索引](https://docs.cloud.google.com/iam/docs/roles-permissions/dataproc)。

**清理：** 集群清理会审查托管 VM、实例组、生成的实例模板和磁盘。辅助节点组不能单独删除。

- 默认保留作业历史。同时选中作业记录时，会先取消活动作业并删除记录，再删除集群。
- 已挂载且 `autoDelete=false` 的已有磁盘、存储桶、自动伸缩策略及外部服务仍作为独立资源保留。
- 虚拟集群清理会保留其 GKE 集群和节点池。
- 删除工作流模板不会取消已经由该模板启动的工作流。

参阅 Google 的[集群删除契约](https://docs.cloud.google.com/managed-spark/docs/reference/rest/v1/projects.regions.clusters/delete)和 [GKE 清理行为](https://docs.cloud.google.com/managed-spark/docs/guides/dpgke/quickstarts/gke-quickstart-create-cluster)。

**限制：** Steward 会核对集群 UUID 和已审查的配置，但 Dataproc API 无法同时锁定作业和 Compute 成员。清理期间请保留服务写入的身份元数据和标签，并避免修改集群。出现额外的 Compute 自动伸缩器或有状态实例组策略时，清理会停止，等待重新审查。

### Discovery Engine

**盘点：** `global`、`us` 和 `eu` 位置中的集合、数据存储、应用、架构、控制规则、服务配置、会话、对话、助手、文档分支与文档以及目标网站。即使 Cloud Asset Inventory 尚未发现资源，地域选择器也会提供 US/EU。预览版 agent／runtime 资源和每一种数据记录不会单独盘点。

文档正文、会话内容、提示词、架构和连接器配置会在存储盘点结果或日志前脱敏，配置一致性检查使用脱敏前的数据。

**权限：**

- 扫描：所选类型的原生 `list` / `get` 权限、集合与祖先资源的读取权限；有连接器的集合另需 `discoveryengine.dataConnectors.get`，网站数据存储还需网站配置与 sitemap 的读取权限。
- 清理另需：所选类型的 `delete` 权限和 `discoveryengine.operations.get`。

参阅[权限索引](https://docs.cloud.google.com/iam/docs/roles-permissions/discoveryengine)。

**清理：**

- 删除应用会保留其关联的数据存储。
- 删除数据存储前，必须先删除或解除所有关联应用；Steward 不会解除未选中应用的关联。
- 计划会包含已发现的子配置、会话和文档。分支和网站搜索配置没有独立删除接口，随数据存储一起删除。删除应用或数据存储时，也会删除其中的服务数据。
- 清理集合时，先删除其中的应用和数据存储。
- 数据存储删除可能持续数天，任务会等待原生操作完成及已审查资源消失，重启后继续核实。

参阅 Google 的[数据存储删除要求](https://docs.cloud.google.com/generative-ai-app-builder/docs/delete-a-data-store)。

**限制：** 这些删除接口不支持以配置或创建 ID 作为原子条件，清理期间应避免修改相关资源。

### Cloud Monitoring

本节介绍告警策略、自定义仪表板、资源组、指标范围和正常运行时间检查（Uptime 检查）。除指标范围外，清理都需要重新扫描，并且只在资源仍与任务中审查的配置一致时才删除。删除同步完成，Steward 通过单独读取返回 404 来确认，工作进程重启后同样如此。Google 的 DELETE 接口没有版本或 etag 条件，清理期间应避免在 Steward 之外修改这些资源。

#### 告警策略

**盘点：** Steward 列举并读取项目中的所有策略，也包括禁用或无效的策略。记录启用状态、严重程度、用户标签、条件类型、通知渠道名称及创建／变更记录。说明文档和条件表达式先参与配置审查，再从持久化资产和 API 输出中脱敏。

**权限：**

- 扫描：`monitoring.alertPolicies.list` 和 `monitoring.alertPolicies.get`，以及下文仪表板检查所需的 `monitoring.dashboards.list/get`（仅扫描策略时也会执行此检查）。
- 清理另需：`monitoring.alertPolicies.delete`。

**清理：** 通知渠道和监控目标不会随之删除。Steward 还会检查连接项目中引用该策略的仪表板：

- 原生 `AlertChart.name` 使用完整的项目与策略路径，`IncidentList.policyNames` 使用 `alertPolicies/ID`。没有策略筛选的事件列表，按可能引用本项目所有策略处理。
- 文本、日志和指标查询中出现策略名称，不会仅因字面匹配而视为引用。未知组件结构或无效策略名称会阻止清理。
- 新扫描中发现的引用仪表板必须显式选择，并先于策略删除。删除前 Steward 会重复检查引用，并确认所选仪表板已不存在；重启后任务仍使用相同的仪表板前置项。
- 仪表板读取不完整或检查期间发生变化，会阻止清理。策略盘点结果过期时，需要重新扫描后才能刷新关系。

参阅[原生删除接口](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.alertPolicies/delete)和 [AlertChart 与 IncidentList 契约](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards#AlertChart)。

**限制：** 读取失败、不完整或配置变化时保留之前的记录。目前只检查连接项目中的仪表板，其他项目仪表板中的引用尚未覆盖。

#### 自定义仪表板

**盘点：** 原生列表与详情结果一致的自定义仪表板，比对范围包括 `etag`、布局和未知字段。查询、文本、注释和组件内容会在存储前及 API 响应中脱敏，保留显示元数据和配置摘要。系统仪表板不纳入项目清理。

**权限：**

- 扫描：`monitoring.dashboards.list` 和 `monitoring.dashboards.get`。
- 清理另需：`monitoring.dashboards.delete`。

**清理：** Steward 读取仪表板两次，再发送无请求体、无调用方参数的 DELETE。配置变化或保护标签会阻止操作。重启后任务继续同一请求。参阅[仪表板删除契约](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards/delete)。

**限制：** 即使把 `etag` 纳入审查，也无法阻止最后一次读取与 DELETE 之间的外部写入。

#### 资源组

**盘点：** 原生资源组层级，以及固定的一分钟时间窗口内的受监控成员。Compute 实例按不可变的数字 ID 匹配，同名重建的 VM 不会继承旧引用。组筛选条件和描述符定义的成员标签会脱敏。尚未映射的成员类型（包括 AWS 成员）保留为未解析引用。以资源组为目标的 Uptime 检查可以通过已支持的成员引用接入网络扫描。

成员关系是动态的，不代表所有权、保留策略或级联删除。

**权限：**

- 扫描：范围项目上的 `monitoring.groups.list` 和 `monitoring.groups.get`；成员列举同样使用 `monitoring.groups.get`。
- 依赖刷新：`monitoring.uptimeCheckConfigs.list/get`、`monitoring.alertPolicies.list/get` 和 `monitoring.dashboards.list/get`。
- 清理另需：`monitoring.groups.delete`。

参阅[原生组契约](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.groups)和[成员时间窗口契约](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.groups.members/list)。

**清理：** 清理前，Steward 会刷新依赖：读取本项目中所有资源组、Uptime 检查、告警策略和仪表板，逐项列举、读取，再列举一次以发现变化。

- 子组、以该组为目标的 Uptime 检查、筛选条件引用该组的告警策略，以及引用该组的仪表板，都需要显式选择。Steward 不会自动选择成员或父组。
- 仪表板中的时间序列筛选和比值分母按原生结构检查，文本和日志内容不算引用。动态 `GROUP` 筛选、模板变量、未知结构，以及尚未解析的 MQL、PromQL 或 SQL 查询，在无法确定引用时会阻止清理。
- 选中的引用方会先于资源组删除，Steward 会逐一确认它们已不存在。删除前，Steward 会重新读取资源组并两次检查引用方。仍有引用方、配置变化或读取结果不确定时，不会发送 DELETE。
- 删除固定使用 `recursive=false`，不能覆盖。组成员会保留。
- 重启后，任务以相同的前置项继续同一请求。

参阅 [Monitoring 组选择器](https://docs.cloud.google.com/monitoring/api/v3/filters)、[仪表板契约](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/projects.dashboards)和[非递归删除契约](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.groups/delete)。

**限制：** 成员读取失败、结果不一致或分页不完整时，保留上次记录。依赖刷新期间出现权限失败、分页不完整或配置变化时，刷新失败并保留已有关系。

#### 指标范围

**盘点：** 当前连接项目的指标范围、被监控项目关联，以及来自其他项目范围的反向引用。扫描读取完整的范围响应；范围不可读不能证明关联已消失。

**权限：**

- 扫描：连接项目上的 `resourcemanager.projects.get`。
- 清理：范围项目和被监控项目上都需要 `monitoring.metricsScopes.link`，并能读取返回的 Monitoring 操作。

**清理：** 可以解除选中的被监控项目关联；范围及其自身项目关联只读。解除关联会改变范围项目可以查询的指标，但会保留被监控项目、时序数据、仪表板和告警配置。其他项目中的反向关联需要在各自的连接中清理。删除前及等待期间，Steward 会再次核对范围和关联的创建时间。

参阅 Google 的[指标范围配置说明](https://docs.cloud.google.com/monitoring/settings/multiple-projects)，以及原生[读取](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/locations.global.metricsScopes/get)与[解除关联](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v1/locations.global.metricsScopes.projects/delete)契约。

**限制：** API 不支持原子的创建时间或 etag 条件，清理期间应避免重新关联项目。

#### 正常运行时间检查

**盘点：** 项目中的 Uptime 检查，每条列表记录都与详情读取核对。记录 HTTP、TCP、被监控资源或资源组、合成监控设置、周期、检查地域和用户标签。原生名称作为身份，显示名称不必唯一。请求认证、请求头和正文会从资产及 API 输出中脱敏。

目标关系连线覆盖 GCE 实例（按数字 ID）、合成监控的 Cloud Run 函数、Cloud Run 服务、Service Directory 服务、Kubernetes Service 所在的集群，以及 Monitoring 资源组。显式内部检查器关联其对等项目中的 VPC。网络扫描依据这些身份建立关联，标签或响应匹配内容中的 URL 不会成为网络成员依据。缺失的目标保留为未解析引用；选择检查不会自动选择或删除其目标。App Engine 和 AWS 目标、单个 Kubernetes Service，以及旧版隐式内部检查器网络尚未解析。

**权限：** `monitoring.uptimeCheckConfigs.list`、`monitoring.uptimeCheckConfigs.get`；清理还需要 `monitoring.uptimeCheckConfigs.delete`。

**清理：** Google 会拒绝删除仍被告警策略引用的检查，因此 Steward 会查找引用该检查的告警策略和仪表板，范围包括连接项目、发现到的所有指标范围项目，以及相关的日志路由目标：

- 时间序列筛选和比值分母按 Uptime 指标和检查 ID 解析。默认主机的日志面板会结合已核实的日志路由和筛选器检查，并识别显式指定的被监控项目日志来源。
- 查询模板、不支持的查询语言、未知结构，以及尚未审查的其他项目或日志视图来源，会阻止清理。
- 连接项目中的引用策略和仪表板需要显式选择，并先于检查删除。
- 其他项目中的引用方保留为阻断项，需要在其自身连接中处理，不会通过当前连接删除。
- Steward 在读取前后会重新读取指标范围和日志路由；权限失败或配置变化会阻止清理。

删除合成检查会保留其 Cloud Run 函数。同时选择检查与其目标时，先删除检查。DELETE 返回空响应本身不能证明检查已消失。参阅 [Monitoring 删除接口](https://docs.cloud.google.com/monitoring/api/ref_v3/rest/v3/projects.uptimeCheckConfigs/delete)。

**限制：** 读取失败或配置变化时保留之前的记录；只有完整的空清单才能确认检查已不存在。Google 遮蔽的敏感值无法按明文比较。发现范围以外的仪表板、详细的日志视图权限以及不支持的查询语言尚未覆盖。

#### 写入协调

在同一连接、同一项目内，Steward 对告警策略、仪表板、资源组、Uptime 检查和通知渠道的写入逐个执行。请求响应丢失，或失败、取消的请求结果无法确认时，该项目仍保持占用；请继续原任务，让 Steward 核实结果。空响应或丢失的响应不能单独解除占用。旧版本按资源类型分别占用的任务仍能被识别。指向同一项目的其他连接和外部客户端不在此协调范围内。

### Cloud Billing 预算

**盘点：** 凭证有权查看的账单账号中的预算（包括已关闭账号），以及通过连接项目的账单关联找到的单项目预算。即使账单账号列表为空或拒绝访问，项目路径仍可使用。记录展示实际账单账号、预算金额、阈值和所有权范围，并归入连接的全局资源；连接项目并不是预算的所有者。通知投递配置和费用筛选条件不会写入存储的配置，API 输出中的相关字段也会脱敏。

预算从可见清单中消失，不会被视为已删除。Steward 会重新读取已保存的预算，并确认其账单账号仍可读取；只有该预算自身返回 404 才会标为已删除，权限错误会保留历史记录。

新扫描的预算会显示与其使用的通知渠道之间的关系连线。邮件通知渠道的清理仍会被阻止，因为 Steward 目前无法确认所有可能使用它的预算；项目范围的发现同样无法确认这一点。

**权限：**

- 通过账单账号扫描：账单账号可见权限，以及 `billing.accounts.get`、`billing.budgets.list` 和 `billing.budgets.get`。
- 通过项目扫描：`resourcemanager.projects.get` 和 `billing.resourcebudgets.read`。
- 清理另需：`billing.budgets.delete`；通过项目发现的预算，还需要在项目读取权限之外授予 `billing.resourcebudgets.write`。不会使用关闭账单账号或删除项目的权限。

参阅[原生预算访问控制要求](https://docs.cloud.google.com/billing/docs/how-to/budget-api-access-control)。

**清理：** 清理需要重新扫描预算及其账单账号。删除前核对两者的配置，删除后通过预算自身的 404 确认，重启后同样如此。账号丢失或读取被拒绝不会作为删除依据。通知渠道和费用所属项目会被保留。

对于通过项目发现的预算，Steward 还会核对其单项目费用范围，以及项目的账单关联是否未变。重新关联项目或失去项目权限，会阻止清理以及对预算已删除的确认。其他账单配置变化需要重新审查清理任务。参阅[原生预算删除契约](https://docs.cloud.google.com/billing/docs/reference/budget/rest/v1/billingAccounts.budgets/delete)。

**限制：**

- 在已可见账号内读取失败时，扫描失败并保留历史记录。
- 同一连接内对同一账号的写入逐个执行。响应丢失时请继续原任务；结果无法确认的失败或取消请求仍保持对该账号的占用。其他连接和外部客户端不在此协调范围内。
- 原生接口不支持带条件的 DELETE，部分仅在 Console 中可见的设置也不会返回，其他客户端的修改仍可能发生在最后一次读取之后。

### Cloud Identity 身份组

**盘点：** 连接**组目录**中的身份组和成员关系。扫描时需包含全局范围。更换目录或服务账号失去访问权限时，Steward 会保留之前发现的身份组，而不是把它们标为已删除。

**权限：** 启用 Cloud Identity API，并为服务账号授予该目录的组管理权限；仅有项目权限不足以授权。Google 支持[无需全域委派的服务账号 Groups Admin 配置](https://docs.cloud.google.com/identity/docs/how-to/setup)。

**清理：** 身份组可以审查后删除，普通成员关系也可以单独删除。

- 删除组会移除计划中的成员关系，保留成员用户、服务账号和嵌套组对象。
- 锁定组受保护。动态组成员由 Google 管理，不能逐项删除。
- 组配置或成员关系变化会阻止旧计划。
- 组删除不可恢复，并会改变访问权限；外部 IAM 绑定及其他产品中的引用需要另行审查。
- 仅出现权限错误不会被视为删除成功。

参阅 Google 的[组删除契约](https://docs.cloud.google.com/identity/docs/reference/rest/v1/groups/delete)和[身份模型](https://docs.cloud.google.com/architecture/identity/overview-google-authentication)。

### Infrastructure Manager

**盘点：** 部署组、部署、修订、资源记录、预览，以及变更和漂移记录。

**权限：**

- 扫描：`config.locations.list`，以及 deployments、revisions、resources、previews、resourcechanges 和 resourcedrifts 对应的原生 `get` / `list` 权限。部署组还需要 `config.deploymentgroups` 和 `config.deploymentgrouprevisions` 对应的原生读取和列举权限。
- 清理另需：`config.deployments.delete` 或 `config.previews.delete`、`config.operations.get`，以及实际资源的读取和列举权限。部署组还需要 `config.deploymentgroups.deprovision` 和 `config.deploymentgroups.delete`。

Infrastructure Manager 使用部署的服务账号和源配置执行，两者必须保持有效。参阅 [Config 权限索引](https://docs.cloud.google.com/iam/docs/roles-permissions/config)。

**清理：** 修订等子级元数据不能单独删除。

- **部署：** 清理会审查当前修订创建的资源及其嵌套影响。原生策略要么全部删除这些资源，要么全部保留；两种情况下都会删除部署及修订元数据。不支持部分保留。包含无法识别或无法映射资源的部署，需要显式指定 `retain_all_resources=true`。执行服务账号和源文件存储桶仍作为依赖保留。参阅 Google 的[部署删除说明](https://docs.cloud.google.com/infrastructure-manager/docs/delete-deployments)。
- **预览：** 仅删除预览元数据。
- **部署组：** 清理会审查当前引用的部署，以及上次成功组修订后被移除的部署，包括它们的实际资源。Steward 先解除部署组的资源配置，再删除部署组及修订元数据。`retain_all_resources=true` 会保留实际资源并删除部署元数据；显式保留全部被引用的 Deployment 时，这些部署也会保留。不支持部分保留。如果修订结果未知或成功历史有歧义，清理会停止，直到能够确认资源影响。参阅 Google 的[部署组说明](https://docs.cloud.google.com/infrastructure-manager/docs/deployment-groups)。

如果子资源仍需修改 VM／MIG 保留设置或删除保护、处理 GKE 工作负载与网络 finalizer，或解绑 TPU 数据盘，部署与部署组清理会停止。Steward 不能把这些步骤与部署销毁组合执行；可以先保留全部已创建资源，在部署移除后再另行清理。部署自身的保护和删除策略也可能阻止销毁。最终检查会核实元数据和实际资源的结果，重启后同样如此。

**限制：** 这些 API 没有原子的配置条件，清理期间应避免修改部署。

### OS Login SSH 公钥

**盘点：** 连接服务账号自身 OS Login 资料中的 SSH 公钥，显示在连接的全局资源中。Steward 不读取或修改其他用户的资料；项目或实例元数据中的 `ssh-keys` 条目不是独立资源。密钥从资料中消失后，只有读取该密钥返回 404 才会标为已删除。

**权限：**

- 扫描：启用 OS Login API；`oslogin.users.getLoginProfile` 和 `oslogin.users.sshPublicKeys.get`。
- 清理另需：`oslogin.users.sshPublicKeys.delete`。

**清理：** Steward 先重新读取密钥，再将其删除。密钥被替换或到期时间变化时，需要重新扫描。参阅 [OS Login API 参考](https://docs.cloud.google.com/compute/docs/oslogin/rest/v1/users.sshPublicKeys/delete)。

### Resource Manager 组织

**盘点：** 沿文件夹父级链发现的连接项目所属组织。读取失败或项目移动时，保留之前的组织记录。

**权限：** 对每一级父文件夹和组织的 Resource Manager 读取权限。

**清理：** 只读。公开的 v3 API 没有组织删除方法，发现项目所属组织也不代表获得组织级清理授权。参阅 Google 的[组织 API](https://docs.cloud.google.com/resource-manager/reference/rest/v3/organizations)和[独立组织生命周期指南](https://docs.cloud.google.com/resource-manager/docs/delete-standalone-org)。

### Security Command Center

Security Command Center（安全指挥中心）的所有记录在 Steward 中都是只读的，Steward 不会修改任何设置。需要包含全局范围才能读取全局设置。

#### 服务设置

**盘点：** 各服务的预期启用状态和实际生效状态，以及模块设置和更新时间。继承配置的实际状态可能因开通状态或计费资格而不同；`INGEST_ONLY` 表示仅接收发现结果，服务本身未启用。这些设置不代表订阅套餐。

项目扫描使用服务自身的地域列表。Steward 还会在相同地域中读取项目的祖先文件夹和组织的设置；`configurationParent` 标明配置所属层级，项目实际生效的设置单独保留。这不会枚举其他项目，也不涵盖组织中所有私有地域。私有服务配置会脱敏，扩展字段中的也不例外。

GKE 集群的服务设置只能在已知完整的 `projects/.../locations/.../clusters/.../securityCenterServices/...` 名称时，通过 `securitycentermanagement.projects.locations.clusters.securityCenterServices.get` 读取，并适用相同的核验、脱敏和 `securitycentermanagement.securityCenterServices.get` 权限。集群服务不会自动枚举，GKE 标识映射也尚未核实，因此不能据此认定所有集群都已覆盖威胁检测。

**权限：** 启用 **Security Center Management API**，并授予：

- 项目及其祖先上的 `securitycentermanagement.locations.list`、`securitycentermanagement.securityCenterServices.list` 和 `securitycentermanagement.securityCenterServices.get`。
- 用于核验祖先链的 `resourcemanager.projects.get`、`resourcemanager.folders.get` 和 `resourcemanager.organizations.get`。

参阅[服务设置契约](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/organizations.locations.securityCenterServices)和[读取权限](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/projects.locations.securityCenterServices/get)。

**限制：**

- 详情读取失败时保留上次记录。地域或服务从列表中消失时，保留最后的记录及原有的最后发现时间；重新可见后继续更新状态。
- 每页结果都会与已核验的祖先链比对。权限失败或项目迁移会使该页失败，但不会替换已有记录。不再可见的祖先保留原有的最后发现时间。
- Steward 在保存前核验每条记录和分页令牌。列表或详情响应格式错误或不完整时，保留上次完整的记录。

#### 组织订阅

**盘点：** 组织当前的 Security Command Center 套餐，以及最近一次订阅的类型、开始时间和结束时间。最近一次订阅可能已经结束，这些时间不代表当前仍在使用付费套餐；原生套餐字段会单独展示。Steward 沿项目的祖先关系读取所属组织，并在读取后复核祖先关系；项目迁移或权限丢失时保留之前的记录。这些记录不代表项目自身的独立计费权益。

**权限：** 启用 **Security Command Center API**，在组织上授予 `securitycenter.subscription.get`，并授予祖先发现所需的 `resourcemanager.projects.get`、中间文件夹上的 `resourcemanager.folders.get` 和 `resourcemanager.organizations.get`。参见[订阅接口契约](https://docs.cloud.google.com/security-command-center/docs/reference/rest/v1beta2/organizations/getSubscription)和 [Security Command Center 权限](https://docs.cloud.google.com/iam/docs/roles-permissions/securitycenter#securitycenter.subscription.get)。

**限制：** 权限失败或订阅响应缺失都会使扫描失败。

#### 计费元数据

**盘点：** 各项目／地域上显式设置的 Security Command Center 套餐，与组织订阅分开保存。Steward 不据此推断继承套餐、试用期或到期时间。项目扫描使用服务原生的地域列表，全局和地域记录使用不同身份。组织计费通过项目的已核验祖先链、在相同地域中单独读取，`configurationParent` 区分项目值和组织值。API 中没有文件夹级计费设置。组织计费值是显式配置，不能证明项目实际权益或试用历史。

**权限：**

- 项目：启用 **Security Center Management API**，授予 `securitycentermanagement.locations.list` 和 `securitycentermanagement.billingMetadata.get`。
- 组织：在组织上授予 `securitycentermanagement.billingMetadata.get`，并具备 Resource Manager 的项目、文件夹和组织 GET 权限。

参见[项目计费接口契约](https://docs.cloud.google.com/security-command-center/docs/reference/security-center-management/rest/v1/projects.locations/getBillingMetadata)。

**限制：** 读取失败或返回身份变化时保留已有记录；地域从可见列表中消失时保留最后的记录。祖先或地域不再可见时，保留原记录及其时间；组织读取被拒绝、资源缺失或身份变化时，该页失败。

## 常见问题

| 现象 | 处理方法 |
| --- | --- |
| JSON 密钥无法验证 | 确认使用完整的服务账号 JSON 密钥，且服务账号未停用、密钥未撤销。 |
| 提示 API 未启用或 `SERVICE_DISABLED` | 在目标项目启用报错中指出的 API，然后点击**重试**。 |
| 连接验证或扫描返回 `403` | 核对服务账号在目标项目上的授权（来自其他项目的服务账号同样需要），并检查组织策略。 |
| 某个产品的扫描项因权限失败 | 按[各服务说明](#各服务说明)中该产品的扫描权限补齐授权，然后点击**重试**。 |
| 没有找到刚创建的资源 | 检查项目和扫描地域，处理失败项，并等待 Cloud Asset Inventory 更新后重扫。 |
| 相同名称出现多个 VM | 核对项目和可用区；不同位置的同名资源有不同的完整资源名称。 |
| 清理因保护设置被阻止 | 按任务中的原因检查 VM、Cloud SQL、磁盘 `autoDelete`、存储桶内容或保护标签；只修改本地记录无法解决。见[删除保护](#删除保护)。 |
| 清理因配置变化或需要刷新而被阻止 | 重新扫描该资源及其依赖，再创建新的清理任务。 |
| 路由器清理无法启动，提示路由器存在未确认的修改 | 另一个任务在该路由器上仍有待确认的修改。**继续**或完成那个任务，见[串行修改与恢复](#串行修改与恢复)。 |
| 响应丢失后，Monitoring 或预算清理一直处于占用状态 | **继续**原任务，让 Steward 核实结果。 |
| 升级前启动的 Security Command Center 扫描失败 | 重新启动扫描。 |
| Discovery Engine 数据存储删除数小时仍未完成 | 数据存储删除可能持续数天，任务会持续等待，重启后同样如此。 |

## 下一步

[扫描资源](./scans.md) · [查看资源关系](./topology.md) · [清理资源](./cleanup.md)
