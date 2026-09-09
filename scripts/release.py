#!/usr/bin/env python3
"""Package native builds and generate checksum-pinned package manifests."""

import argparse
import hashlib
import json
from pathlib import Path
import re
import tarfile
import zipfile

ROOT = Path(__file__).resolve().parent.parent
REPOSITORY = "https://github.com/loomx-ai/steward"
TARGETS = ("darwin_amd64", "darwin_arm64", "linux_amd64", "linux_arm64", "windows_amd64")


def version_arg(value):
    value = value.removeprefix("v")
    if not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)(?:-[0-9A-Za-z]+(?:[.-][0-9A-Za-z]+)*)?", value):
        raise argparse.ArgumentTypeError("expected a version such as v1.2.3 or v1.2.3-rc.1")
    return value


def archive_name(version, target):
    extension = "zip" if target.startswith("windows_") else "tar.gz"
    return f"steward_{version}_{target}.{extension}"


def package(version, target, binary, output):
    output.mkdir(parents=True, exist_ok=True)
    archive = output / archive_name(version, target)
    binary_name = "steward.exe" if target.startswith("windows_") else "steward"
    files = [(binary, binary_name)] + [(ROOT / name, name) for name in ("LICENSE", "README.md", "README.zh-CN.md")]
    if target.startswith("windows_"):
        with zipfile.ZipFile(archive, "w", zipfile.ZIP_DEFLATED) as bundle:
            for source, name in files:
                bundle.write(source, name)
    else:
        with tarfile.open(archive, "w:gz") as bundle:
            for source, name in files:
                info = bundle.gettarinfo(str(source), name)
                info.mode = 0o755 if name == binary_name else 0o644
                info.uid = info.gid = 0
                info.uname = info.gname = "root"
                with source.open("rb") as data:
                    bundle.addfile(info, data)
    return archive


def finalize(version, output):
    names = [archive_name(version, target) for target in TARGETS]
    names += [f"steward_{version}_linux_{arch}.{kind}" for arch in ("amd64", "arm64") for kind in ("deb", "rpm")]
    # Refuse to publish a partial platform set.
    for name in names:
        if not (output / name).is_file():
            raise FileNotFoundError(output / name)
    hashes = {name: hashlib.sha256((output / name).read_bytes()).hexdigest() for name in names}
    base = f"{REPOSITORY}/releases/download/v{version}"
    formula = [
        "class Steward < Formula",
        '  desc "Discover, understand, and safely clean up cloud resources"',
        '  homepage "https://loomx.ai/steward"',
        f'  version "{version}"',
        '  license "Apache-2.0"',
    ]
    for os_name, brew_os in (("darwin", "macos"), ("linux", "linux")):
        formula.append(f"  on_{brew_os} do")
        if os_name == "darwin":
            formula.append("    depends_on macos: :sonoma")
        else:
            formula.append('    depends_on "ca-certificates"')
        for arch, brew_arch in (("arm64", "arm"), ("amd64", "intel")):
            name = archive_name(version, f"{os_name}_{arch}")
            formula += [f"    on_{brew_arch} do", f'      url "{base}/{name}"', f'      sha256 "{hashes[name]}"', "    end"]
        formula.append("  end")
    formula += [
        "  def install",
        '    bin.install "steward"',
        '    generate_completions_from_executable(bin/"steward", "completion")',
        "  end",
        "  test do",
        '    assert_match version.to_s, shell_output("#{bin}/steward --version")',
        "  end",
        "end",
    ]
    (output / "steward.rb").write_text("\n".join(formula) + "\n")
    windows = archive_name(version, "windows_amd64")
    scoop = {
        "version": version,
        "description": "Discover, understand, and safely clean up cloud resources",
        "homepage": "https://loomx.ai/steward",
        "license": "Apache-2.0",
        "architecture": {"64bit": {"url": f"{base}/{windows}", "hash": hashes[windows]}},
        "bin": "steward.exe",
        "checkver": {"github": REPOSITORY},
        "autoupdate": {"architecture": {"64bit": {"url": f"{REPOSITORY}/releases/download/v$version/steward_$version_windows_amd64.zip"}}},
    }
    (output / "steward.json").write_text(json.dumps(scoop, indent=2) + "\n")
    for name in ("steward.rb", "steward.json", "install.sh"):
        hashes[name] = hashlib.sha256((output / name).read_bytes()).hexdigest()
    (output / "checksums.txt").write_text("".join(f"{hashes[name]}  {name}\n" for name in sorted(hashes)))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("command", choices=("package", "finalize"))
    parser.add_argument("--version", required=True, type=version_arg)
    parser.add_argument("--output", type=Path, default=ROOT / "dist")
    parser.add_argument("--target", choices=TARGETS)
    parser.add_argument("--binary", type=Path)
    args = parser.parse_args()
    if args.command == "package":
        if args.target is None or args.binary is None:
            parser.error("package requires --target and --binary")
        print(package(args.version, args.target, args.binary, args.output))
    else:
        finalize(args.version, args.output)


if __name__ == "__main__":
    main()
