# Cloud Steward PRD

## 1. 文档信息

- 产品名称：Cloud Steward
- 文档类型：Product Requirements Document
- 当前版本：v0.1
- 状态：草案
- 更新日期：2026-07-02
- 首期云厂商：阿里云
- 后续云厂商：AWS、Google Cloud、Azure

## 2. 产品概述

Cloud Steward 是一个开源的拓扑感知云资源治理与清理平台。它帮助工程团队发现云上资源、理解资源之间的依赖关系、识别闲置或孤儿资源，并在执行清理前生成可审计、可审批、可回滚思路清晰的清理计划。

首期产品只覆盖阿里云，重点解决单账号或少量账号下的资源发现、拓扑建模、候选识别、清理计划和基础执行问题。AWS、Google Cloud、Azure、Kubernetes 和 IaC 集成作为后续扩展能力。

产品操作入口统一放在 Web UI。CLI 只负责管理本地或自托管 server 的启动、停止和状态查看，不承载扫描、候选识别、计划生成或执行等业务操作。

一句话定位：

> 发现云资源拓扑，解释清理影响，安全回收闲置基础设施。

## 3. 背景与问题

云资源治理常见问题不是“能不能删”，而是“删之前是否知道影响范围”。开发、测试、预发和临时环境会长期积累云盘、EIP、快照、负载均衡、安全组等残留资源。团队通常缺少一套能同时回答资源清单、依赖关系、归属信息、风险判断和审计证据的工具。

现有工具常见不足：

- 偏资源列表，缺少拓扑和依赖解释。
- 偏一键清理，缺少计划、审批和审计。
- 偏成本报表，不能直接落到可执行清理动作。
- 偏安全合规，不能覆盖日常工程清理工作流。
- 多云范围容易过大，早期难以做出可用闭环。

Cloud Steward 的首期目标是先在阿里云场景做出完整闭环，再通过 connector 和规则扩展到 AWS、Google Cloud、Azure 等云厂商。

## 4. 目标用户

### 4.1 主要用户

- 小团队平台工程师：希望定期清理开发、测试、预发环境中的残留资源。
- DevOps / SRE：需要理解资源依赖关系，降低清理风险。
- FinOps 工程师：需要识别可回收资源并量化节省金额。
- 云治理负责人：需要建立资源归属、审批和审计机制。

### 4.2 次要用户

- 个人开发者：希望清理个人阿里云账号中的闲置资源。
- 安全合规人员：关注资源暴露、权限分离和操作审计。
- 企业平台团队：后续需要多账号、多团队、多云治理能力。

## 5. 产品目标

### 5.1 MVP 目标

- 支持阿里云资源扫描，形成标准化资源清单。
- 支持基础资源拓扑关系展示和查询。
- 支持识别常见闲置、孤儿和过期资源。
- 支持生成默认 dry-run 清理计划，解释清理原因、顺序、风险和预计节省。
- 支持手动审批后执行低风险清理动作；MVP 内置 live 子集仅包含 Alibaba Cloud tag 动作，默认关闭。
- 支持输出审计日志和节省报告。
- 提供 Go server、API 和基础 Web UI。
- 提供最小 CLI，仅支持 `server start`、`server stop`、`server status`。
- MVP 覆盖扫描、拓扑、候选、默认 dry-run 计划、手动审批、执行审计和基础报告闭环。

### 5.2 非目标

- 不做完整 CSPM。
- 不做完整 FinOps 平台。
- 不替代 Terraform、Pulumi、CloudFormation 等 IaC 工具。
- 不做云厂商控制台的完整替代品。
- 不把“一键删除所有资源”作为主要卖点。
- MVP 不支持 AWS、Google Cloud、Azure、Kubernetes 和 Terraform state。

## 6. 成功指标

### 6.1 产品指标

- 已发现资源数量。
- 已识别清理候选数量。
- 清理候选误报率。
- dry-run 计划生成成功率。
- 计划审批通过率。
- 计划执行成功率。
- 月度估算节省金额。
- 平均清理周期。

### 6.2 开源指标

- GitHub stars。
- CLI 安装量。
- 活跃 contributor。
- connector 数量。
- 规则包数量。

### 6.3 企业化指标

- 接入账号数量。
- 月活用户数。
- 审计导出次数。
- 自动化清理比例。
- 平均审批时长。
- 成本节省趋势。

## 7. MVP 范围

