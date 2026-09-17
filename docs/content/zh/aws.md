---
title: "Amazon Web Services（AWS）"
description: "连接 AWS 账号，配置资源发现权限，并审查资源、生命周期与 CloudFormation 栈的清理影响。"
navTitle: "AWS"
---

Steward 使用当前连接的凭证访问一个 AWS 账号。每个已支持的资源类型都有一个权威发现来源：具备列举、读取和删除处理器的类型使用 Cloud Control API；Cloud Control 支持不完整的类型使用该产品自身的 API。Resource Explorer 补充其他资源的广泛索引，但不会移除权威来源观测到的资源。

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
| Cloud Control 盘点与详情 | `cloudformation:ListResources`、`cloudformation:GetResource`，以及资源类型处理器要求的产品读取权限 |
| 挂载与成员关系 | `ec2:DescribeInstances`、`ec2:DescribeNetworkInterfaces`、`ec2:DescribeVolumes`、`autoscaling:DescribeAutoScalingGroups`、`eks:DescribeNodegroup` |
| 产品 API 盘点 | `ec2:DescribeImages`、`ec2:DescribeSnapshots`、`es:ListDomainNames`、`es:DescribeDomains`、`rds:DescribeDBClusters`、`rds:DescribeDBInstances`（DocumentDB）、`dms:DescribeReplicationInstances`、`fsx:DescribeFileSystems`、`storagegateway:ListGateways`、`storagegateway:DescribeGatewayInformation`、`route53domains:ListDomains`、`drs:DescribeSourceServers`、`mobiletargeting:ListTemplates`、`mobiletargeting:GetSmsTemplate` |
| 组织树 | `organizations:ListRoots`、`organizations:ListOrganizationalUnitsForParent` |
| Resource Explorer 索引 | `resource-explorer-2:Search`；Steward 调用 `ListResources`，该 API 使用 Search 权限 |
| CloudFormation 栈详情与归属 | `cloudformation:DescribeStacks`、`cloudformation:ListStackResources`、`cloudformation:GetTemplate` |

