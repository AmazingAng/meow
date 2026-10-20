#!/usr/bin/env python3
"""Generate the binary Homebrew formula from a published release's checksums."""
import argparse
from pathlib import Path
import re
import urllib.request


def formula(version, checksums):
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', version):
        raise ValueError('Expected a stable version such as 0.1.0')
    entries = {}
    for line in checksums.splitlines():
        parts = line.split()
        if len(parts) != 2:
            raise ValueError('Invalid checksum manifest line')
        digest, name = parts
        if name in entries or not re.fullmatch(r'[0-9a-f]{64}', digest):
            raise ValueError('Duplicate archive or invalid SHA-256')
        entries[name] = digest
    lines = ['class Meow < Formula', '  desc "Push notifications for AI agents"',
             '  homepage "https://github.com/AmazingAng/meow"', f'  version "{version}"', '']
    for os_name, platform in [('macos', 'darwin'), ('linux', 'linux')]:
        lines += [f'  on_{os_name} do']
        for cpu, architecture in [('arm', 'arm64'), ('intel', 'amd64')]:
            archive = f'meow_{version}_{platform}_{architecture}.tar.gz'
            if archive not in entries:
                raise ValueError('Missing release archive: ' + archive)
            lines += [f'    on_{cpu} do',
                      f'      url "https://github.com/AmazingAng/meow/releases/download/v{version}/{archive}"',
                      f'      sha256 "{entries[archive]}"', '    end']
        lines += ['  end', '']
    lines += ['  def install', '    bin.install "meow"', '  end', '', '  test do',
              f'    assert_match "{version}", shell_output("#{{bin}}/meow --version")', '  end', 'end', '']
    return '\n'.join(lines)


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('version')
    parser.add_argument('--checksums', type=Path, help='Use a previously downloaded checksum manifest')
    parser.add_argument('--output', type=Path, required=True, help='Path to Formula/meow.rb in the tap checkout')
    args = parser.parse_args()
    version = args.version.removeprefix('v')
    if not re.fullmatch(r'[0-9]+\.[0-9]+\.[0-9]+', version):
        parser.error('Version must look like 0.1.0 or v0.1.0')
    if args.checksums:
        checksums = args.checksums.read_text()
    else:
        url = f'https://github.com/AmazingAng/meow/releases/download/v{version}/checksums.txt'
        with urllib.request.urlopen(url, timeout=60) as response:
            checksums = response.read().decode()
    content = formula(version, checksums)
    args.output.parent.mkdir(parents=True, exist_ok=True)
    args.output.write_text(content)
    print('Updated', args.output)


if __name__ == '__main__':
    main()