### 7.1 首期支持范围

首期只支持阿里云，并以单账号或少量账号为目标场景。

资源类型优先级：

1. ECS 实例。
2. 云盘。
3. EIP。
4. 安全组。
5. 快照。
6. VPC。
7. vSwitch。
8. NAT Gateway。
9. 负载均衡。
10. 资源组。

### 7.2 首期清理规则

- 未挂载云盘。
- 未使用 EIP。
- 停止超过 N 天的 ECS 实例。
- 老旧快照。
- 老旧自定义镜像。
- 空资源组。
- 无引用安全组。
- 无流量负载均衡。
- 无流量 NAT Gateway。
- TTL 过期的临时环境。
- owner / team / cost-center 标签缺失且长期无人认领的资源。

### 7.3 后续扩展范围

- AWS connector。
- Google Cloud connector。
- Azure connector。
- Kubernetes connector。
- Terraform state connector。
- Pulumi state connector。
- CloudFormation stack connector。
- Helm release connector。
- CMDB / Service Catalog 集成。
- 私有云 / 混合云 connector。

## 8. 用户场景

### 8.1 场景一：平台工程师清理开发环境

平台工程师希望清理 `env=dev` 且 TTL 过期的阿里云资源。系统需要展示候选资源、资源依赖、风险等级和预计节省金额，并生成 dry-run 计划。工程师确认后提交审批，审批通过后执行清理。

验收标准：

- 用户可以按 tag、region、资源类型和创建时间筛选资源。
- 系统可以展示每个候选资源的命中规则和证据。
- 系统可以生成包含删除顺序和阻塞原因的计划。
- 执行前系统会重新检查资源状态。

### 8.2 场景二：SRE 排查孤儿资源

SRE 发现某个 VPC 成本异常，希望查看该 VPC 下所有资源和依赖关系。系统需要提供资源图谱、依赖证据和置信度，并标记可能孤儿化的资源。

验收标准：

- 用户可以按 VPC 查看资源子图。
- 图谱中能区分确定关系和推断关系。
- 用户可以从图谱中选择资源生成清理计划。

### 8.3 场景三：FinOps 输出节省报告

FinOps 工程师希望查看本月已清理资源、预计节省金额和失败资源。系统需要按 team、application、environment 聚合，并支持导出审计证据。

验收标准：

- 用户可以查看已清理资源列表。
- 用户可以查看计划级和资源级节省估算。
- 用户可以导出审计日志和执行证据。

### 8.4 场景四：个人开发者本地启动服务并在 Web 中扫描账号

个人开发者希望在本地启动 Cloud Steward server，然后通过 Web UI 扫描自己的阿里云账号，查看资源列表和资源详情。CLI 只用于启动、停止和查看 server 状态。

验收标准：

- 用户可以通过 CLI 启动本地 server。
- 用户可以通过 CLI 查看 server 状态。
- 用户可以通过 CLI 停止本地 server。
- 用户可以在 Web UI 中配置阿里云账号和扫描 region。
- 用户可以在 Web UI 中触发扫描并查看结果。
- 默认不会执行破坏性动作；启用 live executor 时首期也只执行 tag 动作。

## 9. 产品流程

### 9.1 资源发现流程

1. 用户在 Web UI 中配置阿里云账号凭证和扫描范围。
2. 用户在 Web UI 中触发扫描。
3. 系统调用阿里云 API 获取资源数据。
4. 系统将原始数据标准化为统一资源模型。
5. 系统保存资源快照和扫描时间。
6. 系统构建资源之间的关系边。
7. 用户在 Web UI 中查看资源和拓扑。

### 9.2 候选识别流程

1. 系统基于内置规则扫描资源。
2. 系统为命中的资源生成候选项。
3. 每个候选项包含原因、证据、风险等级、推荐动作和预计节省。
4. 用户可以在 Web UI 中接受、忽略或延后候选项。

### 9.3 清理计划流程

1. 用户在 Web UI 中选择候选资源或规则范围。
2. 系统扩展依赖关系，识别受影响资源。
3. 系统标记受保护资源和阻塞原因。
4. 系统生成 dry-run 计划。
5. 用户在 Web UI 中查看风险、顺序、节省和影响范围。
6. 需要审批时，计划进入待审批状态。
7. 审批通过后，用户在 Web UI 中显式触发执行。

### 9.4 执行流程

