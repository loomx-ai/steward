<p align="center">
  <a href="https://loomx.ai/steward"><img src="web/public/brand/steward-symbol.svg" alt="Steward" width="72" height="72"></a>
</p>

<h1 align="center">steward</h1>

<p align="center">
  <strong>看清云资源，掌握每一次清理。</strong>
</p>

<p align="center">
  <a href="https://github.com/loomx-ai/steward/releases/latest"><img src="https://img.shields.io/github/v/release/loomx-ai/steward?color=2D7BFE" alt="最新版本"></a>
  <a href="https://github.com/loomx-ai/steward/actions/workflows/test.yml"><img src="https://github.com/loomx-ai/steward/actions/workflows/test.yml/badge.svg" alt="测试"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-9B51E0.svg" alt="Apache-2.0 许可证"></a>
</p>

<p align="center">
  <a href="https://loomx.ai/steward">官方网站</a> ·
  <a href="https://loomx.ai/steward/docs/latest/zh/">使用文档</a> ·
  <a href="https://loomx.ai/steward/docs/latest/zh/tutorials/first-inventory/">入门教程</a>
</p>

<p align="center">
  <a href="README.md">English</a> / 简体中文
</p>

Steward 是一个开源的云资源盘点与清理工具。它将[阿里云](https://loomx.ai/steward/docs/latest/zh/alicloud/)、[AWS](https://loomx.ai/steward/docs/latest/zh/aws/)、[Google Cloud](https://loomx.ai/steward/docs/latest/zh/gcp/) 和 [Microsoft Azure](https://loomx.ai/steward/docs/latest/zh/azure/) 的资源带到同一个界面，帮助你看清正在运行的资源，并在清理前理解影响范围。

你可以在浏览器中使用 [Steward Cloud](https://steward.console.loomx.ai)，也可以[自行安装 Steward](https://loomx.ai/steward/docs/latest/zh/installation/)。

<p align="center">
  <a href="https://loomx.ai/steward/docs/latest/zh/topology/"><img src="docs/assets/relationships-zh.png" alt="Steward 资源全景：阿里云示例 VPC 中的云实例、负载均衡、安全组及其依赖关系" width="960"></a>
  <br>
  <sub>资源全景 · 阿里云示例数据</sub>
</p>

## Steward 能做什么

- **[云资源盘点](https://loomx.ai/steward/docs/latest/zh/resources/)。** 了解各个云账号中正在运行的资源，找到需要关注的资产。
- **[资源关系](https://loomx.ai/steward/docs/latest/zh/topology/)。** 直观探索资源之间的连接，在变更前看清共享依赖。
- **[受控清理](https://loomx.ai/steward/docs/latest/zh/cleanup/)。** 执行前审查影响，执行后跟踪每一次清理的结果。

<a name="快速开始"></a>

## 入门与文档

从[认识 Steward](https://loomx.ai/steward/docs/latest/zh/intro/)了解基本概念，再跟随[快速开始](https://loomx.ai/steward/docs/latest/zh/quick-start/)或[首次资源盘点教程](https://loomx.ai/steward/docs/latest/zh/tutorials/first-inventory/)上手。

[使用文档](https://loomx.ai/steward/docs/latest/zh/)涵盖云连接、资源盘点、关系分析、清理和部署。

## 参与开发

本仓库包含 Steward 服务、Web 控制台和云平台集成。欢迎参与贡献：

- [源码构建与本地开发](https://loomx.ai/steward/docs/latest/zh/development/)
- [报告问题或讨论改进](https://github.com/loomx-ai/steward/issues)

## 许可证

[Apache License 2.0](LICENSE) · Copyright 2026 LoomX