Cloud Control API 的授权使用 `cloudformation:` 前缀。只授予通用读取 Action，并不能替代 EC2、S3、RDS 等资源类型自身的读取权限。可根据 AWS 的[资源操作与处理器权限](https://docs.aws.amazon.com/cloudcontrolapi/latest/userguide/resource-operations.html)核对所扫描类型。只需授予实际使用服务对应的行；某个类型被拒绝只会使它自己的扫描项失败，不影响其他类型。

Resource Explorer 必须能在被查询地域返回资源。Steward 使用该地域的默认视图；视图中的过滤条件会影响可见范围。参阅 [Resource Explorer 设置](https://docs.aws.amazon.com/resource-explorer/latest/userguide/getting-started-setting-up.html)和 [ListResources 权限说明](https://docs.aws.amazon.com/resource-explorer/latest/apireference/API_ListResources.html)。

## 完成第一次扫描

1. 添加并验证连接，核对识别到的 AWS 账号。示例名称可使用「AWS · 测试环境」。
2. 刷新地域。Steward 只自动发现已启用地域；需要其他地域时，先在 AWS 启用，再刷新列表。
3. 从一个有已知资源的地域开始，例如 `us-east-1`。选择该地域，或一个已知 VPC。
4. 运行扫描并检查失败项；不要只根据总资源数判断是否完成。
5. 在 **资源** 中搜索已知实例、存储桶或资源栈，核对地域、账号与资源标识。需要 IAM 等全局资源时，扫描范围应包含全局。

**完成标志：** 找到预期资源，所属账号与地域正确，扫描详情中没有未处理的权限或类型错误。可以继续[查看资源关系](./topology.md)或扩展扫描范围。

## 资源范围

| 类别 | 资源类型 |
| --- | --- |
| 计算 | EC2 实例、AMI、启动模板、专用主机、容量预留与容量预留队列、Auto Scaling 组、Lightsail 实例、密钥对、Instance Connect 终端节点 |
| 容器与无服务器 | EKS 集群、托管节点组、Fargate 配置文件与插件；ECS 集群、服务与任务定义；ECR 仓库与拉取缓存规则；Lambda；App Runner |
| 网络 | VPC、子网、CIDR 块、路由表、安全组、网络 ACL、网卡、弹性 IP，互联网、仅出口、NAT 与虚拟私有网关，客户网关、VPN 连接、VPC 对等连接、终端节点与终端节点服务、流日志、DHCP 选项集 |
| 中转与混合网络 | 中转网关及其路由表、VPC/对等/Connect 挂载、组播域及其关联、成员与源；Direct Connect 连接、链路聚合组、网关、网关关联与虚拟接口；Cloud WAN 全球网络与核心网络；Global Accelerator 加速器、侦听器与终端节点组 |
| 负载均衡、边缘与 DNS | 应用/网络/网关负载均衡器及其侦听器与目标组、传统负载均衡器、CloudFront 分配、WAF Web ACL、Shield Advanced 防护、Route 53 托管区域、运行状况检查、Resolver 规则与注册域名、API Gateway API 与自定义域名、VPC Lattice |
| 存储与备份 | S3 存储桶、EBS 卷与快照、Data Lifecycle Manager 策略、EFS 文件系统、挂载目标与接入点、FSx 文件系统、Storage Gateway、AWS Backup 备份库、备份计划与资源分配、弹性灾难恢复源服务器 |
| 数据库与分析 | RDS 与 Aurora、RDS 代理、Aurora DSQL、DynamoDB、DocumentDB 与 DocumentDB 弹性集群、Neptune 与 Neptune Analytics、Keyspaces、ElastiCache、MemoryDB、Timestream、OpenSearch Service 与 Serverless、Redshift 与 Redshift Serverless、Glue 数据库、Athena 工作组、EMR Serverless、Kinesis、Apache Flink 托管服务、DMS、MSK、Amazon MQ、DataZone、QuickSight 控制面板与数据集 |
| 消息与应用 | SQS、SNS、EventBridge 事件总线与规则、Step Functions、CodePipeline、Cloud Map 命名空间与服务、AppRegistry 应用程序、Pinpoint 短信模板、IVS 频道与实时舞台、Kendra 索引、SageMaker 终端节点、终端节点配置、模型与 HyperPod 集群、AWS PCS 与 Batch 计算环境 |
| 身份、安全与治理 | IAM 用户、用户组、角色、实例配置文件与托管策略，IAM Identity Center 实例与组，Organizations 组织、组织单元与成员账号，KMS 密钥与别名、ACM 证书、GuardDuty、Security Hub、Macie、网络防火墙、IAM 访问分析器、CloudTrail 跟踪与事件数据存储 |
| 监控与编排 | CloudWatch 告警、控制面板与日志组，Synthetics 金丝雀、X-Ray 组、可观测性访问管理器、Managed Grafana 与 Prometheus、CloudFormation 资源栈与 StackSet |

子类型通过父资源列举：EKS 节点组、插件与 Fargate 配置文件按集群列举；负载均衡侦听器按负载均衡器列举；EFS 挂载目标按文件系统列举；组播关联、成员与源按组播域列举；Identity Center 组按实例列举；组织单元按完整组织树列举。父资源读取失败时，子类型扫描项会失败，而不是报告空列表。

全局服务从其主地域读取：IAM、CloudFront、Route 53、Organizations 与 Shield Advanced 使用 `us-east-1`；Global Accelerator 与 Cloud WAN 使用 `us-west-2`。扫描这些资源需要包含全局范围。CloudFront 范围的 WAF Web ACL 在 `us-east-1` 地域扫描中列出。

## 关系与归属

关系来自资源模型（例如资源所属的 VPC、子网与安全组），以及从产品 API 读取的挂载事实：

- **实例存储与网卡：** 设置了 `DeleteOnTermination` 的 EBS 卷或网卡由实例管理，终止实例会将其删除；其他挂载的卷在实例删除后再删除。
- **Auto Scaling 与 EKS：** Auto Scaling 组中的实例、EKS 托管节点组的 Auto Scaling 组由各自的控制器管理，只能通过控制器清理。
- **服务托管网卡：** NAT 网关、VPC 终端节点、负载均衡器、EFS 挂载目标或 Lambda 创建的网卡属于对应服务，不能直接删除。
- **弹性 IP：** 只有在使用它的 NAT 网关或实例删除后才释放地址。
- **CloudFormation：** Steward 读取栈资源和处理后的模板，识别归属与 `DeletionPolicy`。`Retain` 或 `RetainExceptOnCreate` 资源按保留关系处理；启用终止保护的栈会阻止删除。

## 审查清理影响

清理需要 `cloudformation:DeleteResource`、`cloudformation:GetResourceRequestStatus` 以及目标资源类型的删除与查询权限。产品 API 类型使用各自的删除 Action，例如 `ec2:DeregisterImage`、`ec2:DeleteSnapshot`、`es:DeleteDomain`、`rds:DeleteDBCluster`、`dms:DeleteReplicationInstance`、`fsx:DeleteFileSystem`、`storagegateway:DeleteGateway`、`route53domains:DeleteDomain`、`drs:DeleteSourceServer` 与 `mobiletargeting:DeleteSmsTemplate`。

清理计划会列出每次删除移除和保留的资源：

- **保留挂载资源：** 若要保留会随实例删除的卷或网卡，在计划中将其设为保留。终止实例前，Steward 将 `DeleteOnTermination` 设为 false 并回读确认；终止后确认保留资源仍然存在、应删除资源已经消失。这需要 `ec2:ModifyInstanceAttribute` 与 `ec2:ModifyNetworkInterfaceAttribute`。计划审查后挂载关系发生变化时，执行会停止。
- **删除保护：** EC2 实例、RDS 与 Aurora、DynamoDB、EKS 集群、负载均衡器、日志组、Neptune、网络防火墙、Aurora DSQL、CloudTrail 事件数据存储、Auto Scaling 组、DocumentDB 集群与 AMI 的删除保护会在删除前关闭并回读。这需要 `cloudformation:UpdateResource` 以及产品的修改权限，例如 `rds:ModifyDBCluster` 或 `ec2:DisableImageDeregistrationProtection`。
- **前置条件：** 互联网网关与虚拟私有网关会先从 VPC 分离。DocumentDB 集群不能有成员实例；弹性灾难恢复源服务器必须已断开复制。AWS 托管的 KMS 密钥不能删除；Backup 备份库必须没有恢复点，且未处于合规模式锁定状态。这些检查需要 `kms:DescribeKey` 与 `backup:DescribeBackupVault`。
- **服务默认行为：** DocumentDB 集群删除时不创建最终快照；FSx 文件系统遵循各类型默认的最终备份行为；KMS 密钥进入计划删除等待期，已处于待删除状态的密钥视为已删除；删除 Route 53 注册域名不可撤销，且只支持部分顶级域名。

非空存储桶和仓库、其他依赖关系以及状态变化仍可能导致操作失败。Steward 会等待异步操作并回读结果；不要把请求已接受当作资源已经删除。执行前阅读[清理资源](./cleanup.md)。

## 与其他云的差异

部分其他云上的资源在 AWS 中没有对应物。Steward 不会用无关资源代替它们：

| 资源 | AWS 的情况 |
| --- | --- |
| 带宽包 | 互联网、跨地域和 Global Accelerator 流量按 GB 计费，没有带宽包资源。 |
| 跨地域 QoS 与流量标记策略 | 中转网关与 Cloud WAN 没有 QoS 队列或 DSCP 标记策略资源。 |
| 路由映射 | 中转网关没有路由映射对象；Cloud WAN 的路由策略属于核心网络策略文档。 |
| 加速器挂载 | Amazon Elastic Inference 已于 2024 年 4 月 15 日停止服务。 |
| 专属块存储集群与云手机 | AWS 没有对应服务。 |
| 多集群舰队控制面 | EKS 没有托管的舰队控制器资源。 |

## 常见问题

| 现象 | 处理方法 |
| --- | --- |
| `ExpiredToken` 或身份验证失败 | 检查密钥配对、Session Token、到期时间和账号分区；替换完整凭证。 |
| 缺少某个地域 | 检查该地域是否已启用，以及 `ec2:DescribeRegions` 权限。 |
| Resource Explorer 返回未授权或没有资源 | 检查调用地域的默认视图、视图过滤条件、Search 权限和索引状态。 |
| Cloud Control 返回 `AccessDenied` | 同时检查 Cloud Control Action 与具体资源处理器所需的产品权限。 |
| 父资源成功但子类型失败 | 检查子类型的列举权限；组织单元需要使用组织管理账号。 |
| 某个类型在该地域不可用 | 检查 AWS 对该类型和地域的支持，缩小到实际使用的扫描类型。 |
| 实例清理因挂载不一致而停止 | 重新扫描并重新审查计划；卷或网卡在审查后发生了变化。 |
| 栈或资源删除失败 | 检查终止保护、保留策略、云端依赖与操作错误详情；不要反复直接提交删除。 |

下一步：[扫描资源](./scans.md) · [查询资源](./resources.md) · [清理资源](./cleanup.md)
