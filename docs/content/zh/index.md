---
title: "Steward 文档"
description: "连接云账号，盘点资源，查看依赖，再审查和执行清理。"
navTitle: "文档首页"
---

<span id="start"></span>

## 从这里开始

Steward 帮助你盘点已有云资源、理解它们的关系，并在审查影响后执行清理。

- **先了解产品**：[认识 Steward](./intro.md)，看懂扫描、资源记录和清理之间的关系。
- **动手完成一件事**：[第一次资源盘点](./tutorials/first-inventory.md)，从一个已知资源开始，逐步核对扫描结果。
- **查找具体操作**：按侧边栏进入连接、扫描、查询、关系或清理指南。

Steward 支持阿里云、AWS、Google Cloud（GCP）和 Microsoft Azure，可在本机或服务器上运行，也可注册使用由 LoomX 托管的 Steward Cloud。

<div class="docs-start-links"><a href="./quick-start.md"><strong>自行部署</strong><span aria-hidden="true">↗</span><span>安装并启动本地服务</span></a><a href="https://steward.console.loomx.ai"><strong>Steward Cloud</strong> <span aria-hidden="true">↗</span><span>注册账号，进入工作区</span></a><a href="./azure.md"><strong>Microsoft Azure</strong><span>订阅、服务主体与资源锁</span></a></div>

## 连接你的云平台

选择云平台，查看凭证、权限、盘点范围和清理注意事项。

<div class="docs-cloud-links"><a href="./alicloud.md"><strong>阿里云</strong><span>RAM、STS 与浏览器授权</span></a><a href="./aws.md"><strong>AWS</strong><span>IAM、资源发现与资源栈</span></a><a href="./gcp.md"><strong>Google Cloud</strong><span>项目、服务账号与全局网络</span></a><a href="./azure.md"><strong>Microsoft Azure</strong><span>订阅、服务主体与资源锁</span></a></div>

<span id="workflow"></span>

## 常用操作

1.  [添加云连接](./connections.md)，验证凭证并确认地域。
2.  [运行一次扫描](./scans.md)，等待所选范围完成。
3.  [查找资源](./resources.md)，打开详情，核对属性和最后发现时间。
4.  [查看资源关系](./topology.md)，确认仍有哪些资源依赖它。
5.  需要删除时，再[创建清理任务](./cleanup.md)并审查影响范围。

<span id="read-the-map"></span>

## 资源全景

<figure class="docs-figure"><a href="../../assets/topology-zh.png" target="_blank" rel="noreferrer" aria-label="同一 VPC 中的交换机、实例、数据库和安全组（打开原图）"><img src="../../assets/topology-zh.png" alt="同一 VPC 中的交换机、实例、数据库和安全组" width="1440" height="960"></a><figcaption>同一 VPC 中的交换机、实例、数据库和安全组 <span>· 示例数据，点击放大</span></figcaption></figure>

资源清单和关系图来自扫描结果。云端发生变更后，需要重新扫描。清理任务会列出阻塞和保留项；创建任务不会立即删除资源。
