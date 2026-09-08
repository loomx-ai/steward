---
title: "Amazon Web Services（AWS）"
description: "连接 AWS 账号，配置资源发现权限，并审查资源与 CloudFormation 栈的清理影响。"
navTitle: "AWS"
---

Steward 使用当前连接的凭证访问一个 AWS 账号。资源发现结合 Cloud Control API 与 Resource Explorer；资源清单可以包含没有清理能力的只读资源。

## 准备凭证

在 **设置 → 云连接 → 添加连接** 中选择 **AWS**。

| 凭证类型 | 需要填写 | 适用方式 |
| --- | --- | --- |
| Access Key | Access Key ID、Secret Access Key | 为专用 IAM 身份配置的访问密钥。 |
| 临时凭证 | Access Key ID、Secret Access Key、Session Token、到期时间 | 从已授权会话获得的完整临时凭证。 |

当前连接使用显式填写的凭证，不自动读取本机 AWS profile、SSO 会话或实例角色，也不提供自动 AssumeRole 刷新。临时凭证过期后，使用「替换凭证」更新；新凭证应保持原来的云身份。

当前实现面向 AWS 商业分区。中国区和 GovCloud 的身份分区与端点尚未适配，不应按普通商业区连接使用。

## 配置发现权限

连接验证使用 STS `GetCallerIdentity` 识别账号；地域发现使用 EC2 `DescribeRegions`。有效身份不代表已具备全部资源读取权限。

| 用途 | 需要核对的 IAM Action |
| --- | --- |
| 地域与网络目录 | `ec2:DescribeRegions`、`ec2:DescribeVpcs`、`ec2:DescribeSubnets` |
| Resource Explorer 补充发现 | `resource-explorer-2:Search`；Steward 调用 `ListResources`，该 API 使用 Search 权限。 |
| Cloud Control 盘点与详情 | `cloudformation:ListResources`、`cloudformation:GetResource`，以及资源类型读取处理器要求的产品权限。 |
| CloudFormation 栈详情与归属 | `cloudformation:DescribeStacks`、`cloudformation:ListStackResources`、`cloudformation:GetTemplate` |

Cloud Control API 的授权使用 `cloudformation:` 前缀。只授予这两个通用读取 Action，并不能替代 EC2、S3、RDS 等资源类型自身的读取权限。可根据 AWS 的[资源操作与处理器权限](https://docs.aws.amazon.com/cloudcontrolapi/latest/userguide/resource-operations.html)核对所扫描类型。

Resource Explorer 必须能在被查询地域返回资源。Steward 使用该地域的默认视图；视图中的过滤条件会影响可见范围。若需要管理员完成索引、视图或服务关联角色配置，请先在 AWS 中完成。参阅 [Resource Explorer 设置](https://docs.aws.amazon.com/resource-explorer/latest/userguide/getting-started-setting-up.html)和 [ListResources 权限说明](https://docs.aws.amazon.com/resource-explorer/latest/apireference/API_ListResources.html)。

## 完成第一次扫描

1. 添加并验证连接，核对识别到的 AWS 账号。示例名称可使用「AWS · 测试环境」。
2. 刷新地域。Steward 只自动发现已启用地域；需要其他地域时，先在 AWS 启用，再刷新列表。
3. 从一个有已知资源的地域开始，例如 `us-east-1`。选择该地域，或一个已知 VPC。
4. 运行扫描并检查失败项；不要只根据总资源数判断是否完成。
5. 在 **资源** 中搜索已知实例、存储桶或资源栈，核对地域、账号与资源标识。需要 IAM 等全局资源时，补扫全局范围。

**完成标志：** 找到预期资源，所属账号与地域正确，扫描详情中没有未处理的权限或类型错误。可以继续[查看资源关系](./topology.md)或扩展扫描范围。

## 资源范围与关系

| 类别 | 常用资源示例 |
| --- | --- |
| 计算与网络 | EC2 实例、EBS 卷、VPC、子网、安全组、网卡、路由表、NAT 与 Internet Gateway |
| 存储与数据库 | S3 存储桶、EFS、DynamoDB 表、RDS 实例与集群 |
| 应用与容器 | Lambda、ECS、EKS、ECR、负载均衡与目标组、API Gateway |
| 消息与工作流 | SQS、SNS、EventBridge、Kinesis、Step Functions |
| 身份、监控与编排 | IAM 身份及策略、CloudWatch、日志组、CloudFormation 栈 |

Cloud Control 负责已接入类型的资源与属性读取；Resource Explorer 补充其他可发现类型。可以发现某个资源，不等于 Steward 已支持它的删除操作。可清理性应以资源详情和清理计划为准。

EC2 的子网对应资源全景中的子网层级。IAM 等全局资源不属于某个 VPC；按 VPC 盘点时，不应据此判断整个账号已经完整扫描。

## 审查清理影响

清理需要 `cloudformation:DeleteResource`、`cloudformation:GetResourceRequestStatus` 以及目标资源类型的删除与查询权限。CloudFormation 栈使用 `cloudformation:DeleteStack` 等栈操作权限。Internet Gateway 的准备操作还可能需要解除 VPC 关联。

CloudFormation 管理的资源应结合栈一起审查：Steward 读取栈资源和模板，识别归属与 `DeletionPolicy`。`Retain` 或 `RetainExceptOnCreate` 资源按保留关系处理；启用终止保护的栈会阻止删除。无法确认归属或策略时，先解决审查中的不确定项。

资源的云端删除保护、非空存储桶、依赖关系和状态变化仍可能导致操作失败。Steward 会等待异步操作并回读结果；不要把请求已接受当作资源已经删除。执行前阅读[清理资源](./cleanup.md)。

## 常见问题

| 现象 | 处理方法 |
| --- | --- |
| `ExpiredToken` 或身份验证失败 | 检查密钥配对、Session Token、到期时间和账号分区；替换完整凭证。 |
| 缺少某个地域 | 检查该地域是否已启用，以及 `ec2:DescribeRegions` 权限。 |
| Resource Explorer 返回未授权或没有资源 | 检查调用地域的默认视图、视图过滤条件、Search 权限和索引状态。 |
| Cloud Control 返回 `AccessDenied` | 同时检查 Cloud Control Action 与具体资源处理器所需的产品权限。 |
| 某个类型在该地域不可用 | 检查 AWS 对该类型和地域的支持，缩小到实际使用的扫描类型。 |
| 栈或资源删除失败 | 检查终止保护、保留策略、云端依赖与操作错误详情；不要反复直接提交删除。 |

下一步：[扫描资源](./scans.md) · [查询资源](./resources.md) · [清理资源](./cleanup.md)
