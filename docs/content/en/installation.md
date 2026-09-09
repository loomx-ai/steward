---
title: "Install Steward"
description: "Install with Homebrew, Scoop, a script, Linux packages, or a standalone binary."
navTitle: "Installation"
---

Release binaries include the web console and database migrations. Go, Node.js, and a separate database server are not required.

| Platform | Architectures | Installation |
| --- | --- | --- |
| macOS 14+ | Apple Silicon / Intel | Homebrew, script, tar.gz |
| Linux | ARM64 / x86-64 | Homebrew, script, deb, rpm, tar.gz |
| Windows | x86-64 | Scoop, ZIP |

Linux binaries are statically linked with musl, so the same archive works on glibc and musl distributions. Install your distribution's CA certificate package for cloud HTTPS connections. On Windows ARM64, use the Linux ARM64 build in WSL2.

<span id="homebrew"></span>

## Homebrew

On macOS or Linux:

```bash
brew install loomx-ai/tap/steward
steward --version
```

Upgrade with `brew update && brew upgrade steward`; uninstall with `brew uninstall steward`. The tap installs the matching release binary and shell completions.

<span id="script"></span>

## Installation script

On macOS or Linux:

```bash
curl -fsSL https://github.com/loomx-ai/steward/releases/latest/download/install.sh -o install-steward.sh
sh install-steward.sh
export PATH="$HOME/.local/bin:$PATH"
```

The script detects your system and CPU, checks the archive's SHA-256, and installs to `~/.local/bin`. It does not invoke sudo or change shell configuration. Add that directory to your shell's PATH for future terminals. Run the script again to upgrade.

For a pinned version or another writable install directory, replace `v1.2.3` with a published tag:

```bash
STEWARD_INSTALL_DIR="$HOME/bin" sh install-steward.sh v1.2.3
```

Remove the installed `steward` executable to uninstall.

<span id="linux"></span>

## Debian, Ubuntu, Fedora, and RHEL

Download the matching `linux_amd64` or `linux_arm64` package and `checksums.txt` from [GitHub Releases](https://github.com/loomx-ai/steward/releases). Verify the downloaded file as shown below, then install it. These are local packages; an APT or RPM repository is not required.

```bash
# Substitute the version and architecture of your downloaded package.
sudo apt install ./steward_1.2.3_linux_amd64.deb
# Or, on Fedora / RHEL:
sudo dnf install ./steward_1.2.3_linux_amd64.rpm
```

Install a newer package the same way to upgrade. Uninstall with `sudo apt remove steward` or `sudo dnf remove steward`. Installation does not start a background service.

<span id="windows"></span>

## Windows / Scoop

In PowerShell with Scoop installed:

```powershell
scoop bucket add loomx-ai https://github.com/loomx-ai/homebrew-tap
scoop install loomx-ai/steward
steward --version
```

Upgrade with `scoop update steward`; uninstall with `scoop uninstall steward`.

You can also download `steward_<version>_windows_amd64.zip`, check it with `Get-FileHash -Algorithm SHA256 <file>`, compare the hash with `checksums.txt`, and extract `steward.exe` into a directory on PATH. Start it from PowerShell with `steward server start`. Use Ctrl+C in its terminal for graceful shutdown; `steward server stop` forcibly terminates the process on Windows.

<span id="binary"></span>

## Download a binary

Each [GitHub Release](https://github.com/loomx-ai/steward/releases) includes platform archives, Linux packages, and `checksums.txt`. Archive filenames use `darwin` for macOS, `amd64` for Intel/AMD x86-64, and `arm64` for Apple Silicon / ARM64.

On Linux, from the directory containing the downloaded files:

```bash
sha256sum --check --ignore-missing checksums.txt
tar -xzf steward_1.2.3_linux_amd64.tar.gz
mkdir -p "$HOME/.local/bin"
install -m 755 steward "$HOME/.local/bin/steward"
```

On macOS, use `shasum -a 256 <archive>` and compare the result with the matching line in `checksums.txt`, then extract the matching `darwin` archive. Ensure the install directory is on PATH. macOS binaries are not currently Apple-notarized; keep Gatekeeper enabled and follow any system prompt before opening a manually downloaded binary.

<span id="source"></span>

## Build from source

Install Git, Go 1.26+, Node.js 22+, npm, make, and a C compiler for SQLite (Xcode Command Line Tools on macOS, GCC on Linux, or MinGW-w64 on Windows).

```bash
git clone https://github.com/loomx-ai/steward.git
cd steward
make install
make build
./bin/steward --version
./bin/steward server start
```

Use Git Bash with make and MinGW-w64 on Windows. Plain `go install` does not include the web console; use `make build` for the complete application.

<span id="start"></span>

## Start and keep your data

```bash
mkdir -p "$HOME/steward-data"
cd "$HOME/steward-data"
steward server start
```

Open [http://127.0.0.1:8585](http://127.0.0.1:8585). No login is required for local access. Always restart from the same working directory: the database and credential key live under `.steward/` there. Stop the server and back up that directory before upgrading. Uninstalling the executable or package leaves this data in place.

[Connect your first cloud account →](./quick-start.md) · [Network access and backups →](./deployment.md)
