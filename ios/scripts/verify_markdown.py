#!/usr/bin/env python3
"""Run the actual Foundation Markdown model and its regressions, without a simulator.

python3 -B ios/scripts/verify_markdown.py --output dist/markdown-check
"""
import argparse
from pathlib import Path
import shutil
import subprocess

ROOT = Path(__file__).resolve().parents[2]


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--output', required=True, type=Path)
    output = parser.parse_args().output.resolve()
    output.mkdir(parents=True, exist_ok=False)
    package = output / 'package'
    (package / 'Sources').mkdir(parents=True)
    (package / 'Tests').mkdir()
    (package / 'Package.swift').write_text('''// swift-tools-version: 6.0
import PackageDescription
let package = Package(name: "MeowMarkdown", platforms: [.macOS(.v14)], targets: [
    .target(name: "Meow", path: "Sources"),
    .testTarget(name: "MeowMarkdownTests", dependencies: ["Meow"], path: "Tests")
], swiftLanguageModes: [.v5])
''')
    shutil.copy2(ROOT / 'ios/Meow/Models/NotificationBody.swift', package / 'Sources/NotificationBody.swift')
    shutil.copy2(ROOT / 'ios/MeowTests/NotificationBodyTests.swift', package / 'Tests/NotificationBodyTests.swift')
    with (output / 'tests.log').open('w') as log:
        result = subprocess.run(['swift', 'test', '--package-path', str(package)],
                                stdout=log, stderr=subprocess.STDOUT)
    if result.returncode:
        raise RuntimeError('Markdown tests failed: ' + str(output / 'tests.log'))
    print('Markdown parser tests passed: ' + str(output / 'tests.log'))


if __name__ == '__main__':
    main()
