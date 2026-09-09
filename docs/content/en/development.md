---
title: "Developer guide"
description: "Build Steward from source and develop the frontend and API locally."
navTitle: "Source and local development"
---

## Prerequisites

Requires Git, Go 1.26+, Node.js 22+, npm, make, and a C compiler. Use Xcode Command Line Tools on macOS, GCC on Linux, or MinGW-w64 and Git Bash on Windows.

<span id="source"></span>

## Build from source

```sh
git clone https://github.com/loomx-ai/steward.git
cd steward
make install
make build
./bin/steward --version
```

`make build` embeds the web console in the executable. Plain `go install` does not include the web console.

<span id="development"></span>

## Local development

```sh
make dev
```

Open [http://127.0.0.1:5858](http://127.0.0.1:5858). The frontend supports hot reload and its proxy generates an API token automatically. Do not run `make run` at the same time: both use the same API port.

## Run checks

```sh
make test
make lint
```