1. 系统在每一步执行前重新读取资源状态。
2. 系统校验保护规则和 blast radius。
3. 系统按计划依赖顺序执行动作。
4. 系统保存每一步执行证据和云厂商 request id。
5. 执行失败时，系统记录失败原因，并按策略停止或跳过。
6. 执行完成后，系统生成审计日志和节省报告。

## 10. 功能需求

### FR-001 阿里云账号接入

优先级：P0

系统必须支持用户接入阿里云账号，并配置扫描所需的只读权限。

验收标准：

- 支持配置 AccessKey 或等价凭证来源。
- 支持配置扫描 region。
- 支持最小只读权限说明。
- 凭证不可在日志中明文输出。

### FR-002 资源扫描

优先级：P0

系统必须支持扫描阿里云基础资源，并保存资源快照。

验收标准：

- 支持扫描 ECS、云盘、EIP、安全组、快照、VPC、vSwitch。
- 支持按账号和 region 触发扫描。
- 每次扫描生成独立快照。
- 每个资源保留原始云厂商字段。

### FR-003 资源标准化

优先级：P0

系统必须将阿里云资源转换为统一资源模型。

验收标准：

- 每个资源包含 provider、scope、region、type、native_id、name、tags、state、created_at、last_seen_at。
- 支持记录 owner、team、application、environment、cost center。
- 支持记录 protection status。
- 支持记录资源原始数据。

### FR-004 资源拓扑

优先级：P0

系统必须构建基础资源关系，并支持用户查看资源依赖。

验收标准：

- 支持 VPC、vSwitch、ECS、云盘、安全组、EIP 的基础关系。
- 每条关系包含来源、置信度和证据。
- 用户可以查看某个资源的一跳关系。
- 用户可以查看某个 VPC 下的资源子图。

### FR-005 候选识别

优先级：P0

系统必须基于内置规则识别清理候选资源。

验收标准：

- 支持未挂载云盘、未使用 EIP、停止 ECS、老旧快照、无引用安全组等规则。
- 每个候选项包含命中原因、证据、置信度、风险等级和推荐动作。
- 用户可以按规则、资源类型、team、region 和风险等级筛选候选项。
- 用户可以接受、忽略或延后候选项。

### FR-006 CleanupPlan 生成

优先级：P0

系统必须在执行任何破坏性动作前生成 CleanupPlan。

验收标准：

- 计划包含资源清单、动作、顺序、依赖、风险、阻塞原因和预计节省。
- 默认执行器下计划是 dry-run；显式启用 live executor 时计划标记为 live。
- 计划可以标记受保护资源。
- 计划可以解释每个资源为什么被选中。
- 计划可以导出为 JSON 或 Markdown。

### FR-007 安全护栏

优先级：P0

系统必须默认保护生产和高风险资源。

验收标准：

- 默认保护 `env=prod` 和 `environment=production`。
- 默认保护带 `cloud-steward:protect=true` 标签的资源。
- 默认保护关键身份、密钥、备份、审计和安全工具相关资源。
- 不确定依赖关系的高风险资源不得自动删除。
- 支持配置单次计划最大资源数、最大 region 数和最大高风险资源数。

### FR-008 审批

优先级：P1

系统应该支持手动审批清理计划。

验收标准：

- 计划支持 draft、pending_approval、approved、running、completed、failed、canceled 状态。
- 中高风险计划必须审批后才能执行。
- 审批记录包含审批人、审批时间、审批意见。
- 审批前计划内容不可被静默修改。

### FR-009 执行器

优先级：P1

系统应该支持执行低风险清理动作。

验收标准：

- 支持 tag、stop、detach、release、delete 等动作中的 MVP 子集；当前 live 子集只包含 tag。
- 执行动作必须幂等。
- 执行前必须重新读取资源状态。
- 执行器需要通过可替换接口接入，默认本地实现只记录 dry-run 结果；`alicloud-tag` 只添加 `cloud-steward:*` 标签并跳过破坏性动作。
- 执行结果必须记录成功、失败或跳过。
- 每一步保存云厂商 request id。

### FR-010 审计与报告

优先级：P1

系统应该支持审计日志和节省报告。

验收标准：

- 记录扫描、计划、审批和执行事件。
- 记录执行证据、错误详情和 request id。
- 支持按 team、application、environment 聚合节省金额。
- 支持导出审计日志。

### FR-011 CLI

优先级：P0

