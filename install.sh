#!/bin/sh
# Install a checksum-verified release on macOS or Linux. No sudo is invoked.
set -eu

main() {
    version=${1:-latest}
    install_dir=${STEWARD_INSTALL_DIR:-"$HOME/.local/bin"}
    repository=https://github.com/loomx-ai/steward
    for tool in curl tar uname mktemp awk; do
        command -v "$tool" >/dev/null 2>&1 || { echo "Required command not found: $tool" >&2; exit 1; }
    done
    case $(uname -s) in
        Darwin) os=darwin ;;
        Linux) os=linux ;;
        *) echo "Use the Windows ZIP or Scoop package from $repository/releases." >&2; exit 1 ;;
    esac
    case $(uname -m) in
        x86_64|amd64) arch=amd64 ;;
        arm64|aarch64) arch=arm64 ;;
        *) echo "Unsupported architecture: $(uname -m)" >&2; exit 1 ;;
    esac
    if [ "$version" = latest ]; then
        release_url=$(curl --proto '=https' --tlsv1.2 -fsSL -o /dev/null -w '%{url_effective}' "$repository/releases/latest")
        version=${release_url##*/}
    fi
    version=${version#v}
    # Keep release URLs and local paths confined to version-shaped input.
    case $version in
        *[!0-9A-Za-z.-]*) echo "Invalid version: $version" >&2; exit 1 ;;
    esac
    if ! printf '%s\n' "$version" | LC_ALL=C awk '/^[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z]+([.-][0-9A-Za-z]+)*)?$/ {ok=1} END {exit !ok}'; then
        echo "Invalid version: $version (expected v1.2.3 or v1.2.3-rc.1)" >&2
        exit 1
    fi
    archive=steward_${version}_${os}_${arch}.tar.gz
    base=$repository/releases/download/v$version
    temp_dir=$(mktemp -d)
    trap 'rm -rf "$temp_dir"' EXIT
    trap 'exit 1' HUP INT TERM
    curl --proto '=https' --tlsv1.2 -fsSL "$base/$archive" -o "$temp_dir/$archive"
    curl --proto '=https' --tlsv1.2 -fsSL "$base/checksums.txt" -o "$temp_dir/checksums.txt"
    expected=$(awk -v name="$archive" '$2 == name { print $1 }' "$temp_dir/checksums.txt")
    if command -v sha256sum >/dev/null 2>&1; then
        actual=$(sha256sum "$temp_dir/$archive")
    elif command -v shasum >/dev/null 2>&1; then
        actual=$(shasum -a 256 "$temp_dir/$archive")
    else
        echo "SHA-256 verification requires sha256sum or shasum." >&2
        exit 1
    fi
    actual=${actual%% *}
    if [ -z "$expected" ] || [ "$actual" != "$expected" ]; then
        echo "SHA-256 verification failed for $archive; nothing was installed." >&2
        exit 1
    fi
    tar -xzf "$temp_dir/$archive" -C "$temp_dir" steward
    mkdir -p "$install_dir"
    # Stage beside the destination so replacement is atomic, even during upgrades.
    staged=$(mktemp "$install_dir/.steward.XXXXXX")
    trap 'rm -rf "$temp_dir"; rm -f "$staged"' EXIT
    cp "$temp_dir/steward" "$staged"
    chmod 755 "$staged"
    mv -f "$staged" "$install_dir/steward"
    echo "Installed Steward $version to $install_dir/steward"
    case :$PATH: in
        *:"$install_dir":*) ;;
        *) printf 'Add %s to your PATH.\n' "$install_dir" ;;
    esac
    echo "Run: steward server start"
}

# Execute only after the complete script has been received when piped to sh.
main "$@"
