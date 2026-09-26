---
title: "Amazon Web Services（AWS）"
description: "连接 AWS 账号，配置盘点与清理所需的 IAM 权限，完成第一次扫描，并查看资源覆盖范围、清理保护与限制。"
navTitle: "AWS"
---

本页帮助你把一个 AWS 账号接入 Steward、配置所需的 IAM 权限，并了解 Steward 能发现和清理哪些资源。

Steward 使用连接中的凭证读取一个 AWS 账号。每个已支持的资源类型都有一个权威数据来源：具备列举、读取和删除处理器的类型使用 Cloud Control API；Cloud Control 支持不完整的类型使用该产品自身的 API。Resource Explorer 补充其他资源的广泛索引，但不会移除权威来源已发现的资源。

## 准备凭证

打开用户菜单 → **设置** → **云连接** → **添加云连接**，选择 **AWS**，再选择凭证类型：

| 凭证类型 | 需要填写 | 适用场景 |
| --- | --- | --- |
| **AWS Access Key** | Access Key ID、Secret Access Key | 专用 IAM 身份的访问密钥。 |
| **AWS 会话凭证** | Access Key ID、Secret Access Key、Session Token、到期时间 | 从已授权会话获得的一整套临时凭证。 |
| **IAM Identity Center** | 起始 URL 和区域；登录后再选择账号和角色 | [浏览器登录](./connections.md#browser)，自动续期。 |
| **OIDC 工作负载身份** | 角色 ARN；可选的写入角色 ARN | 服务端已配置 [OIDC](./oidc.md) 时使用，无需保存长期密钥即可换取临时凭证。 |

- Steward 只使用你填写的凭证，不会读取本机 AWS profile、SSO 会话或实例角色，也不会通过 AssumeRole 自动刷新凭证。
- 会话凭证过期后，使用 **替换凭证** 更新。新凭证必须属于原来的云身份。
- 目前仅支持 AWS 商业分区。中国区和 GovCloud 的身份分区与端点尚未适配，不要按普通商业区连接。

**IAM Identity Center 登录** 由 Steward 自行发起授权，而不是读取 AWS CLI 缓存的会话：Steward 向你的目录注册一个公共客户端，引导你在浏览器中登录，再把结果换成所选角色的临时凭证，并在到期时自动刷新。连接能读到的范围就是该角色的权限。

- 客户端注册约 90 天后过期，届时连接会提示重新授权。
- 浏览器与 Steward 服务器必须在同一台机器上。

<span id="configure-discovery-permissions"></span>

## 配置权限

验证连接时，Steward 调用 STS `GetCallerIdentity` 识别账号。身份有效不代表能读取所有资源：请按下表授予读取权限，再通过扫描确认。

### 盘点所需的读取权限

| 用途 | IAM Action |
| --- | --- |
| 地域与网络选择 | `ec2:DescribeRegions`、`ec2:DescribeVpcs`、`ec2:DescribeSubnets` |
| Cloud Control 盘点与详情 | `cloudformation:ListResources`、`cloudformation:GetResource`，以及各资源类型处理器要求的产品读取权限 |
| 挂载与成员关系 | `ec2:DescribeInstances`、`ec2:DescribeNetworkInterfaces`、`ec2:DescribeVolumes`、`autoscaling:DescribeAutoScalingGroups`、`eks:DescribeNodegroup`、`config:DescribeConfigRules`、`config:DescribeConformancePackCompliance`、`secretsmanager:DescribeSecret` |
| 产品 API 盘点 | `ec2:DescribeImages`、`ec2:DescribeSnapshots`、`es:ListDomainNames`、`es:DescribeDomains`、`rds:DescribeDBClusters`、`rds:DescribeDBInstances`（DocumentDB）、`dms:DescribeReplicationInstances`、`fsx:DescribeFileSystems`、`storagegateway:ListGateways`、`storagegateway:DescribeGatewayInformation`、`route53domains:ListDomains`、`drs:DescribeSourceServers`、`mobiletargeting:ListTemplates`、`mobiletargeting:GetSmsTemplate`、`ec2:DescribeClientVpnEndpoints`、`ec2:DescribeClientVpnTargetNetworks`、`ec2:DescribeClientVpnAuthorizationRules`、`ec2:DescribeClientVpnRoutes`、`elasticmapreduce:ListClusters`、`elasticmapreduce:DescribeCluster`、`workspaces:DescribeWorkspaceDirectories`、`wafv2:ListWebACLs`、`wafv2:GetWebACL`、`wafv2:ListResourcesForWebACL`、`wafv2:GetWebACLForResource`、`acm-pca:ListCertificateAuthorities`、`acm-pca:DescribeCertificateAuthority`、`bedrock:ListProvisionedModelThroughputs`、`bedrock:GetProvisionedModelThroughput`、`codebuild:ListProjects`、`codebuild:BatchGetProjects`、`cognito-idp:ListUserPools`、`cognito-idp:DescribeUserPool`、`cognito-idp:DescribeUserPoolDomain` |
| 组织树 | `organizations:ListRoots`、`organizations:ListOrganizationalUnitsForParent` |
| Resource Explorer 索引 | `resource-explorer-2:Search`（Steward 调用的 `ListResources` 使用该权限） |
| CloudFormation 栈与归属 | `cloudformation:DescribeStacks`、`cloudformation:ListStackResources`、`cloudformation:GetTemplate` |

- **Cloud Control 需要两层权限。** 它的 Action 使用 `cloudformation:` 前缀，但这些通用 Action 不能替代 EC2、S3、RDS 等产品自身的权限。请按 AWS 的[资源处理器权限说明](https://docs.aws.amazon.com/cloudcontrolapi/latest/userguide/resource-operations.html)逐类核对。
- **MSK 主题使用 Kafka 数据面权限。** 列举和删除主题会以 `kafka-cluster:Connect`、`kafka-cluster:DescribeTopic` 和 `kafka-cluster:DeleteTopic` 访问集群，因此集群必须启用 IAM 访问控制，否则其主题扫描项会失败。
- **只授予实际使用的服务。** 某个类型被拒绝，只会让它自己的扫描项失败，不影响其他类型。
- **Resource Explorer** 必须能在被查询地域返回资源。Steward 使用该地域的默认视图，视图的过滤条件会限制可见范围。参阅 [Resource Explorer 设置](https://docs.aws.amazon.com/resource-explorer/latest/userguide/getting-started-setting-up.html)和 [ListResources 权限说明](https://docs.aws.amazon.com/resource-explorer/latest/apireference/API_ListResources.html)。

### 清理所需的权限

读取权限不包含清理权限。只为准备删除的类型补充以下权限：

| 用途 | IAM Action |
| --- | --- |
| 通过 Cloud Control 删除 | `cloudformation:DeleteResource`、`cloudformation:GetResourceRequestStatus`，以及目标类型的产品删除和读取权限 |
| 通过产品 API 删除 | 该类型自身的删除 Action，例如 `ec2:DeregisterImage`、`ec2:DeleteSnapshot`、`es:DeleteDomain`、`rds:DeleteDBCluster`、`dms:DeleteReplicationInstance`、`fsx:DeleteFileSystem`、`storagegateway:DeleteGateway`、`route53domains:DeleteDomain`、`drs:DeleteSourceServer`、`mobiletargeting:DeleteSmsTemplate`、`ec2:DeleteClientVpnEndpoint`、`ec2:DisassociateClientVpnTargetNetwork`、`ec2:RevokeClientVpnIngress`、`ec2:DeleteClientVpnRoute`、`elasticmapreduce:TerminateJobFlows`、`workspaces:DeregisterWorkspaceDirectory`、`wafv2:DisassociateWebACL`（以及受保护服务自身的权限，例如 `elasticloadbalancing:SetWebACL`）、`acm-pca:DeleteCertificateAuthority`、`bedrock:DeleteProvisionedModelThroughput`、`codebuild:DeleteProject`、`cognito-idp:DeleteUserPoolDomain` |
| 终止实例时保留卷或网卡 | `ec2:ModifyInstanceAttribute`、`ec2:ModifyNetworkInterfaceAttribute` |
| 关闭删除保护 | `cloudformation:UpdateResource`，以及产品的修改权限，例如 `rds:ModifyDBCluster`、`ec2:DisableImageDeregistrationProtection` `elasticmapreduce:SetTerminationProtection` 或 `acm-pca:UpdateCertificateAuthority` |
| KMS 密钥、Backup 备份库、S3 存储桶与 Secrets Manager 密钥的删除前检查 | `kms:DescribeKey`、`backup:DescribeBackupVault`、`s3:ListBucketVersions`、`secretsmanager:DescribeSecret` |

## 完成第一次扫描

先从一个有已知资源的地域开始，确认结果正确后再扩大范围。

1. 添加连接（示例名称：「AWS · 测试环境」）。Steward 会在保存前验证凭证；核对显示的账号 ID 是否符合预期。
2. 在该连接上打开 **管理地域** → **从云 API 刷新**。Steward 通过 `ec2:DescribeRegions` 列出账号中已启用的地域；需要其他地域时，先在 AWS 中启用，再刷新一次。
3. 进入 **扫描** → **开始扫描**。选择 **选择部分地域** 并勾选一个有已知资源的地域（例如 `us-east-1`），或选择 **指定 VPC / vSwitch** 并选中一个已知的 VPC 或子网。需要 IAM 等[全局资源](#global-services)时，加选 **全局**。
4. 打开这次扫描，检查失败项。补齐权限后点击 **重试**。
5. 在 **资源** 中搜索一个已知的实例、存储桶或资源栈，核对账号、地域和资源 ID。

**完成标志：** 预期资源出现在正确的账号和地域下，扫描中没有未处理的权限或类型错误。只看资源总数不能说明扫描已经完整。接下来可以[查看资源关系](./topology.md)或扩大扫描范围。

需要跨地域、跨连接核对覆盖记录时，请跟随 [AWS 云资源盘点教程](./tutorials/aws-resource-inventory.md)。

## 资源覆盖范围

| 类别 | 资源类型 |
| --- | --- |
| 计算 | EC2 实例、AMI、启动模板、专用主机、容量预留与容量预留队列、Auto Scaling 组、Lightsail 实例、密钥对、Instance Connect 终端节点、置放群组、WorkSpaces 云桌面与 WorkSpaces 目录、Elastic Beanstalk 应用程序与环境 |
| 容器与无服务器 | EKS 集群、托管节点组、Fargate 配置文件与插件；ECS 集群、服务与任务定义；ECR 仓库与拉取缓存规则；Lambda；App Runner |
| 网络 | VPC、子网、CIDR 块、路由表、安全组、网络 ACL、网卡、弹性 IP，互联网、仅出口、NAT 与虚拟私有网关，客户网关、VPN 连接、Client VPN 端点及其目标网络、授权规则与路由、托管前缀列表、IPAM 及其范围与池、VPC 对等连接、终端节点与终端节点服务、流日志、DHCP 选项集 |
| 中转与混合网络 | 中转网关及其路由表、VPC/对等/Connect 挂载、组播域及其关联、成员与源；Direct Connect 连接、链路聚合组、网关、网关关联与虚拟接口；Cloud WAN 全球网络与核心网络；Global Accelerator 加速器、侦听器与终端节点组 |
| 负载均衡、边缘与 DNS | 应用/网络/网关负载均衡器及其侦听器、目标组与信任存储、传统负载均衡器、CloudFront 分配、WAF Web ACL 及其关联、Shield Advanced 防护、Route 53 托管区域、运行状况检查、Resolver 规则与注册域名、API Gateway API、自定义域名、使用计划、使用计划密钥与 API 密钥、AppSync GraphQL API、Amplify 应用、VPC Lattice |
| 存储与备份 | S3 存储桶、EBS 卷与快照、Data Lifecycle Manager 策略、EFS 文件系统、挂载目标与接入点、FSx 文件系统、Storage Gateway、Transfer Family 服务器、AWS Backup 备份库、备份计划与资源分配、弹性灾难恢复源服务器 |
| 数据库与分析 | RDS 与 Aurora、RDS 代理、Aurora DSQL、DynamoDB、DocumentDB 与 DocumentDB 弹性集群、Neptune 与 Neptune Analytics、Keyspaces、ElastiCache、MemoryDB、Timestream、OpenSearch Service 与 Serverless、Redshift 与 Redshift Serverless、Glue 数据库、Athena 工作组、EMR 集群、EMR Serverless、Kinesis、Data Firehose 流、Apache Flink 托管服务、DMS、MSK 集群与主题、Amazon MQ、DataZone、QuickSight 控制面板与数据集 |
| 消息与应用 | SQS、SNS 主题与订阅、EventBridge 事件总线与规则、EventBridge Scheduler 计划与计划组、Step Functions、Apache Airflow 托管工作流、CodePipeline、CodeBuild 项目、Cloud Map 命名空间与服务、AppRegistry 应用程序、Pinpoint 短信模板、IVS 频道与实时舞台、Kendra 索引、Bedrock 智能体、知识库、数据源、防护栏与预置吞吐量、SageMaker 终端节点、终端节点配置、模型与 HyperPod 集群、AWS PCS 与 Batch 计算环境 |
| 身份、安全与治理 | IAM 用户、用户组、角色、实例配置文件与托管策略，IAM Identity Center 实例与组，Organizations 组织、组织单元与成员账号，KMS 密钥与别名、ACM 证书、ACM 私有证书颁发机构、Secrets Manager 密钥、Systems Manager 参数、Cognito 用户池、用户池域与身份池、GuardDuty、Security Hub、Macie、网络防火墙、IAM 访问分析器、CloudTrail 跟踪与事件数据存储、AWS Config 规则、修正配置、合规包与聚合器 |
| 监控与编排 | CloudWatch 告警、控制面板与日志组，资源组，Synthetics 金丝雀、X-Ray 组、可观测性访问管理器、Managed Grafana 与 Prometheus、CloudFormation 资源栈与 StackSet |

### 子资源

部分类型通过父资源列举：

| 子类型 | 列举方式 |
| --- | --- |
| EKS 节点组、插件、Fargate 配置文件 | 按集群 |
| 负载均衡侦听器 | 按负载均衡器 |
| EFS 挂载目标 | 按文件系统 |
| 组播关联、成员与源 | 按组播域 |
| IAM Identity Center 组 | 按实例 |
| API Gateway 使用计划密钥 | 按使用计划 |
| MSK 主题 | 按 MSK 预置集群 |
| Bedrock 知识库数据源 | 按知识库 |
| Cognito 用户池域 | 按用户池 |
| Client VPN 目标网络、授权规则与路由 | 按 Client VPN 端点 |
| WAF Web ACL 关联 | 按每个地域级 Web ACL，逐一查询各类受保护资源 |
| 组织单元 | 按完整组织树 |

父资源读取失败时，子类型的扫描项会标记为失败，而不是返回空列表。

<span id="global-services"></span>

### 全局服务

全局服务从各自的主地域读取。扫描这些资源时，扫描范围需要包含 **全局**。

| 主地域 | 服务 |
| --- | --- |
| `us-east-1` | IAM、CloudFront、Route 53、Organizations、Shield Advanced |
| `us-west-2` | Global Accelerator、Cloud WAN |

CloudFront 范围的 WAF Web ACL 出现在 `us-east-1` 地域的扫描结果中。

## 关系与归属

Steward 根据资源模型（例如资源所属的 VPC、子网与安全组）和从产品 API 读取的挂载信息建立关系。这些关系决定清理任务会删除、保留或阻断哪些资源：

| 关系 | 对清理的影响 |
| --- | --- |
| 实例存储与网卡 | 设置了 `DeleteOnTermination` 的 EBS 卷或网卡由实例管理，终止实例会将其删除；其他挂载的卷在实例删除后再删除。 |
| Auto Scaling 与 EKS | Auto Scaling 组中的实例、EKS 托管节点组下的 Auto Scaling 组由各自的控制器管理，只能通过控制器清理。 |
| 服务托管网卡 | NAT 网关、VPC 终端节点、负载均衡器、EFS 挂载目标或 Lambda 创建的网卡属于对应服务，不能直接删除。 |
| 弹性 IP | 只有在使用它的 NAT 网关或实例删除后，才会释放地址。 |
| 必须先删的子资源 | Client VPN 端点要在解除全部目标网络关联之后删除，Config 规则要在删除其修正配置之后删除，这两类子资源会自动加入任务。WorkSpaces 目录要在其云桌面全部终止后才能注销，信任存储要在使用它的侦听器删除后才能删除；Steward 不会替你选中这些资源，未选中时任务会被阻断。 |
| 随父资源删除 | 删除 MSK 集群会删除其主题，删除 SNS 主题会删除其订阅，删除 Client VPN 端点会删除其授权规则与手动添加的路由，删除使用计划会删除其密钥。任务会把它们列为随父资源删除。 |
| 其他前置条件 | IPAM 的私有范围在 IPAM 之前删除，并自动加入任务；IPAM 池、知识库数据源和 Elastic Beanstalk 环境会阻断其所属范围、池、知识库或应用程序的删除，直到你选中它们；Cognito 用户池域在用户池之前删除。删除计划组会同时删除其中的计划。引用托管前缀列表的安全组、置放群组中的实例，会先于前缀列表或置放群组删除。 |
| 只能经父资源删除 | 合规包部署的规则、Elastic Beanstalk 环境的 CloudFormation 栈、Client VPN 子网关联自动添加的路由、EMR 集群的实例，只能通过对应的合规包、关联或集群删除。 |
| CloudFormation | Steward 读取栈资源和处理后的模板，识别归属与 `DeletionPolicy`。`Retain` 或 `RetainExceptOnCreate` 资源按保留资源处理；启用终止保护的栈会阻止删除。 |

## 清理行为与保护

清理任务会列出每次删除移除哪些资源、保留哪些资源。Steward 会等待异步操作完成并回读结果；云端接受了请求不算删除成功。执行前请阅读[清理资源](./cleanup.md)。

### 保留挂载的卷和网卡

若要保留会随实例一起删除的卷或网卡，在审核清理任务时将其设为保留。终止实例前，Steward 把 `DeleteOnTermination` 改为 false 并回读确认；终止后再确认保留资源仍然存在、应删除的资源已经消失。审核之后挂载关系如有变化，执行会停止。

### 删除保护

以下类型的删除保护会在删除前由 Steward 关闭并回读确认：

EC2 实例、AMI、Auto Scaling 组、EKS 集群、负载均衡器、RDS 与 Aurora、DocumentDB 集群、Neptune、Aurora DSQL、DynamoDB、CloudWatch 日志组、网络防火墙、CloudTrail 事件数据存储、EMR 集群（终止保护）、Cognito 用户池。处于启用状态的 ACM 私有 CA 会先被停用并回读确认，再删除。

### 前置条件与阻断

| 资源 | 条件 |
| --- | --- |
| 互联网网关与虚拟私有网关 | 先从 VPC 分离。 |
| DocumentDB 集群 | 不能有成员实例。 |
| AWS Config 规则 | 由其他服务创建的规则（合规包、组织规则、Security Hub 等）以及组织合规包下发到成员账号的合规包，不能直接删除。 |
| MSK 主题 | 以 `__` 开头的内部主题（如 `__consumer_offsets`）属于 Kafka 和 MSK，不能删除。 |
| Secrets Manager 密钥 | 由其他服务管理的密钥（如 RDS 主用户密钥）以及主密钥位于其他地域的副本不能直接删除。 |
| Bedrock 预置吞吐量 | 有承诺期限的预置吞吐量在期限结束前不能删除。 |
| AWS 托管资源 | AWS 托管的前缀列表、默认 IPAM 范围和默认计划组不能删除。 |
| 资源组 | 名称以 `AWS` 开头的组由 AWS 服务创建，不能删除。 |
| WAF Web ACL 关联 | 由 Firewall Manager 管理的 Web ACL，其关联不能解除。CloudFront 分配不在列举范围内：它的 Web ACL 是分配的一项设置。 |
| 弹性灾难恢复源服务器 | 必须已断开复制。 |
| KMS 密钥 | AWS 托管密钥不能删除。已扫描资源通过密钥 ID、密钥 ARN、别名或别名 ARN 引用的客户托管密钥，只会在该资源之后删除；该资源不在任务中时，任务被阻断。若密钥策略只把无条件的密钥管理权授予已扫描的 IAM 用户或角色、且未委派给账号根用户，则在密钥仍存在时，任务不能删除全部这些主体。 |
| Backup 备份库 | 必须没有恢复点，且未处于合规模式锁定。 |
| S3 存储桶 | 不能有任何对象版本或删除标记（即使版本控制已暂停）。Steward 不会清空存储桶。扫描会把非空存储桶标记为受保护，使其在任务中可见；删除前还会再检查一次。清空存储桶后请重新扫描。 |

非空仓库、其他依赖关系以及资源状态变化，仍可能导致操作失败。

### 服务默认行为

| 资源 | 删除时的行为 |
| --- | --- |
| DocumentDB 集群 | 不创建最终快照。 |
| EMR 集群 | 执行终止；未完成的步骤会被取消，实例存储中的数据会丢失，已写入 S3 的日志保留。只列举尚未终止的集群。 |
| WorkSpaces 目录 | 从 WorkSpaces 注销；Directory Service 目录本身保留，不再被 WorkSpaces 使用后按 Directory Service 价格计费。 |
| AWS Config 规则 | 连同评估结果一起删除。 |
| ACM 私有 CA | 删除后保留默认 30 天的恢复期；该 CA 签发的证书将无法再通过验证。处于恢复期的 CA 视为已删除。 |
| Secrets Manager 密钥 | 按服务的恢复期计划删除；处于待删除状态的密钥视为已删除。 |
| Bedrock 知识库数据源 | 每个数据源的数据删除策略决定是否从向量存储中删除其向量。 |
| Elastic Beanstalk 环境 | 连同其 CloudFormation 栈创建的资源一起终止；S3 中的应用程序版本保留。 |
| FSx 文件系统 | 遵循各文件系统类型默认的最终备份行为。 |
| KMS 密钥 | 进入计划删除等待期；已处于待删除状态的密钥视为已删除。 |
| Route 53 注册域名 | 删除不可撤销，且只支持部分顶级域名。 |

## 与其他云的差异

部分其他云上的资源在 AWS 中没有对应物。Steward 不会用无关资源代替它们：

| 资源 | AWS 的情况 |
| --- | --- |
| 带宽包 | 互联网、跨地域和 Global Accelerator 流量按 GB 计费，没有带宽包资源。 |
| 跨地域 QoS 与流量标记策略 | 中转网关与 Cloud WAN 没有 QoS 队列或 DSCP 标记策略资源。 |
| 路由映射 | 中转网关没有路由映射对象；Cloud WAN 的路由策略属于核心网络策略。 |
| 加速器挂载 | Amazon Elastic Inference 已于 2024 年 4 月 15 日停止服务。 |
| 专属块存储集群与云手机 | AWS 没有对应服务。 |
| 多集群舰队控制面 | EKS 没有托管的舰队控制器资源。 |

## 常见问题

| 现象 | 处理方法 |
| --- | --- |
| `ExpiredToken` 或验证失败 | 检查密钥是否配对、Session Token、到期时间和账号分区；替换整套凭证。 |
| 缺少某个地域 | 检查该地域是否已在账号中启用，以及 `ec2:DescribeRegions` 权限。 |
| Resource Explorer 返回未授权或没有资源 | 检查该地域的默认视图及其过滤条件、Search 权限和索引状态。 |
| Cloud Control 返回 `AccessDenied` | 同时检查 Cloud Control Action 与资源处理器所需的产品权限。 |
| 父资源成功但子类型失败 | 检查子类型的列举权限；组织单元需要连接组织的管理账号。 |
| 某个类型在该地域不可用 | 确认 AWS 在该地域支持该类型，只扫描实际使用的类型。 |
| 存储桶显示为受保护 | 桶中仍有对象版本或删除标记。在 Steward 之外清空后重新扫描。 |
| 实例清理因挂载不一致而停止 | 卷或网卡在审核后发生了变化。重新扫描并重新审核任务。 |
| 栈或资源删除失败 | 先检查终止保护、保留策略、依赖关系和操作错误详情，再重试；不要反复直接提交删除。 |

## 下一步

- [扫描资源](./scans.md)
- [查询资源](./resources.md)
- [清理资源](./cleanup.md)
