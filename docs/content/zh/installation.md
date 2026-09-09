---
title: "安装 Steward"
description: "选择操作系统，使用包管理器安装，或直接下载独立二进制。"
navTitle: "安装"
release:
  version: "0.1.0"
  index: "https://loomx-ai.github.io/packages/steward/releases.json"
---

<nav class="docs-os-links" aria-label="操作系统"><a href="#macos">macOS</a><a href="#windows">Windows</a><a href="#linux">Linux</a></nav>

<span id="macos"></span>

## macOS

支持 macOS 14 及以上版本，Apple Silicon 和 Intel 芯片。

<span id="homebrew"></span>

### Homebrew

```sh
brew tap loomx-ai/tap
brew install loomx-ai/tap/steward
```

### 二进制下载

<div class="docs-downloads">
<div class="docs-download">
<a data-release-asset="steward_{version}_darwin_arm64.tar.gz" href="https://github.com/loomx-ai/steward/releases/download/v0.1.0/steward_0.1.0_darwin_arm64.tar.gz" aria-label="下载 Steward darwin arm64"><strong>Apple Silicon</strong><span>下载 ↓</span></a>
<p>v<span data-release-version>0.1.0</span> · tar.gz</p>
<details><summary>SHA-256</summary><code data-release-hash>932872e51c5a69022dc6476ac642536980dd48e9e43a224820364c22292c52e1</code></details>
</div>
<div class="docs-download">
<a data-release-asset="steward_{version}_darwin_amd64.tar.gz" href="https://github.com/loomx-ai/steward/releases/download/v0.1.0/steward_0.1.0_darwin_amd64.tar.gz" aria-label="下载 Steward darwin amd64"><strong>Intel / AMD</strong><span>下载 ↓</span></a>
<p>v<span data-release-version>0.1.0</span> · tar.gz</p>
<details><summary>SHA-256</summary><code data-release-hash>50d9ffc678448abe0db0b1859018cb53e7146df73860309896cbe16b2ea645f4</code></details>
</div>
</div>

<span id="windows"></span>

## Windows

### Scoop

在已安装 Scoop 的 PowerShell 中执行：

```powershell
scoop bucket add loomx-ai https://github.com/loomx-ai/homebrew-tap
scoop install loomx-ai/steward
```

### 二进制下载

<div class="docs-downloads">
<div class="docs-download">
<a data-release-asset="steward_{version}_windows_amd64.zip" href="https://github.com/loomx-ai/steward/releases/download/v0.1.0/steward_0.1.0_windows_amd64.zip" aria-label="下载 Steward windows amd64"><strong>AMD64 / x86-64</strong><span>下载 ↓</span></a>
<p>v<span data-release-version>0.1.0</span> · zip</p>
<details><summary>SHA-256</summary><code data-release-hash>f723f419836d3137ac03a71815eca9478a6e8145527568e65c232ee4fd56ae89</code></details>
</div>
</div>

解压后将 `steward.exe` 所在目录加入用户 PATH，再打开一个 PowerShell 窗口。Windows ARM64 可通过 WSL2 使用 Linux ARM64 版本。

<span id="linux"></span>

## Linux

支持 AMD64（x86-64）和 ARM64（aarch64）。软件源会随稳定版本更新，之后可直接使用系统包管理器升级。

<div data-docs-tabs data-label="Linux 安装方式">
<div data-tab="Ubuntu / Debian">

### Ubuntu / Debian

```sh
sudo apt-get update
sudo apt-get install -y ca-certificates curl
sudo install -d -m 0755 /etc/apt/keyrings
curl -fsSL https://loomx-ai.github.io/packages/steward/gpg.key | sudo tee /etc/apt/keyrings/loomx-steward.asc > /dev/null
echo "deb [arch=$(dpkg --print-architecture) signed-by=/etc/apt/keyrings/loomx-steward.asc] https://loomx-ai.github.io/packages/steward/apt stable main" | sudo tee /etc/apt/sources.list.d/loomx-steward.list
sudo apt-get update
sudo apt-get install steward
```

</div>
<div data-tab="Fedora / RHEL">

### Fedora / RHEL

适用于 Fedora、RHEL 9+、Rocky Linux 9+ 和 Amazon Linux 2023。首次安装时确认导入 LoomX 签名公钥。

```sh
sudo curl -fsSL https://loomx-ai.github.io/packages/steward/steward.repo -o /etc/yum.repos.d/loomx-steward.repo
sudo dnf install steward
```

</div>
<div data-tab="Homebrew">

### Linux Homebrew

```sh
brew tap loomx-ai/tap
brew install loomx-ai/tap/steward
```

</div>
</div>

### 二进制下载

静态链接产物可用于 glibc 和 musl 发行版。系统需安装 CA 证书以连接云服务。

<div class="docs-downloads">
<div class="docs-download">
<a data-release-asset="steward_{version}_linux_arm64.tar.gz" href="https://github.com/loomx-ai/steward/releases/download/v0.1.0/steward_0.1.0_linux_arm64.tar.gz" aria-label="下载 Steward linux arm64"><strong>ARM64</strong><span>下载 ↓</span></a>
<p>v<span data-release-version>0.1.0</span> · tar.gz</p>
<details><summary>SHA-256</summary><code data-release-hash>33ba319bb58b18ffae5e053104fef04badb8abb72e98341bfc8ec85aff49d65e</code></details>
</div>
<div class="docs-download">
<a data-release-asset="steward_{version}_linux_amd64.tar.gz" href="https://github.com/loomx-ai/steward/releases/download/v0.1.0/steward_0.1.0_linux_amd64.tar.gz" aria-label="下载 Steward linux amd64"><strong>AMD64 / x86-64</strong><span>下载 ↓</span></a>
<p>v<span data-release-version>0.1.0</span> · tar.gz</p>
<details><summary>SHA-256</summary><code data-release-hash>c50d13b6343f53267bd068804e4c205511d83384c51d492afd51dbd63cbc4249</code></details>
</div>
</div>

