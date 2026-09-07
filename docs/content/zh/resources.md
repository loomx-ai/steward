---
title: "查询资源"
description: "从名称或资源 ID 开始，核对属性和发现时间。"
navTitle: "查询资源"
---

<span id="find"></span>

## 查找资源

进入「资源」，确认左上角的当前云连接。普通搜索支持名称、云资源 ID 和类型，例如 api-01 或 i-demo-api01。

<figure class="docs-figure"><a href="../../assets/inventory-zh.png" target="_blank" rel="noreferrer" aria-label="资源清单显示名称、类型、地域和最后发现时间（打开原图）"><img src="../../assets/inventory-zh.png" alt="资源清单显示名称、类型、地域和最后发现时间" width="1440" height="960"></a><figcaption>资源清单显示名称、类型、地域和最后发现时间 <span>· 示例数据，点击放大</span></figcaption></figure>

<span id="query"></span>

## 组合查询条件

点击搜索框左侧的模式按钮切换到高级查询。输入字段时使用界面补全，按 Enter 应用。

```
name contains "api" and region = "ap-southeast-1"
```

高级查询支持 and、or、not 和括号。字段与取值以输入框提示为准。官网公开演示只支持普通搜索；高级查询需要本地服务或 Steward Cloud。

<span id="details"></span>

## 查看详情

点击资源名称打开详情。「概览」显示属性和标签，「关系」显示相关资源。使用「在资源全景中查看」定位到资源所在的网络。

列表中的最后发现时间是扫描观察时间，不是实时状态。需要删除时，先核对资源所属账号、地域和原始 ID。

<span id="selection"></span>

## 加入清理清单

勾选资源后选择「加入清理清单」。这只保存待审查目标；到清理清单中创建任务，审查通过并确认执行后才会发起删除。

「标记为脏资源」用于排除已核实的无效记录，清理会忽略这些记录。它不会删除云端资源，不应用来绕过依赖阻塞。
