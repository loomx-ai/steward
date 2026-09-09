---
title: "Install Steward"
description: "Choose your operating system. Install with a package manager or download a standalone binary."
navTitle: "Installation"
release:
  version: "0.1.0"
  index: "https://loomx-ai.github.io/packages/steward/releases.json"
---

<nav class="docs-os-links" aria-label="Operating system"><a href="#macos">macOS</a><a href="#windows">Windows</a><a href="#linux">Linux</a></nav>

<span id="macos"></span>

## macOS

Requires macOS 14 or later. Supports Apple Silicon and Intel Macs.

<span id="homebrew"></span>

### Homebrew

```sh
brew tap loomx-ai/tap
brew install loomx-ai/tap/steward
```

### Binary downloads

<div class="docs-downloads">
<div class="docs-download">
<a data-release-asset="steward_{version}_darwin_arm64.tar.gz" href="https://github.com/loomx-ai/steward/releases/download/v0.1.0/steward_0.1.0_darwin_arm64.tar.gz" aria-label="Download Steward darwin arm64"><strong>Apple Silicon</strong><span>Download ↓</span></a>
<p>v<span data-release-version>0.1.0</span> · tar.gz</p>
<details><summary>SHA-256</summary><code data-release-hash>932872e51c5a69022dc6476ac642536980dd48e9e43a224820364c22292c52e1</code></details>
</div>
<div class="docs-download">
<a data-release-asset="steward_{version}_darwin_amd64.tar.gz" href="https://github.com/loomx-ai/steward/releases/download/v0.1.0/steward_0.1.0_darwin_amd64.tar.gz" aria-label="Download Steward darwin amd64"><strong>Intel / AMD</strong><span>Download ↓</span></a>
<p>v<span data-release-version>0.1.0</span> · tar.gz</p>
<details><summary>SHA-256</summary><code data-release-hash>50d9ffc678448abe0db0b1859018cb53e7146df73860309896cbe16b2ea645f4</code></details>
</div>
</div>

<span id="windows"></span>

## Windows

### Scoop

Run in PowerShell with Scoop installed:

```powershell
scoop bucket add loomx-ai https://github.com/loomx-ai/homebrew-tap
scoop install loomx-ai/steward
```

### Binary downloads

<div class="docs-downloads">
<div class="docs-download">
<a data-release-asset="steward_{version}_windows_amd64.zip" href="https://github.com/loomx-ai/steward/releases/download/v0.1.0/steward_0.1.0_windows_amd64.zip" aria-label="Download Steward windows amd64"><strong>AMD64 / x86-64</strong><span>Download ↓</span></a>
<p>v<span data-release-version>0.1.0</span> · zip</p>
<details><summary>SHA-256</summary><code data-release-hash>f723f419836d3137ac03a71815eca9478a6e8145527568e65c232ee4fd56ae89</code></details>
</div>
</div>

Extract the ZIP, add the directory containing `steward.exe` to your user PATH, and open a new PowerShell window. On Windows ARM64, use the Linux ARM64 build in WSL2.

<span id="linux"></span>

## Linux

Supports AMD64 (x86-64) and ARM64 (aarch64). Repositories follow stable releases, so you can upgrade through your system package manager.

<div data-docs-tabs data-label="Linux installation method">
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

For Fedora, RHEL 9+, Rocky Linux 9+, and Amazon Linux 2023. Confirm the LoomX signing key import when prompted on first installation.

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

### Binary downloads

Statically linked binaries work on glibc and musl distributions. Install your distribution's CA certificates to connect to cloud services.

<div class="docs-downloads">
<div class="docs-download">
<a data-release-asset="steward_{version}_linux_arm64.tar.gz" href="https://github.com/loomx-ai/steward/releases/download/v0.1.0/steward_0.1.0_linux_arm64.tar.gz" aria-label="Download Steward linux arm64"><strong>ARM64</strong><span>Download ↓</span></a>
<p>v<span data-release-version>0.1.0</span> · tar.gz</p>
<details><summary>SHA-256</summary><code data-release-hash>33ba319bb58b18ffae5e053104fef04badb8abb72e98341bfc8ec85aff49d65e</code></details>
</div>
<div class="docs-download">
<a data-release-asset="steward_{version}_linux_amd64.tar.gz" href="https://github.com/loomx-ai/steward/releases/download/v0.1.0/steward_0.1.0_linux_amd64.tar.gz" aria-label="Download Steward linux amd64"><strong>AMD64 / x86-64</strong><span>Download ↓</span></a>
<p>v<span data-release-version>0.1.0</span> · tar.gz</p>
<details><summary>SHA-256</summary><code data-release-hash>c50d13b6343f53267bd068804e4c205511d83384c51d492afd51dbd63cbc4249</code></details>
</div>
</div>

You can also download <a data-release-notes href="https://github.com/loomx-ai/steward/releases/tag/v0.1.0">deb / rpm packages</a> from the selected release, verify their checksum, then install with `sudo apt install ./file.deb` or `sudo dnf install ./file.rpm`. Local package installation does not add a repository.

