#!/usr/bin/env python3
"""Check real Shared.swift server selection using isolated native Swift storage."""
import argparse
import json
import os
from pathlib import Path
import subprocess
import tempfile
import uuid

ROOT = Path(__file__).resolve().parents[2]
GETTER = 'get { defaults.string(forKey: "server").flatMap(URL.init(string:)) }'


def run(args, **kwargs):
    result = subprocess.run(args, text=True, capture_output=True, **kwargs)
    if result.returncode:
        raise RuntimeError(f"{args[0]} failed ({result.returncode}):\n{result.stdout}\n{result.stderr}")
    return result


def coverage_gate(count):
    if count <= 0:
        raise RuntimeError('saved-server getter was not exercised')


def main():
    original = (ROOT / 'ios/Shared/Shared.swift').read_text()
    with tempfile.TemporaryDirectory(prefix='meow-server-contract-') as tmp:
        folder = Path(tmp)
        shared = folder / 'Shared.swift'
        # Only replace platform configuration, not server-selection behavior.
        text = original.replace('"group.blue.meow.app"', json.dumps('blue.meow.test.' + str(uuid.uuid4())))
        default = 'serverURL(from: Bundle.main.object(forInfoDictionaryKey: "MeowDefaultServer") as? String)'
        assert text.count(default) == 1
        text = text.replace(default, 'URL(string: "https://api.meow.blue")')
        (folder / 'main.swift').write_text('''import Foundation
// Unused external types; no keychain operation or gateway behavior is invoked.
struct GatewayBinding: Codable {}
enum NotificationSoundChoice: String { case meow }
func require(_ condition: Bool, _ message: String) {
    if !condition { print("CONTRACT FAILED: " + message); exit(1) }
}
defer { MeowShared.defaults.removePersistentDomain(forName: MeowShared.appGroup) }
MeowShared.defaults.removePersistentDomain(forName: MeowShared.appGroup)
require(MeowShared.defaultServer != nil, "fixture has an official suggestion")
require(MeowShared.server == nil, "a suggestion must not become a saved server")
for index in 0..<128 {
    let url = URL(string: "https://host-\\(index).example.test:8443/team%20\\(index)")!
    MeowShared.server = url
    require(MeowShared.server == url, "self-hosted address must survive exactly")
    MeowShared.server = nil
    require(MeowShared.server == nil, "clearing the server must not select the official default")
}
print("CONTRACT PASSED: 128 saved-server round trips")
''')
        binary = folder / 'checks'

        def execute(source, name):
            shared.write_text(source)
            run(['swiftc', '-profile-generate', '-profile-coverage-mapping', str(shared),
                 str(folder / 'main.swift'), '-o', str(binary)])
            env = dict(os.environ, LLVM_PROFILE_FILE=str(folder / (name + '.profraw')))
            result = subprocess.run([str(binary)], text=True, capture_output=True, env=env)
            if result.returncode and 'CONTRACT FAILED:' not in result.stdout:
                raise RuntimeError('unexpected runtime failure: ' + result.stderr)
            return result

        baseline = execute(text, 'baseline')
        if baseline.returncode:
            raise RuntimeError(baseline.stdout)
        assert text.count(GETTER) == 1
        line = text[:text.index(GETTER)].count('\n') + 1
        run(['xcrun', 'llvm-profdata', 'merge', '-sparse', str(folder / 'baseline.profraw'), '-o', str(folder / 'baseline.profdata')])
        report = json.loads(run(['xcrun', 'llvm-cov', 'export', str(binary), '-instr-profile=' + str(folder / 'baseline.profdata')]).stdout)
        files = [f for d in report['data'] for f in d['files'] if f['filename'] == str(shared)]
        assert len(files) == 1
        counts = [s[2] for s in files[0]['segments'] if s[0] == line and s[3]]
        assert counts
        coverage_gate(max(counts))
        try:
            coverage_gate(0)
        except RuntimeError:
            pass
        else:
            raise RuntimeError('coverage negative control accepted an unexecuted getter')
        mutants = [
            ('default-fallback', GETTER, GETTER[:-2] + ' ?? defaultServer }'),
            ('discard-saved-server', GETTER, 'get { nil }'),
            ('ignore-save', 'defaults.set(newValue.absoluteString, forKey: "server")', '_ = newValue'),
            ('ignore-clear', 'defaults.removeObject(forKey: "server")', '_ = defaults'),
        ]
        killed = []
        for name, old, new in mutants:
            assert text.count(old) == 1, name
            result = execute(text.replace(old, new), name)
            if result.returncode != 1 or 'CONTRACT FAILED:' not in result.stdout:
                raise RuntimeError('mutant survived: ' + name)
            killed.append(name)
        restored = execute(text, 'restored')
        if restored.returncode:
            raise RuntimeError(restored.stdout)
        assert (ROOT / 'ios/Shared/Shared.swift').read_text() == original
        print(json.dumps({'generated_cases': 128, 'changed_getter_covered': True,
                          'coverage_negative_control': 'rejected', 'mutants_killed': killed,
                          'source_restored': True}, indent=2))


if __name__ == '__main__':
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--full', action='store_true', help='Also run CLI tests, vet, format checks and the iOS suite.')
    parser.add_argument('--output', type=Path, help='New directory for the full iOS result bundle.')
    args = parser.parse_args()
    if args.full and args.output is None:
        parser.error('--full requires --output')
    main()
    if args.full:
        print(run(['go', 'test', '-count=1', './cli/cmd/meow'], cwd=ROOT).stdout, flush=True)
        run(['go', 'vet', './cli/cmd/meow'], cwd=ROOT)
        if run(['gofmt', '-l', 'cli/cmd/meow/wizard.go'], cwd=ROOT).stdout.strip():
            raise RuntimeError('wizard.go is not formatted')
        run(['git', 'diff', '--check'], cwd=ROOT)
        print(run(['python3', 'ios/scripts/verify_ui_optimization.py', '--output', str(args.output.resolve())], cwd=ROOT).stdout)