系统必须提供最小 CLI，只负责 server 生命周期管理。CLI 不提供扫描、候选查看、计划生成、审批或执行等业务命令。

验收标准：

- 支持 `steward server start`。
- 支持 `steward server stop`。
- 支持 `steward server status`。
- `server status` 能展示 server 是否运行、监听地址、版本和最近一次启动时间。
- CLI 不暴露 `scan`、`candidates`、`plan`、`apply`、`report` 等业务命令。
- CLI 输出应适合本地脚本和运维检查消费。

### FR-012 Web UI

优先级：P0

系统必须在 MVP 中提供基础 Web UI，覆盖扫描、图谱、候选、计划、审批和报告的核心闭环。

验收标准：

- 支持配置阿里云账号和扫描 region。
- 支持从 Web UI 触发扫描。
- 支持查看扫描任务列表、状态、开始时间、结束时间和失败原因。
- 支持查看扫描出的资源列表。
- 支持查看资源详情和原始字段摘要。
- 支持查看基础资源关系。
- 支持处理候选项并生成 CleanupPlan；默认执行器生成 dry-run CleanupPlan。
- 支持计划详情、手动审批、执行审计和节省报告。

### FR-013 API

优先级：P0

系统必须提供 API，供 Web UI 和后续自动化集成使用。MVP 阶段业务操作入口以 Web UI 为准，CLI 不直接调用业务 API。

验收标准：

- 支持创建扫描任务。
- 支持查询扫描任务状态。
- 支持查询资源列表和资源详情。
- 支持触发扫描分析、查询资源关系和候选项。
- 支持创建、查询、审批和执行 CleanupPlan；默认执行器为 dry-run。
- 支持导出 CleanupPlan JSON 和 Markdown。
- 支持创建计划时配置最大资源数、最大 region 数和最大高风险资源数。
- 支持查询审计事件和节省报告。
- 支持导出审计日志 JSON 和 CSV。
- API 返回结构化错误信息。

## 11. 权限与安全需求

### 11.1 权限分离

系统需要区分三类权限：

- reader：只扫描资源。
- planner：生成清理计划。
- executor：执行破坏性动作。

生产环境中，executor 不应默认开启。

### 11.2 默认保护

系统默认保护以下资源：

- `env=prod`。
- `environment=production`。
- 带 `cloud-steward:protect=true` 标签的资源。
- 关键身份资源。
- KMS、凭据、密钥相关资源。
- root DNS zone。
- backup vault。
- 监控、审计、安全工具资源。
- 删除保护开启的资源。
- 不确定依赖关系的高风险资源。

### 11.3 Blast Radius

每个计划必须支持以下限制：

- 最大资源数量。
- 最大月成本影响。
- 最大账号数量。
- 最大 region 数量。
- 最大高风险资源数量。
- 单次执行最大耗时。

## 12. 非功能需求

### 12.1 可用性

- CLI 应能在本地环境启动、停止和检查 server。
- Web UI 必须支持扫描、分析、候选处理、计划审批和报告闭环。
- API 应提供清晰的错误信息和请求追踪 ID。

### 12.2 可靠性

- 扫描失败不得影响已有快照。
- 执行器必须幂等。
- 执行中断后应支持继续或明确标记失败状态。
- 执行前必须重新校验资源当前状态。

### 12.3 可审计性

- 所有扫描、计划、审批和执行动作必须记录审计事件。
- 审计事件必须包含 actor、action、target、timestamp、result。
- 破坏性动作必须记录云厂商 request id。

### 12.4 可扩展性

- connector 需要预留多云扩展接口。
- 规则需要可配置和可扩展。
- 数据模型需要支持 future provider：aws、gcp、azure、k8s、custom。
- 对象存储首期支持 OSS 或 MinIO，后续支持 S3、GCS、Azure Blob。

## 13. 交互与界面需求

### 13.1 Scan Console

MVP 首个 Web 页面需要支持扫描闭环。

核心能力：

- 配置阿里云账号和 region。
- 触发扫描。
- 查看扫描任务列表和任务状态。
- 查看扫描失败原因。
- 查看扫描产出的资源列表。
- 查看单个资源详情和原始字段摘要。

### 13.2 Graph Explorer

用户需要通过图谱理解云资源拓扑。

核心能力：

- 按账号、region、VPC、resource group、application 过滤。
- 支持节点搜索。
- 支持查看依赖关系。
- 支持查看关系证据和置信度。
- 支持从图中选择资源生成计划。