<span id="script"></span>

## Installation script

For macOS and Linux. The script detects your operating system and architecture and verifies SHA-256 before installing.

```sh
curl -fsSL https://github.com/loomx-ai/steward/releases/latest/download/install.sh -o install-steward.sh
sh install-steward.sh
export PATH="$HOME/.local/bin:$PATH"
```

The default location is `~/.local/bin`; sudo is not required. Add that directory to your shell's PATH for new terminals. Package managers and the script above install the latest stable release. The version picker at the top selects a version for direct downloads.

<details><summary>Install a specific version or choose a directory</summary>
<pre><code>STEWARD_INSTALL_DIR="$HOME/bin" sh install-steward.sh v<span data-release-version>0.1.0</span></code></pre>
<p>Run the script again to upgrade. Remove the steward executable from the install directory to uninstall.</p>
</details>

<span id="verify"></span>

## Verify installation

The release includes the web console and database. Go, Node.js, and a separate database server are not required.

```sh
steward --version
steward --help
```

The version command should print `steward version` followed by the installed version. If the command is not found, check that the installation directory is on PATH, then open a new terminal.

<span id="binary"></span>

## Verify downloads

For manual downloads, compare SHA-256 with the download card or the selected release's <a data-release-checksums href="https://github.com/loomx-ai/steward/releases/download/v0.1.0/checksums.txt">checksums.txt</a>. Use the command for your platform:

```sh
# macOS
shasum -a 256 downloaded-file
# Linux
sha256sum downloaded-file
```

```powershell
# Windows PowerShell
Get-FileHash -Algorithm SHA256 downloaded-file
```

<div data-latest-signature>

The latest stable release also has a [GPG signature for its checksums](https://loomx-ai.github.io/packages/steward/checksums.txt.asc) and a [public signing key](https://loomx-ai.github.io/packages/steward/gpg.key). Key fingerprint: `5DEF 17A8 BE9D 07D3 BED9 DED9 B928 3FAF 5DB1 944D`.

<details><summary>Verify the publisher's signature with GPG</summary>

```sh
curl -fsSLO https://loomx-ai.github.io/packages/steward/gpg.key
curl -fsSLO https://loomx-ai.github.io/packages/steward/checksums.txt
curl -fsSLO https://loomx-ai.github.io/packages/steward/checksums.txt.asc
gpg --show-keys --with-fingerprint gpg.key
gpg --import gpg.key
gpg --verify checksums.txt.asc checksums.txt
```

Confirm the fingerprint matches the one above, then compare the file's SHA-256. APT verifies repository signatures; RPM verifies both repository and package signatures.

</details>
</div>

<details><summary>Extract a macOS / Linux binary</summary>

Extract the downloaded `.tar.gz` archive, then place its `steward` executable in a directory on PATH:

```sh
mkdir -p "$HOME/.local/bin"
install -m 755 steward "$HOME/.local/bin/steward"
export PATH="$HOME/.local/bin:$PATH"
```

Manually downloaded macOS binaries are not yet Apple-notarized. Keep Gatekeeper enabled and follow the system prompt before opening them.

</details>

<span id="upgrade"></span>

## Upgrade and uninstall

Before upgrading, stop Steward and back up `.steward/` in your working directory. It contains your database and credential key; retain the original key.

| Installation | Upgrade | Uninstall |
| --- | --- | --- |
| Homebrew | `brew update && brew upgrade steward` | `brew uninstall steward` |
| APT | `sudo apt-get update && sudo apt-get install --only-upgrade steward` | `sudo apt-get remove steward` |
| DNF | `sudo dnf upgrade steward` | `sudo dnf remove steward` |
| Scoop | `scoop update steward` | `scoop uninstall steward` |
| Script / binary | Install a newer release | Remove the executable |

Uninstalling leaves your working data in place. To roll back, restore a database backup that matches the older version.

<span id="source"></span>

## Build from source

<details><summary>Developer build instructions</summary>

Requires Git, Go 1.26+, Node.js 22+, npm, make, and a C compiler: Xcode Command Line Tools on macOS, GCC on Linux, or MinGW-w64 on Windows.

```sh
git clone https://github.com/loomx-ai/steward.git
cd steward
make install
make build
./bin/steward --version
```

Use Git Bash on Windows. Plain `go install` does not include the web console; use `make build` for the complete application.

</details>

<span id="start"></span>

## Start Steward

Create a fixed working directory and always start from there:

```sh
mkdir -p "$HOME/steward-data"
cd "$HOME/steward-data"
steward server start
```

Open [http://127.0.0.1:8585](http://127.0.0.1:8585). Local access needs no login. On Windows, create and enter your working directory in PowerShell, then use the same start command. Use Ctrl+C for graceful shutdown; `steward server stop` forcibly terminates the process on Windows.

[Connect your first cloud account →](./quick-start.md) · [Network access and backups →](./deployment.md)
