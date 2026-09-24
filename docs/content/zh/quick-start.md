---
title: "快速开始"
description: "打开 Steward Cloud 工作空间，或在自己的机器上启动 Steward，接入云账号并完成第一次扫描。"
navTitle: "快速开始"
---

先选择 Steward 的运行方式。进入界面之后，两种方式的操作步骤完全相同。

| | Steward Cloud | 自行部署 |
| --- | --- | --- |
| 开始方式 | 在浏览器中注册 | 安装一个可执行文件 |
| 由谁运行 | LoomX | 你自己，在本机或服务器上 |
| 数据存放位置 | LoomX 托管的工作空间 | 你自己的数据目录 |
| 云账号接入方式 | 密钥类凭证（含临时凭证） | 密钥类凭证，以及[浏览器登录](./connections.md#browser)和配置后可用的 [OIDC](./oidc.md) |

<span id="cloud"></span>

## 使用 Steward Cloud

1. 打开 [Steward Cloud](https://steward.console.loomx.ai/auth/signup?lang=zh)，用邮箱注册，或选择使用 Google、GitHub 继续。
2. 用邮箱注册时，请在 30 分钟内打开验证邮件中的链接。
3. 等待个人工作空间准备完成，然后点击**打开工作空间**。容量已满时工作空间会排队等待，可点击**刷新状态**查看进度。

接着阅读[接入并扫描](#first-scan)。

<span id="requirements"></span>

## 自行运行 Steward

按操作系统[安装 Steward](./installation.md)。可执行文件已包含 Web 控制台和数据库，无需安装其他组件。

<span id="run"></span>

启动服务：

```sh
steward server start
```

打开 [http://127.0.0.1:8585](http://127.0.0.1:8585)。这种本机模式无需登录，服务只接受来自本机的访问。按 Ctrl+C 停止服务。如需让其他人访问，请参阅[在服务器上部署](./deployment.md)。

<span id="first-scan"></span>

## 接入并扫描

1. 打开用户菜单 → **设置** → **云连接** → **添加云连接**。选择云厂商和凭证类型，填写凭证后创建。Steward 会先确认凭证所属的云身份，再保存连接。凭证类型和权限说明见[连接云账号](./connections.md)。
2. 查看新连接的地域列表，确认包含你使用的地域。
3. 选中该连接，进入**扫描** → **开始扫描**，先扫描一个确定有资源的地域。
4. 扫描完成后，进入**资源**搜索这项资源，或进入**资源全景**查看它所在的网络。

[第一次资源盘点](./tutorials/first-inventory.md)按相同步骤展开，逐步说明预期结果和排查方法。

<span id="data"></span>

## 自行部署时的数据位置

数据保存在 `~/.steward`（Windows 为 `%USERPROFILE%\.steward`）：数据库 `steward.db` 和凭证加密密钥 `credential-master-key`。设置 `STEWARD_HOME` 可改用其他目录。

<aside class="docs-note">数据库和密钥必须一起备份。缺少原始密钥时，Steward 无法解密数据库中保存的云凭证。</aside>

[网络访问与备份 →](./deployment.md)
