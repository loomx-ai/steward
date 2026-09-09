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
  <a href="#快速开始">快速开始</a> ·
  <a href="https://loomx.ai/steward/docs/latest/zh/">使用文档</a> ·
  <a href="https://github.com/loomx-ai/steward/releases">版本下载</a> ·
  <a href="https://loomx.ai/steward">官方网站</a>
</p>

<p align="center">
  <a href="README.md">English</a> / 简体中文
</p>

Steward 是一个开源的云资源盘点与清理工具。接入云账号后，你可以查看正在运行的资源、理解它们之间的依赖，并在删除前审查影响范围。

<p align="center">
  <a href="https://loomx.ai/steward/docs/latest/zh/alicloud/">阿里云</a> &nbsp;·&nbsp;
  <a href="https://loomx.ai/steward/docs/latest/zh/aws/">AWS</a> &nbsp;·&nbsp;
  <a href="https://loomx.ai/steward/docs/latest/zh/gcp/">Google Cloud</a> &nbsp;·&nbsp;
  <a href="https://loomx.ai/steward/docs/latest/zh/azure/">Microsoft Azure</a>
</p>

<p align="center">
  <a href="docs/assets/relationships-zh.png"><img src="docs/assets/relationships-zh.png" alt="Steward 资源全景：阿里云示例 VPC 中的云实例、负载均衡、安全组及其依赖关系" width="960"></a>
  <br>
  <sub>资源全景 · 阿里云示例数据</sub>
</p>

## 为什么选择 Steward？

- **统一盘点云资源。** 按需扫描账号和地域，通过名称、ID、类型和属性查找资源，无需在多个云控制台间切换。
- **直观看清依赖。** 从地域、网络逐层查看资源，了解哪些实例共用负载均衡、子网或安全组。
- **结合上下文排查问题。** 将治理发现与资源详情、扫描结果一起查看，定位需要关注的问题。
- **清理前充分审查。** 确认目标、依赖、阻断项和保留资源后再执行，跟踪进度并通过审计记录核对结果。

## 快速开始

### 1. 安装并启动

**macOS 14+ 或 Linux** 使用 Homebrew：

```sh
brew install loomx-ai/tap/steward
mkdir -p "$HOME/steward-data"
cd "$HOME/steward-data"
steward server start
```

<details>
<summary><strong>Windows — 使用 Scoop 安装</strong></summary>

在 Windows x86-64 的 PowerShell 中运行：

```powershell
scoop bucket add loomx-ai https://github.com/loomx-ai/homebrew-tap
scoop install loomx-ai/steward
New-Item -ItemType Directory -Force "$HOME/steward-data" | Out-Null
Set-Location "$HOME/steward-data"
steward server start
```

</details>

其他安装方式见[安装指南](https://loomx.ai/steward/docs/latest/zh/installation/)，包括 APT、DNF、安装脚本和直接下载。

打开 **[localhost:8585](http://127.0.0.1:8585)**。本机使用无需注册或登录。

### 2. 接入云账号，开始探索

1. 打开用户菜单 → **设置 → 云连接**，选择云平台并添加凭证。
2. 选中该连接，进入**扫描**，先扫描一个正在使用的地域。
3. 在**资源**中找到一个已知资源，再进入**资源全景**查看它与其他资源的关系。

首次盘点只需要相关资源的读取权限。[首次资源盘点教程](https://loomx.ai/steward/docs/latest/zh/tutorials/first-inventory/)提供截图和每一步的预期结果。

重启时继续使用同一工作目录。升级前停止 Steward 并备份其中的 `.steward/` 文件夹，将数据库和原始凭证密钥一起保留。多人共享或远程访问请参考[部署指南](https://loomx.ai/steward/docs/latest/zh/deployment/)。

## 使用文档

| 我想要…… | 从这里开始 |
| --- | --- |
| 在自己的系统上安装 Steward | [安装指南](https://loomx.ai/steward/docs/latest/zh/installation/) |
| 接入云账号 | [云连接与凭证](https://loomx.ai/steward/docs/latest/zh/connections/) |
| 理解资源之间的依赖 | [资源关系](https://loomx.ai/steward/docs/latest/zh/topology/) |
| 审查并清理资源 | [资源清理](https://loomx.ai/steward/docs/latest/zh/cleanup/) |
| 配置网络访问与备份 | [部署指南](https://loomx.ai/steward/docs/latest/zh/deployment/) |

[浏览完整文档 →](https://loomx.ai/steward/docs/latest/zh/)

## 参与贡献

欢迎报告问题、完善云平台支持或改进文档。遇到问题或准备进行较大改动时，请先[创建 Issue](https://github.com/loomx-ai/steward/issues)讨论。

<details>
<summary><strong>在本地开发 Steward</strong></summary>

需要 Git、Go 1.26+、Node.js 22+、npm、make 和 C 编译器。

```sh
git clone https://github.com/loomx-ai/steward.git
cd steward
make install
make dev
```

打开 [localhost:5858](http://127.0.0.1:5858)，在热更新模式下开发。使用 `make build` 将完整应用构建到 `bin/steward`。

提交前运行以下检查，并为行为变更补充测试：

```sh
make test
make lint
make build
```

文档修改参见[文档贡献指南](docs/README.md)，版本发布参见 [RELEASING.md](RELEASING.md)。

</details>

## 许可证

[Apache License 2.0](LICENSE) · Copyright 2026 LoomX
