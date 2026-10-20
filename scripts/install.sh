#!/bin/sh
# Install the published Meow CLI. No Go, GitHub login or sudo is required.
set -eu

main() {
    repository=AmazingAng/meow
    version=latest
    install_dir=${HOME:?HOME is not set}/.local/bin
    server=
    while [ "$#" -gt 0 ]; do
        case "$1" in
            --version|--install-dir|--server)
                [ "$#" -ge 2 ] || { echo "Missing value for $1" >&2; exit 1; }
                case "$1" in
                    --version) version=${2#v} ;;
                    --install-dir) install_dir=$2 ;;
                    --server) server=$2 ;;
                esac
                shift 2 ;;
            --help|-h)
                echo 'Usage: install.sh [--version VERSION] [--install-dir DIR] [--server URL]'
                echo 'Default directory: ~/.local/bin. --server starts guided setup after installation.'
                exit 0 ;;
            *) echo "Unknown option: $1" >&2; exit 1 ;;
        esac
    done
    if [ "$version" != latest ]; then
        printf '%s\n' "$version" | awk '/^[0-9]+\.[0-9]+\.[0-9]+$/ {ok=1} END {exit !ok}' || {
            echo 'Version must look like 0.1.0 or v0.1.0.' >&2; exit 1;
        }
    fi
    case "$(uname -s)" in
        Darwin) platform=darwin ;;
        Linux) platform=linux ;;
        *) echo 'Meow supports macOS and Linux.' >&2; exit 1 ;;
    esac
    case "$(uname -m)" in
        arm64|aarch64) architecture=arm64 ;;
        x86_64|amd64) architecture=amd64 ;;
        *) echo 'Meow requires an ARM64 or x86-64 computer.' >&2; exit 1 ;;
    esac
    for tool in curl tar awk mktemp; do
        command -v "$tool" >/dev/null 2>&1 || { echo "Required command not found: $tool" >&2; exit 1; }
    done
    if command -v sha256sum >/dev/null 2>&1; then
        hash_tool=sha256sum
    elif command -v shasum >/dev/null 2>&1; then
        hash_tool=shasum
    else
        echo 'Install sha256sum or shasum to verify the download.' >&2; exit 1
    fi
    work_dir=$(mktemp -d "${TMPDIR:-/tmp}/meow-install.XXXXXXXX")
    staged=
    trap 'rm -rf "$work_dir"; if [ -n "$staged" ]; then rm -f "$staged"; fi' EXIT
    trap 'exit 1' HUP INT TERM
    base=https://github.com/$repository/releases
    if [ "$version" = latest ]; then
        checksum_url=$base/latest/download/checksums.txt
    else
        checksum_url=$base/download/v$version/checksums.txt
    fi
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
        --connect-timeout 15 --max-time 120 "$checksum_url" -o "$work_dir/checksums.txt"
    # Resolve latest from the manifest, then pin the archive to that same release.
    suffix=_${platform}_${architecture}.tar.gz
    record=$(awk -v suffix="$suffix" -v version="$version" '
        NF == 2 && $2 ~ /^meow_[0-9]+\.[0-9]+\.[0-9]+_/ &&
        substr($2, length($2)-length(suffix)+1) == suffix {
            name=$2; sub(/^meow_/, "", name); sub(suffix "$", "", name)
            if (version == "latest" || name == version) { print $1, $2; count++ }
        }
        END {if (count != 1) exit 1}
    ' "$work_dir/checksums.txt") || { echo 'Release has no unique matching archive.' >&2; exit 1; }
    expected=${record%% *}
    archive=${record#* }
    printf '%s\n' "$expected" | awk 'length($0)==64 && /^[0-9a-fA-F]+$/ {ok=1} END {exit !ok}' || {
        echo 'Invalid release checksum.' >&2; exit 1;
    }
    version=${archive#meow_}
    version=${version%"$suffix"}
    printf '%s\n' "$version" | awk '/^[0-9]+\.[0-9]+\.[0-9]+$/ {ok=1} END {exit !ok}' || {
        echo 'Invalid release version.' >&2; exit 1;
    }
    echo "Downloading Meow $version ($platform/$architecture)…"
    curl --fail --silent --show-error --location --proto '=https' --proto-redir '=https' \
        --connect-timeout 15 --max-time 300 "$base/download/v$version/$archive" -o "$work_dir/archive.tar.gz"
    if [ "$hash_tool" = sha256sum ]; then
        actual=$(sha256sum "$work_dir/archive.tar.gz" | awk '{print $1}')
    else
        actual=$(shasum -a 256 "$work_dir/archive.tar.gz" | awk '{print $1}')
    fi
    [ "$actual" = "$expected" ] || { echo 'Checksum mismatch; existing installation was not changed.' >&2; exit 1; }
    # Only extract the single regular executable; never unpack archive paths into HOME.
    tar -tvzf "$work_dir/archive.tar.gz" meow | awk '
        NR == 1 && substr($0,1,1) == "-" {regular=1}
        END {exit !(NR == 1 && regular)}
    ' || { echo 'Release must contain one regular meow executable.' >&2; exit 1; }
    tar -xzf "$work_dir/archive.tar.gz" -C "$work_dir" meow
    [ -f "$work_dir/meow" ] && [ ! -L "$work_dir/meow" ] || { echo 'Invalid executable.' >&2; exit 1; }
    mkdir -p "$install_dir"
    install_dir=$(cd "$install_dir" && pwd -P)
    [ ! -d "$install_dir/meow" ] || { echo 'Installation target is a directory.' >&2; exit 1; }
    staged=$(mktemp "$install_dir/.meow.XXXXXXXX")
    cp "$work_dir/meow" "$staged"
    chmod 755 "$staged"
    mv -f "$staged" "$install_dir/meow"
    staged=
    echo "Installed $install_dir/meow"
    "$install_dir/meow" --version
    case ":$PATH:" in
        *":$install_dir:"*) ;;
        *) printf '\nTo use meow by name in this terminal, run:\n  export PATH="%s:$PATH"\n' "$install_dir"
           echo 'Add that line to your shell profile to keep it in new terminals.' ;;
    esac
    if [ -n "$server" ]; then
        # The installer can be piped to sh; setup needs the terminal for its key prompt.
        if ! ( : </dev/tty ) 2>/dev/null; then
            echo 'Installation finished. Run meow setup --server YOUR_SERVER in an interactive terminal.' >&2
            exit 1
        fi
        rm -rf "$work_dir"
        trap - EXIT HUP INT TERM
        exec "$install_dir/meow" setup --server "$server" </dev/tty
    fi
    echo 'Next: run meow setup --server YOUR_SERVER (use the address shown in the iPhone app).'
}

main "$@"
