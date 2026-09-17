---
title: "快速开始"
description: "在本机运行 Steward，然后接入一个云账号。"
navTitle: "快速开始"
---

<span id="requirements"></span>

## 准备环境

选择一种[安装方式](./installation.md)。Steward 已包含 Web 控制台和数据库，无需额外安装运行环境。

<span id="run"></span>

## 启动 Steward

<div data-docs-tabs data-label="启动方式">
<div data-tab="macOS / Linux">

### macOS / Linux

```sh
mkdir -p "$HOME/steward-data"
cd "$HOME/steward-data"
steward server start
```

</div>
<div data-tab="Windows">

### Windows

```powershell
New-Item -ItemType Directory -Force "$HOME/steward-data" | Out-Null
Set-Location "$HOME/steward-data"
steward server start
```

</div>
</div>

打开 [http://127.0.0.1:8585](http://127.0.0.1:8585)，无需注册或登录。服务默认只接受本机访问。

按 Ctrl+C 停止服务。下次从同一工作目录启动：

```
steward server start
```

<span id="first-scan"></span>

## 接入并扫描

服务启动后，跟随[第一次资源盘点](./tutorials/first-inventory.md)完成下面的流程。教程会逐步说明预期结果和排查方法。

1.  打开用户菜单中的「设置」，添加云连接。
2.  选择云厂商，填写凭证，验证通过后确认地域列表。
3.  切换到该连接，进入「扫描」，从一个常用地域开始。
4.  扫描完成后，进入「资源」或「资源全景」查看结果。

[凭证类型和连接步骤 →](./connections.md)

<span id="data"></span>

## 数据保存在哪里

默认数据库为 .steward/steward.db，凭证加密密钥为 .steward/credential-master-key，路径相对于启动目录。重启时使用同一目录。

<aside class="docs-note">备份必须同时保存数据库和原始密钥。更换密钥会导致已有云凭证无法解密。</aside>

[网络访问与备份 →](./deployment.md)
