---
title: "阿里云"
description: "连接阿里云账号，核对盘点权限，扫描资源并审查清理影响。"
navTitle: "阿里云"
---

一个阿里云连接对应一个云身份。先用它完成一个已知地域的资源盘点，再扩大扫描范围或配置清理权限。

## 准备凭证

在 **设置 → 云连接 → 添加连接** 中选择 **阿里云**，并选择与账号一致的 **中国站** 或 **国际站**。站点与后续选择的资源地域是两项独立设置。

| 连接方式 | 需要填写 | 使用时注意 |
| --- | --- | --- |
| AccessKey | AccessKey ID、AccessKey Secret | 使用专用 RAM 身份；轮换后替换连接凭证。 |
| STS 临时凭证 | AccessKey ID、AccessKey Secret、Security Token、到期时间 | 四项必须来自同一次凭证签发；预留足够时间完成扫描或任务。 |
| 浏览器授权 | 按浏览器授权页完成登录与授权 | 选择对应站点，核对授权身份，再返回 Steward。 |

使用「替换凭证」维护现有连接时，新凭证必须属于原来的云身份。切换账号应新建连接，避免把不同账号的资源记录混在一起。具体操作见[连接云账号](./connections.md)。

## 配置盘点权限

确认目标账号的 **资源中心（Resource Center）** 已启用。若尚未启用，由账号管理员在资源管理控制台完成开通，并等待首次资源收集完成。参阅阿里云的[资源中心开通说明](https://www.alibabacloud.com/help/en/resource-management/resource-center/user-guide/activate-resource-center)。

Steward 结合资源中心与各云产品的查询 API 获取数据。按实际扫描类型配置 RAM 读取权限：

| 用途 | 常用 RAM Action | 说明 |
| --- | --- | --- |
| 资源中心检索 | `resourcecenter:SearchResources` | 查询当前账号中可访问的资源。 |
| 资源中心配置详情 | `resourcecenter:GetResourceConfiguration` | 对应 `BatchGetResourceConfigurations` API；权限名与 API 名不同。 |
| 地域和网络目录 | `vpc:DescribeRegions`、`vpc:DescribeVpcs`、`vpc:DescribeVSwitches` | 用于地域发现和 VPC、交换机选择。 |
| ECS 盘点 | `ecs:DescribeInstances` 等所选资源类型的读取权限 | 实例、磁盘、网卡等分别使用对应产品 API。 |
| 其他云产品 | 对应产品的 List、Get、Describe 权限 | 随扫描的资源类型增加，不存在覆盖所有产品的固定最小权限清单。 |

上表用于核对常见权限，不是一份完整的跨产品授权策略。资源中心的两项权限可对照 [SearchResources](https://www.alibabacloud.com/help/en/resource-management/resource-center/developer-reference/api-resourcecenter-2022-12-01-searchresources) 和 [BatchGetResourceConfigurations](https://www.alibabacloud.com/help/en/resource-management/resource-center/developer-reference/api-resourcecenter-2022-12-01-batchgetresourceconfigurations) 的 RAM 授权表。

连接验证通过说明凭证可以识别云身份。是否有权读取具体资源，需要通过扫描确认。

## 完成第一次扫描

1. 创建并验证连接。核对账号身份，使用易识别的连接名称，例如「阿里云 · 测试环境」。
2. 刷新地域列表，确认一个已有资源的地域，例如 `cn-hangzhou`。
3. 切换到该连接，进入 **扫描**，先扫描该地域；也可从已知 VPC 或交换机开始。
4. 查看扫描详情，处理失败的类型或权限项，再到 **资源** 中搜索一个已知的实例 ID。
5. 核对地域、VPC、交换机与最后发现时间。结果正确后，再扩大到其他地域和全局资源。

**完成标志：** 能在当前连接下找到预期资源，资源属性与阿里云控制台一致，扫描详情没有尚未处理的失败项。完整操作流程见[第一次资源盘点](./tutorials/first-inventory.md)。

## 资源范围与关系

常用资源包括 ECS 实例、磁盘和网卡，VPC、交换机、安全组、NAT 网关、弹性公网 IP，负载均衡，RDS、Redis、PolarDB，以及 OSS、容器与资源编排相关资源。实际可发现的范围取决于资源类型支持、所选地域和当前身份的权限。

在 **资源全景** 中按地域、VPC 和交换机定位资源；在资源详情中查看依赖关系。跨地域连接和全局资源不应只通过一个地域的图判断是否仍被使用。查看清理范围前，应先扫描相关地域与依赖资源。

资源中心存在收集延迟，产品 API 也可能因权限或限流返回不完整结果。某次扫描未找到资源，不等于已经确认云端资源不存在。

## 审查清理影响

盘点权限不包含清理权限。准备清理时，还需要目标资源的删除、状态查询，以及计划中解除关联或其他准备操作的权限。

- **ECS 实例：** 支持的删除流程可能强制停止并释放实例；删除保护的处理也可能包含修改实例属性。执行前核对实例及关联资源的影响，不能把云端删除保护当作唯一的执行屏障。
- **VPC 与交换机：** 先审查实例、网卡、网关和其他依赖。存在范围外依赖时，按计划中的阻断项处理。
- **OSS 存储桶：** 对象、版本、保留策略或其他云端条件可能阻止删除。以审查结果和 OSS 返回的信息为准。
- **受托管资源：** 对容器集群、资源栈等资源，核对控制器及其管理对象，避免只看一个资源名称就判断影响范围。

创建清理任务不会立即删除资源。完成[清理审查](./cleanup.md)并确认执行后，才会调用云端操作。

## 常见问题

| 现象 | 处理方法 |
| --- | --- |
| 连接验证失败 | 检查站点、AccessKey 配对、STS Token 和到期时间；查看错误详情中的云端错误码。 |
| 提示 `ServiceNotEnabled` | 检查资源中心是否启用，并等待首次收集完成。 |
| 验证成功，但扫描报 `NoPermission` 或 `Forbidden` | 在扫描详情中找到失败的产品和 API，补充该项读取权限后重试。 |
| 资源缺失或关系不全 | 核对当前连接、地域与扫描范围，处理失败项；新资源可稍后重扫。 |
| 无法完成清理 | 查看具体阻断项、操作错误码和请求 ID，检查依赖、资源状态和所需操作权限。 |

下一步：[扫描资源](./scans.md) · [查询资源](./resources.md) · [查看资源关系](./topology.md)
