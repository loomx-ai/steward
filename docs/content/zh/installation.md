---
title: "安装 Steward"
description: "通过 Homebrew、Scoop、安装脚本、Linux 软件包或独立二进制安装。"
navTitle: "安装"
---

发布版二进制内含 Web 控制台和数据库迁移脚本，无需安装 Go、Node.js 或单独的数据库服务。

| 平台 | 架构 | 安装方式 |
| --- | --- | --- |
| macOS 14+ | Apple Silicon / Intel | Homebrew、安装脚本、tar.gz |
| Linux | ARM64 / x86-64 | Homebrew、安装脚本、deb、rpm、tar.gz |
| Windows | x86-64 | Scoop、ZIP |

Linux 产物使用 musl 静态链接，同一压缩包可用于 glibc 和 musl 发行版。请安装发行版的 CA 证书包，以便通过 HTTPS 连接云平台。Windows ARM64 可以在 WSL2 中使用 Linux ARM64 版本。

<span id="homebrew"></span>

## Homebrew

在 macOS 或 Linux 上执行：

```bash
brew install loomx-ai/tap/steward
steward --version
```

使用 `brew update && brew upgrade steward` 升级，`brew uninstall steward` 卸载。Tap 会安装对应平台的发布版二进制及 Shell 补全。

<span id="script"></span>

## 安装脚本

在 macOS 或 Linux 上执行：

```bash
curl -fsSL https://github.com/loomx-ai/steward/releases/latest/download/install.sh -o install-steward.sh
sh install-steward.sh
export PATH="$HOME/.local/bin:$PATH"
```

脚本自动识别系统和 CPU，校验压缩包的 SHA-256，默认安装到 `~/.local/bin`。不会调用 sudo 或修改 Shell 配置；请在 Shell 配置中将该目录加入 PATH，以供后续终端使用。再次运行脚本即可升级。

指定版本或其他可写安装目录时，将 `v1.2.3` 替换为已发布的标签：

```bash
STEWARD_INSTALL_DIR="$HOME/bin" sh install-steward.sh v1.2.3
```

删除安装目录中的 `steward` 可执行文件即可卸载。

<span id="linux"></span>

## Debian、Ubuntu、Fedora 和 RHEL

从 [GitHub Releases](https://github.com/loomx-ai/steward/releases) 下载对应的 `linux_amd64` 或 `linux_arm64` 软件包及 `checksums.txt`，按下文校验后安装。这些是本地软件包，无需配置 APT 或 RPM 仓库。

```bash
# 替换为下载的软件包版本和架构。
sudo apt install ./steward_1.2.3_linux_amd64.deb
# Fedora / RHEL 使用：
sudo dnf install ./steward_1.2.3_linux_amd64.rpm
```

用相同方式安装新版本即可升级。使用 `sudo apt remove steward` 或 `sudo dnf remove steward` 卸载。安装不会自动启动后台服务。

<span id="windows"></span>

## Windows / Scoop

安装 Scoop 后，在 PowerShell 中执行：

```powershell
scoop bucket add loomx-ai https://github.com/loomx-ai/homebrew-tap
scoop install loomx-ai/steward
steward --version
```

使用 `scoop update steward` 升级，`scoop uninstall steward` 卸载。

也可以下载 `steward_<version>_windows_amd64.zip`，运行 `Get-FileHash -Algorithm SHA256 <file>`，与 `checksums.txt` 中对应哈希比对后，将 `steward.exe` 解压到 PATH 中的目录。在 PowerShell 运行 `steward server start` 启动。请在服务所在终端按 Ctrl+C 正常关闭；Windows 上的 `steward server stop` 会强制终止进程。

<span id="binary"></span>

## 下载二进制

每个 [GitHub Release](https://github.com/loomx-ai/steward/releases) 都提供各平台压缩包、Linux 软件包和 `checksums.txt`。文件名中的 `darwin` 表示 macOS，`amd64` 表示 Intel/AMD x86-64，`arm64` 表示 Apple Silicon / ARM64。

Linux 在下载目录执行：

```bash
sha256sum --check --ignore-missing checksums.txt
tar -xzf steward_1.2.3_linux_amd64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 755 steward "$HOME/.local/bin/steward"
```

macOS 使用 `shasum -a 256 <archive>`，将输出与 `checksums.txt` 中对应记录比对，再解压对应的 `darwin` 压缩包。确保安装目录已加入 PATH。macOS 二进制目前尚未经过 Apple 公证；保持 Gatekeeper 开启，手动下载的程序如有系统提示，请按系统流程处理。

<span id="source"></span>

## 从源码构建

需要 Git、Go 1.26+、Node.js 22+、npm、make，以及供 SQLite 使用的 C 编译器：macOS 使用 Xcode Command Line Tools，Linux 使用 GCC，Windows 使用 MinGW-w64。

```bash
git clone https://github.com/loomx-ai/steward.git
cd steward
make install
make build
./bin/steward --version
./bin/steward server start
```

Windows 使用配好 make 和 MinGW-w64 的 Git Bash。单独执行 `go install` 不包含 Web 控制台；完整应用请使用 `make build`。

<span id="start"></span>

## 启动与保留数据

```bash
mkdir -p "$HOME/steward-data"
cd "$HOME/steward-data"
steward server start
```

打开 [http://127.0.0.1:8585](http://127.0.0.1:8585)，本机访问无需登录。后续始终从同一工作目录启动：数据库和凭证密钥保存在其中的 `.steward/` 目录。升级前先停止服务并备份该目录。卸载可执行文件或软件包会保留这些数据。

[接入第一个云账号 →](./quick-start.md) · [网络访问与备份 →](./deployment.md)