### 13.3 Cleanup Inbox

用户需要集中处理系统识别出的清理候选。

核心能力：

- 按节省金额排序。
- 按风险排序。
- 按 owner / team 聚合。
- 按规则分类。
- 支持 accept、ignore、snooze。

### 13.4 Plan Builder

用户需要查看和确认清理计划。

核心能力：

- 展示资源清单。
- 展示删除顺序。
- 展示受保护资源。
- 展示阻塞原因。
- 展示预计节省。
- 展示风险摘要。

### 13.5 Approval Center

团队需要审批中高风险清理计划。

核心能力：

- 查看待审批计划。
- 查看影响范围。
- 评论和确认。
- 审批、拒绝、要求修改。
- 后续集成 Slack、Teams、飞书、钉钉、Jira、ServiceNow。

### 13.6 Audit & Savings

用户需要查看清理结果和价值证明。

核心能力：

- 展示已清理资源。
- 展示失败资源。
- 展示节省金额。
- 按 team / application / environment 聚合。
- 导出审计证据。

## 14. Web API 与 Server CLI 需求

### 14.1 Server CLI 命令

MVP 的 CLI 只管理 server 生命周期，不提供业务操作命令：

```bash
steward server start
steward server stop
steward server status
```

明确不做：

- `steward scan`
- `steward candidates`
- `steward plan`
- `steward apply`
- `steward report`

扫描、资源查看、候选处理、计划审批和执行都只在 Web UI 中操作。

### 14.2 Web API

MVP Web API 需要支持：

- `POST /api/scans`
- `GET /api/scans`
- `GET /api/scans/{id}`
- `POST /api/scans/{id}/reconcile`
- `GET /api/resources`
- `GET /api/resources/{id}`
- `GET /api/graph`
- `GET /api/candidates`
- `PATCH /api/candidates/{id}`
- `POST /api/plans`
- `GET /api/plans`
- `GET /api/plans/{id}`
- `GET /api/plans/{id}/export`
- `POST /api/plans/{id}/approve`
- `POST /api/plans/{id}/execute`
- `GET /api/audits`
- `GET /api/audits/export`
- `GET /api/reports/savings`

## 15. 技术栈与技术约束

当前技术栈拍板为 Go 后端 + React 前端。

### 15.1 后端

- 语言：Go。
- HTTP 服务：Go 标准库 + `chi`。
- CLI：`cobra`，只实现 `server start`、`server stop`、`server status`。
- 数据库：本地测试默认 SQLite；部署场景支持 MySQL。
- ORM：GORM。
- Migration：`goose`，使用显式 SQL migration 管理 schema 演进。
- 后台任务：首期使用数据库 `scan_jobs` 表 + worker 轮询；后续如任务编排复杂，再评估 Temporal。
- 阿里云接入：Alibaba Cloud Go SDK。
- 静态资源：Go `embed` 打包前端产物，支持单二进制自托管部署。

### 15.2 前端

- 框架：React + Vite + TypeScript。
- 组件：MVP 先使用轻量自建组件，后续可按需引入 shadcn/ui。
- 样式：Tailwind CSS。
- 路由：MVP 单页控制台暂不需要专用路由库。
- 请求与缓存：MVP 先使用 `fetch` 和组件状态。
- 表格：MVP 先使用原生 table。
- 表单：MVP 先使用受控表单，后续复杂化后再引入 React Hook Form + Zod。
- 图标：lucide-react。
- 图谱后续：React Flow。
- 图表后续：Recharts 或 ECharts。
- API 类型：MVP 先手写最小 TypeScript 类型，后续可输出 OpenAPI 并使用 `openapi-typescript` 生成类型。

### 15.3 存储与扩展约束

- 首期使用 SQLite 或 MySQL 存储标准化资源、资源关系、候选项、计划、执行记录和审计事件。
- 使用数据库 JSON 字段保存云厂商原始字段和规则证据。
- 图查询首期可以通过 edge table 和数据库 recursive CTE 实现。
- ORM 层使用 GORM，避免首期直接绑定手写 SQL 或代码生成式 DAO。
- 数据库抽象需要避免使用单一数据库私有特性；后续可按需增加 PostgreSQL 支持。
- 原始扫描包、执行日志和审计证据优先保存到 OSS 或 MinIO。
- 后续如资源规模或图查询复杂度提升，再引入图数据库或专用图索引。

