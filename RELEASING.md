# Releasing Steward

The `Release` GitHub Actions workflow builds native CGO binaries for macOS
(Intel and Apple Silicon), Linux (amd64 and arm64), and Windows (amd64).
Every binary embeds the web console and SQL migrations. Linux uses static musl
linking; macOS targets 14+. Release tooling does not require GoReleaser Pro.

## First-time setup

1. Allow GitHub Actions to write repository contents for the release job. The
   workflow scopes `contents: write` to that job; build jobs are read-only.
2. Create the public `loomx-ai/homebrew-tap` repository with an initial README
   and a default branch. The same repository hosts `Formula/steward.rb` for
   Homebrew and `bucket/steward.json` for Scoop.
3. The tap's `Sync packages` workflow downloads the latest stable release's
   `steward.rb`, `steward.json`, and `checksums.txt`, verifies both manifests, and
   commits them using the tap's own `GITHUB_TOKEN`. It runs every 15 minutes and
   supports manual dispatch. GitHub schedules may be delayed.

No cross-repository token or deploy key is required. Deploy keys are disabled by
repository policy. After a stable release, dispatch `sync.yml` in the tap to make
it available immediately, or let the scheduled sync pick it up:

```bash
gh workflow run sync.yml --repo loomx-ai/homebrew-tap
```

The sync always resolves the latest stable release and never advances the tap
to a prerelease. The signed APT/RPM repositories are maintained in `loomx-ai/packages`. Its
workflow verifies upstream checksums, signs package indexes and RPM packages,
tests installation on five Linux distributions, and deploys to GitHub Pages.
It checks hourly and refreshes expiring APT metadata weekly. Dispatch it after
a stable release for immediate availability:

```bash
gh workflow run publish.yml --repo loomx-ai/packages
```

The package repository also publishes `releases.json` for the documentation
version picker, plus a GPG signature for the latest release checksum file.
The installation page keeps working with its embedded version if this index
is temporarily unavailable. When releasing, update the fallback version and
executable links in both `docs/content/*/installation.md` files after the assets
are published. Download integrity remains verified by the release tooling.

No Homebrew core, Scoop main, Winget, Chocolatey, or Snap registry submission
is performed.

## Preview a build

Run `Release` with `workflow_dispatch` on the intended branch. It builds and
smoke-tests all five targets, installs and tests the Linux deb packages, and
uploads a combined `release` Actions artifact containing raw executables, archives, deb/rpm
packages, `checksums.txt`, `install.sh`, and the Homebrew/Scoop manifests.
Manual runs never publish or update the tap. Branch builds have version
`0.0.0-dev.<commit>`; their manifests are previews, not public download URLs.

Packaging changes also run this build on pull requests. Each executable is
started in an empty directory, without cloud credentials, to verify SQLite
initialization, embedded HTML/JavaScript, version reporting, and server status/stop.

## Publish a version

Choose an unused SemVer tag, such as `v1.2.3` or `v1.2.3-rc.1`, on the reviewed
commit and push that tag. A tag push runs the full reusable test workflow,
builds all platforms, and publishes only after both succeed:

```bash
git tag -a v1.2.3 -m 'Steward v1.2.3'
git push origin v1.2.3
```

Prerelease tags create GitHub prereleases and never update the stable tap.
The tap sync updates Homebrew and Scoop after GitHub publication. An existing
release is not overwritten; rerun the tap sync workflow for a sync failure.
If GitHub publication partially fails, inspect the release before retrying.
Do not move a published tag or replace released artifacts; publish a new version.

The tar.gz/ZIP archives contain `steward`/`steward.exe`, the license, and both
READMEs. The standalone binary needs no external migrations or web directory.
Raw downloads are named `steward_<version>_<os>_<arch>` (with `.exe` on Windows).
Checksums cover every executable, archive, package, installer, and manifest. They detect
download corruption; they are not a separate code-signing identity. macOS
binaries are not yet Apple-notarized. No installer disables Gatekeeper.

## Local checks

```bash
make install
make build VERSION=0.0.0-test
python3 scripts/smoke-release.py bin/steward 0.0.0-test
python3 -m unittest discover -s scripts -p 'test_release.py'
python3 -m unittest discover -s scripts -p 'test_install.py'
node docs/check.mjs
```

To package one native build:

```bash
python3 scripts/release.py package --version 0.0.0-test \
  --target darwin_arm64 --binary bin/steward
```

`packaging/nfpm.yaml` is used with nFPM 2.47.0 on Linux. Set `VERSION` and
`GOARCH` and provide `bin/steward`. `scripts/release.py finalize` refuses missing
platform executables, archives, or Linux packages before generating manifests and checksums.

For an older release with archives only, prepare byte-identical executables from
its verified archives. This does not rebuild code or replace any existing asset:

```bash
gh release download v0.1.0 --repo loomx-ai/steward --dir /tmp/steward-original \
  --pattern '*.tar.gz' --pattern '*.zip' --pattern checksums.txt
python3 scripts/release.py prepare-binaries --version 0.1.0 \
  --source /tmp/steward-original --output /tmp/steward-binaries
```

After reviewing the prepared files, upload only those additional executables.
Do not replace the original `checksums.txt`; the package index reads GitHub's
SHA-256 asset digests for additive executables. Future releases list raw binaries
in their original checksums. Publish the package index before the documentation.

Users must stop Steward and back up the database and original encryption key
before an upgrade. Packages only install files; they do not start a service or
delete the user's working directory. Database rollback needs a matching backup.