也可从所选版本的 Release 下载 <a data-release-notes href="https://github.com/loomx-ai/steward/releases/tag/v0.1.0">deb / rpm 软件包</a>，校验后通过 `sudo apt install ./文件.deb` 或 `sudo dnf install ./文件.rpm` 本地安装。此方式不会添加软件源。

<span id="script"></span>

## 安装脚本

适用于 macOS 和 Linux，自动识别系统和架构，并在安装前校验 SHA-256。

```sh
curl -fsSL https://github.com/loomx-ai/steward/releases/latest/download/install.sh -o install-steward.sh
sh install-steward.sh
export PATH="$HOME/.local/bin:$PATH"
```

默认安装到 `~/.local/bin`，不需要 sudo。将该目录加入 Shell 的 PATH，即可在新终端中使用。包管理器和上面的脚本默认安装最新稳定版；页面顶部的版本选择用于下载指定版本。

<details><summary>安装指定版本或自定义目录</summary>
<pre><code>STEWARD_INSTALL_DIR="$HOME/bin" sh install-steward.sh v<span data-release-version>0.1.0</span></code></pre>
<p>再次运行脚本即可升级。卸载时删除安装目录中的 steward 文件。</p>
</details>

<span id="verify"></span>

## 验证安装

发布版已包含 Web 控制台和数据库，无需安装 Go、Node.js 或单独的数据库服务。

```sh
steward --version
steward --help
```

版本命令应输出 `steward version` 和已安装版本号。若提示找不到命令，确认安装目录在 PATH 中，并重新打开终端。

<span id="binary"></span>

## 校验下载

手动下载时，比较下载卡片的 SHA-256，或使用所选版本的 <a data-release-checksums href="https://github.com/loomx-ai/steward/releases/download/v0.1.0/checksums.txt">checksums.txt</a>。可分别运行：

```sh
# macOS
shasum -a 256 下载的文件
# Linux
sha256sum 下载的文件
```

```powershell
# Windows PowerShell
Get-FileHash -Algorithm SHA256 下载的文件
```

<div data-latest-signature>

最新稳定版还提供 [GPG 签名的校验文件](https://loomx-ai.github.io/packages/steward/checksums.txt.asc)和[签名公钥](https://loomx-ai.github.io/packages/steward/gpg.key)。公钥指纹：`5DEF 17A8 BE9D 07D3 BED9 DED9 B928 3FAF 5DB1 944D`。

<details><summary>使用 GPG 验证发布方签名</summary>

```sh
curl -fsSLO https://loomx-ai.github.io/packages/steward/gpg.key
curl -fsSLO https://loomx-ai.github.io/packages/steward/checksums.txt
curl -fsSLO https://loomx-ai.github.io/packages/steward/checksums.txt.asc
gpg --show-keys --with-fingerprint gpg.key
gpg --import gpg.key
gpg --verify checksums.txt.asc checksums.txt
```

确认指纹与上方一致，再比较文件的 SHA-256。APT 会验证软件源签名；RPM 会验证软件源和包签名。

</details>
</div>

<details><summary>解压 macOS / Linux 二进制</summary>

对下载的 `.tar.gz` 文件解压，将其中的 `steward` 放入 PATH 下的目录：

```sh
mkdir -p "$HOME/.local/bin"
install -m 755 steward "$HOME/.local/bin/steward"
export PATH="$HOME/.local/bin:$PATH"
```

macOS 手动下载的二进制尚未经过 Apple 公证。保留 Gatekeeper，并按系统提示确认打开。

</details>

<span id="upgrade"></span>

## 升级和卸载

升级前停止 Steward，并备份工作目录下的 `.steward/`，其中包含数据库和凭证密钥。始终保留原密钥。

| 安装方式 | 升级 | 卸载 |
| --- | --- | --- |
| Homebrew | `brew update && brew upgrade steward` | `brew uninstall steward` |
| APT | `sudo apt-get update && sudo apt-get install --only-upgrade steward` | `sudo apt-get remove steward` |
| DNF | `sudo dnf upgrade steward` | `sudo dnf remove steward` |
| Scoop | `scoop update steward` | `scoop uninstall steward` |
| 安装脚本 / 二进制 | 重新安装新版本 | 删除可执行文件 |

卸载程序会保留工作目录的数据。若需要回退版本，应同时恢复与该版本匹配的数据库备份。

<span id="source"></span>

## 从源码构建

<details><summary>面向开发者的构建步骤</summary>

需要 Git、Go 1.26+、Node.js 22+、npm、make 和 C 编译器（macOS 使用 Xcode Command Line Tools，Linux 使用 GCC，Windows 使用 MinGW-w64）。

```sh
git clone https://github.com/loomx-ai/steward.git
cd steward
make install
make build
./bin/steward --version
```

Windows 使用 Git Bash。单独执行 `go install` 不包含 Web 控制台，完整应用请使用 `make build`。

</details>

<span id="start"></span>

## 启动 Steward

创建一个固定工作目录，并始终从这里启动：

```sh
mkdir -p "$HOME/steward-data"
cd "$HOME/steward-data"
steward server start
```

打开 [http://127.0.0.1:8585](http://127.0.0.1:8585)。本机访问无需登录。Windows 用户先在 PowerShell 中创建并进入工作目录，再运行相同的启动命令。使用 Ctrl+C 停止服务；Windows 的 `steward server stop` 会强制结束进程。

[连接第一个云账号 →](./quick-start.md) · [网络访问与备份 →](./deployment.md)