## 16. 发布路线图

### Phase 0：项目骨架

- 仓库结构。
- Go server 框架。
- Server CLI 框架，只包含 `server start`、`server stop`、`server status`。
- Web API 框架。
- React + Vite + TypeScript + Tailwind CSS 前端框架。
- SQLite 本地测试 schema。
- MySQL 接入说明。
- 基础文档。

### Phase 1：阿里云扫描闭环

- Alibaba Cloud connector。
- ECS、云盘、EIP、安全组、快照、VPC、vSwitch 基础资源。
- 标准化资源模型。
- 资源快照。
- 基础资源列表 API。
- Web Scan Console：账号配置、扫描触发、扫描状态、资源列表和资源详情。

### Phase 2：资源图谱

- VPC / vSwitch / ECS / 云盘 / 安全组 / EIP 关系。
- graph API。
- 简单 Web 图谱视图。
- 关系证据和置信度。

### Phase 3：候选识别

- 未挂载云盘。
- 未使用 EIP。
- 老旧快照。
- 停止 ECS。
- 无引用安全组。
- 候选列表 UI。

### Phase 4：CleanupPlan

- dry-run plan。
- 删除顺序。
- 风险摘要。
- blast radius 限制。
- 手动审批。

### Phase 5：执行器与报告

- 阿里云基础清理动作。
- 幂等执行。
- 失败恢复。
- 审计日志。
- 节省报告。

### Phase 6：多云与 IaC

- AWS connector。
- Google Cloud connector。
- Azure connector。
- Kubernetes connector。
- Terraform state connector。
- owner / team / application 归属增强。

## 17. 风险与依赖

### 17.1 云资源覆盖面

云资源类型非常多，MVP 不能追求全覆盖。需要优先覆盖最常见、成本影响最大、删除相对安全的资源。

### 17.2 依赖关系不完整

云厂商 API 返回的关系不一定完整。系统必须在 UI 和计划中明确关系来源和置信度，不能把推断关系伪装成确定事实。

### 17.3 删除动作不可逆

删除资源有不可逆风险。系统必须优先提供 tag、stop、quarantine、notify 等渐进动作，而不是默认直接 delete。

### 17.4 成本归因复杂

成本数据通常延迟、聚合且难以精确映射到资源。MVP 只做估算和参考，不把节省金额作为强一致数据。

### 17.5 权限与信任

用户需要授予系统云账号权限。系统必须提供最小权限说明、只读模式、执行权限分离和完整审计。

## 18. 开源与商业化边界

### 18.1 开源版

开源版首期应包含：

- 单租户自托管。
- Server CLI。
- Web UI。
- SQLite 本地测试和 MySQL 存储。
- 阿里云基础 connector。
- 资源图谱。
- 候选识别规则。
- CleanupPlan dry-run。
- 手动审批。
- 基础执行器。
- 审计日志。
- SQLite 本地测试和 MySQL 接入说明。
- Helm chart。
- 插件 SDK。

### 18.2 商业化版本

商业化不卖“删除能力”，而卖企业治理能力。

可收费能力：

- 托管 SaaS。
- 多租户。
- SSO / SAML / SCIM。
- 细粒度 RBAC。
- 高级审批流。
- Jira / ServiceNow / Slack / Teams / 飞书 / 钉钉集成。
- 企业审计留存。
- 合规报表。
- 大规模账号调度。
- HA worker。
- 高级拓扑推断。
- 高级成本归因。
- 策略包市场。
- 私有云 / 混合云 connector。
- 专业支持。
- 企业实施服务。

## 19. 开放问题

- 阿里云首期是否只支持一个账号，还是支持多账号顺序扫描？
- 成本数据首期是否接入账单 API，还是先由资源规格估算？
- 执行器首期 live 动作只允许 tag；delete、release、stop、detach 等动作保持 dry-run 或跳过。
- 审批首期是否只做本地手动状态流转，还是需要接入飞书或钉钉？

## 20. 命名建议

- 项目名：Cloud Steward。
- GitHub 仓库：`cloud-steward`。
- CLI：`steward`。
- 官网：`cloudsteward.io` 或 `cloudsteward.dev`。

如果后续区分开源和商业版本：

- 开源项目可以保留 `cloud-steward`。
- 商业产品可以使用 Cloud Steward Cloud 或 Cloud Steward Enterprise。
