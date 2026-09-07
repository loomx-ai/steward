---
title: "快速开始"
description: "在本机运行 Steward，然后接入一个云账号。"
navTitle: "快速开始"
---

<span id="requirements"></span>

## 准备环境

需要 Git、Go 1.26+、Node.js 22+、npm 和 make。构建时需要下载 Go 与 npm 依赖。

<span id="run"></span>

## 安装并启动

```
git clone https://github.com/loomx-ai/steward.git
cd steward
make install
make run
```

打开 [http://127.0.0.1:8585](http://127.0.0.1:8585)，无需注册或登录。服务默认只接受本机访问。

按 Ctrl+C 停止服务。后续可直接运行已构建的程序：

```
./bin/steward server start
```

<span id="first-scan"></span>

## 接入并扫描

1.  打开用户菜单中的「设置」，添加云连接。
2.  选择云厂商，填写凭证，验证通过后确认地域列表。
3.  切换到该连接，进入「扫描」，从一个常用地域开始。
4.  扫描完成后，进入「资源」或「资源全景」查看结果。

[凭证类型和连接步骤 →](./connections.md)

<span id="development"></span>

## 开发模式

```
make dev
```

打开 [http://127.0.0.1:5858](http://127.0.0.1:5858)。前端支持热更新；开发代理使用自动生成的令牌连接 API，无需手动登录。不要与 make run 同时运行，两者使用同一个 API 端口。

<span id="data"></span>

## 数据保存在哪里

默认数据库为 .steward/steward.db，凭证加密密钥为 .steward/credential-master-key，路径相对于启动目录。重启时使用同一目录。

<aside class="docs-note">备份必须同时保存数据库和原始密钥。更换密钥会导致已有云凭证无法解密。</aside>

[网络访问、备份和升级 →](./deployment.md)
