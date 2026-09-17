---
title: "开发者指南"
description: "从源码构建 Steward，并在本地开发前端和 API。"
navTitle: "源码与本地开发"
---

## 准备环境

需要 Git、Go 1.26+、Node.js 22+、npm、make 和 C 编译器。macOS 使用 Xcode Command Line Tools，Linux 使用 GCC，Windows 使用 MinGW-w64 和 Git Bash。

<span id="source"></span>

## 从源码构建

```sh
git clone https://github.com/loomx-ai/steward.git
cd steward
make install
make build
./bin/steward --version
```

`make build` 将 Web 控制台打包进可执行文件。单独执行 `go install` 不包含 Web 控制台。

<span id="development"></span>

## 本地开发

```sh
make dev
```

打开 [http://127.0.0.1:5858](http://127.0.0.1:5858)。前端支持热更新，开发代理会自动生成 API 令牌，无需手动登录。不要同时运行 `make run`，两者会使用同一个 API 端口。

## 运行检查

```sh
make test
make lint
```
