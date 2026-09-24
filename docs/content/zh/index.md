---
title: "Steward 文档"
description: "盘点阿里云、AWS、Azure 与 Google Cloud 中的全部资源，看清依赖关系，在删除之前完成清理审查。"
navTitle: "文档首页"
---

<div class="docs-start-links docs-entry-links"><a href="./quick-start.md"><strong>快速开始</strong><span aria-hidden="true">→</span><span>使用 Steward Cloud 或在本机运行，完成第一次扫描</span></a><a href="./tutorials.md"><strong>动手教程</strong><span aria-hidden="true">→</span><span>一步步盘点阿里云资源、审查清理依赖</span></a><a href="./guides.md"><strong>使用文档</strong><span aria-hidden="true">→</span><span>云连接、扫描、资源关系与清理</span></a></div>

## 认识 Steward

Steward 是 LoomX 推出的开源云资源盘点工具。它扫描你接入的云账号，把发现的资源汇总到一份可搜索的清单里，画出资源之间的关联，并在删除之前检查清理会不会影响仍在使用的资源。扫描只读取数据；在你确认执行清理任务之前，云上不会发生任何变更。

可以直接使用 LoomX 托管的 [Steward Cloud](https://steward.console.loomx.ai)，也可以[安装 Steward](./installation.md)，在自己的电脑或服务器上运行。

[了解工作原理 →](./intro.md)

## 连接云平台

<div class="docs-cloud-links"><a href="./alicloud.md"><strong>阿里云</strong><span>RAM、STS 与浏览器登录</span></a><a href="./aws.md"><strong>AWS</strong><span>访问密钥、IAM Identity Center 与权限</span></a><a href="./gcp.md"><strong>Google Cloud</strong><span>项目与服务账号</span></a><a href="./azure.md"><strong>Microsoft Azure</strong><span>订阅与服务主体</span></a></div>

## 常用操作

<div class="docs-start-links"><a href="./scans.md"><strong>扫描资源</strong><span aria-hidden="true">→</span><span>选择扫描范围，读懂扫描结果</span></a><a href="./topology.md"><strong>查看资源关系</strong><span aria-hidden="true">→</span><span>查看资源所在网络及其关联</span></a><a href="./cleanup.md"><strong>清理资源</strong><span aria-hidden="true">→</span><span>审查目标和阻断项，再确认执行</span></a><a href="./deployment.md"><strong>在服务器上部署</strong><span aria-hidden="true">→</span><span>网络访问、登录与数据备份</span></a></div>
