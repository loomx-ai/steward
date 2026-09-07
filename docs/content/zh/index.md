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

Steward 支持阿里云和 AWS，可在本机或服务器上运行。Steward Cloud 由 LoomX 托管，目前通过邀请开放。

<div class="docs-start-links"><a href="./quick-start.md"><strong>自行部署</strong><span aria-hidden="true">↗</span><span>安装并启动本地服务</span></a><a href="https://steward.console.loomx.ai"><strong>Steward Cloud</strong> <span aria-hidden="true">↗</span><span>已有邀请？进入工作区</span></a></div>

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
