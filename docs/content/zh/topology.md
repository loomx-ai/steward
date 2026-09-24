---
title: "查看资源关系"
description: "在资源全景中定位资源所在的地域、VPC 和交换机，再显示关系连线，找出共享的依赖。"
navTitle: "查看资源关系"
---

**资源全景**展示资源所在的位置（地域、VPC、交换机）以及它与哪些资源相连。决定清理范围之前，用它找出共享的网络、安全组和负载均衡。

<span id="navigate"></span>

## 逐层进入网络

1. 进入**资源全景**。顶层显示账号下的地域；**全局资源**和地域公共资源有单独入口。
2. 选择地域，再进入 VPC。VPC 视图按交换机排列资源。
3. 使用面包屑返回上一级。

<figure class="docs-figure"><a href="../../assets/topology-zh.png" target="_blank" rel="noreferrer" aria-label="production VPC：应用与数据库位于不同交换机（打开原图）"><img src="../../assets/topology-zh.png" alt="production VPC：应用与数据库位于不同交换机" width="1440" height="960"></a><figcaption>production VPC：应用与数据库位于不同交换机 <span>· 示例数据，点击放大</span></figcaption></figure>

聚合节点可以展开查看成员。要定位资源，可在画布中按地域、资源 ID 或名称搜索。要查看属性，右键点击资源并选择**查看详情**。

<span id="relationships"></span>

## 显示关系连线

点击画布工具栏中的**显示关系连线**。本例中，`public-gateway` 指向两台 ECS 实例，两台实例共用 `application-policy` 安全组。

<figure class="docs-figure"><a href="../../assets/relationships-zh.png" target="_blank" rel="noreferrer" aria-label="两台 ECS 共用负载均衡和安全组（打开原图）"><img src="../../assets/relationships-zh.png" alt="两台 ECS 共用负载均衡和安全组" width="1440" height="960"></a><figcaption>两台 ECS 共用负载均衡和安全组 <span>· 示例数据，点击放大</span></figcaption></figure>

只关心一项资源时，在**资源**中打开它并切换到**关系**标签页，以该资源为中心查看关联。

<span id="interpret"></span>

## 如何使用这些关系

删除共享网络或安全组前，逐个打开与之相连的资源，确认它们是否仍然需要。关系图只是线索，能否删除以清理任务的依赖检查和云端实际状态为准。

关系来自已扫描的资源和 Steward 当前支持的关系规则；涉及未扫描资源、或规则尚未覆盖的关系不会显示连线。预期的连线缺失时，先[扫描完整范围](./scans.md#freshness)，再判断是否存在依赖。

确定目标后，右键点击并选择**加入资源清单**，或使用**框选** → **添加所选项**。然后从**资源清单**创建任务，参阅[清理资源](./cleanup.md#select)。

## Google Cloud 网络

GCP VPC 是全局资源，子网属于地域，因此同一 VPC 可能出现在多个地域视图中。清理某个地域内的 VPC 分组时，会保留共享的全局网络。参阅 [Google Cloud](./gcp.md#全局-vpc-与地域子网)。

## 下一步

想练习检查共享资源、并在删除前记录范围决定，请阅读[清理依赖审查教程](./tutorials/review-cleanup-dependencies.md)。
