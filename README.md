<p align="center">
  <a href="https://loomx.ai/steward"><img src="web/public/brand/steward-symbol.svg" alt="Steward" width="72" height="72"></a>
</p>

<h1 align="center">steward</h1>

<p align="center">
  <strong>See your cloud. Take control of cleanup.</strong>
</p>

<p align="center">
  <a href="https://github.com/loomx-ai/steward/releases/latest"><img src="https://img.shields.io/github/v/release/loomx-ai/steward?color=2D7BFE" alt="Latest release"></a>
  <a href="https://github.com/loomx-ai/steward/actions/workflows/test.yml"><img src="https://github.com/loomx-ai/steward/actions/workflows/test.yml/badge.svg" alt="Tests"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-9B51E0.svg" alt="Apache-2.0 license"></a>
</p>

<p align="center">
  <a href="https://loomx.ai/steward">Website</a> ·
  <a href="https://loomx.ai/steward/docs/latest/en/">Documentation</a> ·
  <a href="https://loomx.ai/steward/docs/latest/en/tutorials/first-inventory/">Tutorial</a>
</p>

<p align="center">
  English / <a href="README.zh-CN.md">简体中文</a>
</p>

Steward is an open-source tool for discovering, understanding, and cleaning up cloud resources. It brings [Alibaba Cloud](https://loomx.ai/steward/docs/latest/en/alicloud/), [AWS](https://loomx.ai/steward/docs/latest/en/aws/), [Google Cloud](https://loomx.ai/steward/docs/latest/en/gcp/), and [Microsoft Azure](https://loomx.ai/steward/docs/latest/en/azure/) into one interface, so you can see what is running and review what a cleanup would affect.

Get started in your browser with [Steward Cloud](https://steward.console.loomx.ai), or [install Steward](https://loomx.ai/steward/docs/latest/en/installation/) to run it yourself.

<p align="center">
  <a href="https://loomx.ai/steward/docs/latest/en/topology/"><img src="docs/assets/relationships-en.png" alt="Steward resource panorama showing instances, a load balancer, a security group, and their relationships in a sample Alibaba Cloud VPC" width="960"></a>
  <br>
  <sub>Resource panorama · Alibaba Cloud sample data</sub>
</p>

## What Steward does

- **[Cloud inventory](https://loomx.ai/steward/docs/latest/en/resources/).** Discover what is running across your cloud accounts and find the resources that need attention.
- **[Resource relationships](https://loomx.ai/steward/docs/latest/en/topology/).** Explore how resources connect and understand shared dependencies before making changes.
- **[Reviewed cleanup](https://loomx.ai/steward/docs/latest/en/cleanup/).** Review the impact before execution, then follow the results of each cleanup.

<a name="quick-start"></a>
<a name="get-started"></a>

## Getting started & documentation

Learn the concepts in [What is Steward?](https://loomx.ai/steward/docs/latest/en/intro/), then follow the [quick start](https://loomx.ai/steward/docs/latest/en/quick-start/) or work through your [first resource inventory](https://loomx.ai/steward/docs/latest/en/tutorials/first-inventory/).

The [documentation](https://loomx.ai/steward/docs/latest/en/) covers cloud connections, inventory, relationships, cleanup, and deployment.

## Developing Steward

This repository contains the Steward service, web console, and cloud provider integrations. Contributions are welcome:

- [Build from source and develop locally](https://loomx.ai/steward/docs/latest/en/development/)
- [Report an issue or discuss a proposed change](https://github.com/loomx-ai/steward/issues)

## License

[Apache License 2.0](LICENSE) · Copyright 2026 LoomX
