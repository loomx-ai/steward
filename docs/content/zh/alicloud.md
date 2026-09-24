---
title: "阿里云"
description: "连接阿里云账号，开通资源中心并配置 RAM 权限，完成第一次扫描，并了解资源覆盖范围与清理行为。"
navTitle: "阿里云"
---

本页帮助你把阿里云账号接入 Steward、配置所需的 RAM 权限，并了解 Steward 能发现和清理哪些资源。

一个阿里云连接对应一个云身份。Steward 结合资源中心检索与各云产品的查询 API 获取数据。建议先完成一个已知地域的盘点，再扩大扫描范围或补充清理权限。

## 准备凭证

打开用户菜单 → **设置** → **云连接** → **添加云连接**，选择 **阿里云**，并在 **站点** 中选择与账号一致的 **中国站** 或 **国际站**。站点与之后扫描的资源地域是两项独立设置。

| 凭证类型 | 需要填写 | 使用时注意 |
| --- | --- | --- |
| **阿里云 Access Key** | AccessKey ID、AccessKey Secret | 使用专用 RAM 身份；密钥轮换后替换连接凭证。 |
| **阿里云 STS** | AccessKey ID、AccessKey Secret、Security Token、到期时间 | 各项必须来自同一次凭证签发；有效期要足够完成扫描或清理任务。 |
| **OAuth** | 无需填写，点击 **用浏览器登录** | [浏览器登录](./connections.md#browser)：在对应站点完成登录与授权，核对授权身份后返回 Steward。 |
| **OIDC 工作负载身份** | 角色 ARN、OIDC 身份提供商 ARN；可选的写入角色 ARN | 服务端已配置 [OIDC](./oidc.md) 时使用，无需保存长期密钥即可换取临时凭证。 |

- **替换凭证** 只接受原云身份的新凭证。要接入另一个账号，请新建连接，避免不同账号的资源记录混在一起。具体操作见[连接云账号](./connections.md)。

## 配置权限

连接验证通过只说明凭证能识别云身份。请按下文授予读取权限，再通过扫描确认 Steward 能读到具体资源。

### 开通资源中心

确认目标账号已开通 **资源中心（Resource Center）**。若尚未开通，由账号管理员在资源管理控制台开通，并等待首次资源收集完成后再扫描。参阅阿里云的[资源中心开通说明](https://www.alibabacloud.com/help/en/resource-management/resource-center/user-guide/activate-resource-center)。

### 盘点所需的读取权限

按实际扫描的资源类型授予 RAM 读取权限：

| 用途 | RAM Action | 说明 |
| --- | --- | --- |
| 资源中心检索 | `resourcecenter:SearchResources` | 查询当前账号中可访问的资源。 |
| 资源配置详情 | `resourcecenter:GetResourceConfiguration` | 授权调用 `BatchGetResourceConfigurations` API（权限名与 API 名不同）。 |
| 地域与网络选择 | `vpc:DescribeRegions`、`vpc:DescribeVpcs`、`vpc:DescribeVSwitches` | 发现地域、VPC 和交换机。 |
| ECS 盘点 | `ecs:DescribeInstances`，以及所选其他类型的读取权限 | 实例、磁盘、网卡分别使用各自的产品 API。 |
| 数据库加密密钥 | `rds:DescribeDBInstanceTDE`、`rds:DescribeDBInstanceEncryptionKey`、`polardb:DescribeDBClusterTDE`、`kvstore:DescribeInstanceTDEStatus`、`kvstore:DescribeEncryptionKey` | 识别 RDS、PolarDB、Redis 使用的 KMS 密钥。缺少这些权限时，对应类型的扫描会失败，且在完整扫描成功之前，删除 KMS 密钥会一直被阻断。 |
| RAM 成员与授权 | `ram:ListUsersForGroup`、`ram:ListEntitiesForPolicy` | 识别用户组成员和自定义策略的授权对象，从而保护连接自身使用的用户组和策略。 |
| 其他云产品 | 对应产品的 List、Get、Describe 权限 | 每增加一种资源类型就需要相应的读取权限；不存在覆盖所有产品的固定最小权限清单。 |

上表列出的是常用权限，不是完整的授权策略。资源中心的两项权限可对照 [SearchResources](https://www.alibabacloud.com/help/en/resource-management/resource-center/developer-reference/api-resourcecenter-2022-12-01-searchresources) 和 [BatchGetResourceConfigurations](https://www.alibabacloud.com/help/en/resource-management/resource-center/developer-reference/api-resourcecenter-2022-12-01-batchgetresourceconfigurations) 的 RAM 授权表核对。

### 清理所需的权限

盘点权限不包含清理权限。对准备删除的每种类型，还需要授予：

- 删除操作，以及确认删除结果所需的状态查询权限。
- 清理任务中列出的解除关联或其他准备操作。
- 关闭[删除保护](#deletion-protection)的操作，例如 ECS 实例的 `ecs:ModifyInstanceAttribute`、RDS 的 `rds:ModifyDBInstanceDeletionProtection`。

## 完成第一次扫描

先从一个有已知资源的地域开始，确认结果正确后再扩大范围。

1. 添加连接，使用易识别的名称，例如「阿里云 · 测试环境」。Steward 会在保存前验证凭证；核对显示的账号身份。
2. 在该连接上打开 **管理地域** → **从云 API 刷新**，找到一个已有资源的地域，例如 `cn-hangzhou`。
3. 切换到该连接，进入 **扫描** → **开始扫描**。选择 **选择部分地域** 并勾选该地域；也可以选择 **指定 VPC / vSwitch**，从一个已知的 VPC 或交换机开始。
4. 打开这次扫描，查看失败的类型和权限错误。补齐权限后点击 **重试**。
5. 在 **资源** 中搜索一个已知的实例 ID，核对地域、VPC、交换机和最后发现时间。

**完成标志：** 预期资源出现在当前连接下，属性与阿里云控制台一致，扫描中没有未处理的失败项。确认无误后，再扩大到其他地域和全局资源。

完整操作流程见[第一次资源盘点](./tutorials/first-inventory.md)；跨地域核对账号覆盖与缺失资源，见[阿里云资源盘点教程](./tutorials/alicloud-resource-inventory.md)。

## 资源覆盖范围

Steward 识别 203 类阿里云资源，其中 177 类具有原生清理操作；资源中心返回的其他类型以只读清单展示。所有产品 API 调用都锁定到 api.aliyun.com 发布的官方元数据，列表与读取的响应路径均逐一对照官方响应结构和示例验证。覆盖范围仍在扩展，尚未涵盖阿里云的全部产品。

常用资源类型：

| 类别 | 资源类型 |
| --- | --- |
| 计算与应用 | ECS 实例、磁盘和网卡；SAE；EMR；无影云电脑 |
| 网络 | VPC、交换机、安全组、NAT 网关、弹性公网 IP |
| 负载均衡 | ALB、NLB、CLB 实例及其监听和服务器组 |
| 数据库 | RDS、Redis、PolarDB |
| 消息与事件 | Kafka、RocketMQ、RabbitMQ 及其 Topic 和消费组；事件总线 |
| 存储、日志与镜像 | OSS、日志服务、容器镜像服务 |
| 监控与治理 | 云监控、配置审计 |

实际能发现哪些资源，取决于资源类型支持情况、所选地域和当前身份的权限。

## 资源关系

在 **资源全景** 中按地域、VPC 和交换机定位资源，在资源详情的 **关系** 页签中查看依赖。除网络归属外，Steward 还会读取：

- **加密密钥：** 若资源把某个 KMS 密钥作为加密密钥（例如 ECS 磁盘或快照、启用服务端加密的 OSS 存储桶、RDS、PolarDB 或 Redis 实例），Steward 会将其标记为正在使用该密钥——删除密钥会导致该资源的数据无法读取。RDS、PolarDB 和 Redis 的透明数据加密（TDE）密钥通过[数据库加密密钥权限](#盘点所需的读取权限)读取。
- **RAM 成员关系：** 每个 RAM 用户组包含哪些用户、每条自定义策略授权给了哪些对象，避免把仍在使用的用户组或策略误判为闲置。

仅凭单个地域的视图，无法判断跨地域连接或全局资源是否仍在使用。审核清理范围前，请先扫描相关地域及其依赖资源。

## 清理行为与保护

创建清理任务不会删除任何资源。只有在[审核任务](./cleanup.md#review)并确认执行后，才会调用云端操作。

<span id="deletion-protection"></span>

### 删除保护

云端的删除保护不会拦住你已确认的清理任务：Steward 会在删除前先关闭保护。真正的把关环节是你对任务的审核。以下类型的删除保护或释放保护会被 Steward 关闭：

ECS 实例、ACK 集群、弹性伸缩组、ALB/NLB/CLB 实例、弹性公网 IP、共享带宽、NAT 网关、RDS、PolarDB、Redis、MongoDB、HBase、Lindorm、KMS 密钥、ROS 资源栈、日志服务 Project、EMR 集群。

### 各产品的删除行为

| 资源 | 行为 |
| --- | --- |
| ECS 实例 | 删除时会强制停止运行中的实例并释放。执行前核对对实例及关联资源的影响。 |
| VPC 与交换机 | 先审查实例、网卡、网关等依赖。存在选择范围外的依赖时，按任务中的阻断项处理。 |
| OSS 存储桶 | 对象、版本、保留策略或其他云端条件可能阻止删除。以审核结果和 OSS 返回信息为准。 |
| ALB、NLB、CLB 实例 | 先逐项删除其监听和 CLB 虚拟服务器组。服务器组、CLB 访问控制策略组和证书是独立资源：仍被监听或转发规则使用时，云端会拒绝删除；由 ALB Ingress 管理的服务器组不会直接删除。 |
| Kafka、RocketMQ 4.0 与 5.0 实例 | 先逐项删除实例中的 Topic 和消费组，并在实例仍可查询时确认各自已删除。RocketMQ 4.0 只删除本账号持有的 Topic，其他账号授权的 Topic 不会删除。删除 Topic 会丢弃其中的消息。 |
| 容器镜像服务命名空间 | 删除命名空间会一并删除其中的仓库和镜像，因此任务会先逐个删除仓库。 |
| 日志服务 Project | 删除 Project 会删除其全部日志库（包括服务创建的 `internal-` 日志库）。任务把这些日志库列为随 Project 删除，并在删除后逐一确认。 |
| VPN 网关 | SSL 客户端证书、SSL 服务端和 IPsec 服务端先于所属 VPN 网关删除。 |
| 云监控 | 可清理自建应用分组、报警规则、报警联系人和联系组。由其他服务、标签或资源组同步生成的应用分组会被来源重新创建，因此不会直接删除。删除联系人或联系组后，引用它们的报警规则将不再通知这些对象。 |
| SAE | 应用先于所在命名空间删除；各地域的默认命名空间保留。应用删除是异步操作，Steward 最多等待 10 分钟确认完成。 |
| 配置审计 | 规则与合规包在 `cn-shanghai` 和 `ap-southeast-1` 盘点。删除合规包会一并删除它创建的规则，任务会在之后逐一确认；这些规则不能单独删除。账号组仅盘点，不清理。 |
| 无影云电脑 | 只清理不在云电脑池中的按量付费云电脑；包年包月云电脑到期自动释放。办公网络要等其中的云电脑全部释放后才删除；系统策略保留。 |
| 事件总线 | 事件规则先于所属事件总线删除。 |
| RabbitMQ | 只删除按量付费实例；包年包月实例到期自动释放。 |
| EMR 集群 | 只清理按量付费集群。先关闭释放保护再删除；集群状态变为 `TERMINATED` 即视为完成，最多等待 10 分钟。 |
| 容器集群、资源栈等受托管资源 | 核对控制器及其管理的对象，不要只看一个资源名称就判断影响范围。 |

## 数据时效

资源中心的收集存在延迟，新资源要等资源中心记录后才会出现在 Steward 中；产品 API 也可能因权限或限流返回不完整的结果。某次扫描没有找到某个资源，不等于云端已不存在该资源；下结论前先检查扫描中的失败项。

## 常见问题

| 现象 | 处理方法 |
| --- | --- |
| 连接验证失败 | 检查站点、AccessKey 是否配对、STS Token 和到期时间；查看错误详情中的云端错误码。 |
| 提示 `ServiceNotEnabled` | [开通资源中心](#开通资源中心)，并等待首次收集完成。 |
| 验证通过，但扫描报 `NoPermission` 或 `Forbidden` | 在扫描详情中找到失败的产品和 API，补充对应的读取权限后点击 **重试**。 |
| 资源或关系缺失 | 核对当前连接、地域与扫描范围，处理失败项；新建的资源等资源中心收集后再重新扫描。 |
| KMS 密钥无法删除 | 确认已授予数据库加密密钥的读取权限，且 RDS、PolarDB、Redis 最近一次扫描已完整成功。 |
| 无法完成清理 | 查看任务中的阻断项、云端错误码和请求 ID，再检查依赖、资源状态和所需操作权限。 |

## 下一步

- [扫描资源](./scans.md)
- [查询资源](./resources.md)
- [查看资源关系](./topology.md)
- [清理资源](./cleanup.md)
