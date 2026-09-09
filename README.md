<p align="center">
  <a href="https://loomx.ai/steward"><img src="web/public/brand/steward-symbol.svg" alt="Steward" width="72" height="72"></a>
</p>

<h1 align="center">steward</h1>

<p align="center">
  <strong>See your cloud. Take control of cleanup.</strong>
</p>

<p align="center">
  <a href="https://github.com/loomx-ai/steward/releases/latest"><img src="https://img.shields.io/github/v/release/loomx-ai/steward?color=2D7BFE" alt="Latest release"></a>
  <a href="https://github.com/loomx-ai/steward/actions/workflows/test.yml"><img src="https://github.com/loomx-ai/steward/actions/workflows/test.yml/badge.svg" alt="Tests"></a>
  <a href="LICENSE"><img src="https://img.shields.io/badge/license-Apache--2.0-9B51E0.svg" alt="Apache-2.0 license"></a>
</p>

<p align="center">
  <a href="#get-started">Get started</a> ·
  <a href="https://loomx.ai/steward/docs/latest/en/">Documentation</a> ·
  <a href="https://github.com/loomx-ai/steward/releases">Releases</a> ·
  <a href="https://loomx.ai/steward">Website</a>
</p>

<p align="center">
  English / <a href="README.zh-CN.md">简体中文</a>
</p>

Steward is an open-source tool for cloud inventory, resource relationships, and cleanup. Connect your cloud accounts to see what is running, understand what depends on it, and review the impact before deleting resources.

<p align="center">
  <a href="https://loomx.ai/steward/docs/latest/en/alicloud/">Alibaba Cloud</a> &nbsp;·&nbsp;
  <a href="https://loomx.ai/steward/docs/latest/en/aws/">AWS</a> &nbsp;·&nbsp;
  <a href="https://loomx.ai/steward/docs/latest/en/gcp/">Google Cloud</a> &nbsp;·&nbsp;
  <a href="https://loomx.ai/steward/docs/latest/en/azure/">Microsoft Azure</a>
</p>

<p align="center">
  <a href="docs/assets/relationships-en.png"><img src="docs/assets/relationships-en.png" alt="Steward resource panorama showing instances, a load balancer, a security group, and their relationships in a sample Alibaba Cloud VPC" width="960"></a>
  <br>
  <sub>Resource panorama · Alibaba Cloud sample data</sub>
</p>

## Why Steward?

- **Cloud inventory.** Scan the accounts and regions you choose. Find resources by name, ID, type, and properties without switching between cloud consoles.
- **Visual relationships.** Explore regions, networks, and resource dependencies. See which resources share a load balancer, subnet, or security group.
- **Findings in context.** Review governance findings alongside resource details and scan results to investigate what needs attention.
- **Cleanup you can review.** Inspect targets, dependencies, blockers, and retained resources before confirming execution. Follow progress and check results in the audit trail.

## Get started

### 1. Install and run

On **macOS 14+ or Linux**, with Homebrew:

```sh
brew install loomx-ai/tap/steward
mkdir -p "$HOME/steward-data"
cd "$HOME/steward-data"
steward server start
```

<details>
<summary><strong>Windows — install with Scoop</strong></summary>

Run in PowerShell on Windows x86-64:

```powershell
scoop bucket add loomx-ai https://github.com/loomx-ai/homebrew-tap
scoop install loomx-ai/steward
New-Item -ItemType Directory -Force "$HOME/steward-data" | Out-Null
Set-Location "$HOME/steward-data"
steward server start
```

</details>

Prefer another method? See the [installation guide](https://loomx.ai/steward/docs/latest/en/installation/) for APT, DNF, the installation script, and direct downloads.

Open **[localhost:8585](http://127.0.0.1:8585)**. Local use requires no account or login.

### 2. Connect and explore

1. Open the user menu → **Settings → Cloud connections**. Choose a provider and add your credentials.
2. Select the connection, open **Scans**, and scan one region you use.
3. Open **Resources** to find a known resource, then explore its connections in **Resource panorama**.

Read permissions are enough for your first inventory. Follow the [first inventory tutorial](https://loomx.ai/steward/docs/latest/en/tutorials/first-inventory/) for screenshots and expected results.

Keep using the same working directory on restart. Before upgrading, stop Steward and back up its `.steward/` folder, keeping the database and original credential key together. For shared or remote access, follow the [deployment guide](https://loomx.ai/steward/docs/latest/en/deployment/).

## Documentation

| I want to… | Start here |
| --- | --- |
| Install Steward on my system | [Installation](https://loomx.ai/steward/docs/latest/en/installation/) |
| Connect a cloud account | [Connections and credentials](https://loomx.ai/steward/docs/latest/en/connections/) |
| Understand resource dependencies | [Resource relationships](https://loomx.ai/steward/docs/latest/en/topology/) |
| Review and clean up resources | [Cleanup guide](https://loomx.ai/steward/docs/latest/en/cleanup/) |
| Set up network access and backups | [Deployment](https://loomx.ai/steward/docs/latest/en/deployment/) |

[Browse all documentation →](https://loomx.ai/steward/docs/latest/en/)

## Contributing

Bug reports, cloud provider improvements, and documentation contributions are welcome. [Open an issue](https://github.com/loomx-ai/steward/issues) to report a problem or discuss a substantial change before starting work.

<details>
<summary><strong>Develop Steward locally</strong></summary>

Requires Git, Go 1.26+, Node.js 22+, npm, make, and a C compiler.

```sh
git clone https://github.com/loomx-ai/steward.git
cd steward
make install
make dev
```

Open [localhost:5858](http://127.0.0.1:5858) for development with hot reload. Use `make build` to produce the complete application in `bin/steward`.

Run the checks before submitting changes, and include tests for changed behavior:

```sh
make test
make lint
make build
```

For documentation changes, see the [documentation contributor guide](docs/README.md). Release maintainers can refer to [RELEASING.md](RELEASING.md).

</details>

## License

[Apache License 2.0](LICENSE) · Copyright 2026 LoomX
